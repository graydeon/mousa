// Command beir runs the Mousa lexical retrieval path over a BEIR development
// subset and emits raw per-query results plus aggregate metrics as JSON. It is
// evaluation instrumentation: it never changes the baseline engine, adds no
// dependency, and reports exactly what it measured.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/graydeon/mousa/eval/beir"
)

func main() {
	dataDir := flag.String("data", "", "directory containing corpus.jsonl, queries.jsonl, and qrels/test.tsv (required for a run)")
	dataset := flag.String("dataset", "subset", "dataset name recorded in the report")
	out := flag.String("out", "", "output JSON path (required)")
	reuse := flag.Bool("reuse", false, "reuse an existing indexed store instead of reindexing")
	queryTimeout := flag.Duration("query-timeout", 2*time.Minute, "per-query wall timeout (e.g. 30s, 2m); 0 disables")
	mode := flag.String("mode", string(beir.ModeVerified), "retrieval mode: verified, enforced, or traced")
	limit := flag.Int("limit", 100, "candidate limit per query (1-100)")
	budget := flag.Uint64("budget", 1<<20, "traced-mode pack budget in bytes (e.g. 512, 2048, 8192)")
	policy := flag.String("policy", string(beir.PolicyOriginal), "expression term policy: original (keep repeats), dedup (fold repeats; changes rankings), or drop-floor (drop terms at the BM25 evidence floor; measured floor-weight evaluation)")
	resume := flag.Bool("resume", false, "resume an interrupted run from its journal (manifest must match)")
	queryLimit := flag.Int("queries", 0, "limit the run to the first N judged queries in file order (0 = all; part of the run identity)")
	report := flag.String("report", "", `report mode: "coverage" joins the traced reports named by -reports into the pack byte-budget curve, or "compare" checks two reports' rankings against each other, instead of running queries`)
	reports := flag.String("reports", "", "comma-separated report paths (with -report coverage or compare)")
	flag.Parse()
	switch *report {
	case "":
		if *dataDir == "" || *out == "" {
			flag.Usage()
		}
		config := beir.RunConfig{
			Mode:       beir.SearchMode(*mode),
			Limit:      *limit,
			Budget:     *budget,
			Policy:     beir.ExpressionPolicy(*policy),
			Dataset:    *dataset,
			QueryLimit: *queryLimit,
		}
		if err := run(*dataDir, *out, config, *reuse, *resume, *queryTimeout); err != nil {
			fmt.Fprintf(os.Stderr, "beir: %v\n", err)
			os.Exit(1)
		}
	case "compare":
		if *reports == "" || *out == "" {
			fmt.Fprintln(os.Stderr, `beir: -report compare requires -reports <two comma-separated report paths> and -out`)
			os.Exit(2)
		}
		if err := compareReportTo(*out, strings.Split(*reports, ",")); err != nil {
			fmt.Fprintf(os.Stderr, "beir: %v\n", err)
			os.Exit(1)
		}
	case "coverage":
		if *reports == "" || *out == "" {
			fmt.Fprintln(os.Stderr, `beir: -report coverage requires -reports <comma-separated traced reports> and -out`)
			os.Exit(2)
		}
		if err := coverageReportTo(*out, strings.Split(*reports, ",")); err != nil {
			fmt.Fprintf(os.Stderr, "beir: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "beir: unknown -report mode %q (supported: coverage)\n", *report)
		os.Exit(2)
	}
}

func run(dataDir, out string, config beir.RunConfig, reuse, resume bool, queryTimeout time.Duration) error {
	started := time.Now()
	ctx := context.Background()
	dataset, err := beir.LoadDataset(config.Dataset, dataDir)
	if err != nil {
		return err
	}
	config.Corpus = len(dataset.Corpus)
	config.Judged = len(dataset.JudgedQueries())
	storePath := filepath.Join(filepath.Dir(out), config.Dataset+".sqlite")
	var ingested *beir.IngestedCorpus
	if reuse {
		if _, err := os.Stat(storePath); err != nil {
			return fmt.Errorf("reuse requires an existing store at %s: %w", storePath, err)
		}
	}
	// Journal and manifest live next to the report. The journal is the resume
	// authority: reusing an index is not resuming queries.
	journalPath := out + ".jsonl"
	manifestPath := out + ".manifest.json"
	skip := map[string]beir.SearchResult{}
	if resume {
		completed, err := loadJournal(journalPath)
		if err != nil {
			return fmt.Errorf("resume: %w", err)
		}
		if err := checkManifest(manifestPath, config, queryTimeout); err != nil {
			return fmt.Errorf("resume: %w", err)
		}
		skip = completed
		fmt.Fprintf(os.Stderr, "%s: resuming with %d completed queries\n", config.Dataset, len(skip))
	}
	if !resume {
		if err := writeManifest(manifestPath, config, storePath, queryTimeout); err != nil {
			return err
		}
		// A fresh run replaces any stale journal so completed queries from a
		// different configuration are never mixed in.
		if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	beir.IngestProgress = func(done, total int, elapsed float64) {
		fmt.Fprintf(os.Stderr, "\r%s: ingest %d/%d docs (%.1fs)", config.Dataset, done, total, elapsed)
	}
	if reuse {
		startedOpen := time.Now()
		ingested, err = beir.OpenExisting(ctx, dataset, storePath)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: reusing existing store %s (opened+mapped in %.1fs)\n", config.Dataset, storePath, time.Since(startedOpen).Seconds())
	} else {
		ingested, err = beir.IngestCorpus(ctx, dataset, storePath)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "\n%s: indexed %d docs in %.1fs, %.1f docs/s\n", config.Dataset,
			len(dataset.Corpus), ingested.IndexingSeconds,
			float64(len(dataset.Corpus))/maxSeconds(ingested.IndexingSeconds, 0.001))
	}
	var instrument *beir.ReductionReport
	if config.Policy == beir.PolicyDropFloor {
		beir.SearchInstrumentation = func(report beir.ReductionReport) {
			instrument = &report
		}
	}
	defer ingested.Close()
	beir.SearchProgress = func(done, total int, lastQueryID string) {
		fmt.Fprintf(os.Stderr, "%s: search %d/%d (last %s)\n", config.Dataset, done, total, lastQueryID)
	}
	beir.QueryTimeout = queryTimeout
	journal, err := os.OpenFile(journalPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(journal)
	results, err := beir.SearchAll(ctx, ingested, dataset, config, skip, func(result beir.SearchResult) {
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(os.Stderr, "beir: journal write failed: %v\n", err)
			os.Exit(1)
		}
	})
	closeErr := journal.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}

	judged := dataset.JudgedQueries()
	ndcg := make([]float64, 0, len(results))
	recall := make([]float64, 0, len(results))
	mrr := make([]float64, 0, len(results))
	latencies := make([]int64, 0, len(results))
	reports := make([]beir.QueryReport, 0, len(results))
	var coverageValues []float64
	var usedBytes []uint64
	for _, result := range results {
		relevant := dataset.Qrels[result.QueryID]
		ndcg = append(ndcg, beir.NDCGAt10(result.RankedDocIDs, relevant))
		recall = append(recall, beir.RecallAt100(result.RankedDocIDs, relevant))
		mrr = append(mrr, beir.MRRAt10(result.RankedDocIDs, relevant))
		latencies = append(latencies, result.LatencyMicros)
		entry := beir.QueryReport{
			QueryID: result.QueryID, RankedDocIDs: result.RankedDocIDs,
			LatencyMicros: result.LatencyMicros, AcceptedSegments: result.AcceptedSegments,
			ConsideredSegments: result.ConsideredSegments,
			DroppedTerms:       result.DroppedTerms, ExpressionBytes: result.ExpressionBytes,
		}
		if config.Mode == beir.ModeTraced {
			coverage := beir.GoldCoverage(relevant, result.SelectedDocIDs)
			coverageValues = append(coverageValues, coverage)
			usedBytes = append(usedBytes, result.UsedBytes)
			entry.PackCoverage = &beir.QueryCoverage{
				GoldCoverage: coverage,
				SelectedDocs: len(result.SelectedDocIDs),
				UsedBytes:    result.UsedBytes,
			}
		}
		reports = append(reports, entry)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	peak := peakRSSKB()

	aggregate := beir.AggregateMetrics{
		Queries:           len(results),
		NDCGAt10:          beir.Aggregate(ndcg),
		RecallAt100:       beir.Aggregate(recall),
		MRRAt10:           beir.Aggregate(mrr),
		LatencyP50Micros:  percentile(latencies, 0.50),
		LatencyP90Micros:  percentile(latencies, 0.90),
		LatencyP99Micros:  percentile(latencies, 0.99),
		LatencyMeanMicros: mean(latencies),
	}
	var coverage *beir.PackCoverage
	if config.Mode == beir.ModeTraced {
		meanUsed := uint64(0)
		if len(usedBytes) > 0 {
			var total uint64
			for _, value := range usedBytes {
				total += value
			}
			meanUsed = total / uint64(len(usedBytes))
		}
		utilisation := 0.0
		if config.Budget > 0 {
			utilisation = float64(meanUsed) / float64(config.Budget)
		}
		coverage = &beir.PackCoverage{
			Queries:      len(coverageValues),
			GoldCoverage: beir.Aggregate(coverageValues),
			SelectedDocs: 0,
			UsedBytes:    meanUsed,
			Utilisation:  utilisation,
		}
		aggregate.PackCoverage = coverage
	}
	report := beir.RunReport{
		Dataset: config.Dataset, Mode: string(config.Mode), ExpressionPolicy: string(config.Policy),
		Limit: config.Limit, Budget: config.Budget, RequestNamespace: config.Namespace(),
		CorpusDocuments: len(dataset.Corpus), JudgedQueries: len(judged), QueryLimit: config.QueryLimit,
		IndexBytes: ingested.IndexBytes, IndexingSeconds: ingested.IndexingSeconds,
		PeakRSSKB: peak, StartedAt: started, FinishedAt: time.Now(),
		Reduction: instrument,
		Aggregate: aggregate,
		Queries:   reports,
	}
	if coverage != nil {
		var selectedDocs []int
		for _, entry := range reports {
			if entry.PackCoverage != nil {
				selectedDocs = append(selectedDocs, entry.PackCoverage.SelectedDocs)
			}
		}
		sort.Ints(selectedDocs)
		coverage.SelectedDocs = int(meanInts(selectedDocs))
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	summary := fmt.Sprintf("%s: mode=%s budget=%d docs=%d queries=%d ndcg@10=%.4f recall@100=%.4f mrr@10=%.4f",
		config.Dataset, config.Mode, config.Budget, len(dataset.Corpus), len(results),
		report.Aggregate.NDCGAt10, report.Aggregate.RecallAt100, report.Aggregate.MRRAt10)
	if coverage != nil {
		summary += fmt.Sprintf(" gold-coverage=%.4f used-bytes=%d utilisation=%.1f%%",
			coverage.GoldCoverage, coverage.UsedBytes, 100*coverage.Utilisation)
	}
	fmt.Println(summary)
	return nil
}

// loadJournal reads one JSONL SearchResult per line into a query-ID map.
func loadJournal(path string) (map[string]beir.SearchResult, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return map[string]beir.SearchResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	completed := map[string]beir.SearchResult{}
	decoder := json.NewDecoder(file)
	for {
		var result beir.SearchResult
		if err := decoder.Decode(&result); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("journal %s is unreadable: %w", path, err)
		}
		if _, duplicate := completed[result.QueryID]; duplicate {
			return nil, fmt.Errorf("journal %s records query %s twice", path, result.QueryID)
		}
		completed[result.QueryID] = result
	}
	return completed, nil
}

// journalFormat is the SearchResult line format this harness writes and can
// resume from. It is part of the resume manifest identity because an older
// journal can be missing fields a newer one records (for example the traced
// pack selection), and resuming across formats would silently mix unmeasured
// queries into the report.
const journalFormat = 3

// manifestIdentity is the resume-time binding between a report and the run that
// produced its journal: dataset identity, corpus size, judged-query count, and
// every run parameter. A mismatch means the journal belongs to a different
// experiment and resume is refused instead of mixing results.
type manifestIdentity struct {
	Dataset          string        `json:"dataset"`
	Corpus           int           `json:"corpus_documents"`
	Judged           int           `json:"judged_queries"`
	Mode             string        `json:"mode"`
	Limit            int           `json:"limit"`
	Budget           uint64        `json:"budget_bytes"`
	Policy           string        `json:"expression_policy"`
	QueryTimeout     time.Duration `json:"query_timeout"`
	StorePath        string        `json:"store_path"`
	RequestNamespace string        `json:"request_namespace"`
	JournalFormat    int           `json:"journal_format"`
}

func manifestIdentityFor(config beir.RunConfig, storePath string, queryTimeout time.Duration) manifestIdentity {
	return manifestIdentity{
		Dataset: config.Dataset, Corpus: config.Corpus, Judged: config.Judged,
		Mode: string(config.Mode), Limit: config.Limit, Budget: config.Budget,
		Policy:       string(config.Policy),
		QueryTimeout: queryTimeout, StorePath: storePath,
		RequestNamespace: config.Namespace(),
		JournalFormat:    journalFormat,
	}
}

func writeManifest(path string, config beir.RunConfig, storePath string, queryTimeout time.Duration) error {
	identity := manifestIdentityFor(config, storePath, queryTimeout)
	encoded, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

func checkManifest(path string, config beir.RunConfig, queryTimeout time.Duration) error {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return fmt.Errorf("no manifest at %s: resume requires the manifest written by the interrupted run", path)
	}
	if err != nil {
		return err
	}
	var stored manifestIdentity
	if err := json.Unmarshal(raw, &stored); err != nil {
		return fmt.Errorf("manifest %s is unreadable: %w", path, err)
	}
	// The store path is the manifest's own record: the journal next to this
	// manifest belongs to that store. The query timeout, however, is this
	// invocation's parameter, so resuming with a different bound must refuse.
	current := manifestIdentityFor(config, stored.StorePath, queryTimeout)
	if stored != current {
		return fmt.Errorf("manifest %s describes dataset %q mode %s limit %d budget %d policy %s corpus %d judged %d query-timeout %s journal-format %d; this run is dataset %q mode %s limit %d budget %d policy %s corpus %d judged %d query-timeout %s journal-format %d",
			path,
			stored.Dataset, stored.Mode, stored.Limit, stored.Budget, stored.Policy, stored.Corpus, stored.Judged, stored.QueryTimeout, stored.JournalFormat,
			current.Dataset, current.Mode, current.Limit, current.Budget, current.Policy, current.Corpus, current.Judged, current.QueryTimeout, current.JournalFormat)
	}
	return nil
}

func percentile(sorted []int64, fraction float64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	index := int(fraction * float64(len(sorted)-1))
	return sorted[index]
}

func mean(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	var total int64
	for _, value := range values {
		total += value
	}
	return total / int64(len(values))
}

func peakRSSKB() int64 {
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				var kb int64
				fmt.Sscanf(fields[1], "%d", &kb)
				return kb
			}
		}
	}
	return 0
}

func maxSeconds(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// meanInts is mean() for plain counters such as per-query selected-document
// counts.
func meanInts(values []int) int {
	if len(values) == 0 {
		return 0
	}
	var total int
	for _, value := range values {
		total += value
	}
	return total / len(values)
}

// coverageReport is the pack byte-budget curve artifact: one dataset, one
// protocol, one measurement per budget, joined from the named traced reports.
type coverageReport struct {
	Dataset          string               `json:"dataset"`
	Mode             string               `json:"mode"`
	ExpressionPolicy string               `json:"expression_policy"`
	Limit            int                  `json:"limit"`
	Reports          []string             `json:"reports"`
	Points           []beir.CoveragePoint `json:"points"`
}

func coverageReportTo(out string, paths []string) error {
	reports := make([]beir.RunReport, 0, len(paths))
	for _, path := range paths {
		report, err := beir.LoadRunReport(path)
		if err != nil {
			return err
		}
		reports = append(reports, report)
	}
	points, err := beir.CoverageCurve(reports)
	if err != nil {
		return err
	}
	head := reports[0]
	curve := coverageReport{
		Dataset: head.Dataset, Mode: head.Mode, ExpressionPolicy: head.ExpressionPolicy,
		Limit: head.Limit, Reports: paths, Points: points,
	}
	encoded, err := json.MarshalIndent(curve, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Print(beir.CoverageTable(points))
	return nil
}

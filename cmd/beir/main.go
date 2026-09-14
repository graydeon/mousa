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

type aggregate struct {
	Queries           int     `json:"queries"`
	NDCGAt10          float64 `json:"ndcg_at_10"`
	RecallAt100       float64 `json:"recall_at_100"`
	MRRAt10           float64 `json:"mrr_at_10"`
	LatencyP50Micros  int64   `json:"latency_p50_micros"`
	LatencyP90Micros  int64   `json:"latency_p90_micros"`
	LatencyP99Micros  int64   `json:"latency_p99_micros"`
	LatencyMeanMicros int64   `json:"latency_mean_micros"`
}

type runReport struct {
	Dataset          string        `json:"dataset"`
	Mode             string        `json:"mode"`
	Budget           uint64        `json:"budget_bytes"`
	RequestNamespace string        `json:"request_namespace"`
	Limit            int           `json:"limit"`
	CorpusDocuments  int           `json:"corpus_documents"`
	JudgedQueries    int           `json:"judged_queries"`
	IndexBytes       int64         `json:"index_bytes"`
	IndexingSeconds  float64       `json:"indexing_seconds"`
	PeakRSSKB        int64         `json:"peak_rss_kib"`
	StartedAt        time.Time     `json:"started_at"`
	FinishedAt       time.Time     `json:"finished_at"`
	Aggregate        aggregate     `json:"aggregate"`
	Queries          []queryReport `json:"queries"`
}

type queryReport struct {
	QueryID            string   `json:"query_id"`
	RankedDocIDs       []string `json:"ranked_doc_ids"`
	LatencyMicros      int64    `json:"latency_micros"`
	AcceptedSegments   int      `json:"accepted_segments"`
	ConsideredSegments int      `json:"considered_segments"`
}

func main() {
	dataDir := flag.String("data", "", "directory containing corpus.jsonl, queries.jsonl, and qrels/test.tsv (required)")
	dataset := flag.String("dataset", "subset", "dataset name recorded in the report")
	out := flag.String("out", "", "output JSON path (required)")
	reuse := flag.Bool("reuse", false, "reuse an existing indexed store instead of reindexing")
	queryTimeout := flag.Duration("query-timeout", 2*time.Minute, "per-query wall timeout (e.g. 30s, 2m); 0 disables")
	mode := flag.String("mode", string(beir.ModeVerified), "retrieval mode: verified, enforced, or traced")
	limit := flag.Int("limit", 100, "candidate limit per query (1-100)")
	budget := flag.Uint64("budget", 1<<20, "traced-mode pack budget in bytes (e.g. 512, 2048, 8192)")
	policy := flag.String("policy", string(beir.PolicyOriginal), "expression term policy: original (keep repeats) or dedup (fold repeats; changes rankings)")
	resume := flag.Bool("resume", false, "resume an interrupted run from its journal (manifest must match)")
	flag.Parse()

	if *dataDir == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	config := beir.RunConfig{
		Mode:    beir.SearchMode(*mode),
		Limit:   *limit,
		Budget:  *budget,
		Policy:  beir.ExpressionPolicy(*policy),
		Dataset: *dataset,
	}
	if err := run(*dataDir, *out, config, *reuse, *resume, *queryTimeout); err != nil {
		fmt.Fprintf(os.Stderr, "beir: %v\n", err)
		os.Exit(1)
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
		if err := checkManifest(manifestPath, config, dataset); err != nil {
			return fmt.Errorf("resume: %w", err)
		}
		skip = completed
		fmt.Fprintf(os.Stderr, "%s: resuming with %d completed queries\n", config.Dataset, len(skip))
	}
	if !resume {
		if err := writeManifest(manifestPath, config, dataset, storePath); err != nil {
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
	var ingested *beir.IngestedCorpus
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
	reports := make([]queryReport, 0, len(results))
	for _, result := range results {
		relevant := dataset.Qrels[result.QueryID]
		ndcg = append(ndcg, beir.NDCGAt10(result.RankedDocIDs, relevant))
		recall = append(recall, beir.RecallAt100(result.RankedDocIDs, relevant))
		mrr = append(mrr, beir.MRRAt10(result.RankedDocIDs, relevant))
		latencies = append(latencies, result.LatencyMicros)
		reports = append(reports, queryReport{
			QueryID: result.QueryID, RankedDocIDs: result.RankedDocIDs,
			LatencyMicros: result.LatencyMicros, AcceptedSegments: result.AcceptedSegments,
			ConsideredSegments: result.ConsideredSegments,
		})
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	peak := peakRSSKB()

	report := runReport{
		Dataset: config.Dataset, Mode: string(config.Mode), Limit: config.Limit,
		Budget: config.Budget, RequestNamespace: config.Namespace(),
		CorpusDocuments: len(dataset.Corpus), JudgedQueries: len(judged),
		IndexBytes: ingested.IndexBytes, IndexingSeconds: ingested.IndexingSeconds,
		PeakRSSKB: peak, StartedAt: started, FinishedAt: time.Now(),
		Aggregate: aggregate{
			Queries:           len(results),
			NDCGAt10:          beir.Aggregate(ndcg),
			RecallAt100:       beir.Aggregate(recall),
			MRRAt10:           beir.Aggregate(mrr),
			LatencyP50Micros:  percentile(latencies, 0.50),
			LatencyP90Micros:  percentile(latencies, 0.90),
			LatencyP99Micros:  percentile(latencies, 0.99),
			LatencyMeanMicros: mean(latencies),
		},
		Queries: reports,
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("%s: mode=%s budget=%d docs=%d queries=%d ndcg@10=%.4f recall@100=%.4f mrr@10=%.4f\n",
		config.Dataset, config.Mode, config.Budget, len(dataset.Corpus), len(results),
		report.Aggregate.NDCGAt10, report.Aggregate.RecallAt100, report.Aggregate.MRRAt10)
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
}

func manifestIdentityFor(config beir.RunConfig, dataset *beir.Dataset, storePath string, queryTimeout time.Duration) manifestIdentity {
	return manifestIdentity{
		Dataset: config.Dataset, Corpus: config.Corpus, Judged: config.Judged,
		Mode: string(config.Mode), Limit: config.Limit, Budget: config.Budget,
		Policy:       string(config.Policy),
		QueryTimeout: queryTimeout, StorePath: storePath,
		RequestNamespace: config.Namespace(),
	}
}

func writeManifest(path string, config beir.RunConfig, dataset *beir.Dataset, storePath string) error {
	identity := manifestIdentityFor(config, dataset, storePath, 0)
	encoded, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

func checkManifest(path string, config beir.RunConfig, dataset *beir.Dataset) error {
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
	current := manifestIdentityFor(config, dataset, stored.StorePath, stored.QueryTimeout)
	if stored != current {
		return fmt.Errorf("manifest %s describes %s run for dataset %q (corpus %d, judged %d, mode %s, limit %d, budget %d); this run is dataset %q (corpus %d, judged %d, mode %s, limit %d, budget %d)",
			path, stored.Mode, stored.Dataset, stored.Corpus, stored.Judged, stored.Mode, stored.Limit, stored.Budget,
			current.Dataset, current.Corpus, current.Judged, current.Mode, current.Limit, current.Budget)
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

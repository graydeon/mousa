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
	Dataset         string        `json:"dataset"`
	Mode            string        `json:"mode"`
	Limit           int           `json:"limit"`
	CorpusDocuments int           `json:"corpus_documents"`
	JudgedQueries   int           `json:"judged_queries"`
	IndexBytes      int64         `json:"index_bytes"`
	IndexingSeconds float64       `json:"indexing_seconds"`
	PeakRSSKB       int64         `json:"peak_rss_kib"`
	StartedAt       time.Time     `json:"started_at"`
	FinishedAt      time.Time     `json:"finished_at"`
	Aggregate       aggregate     `json:"aggregate"`
	Queries         []queryReport `json:"queries"`
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
	flag.Parse()

	if *dataDir == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*dataDir, *dataset, beir.SearchMode(*mode), *limit, *out, *reuse, *queryTimeout); err != nil {
		fmt.Fprintf(os.Stderr, "beir: %v\n", err)
		os.Exit(1)
	}
}

func run(dataDir, name string, mode beir.SearchMode, limit int, out string, reuse bool, queryTimeout time.Duration) error {
	started := time.Now()
	ctx := context.Background()
	dataset, err := beir.LoadDataset(name, dataDir)
	if err != nil {
		return err
	}
	storePath := filepath.Join(filepath.Dir(out), name+".sqlite")
	if reuse {
		if _, err := os.Stat(storePath); err != nil {
			return fmt.Errorf("reuse requires an existing store at %s: %w", storePath, err)
		}
	}
	beir.IngestProgress = func(done, total int, elapsed float64) {
		fmt.Fprintf(os.Stderr, "\r%s: ingest %d/%d docs (%.1fs)", name, done, total, elapsed)
	}
	var ingested *beir.IngestedCorpus
	if reuse {
		startedOpen := time.Now()
		ingested, err = beir.OpenExisting(ctx, dataset, storePath)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "%s: reusing existing store %s (opened+mapped in %.1fs)\n", name, storePath, time.Since(startedOpen).Seconds())
	} else {
		ingested, err = beir.IngestCorpus(ctx, dataset, storePath)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "\n%s: indexed %d docs in %.1fs, %.1f docs/s\n", name,
			len(dataset.Corpus), ingested.IndexingSeconds,
			float64(len(dataset.Corpus))/maxSeconds(ingested.IndexingSeconds, 0.001))
	}
	defer ingested.Close()
	beir.SearchProgress = func(done, total int, lastQueryID string) {
		fmt.Fprintf(os.Stderr, "%s: search %d/%d (last %s)\n", name, done, total, lastQueryID)
	}
	beir.QueryTimeout = queryTimeout
	results, err := beir.SearchAll(ctx, ingested, dataset, mode, limit)
	if err != nil {
		return err
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
		Dataset: name, Mode: string(mode), Limit: limit,
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
	fmt.Printf("%s: mode=%s docs=%d queries=%d ndcg@10=%.4f recall@100=%.4f mrr@10=%.4f\n",
		name, mode, len(dataset.Corpus), len(results),
		report.Aggregate.NDCGAt10, report.Aggregate.RecallAt100, report.Aggregate.MRRAt10)
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

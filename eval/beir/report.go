package beir

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// AggregateMetrics is the report-level summary of one harness run: the mean ranking
// metrics over the run's judged queries, the latency distribution, and, for
// traced runs, the mean pack-stage accounting.
type AggregateMetrics struct {
	Queries           int           `json:"queries"`
	NDCGAt10          float64       `json:"ndcg_at_10"`
	RecallAt100       float64       `json:"recall_at_100"`
	MRRAt10           float64       `json:"mrr_at_10"`
	LatencyP50Micros  int64         `json:"latency_p50_micros"`
	LatencyP90Micros  int64         `json:"latency_p90_micros"`
	LatencyP99Micros  int64         `json:"latency_p99_micros"`
	LatencyMeanMicros int64         `json:"latency_mean_micros"`
	PackCoverage      *PackCoverage `json:"pack_coverage,omitempty"`
}

// PackCoverage is the pack-stage accounting of one traced run. It is present
// only for traced runs, because only traced runs carry a packet selection to
// account for. SelectedDocs and UsedBytes are per-query means; Utilisation is
// those mean bytes as a fraction of the pack budget.
type PackCoverage struct {
	Queries      int     `json:"queries"`
	GoldCoverage float64 `json:"gold_coverage"`
	SelectedDocs int     `json:"selected_docs"`
	UsedBytes    uint64  `json:"used_bytes"`
	Utilisation  float64 `json:"utilisation"`
}

// QueryCoverage is one query's pack-stage accounting: how many documents the
// packet selected, how many bytes they cost, and what fraction of the query's
// gold documents they cover.
type QueryCoverage struct {
	GoldCoverage float64 `json:"gold_coverage"`
	SelectedDocs int     `json:"selected_docs"`
	UsedBytes    uint64  `json:"used_bytes"`
}

// QueryReport is one query's raw result: its ranking, its latency, and, for
// traced runs, its pack-stage accounting.
type QueryReport struct {
	QueryID            string         `json:"query_id"`
	RankedDocIDs       []string       `json:"ranked_doc_ids"`
	LatencyMicros      int64          `json:"latency_micros"`
	AcceptedSegments   int            `json:"accepted_segments"`
	ConsideredSegments int            `json:"considered_segments"`
	PackCoverage       *QueryCoverage `json:"pack_coverage,omitempty"`
}

// RunReport is one harness run's complete record: what was run, against which
// corpus, with which protocol, and what it measured.
type RunReport struct {
	Dataset          string           `json:"dataset"`
	Mode             string           `json:"mode"`
	ExpressionPolicy string           `json:"expression_policy"`
	Budget           uint64           `json:"budget_bytes"`
	RequestNamespace string           `json:"request_namespace"`
	Limit            int              `json:"limit"`
	CorpusDocuments  int              `json:"corpus_documents"`
	JudgedQueries    int              `json:"judged_queries"`
	IndexBytes       int64            `json:"index_bytes"`
	IndexingSeconds  float64          `json:"indexing_seconds"`
	PeakRSSKB        int64            `json:"peak_rss_kib"`
	StartedAt        time.Time        `json:"started_at"`
	FinishedAt       time.Time        `json:"finished_at"`
	Aggregate        AggregateMetrics `json:"aggregate"`
	Queries          []QueryReport    `json:"queries"`
}

// LoadRunReport reads one harness report.
func LoadRunReport(path string) (RunReport, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return RunReport{}, err
	}
	var report RunReport
	if err := json.Unmarshal(raw, &report); err != nil {
		return RunReport{}, fmt.Errorf("report %s is unreadable: %w", path, err)
	}
	return report, nil
}

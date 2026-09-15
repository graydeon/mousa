package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/graydeon/mousa/eval/beir"
)

// compareReport is the decision-rule artifact for one pair of harness runs:
// whether the two arms produced the same ranking for every query, and how
// their metrics and latency distributions differ. It is how a
// ranking-identity claim is checked rather than eyeballed.
type compareReport struct {
	Dataset    string         `json:"dataset"`
	Mode       string         `json:"mode"`
	Limit      int            `json:"limit"`
	QueryLimit int            `json:"query_limit"`
	Judged     int            `json:"judged_queries"`
	Baseline   string         `json:"baseline_report"`
	Candidate  string         `json:"candidate_report"`
	Identical  bool           `json:"rankings_identical"`
	Differing  []string       `json:"differing_queries,omitempty"`
	FirstDiff  string         `json:"first_differing_query,omitempty"`
	Comparison metricCompare  `json:"metrics"`
	Latency    latencyCompare `json:"latency"`
	PerQuery   []queryCompare `json:"per_query,omitempty"`
}

// metricCompare carries both arms' aggregate metrics and their deltas
// (candidate minus baseline).
type metricCompare struct {
	Baseline         beir.AggregateMetrics `json:"baseline"`
	Candidate        beir.AggregateMetrics `json:"candidate"`
	NDCGAt10Delta    float64               `json:"ndcg_at_10_delta"`
	RecallAt100Delta float64               `json:"recall_at_100_delta"`
	MRRAt10Delta     float64               `json:"mrr_at_10_delta"`
}

// latencyCompare carries both arms' latency distributions and the candidate's
// p50 relative to the baseline's, which the pre-registered decision rule
// compares against the 0.90 adoption bound.
type latencyCompare struct {
	BaselineP50Micros  int64   `json:"baseline_p50_micros"`
	CandidateP50Micros int64   `json:"candidate_p50_micros"`
	BaselineP99Micros  int64   `json:"baseline_p99_micros"`
	CandidateP99Micros int64   `json:"candidate_p99_micros"`
	P50Ratio           float64 `json:"p50_ratio"`
}

// queryCompare is one query's identity result: whether the two arms returned
// the same ordered document ranking and what each arm's expression cost.
type queryCompare struct {
	QueryID        string  `json:"query_id"`
	Identical      bool    `json:"identical"`
	BaselineBytes  int     `json:"baseline_expression_bytes"`
	CandidateBytes int     `json:"candidate_expression_bytes"`
	DroppedTerms   int     `json:"dropped_terms"`
	LatencyRatio   float64 `json:"latency_ratio"`
}

func compareReportTo(out string, paths []string) error {
	if len(paths) != 2 {
		return fmt.Errorf("compare requires exactly two reports, got %d", len(paths))
	}
	baseline, err := beir.LoadRunReport(paths[0])
	if err != nil {
		return err
	}
	candidate, err := beir.LoadRunReport(paths[1])
	if err != nil {
		return err
	}
	if baseline.Dataset != candidate.Dataset || baseline.Mode != candidate.Mode ||
		baseline.Limit != candidate.Limit || baseline.CorpusDocuments != candidate.CorpusDocuments ||
		baseline.QueryLimit != candidate.QueryLimit {
		return fmt.Errorf("reports are not comparable: dataset %q/%q mode %q/%q limit %d/%d corpus %d/%d slice %d/%d",
			baseline.Dataset, candidate.Dataset, baseline.Mode, candidate.Mode,
			baseline.Limit, candidate.Limit, baseline.CorpusDocuments, candidate.CorpusDocuments,
			baseline.QueryLimit, candidate.QueryLimit)
	}
	if len(baseline.Queries) != len(candidate.Queries) {
		return fmt.Errorf("reports are not comparable: %d vs %d queries", len(baseline.Queries), len(candidate.Queries))
	}

	compare := compareReport{
		Dataset: baseline.Dataset, Mode: baseline.Mode, Limit: baseline.Limit,
		QueryLimit: baseline.QueryLimit, Judged: len(baseline.Queries),
		Baseline: paths[0], Candidate: paths[1],
		Identical: true,
	}
	perQuery := make([]queryCompare, 0, len(baseline.Queries))
	for index, base := range baseline.Queries {
		cand := candidate.Queries[index]
		if base.QueryID != cand.QueryID {
			return fmt.Errorf("reports are not comparable: query %d is %q vs %q", index, base.QueryID, cand.QueryID)
		}
		identical := len(base.RankedDocIDs) == len(cand.RankedDocIDs)
		for i := range base.RankedDocIDs {
			if i >= len(cand.RankedDocIDs) || base.RankedDocIDs[i] != cand.RankedDocIDs[i] {
				identical = false
				break
			}
		}
		if !identical {
			compare.Identical = false
			compare.Differing = append(compare.Differing, base.QueryID)
			if compare.FirstDiff == "" {
				compare.FirstDiff = base.QueryID
			}
		}
		ratio := 0.0
		if base.LatencyMicros > 0 {
			ratio = float64(cand.LatencyMicros) / float64(base.LatencyMicros)
		}
		perQuery = append(perQuery, queryCompare{
			QueryID:        base.QueryID,
			Identical:      identical,
			BaselineBytes:  base.ExpressionBytes,
			CandidateBytes: cand.ExpressionBytes,
			DroppedTerms:   cand.DroppedTerms,
			LatencyRatio:   ratio,
		})
	}
	compare.PerQuery = perQuery
	compare.Comparison = metricCompare{
		Baseline:         baseline.Aggregate,
		Candidate:        candidate.Aggregate,
		NDCGAt10Delta:    candidate.Aggregate.NDCGAt10 - baseline.Aggregate.NDCGAt10,
		RecallAt100Delta: candidate.Aggregate.RecallAt100 - baseline.Aggregate.RecallAt100,
		MRRAt10Delta:     candidate.Aggregate.MRRAt10 - baseline.Aggregate.MRRAt10,
	}
	p50Ratio := 0.0
	if baseline.Aggregate.LatencyP50Micros > 0 {
		p50Ratio = float64(candidate.Aggregate.LatencyP50Micros) / float64(baseline.Aggregate.LatencyP50Micros)
	}
	compare.Latency = latencyCompare{
		BaselineP50Micros:  baseline.Aggregate.LatencyP50Micros,
		CandidateP50Micros: candidate.Aggregate.LatencyP50Micros,
		BaselineP99Micros:  baseline.Aggregate.LatencyP99Micros,
		CandidateP99Micros: candidate.Aggregate.LatencyP99Micros,
		P50Ratio:           p50Ratio,
	}
	if compare.Differing == nil {
		compare.Differing = []string{}
	}
	encoded, err := json.MarshalIndent(compare, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	sort.Strings(compare.Differing)
	fmt.Printf("compare: dataset=%s mode=%s queries=%d rankings_identical=%t differing=%d p50 %d->%d us (ratio %.3f) ndcg@10 delta %+.4f\n",
		compare.Dataset, compare.Mode, compare.Judged, compare.Identical, len(compare.Differing),
		compare.Latency.BaselineP50Micros, compare.Latency.CandidateP50Micros, compare.Latency.P50Ratio,
		compare.Comparison.NDCGAt10Delta)
	return nil
}

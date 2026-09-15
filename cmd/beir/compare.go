package main

import (
	"encoding/json"
	"fmt"
	"github.com/graydeon/mousa/eval/beir"
	"os"
	"reflect"
	"sort"
)

// compareReport is the decision-rule artifact for one pair of harness runs:
// whether the two arms produced the same ranking for every query, and how
// their metrics and latency distributions differ. It is how a
// ranking-identity claim is checked rather than eyeballed.
type compareReport struct {
	Dataset    string               `json:"dataset"`
	Mode       string               `json:"mode"`
	Limit      int                  `json:"limit"`
	QueryLimit int                  `json:"query_limit"`
	Judged     int                  `json:"judged_queries"`
	Baseline   string               `json:"baseline_report"`
	Candidate  string               `json:"candidate_report"`
	Identical  bool                 `json:"rankings_identical"`
	Differing  []string             `json:"differing_queries,omitempty"`
	FirstDiff  string               `json:"first_differing_query,omitempty"`
	Content    beir.ContentIdentity `json:"content"`
	Comparison metricCompare        `json:"metrics"`
	Latency    latencyCompare       `json:"latency"`
	PerQuery   []queryCompare       `json:"per_query,omitempty"`
	Criteria   criterionOutcomes    `json:"criteria"`
}

// metricCompare carries both arms' aggregate metrics and their deltas
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

// criterionOutcome is one pre-registered criterion's explicit result. A
// comparison report records outcomes; it never renders an adoption verdict by
// having been written.
type criterionOutcome struct {
	Criterion string  `json:"criterion"`
	Passed    bool    `json:"passed"`
	Value     float64 `json:"value,omitempty"`
}

// criterionOutcomes carries the pre-registered criteria the comparison can
// evaluate: ranking identity and the p50 latency bound.
type criterionOutcomes struct {
	Identity criterionOutcome `json:"identity"`
	Latency  criterionOutcome `json:"latency"`
}

// compareReportTo compares two saved reports under a declared comparison axis.
//
// Validation is structural before it is comparative: empty or malformed
// reports, incomplete or non-unique query sets, self-inconsistent aggregates,
// and legacy reports without content identity are invalid evidence, not a
// comparison result (the 2026-09-15 review showed empty reports comparing as
// "identical"). A comparison axis declares the one dimension the two arms
// intentionally differ on; every other experiment-defining field must match.
// The written report records criterion outcomes explicitly — a valid
// experiment that fails its criterion is reported as that failure, and the
// command's success at writing the report is never an adoption verdict.
func compareReportTo(out string, paths []string, axis string) error {
	if len(paths) != 2 {
		return fmt.Errorf("compare requires exactly two reports, got %d", len(paths))
	}
	baseline, err := beir.LoadRunReport(paths[0])
	if err != nil {
		return fmt.Errorf("invalid evidence %s: %w", paths[0], err)
	}
	candidate, err := beir.LoadRunReport(paths[1])
	if err != nil {
		return fmt.Errorf("invalid evidence %s: %w", paths[1], err)
	}
	if err := validateComparable(&baseline); err != nil {
		return fmt.Errorf("invalid evidence %s: %w", paths[0], err)
	}
	if err := validateComparable(&candidate); err != nil {
		return fmt.Errorf("invalid evidence %s: %w", paths[1], err)
	}
	if err := requireMatchingFields(baseline, candidate, axis); err != nil {
		return err
	}

	compare := compareReport{
		Dataset: baseline.Dataset, Mode: baseline.Mode, Limit: baseline.Limit,
		QueryLimit: baseline.QueryLimit, Judged: len(baseline.Queries),
		Baseline: paths[0], Candidate: paths[1],
		Content:   baseline.Content,
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
	// Explicit criterion outcomes: the reporter records whether each
	// pre-registered criterion held, so a written report is never mistaken for
	// an adoption verdict. Identity failure is a valid experimental result.
	compare.Criteria = criterionOutcomes{
		Identity: criterionOutcome{
			Criterion: "every ordered ranking identical",
			Passed:    compare.Identical,
		},
		Latency: criterionOutcome{
			Criterion: "p50 latency ratio <= 0.90",
			Passed:    p50Ratio > 0 && p50Ratio <= 0.90,
			Value:     p50Ratio,
		},
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
	fmt.Printf("compare: dataset=%s mode=%s queries=%d rankings_identical=%t differing=%d p50 %d->%d us (ratio %.3f) ndcg@10 delta %+.4f identity-criterion=%t latency-criterion=%t\n",
		compare.Dataset, compare.Mode, compare.Judged, compare.Identical, len(compare.Differing),
		compare.Latency.BaselineP50Micros, compare.Latency.CandidateP50Micros, compare.Latency.P50Ratio,
		compare.Comparison.NDCGAt10Delta,
		compare.Criteria.Identity.Passed, compare.Criteria.Latency.Passed)
	return nil
}

// validateComparable rejects reports that are not valid evidence for any
// comparison: no queries, duplicate query IDs, an aggregate inconsistent with
// its own per-query rows, an invalid ranking, or a report written before
// content identity existed.
func validateComparable(report *beir.RunReport) error {
	if len(report.Queries) == 0 {
		return fmt.Errorf("report has no query results")
	}
	seen := map[string]struct{}{}
	for _, query := range report.Queries {
		if _, duplicate := seen[query.QueryID]; duplicate {
			return fmt.Errorf("query %s appears twice", query.QueryID)
		}
		seen[query.QueryID] = struct{}{}
		if err := beir.ValidateRanking(query.RankedDocIDs); err != nil {
			return err
		}
	}
	if report.Aggregate.Queries != len(report.Queries) {
		return fmt.Errorf("aggregate claims %d queries but the report carries %d", report.Aggregate.Queries, len(report.Queries))
	}
	if report.Protocol.Name == "" {
		return fmt.Errorf("report carries no evaluation protocol identity (legacy report)")
	}
	return nil
}

// requireMatchingFields enforces the declared comparison axis. The axis is the
// one dimension the arms intentionally differ on (expression policy, mode, or
// pack budget); dataset content identity, limit, query slice, corpus size, and
// evaluation protocol must match regardless of axis. An unrecognized axis is
// refused: comparing "whatever happens to match" is how the empty-report
// defect slipped through.
func requireMatchingFields(baseline, candidate beir.RunReport, axis string) error {
	switch axis {
	case "policy", "mode", "budget":
	case "":
		return fmt.Errorf("compare requires an explicitly declared axis (-axis policy|mode|budget); the axis is the one dimension the two arms intentionally differ on")
	default:
		return fmt.Errorf("unknown comparison axis %q (supported: policy, mode, budget)", axis)
	}
	if !reflect.DeepEqual(baseline.Protocol, candidate.Protocol) {
		return fmt.Errorf("reports are not comparable: evaluation protocols differ (baseline %+v / candidate %+v)", baseline.Protocol, candidate.Protocol)
	}
	if baseline.Dataset != candidate.Dataset ||
		baseline.Content != candidate.Content ||
		baseline.CorpusDocuments != candidate.CorpusDocuments ||
		baseline.QueryLimit != candidate.QueryLimit {
		return fmt.Errorf("reports are not comparable on axis %s: dataset, content identity, corpus size, or query slice differs (baseline %+v / candidate %+v)", axis, baseline.Content, candidate.Content)
	}
	switch axis {
	case "policy":
		if baseline.ExpressionPolicy == candidate.ExpressionPolicy {
			return fmt.Errorf("comparison axis is policy but both arms ran %q", baseline.ExpressionPolicy)
		}
		if baseline.Mode != candidate.Mode || baseline.Budget != candidate.Budget {
			return fmt.Errorf("arms differ beyond the declared axis: mode %q/%q budget %d/%d", baseline.Mode, candidate.Mode, baseline.Budget, candidate.Budget)
		}
	case "mode":
		if baseline.Mode == candidate.Mode {
			return fmt.Errorf("comparison axis is mode but both arms ran %q", baseline.Mode)
		}
		if baseline.ExpressionPolicy != candidate.ExpressionPolicy || baseline.Budget != candidate.Budget {
			return fmt.Errorf("arms differ beyond the declared axis: policy %q/%q budget %d/%d", baseline.ExpressionPolicy, candidate.ExpressionPolicy, baseline.Budget, candidate.Budget)
		}
	case "budget":
		if baseline.Budget == candidate.Budget {
			return fmt.Errorf("comparison axis is budget but both arms ran %d", baseline.Budget)
		}
		if baseline.Mode != candidate.Mode || baseline.ExpressionPolicy != candidate.ExpressionPolicy {
			return fmt.Errorf("arms differ beyond the declared axis: mode %q/%q policy %q/%q", baseline.Mode, candidate.Mode, baseline.ExpressionPolicy, candidate.ExpressionPolicy)
		}
	}
	return nil
}

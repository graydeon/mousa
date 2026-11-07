package beir

import (
	"fmt"
	"reflect"
)

// ValidateComparable rejects incomplete report evidence before comparison.
// Legacy reports remain readable, but missing provenance cannot certify a comparison.
func ValidateComparable(report *RunReport) error {
	if len(report.Queries) == 0 {
		return fmt.Errorf("report has no query results")
	}
	seen := make(map[string]struct{}, len(report.Queries))
	for _, query := range report.Queries {
		if query.QueryID == "" {
			return fmt.Errorf("report carries an empty query ID")
		}
		if _, duplicate := seen[query.QueryID]; duplicate {
			return fmt.Errorf("query %s appears twice", query.QueryID)
		}
		seen[query.QueryID] = struct{}{}
		if err := ValidateRanking(query.RankedDocIDs); err != nil {
			return err
		}
	}
	if report.Aggregate.Queries != len(report.Queries) {
		return fmt.Errorf("aggregate claims %d queries but the report carries %d", report.Aggregate.Queries, len(report.Queries))
	}
	expected := report.JudgedQueries
	if report.QueryLimit < 0 {
		return fmt.Errorf("negative query slice %d", report.QueryLimit)
	}
	if report.QueryLimit > 0 {
		expected = min(expected, report.QueryLimit)
	}
	if expected != len(report.Queries) {
		return fmt.Errorf("judged count and query slice require %d queries but the report carries %d", expected, len(report.Queries))
	}
	if report.Protocol.Name == "" {
		return fmt.Errorf("report carries no evaluation protocol identity (legacy report)")
	}
	if report.Content.CorpusSHA256 == "" || report.Content.QueriesSHA256 == "" || report.Content.QrelsSHA256 == "" {
		return fmt.Errorf("report carries incomplete content identity (legacy report); provenance-incomplete evidence cannot be compared")
	}
	if err := ValidateSegmentationIdentity(report.Segmentation); err != nil {
		return fmt.Errorf("report carries invalid segmentation provenance: %w", err)
	}
	if coverage := report.Aggregate.PackCoverage; coverage != nil {
		if coverage.Queries != len(report.Queries) {
			return fmt.Errorf("coverage claims %d queries but the report carries %d", coverage.Queries, len(report.Queries))
		}
		for _, query := range report.Queries {
			if query.PackCoverage == nil {
				return fmt.Errorf("query %s carries no pack coverage", query.QueryID)
			}
		}
	}
	return nil
}

// RequireMatchingFields enforces the declared comparison axis and ordered population.
// Only the named dimension may differ between the two reports.
func RequireMatchingFields(baseline, candidate RunReport, axis string) error {
	switch axis {
	case "policy", "mode", "budget", "segmentation":
	case "":
		return fmt.Errorf("compare requires an explicitly declared axis (-axis policy|mode|budget|segmentation); the axis is the one dimension the two arms intentionally differ on")
	default:
		return fmt.Errorf("unknown comparison axis %q (supported: policy, mode, budget, segmentation)", axis)
	}
	if err := ValidateSegmentationIdentity(baseline.Segmentation); err != nil {
		return fmt.Errorf("invalid baseline segmentation: %w", err)
	}
	if err := ValidateSegmentationIdentity(candidate.Segmentation); err != nil {
		return fmt.Errorf("invalid candidate segmentation: %w", err)
	}
	if axis != "segmentation" && baseline.Segmentation != candidate.Segmentation {
		return fmt.Errorf("arms differ beyond the declared axis: segmentation identities differ")
	}
	if !reflect.DeepEqual(baseline.Protocol, candidate.Protocol) {
		return fmt.Errorf("reports are not comparable: evaluation protocols differ (baseline %+v / candidate %+v)", baseline.Protocol, candidate.Protocol)
	}
	if baseline.Dataset != candidate.Dataset || baseline.Content != candidate.Content || baseline.CorpusDocuments != candidate.CorpusDocuments || baseline.QueryLimit != candidate.QueryLimit || baseline.JudgedQueries != candidate.JudgedQueries || baseline.Limit != candidate.Limit {
		return fmt.Errorf("reports are not comparable on axis %s: dataset, content identity, corpus size, candidate limit, judged count, or query slice differs", axis)
	}
	if len(baseline.Queries) != len(candidate.Queries) {
		return fmt.Errorf("reports are not comparable: query populations differ")
	}
	for i, q := range baseline.Queries {
		if q.QueryID != candidate.Queries[i].QueryID {
			return fmt.Errorf("reports are not comparable: query %d is %q vs %q", i, q.QueryID, candidate.Queries[i].QueryID)
		}
	}
	switch axis {
	case "segmentation":
		if baseline.Segmentation.Policy == candidate.Segmentation.Policy {
			return fmt.Errorf("comparison axis is segmentation but both arms ran %q", baseline.Segmentation.Policy)
		}
		if baseline.Mode != candidate.Mode || baseline.Budget != candidate.Budget || baseline.ExpressionPolicy != candidate.ExpressionPolicy {
			return fmt.Errorf("arms differ beyond the declared segmentation axis: mode, budget, or expression policy differs")
		}
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

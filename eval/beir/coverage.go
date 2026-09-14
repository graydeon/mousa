package beir

import (
	"fmt"
	"sort"
	"strings"
)

// GoldCoverage returns the fraction of one query's relevant documents that the
// packed selection carries: |relevant ∩ selected| / |relevant|.
//
// The selection is the engine's own packet selection read back from the Source
// Trail, so a document counts as covered when at least one of its candidates was
// selected. Documents outside the candidate limit are never selected, which is
// why coverage saturates at Recall@100 as the budget grows. A query with no
// relevant judgments has no coverage to measure and returns 0, matching the
// metric convention in metrics.go.
func GoldCoverage(relevant map[string]int, selectedDocIDs []string) float64 {
	if len(relevant) == 0 {
		return 0
	}
	selected := make(map[string]struct{}, len(selectedDocIDs))
	for _, docID := range selectedDocIDs {
		selected[docID] = struct{}{}
	}
	covered := 0
	for docID := range relevant {
		if _, ok := selected[docID]; ok {
			covered++
		}
	}
	return float64(covered) / float64(len(relevant))
}

// CoveragePoint is one row of a pack byte-budget curve: what one budget bought
// for one dataset and protocol.
type CoveragePoint struct {
	BudgetBytes      uint64  `json:"budget_bytes"`
	GoldCoverage     float64 `json:"gold_coverage"`
	SelectedDocs     int     `json:"selected_docs"`
	UsedBytes        uint64  `json:"used_bytes"`
	Utilisation      float64 `json:"utilisation"`
	RequestNamespace string  `json:"request_namespace"`
}

// CoverageCurve joins the traced reports of one experiment into the pack
// byte-budget curve, ordered by ascending budget. Reports must be comparable:
// same dataset, corpus, judged-query count, candidate limit, and expression
// policy, and each must be a traced run carrying pack coverage. A report that
// cannot be compared is refused rather than averaged into the curve, and two
// reports for the same budget are refused because neither is privileged.
func CoverageCurve(reports []RunReport) ([]CoveragePoint, error) {
	if len(reports) == 0 {
		return nil, fmt.Errorf("coverage curve needs at least one report")
	}
	head := reports[0]
	if head.Mode != string(ModeTraced) || head.Aggregate.PackCoverage == nil {
		return nil, fmt.Errorf("report for dataset %q is a %s run without pack coverage; the curve requires traced runs", head.Dataset, head.Mode)
	}
	points := make([]CoveragePoint, 0, len(reports))
	for _, report := range reports {
		if report.Mode != string(ModeTraced) || report.Aggregate.PackCoverage == nil {
			return nil, fmt.Errorf("report for dataset %q budget %d is a %s run without pack coverage", report.Dataset, report.Budget, report.Mode)
		}
		if report.Dataset != head.Dataset || report.CorpusDocuments != head.CorpusDocuments ||
			report.JudgedQueries != head.JudgedQueries || report.Limit != head.Limit ||
			report.ExpressionPolicy != head.ExpressionPolicy {
			return nil, fmt.Errorf("report for dataset %q budget %d is not comparable with dataset %q budget %d: corpus %d/%d, judged %d/%d, limit %d/%d, policy %q/%q",
				report.Dataset, report.Budget, head.Dataset, head.Budget, report.CorpusDocuments, head.CorpusDocuments,
				report.JudgedQueries, head.JudgedQueries, report.Limit, head.Limit, report.ExpressionPolicy, head.ExpressionPolicy)
		}
		coverage := report.Aggregate.PackCoverage
		points = append(points, CoveragePoint{
			BudgetBytes:      report.Budget,
			GoldCoverage:     coverage.GoldCoverage,
			SelectedDocs:     coverage.SelectedDocs,
			UsedBytes:        coverage.UsedBytes,
			Utilisation:      coverage.Utilisation,
			RequestNamespace: report.RequestNamespace,
		})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].BudgetBytes < points[j].BudgetBytes })
	for index := 1; index < len(points); index++ {
		if points[index].BudgetBytes == points[index-1].BudgetBytes {
			return nil, fmt.Errorf("two reports share budget %d bytes; a curve has one measurement per budget", points[index].BudgetBytes)
		}
	}
	return points, nil
}

// CoverageTable renders a curve as the Markdown table used in docs/RESEARCH.md.
func CoverageTable(points []CoveragePoint) string {
	var table strings.Builder
	table.WriteString("| budget | gold coverage | selected docs | used bytes | utilisation |\n")
	table.WriteString("|---|---|---|---|---|\n")
	for _, point := range points {
		fmt.Fprintf(&table, "| %s | %.3f | %.1f | %.0f | %.1f%% |\n",
			FormatBytes(point.BudgetBytes), point.GoldCoverage, float64(point.SelectedDocs),
			float64(point.UsedBytes), 100*point.Utilisation)
	}
	return table.String()
}

// FormatBytes renders a byte count with the largest unit that divides it
// exactly, so budgets read as the protocol states them (512 B, 2 KiB, 256 KiB)
// without rounding a budget to a number it is not.
func FormatBytes(bytes uint64) string {
	for _, unit := range []struct {
		divisor uint64
		suffix  string
	}{{1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}} {
		if bytes >= unit.divisor && bytes%unit.divisor == 0 {
			return fmt.Sprintf("%d %s", bytes/unit.divisor, unit.suffix)
		}
	}
	return fmt.Sprintf("%d B", bytes)
}

package beir

import (
	"strings"
	"testing"
)

// tracedReport is a minimal traced-mode report with the given mean coverage
// values, the way run() writes it.
func tracedReport(dataset string, budget uint64, limit int, coverage *PackCoverage) RunReport {
	return RunReport{
		Dataset: dataset, Mode: string(ModeTraced), ExpressionPolicy: string(PolicyOriginal),
		Budget: budget, Limit: limit, CorpusDocuments: 100, JudgedQueries: 50,
		Aggregate: AggregateMetrics{Queries: 50, PackCoverage: coverage},
	}
}

// TestGoldCoverageHandComputed pins the coverage definition on values computed
// by hand: |relevant ∩ selected| / |relevant|, with each document counted once
// regardless of how many of its segments were selected.
func TestGoldCoverageHandComputed(t *testing.T) {
	relevant := map[string]int{"a": 1, "b": 1, "c": 2, "d": 3}
	cases := []struct {
		name     string
		selected []string
		want     float64
	}{
		{"empty selection covers nothing", nil, 0},
		{"one of four relevant", []string{"x", "a"}, 0.25},
		{"two of four relevant", []string{"a", "b"}, 0.5},
		{"duplicates count once", []string{"a", "a", "b"}, 0.5},
		{"unranked gold documents only dilute", []string{"a", "b", "x", "y"}, 0.5},
		{"all gold covered", []string{"a", "b", "c", "d"}, 1.0},
	}
	for _, test := range cases {
		if got := GoldCoverage(relevant, test.selected); got != test.want {
			t.Fatalf("%s: GoldCoverage = %v, want %v", test.name, got, test.want)
		}
	}
	if got := GoldCoverage(map[string]int{}, []string{"a"}); got != 0 {
		t.Fatalf("no relevant judgments: GoldCoverage = %v, want 0", got)
	}
}

// TestCoverageCurveJoinsBudgets pins the curve join: points ordered by
// ascending budget, and the means carried through from the reports.
func TestCoverageCurveJoinsBudgets(t *testing.T) {
	reports := []RunReport{
		tracedReport("scifact", 8192, 100, &PackCoverage{Queries: 300, GoldCoverage: 0.732, SelectedDocs: 72, UsedBytes: 7000, Utilisation: 0.85}),
		tracedReport("scifact", 512, 100, &PackCoverage{Queries: 300, GoldCoverage: 0.010, SelectedDocs: 1, UsedBytes: 400, Utilisation: 0.78}),
		tracedReport("scifact", 2048, 100, &PackCoverage{Queries: 300, GoldCoverage: 0.423, SelectedDocs: 20, UsedBytes: 1800, Utilisation: 0.88}),
	}
	points, err := CoverageCurve(reports)
	if err != nil {
		t.Fatalf("CoverageCurve: %v", err)
	}
	if len(points) != 3 {
		t.Fatalf("curve has %d points, want 3", len(points))
	}
	for i, want := range []uint64{512, 2048, 8192} {
		if points[i].BudgetBytes != want {
			t.Fatalf("point %d budget = %d, want %d", i, points[i].BudgetBytes, want)
		}
	}
	if points[0].GoldCoverage != 0.010 || points[0].UsedBytes != 400 || points[0].SelectedDocs != 1 {
		t.Fatalf("512 B point carried wrong means: %+v", points[0])
	}
}

// TestCoverageCurveRefusesIncomparableReports pins that the curve is refused
// rather than silently mixing unlike runs.
func TestCoverageCurveRefusesIncomparableReports(t *testing.T) {
	base := tracedReport("scifact", 512, 100, &PackCoverage{Queries: 300, GoldCoverage: 0.01})
	other := tracedReport("scifact", 2048, 100, &PackCoverage{Queries: 300, GoldCoverage: 0.42})
	differentDataset := other
	differentDataset.Dataset = "nfcorpus"
	differentPolicy := other
	differentPolicy.ExpressionPolicy = string(PolicyDedup)
	differentLimit := other
	differentLimit.Limit = 10
	notTraced := other
	notTraced.Mode = string(ModeVerified)
	notTraced.Aggregate.PackCoverage = nil
	noCoverage := other
	noCoverage.Mode = string(ModeTraced)
	noCoverage.Aggregate.PackCoverage = nil
	sameBudget := tracedReport("scifact", 512, 100, &PackCoverage{Queries: 300, GoldCoverage: 0.011})
	for _, test := range []struct {
		name    string
		reports []RunReport
	}{
		{"different dataset", []RunReport{base, differentDataset}},
		{"different policy", []RunReport{base, differentPolicy}},
		{"different limit", []RunReport{base, differentLimit}},
		{"non-traced report", []RunReport{base, notTraced}},
		{"traced report without coverage", []RunReport{base, noCoverage}},
		{"two reports for one budget", []RunReport{base, sameBudget}},
		{"no reports at all", nil},
	} {
		if _, err := CoverageCurve(test.reports); err == nil {
			t.Fatalf("%s: CoverageCurve accepted incomparable reports", test.name)
		}
	}
}

// TestCoverageTableRenders pins the rendered Markdown units: budgets read as
// the protocol states them and utilisation is a percentage.
func TestCoverageTableRenders(t *testing.T) {
	points := []CoveragePoint{
		{BudgetBytes: 512, GoldCoverage: 0.0104, SelectedDocs: 1, UsedBytes: 397, Utilisation: 0.775},
		{BudgetBytes: 2048, GoldCoverage: 0.423, SelectedDocs: 21, UsedBytes: 1821, Utilisation: 0.889},
		{BudgetBytes: 262144, GoldCoverage: 0.886, SelectedDocs: 88, UsedBytes: 233000, Utilisation: 0.889},
	}
	table := CoverageTable(points)
	for _, want := range []string{
		"| budget | gold coverage | selected docs | used bytes | utilisation |",
		"| 512 B | 0.010 | 1.0 | 397 | 77.5% |",
		"| 2 KiB | 0.423 | 21.0 | 1821 | 88.9% |",
		"| 256 KiB | 0.886 | 88.0 | 233000 | 88.9% |",
	} {
		if !strings.Contains(table, want) {
			t.Fatalf("table missing %q:\n%s", want, table)
		}
	}
	if !strings.HasPrefix(table, "| budget |") {
		t.Fatalf("table does not start with its header:\n%s", table)
	}
}

// TestFormatBytes pins unit selection: only exact multiples are rewritten, so a
// budget is never relabelled to a number it is not.
func TestFormatBytes(t *testing.T) {
	cases := map[uint64]string{
		512: "512 B", 1024: "1 KiB", 2048: "2 KiB", 1536: "1536 B",
		32768: "32 KiB", 262144: "256 KiB", 1500: "1500 B",
	}
	for bytes, want := range cases {
		if got := FormatBytes(bytes); got != want {
			t.Fatalf("FormatBytes(%d) = %q, want %q", bytes, got, want)
		}
	}
}

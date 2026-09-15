package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/graydeon/mousa/eval/beir"
)

// TestCheckManifestRefusesOtherFormatAndTimeout pins the resume refusal: a
// manifest written by this format resumes, while a journal from an older format
// (which can be missing fields such as the traced pack selection) and a run
// with a different query-timeout bound are both refused instead of silently
// mixing results.
func TestCheckManifestRefusesOtherFormatAndTimeout(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "out.json.manifest.json")
	config := beir.RunConfig{
		Mode: beir.ModeTraced, Limit: 100, Budget: 2048,
		Policy: beir.PolicyOriginal, Dataset: "scifact", Corpus: 100, Judged: 50,
	}
	timeout := 2 * time.Minute
	if err := writeManifest(manifest, config, filepath.Join(dir, "scifact.sqlite"), timeout); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	if err := checkManifest(manifest, config, timeout); err != nil {
		t.Fatalf("identical run refused: %v", err)
	}
	if err := checkManifest(manifest, config, time.Minute); err == nil {
		t.Fatal("resume with a different query timeout accepted")
	}

	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "journal_format")
	legacy, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	legacyManifest := filepath.Join(dir, "legacy.manifest.json")
	if err := os.WriteFile(legacyManifest, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkManifest(legacyManifest, config, timeout); err == nil {
		t.Fatal("resume from an older journal format accepted")
	}
}

// validComparableReport is a minimal report the compare validator accepts.
func validComparableReport(dataset, mode, policy string, budget uint64) beir.RunReport {
	return beir.RunReport{
		Dataset: dataset, Mode: mode, ExpressionPolicy: policy, Budget: budget,
		Limit: 100, CorpusDocuments: 2, JudgedQueries: 1,
		Content: beir.ContentIdentity{CorpusSHA256: "c", QueriesSHA256: "q", QrelsSHA256: "r"},
		Protocol: beir.EvaluationProtocol{
			Name: beir.ReferenceProtocol, BEIRRevision: beir.BEIRRevision,
			TrecEvalRevision: beir.TrecEvalRevision, IgnoreIdenticalIDs: true,
		},
		Aggregate: beir.AggregateMetrics{Queries: 1, NDCGAt10: 1, RecallAt100: 1, MRRAt10: 1},
		Queries: []beir.QueryReport{{
			QueryID: "q1", RankedDocIDs: []string{"d1"},
			LatencyMicros: 100, AcceptedSegments: 1, ConsideredSegments: 1,
		}},
	}
}

func writeReport(t *testing.T, path string, report beir.RunReport) {
	t.Helper()
	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCompareRequiresAxisAndValidEvidence converts the review's compare probes
// into correct-behavior requirements: empty reports are invalid evidence, an
// undeclared axis is refused, a differing budget without the budget axis is
// refused, and a valid experiment failing the identity criterion is recorded as
// that failure rather than an error or a pass.
func TestCompareRequiresAxisAndValidEvidence(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, report beir.RunReport) string {
		path := filepath.Join(dir, name)
		writeReport(t, path, report)
		return path
	}
	out := filepath.Join(dir, "out.json")

	// Empty reports are invalid evidence, not an identical comparison.
	emptyA := filepath.Join(dir, "emptyA.json")
	emptyB := filepath.Join(dir, "emptyB.json")
	os.WriteFile(emptyA, []byte("{}"), 0o600)
	os.WriteFile(emptyB, []byte("{}"), 0o600)
	if err := compareReportTo(out, []string{emptyA, emptyB}, "policy"); err == nil {
		t.Fatal("empty reports were accepted as valid evidence")
	}

	// Undeclared axis refused.
	a := write("a.json", validComparableReport("x", "verified", "original", 0))
	b := write("b.json", validComparableReport("x", "verified", "dedup", 0))
	if err := compareReportTo(out, []string{a, b}, ""); err == nil {
		t.Fatal("comparison without a declared axis was accepted")
	}

	// Mismatched budgets with the budget axis unselected are refused.
	budgeted := write("budgeted.json", validComparableReport("x", "traced", "original", 8192))
	if err := compareReportTo(out, []string{a, budgeted}, "policy"); err == nil {
		t.Fatal("unequal budgets accepted without the budget axis")
	}

	// A valid policy comparison that fails the identity criterion writes a
	// report whose criteria record the failure.
	c := validComparableReport("x", "verified", "dedup", 0)
	c.Queries[0].RankedDocIDs = []string{"d2", "d1"}
	cPath := write("c2.json", c)
	if err := compareReportTo(out, []string{a, cPath}, "policy"); err != nil {
		t.Fatalf("valid non-identity comparison errored: %v", err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var result compareReport
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Criteria.Identity.Passed {
		t.Fatal("identity criterion recorded as passed despite differing rankings")
	}

	// A valid identity comparison passes the identity criterion.
	same := write("same.json", validComparableReport("x", "verified", "dedup", 0))
	if err := compareReportTo(out, []string{a, same}, "policy"); err != nil {
		t.Fatalf("valid identity comparison errored: %v", err)
	}
	raw, err = os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Criteria.Identity.Passed {
		t.Fatal("identity criterion recorded as failed for identical rankings")
	}
}

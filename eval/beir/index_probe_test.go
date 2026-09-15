package beir

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ingestFixture writes a tiny BEIR dataset and ingests it through the engine,
// so the probe tests run against a real store with the real tokenizer and the
// real bm25 implementation the policy depends on.
func ingestFixture(t *testing.T, corpus map[string]string, queries map[string]string, qrels map[string]map[string]int) (string, *IngestedCorpus) {
	t.Helper()
	dir := t.TempDir()
	writeJSONL := func(name string, rows []string) {
		t.Helper()
		path := filepath.Join(dir, name)
		var content string
		for _, row := range rows {
			content += row + "\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	corpusRows := make([]string, 0, len(corpus))
	for id, text := range corpus {
		corpusRows = append(corpusRows, fmt.Sprintf(`{"_id":%q,"title":"","text":%q}`, id, text))
	}
	writeJSONL("corpus.jsonl", corpusRows)
	queryRows := make([]string, 0, len(queries))
	for id, text := range queries {
		queryRows = append(queryRows, fmt.Sprintf(`{"_id":%q,"text":%q}`, id, text))
	}
	writeJSONL("queries.jsonl", queryRows)
	qrelRows := make([]string, 0)
	for queryID, docs := range qrels {
		for docID, score := range docs {
			qrelRows = append(qrelRows, fmt.Sprintf("%s\t0\t%s\t%d", queryID, docID, score))
		}
	}
	qrelsDir := filepath.Join(dir, "qrels")
	if err := os.MkdirAll(qrelsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(qrelsDir, "test.tsv"), []byte("query-id\titeration\tcorpus-id\trelevance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if len(qrelRows) > 0 {
		file, err := os.OpenFile(filepath.Join(qrelsDir, "test.tsv"), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range qrelRows {
			fmt.Fprintln(file, row)
		}
		file.Close()
	}
	dataset, err := LoadDataset("fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "fixture.sqlite")
	ingested, err := IngestCorpus(context.Background(), dataset, storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ingested.Close() })
	return dir, ingested
}

// TestIndexProbeMeasuresEvidence pins the two measurements the floor policy
// the engine assigns. Magnitudes are checked against the clamp behaviour the
// policy relies on: a term present in most rows must sit at the floor, a rare
// term must carry orders of magnitude more evidence, and an absent term must
// report zero postings.
func TestIndexProbeMeasuresEvidence(t *testing.T) {
	corpus := map[string]string{
		"d1": "the cat sat quietly",
		"d2": "the dog barked",
		"d3": "the bird flew",
		"d4": "zebra stripes",
	}
	queries := map[string]string{"q1": "zebra"}
	qrels := map[string]map[string]int{"q1": {"d4": 1}}
	_, ingested := ingestFixture(t, corpus, queries, qrels)

	ctx := context.Background()
	probe, err := OpenIndexProbe(ctx, ingested.Path)
	if err != nil {
		t.Fatalf("OpenIndexProbe: %v", err)
	}
	defer probe.Close()

	if want := 4; probe.Rows() != want {
		t.Fatalf("probe rows = %d, want %d", probe.Rows(), want)
	}

	// "the" appears in three of four rows (df 3 >= rows/2 = 2), so the floor
	// premise requires its strongest magnitude to be at most the floor limit.
	floor, err := probe.TermEvidence(ctx, "the")
	if err != nil {
		t.Fatalf("probe the: %v", err)
	}
	if floor.DocumentFrequency != 3 {
		t.Fatalf("df(the) = %d, want 3", floor.DocumentFrequency)
	}
	if floor.MaxMagnitude > floorMagnitudeLimit {
		t.Fatalf("df-majority term carries magnitude %g > floor %g: this SQLite build does not clamp bm25 as the policy requires",
			floor.MaxMagnitude, floorMagnitudeLimit)
	}

	// "zebra" appears in one row and must carry real evidence, well above the
	// floor limit; otherwise the policy would have nothing to keep.
	rare, err := probe.TermEvidence(ctx, "zebra")
	if err != nil {
		t.Fatalf("probe zebra: %v", err)
	}
	if rare.DocumentFrequency != 1 {
		t.Fatalf("df(zebra) = %d, want 1", rare.DocumentFrequency)
	}
	if rare.MaxMagnitude <= floorMagnitudeLimit {
		t.Fatalf("rare term magnitude %g <= floor %g: the floor boundary does not separate evidence",
			rare.MaxMagnitude, floorMagnitudeLimit)
	}

	// An absent term has no postings: zero frequency and zero magnitude.
	absent, err := probe.TermEvidence(ctx, "unicorn")
	if err != nil {
		t.Fatalf("probe unicorn: %v", err)
	}
	if absent.DocumentFrequency != 0 || absent.MaxMagnitude != 0 {
		t.Fatalf("absent term evidence = %+v, want zero frequency and magnitude", absent)
	}

	// Caching: a second measurement must not run another probe.
	if _, err := probe.TermEvidence(ctx, "the"); err != nil {
		t.Fatalf("cached probe: %v", err)
	}
	count, _ := probe.Probes()
	if count != 3 {
		t.Fatalf("probe count = %d, want 3 distinct terms measured", count)
	}
}

// TestVerifyFloorPremiseDetectsUnclamped pins the fail-closed guard: a probe
// oracle that does not clamp bm25 at the floor must make VerifyFloorPremise
// refuse the policy, because dropping df-majority terms would then change
// rankings.

// fakePremiseSource answers TermEvidence from a hand-computed table so the
// premise guard is tested without an index, including the refusal cases.
type fakePremiseSource struct {
	rows     int
	evidence map[string]TermEvidence
}

func (fake fakePremiseSource) Rows() int { return fake.rows }

func (fake fakePremiseSource) TermEvidence(_ context.Context, term string) (TermEvidence, error) {
	evidence, ok := fake.evidence[term]
	if !ok {
		return TermEvidence{}, fmt.Errorf("no evidence for %q", term)
	}
	return evidence, nil
}

func TestVerifyFloorPremiseDetectsUnclamped(t *testing.T) {
	ctx := context.Background()
	clamped := fakePremiseSource{rows: 1000, evidence: map[string]TermEvidence{
		"the":  {MaxMagnitude: 1.9e-6, DocumentFrequency: 900},
		"rare": {MaxMagnitude: 1.4, DocumentFrequency: 7},
	}}
	if err := VerifyFloorPremise(ctx, clamped, []string{"the", "rare"}); err != nil {
		t.Fatalf("clamped premise refused: %v", err)
	}

	// The same term measured with a non-clamped magnitude: the guard must
	// refuse, because the policy's semantics bound would be false.
	unclamped := fakePremiseSource{rows: 1000, evidence: map[string]TermEvidence{
		"the":  {MaxMagnitude: 1.9, DocumentFrequency: 900},
		"rare": {MaxMagnitude: 1.4, DocumentFrequency: 7},
	}}
	if err := VerifyFloorPremise(ctx, unclamped, []string{"the", "rare"}); err == nil {
		t.Fatal("unclamped premise accepted")
	}

	// A sample with no boundary on one side cannot verify the premise at all.
	oneSided := fakePremiseSource{rows: 1000, evidence: map[string]TermEvidence{
		"rare": {MaxMagnitude: 1.4, DocumentFrequency: 7},
	}}
	if err := VerifyFloorPremise(ctx, oneSided, []string{"rare"}); err == nil {
		t.Fatal("one-sided sample accepted")
	}
}

// TestReviewGuardRejectsBelowFloorOutsideTerm converts the review's guard probe
// into the correct-behavior requirement: an outside-boundary term whose
// magnitude sits at or below the floor must fail the premise, so the run is
// refused instead of silently changing rankings.
func TestReviewGuardRejectsBelowFloorOutsideTerm(t *testing.T) {
	source := fakePremiseSource{rows: 1000, evidence: map[string]TermEvidence{
		"inside":   {DocumentFrequency: 900, MaxMagnitude: 1e-6},
		"boundary": {DocumentFrequency: 499, MaxMagnitude: 0.1},
		"weak":     {DocumentFrequency: 20, MaxMagnitude: 1e-7},
		"strong":   {DocumentFrequency: 5, MaxMagnitude: 10},
	}}
	err := VerifyFloorPremise(context.Background(), source, []string{"inside", "boundary", "weak", "strong"})
	if err == nil {
		t.Fatal("guard accepted a below-floor outside term; the premise must fail closed")
	}
	if !strings.Contains(err.Error(), "weak") {
		t.Fatalf("guard error should name the violating term, got %v", err)
	}
}

// TestGuardAcceptsSeparatingBoundary pins the positive case: the strongest
// floor term at the limit and the weakest non-floor term just above it pass.
func TestGuardAcceptsSeparatingBoundary(t *testing.T) {
	source := fakePremiseSource{rows: 1000, evidence: map[string]TermEvidence{
		"floorTerm": {DocumentFrequency: 900, MaxMagnitude: 2e-6},
		"realTerm":  {DocumentFrequency: 5, MaxMagnitude: 1e-3},
	}}
	if err := VerifyFloorPremise(context.Background(), source, []string{"floorTerm", "realTerm"}); err != nil {
		t.Fatalf("separating boundary refused: %v", err)
	}
}

// TestGuardRefusesUnclampedBuild pins the failure the premise exists to catch:
// a build without the IDF clamp lets a floor term carry real evidence.
func TestGuardRefusesUnclampedBuild(t *testing.T) {
	source := fakePremiseSource{rows: 1000, evidence: map[string]TermEvidence{
		"floorTerm": {DocumentFrequency: 900, MaxMagnitude: 0.5},
		"realTerm":  {DocumentFrequency: 5, MaxMagnitude: 10},
	}}
	if err := VerifyFloorPremise(context.Background(), source, []string{"floorTerm", "realTerm"}); err == nil {
		t.Fatal("unclamped floor term accepted; premise must refuse the policy")
	}
}

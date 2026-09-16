package beir

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

// fakeOracle answers TermEvidence from a hand-computed table, so the reducer's
// classification is tested against known magnitudes without an index.
type fakeOracle struct {
	evidence map[string]TermEvidence
}

func (fake fakeOracle) TermEvidence(term string) (TermEvidence, error) {
	return fake.evidence[term], nil
}

// TestReduceFloorTermsDropsOnlyFloorEvidence pins the policy on hand-computed
// values: terms at or below the floor limit are dropped wherever they occur,
// evidence-bearing terms survive in order with duplicates intact, and the
// reduction is a subset of the capped baseline expression.
func TestReduceFloorTermsDropsOnlyFloorEvidence(t *testing.T) {
	oracle := fakeOracle{evidence: map[string]TermEvidence{
		"alpha":   {MaxMagnitude: 1.9e-6, DocumentFrequency: 500},
		"beta":    {MaxMagnitude: 2.31, DocumentFrequency: 12},
		"gamma":   {MaxMagnitude: 0.0, DocumentFrequency: 0},
		"delta":   {MaxMagnitude: 4.2e-6, DocumentFrequency: 480}, // just above the floor limit
		"epsilon": {MaxMagnitude: 1.2, DocumentFrequency: 30},
	}}
	// "alpha" repeats: both instances must go. "gamma" has no postings (0
	// magnitude): the zero-posting evaluation zero-posting class is a special case of the
	// floor. "delta" sits just above the limit and must survive.
	reduction, err := ReduceFloorTerms("alpha beta gamma ALPHA alpha delta", oracle)
	if err != nil {
		t.Fatalf("ReduceFloorTerms: %v", err)
	}
	want := `"beta" OR "delta"`
	if reduction.Expression != want {
		t.Fatalf("reduced expression = %q, want %q", reduction.Expression, want)
	}
	if got, want := strings.Join(reduction.Kept, " "), `"beta" "delta"`; got != want {
		t.Fatalf("kept = %q, want %q", got, want)
	}
	if got, want := strings.Join(reduction.Dropped, " "), `alpha gamma alpha alpha`; got != want {
		t.Fatalf("dropped = %q, want %q", got, want)
	}
	if got, want := reduction.Baseline, `"alpha" OR "beta" OR "gamma" OR "alpha" OR "alpha" OR "delta"`; got != want {
		t.Fatalf("baseline = %q, want %q", got, want)
	}
	// Containment: every kept term must appear in the baseline expression, in
	// the baseline's order.
	lastIndex := -1
	for _, kept := range reduction.Kept {
		found := -1
		for i, quoted := range strings.Split(reduction.Baseline, " OR ") {
			if quoted == kept && i > lastIndex {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("kept term %s not found after index %d in baseline %q", kept, lastIndex, reduction.Baseline)
		}
		lastIndex = found
	}
}

// TestReduceFloorTermsRefusesAllFloor pins the fail-safe: a query whose every
// term is at the floor has no evidence to rank by, so the reduction is
// refused instead of silently emitting an expression the store would reject.
func TestReduceFloorTermsRefusesAllFloor(t *testing.T) {
	oracle := fakeOracle{evidence: map[string]TermEvidence{
		"the": {MaxMagnitude: 1.2e-6, DocumentFrequency: 900},
		"of":  {MaxMagnitude: 0, DocumentFrequency: 0},
	}}
	if _, err := ReduceFloorTerms("the of THE", oracle); err == nil {
		t.Fatal("all-floor reduction accepted")
	}
}

// TestReduceFloorTermsProbeErrorPropagates pins that a probe failure stops the
// run instead of degrading into an unreduced or wrongly reduced expression.
func TestReduceFloorTermsProbeErrorPropagates(t *testing.T) {
	if _, err := ReduceFloorTerms("alpha beta", erroringOracle{}); err == nil {
		t.Fatal("probe error swallowed")
	}
}

type erroringOracle struct{}

func (erroringOracle) TermEvidence(term string) (TermEvidence, error) {
	return TermEvidence{}, errProbeFailed
}

// TestQueryTermsPreservePublishedSplitting checks the query preparation rule,
// not the index's tokenization or diacritic folding.
func TestQueryTermsPreservePublishedSplitting(t *testing.T) {
	got := mousa.LexicalQueryTerms("Hello, WORLD! naïve 42 x-ray")
	want := []string{"hello", "world", "naïve", "42", "x", "ray"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("queryTerms = %q, want %q", got, want)
	}
}

// The cap counts UTF-8 bytes, quotes, and separators without truncating a term.
func TestCappedTermsDropsTrailing(t *testing.T) {
	prefix := strings.Repeat("é", 2042)
	terms, err := mousa.PrepareLexicalTerms(prefix+" one two", false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`"` + prefix + `"`, `"one"`}
	if !slices.Equal(terms, want) {
		t.Fatalf("capped terms = %q, want %q", terms, want)
	}
}

var errProbeFailed = fmt.Errorf("probe failed")

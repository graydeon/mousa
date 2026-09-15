package beir

import (
	"strings"
	"testing"
)

// These fixtures pin the reference evaluation protocol's differential behavior.
// The fixtures cover graded relevance, empty results, and invalid identifiers
// to distinguish graded scoring from the previous binary implementation.

// TestRecallAt100ZeroJudgmentDenominator: a zero-relevance judgment is not a
// relevant document. Retrieving the sole relevant document out of {positive,
// zero} must score 1, not 0.5.
func TestRecallAt100ZeroJudgmentDenominator(t *testing.T) {
	relevant := map[string]int{"yes": 1, "no": 0}
	if got := RecallAt100([]string{"yes"}, relevant); got != 1.0 {
		t.Fatalf("RecallAt100 = %v, want 1 (zero judgments are not relevant documents)", got)
	}
	if got := RecallAt100(nil, relevant); got != 0 {
		t.Fatalf("RecallAt100(nil) = %v, want 0", got)
	}
}

// TestNDCGAt10GradedGain: gains are the relevance values themselves, so an
// unequal-grades ranking depends on order. The previous binary implementation
// scored both orders 1.0.
func TestNDCGAt10GradedGain(t *testing.T) {
	relevant := map[string]int{"low": 1, "high": 2}
	best := NDCGAt10([]string{"high", "low"}, relevant)
	worst := NDCGAt10([]string{"low", "high"}, relevant)
	if !approx(best, 1.0) {
		t.Fatalf("NDCGAt10(high first) = %v, want 1", best)
	}
	// (1*1 + 2/log2(3)) / (2*1 + 1/log2(3))
	ideal := 2.0 + 1.0/log2(3)
	want := (1.0 + 2.0/log2(3)) / ideal
	if !approx(worst, want) {
		t.Fatalf("NDCGAt10(low first) = %v, want %v", worst, want)
	}
	if worst >= best {
		t.Fatalf("reversed graded order must not outscore the ideal order: %v >= %v", worst, best)
	}
}

// TestBinaryNDCGAt10IdealIgnoresZeroJudgments: the binary form keeps its own
// semantics (gain 1) but must not count zero-relevance entries in the ideal
// denominator either.
func TestBinaryNDCGAt10IdealIgnoresZeroJudgments(t *testing.T) {
	relevant := map[string]int{"a": 1, "z": 0}
	if got := BinaryNDCGAt10([]string{"a"}, relevant); !approx(got, 1.0) {
		t.Fatalf("BinaryNDCGAt10 = %v, want 1", got)
	}
	if got := NDCGAt10([]string{"a"}, relevant); !approx(got, 1.0) {
		t.Fatalf("graded NDCGAt10 = %v, want 1", got)
	}
}

func TestScoreQueryIgnoresIdenticalIDs(t *testing.T) {
	// The query ID is also a corpus ID (the ArguAna shape): the query document
	// itself is judged relevant and so is another document.
	relevant := map[string]int{"a": 1, "b": 1}
	score := ScoreQuery("a", []string{"a", "b"}, relevant, true)
	// After removing "a", the one remaining relevant document is at rank 1
	// against an ideal of two relevant documents.
	want := 1.0 / (1.0 + 1.0/log2(3))
	if !approx(score.NDCGAt10, want) || !approx(score.RecallAt100, 0.5) {
		t.Fatalf("after self-ID removal: ndcg=%v recall=%v, want %v/%v", score.NDCGAt10, score.RecallAt100, want, 0.5)
	}
	if score.IdenticalIDsRemoved != 1 {
		t.Fatalf("IdenticalIDsRemoved = %d, want 1", score.IdenticalIDsRemoved)
	}
	// With the rule disabled the query document consumes rank 1 with gain 1.
	kept := ScoreQuery("a", []string{"a", "b"}, relevant, false)
	ideal := 1.0 + 1.0/log2(3)
	if !approx(kept.NDCGAt10, ideal/ideal) {
		t.Fatalf("with the rule disabled the query document occupies rank 1: %+v", kept)
	}
}

// TestValidateRankingRejectsDuplicates: a repeated document ID is an invalid
// run, not a measurable ranking.
func TestValidateRankingRejectsDuplicates(t *testing.T) {
	if err := ValidateRanking([]string{"a", "b"}); err != nil {
		t.Fatalf("unique ranking rejected: %v", err)
	}
	if err := ValidateRanking(nil); err != nil {
		t.Fatalf("empty ranking rejected: %v", err)
	}
	err := ValidateRanking([]string{"a", "b", "a"})
	if err == nil || !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("duplicate ranking must be rejected naming the document, got %v", err)
	}
}

// TestEmptyRankingScoresZero: an empty (or JSON-null) ranked list scores 0 for
// every metric without panicking.
func TestEmptyRankingScoresZero(t *testing.T) {
	score := ScoreQuery("q", nil, map[string]int{"a": 1}, true)
	if score.NDCGAt10 != 0 || score.RecallAt100 != 0 || score.MRRAt10 != 0 || score.BinaryNDCGAt10 != 0 {
		t.Fatalf("empty ranking must score zero everywhere: %+v", score)
	}
	if ScoreQuery("q", nil, nil, true).NDCGAt10 != 0 {
		t.Fatalf("no judgments must score zero, not NaN")
	}
}

// TestUnjudgedDocumentsConsumeRankSlots: documents absent from the qrels gain
// nothing but still occupy ranks, matching the reference evaluator's run
// semantics.
func TestUnjudgedDocumentsConsumeRankSlots(t *testing.T) {
	relevant := map[string]int{"a": 1}
	if got := NDCGAt10([]string{"x", "a"}, relevant); !approx(got, 1.0/log2(3)) {
		t.Fatalf("NDCGAt10 = %v, want %v", got, 1.0/log2(3))
	}
}

// TestRecallAt100CountsDistinctPositiveJudgments: a set metric; a repeated hit
// cannot inflate it even though ValidateRanking is the structural guard.
func TestRecallAt100CountsDistinctPositiveJudgments(t *testing.T) {
	relevant := map[string]int{"a": 1, "b": 1}
	if got := RecallAt100([]string{"a", "a", "b"}, relevant); !approx(got, 1.0) {
		t.Fatalf("RecallAt100 = %v, want 1", got)
	}
}

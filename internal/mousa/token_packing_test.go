package mousa

import (
	"context"
	"os"
	"testing"

	"github.com/graydeon/mousa/internal/tokenizer"
)

func TestTokenPackingWholeRendering(t *testing.T) {
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official backend environment not configured")
	}
	candidates := exactCandidates(t, "amber oversized oversized oversized oversized oversized oversized oversized oversized oversized oversized", "amber", "amber", "amber tail")
	for i := range candidates {
		candidates[i].LexicalRank = i + 1
		candidates[i].BM25 = float64(i)
	}
	base := exactTrail(t, candidates, 262144)
	c, err := tokenizer.Start(context.Background(), python, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	header := ContextPassage(testTrailRequest(t).SourceID, candidates[1].Segment, candidates[1].Text)
	cap, err := c.Count(header)
	if err != nil {
		t.Fatal(err)
	}
	trail, text, err := PackTokenContext(base, testTrailRequest(t).SourceID, candidates, TokenBudget{Cap: cap}, c)
	if err != nil {
		t.Fatal(err)
	}
	if trail.Candidates[0].Selected || !trail.Candidates[1].Selected || trail.Candidates[2].Omission != "duplicate" || trail.Candidates[3].Selected {
		t.Fatalf("selection %+v", trail.Candidates)
	}
	if text != header || trail.Token.MeasuredTokens != cap {
		t.Fatalf("wrong rendering/count %q %+v", text, trail.Token)
	}
	data, err := EncodeSourceTrail(trail)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSourceTrail(data); err != nil {
		t.Fatal(err)
	}
}

func TestTokenPackingConcatenationBoundary(t *testing.T) {
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official backend environment not configured")
	}
	candidates := exactCandidates(t, "amber\n", "amber\n")
	for i := range candidates {
		candidates[i].LexicalRank = i + 1
		candidates[i].BM25 = float64(i)
	}
	request := testTrailRequest(t)
	base, err := NewSourceTrail(request, trailDecision(t, request, PolicyOutcomeAllow), "amber", candidates, 100, PackingOriginal)
	if err != nil {
		t.Fatal(err)
	}
	counter, err := tokenizer.Start(context.Background(), python, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer counter.Close()
	a := ContextPassage(request.SourceID, candidates[0].Segment, candidates[0].Text)
	b := "\n\n" + ContextPassage(request.SourceID, candidates[1].Segment, candidates[1].Text)
	whole, err := counter.Count(a + b)
	if err != nil {
		t.Fatal(err)
	}
	left, err := counter.Count(a)
	if err != nil {
		t.Fatal(err)
	}
	right, err := counter.Count(b)
	if err != nil {
		t.Fatal(err)
	}
	if left+right == whole {
		t.Fatal("fixture must distinguish summed counts from whole rendering")
	}
	trail, text, err := PackTokenContext(base, request.SourceID, candidates, TokenBudget{Cap: whole}, counter)
	if err != nil {
		t.Fatal(err)
	}
	if text != a+b || !trail.Candidates[0].Selected || !trail.Candidates[1].Selected || trail.Token.MeasuredTokens != whole {
		t.Fatal("concatenation boundary was not measured as a whole")
	}
}

func TestTokenDeniedRecordedCandidates(t *testing.T) {
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official backend environment not configured")
	}
	candidates := exactCandidates(t, "amber")
	candidates[0].LexicalRank = 1
	base := exactTrail(t, candidates, 100)
	counter, err := tokenizer.Start(context.Background(), python, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer counter.Close()
	trail, _, err := PackTokenContext(base, testTrailRequest(t).SourceID, candidates, TokenBudget{Cap: 1000}, counter)
	if err != nil {
		t.Fatal(err)
	}
	trail.Outcome = string(PolicyOutcomeDeny)
	trail.PacketID, err = tokenPacketIdentity(trail)
	if err != nil {
		t.Fatal(err)
	}
	trail.ID, err = NewSourceTrailID(trail)
	if err != nil {
		t.Fatal(err)
	}
	if err := trail.Validate(); err == nil {
		t.Fatal("denied token trail retained selected candidate facts")
	}
}

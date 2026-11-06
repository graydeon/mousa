package mousa

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/tokenizer"
)

func associationCounter(t *testing.T) *tokenizer.Counter {
	t.Helper()
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer environment not configured")
	}
	c, err := tokenizer.Start(context.Background(), python, cache)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func tokenAssociationFixture(t *testing.T, texts ...string) ([]VerifiedLexicalCandidate, []AssociatedPassage) {
	t.Helper()
	all := exactCandidates(t, texts...)
	all[0].LexicalRank = 1
	associated := make([]AssociatedPassage, len(all)-1)
	for i, c := range all[1:] {
		associated[i] = AssociatedPassage{Segment: c.Segment, Item: "qualification", Text: c.Text, Declaration: AssociationDeclaration{"procedure", "qualification", "explicit qualification", "example.operator"}}
	}
	return all[:1], associated
}

func TestTokenAssociatedPackingBoundaries(t *testing.T) {
	t.Parallel()
	for _, packing := range []string{PackingOriginal, PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			primary, associated := tokenAssociationFixture(t, "amber", "a larger supporting qualification that will not fit in the remaining token allowance", "small")
			request := testTrailRequest(t)
			base, err := NewSourceTrail(request, trailDecision(t, request, PolicyOutcomeAllow), "amber", primary, 8192, packing)
			if err != nil {
				t.Fatal(err)
			}
			reference := associationCounter(t)
			expected := ContextPassage(request.SourceID, primary[0].Segment, primary[0].Text) + "\n\n" + ContextPassage(request.SourceID, associated[1].Segment, associated[1].Text)
			cap, err := reference.Count(expected)
			if err != nil {
				t.Fatal(err)
			}
			reference.Close()
			for _, delta := range []uint64{0, 1} {
				c := associationCounter(t)
				trail, text, err := PackAssociatedTokenContext(base, request.SourceID, primary, TokenBudget{Cap: cap - delta}, c, nil, func(selected SourceTrail) ([]AssociatedPassage, []AssociationOmission, error) {
					if !selected.Candidates[0].Selected {
						t.Fatal("primary lost priority")
					}
					return associated, nil, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if trail.Associated[0].Omission != "tokens" || !trail.Candidates[0].Selected {
					t.Fatalf("early oversized qualification: %+v", trail)
				}
				if delta == 0 && (text != expected || !trail.Associated[1].Selected) {
					t.Fatal("later smaller qualification did not fit")
				}
				if delta == 1 && trail.Associated[1].Omission != "tokens" {
					t.Fatal("one-short supporting note was selected")
				}
				if err := trail.Validate(); err != nil {
					t.Fatal(err)
				}
			}
			// A same-segment relation is always a duplicate; byte-equal distinct
			// segments are duplicates only under exact-v1.
			duplicatePrimary := AssociatedPassage{Segment: primary[0].Segment, Item: "qualification", Text: primary[0].Text, Declaration: associated[0].Declaration}
			equal := exactCandidates(t, "different", "amber")[1]
			duplicateText := AssociatedPassage{Segment: equal.Segment, Item: "qualification", Text: equal.Text, Declaration: associated[0].Declaration}
			trail, _, err := PackAssociatedTokenContext(base, request.SourceID, primary, TokenBudget{Cap: 1000}, associationCounter(t), nil, func(SourceTrail) ([]AssociatedPassage, []AssociationOmission, error) {
				return []AssociatedPassage{duplicatePrimary, duplicateText}, nil, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if trail.Associated[0].Omission != "duplicate" || (trail.Associated[1].Omission == "duplicate") != (packing == PackingExactV1) {
				t.Fatal("cross-stage duplicate policy", trail.Associated)
			}
		})
	}
}

func TestTokenAssociatedCombinedAdmission(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		rows, size int
		fail       string
	}{
		{"rows-exact", 100, 1, ""}, {"rows-over", 101, 1, "passage rows"},
		{"bytes-exact", 64, 4096, ""}, {"bytes-over", 65, 4096, "passage bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			texts := make([]string, tc.rows)
			for i := range texts {
				texts[i] = strings.Repeat("x", tc.size)
			}
			primary, associated := tokenAssociationFixture(t, texts...)
			request := testTrailRequest(t)
			base, err := NewSourceTrail(request, trailDecision(t, request, PolicyOutcomeAllow), "amber", primary, 262144, PackingExactV1)
			if err != nil {
				t.Fatal(err)
			}
			trail, _, err := PackAssociatedTokenContext(base, request.SourceID, primary, TokenBudget{Cap: 1000}, associationCounter(t), nil, func(SourceTrail) ([]AssociatedPassage, []AssociationOmission, error) { return associated, nil, nil })
			if tc.fail != "" {
				if err == nil || !strings.Contains(err.Error(), tc.fail) {
					t.Fatalf("wrong admission failure: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			} else {
				if len(trail.Associated) != tc.rows-1 {
					t.Fatal("consideration silently truncated")
				}
				for i, r := range trail.Associated {
					if r.Omission != "duplicate" {
						t.Fatal(fmt.Sprintf("row %d lost duplicate", i))
					}
				}
			}
		})
	}
}

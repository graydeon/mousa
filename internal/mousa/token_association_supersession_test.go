package mousa

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
)

func TestTokenAssociatedSupersessionComposition(t *testing.T) {
	for _, packing := range []string{PackingOriginal, PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			declaration := v4Declaration(t)
			state := supersessionSelectionState(t, declaration)
			request := v4Request(t, declaration.SourceID)
			candidates := v4Candidates(t, declaration)
			for i := range candidates {
				candidates[i].LexicalRank = i + 1
				candidates[i].BM25 = float64(i)
			}
			base, err := NewSourceTrailWithSupersession(request, trailDecision(t, request, PolicyOutcomeAllow), "term", candidates, 8192, packing, &state, &declaration)
			if err != nil {
				t.Fatal(err)
			}
			before, err := EncodeSourceTrail(base)
			if err != nil {
				t.Fatal(err)
			}
			for _, firing := range []bool{false, true} {
				got, _, err := PackAssociatedTokenContext(base, request.SourceID, candidates, TokenBudget{Cap: 4096}, associationCounter(t), &declaration, func(selected SourceTrail) ([]AssociatedPassage, []AssociationOmission, error) {
					if selected.Supersession == nil {
						t.Fatal("resolver lost producing consultation")
					}
					if firing {
						return []AssociatedPassage{associatedPassageFixture(t)}, nil, nil
					}
					return nil, nil, nil
				})
				if err != nil {
					t.Fatalf("token association supersession composition: %v", err)
				}
				want := "mousa.source_trail.v8"
				if firing {
					want = "mousa.source_trail.v13"
				}
				if got.Schema != want {
					t.Fatalf("schema = %s, want %s", got.Schema, want)
				}
				encoded, err := EncodeSourceTrail(got)
				if err != nil {
					t.Fatal(err)
				}
				decoded, err := DecodeSourceTrail(encoded)
				if err != nil || decoded.ID != got.ID {
					t.Fatalf("canonical readback: %v", err)
				}
				got.Supersession.Dispositions[0].ContentSHA256 = SHA256{42}
				*got.Supersession.DeclarationID = SupersessionDeclarationID{}
				*got.Supersession.ActivationID = SupersessionActivationID{}
				got.Candidates[0].TextBytes++
				after, err := EncodeSourceTrail(base)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatal("token composition aliased its producing base", err)
				}
			}
		})
	}
}

func TestTokenAssociatedSupersessionSuppressionBeforeAggregate(t *testing.T) {
	for _, packing := range []string{PackingOriginal, PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			d := v4Declaration(t)
			state := supersessionSelectionState(t, d)
			request := v4Request(t, d.SourceID)
			var candidates []VerifiedLexicalCandidate
			var associated []AssociatedPassage
			for i := range 68 {
				text := fmt.Sprintf("%04d", i) + strings.Repeat("x", 4092)
				c := supersessionCandidate(t, d.PredecessorRepresentationID, d.SourceID, text, i+1, CandidateAccepted)
				if i < 34 {
					c.LexicalRank, c.BM25 = i+1, float64(i)
					candidates = append(candidates, c)
				} else {
					associated = append(associated, AssociatedPassage{Segment: c.Segment, Item: "old", Text: text, Declaration: AssociationDeclaration{"primary", "old", "withheld qualification", "test"}})
				}
			}
			primary := supersessionCandidate(t, supersessionRepresentation("aggregate-primary"), d.SourceID, "go", 35, CandidateAccepted)
			primary.LexicalRank, primary.BM25 = 35, 34
			candidates = append(candidates, primary)
			support := supersessionCandidate(t, supersessionRepresentation("aggregate-support"), d.SourceID, "support", 1, CandidateAccepted)
			associated = append(associated, AssociatedPassage{Segment: support.Segment, Item: "support", Text: support.Text, Declaration: AssociationDeclaration{"primary", "support", "surviving qualification", "test"}})
			base, err := NewSourceTrailWithSupersession(request, trailDecision(t, request, PolicyOutcomeAllow), "term", candidates, 9, packing, &state, &d)
			if err != nil {
				t.Fatal(err)
			}
			got, rendered, err := PackAssociatedTokenContext(base, d.SourceID, candidates, TokenBudget{Cap: 4096}, associationCounter(t), &d, func(SourceTrail) ([]AssociatedPassage, []AssociationOmission, error) {
				return associated, nil, nil
			})
			if err != nil {
				t.Fatal("withheld bytes consumed admitted aggregate", err)
			}
			if got.Schema != SourceTrailSchemaV13 || got.UsedBytes != 9 || !strings.HasSuffix(rendered, "support") {
				t.Fatal("surviving support lost exact byte fit")
			}
			for i := range 34 {
				if got.Candidates[i].Selected || got.Token.ProposedTokens[i] != nil || got.Associated[i].Omission != "superseded" || got.Token.Associations.ProposedTokens[i] != nil {
					t.Fatal("withheld predecessor was budgeted or counted")
				}
			}
			count := uint64(1)
			got.Token.Associations.ProposedTokens[0] = &count
			got.PacketID, err = tokenPacketIdentity(got)
			if err != nil {
				t.Fatal(err)
			}
			got.ID, err = NewSourceTrailID(got)
			if err != nil {
				t.Fatal(err)
			}
			if err := got.Validate(); err == nil || !strings.Contains(err.Error(), "associated token accounting") {
				t.Fatalf("withheld support count missed named invariant: %v", err)
			}
			got.Token.Associations.ProposedTokens[0] = nil
			got.Associated[0].TextBytes = math.MaxUint64
			got.PacketID, err = tokenPacketIdentity(got)
			if err != nil {
				t.Fatal(err)
			}
			got.ID, err = NewSourceTrailID(got)
			if err != nil {
				t.Fatal(err)
			}
			if err := got.Validate(); err == nil || !strings.Contains(err.Error(), "association profile") {
				t.Fatalf("overflow was not rejected by bounded profile: %v", err)
			}
		})
	}
}

func TestTokenAssociatedSupersessionClosedCanonicalProfile(t *testing.T) {
	data, err := os.ReadFile("testdata/trail-v11.json")
	if err != nil {
		t.Fatal(err)
	}
	base, err := DecodeSourceTrail(data)
	if err != nil {
		t.Fatal(err)
	}
	base.Schema = SourceTrailSchemaV13
	base.Supersession = &SupersessionSelection{Consulted: true, Dispositions: []SupersessionDisposition{}}
	base.PacketID, err = tokenPacketIdentity(base)
	if err != nil {
		t.Fatal(err)
	}
	base.ID, err = NewSourceTrailID(base)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := EncodeSourceTrail(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, reason string
		mutate       func(*SourceTrail)
	}{
		{"count-alignment", "association profile", func(t *SourceTrail) { t.Token.Associations.ProposedTokens = nil }},
		{"primary-alignment", "association profile", func(t *SourceTrail) { t.Token.PrimaryIDs[0] = SegmentID{} }},
		{"missing-consultation", "token selection", func(t *SourceTrail) { t.Supersession = nil }},
		{"unmeasured-selected", "associated token accounting", func(t *SourceTrail) { t.Token.Associations.ProposedTokens[0] = nil }},
		{"overflow", "association profile", func(t *SourceTrail) { t.Associated[0].TextBytes = math.MaxUint64 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed, err := DecodeSourceTrail(valid)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&changed)
			changed.PacketID, err = tokenPacketIdentity(changed)
			if err != nil {
				t.Fatal(err)
			}
			changed.ID, err = NewSourceTrailID(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err := changed.Validate(); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("rehashed %s did not fail named invariant: %v", tc.name, err)
			}
		})
	}
	for _, replacement := range []string{"null", "0", "\"true\""} {
		malformed := bytes.Replace(valid, []byte("\"consulted\":true"), []byte("\"consulted\":"+replacement), 1)
		if bytes.Equal(malformed, valid) {
			t.Fatal("typed-shape mutation missed its field")
		}
		if _, err := DecodeSourceTrail(malformed); err == nil {
			t.Fatal("invalid consultation JSON type accepted")
		}
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(valid, &shape); err != nil {
		t.Fatal(err)
	}
	shape["associated"] = []byte("null")
	malformed, err := json.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSourceTrail(malformed); err == nil || !strings.Contains(err.Error(), "canonical member") {
		t.Fatalf("null association member accepted by closed shape: %v", err)
	}
}

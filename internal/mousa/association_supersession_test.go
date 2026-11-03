package mousa

import (
	"math"
	"testing"
)

func TestAssociatedSupersessionConstructor(t *testing.T) {
	t.Parallel()
	declaration := v4Declaration(t)
	state := supersessionSelectionState(t, declaration)
	request := v4Request(t, declaration.SourceID)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	candidates := v4Candidates(t, declaration)
	for _, packing := range []string{PackingOriginal, PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			base, err := NewSourceTrailWithSupersession(request, decision, "term", candidates, 100, packing, &state, &declaration)
			if err != nil {
				t.Fatal(err)
			}
			passage := associatedPassageFixture(t)
			unchanged, err := AppendSupersessionAssociatedPassages(base, candidates, nil, nil, &declaration)
			if err != nil || unchanged.ID != base.ID || unchanged.Schema != SourceTrailSchemaV4 {
				t.Fatal("nonfiring stage changed history", err)
			}
			got, err := AppendSupersessionAssociatedPassages(base, candidates, []AssociatedPassage{passage}, nil, &declaration)
			if err != nil {
				t.Fatal(err)
			}
			data, err := EncodeSourceTrail(got)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeSourceTrail(data)
			if err != nil || decoded.ID != got.ID || got.Schema != SourceTrailSchemaV12 {
				t.Fatal("constructor/codec disagree", err)
			}
			if _, err := AppendAssociatedPassages(base, candidates, []AssociatedPassage{passage}, nil); err == nil {
				t.Fatal("unconsulted constructor accepted composed stage")
			}
			if _, err := AppendSupersessionAssociatedPassages(base, candidates, []AssociatedPassage{passage}, nil, nil); err == nil {
				t.Fatal("missing recorded declaration accepted")
			}
			for _, mutation := range []string{"overflow", "selected-suppressed", "no-primary", "unbounded"} {
				var damaged SourceTrail
				damaged, err = DecodeSourceTrail(data)
				if err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "overflow":
					damaged.Associated[0].TextBytes = math.MaxUint64
				case "selected-suppressed":
					damaged.Associated[0].Omission = "superseded"
				case "no-primary":
					for i := range damaged.Candidates {
						damaged.Candidates[i].Selected = false
					}
				case "unbounded":
					for len(damaged.Associated) <= MaxAssociationPassages {
						damaged.Associated = append(damaged.Associated, damaged.Associated[0])
					}
				}
				id, e := contextPacketIDV3(damaged)
				if e != nil {
					t.Fatal(e)
				}
				damaged.PacketID = id.String()
				damaged.ID, err = NewSourceTrailID(damaged)
				if err != nil {
					t.Fatal(err)
				}
				if damaged.Validate() == nil {
					t.Fatalf("resealed %s accepted", mutation)
				}
			}
		})
	}
}

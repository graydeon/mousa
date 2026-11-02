package sqlite

import (
	"github.com/graydeon/mousa/internal/mousa"
	"testing"
)

func TestAssociatedSupersessionFreedPrimaryFires(t *testing.T) {
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			f := newV4Fixture(t)
			budget := uint64(len("sharedterm predecessor evidence") + len("sharedterm peer evidence"))
			plain, err := f.store.EvaluateAndTraceLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "before"), "predecessor", 10, budget, packing)
			if err != nil || len(plain.Candidates) != 2 || !plain.Trail.Candidates[0].Selected || plain.Trail.Candidates[1].Selected {
				t.Fatalf("baseline selection: %+v %v", plain, err)
			}
			names := make([]string, 2)
			for i, c := range plain.Candidates {
				names[i], err = localItemPathOfRepresentation(t.Context(), f.store.db, f.source.ID, c.Segment.RepresentationID)
				if err != nil {
					t.Fatal(err)
				}
			}
			declaration := supersessionTestDeclaration(t, f.source.ID, names[0], plain.Candidates[0].Segment.RepresentationID, "successor", f.successor.ID, "test", "explicit replacement")
			if err := f.store.PutSupersessionDeclaration(t.Context(), declaration); err != nil {
				t.Fatal(err)
			}
			activation := activationTestTransition(t, f.source.ID, mousa.SupersessionActivationID{}, declaration.ID, "activate", activationBaseUsec)
			if err := f.store.ApplySupersessionActivation(t.Context(), activation); err != nil {
				t.Fatal(err)
			}
			ds := []mousa.AssociationDeclaration{{FromItem: names[0], ToItem: "successor", Basis: "must not fire", Author: "test"}, {FromItem: names[1], ToItem: "peer", Basis: "freed primary support", Author: "test"}, {FromItem: "peer", ToItem: "successor", Basis: "must not recurse", Author: "test"}}
			composed, err := f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "after"), "predecessor", 10, budget, packing, ds)
			if err != nil {
				t.Fatal(err)
			}
			if composed.Trail.Candidates[0].Selected || !composed.Trail.Candidates[1].Selected || composed.Trail.Candidates[1].FinalRank != 2 || len(composed.Trail.Associated) != 1 || composed.Trail.Associated[0].ToItem != "peer" || !composed.Trail.Associated[0].Selected || composed.Trail.UsedBytes != budget {
				t.Fatalf("freed primary/priority/depth: %+v", composed.Trail)
			}
		})
	}
}

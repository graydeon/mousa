package sqlite

import (
	"github.com/graydeon/mousa/internal/mousa"
	"testing"
)

func TestAssociatedSupersessionSelection(t *testing.T) {
	t.Parallel()
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			f := newV4Fixture(t)
			f.activate(t)
			declarations := []mousa.AssociationDeclaration{
				{FromItem: "predecessor", ToItem: "peer", Basis: "must not fire", Author: "test"},
				{FromItem: "peer", ToItem: "predecessor", Basis: "explicit qualification", Author: "test"},
				{FromItem: "peer", ToItem: "twin", Basis: "surviving identical support", Author: "test"},
			}
			// Only peer matches; predecessor and twin are nonmatching supporting targets.
			got, err := f.store.traceLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "composed"), "peer", 10, 1024, true, packing, declarations, true)
			if err != nil {
				t.Fatal(err)
			}
			if got.Trail.Schema != "mousa.source_trail.v12" || len(got.Trail.Associated) != 2 {
				t.Fatalf("composed stage: %+v", got.Trail)
			}
			old, twin := got.Trail.Associated[0], got.Trail.Associated[1]
			if old.Selected || old.Omission != "superseded" || old.DuplicateOf != "" || got.Associated[0].Text != "" {
				t.Fatalf("withheld support: %+v", old)
			}
			if !twin.Selected || twin.Omission != "" || got.Associated[1].Text != "sharedterm predecessor evidence" {
				t.Fatalf("surviving support: %+v", twin)
			}
			if got.Trail.UsedBytes != uint64(len("sharedterm peer evidence")+len("sharedterm predecessor evidence")) {
				t.Fatalf("budget: %d", got.Trail.UsedBytes)
			}
			read, err := f.store.GetSourceTrail(t.Context(), got.Trail.ID)
			if err != nil || read.ID != got.Trail.ID {
				t.Fatalf("history: %+v %v", read, err)
			}
			// A matching suppressed primary cannot expand its own declaration.
			only, err := f.store.traceLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "nonfiring"), "predecessor", 10, 1024, true, packing, declarations[:1], true)
			if err != nil {
				t.Fatal(err)
			}
			if len(only.Trail.Associated) != 0 || only.Trail.Schema != mousa.SourceTrailSchemaV4 {
				t.Fatalf("suppressed primary fired: %+v", only.Trail)
			}
		})
	}
}

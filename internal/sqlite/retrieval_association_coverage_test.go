package sqlite

import (
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestRetrievalAssociatedNoCoverageAndDuplicateTargets(t *testing.T) {
	f := newV4Fixture(t)
	defer f.store.Close()
	space := importAssociationVector(t, f)
	seedLocalItem(t, f.store, f.source, "peer", "peer revised evidence")
	ds := []mousa.AssociationDeclaration{
		{FromItem: "peer", ToItem: "twin", Basis: "first", Author: "test"},
		{FromItem: "predecessor", ToItem: "twin", Basis: "duplicate target", Author: "test"},
		{FromItem: "peer", ToItem: "predecessor", Basis: "primary duplicate", Author: "test"},
	}
	vector, err := f.store.EvaluateAndTraceAssociatedVector(t.Context(), testEvaluationRequest(t, f.source.ID, "no-current-vectors"), space, []float64{1, 0}, 1024, mousa.PackingOriginal, ds, false)
	if err != nil || vector.Trail.Vector.Indexed != 0 || vector.Trail.Vector.Eligible != 4 || len(vector.Associated) != 0 || vector.Trail.Schema != mousa.SourceTrailSchemaV5 {
		t.Fatal("vector absence fired support or lost coverage", err)
	}
	hybrid, err := f.store.EvaluateAndTraceAssociatedHybrid(t.Context(), testEvaluationRequest(t, f.source.ID, "lexical-only-support"), "peer predecessor", "original", space, []float64{1, 0}, 1024, mousa.PackingOriginal, ds, false)
	if err != nil {
		t.Fatal(err)
	}
	if hybrid.Trail.Hybrid.Indexed != 0 || hybrid.Trail.Hybrid.Eligible != 4 || len(hybrid.Trail.AssociationOmissions) != 1 || hybrid.Trail.AssociationOmissions[0].Reason != mousa.AssociationDuplicateTarget {
		t.Fatal("lexical-only union or target deduplication")
	}
	for _, r := range hybrid.Trail.Hybrid.Candidates {
		if r.Vector != nil || r.Lexical == nil || r.RRF != mousa.HybridRRFScore(r.Lexical.Rank, 0) {
			t.Fatal("invented vector contribution")
		}
	}
	for _, r := range hybrid.Trail.Associated {
		if r.Selected || r.Omission != "duplicate" || r.DuplicateOf != r.SegmentID.String() {
			t.Fatal("associated primary must remain identity duplicate")
		}
	}
	seedLocalItem(t, f.store, f.source, "twin", "replacement target")
	if _, err := f.store.GetSourceTrail(t.Context(), hybrid.Trail.ID); err != nil {
		t.Fatal("target revision reinterpreted producing pins", err)
	}
}

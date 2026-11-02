package sqlite

import (
	"fmt"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestAssociatedSupersessionNoHistoryTargetsAndLimits(t *testing.T) {
	f := newV4Fixture(t)
	ds := []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "missing", Basis: "qualification", Author: "test"}}
	query := func(name, expression string, ds []mousa.AssociationDeclaration) TracedLexicalResult {
		t.Helper()
		r, err := f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, name), expression, 10, 1024, mousa.PackingOriginal, ds)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	unknown := query("no-history", "peer", ds)
	if unknown.Trail.Schema != mousa.SourceTrailSchemaV12 || unknown.Trail.Supersession.ActivationID != nil || unknown.Trail.AssociationOmissions[0].Reason != mousa.AssociationTargetUnknown {
		t.Fatalf("no history/unknown: %+v", unknown.Trail)
	}
	empty := query("no-matches", "unmatchedterm", ds)
	if empty.Trail.Schema != mousa.SourceTrailSchemaV4 || len(empty.Trail.AssociationOmissions) != 0 {
		t.Fatal("no matches fabricated stage")
	}
	f.activate(t)
	if _, err := f.store.DeleteLocalItem(t.Context(), f.source.ID, "successor"); err != nil {
		t.Fatal(err)
	}
	ds[0].ToItem = "predecessor"
	suppressed := query("successor-withdrawn", "peer", ds)
	if suppressed.Trail.Associated[0].Omission != "superseded" || suppressed.Trail.Associated[0].Selected {
		t.Fatal("unavailable successor restored associated predecessor")
	}
	ds[0].ToItem = "successor"
	inactive := query("inactive", "peer", ds)
	if inactive.Trail.AssociationOmissions[0].Reason != mousa.AssociationTargetInactive {
		t.Fatal("inactive target behavior changed")
	}
	seedLocalItem(t, f.store, f.source, "large", strings.Repeat("# a\n\n", mousa.MaxAssociationPassages+2))
	ds[0].ToItem = "large"
	truncated := query("truncated", "peer", ds)
	if len(truncated.Trail.Associated) != mousa.MaxAssociationPassages || truncated.Trail.AssociationOmissions[0].Reason != mousa.AssociationTargetTruncated {
		t.Fatal("composed consideration cap changed")
	}
	ds = nil
	for i := range 6 {
		ds = append(ds, mousa.AssociationDeclaration{FromItem: "peer", ToItem: fmt.Sprint("missing-", i), Basis: "qualification", Author: "test"})
	}
	ds = append(ds, ds[0])
	capped := query("fan-out", "peer", ds)
	if len(capped.Trail.AssociationOmissions) != 7 || capped.Trail.AssociationOmissions[4].Reason != mousa.AssociationFanOut || capped.Trail.AssociationOmissions[6].Reason != mousa.AssociationDuplicateTarget {
		t.Fatal("composed fan-out/deduplicated target limit changed")
	}
}

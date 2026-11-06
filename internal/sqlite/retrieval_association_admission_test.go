package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestRetrievalAssociatedPrimaryAdmissionAndFailures(t *testing.T) {
	for _, mode := range []string{"vector", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			space := importSupersessionVectors(t, f)
			f.activate(t)
			ds := []mousa.AssociationDeclaration{{FromItem: "predecessor", ToItem: "successor", Basis: "support", Author: "test"}}
			query := func(ctx context.Context, name string, declarations []mousa.AssociationDeclaration, suppress bool) (TracedVectorResult, error) {
				r := testEvaluationRequest(t, f.source.ID, name)
				if mode == "vector" {
					return f.store.EvaluateAndTraceAssociatedVector(ctx, r, space, []float64{1, 0}, 1024, mousa.PackingOriginal, declarations, suppress)
				}
				return f.store.EvaluateAndTraceAssociatedHybrid(ctx, r, "predecessor", "original", space, []float64{1, 0}, 1024, mousa.PackingOriginal, declarations, suppress)
			}
			got, err := query(t.Context(), "suppressed-primary", ds, true)
			if err != nil || got.Trail.Schema != mousa.SourceTrailSchemaV9 || len(got.Associated) != 0 {
				t.Fatal("suppressed primary fired support", err)
			}
			got, err = query(t.Context(), "ordinary-primary", ds, false)
			if err != nil || len(got.Associated) != 1 || !got.Trail.Associated[0].Selected {
				t.Fatal("unembedded support missing", err)
			}
			id := got.Trail.ID
			got.Associated[0].Text = "caller mutation"
			got.Trail.Associated[0].Author = "caller mutation"
			read, err := f.store.GetSourceTrail(t.Context(), id)
			if err != nil || read.Associated[0].Author != "test" {
				t.Fatal("result aliases retained record", err)
			}
			before := freshAuditCounts(t, f.store)
			if _, err := query(t.Context(), "duplicate-declaration", append(ds, ds[0]), false); err == nil {
				t.Fatal("duplicate declaration accepted")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := query(ctx, "cancelled", ds, true); err == nil {
				t.Fatal("cancelled query accepted")
			}
			if !reflect.DeepEqual(before, freshAuditCounts(t, f.store)) {
				t.Fatal("failure committed producing state")
			}
			withdrawVerifiedSource(t, f.store, f.source.ID, "withdraw-association", 3)
			denied, err := query(t.Context(), "denied-association", ds, true)
			if err != nil || denied.Decision.Outcome != mousa.PolicyOutcomeDeny || len(denied.Associated) != 0 || len(denied.Passages) != 0 || len(denied.Trail.Associated) != 0 {
				t.Fatal("declaration bypassed authorization", err)
			}
		})
	}
}

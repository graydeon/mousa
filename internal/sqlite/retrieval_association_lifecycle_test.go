package sqlite

import (
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestRetrievalAssociatedMixedAncestry(t *testing.T) {
	for _, mode := range []string{"vector", "hybrid"} {
		for _, measured := range []bool{false, true} {
			t.Run(mode+"/"+map[bool]string{false: "bytes", true: "tokens"}[measured], func(t *testing.T) {
				f := newV4Fixture(t)
				defer f.store.Close()
				space := importAssociationVector(t, f)
				item, err := f.store.GetLocalItem(t.Context(), f.source.ID, "twin")
				if err != nil {
					t.Fatal(err)
				}
				other := addVerifiedLexicalDocument(t, f.store, "mixed-support-ancestor", "foreign ancestor")
				text := "Mixed supporting text"
				mixed := putVerifiedRepresentation(t, f.store, []mousa.DerivationInput{
					mousa.NewArtifactDerivationInput(item.Artifact.ID),
					mousa.NewRepresentationDerivationInput(other.representation.ID),
				}, "mixed-support", []byte(text), true)
				if _, err := f.store.ActivateLocalItem(t.Context(), f.source.ID, "twin", mixed.representation.ID, []byte(text)); err != nil {
					t.Fatal(err)
				}
				ds := []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "twin", Basis: "mixed ancestry", Author: "test"}}
				query := func(name string) (TracedVectorResult, error) {
					request := testEvaluationRequest(t, f.source.ID, name)
					if measured {
						options := tokenOptions(t)
						options.Associations = ds
						options.Supersession = true
						return queryTokenRetrieval(t, f, space, mode, mousa.PackingOriginal, name, 1024, options)
					}
					if mode == "vector" {
						return f.store.EvaluateAndTraceAssociatedVector(t.Context(), request, space, []float64{1, 0}, 1024, mousa.PackingOriginal, ds, true)
					}
					return f.store.EvaluateAndTraceAssociatedHybrid(t.Context(), request, "peer", "original", space, []float64{1, 0}, 1024, mousa.PackingOriginal, ds, true)
				}
				before, err := query("mixed-before")
				if err != nil {
					t.Fatal(err)
				}
				if len(before.Associated) != 1 || before.Associated[0].Text != text {
					t.Fatal("mixed ancestry fixture did not release support before withdrawal")
				}
				canonical, err := f.store.GetSourceTrail(t.Context(), before.Trail.ID)
				if err != nil {
					t.Fatal(err)
				}
				withdrawVerifiedSource(t, f.store, other.source.ID, "withdraw-support-ancestor", 200)
				after, err := query("mixed-after")
				if err != nil {
					t.Fatal(err)
				}
				if len(after.Associated) != 0 || len(after.Trail.Associated) != 0 || strings.Contains(after.Context, text) {
					t.Fatal("withdrawn ancestry released supporting content or metadata")
				}
				if after.Trail.Token != nil && (len(after.Trail.Token.Associations.AssociatedIDs) != 0 || len(after.Trail.Token.Associations.ProposedTokens) != 0) {
					t.Fatal("withdrawn ancestry retained aligned count placeholders")
				}
				read, err := f.store.GetSourceTrail(t.Context(), before.Trail.ID)
				if err != nil || !reflect.DeepEqual(read, canonical) {
					t.Fatal("fresh lifecycle rewrote producing history", err)
				}
				inspected, err := f.store.InspectSourceTrail(t.Context(), testEvaluationRequest(t, f.source.ID, "mixed-history"), after.Trail.ID)
				if err != nil || inspected.Historical == nil || len(inspected.Historical.Associated) != 0 {
					t.Fatal("fresh history leaked lifecycle-rejected supporting rows", err)
				}
			})
		}
	}
}

package sqlite

import (
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestRetrievalAssociatedResealedSuppression(t *testing.T) {
	for _, mode := range []string{"vector", "hybrid"} {
		for _, measured := range []bool{false, true} {
			for _, mutation := range []string{"suppression", "false-suppression", "size"} {
				t.Run(mode+"/"+map[bool]string{false: "bytes", true: "tokens"}[measured]+"/"+mutation, func(t *testing.T) {
					f := newV4Fixture(t)
					defer f.store.Close()
					space := importAssociationVector(t, f)
					f.activate(t)
					ds := []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "predecessor", Basis: "withheld", Author: "test"}, {FromItem: "peer", ToItem: "twin", Basis: "survivor", Author: "test"}}
					var got TracedVectorResult
					var err error
					budget := uint64(len("sharedterm peer evidence"))
					if measured {
						options := tokenOptions(t)
						options.Supersession = true
						options.Associations = ds
						if mode == "vector" {
							got, err = f.store.EvaluateAndTraceTokenVector(t.Context(), testEvaluationRequest(t, f.source.ID, "tamper"), space, []float64{1, 0}, budget, mousa.PackingOriginal, options)
						} else {
							got, err = f.store.EvaluateAndTraceTokenHybrid(t.Context(), testEvaluationRequest(t, f.source.ID, "tamper"), "peer", "original", space, []float64{1, 0}, budget, mousa.PackingOriginal, options)
						}
					} else if mode == "vector" {
						got, err = f.store.EvaluateAndTraceAssociatedVector(t.Context(), testEvaluationRequest(t, f.source.ID, "tamper"), space, []float64{1, 0}, budget, mousa.PackingOriginal, ds, true)
					} else {
						got, err = f.store.EvaluateAndTraceAssociatedHybrid(t.Context(), testEvaluationRequest(t, f.source.ID, "tamper"), "peer", "original", space, []float64{1, 0}, budget, mousa.PackingOriginal, ds, true)
					}
					if err != nil {
						t.Fatal(err)
					}
					changed := got.Trail
					reason := "associated suppression disagrees"
					switch mutation {
					case "suppression":
						changed.Associated[0].Omission = "budget"
						if measured {
							changed.Associated[0].Omission = "bytes"
						}
					case "false-suppression":
						changed.Associated[1].Omission = "superseded"
					case "size":
						changed.Associated[0].TextBytes++
						reason = "associated size disagrees"
					}
					if measured {
						changed = rebindTokenRecord(t, changed)
					} else {
						changed.ID, err = mousa.NewSourceTrailID(changed)
						if err != nil {
							t.Fatal(err)
						}
					}
					data, err := mousa.EncodeSourceTrail(changed)
					if err != nil {
						t.Fatal("semantic tamper prerequisite", err)
					}
					if _, err = mousa.DecodeSourceTrail(data); err != nil {
						t.Fatal("structural prerequisite", err)
					}
					commitV4Trail(t, f.store, changed)
					if _, err = f.store.GetSourceTrail(t.Context(), changed.ID); !IsCode(err, CodeIntegrity) || !strings.Contains(err.Error(), reason) {
						t.Fatalf("readback semantic rejection: %v", err)
					}
					path := f.store.path
					f.store.Close()
					reopened, err := Open(t.Context(), path)
					if reopened != nil {
						reopened.Close()
					}
					if !IsCode(err, CodeIntegrity) || !strings.Contains(err.Error(), reason) {
						t.Fatalf("startup semantic rejection: %v", err)
					}
				})
			}
		}
	}
}

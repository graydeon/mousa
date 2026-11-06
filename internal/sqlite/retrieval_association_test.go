package sqlite

import (
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func importAssociationVector(t *testing.T, f *v4Fixture) mousa.SHA256 {
	t.Helper()
	inputs, err := f.store.ExportEmbeddingInputs(t.Context(), testEvaluationRequest(t, f.source.ID, "association-export"))
	if err != nil {
		t.Fatal(err)
	}
	batch := mousa.VectorImport{Schema: mousa.VectorImportSchema, Space: vectorSpace()}
	for _, p := range inputs.Passages {
		if p.Item == "peer" {
			batch.Embeddings = append(batch.Embeddings, mousa.SuppliedEmbedding{EmbeddingInput: p.EmbeddingInput, Values: []float64{1, 0}})
		}
	}
	space, err := f.store.ImportEmbeddings(t.Context(), f.source.ID, batch)
	if err != nil {
		t.Fatal(err)
	}
	return space
}

func TestRetrievalAssociatedComposition(t *testing.T) {
	for _, mode := range []string{"vector", "hybrid"} {
		for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
			t.Run(mode+"/"+packing, func(t *testing.T) {
				f := newV4Fixture(t)
				defer f.store.Close()
				space := importAssociationVector(t, f)
				_, active := f.activate(t)
				ds := []mousa.AssociationDeclaration{
					{FromItem: "peer", ToItem: "predecessor", Basis: "support", Author: "test"},
					{FromItem: "peer", ToItem: "twin", Basis: "equal support", Author: "test"},
				}
				serial := 0
				query := func(token, suppress bool, bytes, tokens uint64, declarations []mousa.AssociationDeclaration) (TracedVectorResult, error) {
					serial++
					r := testEvaluationRequest(t, f.source.ID, "associated-"+string(rune('a'+serial)))
					if token {
						o := tokenOptions(t)
						o.Associations, o.Supersession, o.Budget.Cap = declarations, suppress, tokens
						if mode == "vector" {
							return f.store.EvaluateAndTraceTokenVector(t.Context(), r, space, []float64{1, 0}, bytes, packing, o)
						}
						return f.store.EvaluateAndTraceTokenHybrid(t.Context(), r, "peer", "original", space, []float64{1, 0}, bytes, packing, o)
					}
					if mode == "vector" {
						return f.store.EvaluateAndTraceAssociatedVector(t.Context(), r, space, []float64{1, 0}, bytes, packing, declarations, suppress)
					}
					return f.store.EvaluateAndTraceAssociatedHybrid(t.Context(), r, "peer", "original", space, []float64{1, 0}, bytes, packing, declarations, suppress)
				}
				for _, token := range []bool{false, true} {
					plain, err := query(token, false, 1024, 4096, nil)
					if err != nil {
						t.Fatal(err)
					}
					for _, suppress := range []bool{false, true} {
						got, err := query(token, suppress, 1024, 4096, ds)
						if err != nil {
							t.Fatal(err)
						}
						want := mousa.SourceTrailSchemaV14
						if token {
							want = mousa.SourceTrailSchemaV15
						}
						if got.Trail.Schema != want || len(got.Associated) != 2 || len(got.Passages) != 1 || len(got.Trail.Candidates) != 1 {
							t.Fatalf("composition: %+v", got)
						}
						if !reflect.DeepEqual(got.Trail.Vector, plain.Trail.Vector) || !reflect.DeepEqual(got.Trail.Hybrid, plain.Trail.Hybrid) {
							t.Fatal("support changed producing retrieval facts")
						}
						if suppress {
							if got.Trail.Associated[0].Omission != "superseded" || got.Associated[0].Text != "" || !got.Trail.Associated[1].Selected {
								t.Fatal("suppression must precede support deduplication")
							}
							if token && got.Trail.Token.Associations.ProposedTokens[0] != nil {
								t.Fatal("suppressed support was counted")
							}
						} else if !got.Trail.Associated[0].Selected || (packing == mousa.PackingExactV1 && got.Trail.Associated[1].Omission != "duplicate") {
							t.Fatal("support equality packing")
						}
						data, err := mousa.EncodeSourceTrail(got.Trail)
						if err != nil {
							t.Fatal(err)
						}
						decoded, err := mousa.DecodeSourceTrail(data)
						if err != nil || !reflect.DeepEqual(decoded, got.Trail) {
							t.Fatal("closed roundtrip", err)
						}
						limit := uint64(4096)
						if token {
							limit = got.Trail.Token.MeasuredTokens
						}
						exact, err := query(token, suppress, got.Trail.UsedBytes, limit, ds)
						if err != nil || exact.Trail.UsedBytes != got.Trail.UsedBytes || exact.Context != got.Context {
							t.Fatal("exact fit", err)
						}
						if suppress {
							short, err := query(token, true, got.Trail.UsedBytes-1, limit, ds)
							if err != nil || short.Trail.Associated[1].Selected {
								t.Fatal("one-short byte fit", err)
							}
							if token {
								short, err = query(true, true, got.Trail.UsedBytes, limit-1, ds)
								if err != nil || short.Trail.Associated[1].Omission != "tokens" {
									t.Fatal("one-short token fit", err)
								}
							}
						}
						history, err := f.store.InspectSourceTrail(t.Context(), testEvaluationRequest(t, f.source.ID, "history-"+string(rune('a'+serial))), got.Trail.ID)
						if err != nil || history.Historical == nil || len(history.Historical.Associated) != 2 {
							t.Fatal("history", err)
						}
						if token {
							history, err = f.store.InspectSourceTrailWithTokens(t.Context(), testEvaluationRequest(t, f.source.ID, "recount-"+string(rune('a'+serial))), got.Trail.ID, tokenOptions(t).Counter)
							if err != nil || history.Historical.TokenVerification != "official_recount_verified" {
								t.Fatal("recount", err)
							}
						}
					}
					unfired, err := query(token, false, 1024, 4096, []mousa.AssociationDeclaration{{FromItem: "successor", ToItem: "twin", Basis: "unselected", Author: "test"}})
					if err != nil || unfired.Trail.Schema != plain.Trail.Schema || len(unfired.Associated) != 0 {
						t.Fatal("no-fire profile", err)
					}
					missing, err := query(token, false, 1024, 4096, []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "missing", Basis: "missing", Author: "test"}})
					if err != nil || len(missing.Trail.AssociationOmissions) != 1 {
						t.Fatal("omission-only", err)
					}
					zero, err := query(token, true, 1, 4096, ds)
					if err != nil || len(zero.Associated) != 0 {
						t.Fatal("no selected primary", err)
					}
				}
				transition := activationTestTransition(t, f.source.ID, active.CurrentActivationID, mousa.SupersessionDeclarationID{}, "later", activationBaseUsec+1)
				if err := f.store.ApplySupersessionActivation(t.Context(), transition); err != nil {
					t.Fatal(err)
				}
				reopened, err := Open(t.Context(), f.store.path)
				if err != nil {
					t.Fatal("startup after changed activation", err)
				}
				reopened.Close()
			})
		}
	}
}

func TestRetrievalAssociatedResealedAttribution(t *testing.T) {
	f := newV4Fixture(t)
	defer f.store.Close()
	space := importAssociationVector(t, f)
	o := tokenOptions(t)
	o.Associations = []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "twin", Basis: "support", Author: "test"}}
	got, err := f.store.EvaluateAndTraceTokenVector(t.Context(), testEvaluationRequest(t, f.source.ID, "tamper"), space, []float64{1, 0}, 1024, mousa.PackingOriginal, o)
	if err != nil {
		t.Fatal(err)
	}
	got.Trail.Associated[0].FromItem = "successor"
	changed := rebindTokenRecord(t, got.Trail)
	commitV4Trail(t, f.store, changed)
	if _, err := f.store.GetSourceTrail(t.Context(), changed.ID); !IsCode(err, CodeIntegrity) || !strings.Contains(err.Error(), "declaring item contributed no selected") {
		t.Fatalf("semantic attribution: %v", err)
	}
	path := f.store.path
	f.store.Close()
	reopened, err := Open(t.Context(), path)
	if reopened != nil {
		reopened.Close()
	}
	if !IsCode(err, CodeIntegrity) {
		t.Fatalf("startup accepted resealed attribution: %v", err)
	}
}

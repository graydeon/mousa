package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func importSupersessionVectors(t *testing.T, f *v4Fixture) mousa.SHA256 {
	t.Helper()
	ctx := context.Background()
	exported, err := f.store.ExportEmbeddingInputs(ctx, testEvaluationRequest(t, f.source.ID, "export"))
	if err != nil {
		t.Fatal(err)
	}
	input := mousa.VectorImport{Schema: mousa.VectorImportSchema, Space: vectorSpace()}
	for _, p := range exported.Passages {
		if p.Item == "successor" {
			continue
		}
		values := []float64{0, 1}
		if p.Item == "predecessor" {
			values = []float64{1, 0}
		}
		if p.Item == "peer" {
			values = []float64{0.8, 0.6}
		}
		input.Embeddings = append(input.Embeddings, mousa.SuppliedEmbedding{EmbeddingInput: p.EmbeddingInput, Values: values})
	}
	space, err := f.store.ImportEmbeddings(ctx, f.source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	return space
}

func TestRetrievalSupersessionSurvivorsAndRecordedHistory(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"vector", "hybrid"} {
		for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
			t.Run(mode+"/"+packing, func(t *testing.T) {
				f := newV4Fixture(t)
				defer f.store.Close()
				space := importSupersessionVectors(t, f)
				serial := 0
				query := func(opted bool, budget uint64) TracedVectorResult {
					t.Helper()
					serial++
					request := testEvaluationRequest(t, f.source.ID, mode+packing+string(rune('a'+serial)))
					var got TracedVectorResult
					var err error
					if mode == "vector" {
						if opted {
							got, err = f.store.EvaluateAndTraceSupersessionVector(ctx, request, space, []float64{1, 0}, budget, packing)
						} else {
							got, err = f.store.EvaluateAndTraceVector(ctx, request, space, []float64{1, 0}, budget, packing)
						}
					} else {
						if opted {
							got, err = f.store.EvaluateAndTraceSupersessionHybrid(ctx, request, "predecessor", "original", space, []float64{1, 0}, budget, packing)
						} else {
							got, err = f.store.EvaluateAndTraceHybrid(ctx, request, "predecessor", "original", space, []float64{1, 0}, budget, packing)
						}
					}
					if err != nil {
						t.Fatal(err)
					}
					return got
				}
				ordinary := query(false, 31)
				none := query(true, 31)
				if none.Trail.Supersession.ActivationID != nil || !none.Trail.Supersession.Consulted || none.Trail.PacketID != ordinary.Trail.PacketID {
					t.Fatal("no-history changed packing")
				}
				declaration, active := f.activate(t)
				got := query(true, 31)
				if got.Trail.Schema != mousa.SourceTrailSchemaV9 || len(got.Trail.Supersession.Dispositions) != 1 || *got.Trail.Supersession.DeclarationID != declaration.ID {
					t.Fatalf("selection: %+v", got.Trail)
				}
				if !reflect.DeepEqual(got.Trail.Vector, ordinary.Trail.Vector) || !reflect.DeepEqual(got.Trail.Hybrid, ordinary.Trail.Hybrid) {
					t.Fatal("withholding changed producing ranks or coverage")
				}
				if ordinary.Passages[0].Item != "predecessor" || !ordinary.Trail.Candidates[0].Selected {
					t.Fatal("fixture predecessor must win")
				}
				retainedTwin := false
				for i, p := range got.Passages {
					row := got.Trail.Candidates[i]
					if p.Item == "predecessor" && (p.Text != "" || row.Selected || row.TextBytes != 31 || row.FinalRank != 1 || row.Omission != "") {
						t.Fatalf("withheld facts: %+v %+v", p, row)
					}
					if p.Item == "twin" && row.Selected {
						retainedTwin = true
					}
				}
				// Hybrid ranks the twin before the vector-only peer; vector ranks the peer
				// before the twin, so a one-passage budget intentionally chooses that peer.
				if mode == "hybrid" && !retainedTwin {
					t.Fatal("withheld twin seeded duplicate retention")
				}
				wide := query(true, 100)
				for i, p := range wide.Passages {
					if p.Item == "twin" && (!wide.Trail.Candidates[i].Selected || wide.Trail.Candidates[i].Omission != "") {
						t.Fatal("withheld copy displaced twin")
					}
				}
				if _, err := f.store.DeleteLocalItem(ctx, f.source.ID, "successor"); err != nil {
					t.Fatal(err)
				}
				unavailable := query(true, 31)
				if len(unavailable.Trail.Supersession.Dispositions) != 1 {
					t.Fatal("unavailable successor restored predecessor")
				}
				saved, err := mousa.EncodeSourceTrail(got.Trail)
				if err != nil {
					t.Fatal(err)
				}
				transition := activationTestTransition(t, f.source.ID, active.CurrentActivationID, mousa.SupersessionDeclarationID{}, "deactivate", activationBaseUsec+1)
				if err := f.store.ApplySupersessionActivation(ctx, transition); err != nil {
					t.Fatal(err)
				}
				deactivated := query(true, 31)
				if deactivated.Trail.Supersession.DeclarationID != nil || deactivated.Trail.Supersession.ActivationID == nil || deactivated.Trail.PacketID != ordinary.Trail.PacketID {
					t.Fatal("deactivation not recorded")
				}
				reactivation := activationTestTransition(t, f.source.ID, transition.ID, declaration.ID, "reactivate", activationBaseUsec+2)
				if err := f.store.ApplySupersessionActivation(ctx, reactivation); err != nil {
					t.Fatal(err)
				}
				seedLocalItem(t, f.store, f.source, "predecessor", "predecessor revised guidance")
				current, err := f.store.ExportEmbeddingInputs(ctx, testEvaluationRequest(t, f.source.ID, "revised-input"))
				if err != nil {
					t.Fatal(err)
				}
				for _, passage := range current.Passages {
					if passage.Item == "predecessor" {
						input := mousa.VectorImport{Schema: mousa.VectorImportSchema, Space: vectorSpace(), Embeddings: []mousa.SuppliedEmbedding{{EmbeddingInput: passage.EmbeddingInput, Values: []float64{1, 0}}}}
						if _, err := f.store.ImportEmbeddings(ctx, f.source.ID, input); err != nil {
							t.Fatal(err)
						}
					}
				}
				revised := query(true, 100)
				if len(revised.Trail.Supersession.Dispositions) != 0 {
					t.Fatal("old declaration matched a new predecessor revision")
				}
				path := f.store.path
				if err := f.store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				historical, err := reopened.GetSourceTrail(ctx, got.Trail.ID)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := mousa.EncodeSourceTrail(historical)
				if err != nil || string(encoded) != string(saved) {
					t.Fatal("history reinterpreted later state")
				}
				inspection, err := reopened.InspectSourceTrail(ctx, testEvaluationRequest(t, f.source.ID, "inspect"), got.Trail.ID)
				if err != nil || !reflect.DeepEqual(inspection.Historical.Supersession, got.Trail.Supersession) {
					t.Fatalf("inspection: %+v %v", inspection, err)
				}
			})
		}
	}
}

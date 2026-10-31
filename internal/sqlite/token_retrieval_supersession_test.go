package sqlite

import (
	"context"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func queryTokenRetrieval(t *testing.T, f *v4Fixture, space mousa.SHA256, mode, packing, name string, bytes uint64, options TokenQuery) (TracedVectorResult, error) {
	t.Helper()
	request := testEvaluationRequest(t, f.source.ID, name)
	if mode == "vector" {
		return f.store.EvaluateAndTraceTokenVector(t.Context(), request, space, []float64{1, 0}, bytes, packing, options)
	}
	return f.store.EvaluateAndTraceTokenHybrid(t.Context(), request, "predecessor", "original", space, []float64{1, 0}, bytes, packing, options)
}

func TestTokenRetrievalSupersessionSurvivors(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"vector", "hybrid"} {
		for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
			t.Run(mode+"/"+packing, func(t *testing.T) {
				t.Parallel()
				f := newV4Fixture(t)
				defer f.store.Close()
				space := importSupersessionVectors(t, f)
				serial := 0
				query := func(enforce bool, bytes uint64, budget mousa.TokenBudget) TracedVectorResult {
					t.Helper()
					serial++
					options := tokenOptions(t)
					options.Supersession, options.Budget = enforce, budget
					got, err := queryTokenRetrieval(t, f, space, mode, packing, fmt.Sprint(serial), bytes, options)
					if err != nil {
						t.Fatal(err)
					}
					if got.Trail.Token.RenderedSHA256 != mousa.SHA256(sha256.Sum256([]byte(got.Context))) {
						t.Fatal("returned rendering digest")
					}
					return got
				}
				ordinary := query(false, 31, mousa.TokenBudget{Cap: 1000})
				none := query(true, 31, mousa.TokenBudget{Cap: 1000})
				if none.Context != ordinary.Context || none.Trail.Supersession.ActivationID != nil || none.Trail.PacketID == ordinary.Trail.PacketID {
					t.Fatal("no-history selection/packet binding")
				}
				declaration, state := f.activate(t)
				enforced := query(true, 31, mousa.TokenBudget{Cap: 1000})
				if enforced.Trail.Schema != "mousa.source_trail.v10" || len(enforced.Trail.Supersession.Dispositions) != 1 {
					t.Fatalf("combined selection: %+v", enforced.Trail)
				}
				if !reflect.DeepEqual(ordinary.Trail.Vector, enforced.Trail.Vector) || !reflect.DeepEqual(ordinary.Trail.Hybrid, enforced.Trail.Hybrid) {
					t.Fatal("suppression changed producing ranks or coverage")
				}
				if !ordinary.Trail.Candidates[0].Selected || enforced.Trail.Candidates[0].Selected || enforced.Trail.Token.ProposedTokens[0] != nil || enforced.Trail.Candidates[0].TextBytes != 31 {
					t.Fatal("withheld predecessor entered token packing or lost canonical size")
				}
				if enforced.Context == "" || enforced.Context == ordinary.Context || enforced.Trail.UsedBytes == 0 {
					t.Fatal("later survivor did not replace predecessor")
				}
				for i, p := range enforced.Passages {
					if p.Item == "predecessor" && (p.Text != "" || enforced.Verified[i].Text != "") {
						t.Fatal("withheld bytes escaped")
					}
				}
				count := enforced.Trail.Token.MeasuredTokens
				exact := query(true, 31, mousa.TokenBudget{Cap: count})
				if exact.Context != enforced.Context {
					t.Fatal("exact fit changed context")
				}
				short := query(true, 31, mousa.TokenBudget{Cap: count - 1})
				if short.Context == enforced.Context {
					t.Fatal("one-token-short retained full context")
				}
				reserved := query(true, 31, mousa.TokenBudget{Cap: 1000, Window: count + 12, PromptReserve: 5, OutputReserve: 7})
				if reserved.Context != enforced.Context || reserved.Trail.Token.Limit != count {
					t.Fatal("reservation arithmetic")
				}
				zero := query(true, 31, mousa.TokenBudget{Cap: 1000, Window: 12, PromptReserve: 5, OutputReserve: 7})
				if zero.Context != "" || zero.Trail.Token.MeasuredTokens != 0 || zero.Trail.Token.RenderedBytes != 0 {
					t.Fatal("zero allowance released text")
				}
				pressure := query(true, 1, mousa.TokenBudget{Cap: 1000})
				for i, row := range pressure.Trail.Candidates {
					if i != 0 && (row.Omission != "bytes" || pressure.Trail.Token.ProposedTokens[i] != nil) {
						t.Fatal("byte pressure counted rejected proposal")
					}
				}
				wide := query(true, 1024, mousa.TokenBudget{Cap: 1000})
				for i, p := range wide.Passages {
					if p.Item == "twin" && (!wide.Trail.Candidates[i].Selected || wide.Trail.Candidates[i].Omission != "") {
						t.Fatal("withheld twin seeded deduplication")
					}
				}
				saved, err := mousa.EncodeSourceTrail(enforced.Trail)
				if err != nil {
					t.Fatal(err)
				}
				inspection, err := f.store.InspectSourceTrailWithTokens(t.Context(), testEvaluationRequest(t, f.source.ID, "recount"), enforced.Trail.ID, tokenOptions(t).Counter)
				if err != nil || inspection.Historical.TokenVerification != "official_recount_verified" {
					t.Fatalf("recount: %+v %v", inspection, err)
				}
				if _, err := f.store.DeleteLocalItem(t.Context(), f.source.ID, "successor"); err != nil {
					t.Fatal(err)
				}
				unavailable := query(true, 31, mousa.TokenBudget{Cap: 1000})
				if len(unavailable.Trail.Supersession.Dispositions) != 1 || unavailable.Context != enforced.Context {
					t.Fatal("missing successor restored predecessor")
				}
				transition := activationTestTransition(t, f.source.ID, state.CurrentActivationID, mousa.SupersessionDeclarationID{}, "deactivate", activationBaseUsec+1)
				if err := f.store.ApplySupersessionActivation(t.Context(), transition); err != nil {
					t.Fatal(err)
				}
				inactive := query(true, 31, mousa.TokenBudget{Cap: 1000})
				if inactive.Context != ordinary.Context || inactive.Trail.Supersession.DeclarationID != nil || inactive.Trail.Supersession.ActivationID == nil {
					t.Fatal("deactivation not recorded")
				}
				reactivate := activationTestTransition(t, f.source.ID, transition.ID, declaration.ID, "reactivate", activationBaseUsec+2)
				if err := f.store.ApplySupersessionActivation(t.Context(), reactivate); err != nil {
					t.Fatal(err)
				}
				seedLocalItem(t, f.store, f.source, "predecessor", "predecessor revised guidance")
				current, err := f.store.ExportEmbeddingInputs(t.Context(), testEvaluationRequest(t, f.source.ID, "revised-inputs"))
				if err != nil {
					t.Fatal(err)
				}
				for _, passage := range current.Passages {
					if passage.Item == "predecessor" {
						input := mousa.VectorImport{Schema: mousa.VectorImportSchema, Space: vectorSpace(), Embeddings: []mousa.SuppliedEmbedding{{EmbeddingInput: passage.EmbeddingInput, Values: []float64{1, 0}}}}
						if _, err := f.store.ImportEmbeddings(t.Context(), f.source.ID, input); err != nil {
							t.Fatal(err)
						}
					}
				}
				revised := query(true, 1024, mousa.TokenBudget{Cap: 1000})
				if len(revised.Trail.Supersession.Dispositions) != 0 || !strings.Contains(revised.Context, "predecessor revised guidance") {
					t.Fatal("changed exact predecessor pin still matched")
				}
				path := f.store.path
				f.store.Close()
				reopened, err := Open(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				history, err := reopened.GetSourceTrail(t.Context(), enforced.Trail.ID)
				if err != nil {
					t.Fatal(err)
				}
				after, err := mousa.EncodeSourceTrail(history)
				if err != nil || string(after) != string(saved) {
					t.Fatal("restart reinterpreted producing history")
				}
			})
		}
	}
}

func TestTokenRetrievalSupersessionIntegrity(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"vector", "hybrid"} {
		for _, damage := range []string{"missing-membership", "extra-membership", "wrong-pin", "missing-event", "text", "segment", "representation"} {
			t.Run(mode+"/"+damage, func(t *testing.T) {
				t.Parallel()
				f := newV4Fixture(t)
				defer f.store.Close()
				space := importSupersessionVectors(t, f)
				f.activate(t)
				options := tokenOptions(t)
				options.Supersession = true
				got, err := queryTokenRetrieval(t, f, space, mode, mousa.PackingOriginal, "producing", 1, options)
				if err != nil {
					t.Fatal(err)
				}
				trail := got.Trail
				reason := "supersession"
				switch damage {
				case "missing-membership":
					trail.Supersession.Dispositions = []mousa.SupersessionDisposition{}
					trail.Candidates[0].Omission = "bytes"
				case "extra-membership":
					extra := trail.Supersession.Dispositions[0]
					extra.SegmentID, extra.ContentSHA256 = trail.Candidates[1].SegmentID, trail.Candidates[1].ContentSHA256
					trail.Candidates[1].Omission = ""
					trail.Supersession.Dispositions = append(trail.Supersession.Dispositions, extra)
				case "wrong-pin":
					trail.Supersession.Dispositions[0].SuccessorItemID = "peer"
				case "missing-event":
					id := mousa.SupersessionActivationID{99}
					trail.Supersession.ActivationID = &id
				case "text", "segment", "representation":
					statement, victim := `UPDATE segment_lexical_fts SET text='changed bytes' WHERE rowid=(SELECT rowid FROM segment_lexical_rows WHERE segment_id=?)`, trail.Candidates[0].SegmentID[:]
					reason = "text"
					if damage == "segment" {
						statement, reason = "DELETE FROM segments WHERE id=?", "segment"
					}
					if damage == "representation" {
						statement, victim, reason = "DELETE FROM representations WHERE id=?", f.predecessor.ID[:], "representation"
					}
					damageCanonicalParent(t, f.store, statement, victim)
				}
				if damage != "text" && damage != "segment" && damage != "representation" {
					trail = rebindTokenRecord(t, trail)
					encoded, err := mousa.EncodeSourceTrail(trail)
					if err != nil {
						t.Fatalf("structural prerequisite: %v", err)
					}
					if _, err := mousa.DecodeSourceTrail(encoded); err != nil {
						t.Fatal(err)
					}
					commitV4Trail(t, f.store, trail)
				}
				_, err = f.store.GetSourceTrail(t.Context(), trail.ID)
				if !IsCode(err, CodeIntegrity) || !strings.Contains(err.Error(), reason) {
					t.Fatalf("intended %s failure: %v", reason, err)
				}
				path := f.store.path
				f.store.Close()
				reopened, err := OpenReadOnly(t.Context(), path)
				if reopened != nil {
					reopened.Close()
				}
				if !IsCode(err, CodeIntegrity) {
					t.Fatalf("startup accepted damage: %v", err)
				}
			})
		}
	}
}

func TestTokenRetrievalSupersessionFailuresAndDenial(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"vector", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newV4Fixture(t)
			defer f.store.Close()
			space := importSupersessionVectors(t, f)
			f.activate(t)
			options := tokenOptions(t)
			options.Supersession = true
			options.Counter.Close()
			before := freshAuditCounts(t, f.store)
			_, err := queryTokenRetrieval(t, f, space, mode, mousa.PackingExactV1, "failed-counter", 1024, options)
			if err == nil || !reflect.DeepEqual(before, freshAuditCounts(t, f.store)) {
				t.Fatal("counter failure committed", err)
			}
			_, rep, text := prepareLocalRevision(t, f.store, "predecessor", strings.Repeat("x", mousa.MaxVectorTextBytes+1))
			if _, err := f.store.ActivateLocalItem(t.Context(), f.source.ID, "predecessor", rep.ID, text); err != nil {
				t.Fatal(err)
			}
			options = tokenOptions(t)
			options.Supersession = true
			_, err = queryTokenRetrieval(t, f, space, mode, mousa.PackingOriginal, "source-bound", 1024, options)
			if !IsCode(err, CodeResourceLimit) || !reflect.DeepEqual(before, freshAuditCounts(t, f.store)) {
				t.Fatal("source admission bypassed or committed", err)
			}
			if _, err := f.store.db.ExecContext(t.Context(), `UPDATE supersession_activation_state SET active_declaration_id=NULL`); err != nil {
				t.Fatal(err)
			}
			withdrawVerifiedSource(t, f.store, f.source.ID, "withdraw", 3)
			options = tokenOptions(t)
			options.Supersession = true
			denied, err := queryTokenRetrieval(t, f, space, mode, mousa.PackingOriginal, "deny", 1024, options)
			if err != nil || denied.Context != "" || denied.Trail.Supersession.Consulted || len(denied.Trail.Candidates) != 0 || denied.Trail.Token.MeasuredTokens != 0 {
				t.Fatalf("denial consulted source/administration: %+v %v", denied, err)
			}
			hidden, err := f.store.InspectSourceTrail(context.Background(), testEvaluationRequest(t, f.source.ID, "hidden"), denied.Trail.ID)
			if err != nil || hidden.Historical != nil {
				t.Fatal("denied inspection leaked history", err)
			}
		})
	}
}

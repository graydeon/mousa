package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func querySupersessionMode(ctx context.Context, store *Store, mode string, request mousa.PolicyEvaluationRequest, space mousa.SHA256, budget uint64) (TracedVectorResult, error) {
	if mode == "vector" {
		return store.EvaluateAndTraceSupersessionVector(ctx, request, space, []float64{1, 0}, budget, mousa.PackingOriginal)
	}
	return store.EvaluateAndTraceSupersessionHybrid(ctx, request, "predecessor", "original", space, []float64{1, 0}, budget, mousa.PackingOriginal)
}

func TestRetrievalSupersessionRecordedMembershipAndMissingParents(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"vector", "hybrid"} {
		for _, damage := range []string{"missing-membership", "wrong-successor", "missing-event"} {
			t.Run(mode+"/"+damage, func(t *testing.T) {
				f := newV4Fixture(t)
				defer f.store.Close()
				space := importSupersessionVectors(t, f)
				f.activate(t)
				got, err := querySupersessionMode(ctx, f.store, mode, testEvaluationRequest(t, f.source.ID, "producing"), space, 1)
				if err != nil {
					t.Fatal(err)
				}
				changed := got.Trail
				switch damage {
				case "missing-event":
					missing := mousa.SupersessionActivationID{99}
					changed.Supersession.ActivationID = &missing
				case "missing-membership":
					changed.Supersession.Dispositions = []mousa.SupersessionDisposition{}
				case "wrong-successor":
					changed.Supersession.Dispositions[0].SuccessorItemID = "peer"
				}
				changed = resealV4Trail(t, changed)
				if err := changed.Validate(); err != nil {
					t.Fatalf("mutation failed structural prerequisite: %v", err)
				}
				commitV4Trail(t, f.store, changed)
				got.Trail = changed
				if _, err := f.store.GetSourceTrail(ctx, got.Trail.ID); !IsCode(err, CodeIntegrity) {
					t.Fatalf("canonical read: %v", err)
				}
				path := f.store.path
				if err := f.store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := Open(ctx, path)
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

func TestRetrievalSupersessionDenialSkipsConsultationAndAllowRollsBack(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"vector", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			space := importSupersessionVectors(t, f)
			f.activate(t)
			if _, err := f.store.db.ExecContext(ctx, `UPDATE supersession_activation_state SET active_declaration_id=NULL`); err != nil {
				t.Fatal(err)
			}
			var before, after int
			if err := f.store.db.QueryRowContext(ctx, `SELECT count(*) FROM policy_decisions`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			_, err := querySupersessionMode(ctx, f.store, mode, testEvaluationRequest(t, f.source.ID, "damaged-allow"), space, 31)
			if !IsCode(err, CodeIntegrity) {
				t.Fatalf("damaged allow: %v", err)
			}
			if err := f.store.db.QueryRowContext(ctx, `SELECT count(*) FROM policy_decisions`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("failed producing transaction committed audit state")
			}
			withdrawVerifiedSource(t, f.store, f.source.ID, "withdraw", 3)
			denied, err := querySupersessionMode(ctx, f.store, mode, testEvaluationRequest(t, f.source.ID, "damaged-deny"), space, 31)
			if err != nil || denied.Decision.Outcome != mousa.PolicyOutcomeDeny || denied.Trail.Supersession.Consulted || len(denied.Trail.Candidates) != 0 || len(denied.Passages) != 0 {
				t.Fatalf("denial consulted administration: %+v %v", denied, err)
			}
		})
	}
}

func TestRetrievalSupersessionSourceAdmissionPrecedesMask(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"vector", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			space := importSupersessionVectors(t, f)
			// Even a declaration withholding all these bytes cannot bypass source admission.
			_, rep, text := prepareLocalRevision(t, f.store, "predecessor", strings.Repeat("x", mousa.MaxVectorTextBytes+1))
			if _, err := f.store.ActivateLocalItem(ctx, f.source.ID, "predecessor", rep.ID, text); err != nil {
				t.Fatal(err)
			}
			f.predecessor = rep
			f.activate(t)
			var before, after int
			if err := f.store.db.QueryRowContext(ctx, `SELECT count(*) FROM source_trails`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			_, err := querySupersessionMode(ctx, f.store, mode, testEvaluationRequest(t, f.source.ID, "oversized"), space, 31)
			if !IsCode(err, CodeResourceLimit) {
				t.Fatalf("masked source bypassed bound: %v", err)
			}
			if err := f.store.db.QueryRowContext(ctx, `SELECT count(*) FROM source_trails`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("resource refusal committed a trail")
			}
		})
	}
}

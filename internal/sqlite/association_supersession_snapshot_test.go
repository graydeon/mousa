package sqlite

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestAssociatedSupersessionResolutionUsesSuppliedSnapshot(t *testing.T) {
	t.Parallel()
	f := newV4Fixture(t)
	_, state := f.activate(t)
	input := f.retrieve(t, "snapshot-primary")
	other, err := Open(t.Context(), f.store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := f.store.db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	consultation, err := readSupersessionConsultation(t.Context(), tx, f.source.ID)
	if err != nil {
		t.Fatal(err)
	}
	primary, err := mousa.NewSourceTrailWithSupersession(input.request, input.decision, "sharedterm", input.candidates, 1024, mousa.PackingOriginal, consultation.state, consultation.declaration)
	if err != nil {
		t.Fatal(err)
	}
	// Commit a new target revision and activation after the reader established its snapshot.
	_, revision, text := prepareLocalRevision(t, other, "predecessor", "later target bytes")
	if _, err := other.ActivateLocalItem(t.Context(), f.source.ID, "predecessor", revision.ID, text); err != nil {
		t.Fatal(err)
	}
	off := activationTestTransition(t, f.source.ID, state.CurrentActivationID, mousa.SupersessionDeclarationID{}, "later deactivation", activationBaseUsec+1)
	if err := other.ApplySupersessionActivation(t.Context(), off); err != nil {
		t.Fatal(err)
	}
	ds := []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "predecessor", Basis: "qualification", Author: "test"}}
	passages, omissions, err := resolveAssociatedPassages(t.Context(), tx, f.source.ID, ds, input.candidates, primary)
	if err != nil || len(passages) != 1 || passages[0].Segment.RepresentationID != f.predecessor.ID {
		t.Fatalf("mixed target snapshot: %+v %v", passages, err)
	}
	composed, err := mousa.AppendSupersessionAssociatedPassages(primary, input.candidates, passages, omissions, consultation.declaration)
	if err != nil || composed.Associated[0].Omission != "superseded" {
		t.Fatalf("mixed consultation: %+v %v", composed, err)
	}
	if err := verifyTrailContent(t.Context(), tx, composed, f.source.ID); err != nil {
		t.Fatal(err)
	}
}

func TestAssociatedSupersessionMissingParentAndStartup(t *testing.T) {
	t.Parallel()
	for _, parent := range []string{"segment", "representation"} {
		t.Run(parent, func(t *testing.T) {
			f := newV4Fixture(t)
			f.activate(t)
			ds := []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "predecessor", Basis: "qualification", Author: "test"}}
			r, err := f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "parent"), "peer", 10, 1024, mousa.PackingOriginal, ds)
			if err != nil {
				t.Fatal(err)
			}
			conn, err := f.store.db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), "PRAGMA foreign_keys=OFF"); err != nil {
				t.Fatal(err)
			}
			statement, victim := "DELETE FROM segments WHERE id = ?", r.Trail.Associated[0].SegmentID[:]
			if parent == "representation" {
				statement, victim = "DELETE FROM representations WHERE id = ?", f.predecessor.ID[:]
			}
			if _, err := conn.ExecContext(t.Context(), statement, victim); err != nil {
				t.Fatal(err)
			}
			conn.Close()
			if _, err := f.store.GetSourceTrail(t.Context(), r.Trail.ID); !IsCode(err, CodeIntegrity) {
				t.Fatalf("missing named parent: %v", err)
			}
			if _, err := f.store.GetSourceTrail(t.Context(), mousa.SourceTrailID{99}); !IsCode(err, CodeNotFound) {
				t.Fatalf("unknown identity: %v", err)
			}
			path := f.store.path
			if err := f.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(context.Background(), path)
			if reopened != nil {
				reopened.Close()
			}
			if !IsCode(err, CodeIntegrity) {
				t.Fatalf("startup accepted damage: %v", err)
			}
		})
	}
}

func TestAssociatedSupersessionWithdrawalSkipsDamagedConsultation(t *testing.T) {
	t.Parallel()
	f := newV4Fixture(t)
	f.activate(t)
	ds := []mousa.AssociationDeclaration{{FromItem: "peer", ToItem: "predecessor", Basis: "qualification", Author: "test"}}
	if _, err := f.store.db.ExecContext(t.Context(), `UPDATE supersession_activation_state SET active_declaration_id=NULL`); err != nil {
		t.Fatal(err)
	}
	before := freshAuditCounts(t, f.store)
	_, err := f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "damaged-allow"), "peer", 10, 1024, mousa.PackingOriginal, ds)
	if !IsCode(err, CodeIntegrity) || !reflect.DeepEqual(freshAuditCounts(t, f.store), before) {
		t.Fatalf("damaged allow did not roll back: %v", err)
	}
	withdrawVerifiedSource(t, f.store, f.source.ID, "withdraw-composed", 3)
	denied, err := f.store.EvaluateAndTraceAssociatedSupersessionLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "withdrawn"), "peer", 10, 1024, mousa.PackingOriginal, ds)
	if err != nil || denied.Decision.Outcome != mousa.PolicyOutcomeDeny || denied.Trail.Supersession.Consulted || len(denied.Trail.Associated) != 0 || len(denied.Candidates) != 0 || denied.Trail.Schema != mousa.SourceTrailSchemaV4 {
		t.Fatalf("withdrawal consulted or expanded: %+v %v", denied, err)
	}
}

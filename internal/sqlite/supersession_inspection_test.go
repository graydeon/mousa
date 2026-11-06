package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestInspectSupersessionLifecycleAndFreshAuthorization(t *testing.T) {
	f := newV4Fixture(t)
	defer f.store.Close()
	inspect := func(name, state string, activation *mousa.SupersessionActivation, declaration *mousa.SupersessionDeclaration) {
		t.Helper()
		before := canonicalRecordDigest(t, f.store)
		counts := freshAuditCounts(t, f.store)
		request := testEvaluationRequest(t, f.source.ID, name)
		got, err := f.store.InspectSupersession(t.Context(), request)
		want := &SupersessionCurrent{State: state, Activation: activation, Declaration: declaration}
		if err != nil || got.Decision.Outcome != mousa.PolicyOutcomeAllow || !reflect.DeepEqual(got.Current, want) {
			t.Fatalf("inspection = %#v, %v; want %#v", got, err, want)
		}
		encoded, err := json.Marshal(got.Current)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil || len(fields) != 3 || fields["activation"] == nil || fields["declaration"] == nil {
			t.Fatalf("required null members missing: %s, %v", encoded, err)
		}
		after := freshAuditCounts(t, f.store)
		if after["policy_decisions"] != counts["policy_decisions"]+1 {
			t.Fatal("inspection did not persist fresh decision")
		}
		for _, table := range []string{"source_trails", "source_trail_candidates", "supersession_activations", "supersession_activation_state", "supersession_declarations"} {
			if after[table] != counts[table] {
				t.Fatalf("inspection mutated %s", table)
			}
		}
		if canonicalRecordDigest(t, f.store) != before {
			t.Fatal("inspection mutated canonical evidence")
		}
		if _, err := f.store.InspectSupersession(t.Context(), request); !IsCode(err, CodeConflict) {
			t.Fatalf("replayed authorization = %v", err)
		}
	}
	inspect("empty", "no_history", nil, nil)
	declaration, state := f.activate(t)
	initial, err := f.store.GetSupersessionActivation(t.Context(), state.CurrentActivationID)
	if err != nil {
		t.Fatal(err)
	}
	inspect("active", "active", &initial, &declaration)
	deactivation := activationTestTransition(t, f.source.ID, initial.ID, mousa.SupersessionDeclarationID{}, "stop", activationBaseUsec+1)
	if err := f.store.ApplySupersessionActivation(t.Context(), deactivation); err != nil {
		t.Fatal(err)
	}
	inspect("deactivated", "deactivated", &deactivation, nil)
	reactivation := activationTestTransition(t, f.source.ID, deactivation.ID, declaration.ID, "resume", activationBaseUsec+2)
	if err := f.store.ApplySupersessionActivation(t.Context(), reactivation); err != nil {
		t.Fatal(err)
	}
	inspect("reactivated", "active", &reactivation, &declaration)
}

func TestInspectSupersessionDenialMasksHistoryAndDamage(t *testing.T) {
	for _, history := range []string{"none", "active", "deactivated", "damaged"} {
		t.Run(history, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			if history != "none" {
				_, state := f.activate(t)
				if history == "deactivated" {
					event := activationTestTransition(t, f.source.ID, state.CurrentActivationID, mousa.SupersessionDeclarationID{}, "private reason", activationBaseUsec+1)
					if err := f.store.ApplySupersessionActivation(t.Context(), event); err != nil {
						t.Fatal(err)
					}
				}
				if history == "damaged" {
					if _, err := f.store.db.Exec(`DELETE FROM supersession_activation_state`); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, deniedBy := range []string{"caller", "withdrawal"} {
				request := testEvaluationRequest(t, f.source.ID, deniedBy)
				if deniedBy == "caller" {
					request.ExternalCallerID = "not-authorized"
					var err error
					request.ID, err = mousa.NewPolicyEvaluationRequestID(request)
					if err != nil {
						t.Fatal(err)
					}
				} else {
					withdrawVerifiedSource(t, f.store, f.source.ID, "withdraw", 500)
				}
				before := freshAuditCounts(t, f.store)
				got, err := f.store.InspectSupersession(t.Context(), request)
				if err != nil || got.Decision.Outcome != mousa.PolicyOutcomeDeny || got.Current != nil {
					t.Fatalf("hidden %s leaked through %s: %#v, %v", history, deniedBy, got, err)
				}
				after := freshAuditCounts(t, f.store)
				for _, table := range []string{"supersession_activations", "supersession_activation_state", "supersession_declarations"} {
					if before[table] != after[table] {
						t.Fatalf("denial repaired %s", table)
					}
				}
			}
		})
	}
}

func TestInspectSupersessionCorruptionRollsBackReceipt(t *testing.T) {
	for _, damage := range []string{
		`DELETE FROM supersession_activation_state`,
		`UPDATE supersession_activations SET record_json = CAST(record_json || ' ' AS BLOB)`,
		`UPDATE supersession_declarations SET record_json = CAST(record_json || ' ' AS BLOB)`,
	} {
		t.Run(damage, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			f.activate(t)
			if _, err := f.store.db.Exec(damage); err != nil {
				t.Fatal(err)
			}
			before := freshAuditCounts(t, f.store)
			got, err := f.store.InspectSupersession(t.Context(), testEvaluationRequest(t, f.source.ID, "damaged"))
			if !IsCode(err, CodeIntegrity) || got.Current != nil || !reflect.DeepEqual(before, freshAuditCounts(t, f.store)) {
				t.Fatalf("corruption became absence or committed receipt: %#v, %v", got, err)
			}
		})
	}
}

func TestInspectSupersessionOwnsWriterSnapshot(t *testing.T) {
	f := newV4Fixture(t)
	defer f.store.Close()
	declaration, state := f.activate(t)
	other, err := Open(t.Context(), f.store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := f.store.InspectSupersession(ctx, testEvaluationRequest(t, f.source.ID, "blocked")); err == nil {
		t.Fatal("inspection bypassed owning writer transaction")
	}
	if _, err := conn.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	round := make(chan struct{})
	transitioned := make(chan struct{}, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(transitioned)
		expected := state.CurrentActivationID
		for i := range 10 {
			if _, ok := <-round; !ok {
				return
			}
			selected := mousa.SupersessionDeclarationID{}
			if i%2 == 1 {
				selected = declaration.ID
			}
			event := activationTestTransition(t, f.source.ID, expected, selected, "transition", activationBaseUsec+int64(i)+1)
			if err := other.ApplySupersessionActivation(t.Context(), event); err != nil {
				errors <- err
				return
			}
			expected = event.ID
			transitioned <- struct{}{}
		}
	}()
	for i := range 10 {
		round <- struct{}{}
		got, err := f.store.InspectSupersession(t.Context(), testEvaluationRequest(t, f.source.ID, fmt.Sprintf("concurrent-%d", i)))
		if err != nil {
			errors <- err
			break
		}
		if got.Current == nil || got.Current.Activation == nil || (got.Current.Activation.DeclarationID == nil) != (got.Current.Declaration == nil) {
			errors <- fmt.Errorf("mixed inspection snapshot: %#v", got.Current)
			break
		}
		if got.Current.Declaration != nil && *got.Current.Activation.DeclarationID != got.Current.Declaration.ID {
			errors <- fmt.Errorf("activation/declaration snapshot mismatch")
			break
		}
		if _, ok := <-transitioned; !ok {
			break
		}
	}
	close(round)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func TestInspectSupersessionPreservesHistoricalAndNeverActivatedPins(t *testing.T) {
	f := newV4Fixture(t)
	defer f.store.Close()
	_, replacement, text := prepareLocalRevision(t, f.store, "predecessor", "A later revision.")
	if _, err := f.store.ActivateLocalItem(t.Context(), f.source.ID, "predecessor", replacement.ID, text); err != nil {
		t.Fatal(err)
	}
	_, neverActivated, _ := prepareLocalRevision(t, f.store, "unpublished", "Canonical but never activated.")
	declaration := supersessionTestDeclaration(t, f.source.ID, "predecessor", f.predecessor.ID, "unpublished", neverActivated.ID, "operator", "historical pins")
	if err := f.store.PutSupersessionDeclaration(t.Context(), declaration); err != nil {
		t.Fatal(err)
	}
	event := activationTestTransition(t, f.source.ID, mousa.SupersessionActivationID{}, declaration.ID, "select historical pins", activationBaseUsec)
	if err := f.store.ApplySupersessionActivation(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.InspectSupersession(t.Context(), testEvaluationRequest(t, f.source.ID, "historical-pins"))
	if err != nil || got.Current == nil || !reflect.DeepEqual(got.Current.Declaration, &declaration) || !reflect.DeepEqual(got.Current.Activation, &event) {
		t.Fatalf("inspection replaced or rejected historical pins: %#v, %v", got, err)
	}
}

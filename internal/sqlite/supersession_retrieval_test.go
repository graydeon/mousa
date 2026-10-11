package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func freshSupersession(t *testing.T, fixture *v4Fixture, name, expression, policy string, limit int, budget uint64) TracedLexicalResult {
	t.Helper()
	result, err := fixture.store.EvaluateAndTraceSupersessionLexical(context.Background(), testEvaluationRequest(t, fixture.source.ID, name), expression, limit, budget, policy)
	if err != nil {
		t.Fatalf("EvaluateAndTraceSupersessionLexical: %v", err)
	}
	if result.Trail.Schema != mousa.SourceTrailSchemaV4 || result.Trail.PackingPolicy != policy || result.Trail.Supersession == nil {
		t.Fatalf("opt-in trail = %#v", result.Trail)
	}
	read, err := fixture.store.GetSourceTrail(context.Background(), result.Trail.ID)
	if err != nil || !reflect.DeepEqual(read, result.Trail) {
		t.Fatalf("canonical read = %#v, %v; want %#v", read, err, result.Trail)
	}
	for index, candidate := range result.Candidates {
		row := result.Trail.Candidates[index]
		withheld := false
		for _, disposition := range result.Trail.Supersession.Dispositions {
			withheld = withheld || disposition.SegmentID == candidate.Segment.ID
		}
		if candidate.Segment.ID != row.SegmentID || candidate.FinalRank != row.FinalRank || candidate.Disposition != row.Disposition || candidate.Segment.ContentSHA256 != row.ContentSHA256 || len(candidate.Paths) == 0 {
			t.Fatalf("candidate metadata changed: %#v / %#v", candidate, row)
		}
		if withheld {
			if candidate.Text != "" || row.TextBytes == 0 || row.Selected || row.Disposition != mousa.CandidateAccepted || row.Omission != "" || row.DuplicateOf != "" {
				t.Fatalf("withheld candidate leaked or changed canonical metadata: %#v / %#v", candidate, row)
			}
		} else if candidate.Disposition == mousa.CandidateAccepted && uint64(len(candidate.Text)) != row.TextBytes {
			t.Fatalf("survivor text changed: %#v / %#v", candidate, row)
		}
	}
	return result
}

func TestSupersessionFreshDefaultNoHistoryAndActive(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(policy, func(t *testing.T) {
			fixture := newV4Fixture(t)
			defer fixture.store.Close()
			ctx := context.Background()
			baseline, err := fixture.store.EvaluateAndTraceLexical(ctx, testEvaluationRequest(t, fixture.source.ID, "default"), "sharedterm", 10, 1<<20, policy)
			if err != nil || baseline.Trail.Supersession != nil || baseline.Trail.Schema == mousa.SourceTrailSchemaV4 {
				t.Fatalf("default = %#v, %v", baseline, err)
			}
			noHistory := freshSupersession(t, fixture, "no-history", "sharedterm", policy, 10, 1<<20)
			if !noHistory.Trail.Supersession.Consulted || noHistory.Trail.Supersession.ActivationID != nil || noHistory.Trail.Supersession.DeclarationID != nil || len(noHistory.Trail.Supersession.Dispositions) != 0 || noHistory.Trail.PacketID != baseline.Trail.PacketID || noHistory.Trail.ID == baseline.Trail.ID {
				t.Fatalf("no history changed selection or failed versioning: %#v", noHistory.Trail)
			}
			declaration, state := fixture.activate(t)
			active := freshSupersession(t, fixture, "active", "sharedterm", policy, 10, 1<<20)
			member := active.Trail.Supersession
			if member.ActivationID == nil || *member.ActivationID != state.CurrentActivationID || member.DeclarationID == nil || *member.DeclarationID != declaration.ID || len(member.Dispositions) != 1 {
				t.Fatalf("active consultation = %#v", member)
			}
			selected := 0
			for index, row := range active.Trail.Candidates {
				if row.Selected {
					selected++
				}
				if active.Candidates[index].Segment.RepresentationID == fixture.twin.ID && !row.Selected {
					t.Fatal("withheld predecessor incorrectly seeded duplicate retention")
				}
				candidate := active.Candidates[index]
				candidate.Text = baseline.Candidates[index].Text
				if !reflect.DeepEqual(candidate, baseline.Candidates[index]) {
					t.Fatal("supersession changed verified ranks, ancestry or candidate metadata")
				}
			}
			if len(active.Candidates) != 4 || selected != 3 {
				t.Fatalf("active considered=%d selected=%d, want 4 / 3", len(active.Candidates), selected)
			}
			if _, err := fixture.store.EvaluateAndTraceSupersessionLexical(ctx, testEvaluationRequest(t, fixture.source.ID, "active"), "sharedterm", 10, 1<<20, policy); !IsCode(err, CodeConflict) {
				t.Fatalf("repeated fresh request = %v, want conflict", err)
			}
			defaultAfter, err := fixture.store.EvaluateAndTraceLexical(ctx, testEvaluationRequest(t, fixture.source.ID, "default-after"), "sharedterm", 10, 1<<20, policy)
			if err != nil || defaultAfter.Trail.Supersession != nil || defaultAfter.Trail.PacketID != baseline.Trail.PacketID || !reflect.DeepEqual(defaultAfter.Candidates, baseline.Candidates) {
				t.Fatalf("default path changed by opt-in: %#v, %v", defaultAfter, err)
			}
			view, err := fixture.store.InspectSourceTrail(ctx, testEvaluationRequest(t, fixture.source.ID, "inspect-default"), baseline.Trail.ID)
			if err != nil || view.Historical == nil || view.Historical.Supersession != nil {
				t.Fatalf("legacy view = %#v, %v", view, err)
			}
			data, err := json.Marshal(view)
			if err != nil || bytes.Contains(data, []byte(`"supersession"`)) {
				t.Fatalf("legacy JSON gained opt-in member: %s / %v", data, err)
			}
		})
	}
}

func TestSupersessionFreshTransitionsRevisionsAndRestart(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(policy, func(t *testing.T) {
			ctx := context.Background()
			fixture := newV4Fixture(t)
			declaration, initial := fixture.activate(t)
			active := freshSupersession(t, fixture, "history-active", "sharedterm", policy, 10, 1<<20)
			deactivate := activationTestTransition(t, fixture.source.ID, initial.CurrentActivationID, mousa.SupersessionDeclarationID{}, "deactivate", activationBaseUsec+1)
			if err := fixture.store.ApplySupersessionActivation(ctx, deactivate); err != nil {
				t.Fatal(err)
			}
			off := freshSupersession(t, fixture, "history-off", "sharedterm", policy, 10, 1<<20)
			if off.Trail.Supersession.ActivationID == nil || *off.Trail.Supersession.ActivationID != deactivate.ID || off.Trail.Supersession.DeclarationID != nil || len(off.Trail.Supersession.Dispositions) != 0 {
				t.Fatalf("deactivation = %#v", off.Trail.Supersession)
			}
			reactivate := activationTestTransition(t, fixture.source.ID, deactivate.ID, declaration.ID, "reactivate", activationBaseUsec+2)
			if err := fixture.store.ApplySupersessionActivation(ctx, reactivate); err != nil {
				t.Fatal(err)
			}
			on := freshSupersession(t, fixture, "history-on", "sharedterm", policy, 10, 1<<20)
			if len(on.Trail.Supersession.Dispositions) != 1 || *on.Trail.Supersession.ActivationID != reactivate.ID || on.Trail.PacketID != active.Trail.PacketID {
				t.Fatalf("reactivation = %#v", on.Trail)
			}
			_, revised, text := prepareLocalRevision(t, fixture.store, "predecessor", "sharedterm revised predecessor evidence")
			if _, err := fixture.store.ActivateLocalItem(ctx, fixture.source.ID, "predecessor", revised.ID, text); err != nil {
				t.Fatal(err)
			}
			stale := freshSupersession(t, fixture, "history-stale", "sharedterm", policy, 10, 1<<20)
			if len(stale.Trail.Supersession.Dispositions) != 0 || stale.Trail.Supersession.DeclarationID == nil || *stale.Trail.Supersession.DeclarationID != declaration.ID {
				t.Fatalf("later predecessor revision matched stale pin: %#v", stale.Trail)
			}
			for _, result := range []TracedLexicalResult{active, off, on, stale} {
				view, err := fixture.store.InspectSourceTrail(ctx, testEvaluationRequest(t, fixture.source.ID, "inspect-"+result.Trail.ID.String()), result.Trail.ID)
				if err != nil || view.Historical == nil || view.Historical.Schema != mousa.SourceTrailSchemaV4 || view.Historical.PackingPolicy != policy || !reflect.DeepEqual(view.Historical.Supersession, result.Trail.Supersession) {
					t.Fatalf("historical view reinterpreted old state: %#v, %v", view, err)
				}
			}
			path := fixture.store.path
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			fixture.store = reopened
			defer reopened.Close()
			for _, result := range []TracedLexicalResult{active, off, on, stale} {
				got, err := reopened.GetSourceTrail(ctx, result.Trail.ID)
				if err != nil || !reflect.DeepEqual(got, result.Trail) {
					t.Fatalf("restart history = %#v, %v", got, err)
				}
			}
			after := freshSupersession(t, fixture, "restart", "sharedterm", policy, 10, 1<<20)
			if after.Trail.PacketID != stale.Trail.PacketID || !reflect.DeepEqual(after.Candidates, stale.Candidates) || !reflect.DeepEqual(after.Trail.Supersession, stale.Trail.Supersession) {
				t.Fatal("restart changed fresh selection")
			}
		})
	}
}

func TestSupersessionFreshSuccessorAvailabilityAndEmptyPackets(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		for _, scenario := range []string{"unmatched", "limited", "deleted", "revised", "budget", "no-matches"} {
			t.Run(policy+"/"+scenario, func(t *testing.T) {
				fixture := newV4Fixture(t)
				defer fixture.store.Close()
				fixture.activate(t)
				expression, limit, budget := "predecessor", 10, uint64(1<<20)
				switch scenario {
				case "limited":
					// Only predecessor/twin match; limit cannot manufacture successor evidence.
					limit = 1
				case "deleted":
					if _, err := fixture.store.DeleteLocalItem(context.Background(), fixture.source.ID, "successor"); err != nil {
						t.Fatal(err)
					}
				case "revised":
					_, revised, text := prepareLocalRevision(t, fixture.store, "successor", "other successor revision")
					if _, err := fixture.store.ActivateLocalItem(context.Background(), fixture.source.ID, "successor", revised.ID, text); err != nil {
						t.Fatal(err)
					}
				case "budget":
					expression, budget = "sharedterm", 1
				case "no-matches":
					expression = "absentterm"
				}
				// Remove the byte-equal unrelated candidate to obtain an unambiguous all-suppressed query.
				if _, err := fixture.store.DeleteLocalItem(context.Background(), fixture.source.ID, "twin"); err != nil {
					t.Fatal(err)
				}
				result := freshSupersession(t, fixture, "availability-"+scenario, expression, policy, limit, budget)
				want := 1
				if scenario == "no-matches" {
					want = 0
				}
				if len(result.Trail.Supersession.Dispositions) != want || result.Trail.UsedBytes != 0 {
					t.Fatalf("availability selection = %#v", result.Trail)
				}
				for _, row := range result.Trail.Candidates {
					if row.Selected {
						t.Fatalf("empty packet manufactured evidence: %#v", row)
					}
				}
			})
		}
	}
}

func TestSupersessionFreshFreesBudgetWithoutRenumbering(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(policy, func(t *testing.T) {
			fixture := newV4Fixture(t)
			defer fixture.store.Close()
			// Pin the first ranked representation, rather than assuming a particular digest tie order.
			input := fixture.retrieve(t, "rank-fixture")
			first := input.candidates[0]
			itemNames := map[mousa.RepresentationID]string{fixture.predecessor.ID: "predecessor", fixture.twin.ID: "twin", fixture.successor.ID: "successor", fixture.peer.ID: "peer"}
			successor := fixture.successor.ID
			if first.Segment.RepresentationID == successor {
				successor = fixture.peer.ID
			}
			declaration := supersessionTestDeclaration(t, fixture.source.ID, itemNames[first.Segment.RepresentationID], first.Segment.RepresentationID, itemNames[successor], successor, "example.operations", "first ranked revision replaced")
			if err := fixture.store.PutSupersessionDeclaration(context.Background(), declaration); err != nil {
				t.Fatal(err)
			}
			activation := activationTestTransition(t, fixture.source.ID, mousa.SupersessionActivationID{}, declaration.ID, "activate first rank", activationBaseUsec)
			if err := fixture.store.ApplySupersessionActivation(context.Background(), activation); err != nil {
				t.Fatal(err)
			}
			smallestSurvivor := len(input.candidates[1].Text)
			for _, candidate := range input.candidates[1:] {
				smallestSurvivor = min(smallestSurvivor, len(candidate.Text))
			}
			budget := uint64(len(first.Text) + smallestSurvivor - 1)
			baseline, err := fixture.store.EvaluateAndTraceLexical(context.Background(), testEvaluationRequest(t, fixture.source.ID, "budget-default"), "sharedterm", 10, budget, policy)
			if err != nil {
				t.Fatal(err)
			}
			opted := freshSupersession(t, fixture, "budget-opted", "sharedterm", policy, 10, budget)
			if !baseline.Trail.Candidates[0].Selected || opted.Trail.Candidates[0].Selected || len(opted.Trail.Supersession.Dispositions) != 1 {
				t.Fatal("first-ranked predecessor was not removed before packing")
			}
			freed := false
			for index, row := range opted.Trail.Candidates {
				if row.FinalRank != baseline.Trail.Candidates[index].FinalRank || row.TextBytes != baseline.Trail.Candidates[index].TextBytes {
					t.Fatal("suppression changed rank or canonical size")
				}
				freed = freed || row.Selected && !baseline.Trail.Candidates[index].Selected
			}
			if !freed || opted.Trail.UsedBytes > budget {
				t.Fatalf("suppression did not free budget for later rank: %#v", opted.Trail)
			}
		})
	}
}

func TestSupersessionFreshSourceIsolationAndAncestorPin(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(policy, func(t *testing.T) {
			fixture := newV4Fixture(t)
			defer fixture.store.Close()
			fixture.activate(t)
			foreign := addVerifiedLexicalDocument(t, fixture.store, "foreign-supersession", "sharedterm predecessor evidence")
			foreignResult, err := fixture.store.EvaluateAndTraceSupersessionLexical(context.Background(), testEvaluationRequest(t, foreign.source.ID, "foreign"), "sharedterm", 10, 1<<20, policy)
			if err != nil || len(foreignResult.Candidates) != 1 || len(foreignResult.Trail.Supersession.Dispositions) != 0 || foreignResult.Trail.Supersession.ActivationID != nil || foreignResult.Candidates[0].Text == "" {
				t.Fatalf("foreign selection = %#v, %v", foreignResult, err)
			}
			derived := putVerifiedRepresentation(t, fixture.store, []mousa.DerivationInput{mousa.NewRepresentationDerivationInput(fixture.predecessor.ID)}, "supersession-test.descendant", []byte("sharedterm derived predecessor evidence"), true)
			result := freshSupersession(t, fixture, "ancestor", "sharedterm", policy, 10, 1<<20)
			found := false
			for index, candidate := range result.Candidates {
				if candidate.Segment.RepresentationID == derived.representation.ID {
					found = true
					if candidate.Text == "" || !result.Trail.Candidates[index].Selected || len(candidate.Paths) == 0 {
						t.Fatalf("ancestor pin suppressed descendant: %#v", candidate)
					}
				}
				if candidate.Segment.RepresentationID == foreign.representation.ID {
					t.Fatal("source-scoped query included foreign candidate")
				}
			}
			if !found || len(result.Trail.Supersession.Dispositions) != 1 {
				t.Fatalf("descendant fixture missing or suppression widened: %#v", result.Trail)
			}
		})
	}
}

func freshAuditCounts(t *testing.T, store *Store) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, table := range []string{"policy_decisions", "policy_decision_inputs", "source_trails", "source_trail_candidates", "supersession_activations", "supersession_activation_state", "supersession_declarations"} {
		var count int
		if err := store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

func TestSupersessionFreshLiveCorruptionRollsBack(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		for _, damage := range []string{"projection-missing", "projection-mismatch", "event-missing", "event-record", "chain-missing", "declaration-missing", "declaration-record", "cross-source"} {
			t.Run(policy+"/"+damage, func(t *testing.T) {
				fixture := newV4Fixture(t)
				defer fixture.store.Close()
				declaration, state := fixture.activate(t)
				ctx := context.Background()
				if damage == "chain-missing" {
					off := activationTestTransition(t, fixture.source.ID, state.CurrentActivationID, mousa.SupersessionDeclarationID{}, "off", activationBaseUsec+1)
					on := activationTestTransition(t, fixture.source.ID, off.ID, declaration.ID, "on", activationBaseUsec+2)
					for _, event := range []mousa.SupersessionActivation{off, on} {
						if err := fixture.store.ApplySupersessionActivation(ctx, event); err != nil {
							t.Fatal(err)
						}
					}
					execDamagingStatement(t, fixture.store, `DELETE FROM supersession_activations WHERE id = ?`, off.ID[:])
				} else {
					switch damage {
					case "projection-missing":
						execDamagingStatement(t, fixture.store, `DELETE FROM supersession_activation_state WHERE source_id = ?`, fixture.source.ID[:])
					case "projection-mismatch":
						execDamagingStatement(t, fixture.store, `UPDATE supersession_activation_state SET active_declaration_id = NULL WHERE source_id = ?`, fixture.source.ID[:])
					case "event-missing":
						execDamagingStatement(t, fixture.store, `DELETE FROM supersession_activations WHERE id = ?`, state.CurrentActivationID[:])
					case "event-record":
						execDamagingStatement(t, fixture.store, `UPDATE supersession_activations SET record_json = ? WHERE id = ?`, []byte("{}"), state.CurrentActivationID[:])
					case "declaration-missing":
						execDamagingStatement(t, fixture.store, `DELETE FROM supersession_declarations WHERE id = ?`, declaration.ID[:])
					case "declaration-record":
						execDamagingStatement(t, fixture.store, `UPDATE supersession_declarations SET record_json = ? WHERE id = ?`, []byte("{}"), declaration.ID[:])
					case "cross-source":
						foreign := addVerifiedLexicalDocument(t, fixture.store, "cross-source", "foreign evidence")
						event := activationTestTransition(t, foreign.source.ID, mousa.SupersessionActivationID{}, declaration.ID, "foreign source event", activationBaseUsec)
						data, err := mousa.EncodeSupersessionActivation(event)
						if err != nil {
							t.Fatal(err)
						}
						execDamagingStatement(t, fixture.store, `INSERT INTO supersession_activations(id, source_id, expected_previous_activation_id, declaration_id, record_json) VALUES(?, ?, NULL, ?, ?)`, event.ID[:], foreign.source.ID[:], declaration.ID[:], data)
						execDamagingStatement(t, fixture.store, `UPDATE supersession_activation_state SET current_activation_id = ? WHERE source_id = ?`, event.ID[:], fixture.source.ID[:])
					}
				}
				before := freshAuditCounts(t, fixture.store)
				current, active := rawActivationState(t, fixture.store, fixture.source.ID)
				result, err := fixture.store.EvaluateAndTraceSupersessionLexical(ctx, testEvaluationRequest(t, fixture.source.ID, "damaged"), "sharedterm", 10, 1<<20, policy)
				if !IsCode(err, CodeIntegrity) || !reflect.DeepEqual(result, TracedLexicalResult{}) {
					t.Fatalf("corrupt request released result: %#v, %v", result, err)
				}
				if !reflect.DeepEqual(before, freshAuditCounts(t, fixture.store)) {
					t.Fatal("failed fresh request committed partial decision/trail/admin rows")
				}
				gotCurrent, gotActive := rawActivationState(t, fixture.store, fixture.source.ID)
				if !bytes.Equal(current, gotCurrent) || !bytes.Equal(active, gotActive) {
					t.Fatal("failed retrieval repaired damaged projection")
				}
			})
		}
	}
}

func TestSupersessionFreshDenialSkipsDamagedAdministration(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		for _, denial := range []string{"policy", "withdrawn"} {
			t.Run(policy+"/"+denial, func(t *testing.T) {
				fixture := newV4Fixture(t)
				defer fixture.store.Close()
				_, state := fixture.activate(t)
				execDamagingStatement(t, fixture.store, `UPDATE supersession_activations SET record_json = ? WHERE id = ?`, []byte("{}"), state.CurrentActivationID[:])
				request := testEvaluationRequest(t, fixture.source.ID, "denied")
				if denial == "policy" {
					request.ExternalCallerID = "unauthorized"
					id, err := mousa.NewPolicyEvaluationRequestID(request)
					if err != nil {
						t.Fatal(err)
					}
					request.ID = id
				} else {
					withdrawVerifiedSource(t, fixture.store, fixture.source.ID, "withdrawn", 200)
				}
				result, err := fixture.store.EvaluateAndTraceSupersessionLexical(context.Background(), request, "sharedterm", 10, 1<<20, policy)
				if err != nil || result.Decision.Outcome != mousa.PolicyOutcomeDeny || result.Trail.Schema != mousa.SourceTrailSchemaV4 || result.Trail.Supersession == nil || result.Trail.Supersession.Consulted || result.Trail.Supersession.ActivationID != nil || result.Trail.Supersession.DeclarationID != nil || len(result.Candidates) != 0 || result.Trail.UsedBytes != 0 {
					t.Fatalf("denial consulted damaged administration: %#v, %v", result, err)
				}
				view, err := fixture.store.InspectSourceTrail(context.Background(), testEvaluationRequest(t, fixture.source.ID, "inspect-denied"), result.Trail.ID)
				if denial == "policy" {
					// A newly authorized inspection may disclose the recorded unconsulted denial.
					if err != nil || view.Historical == nil || view.Historical.Supersession == nil || view.Historical.Supersession.Consulted {
						t.Fatalf("historical denied record = %#v, %v", view, err)
					}
				} else if err != nil || view.Historical != nil {
					t.Fatalf("fresh lifecycle denial exposed history: %#v, %v", view, err)
				}
			})
		}
	}
}

func TestSupersessionFreshTransactionOrdering(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(policy, func(t *testing.T) {
			fixture := newV4Fixture(t)
			defer fixture.store.Close()
			_, state := fixture.activate(t)
			ctx := context.Background()
			writer, err := Open(ctx, fixture.store.path)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			conn, err := writer.db.Conn(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
				t.Fatal(err)
			}
			defer conn.ExecContext(ctx, `ROLLBACK`)
			off := activationTestTransition(t, fixture.source.ID, state.CurrentActivationID, mousa.SupersessionDeclarationID{}, "ordered deactivation", activationBaseUsec+1)
			data, err := mousa.EncodeSupersessionActivation(off)
			if err != nil {
				t.Fatal(err)
			}
			if err := applySupersessionActivation(ctx, conn, off, data); err != nil {
				t.Fatal(err)
			}
			// A separate handle owns the SQLite writer lock while its transition is
			// uncommitted. The fresh writer must observe the complete committed state,
			// regardless of goroutine scheduling; no timed wait is involved.
			started := make(chan struct{})
			done := make(chan struct {
				result TracedLexicalResult
				err    error
			}, 1)
			request := testEvaluationRequest(t, fixture.source.ID, "ordered")
			go func() {
				close(started)
				result, err := fixture.store.EvaluateAndTraceSupersessionLexical(ctx, request, "sharedterm", 10, 1<<20, policy)
				done <- struct {
					result TracedLexicalResult
					err    error
				}{result, err}
			}()
			<-started
			if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
				t.Fatal(err)
			}
			if err := conn.Close(); err != nil {
				t.Fatal(err)
			}
			got := <-done
			if got.err != nil || got.result.Trail.Supersession == nil || got.result.Trail.Supersession.ActivationID == nil || *got.result.Trail.Supersession.ActivationID != off.ID || got.result.Trail.Supersession.DeclarationID != nil || len(got.result.Trail.Supersession.Dispositions) != 0 {
				t.Fatalf("retrieval observed mixed precommit administration: %#v, %v", got.result, got.err)
			}
		})
	}
}

func TestSupersessionFreshCanonicalReadbackFailureRollsBack(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(policy, func(t *testing.T) {
			fixture := newV4Fixture(t)
			defer fixture.store.Close()
			fixture.activate(t)
			if _, err := fixture.store.db.Exec(`CREATE TRIGGER corrupt_new_supersession_candidate AFTER INSERT ON source_trail_candidates BEGIN UPDATE source_trail_candidates SET final_rank = final_rank + 1 WHERE trail_id = NEW.trail_id AND ordinal = NEW.ordinal; END`); err != nil {
				t.Fatal(err)
			}
			before := freshAuditCounts(t, fixture.store)
			result, err := fixture.store.EvaluateAndTraceSupersessionLexical(context.Background(), testEvaluationRequest(t, fixture.source.ID, "readback-failure"), "sharedterm", 10, 1<<20, policy)
			if !IsCode(err, CodeIntegrity) || !reflect.DeepEqual(result, TracedLexicalResult{}) || !reflect.DeepEqual(before, freshAuditCounts(t, fixture.store)) {
				t.Fatalf("canonical readback failure committed or released: %#v, %v", result, err)
			}
		})
	}
}

func TestSupersessionFreshUnknownHistoryAndReadOnlyBoundaries(t *testing.T) {
	fixture := newV4Fixture(t)
	defer fixture.store.Close()
	ctx := context.Background()
	unknown := mousa.SourceTrailID{1}
	before := freshAuditCounts(t, fixture.store)
	if _, err := fixture.store.InspectSourceTrail(ctx, testEvaluationRequest(t, fixture.source.ID, "unknown-history"), unknown); !IsCode(err, CodeNotFound) {
		t.Fatalf("unknown history = %v, want not-found", err)
	}
	if !reflect.DeepEqual(before, freshAuditCounts(t, fixture.store)) {
		t.Fatal("unknown history committed authorization")
	}
	readOnly, err := OpenReadOnly(ctx, fixture.store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		if _, err := readOnly.EvaluateAndTraceSupersessionLexical(ctx, testEvaluationRequest(t, fixture.source.ID, "read-only-"+policy), "sharedterm", 10, 1000, policy); !IsCode(err, CodeReadOnly) {
			t.Fatalf("read-only fresh opt-in = %v", err)
		}
	}
}

func TestSupersessionFreshMatchedSuccessorOutsideLimit(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(policy, func(t *testing.T) {
			fixture := newV4Fixture(t)
			defer fixture.store.Close()
			input := fixture.retrieve(t, "limit-fixture")
			names := map[mousa.RepresentationID]string{fixture.predecessor.ID: "predecessor", fixture.twin.ID: "twin", fixture.successor.ID: "successor", fixture.peer.ID: "peer"}
			predecessor := input.candidates[0].Segment.RepresentationID
			successor := input.candidates[1].Segment.RepresentationID
			declaration := supersessionTestDeclaration(t, fixture.source.ID, names[predecessor], predecessor, names[successor], successor, "example.operations", "matched successor outside limit")
			if err := fixture.store.PutSupersessionDeclaration(context.Background(), declaration); err != nil {
				t.Fatal(err)
			}
			event := activationTestTransition(t, fixture.source.ID, mousa.SupersessionActivationID{}, declaration.ID, "activate", activationBaseUsec)
			if err := fixture.store.ApplySupersessionActivation(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			result := freshSupersession(t, fixture, "limited-matched", "sharedterm", policy, 1, 1<<20)
			if len(result.Candidates) != 1 || result.Candidates[0].Segment.RepresentationID != predecessor || result.Candidates[0].Text != "" || result.Trail.UsedBytes != 0 || len(result.Trail.Supersession.Dispositions) != 1 {
				t.Fatalf("matched successor outside limit reinstated predecessor: %#v", result)
			}
		})
	}
}

func TestSupersessionFreshWithheldDuplicateNeverSeedsRetention(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(policy, func(t *testing.T) {
			fixture := newV4Fixture(t)
			defer fixture.store.Close()
			input := fixture.retrieve(t, "duplicate-order")
			pinned, item := fixture.predecessor.ID, "predecessor"
			for _, candidate := range input.candidates {
				if candidate.Segment.RepresentationID == fixture.predecessor.ID {
					break
				}
				if candidate.Segment.RepresentationID == fixture.twin.ID {
					pinned, item = fixture.twin.ID, "twin"
					break
				}
			}
			declaration := supersessionTestDeclaration(t, fixture.source.ID, item, pinned, "successor", fixture.successor.ID, "example.operations", "earlier duplicate replaced")
			if err := fixture.store.PutSupersessionDeclaration(context.Background(), declaration); err != nil {
				t.Fatal(err)
			}
			event := activationTestTransition(t, fixture.source.ID, mousa.SupersessionActivationID{}, declaration.ID, "activate", activationBaseUsec)
			if err := fixture.store.ApplySupersessionActivation(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			result := freshSupersession(t, fixture, "duplicate-opted", "predecessor", policy, 10, 1<<20)
			if len(result.Candidates) != 2 || result.Candidates[0].Segment.RepresentationID != pinned || result.Candidates[0].Text != "" || !result.Trail.Candidates[1].Selected || result.Trail.Candidates[1].Omission != "" || result.Trail.Candidates[1].DuplicateOf != "" {
				t.Fatalf("withheld duplicate displaced later equal survivor: %#v", result)
			}
		})
	}
}

func TestSupersessionFreshMixedLifecycleAndBudgetOmissions(t *testing.T) {
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		for _, budget := range []uint64{1, 1 << 20} {
			name := "surviving-budget"
			if budget > 1 {
				name = "lifecycle-only"
			}
			t.Run(policy+"/"+name, func(t *testing.T) {
				fixture := newV4Fixture(t)
				defer fixture.store.Close()
				fixture.activate(t)
				foreign := addVerifiedLexicalDocument(t, fixture.store, "withdrawn-ancestor", "foreign predecessor")
				derived := putVerifiedRepresentation(t, fixture.store, []mousa.DerivationInput{
					mousa.NewRepresentationDerivationInput(fixture.predecessor.ID),
					mousa.NewRepresentationDerivationInput(foreign.representation.ID),
				}, "supersession-test.withdrawn-ancestor", []byte("sharedterm predecessor derived evidence"), true)
				withdrawVerifiedSource(t, fixture.store, foreign.source.ID, "withdraw-derived", 200)
				if budget > 1 {
					if _, err := fixture.store.DeleteLocalItem(context.Background(), fixture.source.ID, "twin"); err != nil {
						t.Fatal(err)
					}
				}
				result := freshSupersession(t, fixture, "mixed", "predecessor", policy, 10, budget)
				rejected, selected := 0, 0
				for index, candidate := range result.Candidates {
					if candidate.Segment.RepresentationID == derived.representation.ID {
						rejected++
						if candidate.Disposition != mousa.CandidateRejected || candidate.Text != "" || result.Trail.Candidates[index].TextBytes != 0 {
							t.Fatalf("lifecycle rejection leaked text or became supersession: %#v", candidate)
						}
					}
					if result.Trail.Candidates[index].Selected {
						selected++
					}
				}
				if rejected != 1 || selected != 0 || len(result.Trail.Supersession.Dispositions) != 1 {
					t.Fatalf("mixed dispositions conflated: %#v", result)
				}
			})
		}
	}
}

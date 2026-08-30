package sqlite

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestPolicyBindingActivationCASReplayAndReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "policy-binding.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	definition := testPolicyDefinition(t, "example", "policy", "1", "opaque", "opaque", "definition")
	if err := store.PutPolicyDefinition(ctx, definition); err != nil {
		t.Fatal(err)
	}
	binding := testPolicyBinding(t, "example", "series", "1", definition.ID)
	if err := store.PutPolicyBinding(ctx, binding); err != nil {
		t.Fatalf("PutPolicyBinding: %v", err)
	}
	var missingSource mousa.SourceID
	missingSource[0] = 1
	missingScope := mousa.NewSourcePolicyScope(missingSource)
	missingID, _ := mousa.NewPolicyBindingID("operator.example", "missing-source", "1", missingScope, definition.ID)
	missingBinding := mousa.PolicyBinding{Schema: mousa.PolicyBindingSchema, ID: missingID, Namespace: "operator.example", ExternalBindingID: "missing-source", ExternalBindingVersion: "1", Scope: missingScope, PolicyDefinitionID: definition.ID}
	if err := store.PutPolicyBinding(ctx, missingBinding); !IsCode(err, CodeConflict) {
		t.Fatalf("missing typed scope target = %v", err)
	}
	if err := store.PutPolicyBinding(ctx, binding); err != nil {
		t.Fatalf("exact binding replay: %v", err)
	}
	gotBinding, err := store.GetPolicyBinding(ctx, binding.ID)
	if err != nil || !reflect.DeepEqual(gotBinding, binding) {
		t.Fatalf("GetPolicyBinding = %#v, %v", gotBinding, err)
	}

	first := testPolicyActivation(t, "example", "series", "activate", nil, &binding.ID)
	if err := store.ApplyPolicyActivation(ctx, first); err != nil {
		t.Fatalf("first activation: %v", err)
	}
	if err := store.ApplyPolicyActivation(ctx, first); err != nil {
		t.Fatalf("exact activation replay: %v", err)
	}
	wantState := mousa.PolicyBindingState{Namespace: "example", ExternalBindingID: "series", CurrentActivationID: first.ID, ActiveBindingID: &binding.ID}
	if got, err := store.GetPolicyBindingState(ctx, "example", "series"); err != nil || !reflect.DeepEqual(got, wantState) {
		t.Fatalf("active state = %#v, %v", got, err)
	}
	if got, err := store.GetPolicyActivation(ctx, first.ID); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("GetPolicyActivation = %#v, %v", got, err)
	}

	stale := testPolicyActivation(t, "example", "series", "stale", nil, nil)
	if err := store.ApplyPolicyActivation(ctx, stale); !IsCode(err, CodeConflict) {
		t.Fatalf("stale activation = %v", err)
	}
	noOp := testPolicyActivation(t, "example", "series", "noop", &first.ID, &binding.ID)
	if err := store.ApplyPolicyActivation(ctx, noOp); !IsCode(err, CodeConflict) {
		t.Fatalf("new no-op activation = %v", err)
	}
	deactivate := testPolicyActivation(t, "example", "series", "deactivate", &first.ID, nil)
	if err := store.ApplyPolicyActivation(ctx, deactivate); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if got, err := store.GetPolicyBindingState(ctx, "example", "series"); err != nil || got.ActiveBindingID != nil || got.CurrentActivationID != deactivate.ID {
		t.Fatalf("inactive state = %#v, %v", got, err)
	}
	newNoOp := testPolicyActivation(t, "example", "series", "still-inactive", &deactivate.ID, nil)
	if err := store.ApplyPolicyActivation(ctx, newNoOp); !IsCode(err, CodeConflict) {
		t.Fatalf("inactive no-op = %v", err)
	}
	reactivate := testPolicyActivation(t, "example", "series", "reactivate", &deactivate.ID, &binding.ID)
	if err := store.ApplyPolicyActivation(ctx, reactivate); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if err := store.ApplyPolicyActivation(ctx, first); err != nil {
		t.Fatalf("historical exact replay after later transitions: %v", err)
	}

	other := testPolicyBinding(t, "example", "other", "1", definition.ID)
	if err := store.PutPolicyBinding(ctx, other); err != nil {
		t.Fatal(err)
	}
	otherActivation := testPolicyActivation(t, "example", "other", "activate", nil, &other.ID)
	if err := store.ApplyPolicyActivation(ctx, otherActivation); err != nil {
		t.Fatalf("second logical series at deployment scope: %v", err)
	}
	if firstState, err := store.GetPolicyBindingState(ctx, "example", "series"); err != nil || firstState.ActiveBindingID == nil || *firstState.ActiveBindingID != binding.ID {
		t.Fatalf("first simultaneous state = %#v, %v", firstState, err)
	}
	if otherState, err := store.GetPolicyBindingState(ctx, "example", "other"); err != nil || otherState.ActiveBindingID == nil || *otherState.ActiveBindingID != other.ID {
		t.Fatalf("second simultaneous state = %#v, %v", otherState, err)
	}
	crossSeries := testPolicyActivation(t, "example", "series", "cross-series", &reactivate.ID, &other.ID)
	if err := store.ApplyPolicyActivation(ctx, crossSeries); !IsCode(err, CodeConflict) {
		t.Fatalf("cross-series selection = %v", err)
	}
	fork := testPolicyActivation(t, "example", "series", "fork", &first.ID, &binding.ID)
	if err := store.ApplyPolicyActivation(ctx, fork); !IsCode(err, CodeConflict) {
		t.Fatalf("fork = %v", err)
	}

	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if got, err := readOnly.GetPolicyBindingState(ctx, "example", "series"); err != nil || got.CurrentActivationID != reactivate.ID {
		t.Fatalf("read-only state = %#v, %v", got, err)
	}
	if err := readOnly.PutPolicyBinding(ctx, mousa.PolicyBinding{}); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only binding precedence = %v", err)
	}
	if err := readOnly.ApplyPolicyActivation(ctx, mousa.PolicyActivation{}); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only activation precedence = %v", err)
	}
}

func TestPolicyBindingWithdrawnSourceCanBeStoredAndSelected(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "withdrawn-source-binding.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	batch := testIngestBatch(t, "withdrawn-source-binding")
	batch.Checkpoint = &mousa.CheckpointAdvance{Next: []byte("withdrawn-source-binding-next")}
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatalf("create canonical evidence before withdrawal: %v", err)
	}
	withdrawalID, err := mousa.NewWithdrawalID(batch.Source.ID, "withdrawn-source-binding")
	if err != nil {
		t.Fatal(err)
	}
	withdrawal := mousa.SourceWithdrawal{Schema: mousa.SourceWithdrawalSchema, ID: withdrawalID, SourceID: batch.Source.ID, ExternalWithdrawalID: "withdrawn-source-binding", AdapterID: "test.adapter", AdapterVersion: "1", OccurredAtUsec: 2}
	if err := store.WithdrawSource(ctx, withdrawal); err != nil {
		t.Fatalf("withdraw source: %v", err)
	}
	state, err := store.GetIngestState(ctx, batch.Source.ID)
	if err != nil {
		t.Fatalf("get withdrawn source state: %v", err)
	}
	if state.CollectionState != mousa.CollectionWithdrawn || state.CurrentWithdrawalID == nil || *state.CurrentWithdrawalID != withdrawalID {
		t.Fatalf("withdrawn source state = %#v", state)
	}

	definition := testPolicyDefinition(t, "withdrawn-source", "policy", "1", "application/x-unsupported-policy", "urn:unsupported:policy", "opaque definition")
	if err := store.PutPolicyDefinition(ctx, definition); err != nil {
		t.Fatalf("put opaque policy definition: %v", err)
	}
	scope := mousa.NewSourcePolicyScope(batch.Source.ID)
	bindingID, err := mousa.NewPolicyBindingID("withdrawn-source", "binding", "1", scope, definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := mousa.PolicyBinding{Schema: mousa.PolicyBindingSchema, ID: bindingID, Namespace: "withdrawn-source", ExternalBindingID: "binding", ExternalBindingVersion: "1", Scope: scope, PolicyDefinitionID: definition.ID}
	if err := store.PutPolicyBinding(ctx, binding); err != nil {
		t.Fatalf("put withdrawn-source binding: %v", err)
	}
	if got, err := store.GetPolicyBinding(ctx, binding.ID); err != nil || !reflect.DeepEqual(got, binding) {
		t.Fatalf("withdrawn-source binding = %#v, %v", got, err)
	}

	activation := testPolicyActivation(t, "withdrawn-source", "binding", "activate", nil, &binding.ID)
	if err := store.ApplyPolicyActivation(ctx, activation); err != nil {
		t.Fatalf("select withdrawn-source binding: %v", err)
	}
	wantState := mousa.PolicyBindingState{Namespace: "withdrawn-source", ExternalBindingID: "binding", CurrentActivationID: activation.ID, ActiveBindingID: &binding.ID}
	if got, err := store.GetPolicyBindingState(ctx, "withdrawn-source", "binding"); err != nil || !reflect.DeepEqual(got, wantState) {
		t.Fatalf("withdrawn-source binding state = %#v, %v", got, err)
	}
}

func TestPolicyBindingParentsRequireCanonicalReadback(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name       string
		scope      func(policyParentFixture) mousa.PolicyScope
		table      string
		missing    bool
		definition bool
	}{
		{"definition corruption", func(f policyParentFixture) mousa.PolicyScope { return mousa.NewDeploymentPolicyScope() }, "policy_definitions", false, true},
		{"source corruption", func(f policyParentFixture) mousa.PolicyScope { return mousa.NewSourcePolicyScope(f.source.ID) }, "sources", false, false},
		{"observation corruption", func(f policyParentFixture) mousa.PolicyScope {
			return mousa.NewObservationPolicyScope(f.observation.ID)
		}, "observations", false, false},
		{"artifact corruption", func(f policyParentFixture) mousa.PolicyScope { return mousa.NewArtifactPolicyScope(f.artifact.ID) }, "artifacts", false, false},
		{"representation corruption", func(f policyParentFixture) mousa.PolicyScope { return mousa.NewRepresentationPolicyScope(f.base.ID) }, "representations", false, false},
		{"segment corruption", func(f policyParentFixture) mousa.PolicyScope { return mousa.NewSegmentPolicyScope(f.segment.ID) }, "segments", false, false},
		{"missing definition", func(f policyParentFixture) mousa.PolicyScope { return mousa.NewDeploymentPolicyScope() }, "", true, true},
		{"missing source", func(f policyParentFixture) mousa.PolicyScope {
			id := f.source.ID
			id[0] ^= 0xff
			return mousa.NewSourcePolicyScope(id)
		}, "", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, fixture := createPolicyParentFixture(t, filepath.Join(t.TempDir(), "parents.sqlite"))
			defer store.Close()
			definitionID := fixture.definition.ID
			if test.missing && test.definition {
				definitionID[0] ^= 0xff
			}
			scope := test.scope(fixture)
			bindingID, err := mousa.NewPolicyBindingID("parents", test.name, "1", scope, definitionID)
			if err != nil {
				t.Fatal(err)
			}
			binding := mousa.PolicyBinding{Schema: mousa.PolicyBindingSchema, ID: bindingID, Namespace: "parents", ExternalBindingID: test.name, ExternalBindingVersion: "1", Scope: scope, PolicyDefinitionID: definitionID}
			if test.table != "" {
				if _, err := store.db.ExecContext(ctx, `UPDATE `+test.table+` SET record_json = CAST(? AS BLOB)`, []byte("corrupt")); err != nil {
					t.Fatal(err)
				}
			}
			err = store.PutPolicyBinding(ctx, binding)
			wantCode := CodeIntegrity
			if test.missing {
				wantCode = CodeConflict
			}
			if !IsCode(err, wantCode) {
				t.Fatalf("PutPolicyBinding = %v, want %s", err, wantCode)
			}
			var count int
			if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM policy_bindings WHERE id = ?`, binding.ID[:]).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("failed binding inserted %d row(s)", count)
			}
		})
	}
}

type policyParentFixture struct {
	definition  mousa.PolicyDefinition
	source      mousa.Source
	observation mousa.Observation
	artifact    mousa.Artifact
	base        mousa.Representation
	segment     mousa.Segment
}

func createPolicyParentFixture(t testing.TB, path string) (*Store, policyParentFixture) {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	definition := testPolicyDefinition(t, "parents", "policy", "1", "opaque", "opaque", "definition")
	source, observation, artifact, base, mixed, segment := testRecordGraph(t.(*testing.T))
	if err := store.PutPolicyDefinition(ctx, definition); err != nil {
		t.Fatal(err)
	}
	for _, put := range []func() error{
		func() error { return store.PutSource(ctx, source) },
		func() error { return store.PutObservation(ctx, observation) },
		func() error { return store.PutArtifact(ctx, artifact) },
		func() error { return store.PutRepresentation(ctx, base) },
		func() error { return store.PutRepresentation(ctx, mixed) },
		func() error { return store.PutSegment(ctx, segment) },
	} {
		if err := put(); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	return store, policyParentFixture{definition: definition, source: source, observation: observation, artifact: artifact, base: base, segment: segment}
}

func TestPolicyBindingActivationIntegrityAndBackup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "policy-binding.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	definition := testPolicyDefinition(t, "example", "policy", "1", "opaque", "opaque", "definition")
	binding := testPolicyBinding(t, "example", "series", "1", definition.ID)
	activation := testPolicyActivation(t, "example", "series", "activate", nil, &binding.ID)
	if err := store.PutPolicyDefinition(ctx, definition); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPolicyBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyPolicyActivation(ctx, activation); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := store.Backup(ctx, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := OpenReadOnly(ctx, backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := backup.GetPolicyBindingState(ctx, "example", "series"); err != nil || got.CurrentActivationID != activation.ID {
		t.Fatalf("backup state = %#v, %v", got, err)
	}
	if got, err := backup.GetPolicyDefinition(ctx, definition.ID); err != nil || !reflect.DeepEqual(got, definition) {
		t.Fatalf("backup definition = %#v, %v", got, err)
	}
	if got, err := backup.GetPolicyBinding(ctx, binding.ID); err != nil || !reflect.DeepEqual(got, binding) {
		t.Fatalf("backup binding = %#v, %v", got, err)
	}
	if got, err := backup.GetPolicyActivation(ctx, activation.ID); err != nil || !reflect.DeepEqual(got, activation) {
		t.Fatalf("backup activation = %#v, %v", got, err)
	}
	backup.Close()

	tamper, err := connect(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tamper.ExecContext(ctx, `UPDATE policy_binding_state SET active_binding_id = NULL`); err != nil {
		t.Fatal(err)
	}
	tamper.Close()
	if reopened, err := Open(ctx, path); reopened != nil || !IsCode(err, CodeIntegrity) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("tampered startup = %v, %v", reopened, err)
	}
}

func TestPolicyBindingActivationStartupRejectsTampering(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, policyTamperFixture)
	}{
		{"canonical binding bytes", func(t *testing.T, path string, fixture policyTamperFixture) {
			execPolicyTamper(t, path, `UPDATE policy_bindings SET record_json = CAST(substr(record_json, 1, length(record_json) - 1) AS BLOB) WHERE id = ?`, fixture.firstBinding.ID[:])
		}},
		{"binding projection", func(t *testing.T, path string, fixture policyTamperFixture) {
			execPolicyTamper(t, path, `UPDATE policy_bindings SET external_binding_version = 'tampered' WHERE id = ?`, fixture.firstBinding.ID[:])
		}},
		{"selected binding projection", func(t *testing.T, path string, fixture policyTamperFixture) {
			execPolicyTamper(t, path, `UPDATE policy_activations SET active_binding_id = randomblob(32) WHERE id = ?`, fixture.secondActivation.ID[:])
		}},
		{"predecessor projection", func(t *testing.T, path string, fixture policyTamperFixture) {
			execPolicyTamper(t, path, `UPDATE policy_activations SET expected_previous_activation_id = randomblob(32) WHERE id = ?`, fixture.secondActivation.ID[:])
		}},
		{"root index", func(t *testing.T, path string, _ policyTamperFixture) {
			execPolicyTamper(t, path, `DROP INDEX policy_activations_root_idx`)
		}},
		{"fork index", func(t *testing.T, path string, _ policyTamperFixture) {
			execPolicyTamper(t, path, `DROP INDEX policy_activations_predecessor_idx`)
		}},
		{"wrong tip", func(t *testing.T, path string, fixture policyTamperFixture) {
			execPolicyTamper(t, path, `UPDATE policy_binding_state SET current_activation_id = ?, active_binding_id = ? WHERE namespace = 'tamper' AND external_binding_id = 'series'`, fixture.firstActivation.ID[:], fixture.firstBinding.ID[:])
		}},
		{"active state", func(t *testing.T, path string, _ policyTamperFixture) {
			execPolicyTamper(t, path, `UPDATE policy_binding_state SET active_binding_id = NULL`)
		}},
		{"scope parent", func(t *testing.T, path string, fixture policyTamperFixture) {
			execPolicyTamper(t, path, `DELETE FROM sources WHERE id = ?`, fixture.sourceID[:])
		}},
		{"unreachable event", func(t *testing.T, path string, fixture policyTamperFixture) {
			data, err := mousa.EncodePolicyActivation(fixture.orphanActivation)
			if err != nil {
				t.Fatal(err)
			}
			execPolicyTamper(t, path, `INSERT INTO policy_activations(id, namespace, external_binding_id, external_activation_id, expected_previous_activation_id, active_binding_id, record_json) VALUES(?, ?, ?, ?, NULL, ?, ?)`, fixture.orphanActivation.ID[:], fixture.orphanActivation.Namespace, fixture.orphanActivation.ExternalBindingID, fixture.orphanActivation.ExternalActivationID, fixture.orphanBinding.ID[:], data)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tampered.sqlite")
			fixture := createPolicyTamperFixture(t, path)
			test.mutate(t, path, fixture)
			if store, err := Open(context.Background(), path); store != nil || !IsCode(err, CodeIntegrity) {
				if store != nil {
					store.Close()
				}
				t.Fatalf("Open tampered database = %v, %v", store, err)
			}
		})
	}
}

type policyTamperFixture struct {
	firstBinding, secondBinding       mousa.PolicyBinding
	firstActivation, secondActivation mousa.PolicyActivation
	sourceID                          mousa.SourceID
	orphanBinding                     mousa.PolicyBinding
	orphanActivation                  mousa.PolicyActivation
}

func createPolicyTamperFixture(t testing.TB, path string) policyTamperFixture {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	definition := testPolicyDefinition(t, "tamper", "policy", "1", "opaque", "opaque", "definition")
	first := testPolicyBinding(t, "tamper", "series", "1", definition.ID)
	second := testPolicyBinding(t, "tamper", "series", "2", definition.ID)
	orphan := testPolicyBinding(t, "tamper", "orphan", "1", definition.ID)
	root := testPolicyActivation(t, "tamper", "series", "root", nil, &first.ID)
	tip := testPolicyActivation(t, "tamper", "series", "tip", &root.ID, &second.ID)
	orphanActivation := testPolicyActivation(t, "tamper", "orphan", "root", nil, &orphan.ID)
	source, err := mousa.NewSourceID("tamper", "source")
	if err != nil {
		t.Fatal(err)
	}
	sourceRecord := mousa.Source{Schema: mousa.SourceSchema, ID: source, Namespace: "tamper", ExternalSourceID: "source"}
	sourceScope := mousa.NewSourcePolicyScope(source)
	sourceBindingID, err := mousa.NewPolicyBindingID("tamper", "source-series", "1", sourceScope, definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourceBinding := mousa.PolicyBinding{Schema: mousa.PolicyBindingSchema, ID: sourceBindingID, Namespace: "tamper", ExternalBindingID: "source-series", ExternalBindingVersion: "1", Scope: sourceScope, PolicyDefinitionID: definition.ID}
	if err := store.PutPolicyDefinition(ctx, definition); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPolicyBinding(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPolicyBinding(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPolicyBinding(ctx, orphan); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSource(ctx, sourceRecord); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPolicyBinding(ctx, sourceBinding); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyPolicyActivation(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyPolicyActivation(ctx, tip); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return policyTamperFixture{first, second, root, tip, source, orphan, orphanActivation}
}

func execPolicyTamper(t testing.TB, path, query string, args ...any) {
	t.Helper()
	db, err := connect(context.Background(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func testPolicyBinding(t testing.TB, namespace, externalID, version string, definitionID mousa.PolicyDefinitionID) mousa.PolicyBinding {
	t.Helper()
	scope := mousa.NewDeploymentPolicyScope()
	id, err := mousa.NewPolicyBindingID(namespace, externalID, version, scope, definitionID)
	if err != nil {
		t.Fatal(err)
	}
	return mousa.PolicyBinding{Schema: mousa.PolicyBindingSchema, ID: id, Namespace: namespace, ExternalBindingID: externalID, ExternalBindingVersion: version, Scope: scope, PolicyDefinitionID: definitionID}
}

func testPolicyActivation(t testing.TB, namespace, externalID, activationID string, previous *mousa.PolicyActivationID, active *mousa.PolicyBindingID) mousa.PolicyActivation {
	t.Helper()
	id, err := mousa.NewPolicyActivationID(namespace, externalID, activationID)
	if err != nil {
		t.Fatal(err)
	}
	return mousa.PolicyActivation{Schema: mousa.PolicyActivationSchema, ID: id, Namespace: namespace, ExternalBindingID: externalID, ExternalActivationID: activationID, ExpectedPreviousActivationID: previous, ActiveBindingID: active, ActorID: "actor", ActorVersion: "1", OccurredAtUsec: 1}
}

func TestPolicyBindingDirectCoreDiscriminators(t *testing.T) {
	definition := testPolicyDefinition(t, "direct", "policy", "1", "opaque", "opaque", "definition")
	var raw [32]byte
	raw[0] = 1
	sourceID := mousa.SourceID(raw)
	sourceScope := mousa.NewSourcePolicyScope(sourceID)
	sourceBindingID, err := mousa.NewPolicyBindingID("direct", "binding", "1", sourceScope, definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	observationBindingID, err := mousa.NewPolicyBindingID("direct", "binding", "1", mousa.NewObservationPolicyScope(mousa.ObservationID(raw)), definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sourceBindingID == observationBindingID {
		t.Fatal("typed scope kinds reused the same binding identity")
	}
	sourceBinding := mousa.PolicyBinding{Schema: mousa.PolicyBindingSchema, ID: sourceBindingID, Namespace: "direct", ExternalBindingID: "binding", ExternalBindingVersion: "1", Scope: sourceScope, PolicyDefinitionID: definition.ID}
	sourceJSON, err := mousa.EncodePolicyBinding(sourceBinding)
	if err != nil {
		t.Fatal(err)
	}
	deployment := testPolicyBinding(t, "direct", "deployment", "1", definition.ID)
	deploymentJSON, err := mousa.EncodePolicyBinding(deployment)
	if err != nil {
		t.Fatal(err)
	}
	activation := testPolicyActivation(t, "direct", "deployment", "activate", nil, &deployment.ID)
	activationJSON, err := mousa.EncodePolicyActivation(activation)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("identity_and_contracts", func(t *testing.T) {
		zeroID := strings.Repeat("0", 64)
		for _, test := range []struct {
			name string
			data []byte
		}{
			{"deployment extraneous subject", bytes.Replace(deploymentJSON, []byte(`"kind":"deployment"`), []byte(`"kind":"deployment","id":"`+zeroID+`"`), 1)},
			{"typed scope missing subject", bytes.Replace(sourceJSON, []byte(`,"id":"`+sourceID.String()+`"`), nil, 1)},
			{"typed scope zero subject", bytes.Replace(sourceJSON, []byte(sourceID.String()), []byte(zeroID), 1)},
			{"typed scope malformed subject", bytes.Replace(sourceJSON, []byte(sourceID.String()), []byte("bad"), 1)},
			{"typed scope extraneous subject", bytes.Replace(sourceJSON, []byte(`}`), []byte(`,"extra":true}`), 1)},
			{"unknown kind", bytes.Replace(sourceJSON, []byte(`"kind":"source"`), []byte(`"kind":"unknown"`), 1)},
			{"unknown top-level field", bytes.Replace(deploymentJSON, []byte("}\n"), []byte(",\"extra\":true}\n"), 1)},
			{"missing required field", bytes.Replace(deploymentJSON, []byte(`"namespace":"direct",`), nil, 1)},
			{"trailing value", append(append([]byte(nil), deploymentJSON...), []byte(`{}`)...)},
		} {
			t.Run(test.name, func(t *testing.T) {
				if _, err := mousa.DecodePolicyBinding(test.data); err == nil {
					t.Fatal("invalid policy binding was accepted")
				}
			})
		}
		whitespace := bytes.Replace(deploymentJSON, []byte(`"scope":`), []byte(`"scope" : `), 1)
		if _, err := mousa.DecodePolicyBinding(whitespace); err != nil {
			t.Fatalf("valid JSON whitespace rejected: %v", err)
		}
		for _, test := range []struct {
			name  string
			parse func(string) error
		}{
			{"binding malformed", func(value string) error { _, err := mousa.ParsePolicyBindingID(value); return err }},
			{"binding uppercase", func(string) error {
				_, err := mousa.ParsePolicyBindingID(strings.ToUpper(deployment.ID.String()))
				return err
			}},
			{"activation malformed", func(value string) error { _, err := mousa.ParsePolicyActivationID(value); return err }},
			{"activation uppercase", func(string) error {
				_, err := mousa.ParsePolicyActivationID(strings.ToUpper(activation.ID.String()))
				return err
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				if err := test.parse("bad"); err == nil {
					t.Fatal("invalid ID was accepted")
				}
			})
		}
	})

	t.Run("zero_id_boundary", func(t *testing.T) {
		zeroID := strings.Repeat("0", 64)
		if got, err := mousa.ParsePolicyBindingID(zeroID); err != nil || got != (mousa.PolicyBindingID{}) {
			t.Fatalf("ParsePolicyBindingID(zero) = %v, %v", got, err)
		}
		if got, err := mousa.ParsePolicyActivationID(zeroID); err != nil || got != (mousa.PolicyActivationID{}) {
			t.Fatalf("ParsePolicyActivationID(zero) = %v, %v", got, err)
		}
		for _, test := range []struct {
			name   string
			field  string
			decode func() error
		}{
			{"binding record ID", "id", func() error {
				_, err := mousa.DecodePolicyBinding(bytes.Replace(deploymentJSON, []byte(deployment.ID.String()), []byte(zeroID), 1))
				return err
			}},
			{"activation record ID", "id", func() error {
				_, err := mousa.DecodePolicyActivation(bytes.Replace(activationJSON, []byte(activation.ID.String()), []byte(zeroID), 1))
				return err
			}},
			{"typed scope subject ID", "scope.id", func() error {
				_, err := mousa.DecodePolicyBinding(bytes.Replace(sourceJSON, []byte(sourceID.String()), []byte(zeroID), 1))
				return err
			}},
			{"activation predecessor ID", "expected_previous_activation_id", func() error {
				_, err := mousa.DecodePolicyActivation(bytes.Replace(activationJSON, []byte(`"expected_previous_activation_id":null`), []byte(`"expected_previous_activation_id":"`+zeroID+`"`), 1))
				return err
			}},
			{"activation selected binding ID", "active_binding_id", func() error {
				_, err := mousa.DecodePolicyActivation(bytes.Replace(activationJSON, []byte(deployment.ID.String()), []byte(zeroID), 1))
				return err
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				requireMousaValidationField(t, test.decode(), mousa.ValidationCodeInvalidID, test.field)
			})
		}
	})

	t.Run("duplicate_fields", func(t *testing.T) {
		for _, test := range []struct {
			name string
			data []byte
		}{
			{"binding top level", bytes.Replace(deploymentJSON, []byte(`"namespace":"direct"`), []byte(`"namespace":"direct","namespace":"direct"`), 1)},
			{"binding nested scope", bytes.Replace(deploymentJSON, []byte(`"kind":"deployment"`), []byte(`"kind":"deployment","kind":"deployment"`), 1)},
			{"activation top level", bytes.Replace(activationJSON, []byte(`"actor_id":"actor"`), []byte(`"actor_id":"actor","actor_id":"actor"`), 1)},
		} {
			t.Run(test.name, func(t *testing.T) {
				var err error
				if strings.HasPrefix(test.name, "activation") {
					_, err = mousa.DecodePolicyActivation(test.data)
				} else {
					_, err = mousa.DecodePolicyBinding(test.data)
				}
				requireMousaValidationCode(t, err, mousa.ValidationCodeInvalidJSON)
			})
		}
	})
}

func TestPolicyActivationIdentityExclusionAndConflict(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "activation-identity.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	definition := testPolicyDefinition(t, "identity", "policy", "1", "opaque", "opaque", "definition")
	binding := testPolicyBinding(t, "identity", "series", "1", definition.ID)
	if err := store.PutPolicyDefinition(ctx, definition); err != nil {
		t.Fatal(err)
	}
	if err := store.PutPolicyBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	original := testPolicyActivation(t, "identity", "series", "stable-event", nil, &binding.ID)
	if err := store.ApplyPolicyActivation(ctx, original); err != nil {
		t.Fatal(err)
	}
	originalBytes, err := mousa.EncodePolicyActivation(original)
	if err != nil {
		t.Fatal(err)
	}
	originalState := mousa.PolicyBindingState{Namespace: "identity", ExternalBindingID: "series", CurrentActivationID: original.ID, ActiveBindingID: &binding.ID}
	previous, err := mousa.NewPolicyActivationID("identity", "series", "previous")
	if err != nil {
		t.Fatal(err)
	}
	reason := "changed reason"
	changes := []struct {
		name   string
		mutate func(*mousa.PolicyActivation)
	}{
		{"predecessor null to non-null", func(record *mousa.PolicyActivation) { record.ExpectedPreviousActivationID = &previous }},
		{"selected binding non-null to null", func(record *mousa.PolicyActivation) { record.ActiveBindingID = nil }},
		{"actor ID", func(record *mousa.PolicyActivation) { record.ActorID = "other-actor" }},
		{"actor version", func(record *mousa.PolicyActivation) { record.ActorVersion = "2" }},
		{"occurrence time", func(record *mousa.PolicyActivation) { record.OccurredAtUsec = 2 }},
		{"reason", func(record *mousa.PolicyActivation) { record.Reason = &reason }},
	}
	for _, test := range changes {
		t.Run(test.name, func(t *testing.T) {
			changed := original
			test.mutate(&changed)
			if changed.ID != original.ID {
				t.Fatal("content change altered stable activation identity")
			}
			changedBytes, err := mousa.EncodePolicyActivation(changed)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(changedBytes, originalBytes) {
				t.Fatal("content change preserved canonical bytes")
			}
			if err := store.ApplyPolicyActivation(ctx, changed); !IsCode(err, CodeConflict) {
				t.Fatalf("identity reuse = %v, want conflict", err)
			}
			stored, err := store.GetPolicyActivation(ctx, original.ID)
			if err != nil || !reflect.DeepEqual(stored, original) {
				t.Fatalf("stored activation mutated = %#v, %v", stored, err)
			}
			state, err := store.GetPolicyBindingState(ctx, "identity", "series")
			if err != nil || !reflect.DeepEqual(state, originalState) {
				t.Fatalf("stored state mutated = %#v, %v", state, err)
			}
		})
	}
}

func requireMousaValidationCode(t testing.TB, err error, code mousa.ValidationCode) {
	t.Helper()
	var validationErr *mousa.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want *mousa.ValidationError", err)
	}
	if validationErr.Code != code {
		t.Fatalf("validation code = %q, want %q", validationErr.Code, code)
	}
}

func requireMousaValidationField(t testing.TB, err error, code mousa.ValidationCode, field string) {
	t.Helper()
	var validationErr *mousa.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want *mousa.ValidationError", err)
	}
	if validationErr.Code != code || validationErr.Field != field {
		t.Fatalf("validation error = (%q, %q), want (%q, %q)", validationErr.Code, validationErr.Field, code, field)
	}
}

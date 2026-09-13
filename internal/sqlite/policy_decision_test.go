package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

func testEvaluationRequest(t testing.TB, sourceID mousa.SourceID, externalRequestID string) mousa.PolicyEvaluationRequest {
	t.Helper()
	request := mousa.PolicyEvaluationRequest{
		Schema:            mousa.PolicyEvaluationRequestSchema,
		Action:            mousa.SourceRetrievalAction,
		CallerNamespace:   "example.caller",
		ExternalCallerID:  "caller-1",
		ExternalRequestID: externalRequestID,
		PurposeNamespace:  "example.purpose",
		ExternalPurposeID: "purpose-1",
		SourceID:          sourceID,
		RequestedAtUsec:   100,
	}
	id, err := mousa.NewPolicyEvaluationRequestID(request)
	if err != nil {
		t.Fatalf("NewPolicyEvaluationRequestID: %v", err)
	}
	request.ID = id
	return request
}

func seedEvaluationFixture(t testing.TB, store *Store) mousa.PolicyDefinition {
	t.Helper()
	ctx := context.Background()
	source := testSource(t)
	if err := store.PutSource(ctx, source); err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	inner := mousa.SourceRetrievalPolicy{
		Schema:            mousa.SourceRetrievalPolicySchema,
		Action:            mousa.SourceRetrievalAction,
		CallerNamespace:   "example.caller",
		ExternalCallerID:  "caller-1",
		PurposeNamespace:  "example.purpose",
		ExternalPurposeID: "purpose-1",
		Effect:            mousa.SourceRetrievalEffectAllow,
	}
	data, err := mousa.EncodeSourceRetrievalPolicy(inner)
	if err != nil {
		t.Fatalf("EncodeSourceRetrievalPolicy: %v", err)
	}
	digest := mousa.SHA256(sha256.Sum256(data))
	definitionID, err := mousa.NewPolicyDefinitionID("example", "allow", "1", mousa.SourceRetrievalPolicyMediaType, mousa.SourceRetrievalPolicySchema, digest)
	if err != nil {
		t.Fatal(err)
	}
	definition := mousa.PolicyDefinition{
		Schema:                mousa.PolicyDefinitionSchema,
		ID:                    definitionID,
		Namespace:             "example",
		ExternalPolicyID:      "allow",
		ExternalPolicyVersion: "1",
		DefinitionMediaType:   mousa.SourceRetrievalPolicyMediaType,
		DefinitionSchema:      mousa.SourceRetrievalPolicySchema,
		DefinitionSHA256:      digest,
		Definition:            string(data),
	}
	if err := store.PutPolicyDefinition(ctx, definition); err != nil {
		t.Fatalf("PutPolicyDefinition: %v", err)
	}
	scope := mousa.NewDeploymentPolicyScope()
	bindingID, err := mousa.NewPolicyBindingID("example", "deployment", "1", scope, definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := mousa.PolicyBinding{Schema: mousa.PolicyBindingSchema, ID: bindingID, Namespace: "example", ExternalBindingID: "deployment", ExternalBindingVersion: "1", Scope: scope, PolicyDefinitionID: definition.ID}
	if err := store.PutPolicyBinding(ctx, binding); err != nil {
		t.Fatalf("PutPolicyBinding: %v", err)
	}
	activation := testPolicyActivation(t, "example", "deployment", "activate", nil, &binding.ID)
	if err := store.ApplyPolicyActivation(ctx, activation); err != nil {
		t.Fatalf("ApplyPolicyActivation: %v", err)
	}
	return definition
}

func TestPolicyDecisionEvaluationAllowDenyAndRetry(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "decisions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedEvaluationFixture(t, store)
	source := testSource(t)

	request := testEvaluationRequest(t, source.ID, "request-1")
	decision, err := store.EvaluateSourceRetrieval(ctx, request)
	if err != nil {
		t.Fatalf("EvaluateSourceRetrieval: %v", err)
	}
	if err := decision.Validate(); err != nil {
		t.Fatalf("stored decision validation: %v", err)
	}
	if decision.Outcome != mousa.PolicyOutcomeDeny || len(decision.ReasonCodes) != 1 || decision.ReasonCodes[0] != mousa.PolicyReasonMissingSourceState {
		t.Fatalf("decision without ingest state = %#v", decision.ReasonCodes)
	}
	if decision.StatePresent || decision.CollectionState != nil || decision.CurrentWithdrawalID != nil {
		t.Fatalf("lifecycle evidence = %#v", decision)
	}
	if len(decision.PolicyInputs) != 1 || decision.PolicyInputs[0].Layer != mousa.PolicyLayerDeployment || decision.PolicyInputs[0].Result != mousa.PolicyInputAllow {
		t.Fatalf("inputs = %#v", decision.PolicyInputs)
	}
	if decision.EvaluatedAtUsec <= 0 {
		t.Fatalf("evaluated_at_usec = %d", decision.EvaluatedAtUsec)
	}

	retry, err := store.EvaluateSourceRetrieval(ctx, request)
	if err != nil || !reflect.DeepEqual(retry, decision) {
		t.Fatalf("exact retry = %#v, %v", retry, err)
	}

	conflicting := request
	conflicting.RequestedAtUsec = 200
	conflicting.ID, err = mousa.NewPolicyEvaluationRequestID(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EvaluateSourceRetrieval(ctx, conflicting); !IsCode(err, CodeConflict) {
		t.Fatalf("conflicting external request identity = %v", err)
	}
	second, err := store.GetPolicyDecision(ctx, decision.ID)
	if err != nil || !reflect.DeepEqual(second, decision) {
		t.Fatalf("GetPolicyDecision after conflict = %#v, %v", second, err)
	}
}

func TestPolicyDecisionEvaluationAllowWithActiveIngestState(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "allow-decisions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedEvaluationFixture(t, store)

	sequence := uint64(1)
	batch := testPushBatch(t, "decision-allow", &sequence)
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatalf("ApplyIngest: %v", err)
	}

	request := testEvaluationRequest(t, batch.Source.ID, "request-allow")
	decision, err := store.EvaluateSourceRetrieval(ctx, request)
	if err != nil {
		t.Fatalf("EvaluateSourceRetrieval: %v", err)
	}
	if decision.Outcome != mousa.PolicyOutcomeAllow || !decision.StatePresent || decision.CurrentWithdrawalID != nil || *decision.CollectionState != mousa.CollectionActive {
		t.Fatalf("allow decision = %#v", decision)
	}
	if len(decision.ReasonCodes) != 1 || decision.ReasonCodes[0] != mousa.PolicyReasonAllow {
		t.Fatalf("allow reasons = %v", decision.ReasonCodes)
	}
	if len(decision.PolicyInputs) != 1 || decision.PolicyInputs[0].Result != mousa.PolicyInputAllow {
		t.Fatalf("inputs = %#v", decision.PolicyInputs)
	}
}

func TestPolicyDecisionEvaluationRejectsInvalidAndReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "readonly-decisions.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedEvaluationFixture(t, store)
	source := testSource(t)

	request := testEvaluationRequest(t, source.ID, "request-readonly")
	decision, err := store.EvaluateSourceRetrieval(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if _, err := readOnly.EvaluateSourceRetrieval(ctx, mousa.PolicyEvaluationRequest{}); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only invalid request = %v, want read_only precedence", err)
	}
	if _, err := readOnly.EvaluateSourceRetrieval(ctx, request); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only valid request = %v", err)
	}
	if _, err := readOnly.GetPolicyDecision(ctx, decision.ID); err != nil {
		t.Fatalf("read-only get = %v", err)
	}

	writable, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writable.Close()
	if _, err := writable.EvaluateSourceRetrieval(ctx, mousa.PolicyEvaluationRequest{}); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("empty request = %v", err)
	}
	unknown := request
	unknown.Action = "evidence.export"
	unknown.ID, _ = mousa.NewPolicyEvaluationRequestID(unknown)
	if _, err := writable.EvaluateSourceRetrieval(ctx, unknown); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("unknown action = %v", err)
	}
	var missingSource mousa.SourceID
	missingSource[0] = 1
	if _, err := writable.EvaluateSourceRetrieval(ctx, testEvaluationRequest(t, missingSource, "request-missing")); !IsCode(err, CodeIntegrity) {
		t.Fatalf("missing typed source = %v", err)
	}
}

func TestPolicyDecisionInputProjectionTamperIsIntegrity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "tampered-decisions.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedEvaluationFixture(t, store)
	sequence := uint64(1)
	batch := testPushBatch(t, "decision-tamper", &sequence)
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EvaluateSourceRetrieval(ctx, testEvaluationRequest(t, batch.Source.ID, "request-1")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	execPolicyTamper(t, path, `DELETE FROM policy_decision_inputs WHERE ordinal = 0`)
	if _, err := Open(ctx, path); !IsCode(err, CodeIntegrity) {
		t.Fatalf("missing input row = %v, want integrity", err)
	}
}

func TestPolicyDecisionMigrationExactSchemaBackupAndUpgrade(t *testing.T) {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 8 || migrations[6].version != 7 || migrations[6].name != "policy_decisions" {
		t.Fatalf("migration 7 = %#v", migrations)
	}
	// Frozen like migrations 3-6: any later edit to v7 bytes is a new migration, not a repair.
	if len(migrations[6].sql) != 2576 || fmt.Sprintf("%x", migrations[6].hash) != "4457d69654acf14b01bc1e9ed5f5f5a3ae73a6d3bb1f27ad9917a8bff12e834f" || migrations[6].sql[len(migrations[6].sql)-1] != '\n' {
		t.Fatalf("migration 7 bytes changed: length %d hash %x", len(migrations[6].sql), migrations[6].hash)
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v6.sqlite")
	createVersionSix(t, path)
	if readOnly, err := OpenReadOnly(ctx, path); readOnly != nil || !IsCode(err, CodeReadOnly) {
		if readOnly != nil {
			readOnly.Close()
		}
		t.Fatalf("OpenReadOnly v6 = %v, %v", readOnly, err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, table := range []string{"policy_decisions", "policy_decision_inputs"} {
		var tableSQL string
		if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&tableSQL); err != nil || !strings.HasSuffix(tableSQL, " STRICT") {
			t.Fatalf("%s schema = %q err=%v", table, tableSQL, err)
		}
	}
	for table, want := range map[string][]string{
		"policy_decisions":       {"id", "request_id", "caller_namespace", "external_caller_id", "external_request_id", "source_id", "outcome", "evaluated_at_usec", "current_withdrawal_id", "record_json"},
		"policy_decision_inputs": {"decision_id", "ordinal", "layer", "activation_id", "binding_id", "definition_id", "result"},
	} {
		rows, err := store.db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			got = append(got, name)
		}
		rows.Close()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s columns = %v, want %v", table, got, want)
		}
	}
	for table, want := range map[string]int{"policy_decisions": 2, "policy_decision_inputs": 4} {
		var got int
		if err := store.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_list(?)`, table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s foreign keys = %d err=%v, want %d", table, got, err, want)
		}
	}
	var storedHash []byte
	if err := store.db.QueryRow(`SELECT sha256 FROM schema_migrations WHERE version = 7 AND name = 'policy_decisions'`).Scan(&storedHash); err != nil || !equalBytes(storedHash, migrations[6].hash[:]) {
		t.Fatalf("stored migration hash = %x err=%v", storedHash, err)
	}
	backupPath := path + ".pre-migrate-v6-to-v7.sqlite"
	backup, err := connect(ctx, backupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := verifyVersion(ctx, backup, migrations, 6, false); err != nil {
		t.Fatalf("v6 backup: %v", err)
	}
}

// TestPolicyDecisionConcurrentExactRetriesProduceOneDecision drives identical requests through
// independent stores sharing one database file, so the writer transactions genuinely contend.
func TestPolicyDecisionConcurrentExactRetriesProduceOneDecision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent-decisions.sqlite")
	seed, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedEvaluationFixture(t, seed)
	source := testSource(t)
	request := testEvaluationRequest(t, source.ID, "request-concurrent")
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	const workers = 4
	stores := make([]*Store, workers)
	for i := range stores {
		stores[i], err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer stores[i].Close()
	}
	decisions := make([]mousa.PolicyDecision, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			store := stores[i]
			for range 50 {
				decision, err := store.EvaluateSourceRetrieval(ctx, request)
				if err == nil {
					decisions[i], errs[i] = decision, nil
					return
				}
				if !IsCode(err, CodeBusy) {
					errs[i] = err
					return
				}
				time.Sleep(time.Millisecond)
			}
			errs[i] = errors.New("exact retry exhausted busy attempts")
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
	for _, decision := range decisions[1:] {
		if !reflect.DeepEqual(decision, decisions[0]) {
			t.Fatalf("decision %#v differs from worker 0 %#v", decision, decisions[0])
		}
	}
	var decisionRows, inputRows int
	if err := stores[0].db.QueryRow(`SELECT count(*) FROM policy_decisions`).Scan(&decisionRows); err != nil {
		t.Fatal(err)
	}
	if err := stores[0].db.QueryRow(`SELECT count(*) FROM policy_decision_inputs`).Scan(&inputRows); err != nil {
		t.Fatal(err)
	}
	if decisionRows != 1 || inputRows != 1 {
		t.Fatalf("rows = %d decisions, %d inputs, want exactly one of each", decisionRows, inputRows)
	}
	stored, err := stores[0].GetPolicyDecision(ctx, decisions[0].ID)
	if err != nil || !reflect.DeepEqual(stored, decisions[0]) {
		t.Fatalf("stored decision = %#v, %v", stored, err)
	}
}

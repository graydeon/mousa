package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
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

func seedEvaluationFixture(t testing.TB, store *Store) {
	t.Helper()
	if err := store.PutSource(context.Background(), testSource(t)); err != nil {
		t.Fatalf("PutSource: %v", err)
	}
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
	if decision.Access != mousa.SourceAccessAllow {
		t.Fatalf("access without a stored decision = %q, want allow", decision.Access)
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
	var decisionRows int
	if err := stores[0].db.QueryRow(`SELECT count(*) FROM policy_decisions`).Scan(&decisionRows); err != nil {
		t.Fatal(err)
	}
	if decisionRows != 1 {
		t.Fatalf("rows = %d decisions, want exactly one", decisionRows)
	}
	stored, err := stores[0].GetPolicyDecision(ctx, decisions[0].ID)
	if err != nil || !reflect.DeepEqual(stored, decisions[0]) {
		t.Fatalf("stored decision = %#v, %v", stored, err)
	}
}

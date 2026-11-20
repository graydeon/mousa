package sqlite

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

// TestSourceAccessAllowDenyAndWithdrawal drives the per-source access decision
// through the enforced search path of a real store.
func TestSourceAccessAllowDenyAndWithdrawal(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "access.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedEvaluationFixture(t, store)
	sequence := uint64(1)
	batch := testPushBatch(t, "access", &sequence)
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatalf("ApplyIngest: %v", err)
	}
	document := addEnforcedDocument(t, store, batch.Observation, "access-body", "harbor access evidence")
	sourceID := batch.Source.ID
	search := func(externalRequestID string) (mousa.PolicyDecision, []mousa.VerifiedLexicalCandidate) {
		t.Helper()
		request := testEvaluationRequest(t, sourceID, externalRequestID)
		decision, err := store.EvaluateSourceRetrieval(ctx, request)
		if err != nil {
			t.Fatalf("EvaluateSourceRetrieval(%s): %v", externalRequestID, err)
		}
		result, err := store.SearchEnforcedLexical(ctx, request, "harbor", 10)
		if err != nil {
			t.Fatalf("SearchEnforcedLexical(%s): %v", externalRequestID, err)
		}
		return decision, result.Candidates
	}
	requireReasons := func(decision mousa.PolicyDecision, outcome mousa.PolicyDecisionOutcome, access mousa.SourceAccess, reasons ...mousa.PolicyDecisionReason) {
		t.Helper()
		if decision.Outcome != outcome || decision.Access != access || !reflect.DeepEqual(decision.ReasonCodes, reasons) {
			t.Fatalf("decision = %s %s %v, want %s %s %v", decision.Outcome, decision.Access, decision.ReasonCodes, outcome, access, reasons)
		}
	}

	decision, candidates := search("default")
	requireReasons(decision, mousa.PolicyOutcomeDeny, mousa.SourceAccessDeny, mousa.PolicyReasonAccessDefault)
	if decision.AccessEventID != nil {
		t.Fatalf("default decision cited an event: %v", decision.AccessEventID)
	}
	if len(candidates) != 0 {
		t.Fatalf("default-denied source released candidates: %#v", candidates)
	}

	allowEvent, changed, err := store.SetSourceAccess(ctx, sourceID, mousa.SourceAccessAllow, "test")
	if err != nil || !changed || allowEvent.Access != mousa.SourceAccessAllow {
		t.Fatalf("SetSourceAccess(allow) = %#v, %v, %v", allowEvent, changed, err)
	}
	decision, candidates = search("allowed")
	requireReasons(decision, mousa.PolicyOutcomeAllow, mousa.SourceAccessAllow, mousa.PolicyReasonAllow)
	if decision.AccessEventID == nil || *decision.AccessEventID != allowEvent.ID {
		t.Fatalf("allow decision cited %v, want %v", decision.AccessEventID, allowEvent.ID)
	}
	if len(candidates) != 1 || candidates[0].Segment.ID != document.segments[0].ID {
		t.Fatalf("allowed candidates = %#v", candidates)
	}

	var denyEvent mousa.AccessEvent
	for _, want := range []bool{true, false} {
		event, changed, err := store.SetSourceAccess(ctx, sourceID, mousa.SourceAccessDeny, "test")
		if err != nil || changed != want {
			t.Fatalf("SetSourceAccess(deny) = %v, %v, want changed %v", changed, err, want)
		}
		if want {
			denyEvent = event
		} else if event != denyEvent {
			t.Fatalf("deny repeat returned %#v, want %#v", event, denyEvent)
		}
	}
	var eventRows int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM access_events WHERE source_id = ?`, sourceID[:]).Scan(&eventRows); err != nil || eventRows != 2 {
		t.Fatalf("access_events rows = %d, %v, want 2", eventRows, err)
	}
	decision, candidates = search("denied")
	requireReasons(decision, mousa.PolicyOutcomeDeny, mousa.SourceAccessDeny, mousa.PolicyReasonAccessDenied)
	if decision.AccessEventID == nil || *decision.AccessEventID != denyEvent.ID {
		t.Fatalf("deny decision cited %v, want %v", decision.AccessEventID, denyEvent.ID)
	}
	if len(candidates) != 0 {
		t.Fatalf("denied source released candidates: %#v", candidates)
	}

	if _, changed, err := store.SetSourceAccess(ctx, sourceID, mousa.SourceAccessAllow, "test"); err != nil || !changed {
		t.Fatalf("SetSourceAccess(allow) = %v, %v", changed, err)
	}
	decision, candidates = search("re-allowed")
	requireReasons(decision, mousa.PolicyOutcomeAllow, mousa.SourceAccessAllow, mousa.PolicyReasonAllow)
	if len(candidates) != 1 {
		t.Fatalf("re-allowed source candidates = %#v", candidates)
	}

	withdrawalID, err := mousa.NewWithdrawalID(sourceID, "access-withdrawal")
	if err != nil {
		t.Fatal(err)
	}
	withdrawal := mousa.SourceWithdrawal{Schema: mousa.SourceWithdrawalSchema, ID: withdrawalID, SourceID: sourceID, ExternalWithdrawalID: "access-withdrawal", AdapterID: "test.adapter", AdapterVersion: "1", OccurredAtUsec: 2}
	if err := store.WithdrawSource(ctx, withdrawal); err != nil {
		t.Fatalf("WithdrawSource: %v", err)
	}
	decision, candidates = search("withdrawn")
	requireReasons(decision, mousa.PolicyOutcomeDeny, mousa.SourceAccessAllow, mousa.PolicyReasonSourceWithdrawn)
	if len(candidates) != 0 {
		t.Fatalf("withdrawn source released candidates: %#v", candidates)
	}

	var missing mousa.SourceID
	missing[0] = 1
	if _, _, err := store.SetSourceAccess(ctx, missing, mousa.SourceAccessDeny, "test"); !IsCode(err, CodeConflict) {
		t.Fatalf("missing source = %v, want conflict", err)
	}
	if _, _, err := store.SetSourceAccess(ctx, sourceID, "maybe", "test"); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("unknown access = %v, want invalid_record", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if _, _, err := readOnly.SetSourceAccess(ctx, sourceID, mousa.SourceAccessDeny, "test"); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only = %v, want read_only", err)
	}
}

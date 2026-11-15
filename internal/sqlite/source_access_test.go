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
	request, document := seedEnforcedRetrieval(t, store, "harbor access evidence")
	sourceID := request.SourceID
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
	requireReasons(decision, mousa.PolicyOutcomeAllow, mousa.SourceAccessAllow, mousa.PolicyReasonAllow)
	if len(candidates) != 1 || candidates[0].Segment.ID != document.segments[0].ID {
		t.Fatalf("default allow candidates = %#v", candidates)
	}

	for _, want := range []bool{true, false} {
		changed, err := store.SetSourceAccess(ctx, sourceID, mousa.SourceAccessDeny)
		if err != nil || changed != want {
			t.Fatalf("SetSourceAccess(deny) = %v, %v, want changed %v", changed, err, want)
		}
	}
	decision, candidates = search("denied")
	requireReasons(decision, mousa.PolicyOutcomeDeny, mousa.SourceAccessDeny, mousa.PolicyReasonAccessDenied)
	if len(candidates) != 0 {
		t.Fatalf("denied source released candidates: %#v", candidates)
	}

	if changed, err := store.SetSourceAccess(ctx, sourceID, mousa.SourceAccessAllow); err != nil || !changed {
		t.Fatalf("SetSourceAccess(allow) = %v, %v", changed, err)
	}
	decision, candidates = search("allowed")
	requireReasons(decision, mousa.PolicyOutcomeAllow, mousa.SourceAccessAllow, mousa.PolicyReasonAllow)
	if len(candidates) != 1 {
		t.Fatalf("allowed source candidates = %#v", candidates)
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
	if _, err := store.SetSourceAccess(ctx, missing, mousa.SourceAccessDeny); !IsCode(err, CodeConflict) {
		t.Fatalf("missing source = %v, want conflict", err)
	}
	if _, err := store.SetSourceAccess(ctx, sourceID, "maybe"); !IsCode(err, CodeInvalidRecord) {
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
	if _, err := readOnly.SetSourceAccess(ctx, sourceID, mousa.SourceAccessDeny); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only = %v, want read_only", err)
	}
}

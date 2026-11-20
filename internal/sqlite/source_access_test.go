package sqlite

import (
	"encoding/hex"
	"fmt"
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

// accessOrderFixture is a real store with one searchable, allowed-then-denied source and a
// second allowed source.
type accessOrderFixture struct {
	store       *Store
	path        string
	source      mousa.SourceID
	other       mousa.SourceID
	allow, deny mousa.AccessEvent
	requests    int
}

func newAccessOrderFixture(t *testing.T) *accessOrderFixture {
	t.Helper()
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "order.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	seedEvaluationFixture(t, store)
	sequence := uint64(1)
	batch := testPushBatch(t, "order", &sequence)
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatalf("ApplyIngest: %v", err)
	}
	addEnforcedDocument(t, store, batch.Observation, "order-body", "harbor order evidence")
	otherID, err := mousa.NewSourceID("test", "source-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutSource(ctx, mousa.Source{Schema: mousa.SourceSchema, ID: otherID, Namespace: "test", ExternalSourceID: "source-2"}); err != nil {
		t.Fatal(err)
	}
	f := &accessOrderFixture{store: store, path: path, source: batch.Source.ID, other: otherID}
	f.allow = f.set(t, f.source, mousa.SourceAccessAllow)
	f.set(t, f.other, mousa.SourceAccessAllow)
	f.deny = f.set(t, f.source, mousa.SourceAccessDeny)
	return f
}

func (f *accessOrderFixture) set(t *testing.T, sourceID mousa.SourceID, access mousa.SourceAccess) mousa.AccessEvent {
	t.Helper()
	event, changed, err := f.store.SetSourceAccess(t.Context(), sourceID, access, "test")
	if err != nil || !changed {
		t.Fatalf("SetSourceAccess(%s) = %v, %v", access, changed, err)
	}
	return event
}

func (f *accessOrderFixture) query(t *testing.T, sourceID mousa.SourceID) (TracedLexicalResult, error) {
	t.Helper()
	f.requests++
	request := testEvaluationRequest(t, sourceID, fmt.Sprintf("order-%d", f.requests))
	return f.store.EvaluateAndTraceLexical(t.Context(), request, "harbor", 10, 8192, mousa.PackingOriginal)
}

func (f *accessOrderFixture) seq(t *testing.T, id mousa.AccessEventID) int64 {
	t.Helper()
	var seq int64
	if err := f.store.db.QueryRowContext(t.Context(), `SELECT seq FROM log WHERE kind = 'access_event' AND record_id = ?`, id[:]).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	return seq
}

func (f *accessOrderFixture) requireRefused(t *testing.T) {
	t.Helper()
	result, err := f.query(t, f.source)
	if !IsCode(err, CodeIntegrity) {
		t.Fatalf("query = %v, want integrity", err)
	}
	if !reflect.DeepEqual(result, TracedLexicalResult{}) {
		t.Fatalf("refused query released %#v", result)
	}
	other, err := f.query(t, f.other)
	if err != nil || other.Decision.Access != mousa.SourceAccessAllow {
		t.Fatalf("other source query = %v, %v, want allow", other.Decision.Access, err)
	}
}

// TestAccessEventTamperingIsRefusedAtRead edits the access history of one source of an open
// store with raw SQL. Every edit is refused by the next query on that source only.
func TestAccessEventTamperingIsRefusedAtRead(t *testing.T) {
	t.Run("swap allow and deny seq", func(t *testing.T) {
		f := newAccessOrderFixture(t)
		allowSeq, denySeq := f.seq(t, f.allow.ID), f.seq(t, f.deny.ID)
		rawExec(t, f.path, fmt.Sprintf(`UPDATE log SET seq = 1000 WHERE seq = %[1]d; UPDATE log SET seq = %[1]d WHERE seq = %[2]d; UPDATE log SET seq = %[2]d WHERE seq = 1000`, allowSeq, denySeq))
		f.requireRefused(t)
		if err := f.store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(t.Context(), f.path)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		f.store = reopened
		f.requireRefused(t)
	})
	t.Run("delete last leaf only", func(t *testing.T) {
		f := newAccessOrderFixture(t)
		rawExec(t, f.path, fmt.Sprintf(`DELETE FROM log WHERE seq = %d`, f.seq(t, f.deny.ID)))
		f.requireRefused(t)
	})
	t.Run("delete last row only", func(t *testing.T) {
		f := newAccessOrderFixture(t)
		rawExec(t, f.path, fmt.Sprintf(`DELETE FROM access_events WHERE id = X'%s'`, hex.EncodeToString(f.deny.ID[:])))
		f.requireRefused(t)
	})
}

// TestAccessEventTimesIncreaseWithSeq freezes and then rewinds the clock between changes: each
// stored time is at least the previous one plus 1, and the read passes.
func TestAccessEventTimesIncreaseWithSeq(t *testing.T) {
	ctx := t.Context()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "clock.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedEvaluationFixture(t, store)
	sourceID := testSource(t).ID
	steps := []struct {
		access mousa.SourceAccess
		now    int64
		want   int64
	}{
		{mousa.SourceAccessAllow, 1000, 1000},
		{mousa.SourceAccessDeny, 1000, 1001},
		{mousa.SourceAccessAllow, 1000, 1002},
		{mousa.SourceAccessDeny, 10, 1003},
		{mousa.SourceAccessAllow, 5000, 5000},
	}
	for i, step := range steps {
		event, changed, err := store.setSourceAccess(ctx, sourceID, step.access, "test", false, step.now)
		if err != nil || !changed || event.OccurredAtUsec != step.want {
			t.Fatalf("step %d: event at %d, changed %v, err %v; want at %d", i, event.OccurredAtUsec, changed, err, step.want)
		}
		decision, err := store.EvaluateSourceRetrieval(ctx, testEvaluationRequest(t, sourceID, fmt.Sprintf("clock-%d", i)))
		if err != nil || decision.Access != step.access || *decision.AccessEventID != event.ID {
			t.Fatalf("step %d: decision %v %v, err %v", i, decision.Access, decision.AccessEventID, err)
		}
	}
}

// TestCurrentAccessEventUntouchedHistory reads the latest of 1, 2 and 50 real changes.
func TestCurrentAccessEventUntouchedHistory(t *testing.T) {
	for _, n := range []int{1, 2, 50} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			ctx := t.Context()
			store, err := Open(ctx, filepath.Join(t.TempDir(), "history.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			seedEvaluationFixture(t, store)
			sourceID := testSource(t).ID
			var last mousa.AccessEvent
			for i := range n {
				access := mousa.SourceAccessAllow
				if i%2 == 1 {
					access = mousa.SourceAccessDeny
				}
				if last, _, err = store.SetSourceAccess(ctx, sourceID, access, "test"); err != nil {
					t.Fatal(err)
				}
			}
			got, err := currentAccessEvent(ctx, store.db, sourceID)
			if err != nil || got == nil || *got != last {
				t.Fatalf("currentAccessEvent = %v, %v, want %v", got, err, last)
			}
		})
	}
}

// BenchmarkCurrentAccessEvent times the per-query access read for 1, 10 and 100 events.
func BenchmarkCurrentAccessEvent(b *testing.B) {
	for _, n := range []int{1, 10, 100} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ctx := b.Context()
			store, err := Open(ctx, filepath.Join(b.TempDir(), "bench.sqlite"))
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()
			seedEvaluationFixture(b, store)
			sourceID := testSource(b).ID
			for i := range n {
				access := mousa.SourceAccessAllow
				if i%2 == 1 {
					access = mousa.SourceAccessDeny
				}
				if _, _, err := store.setSourceAccess(ctx, sourceID, access, "bench", false, int64(i+1)); err != nil {
					b.Fatal(err)
				}
			}
			for b.Loop() {
				if _, err := currentAccessEvent(ctx, store.db, sourceID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

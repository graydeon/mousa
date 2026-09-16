package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestTraceEnforcedLexicalRecordsAndRepeats(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	request, document := seedEnforcedRetrieval(t, store, "sharedterm enforced evidence")
	decision, err := store.EvaluateSourceRetrieval(ctx, request)
	if err != nil || decision.Outcome != mousa.PolicyOutcomeAllow {
		t.Fatalf("EvaluateSourceRetrieval = %#v, %v", decision, err)
	}

	result, err := store.TraceEnforcedLexical(ctx, request, "sharedterm", 10, 1<<20, "original")
	if err != nil {
		t.Fatalf("TraceEnforcedLexical: %v", err)
	}
	if result.Trail.Outcome != "allow" || result.Trail.Expression != "sharedterm" {
		t.Fatalf("trail = %#v", result.Trail)
	}
	if len(result.Candidates) == 0 || result.Candidates[0].Segment.ID != document.segments[0].ID {
		t.Fatalf("traced candidates = %#v", result.Candidates)
	}
	if err := result.Trail.Validate(); err != nil {
		t.Fatalf("trail validate: %v", err)
	}
	selected := 0
	for _, candidate := range result.Trail.Candidates {
		if candidate.Selected {
			selected++
		}
	}
	if selected == 0 {
		t.Fatal("no candidate selected in the trail")
	}

	stored, err := store.GetSourceTrail(ctx, result.Trail.ID)
	if err != nil || !reflect.DeepEqual(stored, result.Trail) {
		t.Fatalf("GetSourceTrail = %#v, %v", stored, err)
	}

	repeat, err := store.TraceEnforcedLexical(ctx, request, "sharedterm", 10, 1<<20, "original")
	if err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if !reflect.DeepEqual(repeat.Trail, result.Trail) {
		t.Fatal("exact retry produced a different trail")
	}
}

func TestTraceEnforcedLexicalRecordsDenyDecision(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "trail-deny.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedEvaluationFixture(t, store)
	source := testSource(t)
	request := testEvaluationRequest(t, source.ID, "request-deny")
	decision, err := store.EvaluateSourceRetrieval(ctx, request)
	if err != nil || decision.Outcome != mousa.PolicyOutcomeDeny {
		t.Fatalf("EvaluateSourceRetrieval = %#v, %v", decision, err)
	}
	result, err := store.TraceEnforcedLexical(ctx, request, "anything", 10, 64, "original")
	if err != nil {
		t.Fatalf("deny trace: %v", err)
	}
	if result.Trail.Outcome != "deny" || len(result.Trail.Candidates) != 0 || result.Trail.UsedBytes != 0 {
		t.Fatalf("deny trail = %#v", result.Trail)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("deny candidates = %#v, want empty", result.Candidates)
	}
	if _, err := store.GetSourceTrail(ctx, result.Trail.ID); err != nil {
		t.Fatalf("GetSourceTrail deny: %v", err)
	}
}

func TestTraceEnforcedLexicalPrecedenceAndReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trail-prec.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := seedEnforcedRetrieval(t, store, "sharedterm enforced evidence")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if _, err := readOnly.TraceEnforcedLexical(ctx, request, "sharedterm", 10, 64, "original"); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only trace error = %v, want read_only", err)
	}

	writable, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writable.Close()
	if _, err := writable.TraceEnforcedLexical(ctx, mousa.PolicyEvaluationRequest{}, "sharedterm", 10, 64, "original"); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("invalid request error = %v, want invalid_record", err)
	}
	if _, err := writable.TraceEnforcedLexical(ctx, request, "sharedterm", 0, 64, "original"); !IsCode(err, CodeInvalidQuery) {
		t.Fatalf("invalid limit error = %v, want invalid_query", err)
	}
	if _, err := writable.TraceEnforcedLexical(ctx, request, "sharedterm", 10, 0, "original"); !IsCode(err, CodeInvalidQuery) {
		t.Fatalf("zero budget error = %v, want invalid_query", err)
	}
	if _, err := writable.TraceEnforcedLexical(ctx, request, "sharedterm", 10, 64, "original"); !IsCode(err, CodeNotFound) {
		t.Fatalf("never-evaluated error = %v, want not_found", err)
	}
}

func TestSourceTrailTamperFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	request, _ := seedEnforcedRetrieval(t, store, "sharedterm enforced evidence")
	if _, err := store.EvaluateSourceRetrieval(ctx, request); err != nil {
		t.Fatal(err)
	}
	result, err := store.TraceEnforcedLexical(ctx, request, "sharedterm", 10, 1<<20, "original")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	rawExec(t, store.path, `UPDATE source_trails SET outcome = 'deny' WHERE id = x'`+result.Trail.ID.String()+`'`)
	if _, err := OpenReadOnly(ctx, store.path); !IsCode(err, CodeIntegrity) {
		t.Fatalf("tampered outcome open error = %v, want integrity", err)
	}

	rawExec(t, store.path, `UPDATE source_trails SET outcome = 'allow' WHERE id = x'`+result.Trail.ID.String()+`'`)
	rawExec(t, store.path, `DELETE FROM source_trail_candidates WHERE ordinal = 0 AND trail_id = x'`+result.Trail.ID.String()+`'`)
	if _, err := OpenReadOnly(ctx, store.path); !IsCode(err, CodeIntegrity) {
		t.Fatalf("missing candidate row open error = %v, want integrity", err)
	}

	// The deleted candidate row cannot be restored from SQL alone; tamper the disposition of the
	// remaining rows instead, which the reader must compare against the record.
	rawExec(t, store.path, `UPDATE source_trail_candidates SET disposition = 'rejected' WHERE trail_id = x'`+result.Trail.ID.String()+`' AND ordinal = 0`)
	if _, err := OpenReadOnly(ctx, store.path); !IsCode(err, CodeIntegrity) {
		t.Fatalf("tampered disposition open error = %v, want integrity", err)
	}

	// Finally tamper the packet_id projection.
	var otherPacket mousa.ContextPacketID
	copy(otherPacket[:], result.Trail.PacketID[:])
	otherPacket[0] ^= 0xff
	rawExec(t, store.path, `UPDATE source_trails SET packet_id = x'`+otherPacket.String()+`' WHERE id = x'`+result.Trail.ID.String()+`'`)
	if _, err := OpenReadOnly(ctx, store.path); !IsCode(err, CodeIntegrity) {
		t.Fatalf("tampered packet_id open error = %v, want integrity", err)
	}
}

func TestSourceTrailCandidateRowTamperFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	request, _ := seedEnforcedRetrieval(t, store, "sharedterm enforced evidence")
	if _, err := store.EvaluateSourceRetrieval(ctx, request); err != nil {
		t.Fatal(err)
	}
	result, err := store.TraceEnforcedLexical(ctx, request, "sharedterm", 10, 1<<20, "original")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Tamper only a candidate row column; the record and other projections stay consistent, so the
	// ordered row comparison is the only check that can detect this.
	rawExec(t, store.path, `UPDATE source_trail_candidates SET final_rank = final_rank + 1 WHERE trail_id = x'`+result.Trail.ID.String()+`' AND ordinal = 0`)
	if _, err := OpenReadOnly(ctx, store.path); !IsCode(err, CodeIntegrity) {
		t.Fatalf("tampered final_rank open error = %v, want integrity", err)
	}
}

func TestEvaluateAndTraceRollsBackDecisionOnTraceFailure(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	request, _ := seedEnforcedRetrieval(t, store, "sharedterm evidence")
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER refuse_trail BEFORE INSERT ON source_trails
		BEGIN SELECT RAISE(ABORT, 'trace storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EvaluateAndTraceLexical(ctx, request, "sharedterm", 10, 1024, "original"); err == nil {
		t.Fatal("trace storage failure was ignored")
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER refuse_trail`); err != nil {
		t.Fatal(err)
	}
	withdrawVerifiedSource(t, store, request.SourceID, "after-failed-trace", 200)
	result, err := store.EvaluateAndTraceLexical(ctx, request, "sharedterm", 10, 1024, "original")
	if err != nil {
		t.Fatalf("failed trace consumed its request identity: %v", err)
	}
	if result.Decision.Outcome != mousa.PolicyOutcomeDeny || len(result.Candidates) != 0 {
		t.Fatalf("retry reused authorization from before withdrawal: %#v", result)
	}
	if _, err := store.EvaluateAndTraceLexical(ctx, request, "sharedterm", 10, 1024, "original"); !IsCode(err, CodeConflict) {
		t.Fatalf("current evaluation accepted a historical request: %v", err)
	}
}

func TestInspectSourceTrailOmitsHistoricalRejectedMetadata(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	request, document := seedEnforcedRetrieval(t, store, "sharedterm rejected evidence")
	if _, err := store.EvaluateSourceRetrieval(ctx, request); err != nil {
		t.Fatal(err)
	}
	withdrawal := withdrawVerifiedSource(t, store, request.SourceID, "before-historical-trace", 200)
	traced, err := store.TraceEnforcedLexical(ctx, request, "sharedterm", 10, 1024, "original")
	if err != nil {
		t.Fatal(err)
	}
	if len(traced.Trail.Candidates) != 1 || traced.Trail.Candidates[0].Disposition != mousa.CandidateRejected {
		t.Fatalf("fixture did not record a historical rejection: %#v", traced.Trail)
	}
	source, err := store.GetSource(ctx, request.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	resumeVerifiedSource(t, store, source, withdrawal, "inspection-resume", 300)
	inspection, err := store.InspectSourceTrail(ctx, testEvaluationRequest(t, source.ID, "inspect-rejection"), traced.Trail.ID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Historical == nil || inspection.Historical.LifecycleExcluded != 1 || len(inspection.Historical.Candidates) != 0 {
		t.Fatalf("historical rejection was not filtered: %#v", inspection)
	}
	data, err := json.Marshal(inspection)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{document.segments[0].ID.String(), document.segments[0].ContentSHA256.String(), "sharedterm rejected evidence"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("inspection released rejected metadata: %s", data)
		}
	}
}

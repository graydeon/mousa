package sqlite

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestApplyIngestPullAdvancesCheckpointAtomically(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "ingest.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	source, observation, _, _, _, _ := testRecordGraph(t)
	batch := mousa.IngestBatch{
		AdapterID:      "test.adapter",
		AdapterVersion: "1",
		Initiative:     mousa.InitiativePull,
		Form:           mousa.FormItem,
		CapturedAtUsec: 1,
		Checkpoint: &mousa.CheckpointAdvance{
			Next: []byte("checkpoint-1"),
		},
		Source:      source,
		Observation: observation,
	}
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatalf("ApplyIngest: %v", err)
	}
	if _, err := store.GetSource(ctx, source.ID); err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if _, err := store.GetObservation(ctx, observation.ID); err != nil {
		t.Fatalf("GetObservation: %v", err)
	}
	state, err := store.GetIngestState(ctx, source.ID)
	if err != nil {
		t.Fatalf("GetIngestState: %v", err)
	}
	if !reflect.DeepEqual(state.Checkpoint, []byte("checkpoint-1")) || state.LastObservationID == nil || *state.LastObservationID != observation.ID {
		t.Fatalf("state = %#v, want committed checkpoint and observation", state)
	}
}

func TestApplyIngestPullCASRollbackAndOldReplay(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mousa.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testIngestBatch(t, "pull-1")
	first.Checkpoint = &mousa.CheckpointAdvance{Next: []byte("one")}
	if err := store.ApplyIngest(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := testIngestBatch(t, "pull-2")
	second.Checkpoint = &mousa.CheckpointAdvance{Expected: []byte("one"), Next: []byte("two")}
	if err := store.ApplyIngest(ctx, second); err != nil {
		t.Fatal(err)
	}
	bad := testIngestBatch(t, "pull-bad")
	bad.Checkpoint = &mousa.CheckpointAdvance{Expected: []byte("stale"), Next: []byte("three")}
	if err := store.ApplyIngest(ctx, bad); !IsCode(err, CodeConflict) {
		t.Fatalf("mismatch = %v", err)
	}
	if _, err := store.GetObservation(ctx, bad.Observation.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("partial observation = %v", err)
	}
	oversized := testIngestBatch(t, "oversized-checkpoint")
	oversized.Checkpoint = &mousa.CheckpointAdvance{Expected: []byte("two"), Next: make([]byte, 4097)}
	if err := store.ApplyIngest(ctx, oversized); !IsCode(err, CodeResourceLimit) {
		t.Fatalf("oversized checkpoint = %v", err)
	}
	if err := store.ApplyIngest(ctx, first); err != nil {
		t.Fatalf("old exact replay: %v", err)
	}
	state, err := store.GetIngestState(ctx, first.Source.ID)
	if err != nil || string(state.Checkpoint) != "two" {
		t.Fatalf("state = %#v err=%v", state, err)
	}
}

func TestApplyIngestPushGapLateAndUint64Boundary(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mousa.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sequence := uint64(10)
	first := testPushBatch(t, "push-10", &sequence)
	if err := store.ApplyIngest(ctx, first); err != nil {
		t.Fatal(err)
	}
	sequence = 11
	contiguous := testPushBatch(t, "push-11", &sequence)
	if err := store.ApplyIngest(ctx, contiguous); err != nil {
		t.Fatal(err)
	}
	sequence = 13
	gap := testPushBatch(t, "push-13", &sequence)
	if err := store.ApplyIngest(ctx, gap); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("missing gap = %v", err)
	}
	gap.Gaps = []mousa.SequenceGap{{Start: 11, End: 13}}
	if err := store.ApplyIngest(ctx, gap); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("wrong gap = %v", err)
	}
	gap.Gaps = []mousa.SequenceGap{{Start: 12, End: 13}}
	if err := store.ApplyIngest(ctx, gap); err != nil {
		t.Fatal(err)
	}
	sequence = 12
	late := testPushBatch(t, "push-12", &sequence)
	if err := store.ApplyIngest(ctx, late); err != nil {
		t.Fatal(err)
	}
	sequence = math.MaxUint64
	max := testPushBatch(t, "push-max", &sequence)
	max.Gaps = []mousa.SequenceGap{{Start: 14, End: math.MaxUint64}}
	if err := store.ApplyIngest(ctx, max); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetIngestState(ctx, first.Source.ID)
	if err != nil || state.HighSequence == nil || *state.HighSequence != math.MaxUint64 {
		t.Fatalf("state = %#v err=%v", state, err)
	}
	var gapCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM ingest_gaps`).Scan(&gapCount); err != nil || gapCount != 2 {
		t.Fatalf("gaps = %d err=%v", gapCount, err)
	}
}

func TestWithdrawSourceRequiresExplicitResumeAndPreservesOldReplay(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mousa.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first := testIngestBatch(t, "before-withdrawal")
	first.Checkpoint = &mousa.CheckpointAdvance{Next: []byte("one")}
	if err := store.ApplyIngest(ctx, first); err != nil {
		t.Fatal(err)
	}
	id, err := mousa.NewWithdrawalID(first.Source.ID, "withdrawal-1")
	if err != nil {
		t.Fatal(err)
	}
	withdrawal := mousa.SourceWithdrawal{Schema: mousa.SourceWithdrawalSchema, ID: id, SourceID: first.Source.ID, ExternalWithdrawalID: "withdrawal-1", AdapterID: "test.adapter", AdapterVersion: "1", OccurredAtUsec: 2}
	if err := store.WithdrawSource(ctx, withdrawal); err != nil {
		t.Fatal(err)
	}
	if err := store.WithdrawSource(ctx, withdrawal); err != nil {
		t.Fatalf("withdraw replay: %v", err)
	}
	conflictingWithdrawal := withdrawal
	conflictingWithdrawal.AdapterVersion = "2"
	if err := store.WithdrawSource(ctx, conflictingWithdrawal); !IsCode(err, CodeConflict) {
		t.Fatalf("conflicting withdrawal reuse = %v", err)
	}
	secondID, err := mousa.NewWithdrawalID(first.Source.ID, "withdrawal-2")
	if err != nil {
		t.Fatal(err)
	}
	second := withdrawal
	second.ID = secondID
	second.ExternalWithdrawalID = "withdrawal-2"
	if err := store.WithdrawSource(ctx, second); !IsCode(err, CodeConflict) {
		t.Fatalf("second withdrawal = %v", err)
	}
	blocked := testIngestBatch(t, "blocked")
	blocked.Checkpoint = &mousa.CheckpointAdvance{Expected: []byte("one"), Next: []byte("two")}
	if err := store.ApplyIngest(ctx, blocked); !IsCode(err, CodeConflict) {
		t.Fatalf("missing resume = %v", err)
	}
	if err := store.ApplyIngest(ctx, first); err != nil {
		t.Fatalf("old replay: %v", err)
	}
	state, err := store.GetIngestState(ctx, first.Source.ID)
	if err != nil || state.CollectionState != mousa.CollectionWithdrawn {
		t.Fatalf("old replay resumed: %#v err=%v", state, err)
	}
	wrongID, _ := mousa.NewWithdrawalID(first.Source.ID, "wrong-resume")
	blocked.ResumeWithdrawalID = &wrongID
	if err := store.ApplyIngest(ctx, blocked); !IsCode(err, CodeConflict) {
		t.Fatalf("wrong resume = %v", err)
	}
	blocked.ResumeWithdrawalID = &id
	if err := store.ApplyIngest(ctx, blocked); err != nil {
		t.Fatalf("resume: %v", err)
	}
	state, err = store.GetIngestState(ctx, first.Source.ID)
	if err != nil || state.CollectionState != mousa.CollectionActive || state.CurrentWithdrawalID != nil {
		t.Fatalf("active state: %#v err=%v", state, err)
	}
}

func TestWithdrawSourceRejectsHistoricalReplayWhenDifferentWithdrawalCurrent(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mousa.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first := testIngestBatch(t, "historical-withdrawal-first")
	first.Checkpoint = &mousa.CheckpointAdvance{Next: []byte("one")}
	if err := store.ApplyIngest(ctx, first); err != nil {
		t.Fatal(err)
	}
	firstID, err := mousa.NewWithdrawalID(first.Source.ID, "historical-withdrawal-1")
	if err != nil {
		t.Fatal(err)
	}
	firstWithdrawal := mousa.SourceWithdrawal{Schema: mousa.SourceWithdrawalSchema, ID: firstID, SourceID: first.Source.ID, ExternalWithdrawalID: "historical-withdrawal-1", AdapterID: "test.adapter", AdapterVersion: "1", OccurredAtUsec: 2}
	if err := store.WithdrawSource(ctx, firstWithdrawal); err != nil {
		t.Fatal(err)
	}

	resume := testIngestBatch(t, "historical-withdrawal-resume")
	resume.CapturedAtUsec = 3
	resume.Checkpoint = &mousa.CheckpointAdvance{Expected: []byte("one"), Next: []byte("two")}
	resume.ResumeWithdrawalID = &firstID
	if err := store.ApplyIngest(ctx, resume); err != nil {
		t.Fatal(err)
	}
	secondID, err := mousa.NewWithdrawalID(first.Source.ID, "historical-withdrawal-2")
	if err != nil {
		t.Fatal(err)
	}
	secondWithdrawal := firstWithdrawal
	secondWithdrawal.ID = secondID
	secondWithdrawal.ExternalWithdrawalID = "historical-withdrawal-2"
	secondWithdrawal.OccurredAtUsec = 4
	if err := store.WithdrawSource(ctx, secondWithdrawal); err != nil {
		t.Fatal(err)
	}

	if err := store.WithdrawSource(ctx, firstWithdrawal); !IsCode(err, CodeConflict) {
		t.Fatalf("historical withdrawal replay = %v, want conflict", err)
	}
	state, err := store.GetIngestState(ctx, first.Source.ID)
	if err != nil || state.CollectionState != mousa.CollectionWithdrawn || state.CurrentWithdrawalID == nil || *state.CurrentWithdrawalID != secondID {
		t.Fatalf("state after replay = %#v err=%v", state, err)
	}
}

func TestOpenRejectsCrossSourceCurrentWithdrawalProjection(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mousa.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}

	first := testPushBatch(t, "cross-source-first", nil)
	if err := store.ApplyIngest(ctx, first); err != nil {
		t.Fatal(err)
	}
	secondSource := testSourceNamed(t, "cross-source-second")
	secondObservationID, err := mousa.NewObservationID(secondSource.ID, "cross-source-second")
	if err != nil {
		t.Fatal(err)
	}
	second := mousa.IngestBatch{AdapterID: "test.adapter", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem, CapturedAtUsec: 1, Source: secondSource, Observation: mousa.Observation{Schema: mousa.ObservationSchema, ID: secondObservationID, SourceID: secondSource.ID, ExternalObservationID: "cross-source-second"}}
	if err := store.ApplyIngest(ctx, second); err != nil {
		t.Fatal(err)
	}
	firstWithdrawalID, _ := mousa.NewWithdrawalID(first.Source.ID, "cross-source-first")
	firstWithdrawal := mousa.SourceWithdrawal{Schema: mousa.SourceWithdrawalSchema, ID: firstWithdrawalID, SourceID: first.Source.ID, ExternalWithdrawalID: "cross-source-first", AdapterID: "test.adapter", AdapterVersion: "1", OccurredAtUsec: 2}
	if err := store.WithdrawSource(ctx, firstWithdrawal); err != nil {
		t.Fatal(err)
	}
	secondWithdrawalID, _ := mousa.NewWithdrawalID(second.Source.ID, "cross-source-second")
	secondWithdrawal := mousa.SourceWithdrawal{Schema: mousa.SourceWithdrawalSchema, ID: secondWithdrawalID, SourceID: second.Source.ID, ExternalWithdrawalID: "cross-source-second", AdapterID: "test.adapter", AdapterVersion: "1", OccurredAtUsec: 2}
	if err := store.WithdrawSource(ctx, secondWithdrawal); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	rawExec(t, path, `UPDATE source_ingest_state SET current_withdrawal_id = (SELECT id FROM source_withdrawals WHERE source_id = x'`+second.Source.ID.String()+`') WHERE source_id = x'`+first.Source.ID.String()+`'`)
	reopened, err := Open(ctx, path)
	if reopened != nil {
		reopened.Close()
	}
	if reopened != nil || !IsCode(err, CodeIntegrity) {
		t.Fatalf("Open = %v, %v, want integrity", reopened, err)
	}
}

func TestReadOnlyIngestMethodsRejectBeforeValidation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mousa.sqlite")
	store, err := Open(ctx, path)
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
	if err := readOnly.ApplyIngest(ctx, mousa.IngestBatch{}); !IsCode(err, CodeReadOnly) {
		t.Fatalf("ApplyIngest = %v", err)
	}
	if err := readOnly.WithdrawSource(ctx, mousa.SourceWithdrawal{}); !IsCode(err, CodeReadOnly) {
		t.Fatalf("WithdrawSource = %v", err)
	}
}

func TestApplyIngestConflictingReceiptAndProjectionTamperFailClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mousa.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	batch := testIngestBatch(t, "conflict")
	batch.Checkpoint = &mousa.CheckpointAdvance{Next: []byte("one")}
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	conflict := batch
	conflict.AdapterVersion = "2"
	if err := store.ApplyIngest(ctx, conflict); !IsCode(err, CodeConflict) {
		t.Fatalf("conflicting receipt = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	rawExec(t, path, `UPDATE ingest_receipts SET next_checkpoint = CAST('wrong' AS BLOB)`)
	if reopened, err := Open(ctx, path); reopened != nil || !IsCode(err, CodeIntegrity) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("tampered Open = %v, %v", reopened, err)
	}
}

func TestApplyIngestStoresArtifactsAtomicallyAndRejectsDuplicateArtifact(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mousa.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, observation, artifact, _, _, _ := testRecordGraph(t)
	batch := mousa.IngestBatch{AdapterID: "test.adapter", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem, CapturedAtUsec: 1, Source: source, Observation: observation, Artifacts: []mousa.Artifact{artifact}}
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	if stored, err := store.GetArtifact(ctx, artifact.ID); err != nil || !reflect.DeepEqual(stored, artifact) {
		t.Fatalf("artifact = %#v err=%v", stored, err)
	}
	conflictingReplay := batch
	conflictingArtifact := artifact
	conflictingArtifact.ContentSHA256 = testDigest("different artifact")
	conflictingReplay.Artifacts = []mousa.Artifact{conflictingArtifact}
	if err := store.ApplyIngest(ctx, conflictingReplay); !IsCode(err, CodeConflict) {
		t.Fatalf("conflicting artifact replay = %v", err)
	}
	duplicate := testPushBatch(t, "duplicate-artifact", nil)
	duplicateArtifact := artifact
	duplicateArtifact.ObservationID = duplicate.Observation.ID
	duplicateArtifact.ID, err = mousa.NewArtifactID(duplicate.Observation.ID, artifact.ArtifactKey)
	if err != nil {
		t.Fatal(err)
	}
	duplicate.Artifacts = []mousa.Artifact{duplicateArtifact, duplicateArtifact}
	if err := store.ApplyIngest(ctx, duplicate); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("duplicate artifacts = %v", err)
	}
	if _, err := store.GetObservation(ctx, duplicate.Observation.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("partial duplicate observation = %v", err)
	}
}

func TestApplyIngestBusyIsBoundedAndRetryable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mousa.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	locker, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	conn, err := locker.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	batch := testIngestBatch(t, "busy")
	batch.Checkpoint = &mousa.CheckpointAdvance{Next: []byte("one")}
	err = store.ApplyIngest(ctx, batch)
	var storageErr *Error
	if !errors.As(err, &storageErr) || storageErr.Code != CodeBusy || !storageErr.Retryable {
		t.Fatalf("busy = %#v", err)
	}
}

func TestApplyIngestReadBackFailureAndCancellationRollBackPartialWrites(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "mousa.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `CREATE TEMP TRIGGER corrupt_ingest_readback AFTER INSERT ON ingest_receipts BEGIN UPDATE ingest_receipts SET next_checkpoint = x'00' WHERE observation_id = NEW.observation_id; END`); err != nil {
		t.Fatal(err)
	}
	batch := testIngestBatch(t, "readback-failure")
	batch.Checkpoint = &mousa.CheckpointAdvance{Next: []byte("one")}
	if err := store.ApplyIngest(ctx, batch); !IsCode(err, CodeIntegrity) {
		t.Fatalf("read-back failure = %v", err)
	}
	if _, err := store.GetSource(ctx, batch.Source.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("partial source = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER corrupt_ingest_readback`); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	cancelledBatch := testIngestBatch(t, "cancelled")
	cancelledBatch.Checkpoint = &mousa.CheckpointAdvance{Next: []byte("one")}
	if err := store.ApplyIngest(cancelled, cancelledBatch); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled = %v", err)
	}
	if _, err := store.GetObservation(ctx, cancelledBatch.Observation.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("cancelled partial observation = %v", err)
	}
}

func testIngestBatch(t *testing.T, externalObservationID string) mousa.IngestBatch {
	t.Helper()
	source := testSource(t)
	observationID, err := mousa.NewObservationID(source.ID, externalObservationID)
	if err != nil {
		t.Fatal(err)
	}
	return mousa.IngestBatch{AdapterID: "test.adapter", AdapterVersion: "1", Initiative: mousa.InitiativePull, Form: mousa.FormItem, CapturedAtUsec: 1, Source: source, Observation: mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: source.ID, ExternalObservationID: externalObservationID}}
}

func testPushBatch(t *testing.T, externalObservationID string, sequence *uint64) mousa.IngestBatch {
	t.Helper()
	batch := testIngestBatch(t, externalObservationID)
	batch.Initiative = mousa.InitiativePush
	batch.Sequence = sequence
	return batch
}

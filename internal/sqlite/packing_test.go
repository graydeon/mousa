package sqlite

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestExactTrailMixedVersionsAndReopen(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	text := "sharedterm" + strings.Repeat(" ", 4086)
	request, _ := seedEnforcedRetrieval(t, store, text+text)
	if _, err := store.EvaluateSourceRetrieval(ctx, request); err != nil {
		t.Fatal(err)
	}
	original, err := store.TraceEnforcedLexical(ctx, request, "sharedterm", 10, 8192, mousa.PackingOriginal)
	if err != nil {
		t.Fatal(err)
	}
	exact, err := store.TraceEnforcedLexical(ctx, request, "sharedterm", 10, 8192, mousa.PackingExactV1)
	if err != nil {
		t.Fatal(err)
	}
	if original.Trail.UsedBytes != 8192 || exact.Trail.UsedBytes != 4096 || exact.Trail.Candidates[1].DuplicateOf != exact.Trail.Candidates[0].SegmentID.String() {
		t.Fatalf("packing: %+v", exact.Trail)
	}
	before, err := mousa.EncodeSourceTrail(original.Trail)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, open := range []func(context.Context, string) (*Store, error){OpenReadOnly, Open} {
		reopened, err := open(ctx, store.path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := reopened.GetSourceTrail(ctx, exact.Trail.ID)
		if err != nil || got.ID != exact.Trail.ID {
			t.Fatalf("reopen: %v", err)
		}
		old, err := reopened.GetSourceTrail(ctx, original.Trail.ID)
		if err != nil {
			t.Fatal(err)
		}
		after, err := mousa.EncodeSourceTrail(old)
		if err != nil || string(before) != string(after) {
			t.Fatal("historical v1 bytes changed")
		}
		reopened.Close()
	}
}

func TestExactTrailRecomputedIdentityDoesNotProveContent(t *testing.T) {
	for _, kind := range []string{"false duplicate", "duplicate selected"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store := openLexicalStore(t)
			first := "sharedterm" + strings.Repeat(" ", 4086)
			second := first
			if kind == "false duplicate" {
				second = "sharedterm" + strings.Repeat("x", 4086)
			}
			request, _ := seedEnforcedRetrieval(t, store, first+second)
			if _, err := store.EvaluateSourceRetrieval(ctx, request); err != nil {
				t.Fatal(err)
			}
			// The second passage must also match independently of tokenizer word boundaries.
			result, err := store.TraceEnforcedLexical(ctx, request, "sharedterm*", 10, 8192, mousa.PackingExactV1)
			if err != nil {
				t.Fatal(err)
			}
			forged := result.Trail
			if len(forged.Candidates) != 2 {
				t.Fatalf("expected two candidates: %+v", forged)
			}
			if kind == "false duplicate" {
				forged.Candidates[1].ContentSHA256 = forged.Candidates[0].ContentSHA256
				forged.Candidates[1].Selected = false
				forged.Candidates[1].Omission = "duplicate"
				forged.Candidates[1].DuplicateOf = forged.Candidates[0].SegmentID.String()
				forged.UsedBytes = 4096
			} else {
				forged.Candidates[1].Selected = true
				forged.Candidates[1].Omission = ""
				forged.Candidates[1].DuplicateOf = ""
				forged.UsedBytes = 8192
			}
			selected := []bool{forged.Candidates[0].Selected, forged.Candidates[1].Selected}
			packet, err := mousa.NewContextPacketID(forged.Candidates, forged.BudgetBytes, selected)
			if err != nil {
				t.Fatal(err)
			}
			forged.PacketID = packet.String()
			forged.ID, err = mousa.NewSourceTrailID(forged)
			if err != nil {
				t.Fatal(err)
			}
			if err := forged.Validate(); err != nil {
				t.Fatalf("text-free structure should be internally consistent: %v", err)
			}
			if err := store.writeImmediate(ctx, "insert forged fixture", func(conn *sql.Conn) error { return insertSourceTrail(ctx, conn, forged) }); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetSourceTrail(ctx, forged.ID); !IsCode(err, CodeIntegrity) {
				t.Fatalf("content tamper read: %v", err)
			}
			store.Close()
			if reopened, err := OpenReadOnly(ctx, store.path); !IsCode(err, CodeIntegrity) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("content tamper reopen: %v", err)
			}
		})
	}
}

func TestExactTrailRechecksSharedRepresentationAfterTamper(t *testing.T) {
	for _, kind := range []string{"ancestry", "segment", "text"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store := openLexicalStore(t)
			text := "sharedterm" + strings.Repeat(" ", 4086)
			request, document := seedEnforcedRetrieval(t, store, text+text)
			result, err := store.EvaluateAndTraceLexical(ctx, request, "sharedterm", 10, 8192, mousa.PackingExactV1)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Trail.Candidates) != 2 || result.Trail.Candidates[1].DuplicateOf == "" {
				t.Fatalf("expected two byte-equal passages: %+v", result.Trail)
			}
			if _, err := store.GetSourceTrail(ctx, result.Trail.ID); err != nil {
				t.Fatal(err)
			}
			segmentID := result.Trail.Candidates[1].SegmentID
			var damage, repair string
			var args []any
			switch kind {
			case "ancestry":
				damage = `UPDATE representation_inputs SET ordinal = ordinal + 10 WHERE representation_id = ?`
				repair = `UPDATE representation_inputs SET ordinal = ordinal - 10 WHERE representation_id = ?`
				args = []any{document.representation.ID[:]}
			case "segment":
				damage = `UPDATE segments SET selector_end = x'ffffffffffffffff' WHERE id = ?`
				repair = `UPDATE segments SET selector_end = ? WHERE id = ?`
				args = []any{segmentID[:]}
			case "text":
				damage = `UPDATE segment_lexical_fts SET text = 'changed bytes' WHERE rowid = (SELECT rowid FROM segment_lexical_rows WHERE segment_id = ?)`
				repair = `UPDATE segment_lexical_fts SET text = ? WHERE rowid = (SELECT rowid FROM segment_lexical_rows WHERE segment_id = ?)`
				args = []any{segmentID[:]}
			}
			if _, err := store.db.ExecContext(ctx, damage, args...); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetSourceTrail(ctx, result.Trail.ID); !IsCode(err, CodeIntegrity) {
				t.Fatalf("historical read after %s damage = %v", kind, err)
			}
			if _, err := store.SearchVerifiedLexical(ctx, "sharedterm OR changed", 10); !IsCode(err, CodeIntegrity) {
				t.Fatalf("current retrieval after %s damage = %v", kind, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			for _, open := range []func(context.Context, string) (*Store, error){OpenReadOnly, Open} {
				reopened, err := open(ctx, store.path)
				if reopened != nil {
					reopened.Close()
				}
				if !IsCode(err, CodeIntegrity) {
					t.Fatalf("reopen after %s damage = %v", kind, err)
				}
			}
			db, err := connect(ctx, store.path, false)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "segment" {
				for _, segment := range document.segments {
					if segment.ID == segmentID {
						span, _ := segment.Selector.TextByteRange()
						args = []any{uint64Blob(span.End), segmentID[:]}
					}
				}
			} else if kind == "text" {
				args = []any{text, segmentID[:]}
			}
			_, repairErr := db.ExecContext(ctx, repair, args...)
			closeErr := db.Close()
			if repairErr != nil || closeErr != nil {
				t.Fatalf("repair = %v, close = %v", repairErr, closeErr)
			}
			recovered, err := Open(ctx, store.path)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			trail, err := recovered.GetSourceTrail(ctx, result.Trail.ID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := mousa.EncodeSourceTrail(result.Trail)
			if err != nil {
				t.Fatal(err)
			}
			after, err := mousa.EncodeSourceTrail(trail)
			if err != nil || string(before) != string(after) {
				t.Fatal("recovery changed canonical trail bytes")
			}
		})
	}
}

func TestExactTrailUsesOneReadSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	text := "sharedterm" + strings.Repeat(" ", 4086)
	request, document := seedEnforcedRetrieval(t, store, text+text)
	result, err := store.EvaluateAndTraceLexical(ctx, request, "sharedterm", 10, 8192, mousa.PackingExactV1)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := Open(ctx, store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source_trails`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("pin read snapshot: count=%d, error=%v", count, err)
	}
	if _, err := writer.db.ExecContext(ctx, `UPDATE representation_inputs SET ordinal = ordinal + 10 WHERE representation_id = ?`, document.representation.ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := getSourceTrail(ctx, tx, result.Trail.ID); err != nil {
		t.Fatalf("pinned snapshot changed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSourceTrail(ctx, result.Trail.ID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("next operation reused stale ancestry: %v", err)
	}
}

func TestHistoricalVerificationSnapshotWriterProgress(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store := openLexicalStore(t)
	defer store.Close()
	request, document := seedEnforcedRetrieval(t, store, "sharedterm")
	first, err := store.EvaluateAndTraceLexical(ctx, request, "sharedterm", 10, 8192, mousa.PackingExactV1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.TraceEnforcedLexical(ctx, request, "sharedterm", 10, 4096, mousa.PackingExactV1)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := Open(ctx, store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	var busy, frames, checkpointed int
	if err := writer.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
		t.Fatalf("initial checkpoint: %d, %v", busy, err)
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	read, err := getSourceTrail(ctx, tx, first.Trail.ID)
	if err != nil {
		t.Fatal(err)
	}
	read.Candidates[0].ContentSHA256 = mousa.SHA256{}
	progress := make(chan error, 1)
	go func() {
		for range 16 {
			if _, err := writer.db.ExecContext(ctx, `UPDATE representation_inputs SET ordinal = ordinal + 10 WHERE representation_id = ?`, document.representation.ID[:]); err != nil {
				progress <- err
				return
			}
		}
		progress <- nil
	}()
	for i := range 16 {
		want := first.Trail
		if i%2 != 0 {
			want = second.Trail
		}
		got, err := getSourceTrail(ctx, tx, want.ID)
		if err != nil {
			t.Fatalf("pinned historical read: %v", err)
		}
		before, err := mousa.EncodeSourceTrail(want)
		if err != nil {
			t.Fatal(err)
		}
		after, err := mousa.EncodeSourceTrail(got)
		if err != nil || string(before) != string(after) {
			t.Fatal("snapshot or returned-value mutation changed canonical bytes")
		}
	}
	if err := <-progress; err != nil {
		t.Fatalf("writer failed to progress during read snapshot: %v", err)
	}
	if err := writer.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &frames, &checkpointed); err != nil || frames <= checkpointed {
		t.Fatalf("reader did not retain WAL frames: busy=%d frames=%d checkpointed=%d error=%v", busy, frames, checkpointed, err)
	}
	wal, err := os.Stat(store.path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("writer_commits=16 retained_frames=%d checkpointed_frames=%d wal_bytes=%d", frames, checkpointed, wal.Size())
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := verifySourceTrailRecords(ctx, store.db); !IsCode(err, CodeIntegrity) {
		t.Fatalf("next historical scan reused stale ancestry: %v", err)
	}
	if _, err := writer.db.ExecContext(ctx, `UPDATE representation_inputs SET ordinal = ordinal - 160 WHERE representation_id = ?`, document.representation.ID[:]); err != nil {
		t.Fatal(err)
	}
	if err := verifySourceTrailRecords(ctx, store.db); err != nil {
		t.Fatalf("repaired historical scan: %v", err)
	}
	if err := writer.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 || frames != 0 {
		t.Fatalf("released reader prevented checkpoint: %d %d %d %v", busy, frames, checkpointed, err)
	}
	wal, err = os.Stat(store.path + "-wal")
	if err != nil || wal.Size() != 0 {
		t.Fatalf("WAL not truncated after snapshot release: %v, %v", wal, err)
	}
	t.Log("released_wal_bytes=0")
}

func TestHistoricalVerificationRechecksCandidateRelationships(t *testing.T) {
	ctx := t.Context()
	store := openLexicalStore(t)
	defer store.Close()
	request, _ := seedEnforcedRetrieval(t, store, "sharedterm")
	result, err := store.EvaluateAndTraceLexical(ctx, request, "sharedterm", 10, 8192, mousa.PackingExactV1)
	if err != nil {
		t.Fatal(err)
	}
	other := addVerifiedLexicalDocument(t, store, "other-source", "sharedterm other")
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := verifyExactTrailContent(ctx, tx, result.Trail, request.SourceID); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"digest", "size", "source"} {
		t.Run(kind, func(t *testing.T) {
			trail := result.Trail
			trail.Candidates = slices.Clone(trail.Candidates)
			source := request.SourceID
			switch kind {
			case "digest":
				trail.Candidates[0].ContentSHA256 = mousa.SHA256{}
			case "size":
				trail.Candidates[0].TextBytes++
			case "source":
				source = other.source.ID
			}
			if err := verifyExactTrailContent(ctx, tx, trail, source); !IsCode(err, CodeIntegrity) {
				t.Fatalf("historical verification accepted changed %s: %v", kind, err)
			}
		})
	}
}

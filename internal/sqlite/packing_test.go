package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"testing"

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

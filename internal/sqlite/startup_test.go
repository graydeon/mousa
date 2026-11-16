package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

func TestStartupVerificationReleasesSnapshotsAndRechecksDamage(t *testing.T) {
	for _, test := range []struct {
		name   string
		verify func(context.Context, *sql.DB) error
		damage string
	}{
		{"canonical", verifyCanonicalRecords, `UPDATE artifacts SET record_json = CAST(record_json || char(10) AS BLOB)`},
		{"canonical inputs", verifyCanonicalRecords, `DELETE FROM representation_inputs`},
		{"ingest", verifyIngestRecords, `UPDATE source_ingest_state SET last_captured_at_usec = last_captured_at_usec + 1`},
		{"local items", verifyLocalItemRecords, `UPDATE local_items SET active = 0`},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			store := openFreshLexicalStore(t)
			defer store.Close()
			source, representation, content := prepareLocalRevision(t, store, "doc", "newterm current evidence")
			if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", representation.ID, content); err != nil {
				t.Fatal(err)
			}
			if err := test.verify(ctx, store.db); err != nil {
				t.Fatal(err)
			}
			writer, err := connect(ctx, store.path, false)
			if err != nil {
				t.Fatal(err)
			}
			defer writer.Close()
			if _, err := writer.ExecContext(ctx, test.damage); err != nil {
				t.Fatal(err)
			}
			if err := test.verify(ctx, store.db); !IsCode(err, CodeIntegrity) {
				t.Fatalf("later verification did not reject damage: %v", err)
			}
			var busy, frames, checkpointed int
			if err := writer.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 || frames != 0 {
				t.Fatalf("completed or failed verification retained its snapshot: busy=%d frames=%d checkpointed=%d err=%v", busy, frames, checkpointed, err)
			}
		})
	}
}

func TestStartupPreservesActiveEmptyAndIndexedItems(t *testing.T) {
	ctx := t.Context()
	store := openFreshLexicalStore(t)
	path := store.path
	source, empty, emptyText := prepareLocalRevision(t, store, "empty", "")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "empty", empty.ID, emptyText); err != nil {
		t.Fatal(err)
	}
	_, visible, visibleText := prepareLocalRevision(t, store, "visible", "newterm visible evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "visible", visible.ID, visibleText); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	item, err := store.GetLocalItem(ctx, source.ID, "empty")
	if err != nil || !item.Active || item.RepresentationID != empty.ID {
		t.Fatalf("empty item activation after reopen: %#v, %v", item, err)
	}
	assertOnlyLocalText(t, store, string(visibleText))
}

package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
)

func TestActiveRepresentationMigrationPreservesRevisionsAndBackup(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	path := store.path
	source, old, oldText := prepareLocalRevision(t, store, "doc", "oldterm original evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", old.ID, oldText); err != nil {
		t.Fatal(err)
	}
	_, current, currentText := prepareLocalRevision(t, store, "doc", "newterm replacement evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", current.ID, currentText); err != nil {
		t.Fatal(err)
	}
	_, deleted, deletedText := prepareLocalRevision(t, store, "deleted", "oldterm withdrawn item")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "deleted", deleted.ID, deletedText); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteLocalItem(ctx, source.ID, "deleted"); err != nil {
		t.Fatal(err)
	}
	before, err := store.SearchVerifiedLexical(ctx, "oldterm OR newterm", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 || before[0].Segment.RepresentationID != current.ID {
		t.Fatalf("fixture did not select only the current revision: %#v", before)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TABLE supersession_activation_state; DROP TABLE supersession_activations; DROP TABLE supersession_declarations; DROP INDEX IF EXISTS local_items_active_representation_idx; DELETE FROM schema_migrations WHERE version > 10`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, store.db, migrations, 10, true, false); err != nil {
		t.Fatalf("invalid version-10 fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if readOnly, err := OpenReadOnly(ctx, path); readOnly != nil || !IsCode(err, CodeReadOnly) {
		if readOnly != nil {
			readOnly.Close()
		}
		t.Fatalf("read-only version-10 open = %v, %v", readOnly, err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	after, err := store.SearchVerifiedLexical(ctx, "oldterm OR newterm", 10)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("migration changed selected revision: before=%#v after=%#v err=%v", before, after, err)
	}
	for _, representation := range []struct {
		item   string
		active bool
	}{
		{"doc", true},
		{"deleted", false},
	} {
		item, err := store.GetLocalItem(ctx, source.ID, representation.item)
		if err != nil || item.Active != representation.active {
			t.Fatalf("item %q activation after migration: %#v, %v", representation.item, item, err)
		}
	}
	if _, err := store.GetRepresentation(ctx, old.ID); err != nil {
		t.Fatalf("migration removed superseded history: %v", err)
	}
	backupPath := path + ".pre-migrate-v10-to-v11.sqlite"
	backup, err := connect(ctx, backupPath, true)
	if err != nil {
		t.Fatalf("open verified backup %s: %v", filepath.Base(backupPath), err)
	}
	defer backup.Close()
	if err := verifyVersion(ctx, backup, migrations, 10, false, true); err != nil {
		t.Fatalf("verify version-10 backup: %v", err)
	}
}

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
			store := openLexicalStore(t)
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
	store := openLexicalStore(t)
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

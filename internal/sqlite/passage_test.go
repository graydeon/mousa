package sqlite

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestPassageActivationRollbackSnapshotAndHistory(t *testing.T) {
	ctx := t.Context()
	store := openLexicalStore(t)
	path := store.path
	text := "# Recovery\n\nnewterm instructions\n\n" + strings.Repeat("background paragraph\n\n", 100)
	source, old, content := prepareLocalRevision(t, store, "doc", text)
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", old.ID, content); err != nil {
		t.Fatal(err)
	}
	oldJSON, err := mousa.EncodeRepresentation(old)
	if err != nil {
		t.Fatal(err)
	}
	_, next, _ := prepareLocalRevision(t, store, "doc", text, mousa.TextSegmentPassageV1)
	reader, err := connect(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if item, err := getLocalItem(ctx, tx, source.ID, "doc"); err != nil || item.RepresentationID != old.ID {
		t.Fatalf("old snapshot: %v %v", item, err)
	}
	if _, err := store.db.Exec(`CREATE TEMP TRIGGER reject_passage BEFORE UPDATE ON local_items BEGIN SELECT RAISE(ABORT, 'interrupted'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", next.ID, content); err == nil {
		t.Fatal("injected failure accepted")
	}
	if item, err := store.GetLocalItem(ctx, source.ID, "doc"); err != nil || item.RepresentationID != old.ID {
		t.Fatalf("rollback changed activation: %v %v", item, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_passage`); err != nil {
		t.Fatal(err)
	}
	if action, err := store.ActivateLocalItem(ctx, source.ID, "doc", next.ID, content); err != nil || action != "updated" {
		t.Fatalf("replacement: %s %v", action, err)
	}
	if item, err := getLocalItem(ctx, tx, source.ID, "doc"); err != nil || item.RepresentationID != old.ID {
		t.Fatalf("reader mixed activation: %v %v", item, err)
	}
	oldRows, err := searchLexical(ctx, tx, "newterm", 100)
	if err != nil || len(oldRows) != 1 || oldRows[0].Segment.RepresentationID != old.ID {
		t.Fatalf("reader mixed index: %v %v", oldRows, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if action, err := store.ActivateLocalItem(ctx, source.ID, "doc", next.ID, content); err != nil || action != "unchanged" {
		t.Fatalf("retry: %s %v", action, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	historical, err := store.GetRepresentation(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := mousa.EncodeRepresentation(historical)
	if err != nil || !bytes.Equal(actual, oldJSON) {
		t.Fatal("reopen rewrote historical identity")
	}
	rows, err := store.SearchLexical(ctx, "newterm", 100)
	if err != nil || len(rows) != 1 || rows[0].Segment.RepresentationID != next.ID || len(rows[0].Text) > 1024 {
		t.Fatalf("reopened current evidence: %v %v", rows, err)
	}
}

func TestPassageAbruptActivationAndRetry(t *testing.T) {
	ctx := t.Context()
	store := openLexicalStore(t)
	path := store.path
	source, old, content := prepareLocalRevision(t, store, "doc", "newterm evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", old.ID, content); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLocalItemCrashHelper$")
	cmd.Env = append(os.Environ(), "MOUSA_LOCAL_CRASH_PATH="+path, "MOUSA_LOCAL_CRASH_POLICY=passage-v1")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 77 {
		t.Fatalf("crash: %v %s", err, out)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if item, err := store.GetLocalItem(ctx, source.ID, "doc"); err != nil || item.RepresentationID != old.ID {
		t.Fatalf("crash changed current identity: %v %v", item, err)
	}
	_, next, _ := prepareLocalRevision(t, store, "doc", "newterm evidence", mousa.TextSegmentPassageV1)
	if action, err := store.ActivateLocalItem(ctx, source.ID, "doc", next.ID, content); err != nil || action != "updated" {
		t.Fatalf("crash retry: %s %v", action, err)
	}
}

func TestPassageActivationRejectsIncompleteSegmentSet(t *testing.T) {
	ctx := t.Context()
	store := openLexicalStore(t)
	defer store.Close()
	source, rep, content := prepareLocalRevision(t, store, "doc", strings.Repeat("a", 2050), mousa.TextSegmentPassageV1)
	segments, err := mousa.SegmentUTF8Text(rep, content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM segments WHERE id = ?`, segments[1].ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", rep.ID, content); !IsCode(err, CodeIntegrity) {
		t.Fatalf("incomplete activation: %v", err)
	}
	if err := store.PutSegment(ctx, segments[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", rep.ID, content); err != nil {
		t.Fatal(err)
	}
}

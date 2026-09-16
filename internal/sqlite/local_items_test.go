package sqlite

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
	driverSQLite "modernc.org/sqlite"
)

func prepareLocalRevision(t *testing.T, store *Store, item, text string, policies ...string) (mousa.Source, mousa.Representation, []byte) {
	t.Helper()
	ctx := context.Background()
	sourceID, err := mousa.NewSourceID("mousa-local", "activation-test")
	if err != nil {
		t.Fatal(err)
	}
	source := mousa.Source{Schema: mousa.SourceSchema, ID: sourceID, Namespace: "mousa-local", ExternalSourceID: "activation-test"}
	digest := testDigest(text)
	external := fmt.Sprintf("item/%s@%x", item, digest)
	observationID, err := mousa.NewObservationID(sourceID, external)
	if err != nil {
		t.Fatal(err)
	}
	observation := mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: sourceID, ExternalObservationID: external}
	artifactID, err := mousa.NewArtifactID(observationID, "body")
	if err != nil {
		t.Fatal(err)
	}
	artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: "body", MediaType: mousa.UTF8TextMediaType, ContentSHA256: digest, ByteLength: uint64(len(text))}
	batch := mousa.IngestBatch{AdapterID: "test", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem, CapturedAtUsec: 1, Source: source, Observation: observation, Artifacts: []mousa.Artifact{artifact}}
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	policy := mousa.TextSegmentFixedV1
	if len(policies) > 0 {
		policy = policies[0]
	}
	representation, normalized, err := mousa.NormalizeUTF8TextWithPolicy(artifact, []byte(text), policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepresentation(ctx, representation); err != nil {
		t.Fatal(err)
	}
	segments, err := mousa.SegmentUTF8Text(representation, normalized)
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range segments {
		if err := store.PutSegment(ctx, segment); err != nil {
			t.Fatal(err)
		}
	}
	return source, representation, normalized
}

func assertOnlyLocalText(t *testing.T, store *Store, want string) {
	t.Helper()
	got, err := store.SearchLexical(context.Background(), "oldterm OR newterm", 10)
	if err != nil || len(got) != 1 || got[0].Text != want {
		t.Fatalf("retrieved revisions = %#v, %v; want only %q", got, err, want)
	}
}

func TestLocalItemReplacementAndDeletionRollBackTogether(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	source, old, oldText := prepareLocalRevision(t, store, "doc", "oldterm evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", old.ID, oldText); err != nil {
		t.Fatal(err)
	}
	_, next, nextText := prepareLocalRevision(t, store, "doc", "newterm evidence")
	if _, err := store.db.ExecContext(ctx, `CREATE TEMP TRIGGER reject_local_activation BEFORE UPDATE ON local_items BEGIN SELECT RAISE(ABORT, 'injected activation failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", next.ID, nextText); err == nil {
		t.Fatal("replacement ignored injected failure")
	}
	assertOnlyLocalText(t, store, string(oldText))
	if _, err := store.DeleteLocalItem(ctx, source.ID, "doc"); err == nil {
		t.Fatal("deletion ignored injected failure")
	}
	assertOnlyLocalText(t, store, string(oldText))
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_local_activation`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", next.ID, nextText); err != nil {
		t.Fatal(err)
	}
	assertOnlyLocalText(t, store, string(nextText))
	if _, err := store.DeleteLocalItem(ctx, source.ID, "doc"); err != nil {
		t.Fatal(err)
	}
	got, err := store.SearchLexical(ctx, "oldterm OR newterm", 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("deleted evidence = %#v, %v", got, err)
	}
}

func TestLocalItemReaderSeesOneActivationSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	source, old, oldText := prepareLocalRevision(t, store, "doc", "oldterm evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", old.ID, oldText); err != nil {
		t.Fatal(err)
	}
	_, next, nextText := prepareLocalRevision(t, store, "doc", "newterm evidence")
	reader, err := connect(ctx, store.path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	before, err := getLocalItem(ctx, tx, source.ID, "doc")
	if err != nil || before.RepresentationID != old.ID {
		t.Fatalf("initial snapshot = %#v, %v", before, err)
	}
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", next.ID, nextText); err != nil {
		t.Fatal(err)
	}
	var text string
	if err := tx.QueryRowContext(ctx, `SELECT text FROM segment_lexical_fts WHERE segment_lexical_fts MATCH 'oldterm OR newterm'`).Scan(&text); err != nil || text != string(oldText) {
		t.Fatalf("existing reader saw %q, %v; want old revision", text, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assertOnlyLocalText(t, store, string(nextText))
}

func TestLocalItemAbruptExitBeforeCommitRetainsOldEvidence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "crash.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	source, old, oldText := prepareLocalRevision(t, store, "doc", "oldterm evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", old.ID, oldText); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestLocalItemCrashHelper$")
	cmd.Env = append(os.Environ(), "MOUSA_LOCAL_CRASH_PATH="+path)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 77 {
		t.Fatalf("crash helper = %v; output %s", err, out)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertOnlyLocalText(t, store, string(oldText))
	current, err := store.GetLocalItem(ctx, source.ID, "doc")
	if err != nil || current.RepresentationID != old.ID || !current.Active {
		t.Fatalf("recovered activation = %#v, %v", current, err)
	}
	_, next, nextText := prepareLocalRevision(t, store, "doc", "newterm evidence")
	if action, err := store.ActivateLocalItem(ctx, source.ID, "doc", next.ID, nextText); err != nil || action != "updated" {
		t.Fatalf("retry = %q, %v", action, err)
	}
	assertOnlyLocalText(t, store, string(nextText))
}

func TestLocalItemCrashHelper(t *testing.T) {
	path := os.Getenv("MOUSA_LOCAL_CRASH_PATH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	driverSQLite.MustRegisterScalarFunction("crash_before_local_commit", 0, func(*driverSQLite.FunctionContext, []driver.Value) (driver.Value, error) {
		os.Exit(77)
		return nil, nil
	})
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	policy := os.Getenv("MOUSA_LOCAL_CRASH_POLICY")
	if policy == "" {
		policy = mousa.TextSegmentFixedV1
	}
	source, next, text := prepareLocalRevision(t, store, "doc", "newterm evidence", policy)
	if _, err := store.db.Exec(`CREATE TEMP TRIGGER crash_local_activation AFTER UPDATE ON local_items BEGIN SELECT crash_before_local_commit(); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateLocalItem(context.Background(), source.ID, "doc", next.ID, text); err != nil {
		t.Fatal(err)
	}
	t.Fatal("activation did not reach crash point")
}

func TestLocalItemMigrationRequiresReplayAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	path := store.path
	source, representation, content := prepareLocalRevision(t, store, "doc", "oldterm legacy evidence")
	if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
		t.Fatal(err)
	}
	// The legacy fixture contains exactly the first nine schema versions.
	if _, err := store.db.Exec(`DROP TABLE local_items; DROP TABLE local_recovery_sources; DELETE FROM schema_migrations WHERE version = 10`); err != nil {
		t.Fatal(err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, store.db, migrations, 9, true, false); err != nil {
		t.Fatalf("invalid legacy fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if required, err := store.LocalSourceNeedsRecovery(ctx, source.ID); err != nil || !required {
		t.Fatalf("recovery state = %v, %v", required, err)
	}
	got, err := store.SearchLexical(ctx, "oldterm", 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("legacy index was exposed as current: %#v, %v", got, err)
	}
	if _, err := store.GetRepresentation(ctx, representation.ID); err != nil {
		t.Fatalf("migration removed history: %v", err)
	}
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", representation.ID, content); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteLocalRecovery(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	if required, err := store.LocalSourceNeedsRecovery(ctx, source.ID); err != nil || required {
		t.Fatalf("completed recovery state = %v, %v", required, err)
	}
	assertOnlyLocalText(t, store, string(content))
}

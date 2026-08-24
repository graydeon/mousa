package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	_ "modernc.org/sqlite"
)

func TestBackupCapturesWALAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, filepath.Join(dir, "live.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	source, observation, artifact, base, mixed, segment := testRecordGraph(t)
	for _, write := range []struct {
		name string
		put  func() error
	}{
		{"source", func() error { return store.PutSource(ctx, source) }},
		{"observation", func() error { return store.PutObservation(ctx, observation) }},
		{"artifact", func() error { return store.PutArtifact(ctx, artifact) }},
		{"base representation", func() error { return store.PutRepresentation(ctx, base) }},
		{"mixed representation", func() error { return store.PutRepresentation(ctx, mixed) }},
		{"segment", func() error { return store.PutSegment(ctx, segment) }},
	} {
		if err := write.put(); err != nil {
			t.Fatalf("put %s: %v", write.name, err)
		}
	}
	destination := filepath.Join(dir, "backup.sqlite")
	if err := store.Backup(ctx, destination); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	backup, err := OpenReadOnly(ctx, destination)
	if err != nil {
		t.Fatalf("OpenReadOnly backup: %v", err)
	}
	if _, err := backup.GetSegment(ctx, segment.ID); err != nil {
		t.Fatalf("GetSegment backup: %v", err)
	}
	if err := backup.Close(); err != nil {
		t.Fatalf("Close backup: %v", err)
	}
	before, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if err := store.Backup(ctx, destination); !IsCode(err, CodeConflict) {
		t.Fatalf("second Backup error = %v, want conflict", err)
	}
	after, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read backup after refusal: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("refused backup changed destination")
	}
}

func TestRecordWritesFailClosedAndRetryExactly(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "records.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	source, observation, artifact, base, mixed, segment := testRecordGraph(t)
	if err := store.PutObservation(ctx, observation); !IsCode(err, CodeConflict) {
		t.Fatalf("missing parent error = %v, want conflict", err)
	}
	for _, put := range []func() error{
		func() error { return store.PutSource(ctx, source) },
		func() error { return store.PutObservation(ctx, observation) },
		func() error { return store.PutArtifact(ctx, artifact) },
		func() error { return store.PutRepresentation(ctx, base) },
		func() error { return store.PutRepresentation(ctx, mixed) },
		func() error { return store.PutSegment(ctx, segment) },
	} {
		if err := put(); err != nil {
			t.Fatalf("first write: %v", err)
		}
		if err := put(); err != nil {
			t.Fatalf("exact duplicate retry: %v", err)
		}
	}
	collision := artifact
	collision.ContentSHA256 = testDigest("different")
	if err := store.PutArtifact(ctx, collision); !IsCode(err, CodeConflict) {
		t.Fatalf("same-ID collision error = %v, want conflict", err)
	}
	outOfBounds := segment
	outOfBounds.Selector = mousa.NewTextByteRangeSelector(0, mixed.ByteLength+1)
	outOfBounds.ID, err = mousa.NewSegmentID(mixed.ID, outOfBounds.Selector, outOfBounds.ContentSHA256)
	if err != nil {
		t.Fatalf("NewSegmentID out of bounds: %v", err)
	}
	if err := store.PutSegment(ctx, outOfBounds); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("selector bounds error = %v, want invalid_record", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM sources WHERE id = ?`, source.ID[:]); err == nil {
		t.Fatal("parent delete succeeded")
	}
	if _, err := store.GetSource(ctx, source.ID); err != nil {
		t.Fatalf("source after restricted delete: %v", err)
	}

	interrupted := errors.New("interrupt after insert")
	other := testSourceNamed(t, "other")
	data, err := mousa.EncodeSource(other)
	if err != nil {
		t.Fatalf("EncodeSource: %v", err)
	}
	err = store.writeImmediate(ctx, "interrupted test", func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `INSERT INTO sources(id, record_json) VALUES(?, ?)`, other.ID[:], data); err != nil {
			return err
		}
		return interrupted
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("interrupted write error = %v", err)
	}
	if _, err := store.GetSource(ctx, other.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("interrupted row error = %v, want not_found", err)
	}
}

func TestFailedMigrationRollsBackAllState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "failed.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	defer conn.Close()
	badSQL := []byte(`PRAGMA application_id = 1297044819; CREATE TABLE partial(id INTEGER); THIS IS NOT SQL;`)
	if err := applyMigration(ctx, conn, badSQL, sha256.Sum256(badSQL)); err == nil {
		t.Fatal("applyMigration succeeded")
	}
	var applicationID int
	if err := conn.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&applicationID); err != nil {
		t.Fatalf("application_id: %v", err)
	}
	if applicationID != 0 {
		t.Fatalf("application_id = %d, want 0", applicationID)
	}
	var objects int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
		t.Fatalf("object count: %v", err)
	}
	if objects != 0 {
		t.Fatalf("object count = %d, want 0", objects)
	}
}

func TestInjectedCommittedMigrationResumesAsCurrent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "committed.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		t.Fatalf("Conn: %v", err)
	}
	migrationSQL, err := migrationFiles.ReadFile("migrations/0001_records.sql")
	if err != nil {
		conn.Close()
		db.Close()
		t.Fatalf("read migration: %v", err)
	}
	if err := applyMigration(ctx, conn, migrationSQL, sha256.Sum256(migrationSQL)); err != nil {
		conn.Close()
		db.Close()
		t.Fatalf("applyMigration: %v", err)
	}
	if err := conn.Close(); err != nil {
		db.Close()
		t.Fatalf("close connection: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open committed migration: %v", err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("migration count: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration count = %d, want 1", count)
	}
}

func TestBusyErrorIsRetryable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "busy.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	locker, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open locker: %v", err)
	}
	defer locker.Close()
	conn, err := locker.db.Conn(ctx)
	if err != nil {
		t.Fatalf("locker conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("begin lock: %v", err)
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	err = store.PutSource(ctx, testSourceNamed(t, "busy"))
	var storageErr *Error
	if !errors.As(err, &storageErr) || storageErr.Code != CodeBusy || !storageErr.Retryable {
		t.Fatalf("busy error = %#v, want retryable busy", err)
	}
}

func TestInterruptedMigrationAndRecordWriteRecoverOnReopen(t *testing.T) {
	ctx := context.Background()
	t.Run("migration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "migration.sqlite")
		runCrashHelper(t, "migration", path)
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("Open after interrupted migration: %v", err)
		}
		defer store.Close()
		var migrations int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrations); err != nil {
			t.Fatalf("migration count: %v", err)
		}
		if migrations != 1 {
			t.Fatalf("migration count = %d, want 1", migrations)
		}
	})
	t.Run("record", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "record.sqlite")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		runCrashHelper(t, "record", path)
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatalf("Open after interrupted record: %v", err)
		}
		defer store.Close()
		id, err := mousa.NewSourceID("crash", "record")
		if err != nil {
			t.Fatalf("NewSourceID: %v", err)
		}
		if _, err := store.GetSource(ctx, id); !IsCode(err, CodeNotFound) {
			t.Fatalf("interrupted record error = %v, want not_found", err)
		}
	})
}

func TestCrashHelper(t *testing.T) {
	mode := os.Getenv("MOUSA_SQLITE_CRASH_MODE")
	if mode == "" {
		t.Skip("subprocess helper")
	}
	path := os.Getenv("MOUSA_SQLITE_CRASH_PATH")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		os.Exit(2)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		os.Exit(3)
	}
	if _, err := conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		os.Exit(4)
	}
	switch mode {
	case "migration":
		migrationSQL, err := migrationFiles.ReadFile("migrations/0001_records.sql")
		if err != nil {
			os.Exit(5)
		}
		if _, err := conn.ExecContext(context.Background(), string(migrationSQL)); err != nil {
			os.Exit(6)
		}
	case "record":
		id, err := mousa.NewSourceID("crash", "record")
		if err != nil {
			os.Exit(7)
		}
		record := mousa.Source{Schema: mousa.SourceSchema, ID: id, Namespace: "crash", ExternalSourceID: "record"}
		data, err := mousa.EncodeSource(record)
		if err != nil {
			os.Exit(8)
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO sources(id, record_json) VALUES(?, ?)`, id[:], data); err != nil {
			os.Exit(9)
		}
	default:
		os.Exit(10)
	}
	os.Exit(0)
}

func runCrashHelper(t *testing.T, mode, path string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
	command.Env = append(os.Environ(), "MOUSA_SQLITE_CRASH_MODE="+mode, "MOUSA_SQLITE_CRASH_PATH="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash helper %s: %v\n%s", mode, err, output)
	}
}

func TestCheckpointTruncatesOrReturnsRetryableBusy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "checkpoint.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	if err := store.PutSource(ctx, testSourceNamed(t, "first")); err != nil {
		t.Fatalf("PutSource(first): %v", err)
	}
	if err := store.Checkpoint(ctx); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	reader, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_query_only=1&_busy_timeout=1000")
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer reader.Close()
	readerConn, err := reader.Conn(ctx)
	if err != nil {
		t.Fatalf("reader conn: %v", err)
	}
	defer readerConn.Close()
	if _, err := readerConn.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatalf("begin reader: %v", err)
	}
	defer readerConn.ExecContext(context.Background(), `ROLLBACK`)
	var count int
	if err := readerConn.QueryRowContext(ctx, `SELECT count(*) FROM sources`).Scan(&count); err != nil {
		t.Fatalf("establish reader snapshot: %v", err)
	}
	if err := store.PutSource(ctx, testSourceNamed(t, "second")); err != nil {
		t.Fatalf("PutSource(second): %v", err)
	}
	err = store.Checkpoint(ctx)
	var storageErr *Error
	if !errors.As(err, &storageErr) || storageErr.Code != CodeBusy || !storageErr.Retryable {
		t.Fatalf("Checkpoint error = %#v, want retryable busy", err)
	}
}

func TestBusyTimeoutIsBoundedNearOneSecond(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "busy-timing.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	locker, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open locker: %v", err)
	}
	defer locker.Close()
	conn, err := locker.db.Conn(ctx)
	if err != nil {
		t.Fatalf("locker conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatalf("begin lock: %v", err)
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	started := time.Now()
	err = store.PutSource(ctx, testSourceNamed(t, "timed-busy"))
	elapsed := time.Since(started)
	var storageErr *Error
	if !errors.As(err, &storageErr) || storageErr.Code != CodeBusy || !storageErr.Retryable {
		t.Fatalf("busy error = %#v, want retryable busy", err)
	}
	if elapsed < 500*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("busy wait = %v, want bounded near one second", elapsed)
	}
}

func TestBackupSnapshotRestoresAndFailedBackupLeavesSourceUsable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	livePath := filepath.Join(dir, "live.sqlite")
	backupPath := filepath.Join(dir, "backup.sqlite")
	store, err := Open(ctx, livePath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	before := testSourceNamed(t, "before-backup")
	if err := store.PutSource(ctx, before); err != nil {
		t.Fatalf("PutSource before backup: %v", err)
	}
	if err := store.Backup(ctx, backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	after := testSourceNamed(t, "after-backup")
	if err := store.PutSource(ctx, after); err != nil {
		t.Fatalf("PutSource after backup: %v", err)
	}
	if err := store.Backup(ctx, backupPath); !IsCode(err, CodeConflict) {
		t.Fatalf("second Backup error = %v, want conflict", err)
	}
	if _, err := store.GetSource(ctx, before.ID); err != nil {
		t.Fatalf("source unusable after failed backup: %v", err)
	}
	last := testSourceNamed(t, "after-failed-backup")
	if err := store.PutSource(ctx, last); err != nil {
		t.Fatalf("write after failed backup: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close live store: %v", err)
	}

	backupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	restoredPath := filepath.Join(dir, "restored.sqlite")
	if err := os.WriteFile(restoredPath, backupBytes, 0o600); err != nil {
		t.Fatalf("restore backup bytes: %v", err)
	}
	restored, err := Open(ctx, restoredPath)
	if err != nil {
		t.Fatalf("Open restored backup: %v", err)
	}
	defer restored.Close()
	if _, err := restored.GetSource(ctx, before.ID); err != nil {
		t.Fatalf("restored pre-backup source: %v", err)
	}
	for _, absent := range []mousa.SourceID{after.ID, last.ID} {
		if _, err := restored.GetSource(ctx, absent); !IsCode(err, CodeNotFound) {
			t.Fatalf("restored post-backup source error = %v, want not_found", err)
		}
	}
}

func TestSuccessfulCheckpointTruncatesWAL(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "checkpoint-truncate.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	if err := store.PutSource(ctx, testSourceNamed(t, "checkpoint")); err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if err := store.Checkpoint(ctx); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	info, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatalf("stat WAL: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("WAL size = %d, want 0 after TRUNCATE checkpoint", info.Size())
	}
}

func testSourceNamed(t *testing.T, externalID string) mousa.Source {
	t.Helper()
	id, err := mousa.NewSourceID("test", externalID)
	if err != nil {
		t.Fatalf("NewSourceID: %v", err)
	}
	return mousa.Source{Schema: mousa.SourceSchema, ID: id, Namespace: "test", ExternalSourceID: externalID}
}

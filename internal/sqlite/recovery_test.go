package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	createVersionOne(t, path)
	source, observation, artifact, base, mixed, segment := seedVersionOneRecordGraph(t, path)
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := createVerifiedMigrationBackup(ctx, path, migrations, 1); err != nil {
		t.Fatalf("create verified backup: %v", err)
	}
	backupPath := path + ".pre-migrate-v1-to-v2.sqlite"
	backupBefore, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read verified backup: %v", err)
	}
	db, err := connect(ctx, path, false)
	if err != nil {
		t.Fatalf("connect v1: %v", err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("Conn: %v", err)
	}
	v2SQL, err := migrationFiles.ReadFile("migrations/0002_ingest.sql")
	if err != nil {
		t.Fatal(err)
	}
	badSQL := append(append([]byte(nil), v2SQL...), []byte("THIS IS NOT SQL;\n")...)
	if err := applyMigration(ctx, conn, migration{version: 2, name: "ingest", sql: badSQL, hash: sha256.Sum256(badSQL)}); err == nil {
		t.Fatal("applyMigration succeeded")
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close connection: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	verify, err := connect(ctx, path, true)
	if err != nil {
		t.Fatalf("open failed-migration source: %v", err)
	}
	if err := verifyVersion(ctx, verify, migrations, 1, false, true); err != nil {
		t.Fatalf("source is not valid v1: %v", err)
	}
	assertRecordGraph(t, &Store{db: verify, readOnly: true, path: path}, source, observation, artifact, base, mixed, segment)
	if err := verify.Close(); err != nil {
		t.Fatalf("close source verification: %v", err)
	}
	backupAfter, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup after failure: %v", err)
	}
	if !reflect.DeepEqual(backupAfter, backupBefore) {
		t.Fatal("failed migration changed verified backup")
	}
	backup, err := connect(ctx, backupPath, true)
	if err != nil {
		t.Fatalf("open verified backup: %v", err)
	}
	defer backup.Close()
	if err := verifyVersion(ctx, backup, migrations, 1, false, true); err != nil {
		t.Fatalf("backup is not valid v1: %v", err)
	}
	assertRecordGraph(t, &Store{db: backup, readOnly: true, path: backupPath}, source, observation, artifact, base, mixed, segment)
}

func TestVersionOneDamageFailsWithoutRepairWrites(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"changed migration name", func(t *testing.T, path string) {
			rawExec(t, path, `UPDATE schema_migrations SET name = 'renamed' WHERE version = 1`)
		}},
		{"changed migration hash", func(t *testing.T, path string) {
			rawExec(t, path, `UPDATE schema_migrations SET sha256 = randomblob(32) WHERE version = 1`)
		}},
		{"unexpected v1 object", func(t *testing.T, path string) {
			rawExec(t, path, `CREATE TABLE unexpected_object(id INTEGER PRIMARY KEY) STRICT`)
		}},
		{"partial schema", func(t *testing.T, path string) {
			rawExec(t, path, `DROP TABLE segments`)
		}},
		{"malformed schema", func(t *testing.T, path string) {
			rawExec(t, path, `PRAGMA writable_schema=ON; UPDATE sqlite_schema SET sql='broken' WHERE name='sources'; PRAGMA writable_schema=OFF`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "damaged.sqlite")
			createVersionOne(t, path)
			test.mutate(t, path)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read before open: %v", err)
			}
			store, err := Open(context.Background(), path)
			if store != nil {
				store.Close()
				t.Fatal("Open returned a store")
			}
			if !IsCode(err, CodeIntegrity) {
				t.Fatalf("Open error = %v, want integrity", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read after open: %v", readErr)
			}
			if string(after) != string(before) {
				t.Fatal("failed startup changed database bytes")
			}
			if _, statErr := os.Stat(path + ".pre-migrate-v1-to-v2.sqlite"); !os.IsNotExist(statErr) {
				t.Fatalf("failed startup backup stat = %v", statErr)
			}
		})
	}
}

func TestInjectedCommittedMigrationResumesAsCurrent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "committed.sqlite")
	createVersionOne(t, path)
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := createVerifiedMigrationBackup(ctx, path, migrations, 1); err != nil {
		t.Fatalf("create verified backup: %v", err)
	}
	backupPath := path + ".pre-migrate-v1-to-v2.sqlite"
	backupBefore, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	runCrashHelper(t, "committed-migration", path)
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open committed migration: %v", err)
	}
	defer store.Close()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("migration count: %v", err)
	}
	if count != 10 {
		t.Fatalf("migration count = %d, want 10", count)
	}
	backupAfter, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup after reopen: %v", err)
	}
	if !reflect.DeepEqual(backupAfter, backupBefore) {
		t.Fatal("current reopen changed existing migration backup")
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
		createVersionOne(t, path)
		runCrashHelper(t, "migration", path)
		migrationSet, err := loadMigrations(migrationFiles)
		if err != nil {
			t.Fatal(err)
		}
		beforeRecovery, err := connect(ctx, path, true)
		if err != nil {
			t.Fatalf("open interrupted migration: %v", err)
		}
		if err := verifyVersion(ctx, beforeRecovery, migrationSet, 1, false, true); err != nil {
			t.Fatalf("interrupted migration did not leave valid v1: %v", err)
		}
		if err := beforeRecovery.Close(); err != nil {
			t.Fatalf("close interrupted migration: %v", err)
		}
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("Open after interrupted migration: %v", err)
		}
		defer store.Close()
		var migrationCount int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrationCount); err != nil {
			t.Fatalf("migration count: %v", err)
		}
		if migrationCount != 10 {
			t.Fatalf("migration count = %d, want 10", migrationCount)
		}
		backup, err := connect(ctx, path+".pre-migrate-v1-to-v2.sqlite", true)
		if err != nil {
			t.Fatalf("open migration backup: %v", err)
		}
		defer backup.Close()
		loaded, err := loadMigrations(migrationFiles)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyVersion(ctx, backup, loaded, 1, false, true); err != nil {
			t.Fatalf("verify migration backup: %v", err)
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
	t.Run("ingest", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ingest.sqlite")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		runCrashHelper(t, "ingest", path)
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		sequence := uint64(3)
		batch := testPushBatch(t, "crash-ingest", &sequence)
		batch.Gaps = []mousa.SequenceGap{{Start: 1, End: 3}}
		if _, err := store.GetSource(ctx, batch.Source.ID); !IsCode(err, CodeNotFound) {
			t.Fatalf("interrupted ingest source = %v", err)
		}
		if _, err := store.GetObservation(ctx, batch.Observation.ID); !IsCode(err, CodeNotFound) {
			t.Fatalf("interrupted ingest observation = %v", err)
		}
		if _, err := getIngestReceipt(ctx, store.db, batch.Observation.ID); !IsCode(err, CodeNotFound) {
			t.Fatalf("interrupted ingest receipt = %v", err)
		}
		var gaps int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM ingest_gaps WHERE observation_id = ?`, batch.Observation.ID[:]).Scan(&gaps); err != nil || gaps != 0 {
			t.Fatalf("interrupted ingest gaps = %d err=%v", gaps, err)
		}
		if _, err := store.GetIngestState(ctx, batch.Source.ID); !IsCode(err, CodeNotFound) {
			t.Fatalf("interrupted ingest state = %v", err)
		}
	})
	t.Run("withdrawal", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "withdrawal.sqlite")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		batch := testIngestBatch(t, "withdrawal-parent")
		batch.Checkpoint = &mousa.CheckpointAdvance{Next: []byte("one")}
		if err := store.ApplyIngest(ctx, batch); err != nil {
			t.Fatal(err)
		}
		priorState, err := store.GetIngestState(ctx, batch.Source.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		runCrashHelper(t, "withdrawal", path)
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		state, err := store.GetIngestState(ctx, batch.Source.ID)
		if err != nil || !reflect.DeepEqual(state, priorState) {
			t.Fatalf("state = %#v err=%v, want %#v", state, err, priorState)
		}
		withdrawalID, _ := mousa.NewWithdrawalID(batch.Source.ID, "crash-withdrawal")
		if _, err := getSourceWithdrawal(ctx, store.db, withdrawalID); !IsCode(err, CodeNotFound) {
			t.Fatalf("interrupted withdrawal = %v", err)
		}
	})
}

func TestFailedLexicalMigrationRollsBackAllState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "failed-lexical.sqlite")
	createVersionTwo(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data, err := migrationFiles.ReadFile("migrations/0003_lexical.sql")
	if err != nil {
		t.Fatal(err)
	}
	failedSQL := append(append([]byte(nil), data...), []byte("INSERT INTO schema_migrations(version, name, sha256) VALUES(3, 'lexical', zeroblob(32));\nSELECT * FROM missing_lexical_migration_table;\n")...)
	if err := applyMigration(ctx, conn, migration{version: 3, name: "lexical", sql: failedSQL, hash: sha256.Sum256(failedSQL)}); err == nil {
		t.Fatal("failed lexical migration succeeded")
	}
	var objects int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name LIKE 'segment_lexical_%'`).Scan(&objects); err != nil || objects != 0 {
		t.Fatalf("lexical objects after rollback = %d err=%v, want 0", objects, err)
	}
	assertMigrationVersion(t, path, 2)
}

func TestInterruptedAndCommittedLexicalMigrationRecoverOnReopen(t *testing.T) {
	ctx := context.Background()
	t.Run("interrupted after every migration write", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "interrupted-lexical.sqlite")
		createVersionTwo(t, path)
		runCrashHelper(t, "lexical-migration", path)
		assertMigrationVersion(t, path, 2)
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		var version int
		if err := store.db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 10 {
			t.Fatalf("recovered version = %d err=%v, want 10", version, err)
		}
	})
	t.Run("committed before marker", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "committed-lexical.sqlite")
		createVersionTwo(t, path)
		runCrashHelper(t, "committed-lexical-migration", path)
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		var version int
		if err := store.db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 10 {
			t.Fatalf("committed version = %d err=%v, want 10", version, err)
		}
	})
}

func TestFailedClassificationMigrationRollsBackAllState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "failed-classification.sqlite")
	createVersionThree(t, path)
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := createVerifiedMigrationBackup(ctx, path, migrations, 3); err != nil {
		t.Fatal(err)
	}
	backupPath := path + ".pre-migrate-v3-to-v4.sqlite"
	backupBefore, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := migrationFiles.ReadFile("migrations/0004_classifications.sql")
	if err != nil {
		t.Fatal(err)
	}
	failedSQL := append(append([]byte(nil), data...), []byte("SELECT * FROM missing_classification_migration_table;\n")...)
	if err := applyMigration(ctx, conn, migration{version: 4, name: "classifications", sql: failedSQL, hash: sha256.Sum256(failedSQL)}); err == nil {
		t.Fatal("failed classification migration succeeded")
	}
	conn.Close()
	db.Close()
	assertMigrationVersion(t, path, 3)
	verify, err := connect(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, verify, migrations, 3, false, true); err != nil {
		t.Fatalf("source is not valid v3: %v", err)
	}
	var objects int
	if err := verify.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name IN ('classifications', 'classification_bases') OR name LIKE 'classifications_subject_%_idx' OR name LIKE 'classification_bases_%_idx'`).Scan(&objects); err != nil || objects != 0 {
		t.Fatalf("classification objects after rollback = %d err=%v", objects, err)
	}
	verify.Close()
	backupAfter, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backupAfter, backupBefore) {
		t.Fatal("failed classification migration changed verified backup")
	}
}

func TestInterruptedAndCommittedClassificationMigrationRecoverOnReopen(t *testing.T) {
	ctx := context.Background()
	t.Run("interrupted after every migration write", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "interrupted-classification.sqlite")
		createVersionThree(t, path)
		runCrashHelper(t, "classification-migration", path)
		assertMigrationVersion(t, path, 3)
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		assertMigrationVersion(t, path, 10)
		backup, err := connect(ctx, path+".pre-migrate-v3-to-v4.sqlite", true)
		if err != nil {
			t.Fatal(err)
		}
		migrations, err := loadMigrations(migrationFiles)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyVersion(ctx, backup, migrations, 3, false, true); err != nil {
			t.Fatalf("classification backup is not valid v3: %v", err)
		}
		backup.Close()
	})
	t.Run("committed before marker", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "committed-classification.sqlite")
		createVersionThree(t, path)
		runCrashHelper(t, "committed-classification-migration", path)
		assertMigrationVersion(t, path, 4)
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		assertMigrationVersion(t, path, 10)
		if _, err := os.Stat(path + ".pre-migrate-v3-to-v4.sqlite"); !os.IsNotExist(err) {
			t.Fatalf("current committed reopen created backup: %v", err)
		}
	})
}

func TestFailedPolicyDefinitionMigrationRollsBackAllState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "failed-policy-definition.sqlite")
	createVersionFour(t, path)
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := createVerifiedMigrationBackup(ctx, path, migrations, 4); err != nil {
		t.Fatal(err)
	}
	backupPath := path + ".pre-migrate-v4-to-v5.sqlite"
	backupBefore, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := migrationFiles.ReadFile("migrations/0005_policy_definitions.sql")
	if err != nil {
		t.Fatal(err)
	}
	failedSQL := append(append([]byte(nil), data...), []byte("SELECT * FROM missing_policy_definition_migration_table;\n")...)
	if err := applyMigration(ctx, conn, migration{version: 5, name: "policy_definitions", sql: failedSQL, hash: sha256.Sum256(failedSQL)}); err == nil {
		t.Fatal("failed policy definition migration succeeded")
	}
	conn.Close()
	db.Close()
	assertMigrationVersion(t, path, 4)
	verify, err := connect(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, verify, migrations, 4, false, true); err != nil {
		t.Fatalf("source is not valid v4: %v", err)
	}
	var objects int
	if err := verify.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name = 'policy_definitions'`).Scan(&objects); err != nil || objects != 0 {
		t.Fatalf("policy definition objects after rollback = %d err=%v, want 0", objects, err)
	}
	verify.Close()
	backupAfter, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backupAfter, backupBefore) {
		t.Fatal("failed policy definition migration changed verified backup")
	}
}

func TestInterruptedPolicyDefinitionWriteRollsBackOnReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "interrupted-policy-definition-write.sqlite")
	createCurrent(t, path)
	record := testPolicyDefinition(t, "crash", "policy", "1", "opaque", "opaque", "definition")
	runCrashHelper(t, "policy-definition-record", path)
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.GetPolicyDefinition(ctx, record.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("interrupted policy definition write = %v, want not_found", err)
	}
}

func TestInterruptedPolicyBindingAndActivationWritesRollBackOnReopen(t *testing.T) {
	ctx := context.Background()
	t.Run("binding", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "interrupted-policy-binding-write.sqlite")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		definition := testPolicyDefinition(t, "crash", "policy", "1", "opaque", "opaque", "definition")
		if err := store.PutPolicyDefinition(ctx, definition); err != nil {
			t.Fatal(err)
		}
		binding := testPolicyBinding(t, "crash", "series", "1", definition.ID)
		store.Close()
		runCrashHelper(t, "policy-binding-record", path)
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err := store.GetPolicyBinding(ctx, binding.ID); !IsCode(err, CodeNotFound) {
			t.Fatalf("interrupted policy binding write = %v, want not_found", err)
		}
	})
	t.Run("activation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "interrupted-policy-activation-write.sqlite")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		definition := testPolicyDefinition(t, "crash", "policy", "1", "opaque", "opaque", "definition")
		binding := testPolicyBinding(t, "crash", "series", "1", definition.ID)
		if err := store.PutPolicyDefinition(ctx, definition); err != nil {
			t.Fatal(err)
		}
		if err := store.PutPolicyBinding(ctx, binding); err != nil {
			t.Fatal(err)
		}
		activation := testPolicyActivation(t, "crash", "series", "activate", nil, &binding.ID)
		store.Close()
		runCrashHelper(t, "policy-activation-record", path)
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		if _, err := store.GetPolicyActivation(ctx, activation.ID); !IsCode(err, CodeNotFound) {
			t.Fatalf("interrupted policy activation write = %v, want not_found", err)
		}
		if _, err := store.GetPolicyBindingState(ctx, "crash", "series"); !IsCode(err, CodeNotFound) {
			t.Fatalf("interrupted policy binding state = %v, want not_found", err)
		}
	})
}

func TestInterruptedAndCommittedPolicyDefinitionMigrationRecoverOnReopen(t *testing.T) {
	ctx := context.Background()
	t.Run("interrupted after every migration write", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "interrupted-policy-definition.sqlite")
		createVersionFour(t, path)
		runCrashHelper(t, "policy-definition-migration", path)
		assertMigrationVersion(t, path, 4)
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		assertMigrationVersion(t, path, 10)
		backup, err := connect(ctx, path+".pre-migrate-v4-to-v5.sqlite", true)
		if err != nil {
			t.Fatal(err)
		}
		migrations, err := loadMigrations(migrationFiles)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyVersion(ctx, backup, migrations, 4, false, true); err != nil {
			t.Fatalf("policy definition backup is not valid v4: %v", err)
		}
		backup.Close()
	})
	t.Run("committed before marker", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "committed-policy-definition.sqlite")
		createVersionFour(t, path)
		runCrashHelper(t, "committed-policy-definition-migration", path)
		assertMigrationVersion(t, path, 5)
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		assertMigrationVersion(t, path, 10)
		if _, err := os.Stat(path + ".pre-migrate-v4-to-v5.sqlite"); !os.IsNotExist(err) {
			t.Fatalf("current committed reopen created backup: %v", err)
		}
	})
}

func TestFailedPolicyBindingMigrationRollsBackAllState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "failed-policy-binding.sqlite")
	createVersionFive(t, path)
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := createVerifiedMigrationBackup(ctx, path, migrations, 5); err != nil {
		t.Fatal(err)
	}
	backupPath := path + ".pre-migrate-v5-to-v6.sqlite"
	backupBefore, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	data, err := migrationFiles.ReadFile("migrations/0006_policy_bindings.sql")
	if err != nil {
		t.Fatal(err)
	}
	failedSQL := append(append([]byte(nil), data...), []byte("SELECT * FROM missing_policy_binding_migration_table;\n")...)
	if err := applyMigration(ctx, conn, migration{version: 6, name: "policy_bindings", sql: failedSQL, hash: sha256.Sum256(failedSQL)}); err == nil {
		t.Fatal("failed policy binding migration succeeded")
	}
	conn.Close()
	db.Close()
	assertMigrationVersion(t, path, 5)
	verify, err := connect(ctx, path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, verify, migrations, 5, false, true); err != nil {
		t.Fatalf("source is not valid v5: %v", err)
	}
	var objects int
	if err := verify.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE name IN ('policy_bindings', 'policy_activations', 'policy_binding_state')`).Scan(&objects); err != nil || objects != 0 {
		t.Fatalf("policy binding objects after rollback = %d err=%v, want 0", objects, err)
	}
	verify.Close()
	backupAfter, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backupAfter, backupBefore) {
		t.Fatal("failed policy binding migration changed verified backup")
	}
}

func TestInterruptedAndCommittedPolicyBindingMigrationRecoverOnReopen(t *testing.T) {
	ctx := context.Background()
	t.Run("interrupted after every migration write", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "interrupted-policy-binding.sqlite")
		createVersionFive(t, path)
		runCrashHelper(t, "policy-binding-migration", path)
		assertMigrationVersion(t, path, 5)
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		assertMigrationVersion(t, path, 10)
		backup, err := connect(ctx, path+".pre-migrate-v5-to-v6.sqlite", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyVersion(ctx, backup, mustMigrations(t), 5, false, true); err != nil {
			t.Fatalf("policy binding backup is not valid v5: %v", err)
		}
		backup.Close()
	})
	t.Run("committed before marker", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "committed-policy-binding.sqlite")
		createVersionFive(t, path)
		runCrashHelper(t, "committed-policy-binding-migration", path)
		assertMigrationVersion(t, path, 6)
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		assertMigrationVersion(t, path, 10)
		if _, err := os.Stat(path + ".pre-migrate-v5-to-v6.sqlite"); !os.IsNotExist(err) {
			t.Fatalf("current committed reopen created backup: %v", err)
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
	case "migration", "committed-migration", "lexical-migration", "committed-lexical-migration", "classification-migration", "committed-classification-migration", "policy-definition-migration", "committed-policy-definition-migration", "policy-binding-migration", "committed-policy-binding-migration":
		migrationPath := "migrations/0002_ingest.sql"
		version, name := 2, "ingest"
		if mode == "lexical-migration" || mode == "committed-lexical-migration" {
			migrationPath = "migrations/0003_lexical.sql"
			version, name = 3, "lexical"
		} else if mode == "classification-migration" || mode == "committed-classification-migration" {
			migrationPath = "migrations/0004_classifications.sql"
			version, name = 4, "classifications"
		} else if mode == "policy-definition-migration" || mode == "committed-policy-definition-migration" {
			migrationPath = "migrations/0005_policy_definitions.sql"
			version, name = 5, "policy_definitions"
		} else if mode == "policy-binding-migration" || mode == "committed-policy-binding-migration" {
			migrationPath = "migrations/0006_policy_bindings.sql"
			version, name = 6, "policy_bindings"
		}
		migrationSQL, err := migrationFiles.ReadFile(migrationPath)
		if err != nil {
			os.Exit(5)
		}
		if _, err := conn.ExecContext(context.Background(), string(migrationSQL)); err != nil {
			os.Exit(6)
		}
		hash := sha256.Sum256(migrationSQL)
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO schema_migrations(version, name, sha256) VALUES(?, ?, ?)`, version, name, hash[:]); err != nil {
			os.Exit(14)
		}
		if mode == "committed-migration" || mode == "committed-lexical-migration" || mode == "committed-classification-migration" || mode == "committed-policy-definition-migration" || mode == "committed-policy-binding-migration" {
			if _, err := conn.ExecContext(context.Background(), `COMMIT`); err != nil {
				os.Exit(15)
			}
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
	case "policy-definition-record":
		record := testPolicyDefinition(t, "crash", "policy", "1", "opaque", "opaque", "definition")
		data, err := mousa.EncodePolicyDefinition(record)
		if err != nil {
			os.Exit(22)
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO policy_definitions(id, namespace, external_policy_id, external_policy_version, record_json) VALUES(?, ?, ?, ?, ?)`, record.ID[:], record.Namespace, record.ExternalPolicyID, record.ExternalPolicyVersion, data); err != nil {
			os.Exit(23)
		}
	case "policy-binding-record":
		definition := testPolicyDefinition(t, "crash", "policy", "1", "opaque", "opaque", "definition")
		record := testPolicyBinding(t, "crash", "series", "1", definition.ID)
		data, err := mousa.EncodePolicyBinding(record)
		if err != nil {
			os.Exit(24)
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO policy_bindings(id, namespace, external_binding_id, external_binding_version, scope_kind, subject_id, policy_definition_id, record_json) VALUES(?, ?, ?, ?, ?, NULL, ?, ?)`, record.ID[:], record.Namespace, record.ExternalBindingID, record.ExternalBindingVersion, record.Scope.Kind(), record.PolicyDefinitionID[:], data); err != nil {
			os.Exit(25)
		}
	case "policy-activation-record":
		definition := testPolicyDefinition(t, "crash", "policy", "1", "opaque", "opaque", "definition")
		binding := testPolicyBinding(t, "crash", "series", "1", definition.ID)
		record := testPolicyActivation(t, "crash", "series", "activate", nil, &binding.ID)
		data, err := mousa.EncodePolicyActivation(record)
		if err != nil {
			os.Exit(26)
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO policy_activations(id, namespace, external_binding_id, external_activation_id, expected_previous_activation_id, active_binding_id, record_json) VALUES(?, ?, ?, ?, NULL, ?, ?)`, record.ID[:], record.Namespace, record.ExternalBindingID, record.ExternalActivationID, binding.ID[:], data); err != nil {
			os.Exit(27)
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO policy_binding_state(namespace, external_binding_id, current_activation_id, active_binding_id) VALUES(?, ?, ?, ?)`, record.Namespace, record.ExternalBindingID, record.ID[:], binding.ID[:]); err != nil {
			os.Exit(28)
		}
	case "ingest":
		sequence := uint64(3)
		batch := testPushBatch(t, "crash-ingest", &sequence)
		batch.Gaps = []mousa.SequenceGap{{Start: 1, End: 3}}
		receipt, err := batch.Receipt()
		if err != nil {
			os.Exit(16)
		}
		sourceData, _ := mousa.EncodeSource(batch.Source)
		observationData, _ := mousa.EncodeObservation(batch.Observation)
		receiptData, _ := mousa.EncodeIngestReceipt(receipt)
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO sources(id, record_json) VALUES(?, ?)`, batch.Source.ID[:], sourceData); err != nil {
			os.Exit(11)
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO observations(id, source_id, record_json) VALUES(?, ?, ?)`, batch.Observation.ID[:], batch.Source.ID[:], observationData); err != nil {
			os.Exit(12)
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO ingest_receipts(observation_id, source_id, initiative, form, captured_at_usec, sequence, record_json) VALUES(?, ?, ?, ?, ?, ?, ?)`, receipt.ObservationID[:], receipt.SourceID[:], receipt.Initiative, receipt.Form, receipt.CapturedAtUsec, uint64Blob(*receipt.Sequence), receiptData); err != nil {
			os.Exit(17)
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO ingest_gaps(observation_id, ordinal, gap_start, gap_end) VALUES(?, 0, ?, ?)`, receipt.ObservationID[:], uint64Blob(receipt.Gaps[0].Start), uint64Blob(receipt.Gaps[0].End)); err != nil {
			os.Exit(18)
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO source_ingest_state(source_id, collection_state, high_sequence, last_observation_id, last_captured_at_usec) VALUES(?, ?, ?, ?, ?)`, receipt.SourceID[:], mousa.CollectionActive, uint64Blob(*receipt.Sequence), receipt.ObservationID[:], receipt.CapturedAtUsec); err != nil {
			os.Exit(19)
		}
	case "withdrawal":
		sourceID, _ := mousa.NewSourceID("test", "source-1")
		withdrawalID, _ := mousa.NewWithdrawalID(sourceID, "crash-withdrawal")
		withdrawal := mousa.SourceWithdrawal{Schema: mousa.SourceWithdrawalSchema, ID: withdrawalID, SourceID: sourceID, ExternalWithdrawalID: "crash-withdrawal", AdapterID: "test.adapter", AdapterVersion: "1", OccurredAtUsec: 2}
		data, _ := mousa.EncodeSourceWithdrawal(withdrawal)
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO source_withdrawals(id, source_id, occurred_at_usec, record_json) VALUES(?, ?, ?, ?)`, withdrawalID[:], sourceID[:], withdrawal.OccurredAtUsec, data); err != nil {
			os.Exit(13)
		}
		if _, err := conn.ExecContext(context.Background(), `UPDATE source_ingest_state SET collection_state = ?, current_withdrawal_id = ? WHERE source_id = ?`, mousa.CollectionWithdrawn, withdrawalID[:], sourceID[:]); err != nil {
			os.Exit(20)
		}
	default:
		os.Exit(10)
	}
	if _, err := os.Stdout.Write([]byte("ready\n")); err != nil {
		os.Exit(21)
	}
	os.Exit(0)
}

func runCrashHelper(t *testing.T, mode, path string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
	command.Env = append(os.Environ(), "MOUSA_SQLITE_CRASH_MODE="+mode, "MOUSA_SQLITE_CRASH_PATH="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash helper %s: %v\n%s", mode, err, output)
	} else if string(output) != "ready\n" {
		t.Fatalf("crash helper %s marker = %q, want ready", mode, output)
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

func TestBackupPreservesIngestReceiptWithdrawalAndState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "source.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	sequence := uint64(10)
	first := testPushBatch(t, "backup-ingest-first", &sequence)
	if err := store.ApplyIngest(ctx, first); err != nil {
		t.Fatal(err)
	}
	sequence = 13
	batch := testPushBatch(t, "backup-ingest-gap", &sequence)
	batch.Gaps = []mousa.SequenceGap{{Start: 11, End: 13}}
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	wantReceipt, err := batch.Receipt()
	if err != nil {
		t.Fatal(err)
	}
	withdrawalID, _ := mousa.NewWithdrawalID(batch.Source.ID, "backup-withdrawal")
	withdrawal := mousa.SourceWithdrawal{Schema: mousa.SourceWithdrawalSchema, ID: withdrawalID, SourceID: batch.Source.ID, ExternalWithdrawalID: "backup-withdrawal", AdapterID: "test.adapter", AdapterVersion: "1", OccurredAtUsec: 2}
	if err := store.WithdrawSource(ctx, withdrawal); err != nil {
		t.Fatal(err)
	}
	wantState, err := store.GetIngestState(ctx, batch.Source.ID)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := store.Backup(ctx, destination); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := OpenReadOnly(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	gotReceipt, err := getIngestReceipt(ctx, backup.db, batch.Observation.ID)
	if err != nil || !reflect.DeepEqual(gotReceipt, wantReceipt) {
		t.Fatalf("receipt = %#v err=%v, want %#v", gotReceipt, err, wantReceipt)
	}
	if !reflect.DeepEqual(gotReceipt.Gaps, []mousa.SequenceGap{{Start: 11, End: 13}}) {
		t.Fatalf("ordered gaps = %#v", gotReceipt.Gaps)
	}
	rows, err := backup.db.QueryContext(ctx, `SELECT gap_start, gap_end FROM ingest_gaps WHERE observation_id = ? ORDER BY ordinal`, batch.Observation.ID[:])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("restored backup has no ordered gap row")
	}
	var gapStart, gapEnd []byte
	if err := rows.Scan(&gapStart, &gapEnd); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gapStart, uint64Blob(11)) || !reflect.DeepEqual(gapEnd, uint64Blob(13)) || rows.Next() {
		t.Fatalf("restored gap projection = %x-%x", gapStart, gapEnd)
	}
	gotWithdrawal, err := getSourceWithdrawal(ctx, backup.db, withdrawal.ID)
	if err != nil || !reflect.DeepEqual(gotWithdrawal, withdrawal) {
		t.Fatalf("withdrawal = %#v err=%v, want %#v", gotWithdrawal, err, withdrawal)
	}
	state, err := backup.GetIngestState(ctx, batch.Source.ID)
	if err != nil || !reflect.DeepEqual(state, wantState) {
		t.Fatalf("state = %#v err=%v, want %#v", state, err, wantState)
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

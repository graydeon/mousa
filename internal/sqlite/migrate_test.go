package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/graydeon/mousa/internal/mousa"
	_ "modernc.org/sqlite"
)

func TestMigrationHashAndCurrentReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "records.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()
	var hash []byte
	if err := store.db.QueryRow(`SELECT sha256 FROM schema_migrations WHERE version = 1 AND name = 'records'`).Scan(&hash); err != nil {
		t.Fatalf("migration row: %v", err)
	}
	migrationSQL, err := migrationFiles.ReadFile("migrations/0001_records.sql")
	if err != nil {
		t.Fatalf("read embedded migration: %v", err)
	}
	want := sha256.Sum256(migrationSQL)
	if string(hash) != string(want[:]) {
		t.Fatalf("migration hash = %x, want %x", hash, want)
	}
	if got := sha256.Sum256(migrationSQL); got != [32]byte{0x11, 0xb7, 0x62, 0x92, 0x06, 0x1a, 0xad, 0xac, 0x5c, 0x83, 0xd4, 0xc2, 0x6d, 0x26, 0x64, 0x73, 0x99, 0x34, 0x9f, 0x87, 0x53, 0x06, 0x6e, 0xbe, 0x15, 0xeb, 0xa1, 0x82, 0x87, 0xd4, 0x28, 0x4e} {
		t.Fatalf("embedded migration hash changed: %x", got)
	}
}

func TestMigrationLoaderRejectsInvalidSets(t *testing.T) {
	validSQL := []byte(`SELECT 1;`)
	for _, test := range []struct {
		name string
		fs   fstest.MapFS
	}{
		{"empty", fstest.MapFS{}},
		{"malformed name", fstest.MapFS{"migrations/1_bad.sql": {Data: validSQL}}},
		{"gap", fstest.MapFS{"migrations/0002_gap.sql": {Data: validSQL}}},
		{"empty SQL", fstest.MapFS{"migrations/0001_empty.sql": {Data: nil}}},
		{"oversized SQL", fstest.MapFS{"migrations/0001_large.sql": {Data: make([]byte, 1024*1024+1)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := loadMigrations(test.fs); err == nil {
				t.Fatal("loadMigrations succeeded")
			}
		})
	}
	migrations, err := loadMigrations(fstest.MapFS{"migrations/0001_records.sql": {Data: validSQL}})
	if err != nil {
		t.Fatalf("valid loadMigrations: %v", err)
	}
	if len(migrations) != 1 || migrations[0].version != 1 || migrations[0].name != "records" || string(migrations[0].sql) != string(validSQL) {
		t.Fatalf("migration = %#v", migrations)
	}
}

func TestVersionOneMigrationCreatesVerifiedBackupAndFailsClosedOnExistingDestination(t *testing.T) {
	ctx := context.Background()
	t.Run("migrates with verified backup", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "records.sqlite")
		createVersionOne(t, path)
		source, observation, artifact, base, mixed, segment := seedVersionOneRecordGraph(t, path)
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		var version int
		if err := store.db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 9 {
			t.Fatalf("version = %d err=%v", version, err)
		}
		assertRecordGraph(t, store, source, observation, artifact, base, mixed, segment)
		if err := store.Close(); err != nil {
			t.Fatalf("close migrated source: %v", err)
		}
		backup, err := connect(ctx, path+".pre-migrate-v1-to-v2.sqlite", true)
		if err != nil {
			t.Fatalf("open backup: %v", err)
		}
		migrations, err := loadMigrations(migrationFiles)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyVersion(ctx, backup, migrations, 1, false); err != nil {
			t.Fatalf("verify backup: %v", err)
		}
		if err := backup.Close(); err != nil {
			t.Fatalf("close backup: %v", err)
		}
		backupBytes, err := os.ReadFile(path + ".pre-migrate-v1-to-v2.sqlite")
		if err != nil {
			t.Fatalf("read backup: %v", err)
		}
		restoredPath := filepath.Join(dir, "restored.sqlite")
		if err := os.WriteFile(restoredPath, backupBytes, 0o600); err != nil {
			t.Fatalf("restore backup: %v", err)
		}
		restored, err := Open(ctx, restoredPath)
		if err != nil {
			t.Fatalf("Open restored backup: %v", err)
		}
		defer restored.Close()
		assertRecordGraph(t, restored, source, observation, artifact, base, mixed, segment)
	})
	t.Run("existing destination", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "records.sqlite")
		createVersionOne(t, path)
		if err := os.WriteFile(path+".pre-migrate-v1-to-v2.sqlite", []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		if store, err := Open(ctx, path); store != nil || !IsCode(err, CodeConflict) {
			if store != nil {
				store.Close()
			}
			t.Fatalf("Open = %v, %v", store, err)
		}
		db, err := connect(ctx, path, true)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var version int
		if err := db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 1 {
			t.Fatalf("version = %d err=%v", version, err)
		}
	})
}

func TestReadOnlyVersionOneRefusesMigrationWithoutBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.sqlite")
	createVersionOne(t, path)
	if store, err := OpenReadOnly(context.Background(), path); store != nil || !IsCode(err, CodeReadOnly) {
		if store != nil {
			store.Close()
		}
		t.Fatalf("OpenReadOnly = %v, %v", store, err)
	}
	if _, err := os.Stat(path + ".pre-migrate-v1-to-v2.sqlite"); !os.IsNotExist(err) {
		t.Fatalf("backup stat = %v", err)
	}
}

func TestIngestMigrationExactHash(t *testing.T) {
	data, err := migrationFiles.ReadFile("migrations/0002_ingest.sql")
	if err != nil {
		t.Fatal(err)
	}
	want := [32]byte{0x18, 0xf3, 0xed, 0x8a, 0x3c, 0x54, 0x52, 0xe7, 0x78, 0x23, 0x6a, 0x4b, 0x55, 0x51, 0xa3, 0xeb, 0x07, 0x9f, 0xa6, 0x70, 0xcd, 0x4b, 0xb9, 0x11, 0x8a, 0x1b, 0xfd, 0x72, 0x80, 0x70, 0xa4, 0x50}
	if got := sha256.Sum256(data); got != want {
		t.Fatalf("hash = %x, want %x", got, want)
	}
	if len(data) != 3384 || data[len(data)-1] != '\n' {
		t.Fatalf("migration bytes = %d, trailing newline = %v", len(data), len(data) > 0 && data[len(data)-1] == '\n')
	}
}

func TestLexicalMigrationExactSchemaAndHash(t *testing.T) {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 9 {
		t.Fatalf("migration count = %d, want 9", len(migrations))
	}
	if migrations[2].version != 3 || migrations[2].name != "lexical" {
		t.Fatalf("migration 3 = version %d name %q, want version 3 name lexical", migrations[2].version, migrations[2].name)
	}
	if len(migrations[2].sql) != 359 || fmt.Sprintf("%x", migrations[2].hash) != "73d3387b252bc19792dcf0d181a20737f9c4786db199005e4e6a6174cd6bcbdc" || migrations[2].sql[len(migrations[2].sql)-1] != '\n' {
		t.Fatalf("migration 3 bytes/hash/newline = %d/%x/%v", len(migrations[2].sql), migrations[2].hash, migrations[2].sql[len(migrations[2].sql)-1] == '\n')
	}
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "schema.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.Query(`SELECT type || ':' || name FROM sqlite_schema WHERE name LIKE 'segment_lexical_%' ORDER BY type || ':' || name`)
	if err != nil {
		t.Fatal(err)
	}
	var objects []string
	for rows.Next() {
		var object string
		if err := rows.Scan(&object); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, object)
	}
	rows.Close()
	wantObjects := []string{"table:segment_lexical_fts", "table:segment_lexical_fts_config", "table:segment_lexical_fts_content", "table:segment_lexical_fts_data", "table:segment_lexical_fts_docsize", "table:segment_lexical_fts_idx", "table:segment_lexical_rows"}
	if !equalStrings(objects, wantObjects) {
		t.Fatalf("lexical objects = %v, want %v", objects, wantObjects)
	}
	var triggers int
	if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type = 'trigger'`).Scan(&triggers); err != nil || triggers != 0 {
		t.Fatalf("trigger count = %d err=%v, want 0", triggers, err)
	}
	var storedHash []byte
	if err := store.db.QueryRow(`SELECT sha256 FROM schema_migrations WHERE version = 3 AND name = 'lexical'`).Scan(&storedHash); err != nil || !bytes.Equal(storedHash, migrations[2].hash[:]) {
		t.Fatalf("stored migration hash = %x err=%v", storedHash, err)
	}
}

func TestClassificationMigrationExactSchemaBackupAndRetrievalEquivalence(t *testing.T) {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 9 || migrations[3].version != 4 || migrations[3].name != "classifications" {
		t.Fatalf("migration 4 = %#v", migrations)
	}
	if len(migrations[3].sql) != 6563 || fmt.Sprintf("%x", migrations[3].hash) != "cd2f3dd48e6b107900c53ad5036f80a046f2dc414102dc05c06677e53156416a" || migrations[3].sql[len(migrations[3].sql)-1] != '\n' {
		t.Fatalf("migration 4 bytes/hash/newline = %d/%x/%v", len(migrations[3].sql), migrations[3].hash, migrations[3].sql[len(migrations[3].sql)-1] == '\n')
	}

	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "records.sqlite")
	createVersionThree(t, path)
	db, err := connect(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	v3 := &Store{db: db, path: path}
	representation, content, _ := addLexicalDocument(t, v3, "classification-migration", "alpha evidence")
	if err := v3.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
		t.Fatal(err)
	}
	beforeLexical, err := v3.SearchLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	beforeVerified, err := v3.SearchVerifiedLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := v3.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	afterLexical, err := store.SearchLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	afterVerified, err := store.SearchVerifiedLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterLexical, beforeLexical) || !reflect.DeepEqual(afterVerified, beforeVerified) {
		t.Fatalf("retrieval changed: lexical %v -> %v; verified %v -> %v", beforeLexical, afterLexical, beforeVerified, afterVerified)
	}

	rows, err := store.db.Query(`SELECT type || ':' || name FROM sqlite_schema WHERE name IN ('classifications', 'classification_bases') OR name LIKE 'classifications_subject_%_idx' OR name LIKE 'classification_bases_%_idx' ORDER BY type || ':' || name`)
	if err != nil {
		t.Fatal(err)
	}
	var objects []string
	for rows.Next() {
		var object string
		if err := rows.Scan(&object); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, object)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	wantObjects := []string{
		"index:classification_bases_artifact_id_idx", "index:classification_bases_observation_id_idx", "index:classification_bases_representation_id_idx", "index:classification_bases_segment_id_idx", "index:classification_bases_source_id_idx",
		"index:classifications_subject_artifact_id_idx", "index:classifications_subject_observation_id_idx", "index:classifications_subject_representation_id_idx", "index:classifications_subject_segment_id_idx", "index:classifications_subject_source_id_idx",
		"table:classification_bases", "table:classifications",
	}
	if !equalStrings(objects, wantObjects) {
		t.Fatalf("classification objects = %v, want %v", objects, wantObjects)
	}
	for _, table := range []string{"classifications", "classification_bases"} {
		var sqlText string
		if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&sqlText); err != nil || !strings.HasSuffix(sqlText, " STRICT") {
			t.Fatalf("%s schema = %q err=%v", table, sqlText, err)
		}
	}
	var storedHash []byte
	if err := store.db.QueryRow(`SELECT sha256 FROM schema_migrations WHERE version = 4 AND name = 'classifications'`).Scan(&storedHash); err != nil || !bytes.Equal(storedHash, migrations[3].hash[:]) {
		t.Fatalf("stored migration hash = %x err=%v", storedHash, err)
	}

	backupPath := path + ".pre-migrate-v3-to-v4.sqlite"
	backup, err := connect(ctx, backupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, backup, migrations, 3, false); err != nil {
		t.Fatalf("verify v3 backup: %v", err)
	}
	backup.Close()
	backupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(dir, "restored.sqlite")
	if err := os.WriteFile(restoredPath, backupBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	gotLexical, err := restored.SearchLexical(ctx, "alpha", 10)
	if err != nil || !reflect.DeepEqual(gotLexical, beforeLexical) {
		t.Fatalf("restored lexical = %v, %v", gotLexical, err)
	}
	gotVerified, err := restored.SearchVerifiedLexical(ctx, "alpha", 10)
	if err != nil || !reflect.DeepEqual(gotVerified, beforeVerified) {
		t.Fatalf("restored verified = %v, %v", gotVerified, err)
	}
}

func TestPolicyDefinitionMigrationExactSchemaAndBackup(t *testing.T) {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 9 || migrations[4].version != 5 || migrations[4].name != "policy_definitions" {
		t.Fatalf("migration 5 = %#v", migrations)
	}
	if len(migrations[4].sql) != 697 || fmt.Sprintf("%x", migrations[4].hash) != "36fe39648b615c52739c6eaf1a19ebf0c85325a9e45416cb01690304a888add4" || migrations[4].sql[len(migrations[4].sql)-1] != '\n' {
		t.Fatalf("migration 5 bytes/hash/newline = %d/%x/%v", len(migrations[4].sql), migrations[4].hash, migrations[4].sql[len(migrations[4].sql)-1] == '\n')
	}
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "records.sqlite")
	createVersionFour(t, path)
	db, err := connect(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	v4 := &Store{db: db, path: path}
	representation, content, _ := addLexicalDocument(t, v4, "policy-migration", "alpha evidence")
	if err := v4.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
		t.Fatal(err)
	}
	graph, classification := classificationRecordGraph(t)
	for _, put := range []func() error{
		func() error { return v4.PutSource(ctx, graph.source) },
		func() error { return v4.PutObservation(ctx, graph.observation) },
		func() error { return v4.PutArtifact(ctx, graph.artifact) },
		func() error { return v4.PutRepresentation(ctx, graph.base) },
		func() error { return v4.PutRepresentation(ctx, graph.mixed) },
		func() error { return v4.PutSegment(ctx, graph.segment) },
		func() error { return v4.PutClassification(ctx, classification) },
	} {
		if err := put(); err != nil {
			t.Fatal(err)
		}
	}
	beforeLexical, err := v4.SearchLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	beforeVerified, err := v4.SearchVerifiedLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := v4.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got, err := store.GetClassification(ctx, classification.ID); err != nil || !reflect.DeepEqual(got, classification) {
		t.Fatalf("classification after v5 migration = %#v, %v", got, err)
	}
	afterLexical, err := store.SearchLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	afterVerified, err := store.SearchVerifiedLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterLexical, beforeLexical) || !reflect.DeepEqual(afterVerified, beforeVerified) {
		t.Fatalf("retrieval changed: lexical %v -> %v; verified %v -> %v", beforeLexical, afterLexical, beforeVerified, afterVerified)
	}
	var tableSQL string
	if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'policy_definitions'`).Scan(&tableSQL); err != nil || !strings.HasSuffix(tableSQL, " STRICT") || !strings.Contains(tableSQL, "UNIQUE (namespace, external_policy_id, external_policy_version)") {
		t.Fatalf("policy_definitions schema = %q err=%v", tableSQL, err)
	}
	var storedHash []byte
	if err := store.db.QueryRow(`SELECT sha256 FROM schema_migrations WHERE version = 5 AND name = 'policy_definitions'`).Scan(&storedHash); err != nil || !bytes.Equal(storedHash, migrations[4].hash[:]) {
		t.Fatalf("stored migration hash = %x err=%v", storedHash, err)
	}
	backupPath := path + ".pre-migrate-v4-to-v5.sqlite"
	backup, err := connect(ctx, backupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, backup, migrations, 4, false); err != nil {
		t.Fatalf("verify v4 backup: %v", err)
	}
	backup.Close()
	backupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(dir, "restored.sqlite")
	if err := os.WriteFile(restoredPath, backupBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got, err := restored.GetClassification(ctx, classification.ID); err != nil || !reflect.DeepEqual(got, classification) {
		t.Fatalf("restored classification = %#v, %v", got, err)
	}
	gotLexical, err := restored.SearchLexical(ctx, "alpha", 10)
	if err != nil || !reflect.DeepEqual(gotLexical, beforeLexical) {
		t.Fatalf("restored lexical = %v, %v", gotLexical, err)
	}
	gotVerified, err := restored.SearchVerifiedLexical(ctx, "alpha", 10)
	if err != nil || !reflect.DeepEqual(gotVerified, beforeVerified) {
		t.Fatalf("restored verified = %v, %v", gotVerified, err)
	}
	t.Run("occupied backup fails closed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "occupied.sqlite")
		createVersionFour(t, path)
		backupPath := path + ".pre-migrate-v4-to-v5.sqlite"
		const occupied = "occupied"
		if err := os.WriteFile(backupPath, []byte(occupied), 0o600); err != nil {
			t.Fatal(err)
		}
		if store, err := Open(ctx, path); store != nil || !IsCode(err, CodeConflict) {
			if store != nil {
				store.Close()
			}
			t.Fatalf("Open with occupied backup = %v, %v", store, err)
		}
		if got, err := os.ReadFile(backupPath); err != nil || string(got) != occupied {
			t.Fatalf("occupied backup = %q, %v", got, err)
		}
		assertMigrationVersion(t, path, 4)
	})
	t.Run("read-only v4 refuses migration", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "readonly.sqlite")
		createVersionFour(t, path)
		if store, err := OpenReadOnly(ctx, path); store != nil || !IsCode(err, CodeReadOnly) {
			if store != nil {
				store.Close()
			}
			t.Fatalf("OpenReadOnly v4 = %v, %v", store, err)
		}
		assertMigrationVersion(t, path, 4)
		if _, err := os.Stat(path + ".pre-migrate-v4-to-v5.sqlite"); !os.IsNotExist(err) {
			t.Fatalf("read-only migration created backup: %v", err)
		}
	})
}

func TestPolicyBindingMigrationExactSchemaBackupAndRetrievalEquivalence(t *testing.T) {
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 9 || migrations[5].version != 6 || migrations[5].name != "policy_bindings" {
		t.Fatalf("migration 6 = %#v", migrations)
	}
	if len(migrations[5].sql) != 4550 || fmt.Sprintf("%x", migrations[5].hash) != "89f9a11f99e3bc9c4fb18994310a9ece14d6d6a635e3a5b092601e64e1600b1e" || migrations[5].sql[len(migrations[5].sql)-1] != '\n' {
		t.Fatalf("migration 6 bytes/hash/newline = %d/%x/%v", len(migrations[5].sql), migrations[5].hash, migrations[5].sql[len(migrations[5].sql)-1] == '\n')
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v5.sqlite")
	createVersionFive(t, path)
	db, err := connect(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Store{db: db, path: path}
	representation, content, _ := addLexicalDocument(t, legacy, "policy-binding-migration", "alpha evidence")
	if err := legacy.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
		t.Fatal(err)
	}
	beforeLexical, err := legacy.SearchLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	beforeVerified, err := legacy.SearchVerifiedLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	if readOnly, err := OpenReadOnly(ctx, path); readOnly != nil || !IsCode(err, CodeReadOnly) {
		if readOnly != nil {
			readOnly.Close()
		}
		t.Fatalf("OpenReadOnly v5 = %v, %v", readOnly, err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	afterLexical, err := store.SearchLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	afterVerified, err := store.SearchVerifiedLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeLexical, afterLexical) || !reflect.DeepEqual(beforeVerified, afterVerified) {
		t.Fatalf("retrieval changed: lexical %v -> %v; verified %v -> %v", beforeLexical, afterLexical, beforeVerified, afterVerified)
	}
	for _, table := range []string{"policy_bindings", "policy_activations", "policy_binding_state"} {
		var tableSQL string
		if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&tableSQL); err != nil || !strings.HasSuffix(tableSQL, " STRICT") {
			t.Fatalf("%s schema = %q err=%v", table, tableSQL, err)
		}
	}
	for table, want := range map[string][]string{
		"policy_bindings":      {"id", "namespace", "external_binding_id", "external_binding_version", "scope_kind", "subject_id", "policy_definition_id", "record_json"},
		"policy_activations":   {"id", "namespace", "external_binding_id", "external_activation_id", "expected_previous_activation_id", "active_binding_id", "record_json"},
		"policy_binding_state": {"namespace", "external_binding_id", "current_activation_id", "active_binding_id"},
	} {
		rows, err := store.db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			got = append(got, name)
		}
		rows.Close()
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s columns = %v, want %v", table, got, want)
		}
	}
	for table, want := range map[string]int{"policy_bindings": 1, "policy_activations": 6, "policy_binding_state": 6} {
		var got int
		if err := store.db.QueryRow(`SELECT count(*) FROM pragma_foreign_key_list(?)`, table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s foreign keys = %d err=%v, want %d", table, got, err, want)
		}
	}
	var storedHash []byte
	for _, want := range []struct {
		name   string
		table  string
		unique bool
	}{
		{"policy_activations_active_binding_id_idx", "policy_activations", false},
		{"policy_activations_predecessor_idx", "policy_activations", true},
		{"policy_activations_root_idx", "policy_activations", true},
		{"policy_activations_series_idx", "policy_activations", false},
		{"policy_bindings_definition_id_idx", "policy_bindings", false},
		{"policy_bindings_scope_idx", "policy_bindings", false},
		{"policy_binding_state_active_binding_id_idx", "policy_binding_state", false},
	} {
		var count, unique int
		if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND name = ?`, want.name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s count = %d err=%v", want.name, count, err)
		}
		if err := store.db.QueryRow(`SELECT "unique" FROM pragma_index_list(?) WHERE name = ?`, want.table, want.name).Scan(&unique); err != nil {
			t.Fatalf("index %s uniqueness = %v", want.name, err)
		}
		if (unique != 0) != want.unique {
			t.Fatalf("index %s unique = %d, want %v", want.name, unique, want.unique)
		}
	}
	for _, want := range []struct{ name, sql string }{
		{"policy_activations_active_binding_id_idx", "CREATE INDEX policy_activations_active_binding_id_idx ON policy_activations(active_binding_id) WHERE active_binding_id IS NOT NULL"},
		{"policy_binding_state_active_binding_id_idx", "CREATE INDEX policy_binding_state_active_binding_id_idx ON policy_binding_state(active_binding_id) WHERE active_binding_id IS NOT NULL"},
	} {
		var got string
		if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type = 'index' AND name = ?`, want.name).Scan(&got); err != nil || got != want.sql {
			t.Fatalf("index %s SQL = %q err=%v, want %q", want.name, got, err, want.sql)
		}
	}
	var triggers, views int
	if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type = 'trigger'`).Scan(&triggers); err != nil || triggers != 0 {
		t.Fatalf("trigger count = %d err=%v", triggers, err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type = 'view'`).Scan(&views); err != nil || views != 0 {
		t.Fatalf("view count = %d err=%v", views, err)
	}
	if err := store.db.QueryRow(`SELECT sha256 FROM schema_migrations WHERE version = 6 AND name = 'policy_bindings'`).Scan(&storedHash); err != nil || !bytes.Equal(storedHash, migrations[5].hash[:]) {
		t.Fatalf("stored migration hash = %x err=%v", storedHash, err)
	}
	backupPath := path + ".pre-migrate-v5-to-v6.sqlite"
	backup, err := connect(ctx, backupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := verifyVersion(ctx, backup, migrations, 5, false); err != nil {
		t.Fatalf("v5 backup: %v", err)
	}
}

func TestVersionTwoMigrationCreatesVerifiedBackupAndRestores(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "records.sqlite")
	createVersionTwo(t, path)
	source, observation, artifact, base, mixed, segment := seedVersionOneRecordGraph(t, path)
	v2DB, err := connect(ctx, path, false)
	if err != nil {
		t.Fatal(err)
	}
	v2Store := &Store{db: v2DB, path: path}
	sequence := uint64(7)
	batch := testPushBatch(t, "v2-backup", &sequence)
	if err := v2Store.ApplyIngest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	wantState, err := v2Store.GetIngestState(ctx, batch.Source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := v2Store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	backupDB, err := connect(ctx, path+".pre-migrate-v2-to-v3.sqlite", true)
	if err != nil {
		t.Fatal(err)
	}
	backupStore := &Store{db: backupDB, readOnly: true, path: path + ".pre-migrate-v2-to-v3.sqlite"}
	assertRecordGraph(t, backupStore, source, observation, artifact, base, mixed, segment)
	if gotState, err := backupStore.GetIngestState(ctx, batch.Source.ID); err != nil || !reflect.DeepEqual(gotState, wantState) {
		t.Fatalf("backup ingest state = %#v err=%v, want %#v", gotState, err, wantState)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, backupDB, migrations, 2, false); err != nil {
		t.Fatalf("verify v2 backup: %v", err)
	}
	backupStore.Close()
	backupPath := path + ".pre-migrate-v2-to-v3.sqlite"
	backupBytes, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read v2 backup: %v", err)
	}
	restoredPath := filepath.Join(dir, "restored.sqlite")
	if err := os.WriteFile(restoredPath, backupBytes, 0o600); err != nil {
		t.Fatalf("restore v2 backup: %v", err)
	}
	restored, err := Open(ctx, restoredPath)
	if err != nil {
		t.Fatalf("Open restored v2 backup: %v", err)
	}
	defer restored.Close()
	var version int
	if err := restored.db.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version); err != nil || version != 9 {
		t.Fatalf("restored version = %d err=%v, want 9", version, err)
	}
	assertRecordGraph(t, restored, source, observation, artifact, base, mixed, segment)
	if gotState, err := restored.GetIngestState(ctx, batch.Source.ID); err != nil || !reflect.DeepEqual(gotState, wantState) {
		t.Fatalf("restored ingest state = %#v err=%v, want %#v", gotState, err, wantState)
	}
}

func TestVersionTwoMigrationBackupConflictAndReadOnlyRefusal(t *testing.T) {
	ctx := context.Background()
	t.Run("backup conflict", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "records.sqlite")
		createVersionTwo(t, path)
		if err := os.WriteFile(path+".pre-migrate-v2-to-v3.sqlite", []byte("occupied"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(ctx, path); !IsCode(err, CodeConflict) {
			t.Fatalf("Open error = %v, want conflict", err)
		}
		assertMigrationVersion(t, path, 2)
	})
	t.Run("read only old version", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "records.sqlite")
		createVersionTwo(t, path)
		if _, err := OpenReadOnly(ctx, path); !IsCode(err, CodeReadOnly) {
			t.Fatalf("OpenReadOnly error = %v, want read_only", err)
		}
		if _, err := os.Stat(path + ".pre-migrate-v2-to-v3.sqlite"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read-only open created backup: %v", err)
		}
	})
}

func TestLexicalMigrationRejectsDamageAndPlausibleNewerVersion(t *testing.T) {
	ctx := context.Background()
	t.Run("damaged projection", func(t *testing.T) {
		store := openLexicalStore(t)
		representation, content, _ := addLexicalDocument(t, store, "migration-damage", "alpha")
		if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM segment_lexical_fts`); err != nil {
			t.Fatal(err)
		}
		path := store.path
		store.Close()
		if _, err := Open(ctx, path); !IsCode(err, CodeIntegrity) {
			t.Fatalf("Open damaged error = %v, want integrity", err)
		}
	})
	t.Run("plausible newer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "newer.sqlite")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 10, name = 'future' WHERE version = 6`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 10`); err != nil {
			t.Fatal(err)
		}
		store.Close()
		if _, err := Open(ctx, path); !IsCode(err, CodeIncompatibleSchema) {
			t.Fatalf("Open newer error = %v, want incompatible_schema", err)
		}
	})
}

func assertMigrationVersion(t *testing.T, path string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow(`SELECT max(version) FROM schema_migrations`).Scan(&got); err != nil || got != want {
		t.Fatalf("migration version = %d err=%v, want %d", got, err, want)
	}
}

func createVersionOne(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data, err := migrationFiles.ReadFile("migrations/0001_records.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(context.Background(), conn, migration{version: 1, name: "records", sql: data, hash: sha256.Sum256(data)}); err != nil {
		t.Fatal(err)
	}
}

func createVersionTwo(t *testing.T, path string) {
	t.Helper()
	createVersionOne(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data, err := migrationFiles.ReadFile("migrations/0002_ingest.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(context.Background(), conn, migration{version: 2, name: "ingest", sql: data, hash: sha256.Sum256(data)}); err != nil {
		t.Fatal(err)
	}
}

func createVersionThree(t *testing.T, path string) {
	t.Helper()
	createVersionTwo(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data, err := migrationFiles.ReadFile("migrations/0003_lexical.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(context.Background(), conn, migration{version: 3, name: "lexical", sql: data, hash: sha256.Sum256(data)}); err != nil {
		t.Fatal(err)
	}
}

func createVersionFour(t *testing.T, path string) {
	t.Helper()
	createVersionThree(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data, err := migrationFiles.ReadFile("migrations/0004_classifications.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(context.Background(), conn, migration{version: 4, name: "classifications", sql: data, hash: sha256.Sum256(data)}); err != nil {
		t.Fatal(err)
	}
}

func createVersionFive(t *testing.T, path string) {
	t.Helper()
	createVersionFour(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data, err := migrationFiles.ReadFile("migrations/0005_policy_definitions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(context.Background(), conn, migration{version: 5, name: "policy_definitions", sql: data, hash: sha256.Sum256(data)}); err != nil {
		t.Fatal(err)
	}
}

func createVersionSix(t *testing.T, path string) {
	t.Helper()
	createVersionFive(t, path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	data, err := migrationFiles.ReadFile("migrations/0006_policy_bindings.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := applyMigration(context.Background(), conn, migration{version: 6, name: "policy_bindings", sql: data, hash: sha256.Sum256(data)}); err != nil {
		t.Fatal(err)
	}
}

func seedVersionOneRecordGraph(t *testing.T, path string) (mousa.Source, mousa.Observation, mousa.Artifact, mousa.Representation, mousa.Representation, mousa.Segment) {
	t.Helper()
	ctx := context.Background()
	db, err := connect(ctx, path, false)
	if err != nil {
		t.Fatalf("connect v1: %v", err)
	}
	store := &Store{db: db, path: path}
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
			t.Fatalf("put v1 %s: %v", write.name, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close v1 record graph: %v", err)
	}
	return source, observation, artifact, base, mixed, segment
}

func assertRecordGraph(t *testing.T, store *Store, source mousa.Source, observation mousa.Observation, artifact mousa.Artifact, base, mixed mousa.Representation, segment mousa.Segment) {
	t.Helper()
	ctx := context.Background()
	checks := []struct {
		name string
		got  func() (any, error)
		want any
	}{
		{"source", func() (any, error) { return store.GetSource(ctx, source.ID) }, source},
		{"observation", func() (any, error) { return store.GetObservation(ctx, observation.ID) }, observation},
		{"artifact", func() (any, error) { return store.GetArtifact(ctx, artifact.ID) }, artifact},
		{"base representation", func() (any, error) { return store.GetRepresentation(ctx, base.ID) }, base},
		{"mixed representation", func() (any, error) { return store.GetRepresentation(ctx, mixed.ID) }, mixed},
		{"segment", func() (any, error) { return store.GetSegment(ctx, segment.ID) }, segment},
	}
	for _, check := range checks {
		got, err := check.got()
		if err != nil {
			t.Fatalf("get %s: %v", check.name, err)
		}
		if !reflect.DeepEqual(got, check.want) {
			t.Fatalf("%s = %#v, want %#v", check.name, got, check.want)
		}
	}
}

func TestOpenFailsClosedForIncompatibleAndDamagedState(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
		code   Code
	}{
		{"foreign application", func(t *testing.T, path string) {
			rawExec(t, path, `PRAGMA application_id = 7; CREATE TABLE foreign_table(id INTEGER)`)
		}, CodeIncompatibleSchema},
		{"newer migration", func(t *testing.T, path string) {
			createCurrent(t, path)
			rawExec(t, path, `UPDATE schema_migrations SET version = 10 WHERE version = 6`)
		}, CodeIncompatibleSchema},
		{"changed hash", func(t *testing.T, path string) {
			createCurrent(t, path)
			rawExec(t, path, `UPDATE schema_migrations SET sha256 = randomblob(32) WHERE version = 1`)
		}, CodeIntegrity},
		{"missing object", func(t *testing.T, path string) { createCurrent(t, path); rawExec(t, path, `DROP TABLE segments`) }, CodeIntegrity},
		{"noncanonical record", func(t *testing.T, path string) {
			ctx := context.Background()
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if err := store.PutSource(ctx, testSource(t)); err != nil {
				t.Fatalf("PutSource: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			rawExec(t, path, `UPDATE sources SET record_json = CAST(CAST(' ' AS BLOB) || record_json AS BLOB)`)
		}, CodeIntegrity},
		{"malformed schema", func(t *testing.T, path string) {
			createCurrent(t, path)
			rawExec(t, path, `PRAGMA writable_schema=ON; UPDATE sqlite_schema SET sql='broken' WHERE name='sources'; PRAGMA writable_schema=OFF`)
		}, CodeIntegrity},
		{"truncated", func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("not sqlite"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, CodeIntegrity},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "records.sqlite")
			test.mutate(t, path)
			before, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read before: %v", readErr)
			}
			store, err := Open(ctx, path)
			if store != nil {
				store.Close()
				t.Fatal("Open returned a store")
			}
			if !IsCode(err, test.code) {
				t.Fatalf("Open error = %v, want code %s", err, test.code)
			}
			if test.code == CodeIncompatibleSchema {
				after, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatalf("read after: %v", readErr)
				}
				if string(after) != string(before) {
					t.Fatal("incompatible open changed database bytes")
				}
			}
		})
	}
}

func TestOpenRejectsFutureSchemaWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name string
		open func(context.Context, string) (*Store, error)
	}{
		{"writable", Open},
		{"read-only", OpenReadOnly},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "future.sqlite")
			createCurrent(t, path)
			rawExec(t, path, `
				UPDATE schema_migrations SET version = 10 WHERE version = 6;
				CREATE TABLE future_object(id INTEGER PRIMARY KEY) STRICT;
			`)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read before open: %v", err)
			}

			store, err := test.open(context.Background(), path)
			if store != nil {
				store.Close()
				t.Fatal("open returned a store")
			}
			if !IsCode(err, CodeIncompatibleSchema) {
				t.Fatalf("open error = %v, want incompatible_schema", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("read after open: %v", readErr)
			}
			if string(after) != string(before) {
				t.Fatal("incompatible open changed database bytes")
			}
		})
	}
}

func TestCurrentWritableReopenDoesNotChangeDatabaseBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "current.sqlite")
	createCurrent(t, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before reopen: %v", err)
	}
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after reopen: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("current writable reopen changed database bytes")
	}
}

func TestMigrationLoaderRejectsDuplicateVersions(t *testing.T) {
	_, err := loadMigrations(fstest.MapFS{
		"migrations/0001_first.sql":  {Data: []byte(`SELECT 1;`)},
		"migrations/0001_second.sql": {Data: []byte(`SELECT 2;`)},
	})
	if err == nil {
		t.Fatal("loadMigrations accepted duplicate version 1")
	}
}

func TestTruncatedDatabaseFailsWithoutRepairWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "truncated.sqlite")
	before := []byte("not sqlite")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatalf("write truncated database: %v", err)
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
		t.Fatal("failed startup changed truncated database bytes")
	}
}

func TestReadOnlyStoreRejectsEveryWriteMethodFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readonly.sqlite")
	createCurrent(t, path)
	store, err := OpenReadOnly(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	for _, write := range []struct {
		name string
		run  func() error
	}{
		{"PutSource", func() error { return store.PutSource(ctx, mousa.Source{}) }},
		{"PutObservation", func() error { return store.PutObservation(ctx, mousa.Observation{}) }},
		{"PutArtifact", func() error { return store.PutArtifact(ctx, mousa.Artifact{}) }},
		{"PutRepresentation", func() error { return store.PutRepresentation(ctx, mousa.Representation{}) }},
		{"PutSegment", func() error { return store.PutSegment(ctx, mousa.Segment{}) }},
		{"PutPolicyDefinition", func() error { return store.PutPolicyDefinition(ctx, mousa.PolicyDefinition{}) }},
		{"Checkpoint", func() error { return store.Checkpoint(ctx) }},
		{"Backup", func() error { return store.Backup(ctx, filepath.Join(t.TempDir(), "backup.sqlite")) }},
	} {
		t.Run(write.name, func(t *testing.T) {
			if err := write.run(); !IsCode(err, CodeReadOnly) {
				t.Fatalf("error = %v, want read_only", err)
			}
		})
	}
}

func TestOpenReadOnlyVersionZeroDoesNotMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.sqlite")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("create empty database: %v", err)
	}
	store, err := OpenReadOnly(context.Background(), path)
	if store != nil {
		store.Close()
		t.Fatal("OpenReadOnly returned a store")
	}
	if !IsCode(err, CodeReadOnly) {
		t.Fatalf("OpenReadOnly error = %v, want read_only", err)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat empty database: %v", statErr)
	}
	if info.Size() != 0 {
		t.Fatalf("empty database grew to %d bytes", info.Size())
	}
}

func TestOpenReadOnlyReadsWithoutMutationAndRejectsWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "records.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	source := testSource(t)
	if err := store.PutSource(ctx, source); err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	if _, err := readOnly.GetSource(ctx, source.ID); err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if err := readOnly.PutSource(ctx, source); !IsCode(err, CodeReadOnly) {
		t.Fatalf("PutSource error = %v, want read_only", err)
	}
	if err := readOnly.PutSource(ctx, mousa.Source{}); !IsCode(err, CodeReadOnly) {
		t.Fatalf("invalid PutSource error = %v, want read_only precedence", err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatalf("Close read-only: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("read-only open changed database bytes")
	}
}

func createCurrent(t *testing.T, path string) {
	t.Helper()
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open current: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close current: %v", err)
	}
}

func rawExec(t *testing.T, path, statement string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("raw exec: %v", err)
	}
}

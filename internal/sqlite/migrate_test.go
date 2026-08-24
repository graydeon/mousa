package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"os"
	"path/filepath"
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
			rawExec(t, path, `UPDATE schema_migrations SET version = 2 WHERE version = 1`)
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
				UPDATE schema_migrations SET version = 2 WHERE version = 1;
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
			createCurrent(t, path)
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
		})
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

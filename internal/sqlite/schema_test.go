package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
	_ "modernc.org/sqlite"
)

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
		{"newer schema version", func(t *testing.T, path string) {
			createCurrent(t, path)
			rawExec(t, path, `PRAGMA user_version = 2`)
		}, CodeIncompatibleSchema},
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

// Every store this binary cannot read is refused from the header alone, before any record is
// verified, so an earlier store reports re-sync rather than an integrity failure.
func TestOpenRefusesOtherStoresBeforeVerifyingRecords(t *testing.T) {
	for _, test := range []struct {
		name   string
		header string
		resync bool
	}{
		{"pre-rework application ID", `PRAGMA application_id = 1297044819`, true},
		{"earlier schema version", fmt.Sprintf(`PRAGMA application_id = %d; PRAGMA user_version = 0`, applicationID), true},
		{"foreign application ID", `PRAGMA application_id = 7`, false},
		{"no application ID", `PRAGMA application_id = 0`, false},
	} {
		for _, open := range []struct {
			name string
			open func(context.Context, string) (*Store, error)
		}{
			{"writable", Open},
			{"read-only", OpenReadOnly},
		} {
			t.Run(test.name+"/"+open.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "other.sqlite")
				// A malformed record that verification would report as integrity damage.
				rawExec(t, path, test.header+`; CREATE TABLE sources(id BLOB PRIMARY KEY, record_json BLOB); INSERT INTO sources VALUES (x'00', x'7b')`)
				before, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				store, err := open.open(context.Background(), path)
				if store != nil {
					store.Close()
					t.Fatal("open returned a store")
				}
				if !IsCode(err, CodeIncompatibleSchema) {
					t.Fatalf("open error = %v, want incompatible_schema", err)
				}
				if got := errors.Is(err, errResyncRequired); got != test.resync {
					t.Fatalf("open error = %v, re-sync = %v, want %v", err, got, test.resync)
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(after, before) {
					t.Fatal("refused open changed database bytes")
				}
			})
		}
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
				PRAGMA user_version = 2;
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
		{"SetSourceAccess", func() error {
			_, err := store.SetSourceAccess(ctx, mousa.SourceID{1}, mousa.SourceAccessDeny)
			return err
		}},
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

func TestOpenReadOnlyEmptyFileDoesNotCreateSchema(t *testing.T) {
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

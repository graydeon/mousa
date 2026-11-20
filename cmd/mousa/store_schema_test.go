package main

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdata/pre-rework-store.sqlite.gz was written by the binary built from the pre-rework tag:
// one JSONL source "fixture" with one item, synced and queried once, so it holds a stored
// decision and a trail.
func preReworkStore(t *testing.T) string {
	t.Helper()
	compressed, err := os.Open(filepath.Join("testdata", "pre-rework-store.sqlite.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	reader, err := gzip.NewReader(compressed)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "pre-rework.sqlite")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStoreSchemaRefusesOtherStores(t *testing.T) {
	foreign := filepath.Join(t.TempDir(), "foreign.sqlite")
	db, err := sql.Open("sqlite", foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT); INSERT INTO notes(body) VALUES ('not mousa')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		store string
		want  string
	}{
		{"pre-rework store", preReworkStore(t), "re-sync it from its sources"},
		{"non-Mousa SQLite file", foreign, "not a Mousa store"},
	} {
		for _, args := range [][]string{
			{"status", "--source", "fixture"},
			{"query", "--source", "fixture", "heron"},
		} {
			t.Run(test.name+"/"+args[0], func(t *testing.T) {
				before, err := os.ReadFile(test.store)
				if err != nil {
					t.Fatal(err)
				}
				run := testRun{t: t, binary: cliBinary, store: test.store}
				stderr, _ := run.run(true, args...)
				if !strings.Contains(stderr, "incompatible_schema") || !strings.Contains(stderr, test.want) {
					t.Fatalf("stderr = %q, want incompatible_schema and %q", stderr, test.want)
				}
				if strings.Contains(stderr, "integrity") {
					t.Fatalf("refusal verified records first: %q", stderr)
				}
				after, err := os.ReadFile(test.store)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(after, before) {
					t.Fatal("refused store was modified")
				}
			})
		}
	}

	t.Run("fresh store", func(t *testing.T) {
		run, _ := setup(t)
		run.runInput(`{"id":"note","text":"The heron waits by the river."}`+"\n", false, "sync", "--source", "fixture", "--allow")
		_, report := run.run(false, "query", "--source", "fixture", "heron")
		if lenOf(report, "evidence") != 1 {
			t.Fatalf("fresh store query = %v", report)
		}
	})
}

package sqlite

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCreatesCanonicalSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "records.sqlite")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	var applicationID int
	if err := store.db.QueryRow("PRAGMA application_id").Scan(&applicationID); err != nil {
		t.Fatalf("read application_id: %v", err)
	}
	if applicationID != 1297044819 {
		t.Fatalf("application_id = %d, want 1297044819", applicationID)
	}

	rows, err := store.db.Query(`
		SELECT name
		FROM sqlite_schema
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list tables: %v", err)
	}
	want := []string{"artifacts", "callers", "classification_bases", "classifications", "ingest_gaps", "ingest_receipts", "observations", "policy_activations", "policy_binding_state", "policy_bindings", "policy_decision_inputs", "policy_decisions", "policy_definitions", "purposes", "representation_inputs", "representations", "schema_migrations", "segment_lexical_fts", "segment_lexical_fts_config", "segment_lexical_fts_content", "segment_lexical_fts_data", "segment_lexical_fts_docsize", "segment_lexical_fts_idx", "segment_lexical_rows", "segments", "source_ingest_state", "source_trail_candidates", "source_trails", "source_withdrawals", "sources"}
	if !equalStrings(got, want) {
		t.Fatalf("tables = %v, want %v", got, want)
	}

	var version int
	var name string
	var hash []byte
	if err := store.db.QueryRow(`SELECT version, name, sha256 FROM schema_migrations`).Scan(&version, &name, &hash); err != nil {
		if err == sql.ErrNoRows {
			t.Fatal("schema_migrations is empty")
		}
		t.Fatalf("read schema migration: %v", err)
	}
	if version != 1 || name != "records" || len(hash) != 32 {
		t.Fatalf("migration = (%d, %q, %x), want version 1, name records, 32-byte hash", version, name, hash)
	}
	if err := store.db.QueryRow(`SELECT version, name, sha256 FROM schema_migrations WHERE version = 9`).Scan(&version, &name, &hash); err != nil {
		t.Fatalf("read source trails migration: %v", err)
	}
	if version != 9 || name != "source_trails" || len(hash) != 32 {
		t.Fatalf("migration = (%d, %q, %x), want version 9, name source_trails, 32-byte hash", version, name, hash)
	}
}

func TestOpenAppliesAndVerifiesConnectionCapabilities(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "capabilities.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	for pragma, want := range map[string]int{
		"foreign_keys":       1,
		"busy_timeout":       1000,
		"synchronous":        2,
		"wal_autocheckpoint": 1000,
		"trusted_schema":     0,
		"query_only":         0,
	} {
		var got int
		if err := store.db.QueryRow(`PRAGMA ` + pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", pragma, err)
		}
		if got != want {
			t.Fatalf("PRAGMA %s = %d, want %d", pragma, got, want)
		}
	}
	var fts5 int
	if err := store.db.QueryRow(`SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&fts5); err != nil {
		t.Fatalf("check FTS5: %v", err)
	}
	if fts5 != 1 {
		t.Fatal("binding does not provide FTS5")
	}
	var mode string
	if err := store.db.QueryRow(`PRAGMA journal_mode=OFF`).Scan(&mode); err != nil {
		t.Fatalf("defensive journal-mode probe: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("defensive journal-mode probe changed mode to %q", mode)
	}
}

func TestSQLiteRuntimeAndDependencyPins(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "runtime.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	var version string
	if err := store.db.QueryRow(`SELECT sqlite_version()`).Scan(&version); err != nil {
		t.Fatalf("sqlite_version: %v", err)
	}
	if version != "3.53.3" {
		t.Fatalf("sqlite_version = %q, want 3.53.3", version)
	}
	rows, err := store.db.Query(`PRAGMA compile_options`)
	if err != nil {
		t.Fatalf("compile_options: %v", err)
	}
	defer rows.Close()
	var options []string
	for rows.Next() {
		var option string
		if err := rows.Scan(&option); err != nil {
			t.Fatalf("scan compile option: %v", err)
		}
		options = append(options, option)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("compile_options: %v", err)
	}
	if !containsExact(options, "ENABLE_FTS5") {
		t.Fatalf("compile options do not include ENABLE_FTS5: %v", options)
	}
	moduleFile, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	moduleText := string(moduleFile)
	for _, pin := range []string{"modernc.org/sqlite v1.57.0", "modernc.org/libc v1.74.4"} {
		if !strings.Contains(moduleText, pin) {
			t.Fatalf("go.mod does not contain exact pin %q", pin)
		}
	}
}

func containsExact(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

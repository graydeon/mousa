package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func supersessionTestDeclaration(t testing.TB, sourceID mousa.SourceID, predecessorItemID string, predecessorRepresentationID mousa.RepresentationID, successorItemID string, successorRepresentationID mousa.RepresentationID, author, basis string) mousa.SupersessionDeclaration {
	t.Helper()
	id, err := mousa.NewSupersessionDeclarationID(sourceID, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, author, basis)
	if err != nil {
		t.Fatalf("NewSupersessionDeclarationID: %v", err)
	}
	return mousa.SupersessionDeclaration{
		Schema:                      mousa.SupersessionDeclarationSchema,
		ID:                          id,
		SourceID:                    sourceID,
		PredecessorItemID:           predecessorItemID,
		PredecessorRepresentationID: predecessorRepresentationID,
		SuccessorItemID:             successorItemID,
		SuccessorRepresentationID:   successorRepresentationID,
		Author:                      author,
		Basis:                       basis,
	}
}

func supersessionDeclarationRowCount(t testing.TB, store *Store) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM supersession_declarations`).Scan(&count); err != nil {
		t.Fatalf("count supersession declarations: %v", err)
	}
	return count
}

// canonicalRowCounts counts the rows a declaration must never create, change or remove.
func canonicalRowCounts(t testing.TB, store *Store) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for _, table := range []string{"sources", "observations", "artifacts", "representations", "representation_inputs", "segments", "local_items", "segment_lexical_rows"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}

// canonicalRecordDigest hashes every canonical record row plus the row count of the projection
// tables, so an upgrade that rewrites, drops or reorders history changes the digest.
func canonicalRecordDigest(t testing.TB, store *Store) string {
	t.Helper()
	hash := sha256.New()
	for _, table := range []string{"sources", "observations", "artifacts", "representations", "segments"} {
		rows, err := store.db.Query(`SELECT id, record_json FROM ` + table + ` ORDER BY id`)
		if err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		for rows.Next() {
			var id, data []byte
			if err := rows.Scan(&id, &data); err != nil {
				rows.Close()
				t.Fatalf("scan %s row: %v", table, err)
			}
			hash.Write(id)
			hash.Write(data)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close %s: %v", table, err)
		}
	}
	projection := canonicalRowCounts(t, store)
	for _, table := range []string{"representation_inputs", "segments", "local_items", "segment_lexical_rows"} {
		fmt.Fprintf(hash, "%s:%d\n", table, projection[table])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// supersessionFixture prepares two item revisions in one local source without activating either.
func supersessionFixture(t *testing.T, store *Store) (mousa.Source, mousa.Representation, mousa.Representation) {
	t.Helper()
	source, predecessor, _ := prepareLocalRevision(t, store, "doc", "oldterm predecessor evidence")
	_, successor, _ := prepareLocalRevision(t, store, "corrected", "newterm successor evidence")
	return source, predecessor, successor
}

// supersessionCrossSourceFixture adds one canonical representation from a different source, so a
// declaration can name the wrong source while its pinned representation still exists.
func supersessionCrossSourceFixture(t *testing.T) (*Store, mousa.Source, mousa.Representation, mousa.Representation, mousa.Representation) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cross-source.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	source, predecessor, successor := supersessionFixture(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, foreign, _ := seedVersionOneRecordGraph(t, path)
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	return store, source, predecessor, successor, foreign
}

func TestSupersessionDeclarationRoundTripAndExactRetry(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	source, predecessor, successor := supersessionFixture(t, store)
	declaration := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "successor replaces the predecessor")
	before := canonicalRowCounts(t, store)
	if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
		t.Fatalf("PutSupersessionDeclaration: %v", err)
	}
	if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if count := supersessionDeclarationRowCount(t, store); count != 1 {
		t.Fatalf("stored declarations = %d, want 1", count)
	}
	got, err := store.GetSupersessionDeclaration(ctx, declaration.ID)
	if err != nil || !reflect.DeepEqual(got, declaration) {
		t.Fatalf("GetSupersessionDeclaration = %#v, %v", got, err)
	}
	if after := canonicalRowCounts(t, store); !reflect.DeepEqual(after, before) {
		t.Fatalf("declaration write changed canonical rows: before=%v after=%v", before, after)
	}
}

func TestSupersessionDeclarationPersistsAcrossReopenAndReadOnlyRead(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	path := store.path
	source, predecessor, predecessorText := prepareLocalRevision(t, store, "doc", "oldterm persistence evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", predecessor.ID, predecessorText); err != nil {
		t.Fatal(err)
	}
	_, successor, _ := prepareLocalRevision(t, store, "corrected", "newterm persistence evidence")
	declaration := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "persist across reopen")
	if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, err := reopened.GetSupersessionDeclaration(ctx, declaration.ID); err != nil || !reflect.DeepEqual(got, declaration) {
		t.Fatalf("reopened read = %#v, %v", got, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if got, err := readOnly.GetSupersessionDeclaration(ctx, declaration.ID); err != nil || !reflect.DeepEqual(got, declaration) {
		t.Fatalf("read-only read = %#v, %v", got, err)
	}
}

func TestSupersessionDeclarationKeepsPinnedHistoryAfterItemUpdateAndDeletion(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	path := store.path
	source, original, originalText := prepareLocalRevision(t, store, "doc", "oldterm history evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", original.ID, originalText); err != nil {
		t.Fatal(err)
	}
	_, replacement, replacementText := prepareLocalRevision(t, store, "doc", "newterm replacement evidence")
	_, corrected, correctedText := prepareLocalRevision(t, store, "corrected", "corrected history evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "corrected", corrected.ID, correctedText); err != nil {
		t.Fatal(err)
	}
	_, pending, _ := prepareLocalRevision(t, store, "pending", "pending history evidence")

	activeDeclaration := supersessionTestDeclaration(t, source.ID, "doc", original.ID, "corrected", corrected.ID, "example.operations", "both pins were active")
	neverActivatedSuccessor := supersessionTestDeclaration(t, source.ID, "doc", original.ID, "pending", pending.ID, "example.operations", "successor was never activated")
	neitherActivated := supersessionTestDeclaration(t, source.ID, "doc", replacement.ID, "pending", pending.ID, "example.operations", "neither pin was ever activated")
	for _, declaration := range []mousa.SupersessionDeclaration{activeDeclaration, neverActivatedSuccessor, neitherActivated} {
		if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
			t.Fatalf("put %s: %v", declaration.Basis, err)
		}
	}
	if _, err := store.GetLocalItem(ctx, source.ID, "pending"); !IsCode(err, CodeNotFound) {
		t.Fatalf("declaration created a current item: %v", err)
	}
	if action, err := store.ActivateLocalItem(ctx, source.ID, "doc", replacement.ID, replacementText); err != nil || action != "updated" {
		t.Fatalf("update doc = %q, %v", action, err)
	}
	if action, err := store.DeleteLocalItem(ctx, source.ID, "corrected"); err != nil || action != "deleted" {
		t.Fatalf("delete corrected = %q, %v", action, err)
	}

	checks := []mousa.SupersessionDeclaration{activeDeclaration, neverActivatedSuccessor, neitherActivated}
	for _, declaration := range checks {
		got, err := store.GetSupersessionDeclaration(ctx, declaration.ID)
		if err != nil || !reflect.DeepEqual(got, declaration) {
			t.Fatalf("historical read of %s = %#v, %v", declaration.Basis, got, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen after history change: %v", err)
	}
	defer reopened.Close()
	for _, declaration := range checks {
		if got, err := reopened.GetSupersessionDeclaration(ctx, declaration.ID); err != nil || !reflect.DeepEqual(got, declaration) {
			t.Fatalf("reopened historical read of %s = %#v, %v", declaration.Basis, got, err)
		}
	}
}

func TestSupersessionDeclarationRejectsUnpinnedSourceRevisionAndAttribution(t *testing.T) {
	ctx := context.Background()
	store, source, predecessor, successor, foreign := supersessionCrossSourceFixture(t)
	defer store.Close()
	absentSource, err := mousa.NewSourceID("mousa-local", "absent-source")
	if err != nil {
		t.Fatal(err)
	}
	var absentRepresentation mousa.RepresentationID
	absentRepresentation[0] = 0x5a
	cases := []struct {
		name        string
		declaration mousa.SupersessionDeclaration
		code        Code
	}{
		{"absent source", supersessionTestDeclaration(t, absentSource, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "absent source"), CodeNotFound},
		{"absent revision", supersessionTestDeclaration(t, source.ID, "doc", absentRepresentation, "corrected", successor.ID, "example.operations", "absent revision"), CodeNotFound},
		{"representation from another source", supersessionTestDeclaration(t, source.ID, "doc", foreign.ID, "corrected", successor.ID, "example.operations", "another source"), CodeIntegrity},
		{"wrong item attribution", supersessionTestDeclaration(t, source.ID, "other-doc", predecessor.ID, "corrected", successor.ID, "example.operations", "wrong item"), CodeIntegrity},
		{"wrong successor attribution", supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "other-corrected", successor.ID, "example.operations", "wrong successor item"), CodeIntegrity},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if err := store.PutSupersessionDeclaration(ctx, testCase.declaration); !IsCode(err, testCase.code) {
				t.Fatalf("PutSupersessionDeclaration = %v, want %s", err, testCase.code)
			}
			if count := supersessionDeclarationRowCount(t, store); count != 0 {
				t.Fatalf("rejected declaration left %d rows", count)
			}
			if _, err := store.GetSupersessionDeclaration(ctx, testCase.declaration.ID); !IsCode(err, CodeNotFound) {
				t.Fatalf("GetSupersessionDeclaration = %v, want %s", err, CodeNotFound)
			}
		})
	}
}

func TestSupersessionDeclarationRejectsInvalidStructureAndIdentityMismatch(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	source, predecessor, successor := supersessionFixture(t, store)
	valid := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "valid")
	other := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "other basis")
	cases := []struct {
		name   string
		mutate func(*mousa.SupersessionDeclaration)
	}{
		{"identity mismatch", func(d *mousa.SupersessionDeclaration) { d.ID = other.ID }},
		{"zero source", func(d *mousa.SupersessionDeclaration) { d.SourceID = mousa.SourceID{} }},
		{"shared item", func(d *mousa.SupersessionDeclaration) { d.SuccessorItemID = d.PredecessorItemID }},
		{"invalid item UTF-8", func(d *mousa.SupersessionDeclaration) { d.PredecessorItemID = string([]byte{0xff, 0xfe}) }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			declaration := valid
			testCase.mutate(&declaration)
			if err := store.PutSupersessionDeclaration(ctx, declaration); !IsCode(err, CodeInvalidRecord) {
				t.Fatalf("PutSupersessionDeclaration = %v, want %s", err, CodeInvalidRecord)
			}
			if count := supersessionDeclarationRowCount(t, store); count != 0 {
				t.Fatalf("invalid declaration left %d rows", count)
			}
		})
	}
}

func TestSupersessionDeclarationReadOnlyAndCancelledWriteLeaveNoRow(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	path := store.path
	source, predecessor, successor := supersessionFixture(t, store)
	declaration := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "read-only and cancellation")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := readOnly.PutSupersessionDeclaration(ctx, declaration); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only PutSupersessionDeclaration = %v, want %s", err, CodeReadOnly)
	}
	if count := supersessionDeclarationRowCount(t, readOnly); count != 0 {
		t.Fatalf("read-only store holds %d declarations", count)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.PutSupersessionDeclaration(cancelled, declaration); err == nil {
		t.Fatal("cancelled PutSupersessionDeclaration succeeded")
	}
	if count := supersessionDeclarationRowCount(t, store); count != 0 {
		t.Fatalf("cancelled write left %d rows", count)
	}
	if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
		t.Fatalf("write after cancellation: %v", err)
	}
	if count := supersessionDeclarationRowCount(t, store); count != 1 {
		t.Fatalf("stored declarations = %d, want 1", count)
	}
}

func TestSupersessionDeclarationTamperingFailsClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("record json replaced", func(t *testing.T) {
		store := openLexicalStore(t)
		defer store.Close()
		source, predecessor, successor := supersessionFixture(t, store)
		first := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "first record")
		second := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "second record")
		for _, declaration := range []mousa.SupersessionDeclaration{first, second} {
			if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
				t.Fatal(err)
			}
		}
		replacement, err := mousa.EncodeSupersessionDeclaration(second)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE supersession_declarations SET record_json = ? WHERE id = ?`, replacement, first.ID[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSupersessionDeclaration(ctx, first.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSupersessionDeclaration = %v, want %s", err, CodeIntegrity)
		}
		if err := store.PutSupersessionDeclaration(ctx, first); !IsCode(err, CodeConflict) {
			t.Fatalf("PutSupersessionDeclaration = %v, want %s", err, CodeConflict)
		}
		var stored []byte
		if err := store.db.QueryRowContext(ctx, `SELECT record_json FROM supersession_declarations WHERE id = ?`, first.ID[:]).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(stored, replacement) {
			t.Fatal("reused identity rewrote or repaired the stored bytes")
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if reopened, err := Open(ctx, store.path); reopened != nil || !IsCode(err, CodeIntegrity) {
			if reopened != nil {
				reopened.Close()
			}
			t.Fatalf("startup verification = %v, want %s", err, CodeIntegrity)
		}
	})

	t.Run("projection replaced", func(t *testing.T) {
		store := openLexicalStore(t)
		defer store.Close()
		source, predecessor, successor := supersessionFixture(t, store)
		declaration := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "projection tamper")
		if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE supersession_declarations SET predecessor_item_id = ? WHERE id = ?`, "other-doc", declaration.ID[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSupersessionDeclaration(ctx, declaration.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSupersessionDeclaration = %v, want %s", err, CodeIntegrity)
		}
		if err := store.PutSupersessionDeclaration(ctx, declaration); !IsCode(err, CodeConflict) {
			t.Fatalf("PutSupersessionDeclaration = %v, want %s", err, CodeConflict)
		}
		if err := verifySupersessionDeclarationRecords(ctx, store.db); !IsCode(err, CodeIntegrity) {
			t.Fatalf("verifySupersessionDeclarationRecords = %v, want %s", err, CodeIntegrity)
		}
	})

	t.Run("pin ancestry removed", func(t *testing.T) {
		store := openLexicalStore(t)
		defer store.Close()
		source, predecessor, successor := supersessionFixture(t, store)
		declaration := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "provenance tamper")
		if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM representation_inputs WHERE representation_id = ?`, predecessor.ID[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSupersessionDeclaration(ctx, declaration.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSupersessionDeclaration = %v, want %s", err, CodeIntegrity)
		}
		if err := verifySupersessionDeclarationRecords(ctx, store.db); !IsCode(err, CodeIntegrity) {
			t.Fatalf("verifySupersessionDeclarationRecords = %v, want %s", err, CodeIntegrity)
		}
	})

	t.Run("forged row pinned to another source", func(t *testing.T) {
		store, source, _, successor, foreign := supersessionCrossSourceFixture(t)
		defer store.Close()
		declaration := supersessionTestDeclaration(t, source.ID, "doc", foreign.ID, "corrected", successor.ID, "example.operations", "forged provenance")
		data, err := mousa.EncodeSupersessionDeclaration(declaration)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO supersession_declarations(id, source_id, predecessor_item_id, predecessor_representation_id, successor_item_id, successor_representation_id, record_json) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			declaration.ID[:], source.ID[:], declaration.PredecessorItemID, declaration.PredecessorRepresentationID[:], declaration.SuccessorItemID, declaration.SuccessorRepresentationID[:], data); err != nil {
			t.Fatalf("forged insert: %v", err)
		}
		if _, err := store.GetSupersessionDeclaration(ctx, declaration.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSupersessionDeclaration = %v, want %s", err, CodeIntegrity)
		}
		if err := verifySupersessionDeclarationRecords(ctx, store.db); !IsCode(err, CodeIntegrity) {
			t.Fatalf("verifySupersessionDeclarationRecords = %v, want %s", err, CodeIntegrity)
		}
	})
}

func TestSupersessionDeclarationByteLimitBoundaries(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	source, predecessor, successor := supersessionFixture(t, store)

	// The domain limit counts UTF-8 bytes: an item identity of exactly 4096 bytes is accepted.
	atLimit := strings.Repeat("a", mousa.MaxSupersessionItemBytes)
	overLimit := strings.Repeat("a", mousa.MaxSupersessionItemBytes+1)
	_, longRevision, _ := prepareLocalRevision(t, store, atLimit, "long item boundary evidence")
	atLimitDeclaration := supersessionTestDeclaration(t, source.ID, atLimit, longRevision.ID, "corrected", successor.ID, "example.operations", "item identity at the byte limit")
	if err := store.PutSupersessionDeclaration(ctx, atLimitDeclaration); err != nil {
		t.Fatalf("4096-byte item identity: %v", err)
	}
	if got, err := store.GetSupersessionDeclaration(ctx, atLimitDeclaration.ID); err != nil || !reflect.DeepEqual(got, atLimitDeclaration) {
		t.Fatalf("readback at the byte limit = %#v, %v", got, err)
	}
	// The oversized label is refused by the encoder before any SQL, so the placeholder identity is
	// never compared.
	overLimitDeclaration := atLimitDeclaration
	overLimitDeclaration.PredecessorItemID = overLimit
	if err := store.PutSupersessionDeclaration(ctx, overLimitDeclaration); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("4097-byte item identity = %v, want %s", err, CodeInvalidRecord)
	}

	// The projected column bound counts UTF-8 bytes rather than characters: 2048 two-byte
	// characters are exactly 4096 bytes, while 2049 are 4098 bytes and must be refused.
	insert := func(id byte, predecessorItemID, successorItemID string, record []byte) error {
		_, err := store.db.ExecContext(ctx, `INSERT INTO supersession_declarations(id, source_id, predecessor_item_id, predecessor_representation_id, successor_item_id, successor_representation_id, record_json) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			bytes.Repeat([]byte{id}, 32), source.ID[:], predecessorItemID, predecessor.ID[:], successorItemID, successor.ID[:], record)
		return err
	}
	if err := insert(0x21, strings.Repeat("é", 2048), "corrected", []byte("{}")); err != nil {
		t.Fatalf("4096-byte two-byte item identity: %v", err)
	}
	if err := insert(0x22, strings.Repeat("é", 2049), "corrected", []byte("{}")); err == nil || !sqliteConstraint(err) {
		t.Fatalf("4098-byte two-byte item identity = %v, want a constraint failure", err)
	}
	if err := insert(0x23, "doc", "corrected", bytes.Repeat([]byte{'x'}, 65536)); err != nil {
		t.Fatalf("65536-byte record: %v", err)
	}
	if err := insert(0x24, "doc", "corrected", bytes.Repeat([]byte{'x'}, 65537)); err == nil || !sqliteConstraint(err) {
		t.Fatalf("65537-byte record = %v, want a constraint failure", err)
	}
}

func TestSupersessionDeclarationMigrationExactBytesHashAndObjects(t *testing.T) {
	ctx := context.Background()
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) < 12 || migrations[11].version != 12 || migrations[11].name != "supersession_declarations" {
		t.Fatalf("migration 12 = %#v, want version 12 name supersession_declarations", migrations)
	}
	if len(migrations[11].sql) != 1660 || fmt.Sprintf("%x", migrations[11].hash) != "8c1a5fcbf2bae31600bb5bafeeed06da77e830687ea9b21363b8a2bcaf3919f5" || migrations[11].sql[len(migrations[11].sql)-1] != '\n' {
		t.Fatalf("migration 12 bytes/hash/newline = %d/%x/%v", len(migrations[11].sql), migrations[11].hash, migrations[11].sql[len(migrations[11].sql)-1] == '\n')
	}
	store, err := Open(ctx, filepath.Join(t.TempDir(), "supersession.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var createSQL string
	if err := store.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'supersession_declarations'`).Scan(&createSQL); err != nil {
		t.Fatalf("supersession_declarations table: %v", err)
	}
	if !strings.Contains(createSQL, "STRICT") || !strings.Contains(createSQL, "length(CAST(predecessor_item_id AS BLOB))") {
		t.Fatalf("supersession_declarations definition = %s", createSQL)
	}
	// Migration 0013 adds supersession_declarations_id_source_idx, the composite parent key that the
	// same-source activation references need; it is the only index on this table.
	var indexes int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND tbl_name = 'supersession_declarations' AND name NOT LIKE 'sqlite_%'`).Scan(&indexes); err != nil || indexes != 1 {
		t.Fatalf("supersession_declarations indexes = %d, %v; want exactly the composite parent key", indexes, err)
	}
	if err := verifyVersion(ctx, store.db, migrations, len(migrations), true, false); err != nil {
		t.Fatalf("startup verification at version %d: %v", len(migrations), err)
	}
	var storedHash []byte
	if err := store.db.QueryRowContext(ctx, `SELECT sha256 FROM schema_migrations WHERE version = 12 AND name = 'supersession_declarations'`).Scan(&storedHash); err != nil || !bytes.Equal(storedHash, migrations[11].hash[:]) {
		t.Fatalf("stored migration hash = %x, %v", storedHash, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE schema_migrations SET sha256 = zeroblob(32) WHERE version = 12`); err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, store.db, migrations, len(migrations), true, false); !IsCode(err, CodeIntegrity) {
		t.Fatalf("verifyVersion with a rewritten migration hash = %v, want %s", err, CodeIntegrity)
	}
}

func TestSupersessionDeclarationMigrationUpgradesPriorVersionWithoutChangingRecords(t *testing.T) {
	ctx := context.Background()
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	store := openLexicalStore(t)
	path := store.path
	source, predecessor, predecessorText := prepareLocalRevision(t, store, "doc", "oldterm upgrade evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "doc", predecessor.ID, predecessorText); err != nil {
		t.Fatal(err)
	}
	_, successor, successorText := prepareLocalRevision(t, store, "corrected", "newterm upgrade evidence")
	if _, err := store.ActivateLocalItem(ctx, source.ID, "corrected", successor.ID, successorText); err != nil {
		t.Fatal(err)
	}
	declaration := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "upgrade fixture")
	if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
		t.Fatal(err)
	}
	beforeDigest := canonicalRecordDigest(t, store)
	beforeCandidates, err := store.SearchVerifiedLexical(ctx, "oldterm OR newterm", 10)
	if err != nil || len(beforeCandidates) != 2 {
		t.Fatalf("fixture lexical candidates = %#v, %v", beforeCandidates, err)
	}

	// Synthesize version 11: it has no supersession tables, and this slice must not change any
	// canonical record or lexical evidence on the way to the current version.
	if _, err := store.db.ExecContext(ctx, `DROP TABLE supersession_activation_state; DROP TABLE supersession_activations; DROP TABLE supersession_declarations; DELETE FROM schema_migrations WHERE version > 11`); err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, store.db, migrations, 11, true, false); err != nil {
		t.Fatalf("invalid version-11 fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if readOnly, err := OpenReadOnly(ctx, path); readOnly != nil || !IsCode(err, CodeReadOnly) {
		if readOnly != nil {
			readOnly.Close()
		}
		t.Fatalf("read-only version-11 open = %v, %v", readOnly, err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade open: %v", err)
	}
	defer store.Close()
	if afterDigest := canonicalRecordDigest(t, store); afterDigest != beforeDigest {
		t.Fatalf("upgrade changed canonical records: before=%s after=%s", beforeDigest, afterDigest)
	}
	afterCandidates, err := store.SearchVerifiedLexical(ctx, "oldterm OR newterm", 10)
	if err != nil || !reflect.DeepEqual(afterCandidates, beforeCandidates) {
		t.Fatalf("upgrade changed lexical evidence: before=%#v after=%#v err=%v", beforeCandidates, afterCandidates, err)
	}
	if count := supersessionDeclarationRowCount(t, store); count != 0 {
		t.Fatalf("upgraded store holds %d declarations, want 0", count)
	}
	backupPath := path + ".pre-migrate-v11-to-v12.sqlite"
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("pre-migration backup: %v", err)
	}
	backup, err := connect(ctx, backupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := verifyVersion(ctx, backup, migrations, 11, false, true); err != nil {
		t.Fatalf("verify version-11 backup: %v", err)
	}
}

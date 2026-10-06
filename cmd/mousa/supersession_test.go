package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
	modernsqlite "modernc.org/sqlite"
)

// The declaration administration tests run the built CLI against real temporary SQLite stores in
// separate process invocations, so storage, reopen behavior and exit conventions are exercised
// through the native binary rather than an in-process store handle.

// runExitInput runs the CLI with input on stdin and reports its exit code, stdout and stderr. The
// exact code is part of the command contract: 2 for an invalid invocation, 1 for a rejected
// operation.
func (r testRun) runExitInput(input string, args ...string) (int, string, string) {
	r.t.Helper()
	cmd := exec.Command(r.binary, append([]string{"-store", r.store}, args...)...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		r.t.Fatalf("run %v: %v", args, err)
	}
	return exitErr.ExitCode(), stdout.String(), stderr.String()
}

func (r testRun) runExit(args ...string) (int, string, string) {
	return r.runExitInput("", args...)
}

// declarationSource resolves the source identity the declaration must name for one JSONL stream.
func declarationSource(t *testing.T, externalSourceID string) mousa.Source {
	t.Helper()
	source, err := streamSource(externalSourceID)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

// revisionPin discovers one item revision through the supported query interface: the item and
// representation the CLI reports for a token that only that revision contains. No metadata
// command is added to expose revision identities.
func revisionPin(t *testing.T, run testRun, externalSourceID, token string) (string, mousa.RepresentationID) {
	t.Helper()
	report := querySource(t, run, externalSourceID, token)
	evidence := hits(report)
	if len(evidence) != 1 {
		t.Fatalf("query %q returned %d hits, want 1: %v", token, len(evidence), report)
	}
	hit := evidence[0].(map[string]any)
	id, err := mousa.ParseRepresentationID(hit["representation_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return hit["item"].(string), id
}

// declarationJSON returns the canonical encoding of one declaration.
func declarationJSON(t *testing.T, source mousa.Source, predecessorItemID string, predecessorRepresentationID mousa.RepresentationID, successorItemID string, successorRepresentationID mousa.RepresentationID, author, basis string) []byte {
	t.Helper()
	id, err := mousa.NewSupersessionDeclarationID(source.ID, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, author, basis)
	if err != nil {
		t.Fatal(err)
	}
	data, err := mousa.EncodeSupersessionDeclaration(mousa.SupersessionDeclaration{
		Schema:                      mousa.SupersessionDeclarationSchema,
		ID:                          id,
		SourceID:                    source.ID,
		PredecessorItemID:           predecessorItemID,
		PredecessorRepresentationID: predecessorRepresentationID,
		SuccessorItemID:             successorItemID,
		SuccessorRepresentationID:   successorRepresentationID,
		Author:                      author,
		Basis:                       basis,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// declarationPins prepares one JSONL source with two revisions and returns its identity with both
// installed revision pins.
func declarationPins(t *testing.T, run testRun, externalSourceID string) (mousa.Source, string, mousa.RepresentationID, string, mousa.RepresentationID) {
	t.Helper()
	jsonlSync(t, run, externalSourceID, strings.Join([]string{
		record("doc", "oldterm predecessor evidence"),
		record("corrected", "newterm successor evidence"),
	}, "\n"))
	predecessorItemID, predecessorRepresentationID := revisionPin(t, run, externalSourceID, "oldterm")
	successorItemID, successorRepresentationID := revisionPin(t, run, externalSourceID, "newterm")
	return declarationSource(t, externalSourceID), predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID
}

func openRawStore(t *testing.T, path string) *sql.DB {
	t.Helper()
	connector, err := modernsqlite.NewConnector("file:" + path + "?mode=ro&_query_only=1")
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// rawExec runs one statement outside the CLI so a test can inject a damaged stored record.
func rawExec(t *testing.T, path, statement string) {
	t.Helper()
	connector, err := modernsqlite.NewConnector("file:" + path + "?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("raw exec %q: %v", statement, err)
	}
}

// canonicalDigest hashes the canonical record rows and projection counts a declaration write or
// read must never change. It follows the internal store tests' digest so no weaker check replaces
// them.
func canonicalDigest(t *testing.T, path string) string {
	t.Helper()
	db := openRawStore(t, path)
	hash := sha256.New()
	for _, table := range []string{"sources", "observations", "artifacts", "representations", "segments"} {
		rows, err := db.Query(`SELECT id, record_json FROM ` + table + ` ORDER BY id`)
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
	for _, table := range []string{"representation_inputs", "local_items", "segment_lexical_rows"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		fmt.Fprintf(hash, "%s:%d\n", table, count)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// declarationRows reports the stored declaration row count and the digest of their exact bytes.
func declarationRows(t *testing.T, path string) (int, string) {
	t.Helper()
	db := openRawStore(t, path)
	rows, err := db.Query(`SELECT id, record_json FROM supersession_declarations ORDER BY id`)
	if err != nil {
		t.Fatalf("scan declarations: %v", err)
	}
	defer rows.Close()
	hash := sha256.New()
	count := 0
	for rows.Next() {
		var id, data []byte
		if err := rows.Scan(&id, &data); err != nil {
			t.Fatalf("scan declaration row: %v", err)
		}
		hash.Write(id)
		hash.Write(data)
		count++
	}
	return count, hex.EncodeToString(hash.Sum(nil))
}

func storeSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// declarationState captures what a rejected write or a read must leave untouched.
func declarationState(t *testing.T, path string) (string, int, string) {
	t.Helper()
	count, digest := declarationRows(t, path)
	return canonicalDigest(t, path), count, digest
}

func TestSupersessionDeclarationPutGetAndExactRetry(t *testing.T) {
	run, _ := setup(t)
	const source = "declaration/put-get"
	sourceIdentity, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID := declarationPins(t, run, source)
	declaration := declarationJSON(t, sourceIdentity, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, "example.operations", "successor replaces the predecessor")
	declarationID, err := mousa.NewSupersessionDeclarationID(sourceIdentity.ID, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, "example.operations", "successor replaces the predecessor")
	if err != nil {
		t.Fatal(err)
	}

	beforeCanonical, beforeCount, _ := declarationState(t, run.store)
	if beforeCount != 0 {
		t.Fatalf("fresh store holds %d declarations, want 0", beforeCount)
	}

	// The first write is one process invocation; the declaration arrives on stdin.
	code, putOutput, stderr := run.runExitInput(string(declaration), "supersession", "declaration", "put")
	if code != 0 {
		t.Fatalf("put exit = %d, stderr = %s", code, stderr)
	}
	if putOutput != string(declaration) {
		t.Fatalf("put did not emit the canonical stored declaration:\n got %q\nwant %q", putOutput, declaration)
	}
	// The write appends one declaration and leaves every canonical record row untouched: no source
	// item or revision is created, retargeted or repaired.
	afterPutCanonical, count, digest := declarationState(t, run.store)
	if count != 1 {
		t.Fatalf("stored declarations = %d, want 1", count)
	}
	if afterPutCanonical != beforeCanonical {
		t.Fatalf("put changed canonical rows: %s, want %s", afterPutCanonical, beforeCanonical)
	}

	// A separate invocation reads the stored declaration back and emits the same bytes.
	code, getOutput, stderr := run.runExit("supersession", "declaration", "get", declarationID.String())
	if code != 0 {
		t.Fatalf("get exit = %d, stderr = %s", code, stderr)
	}
	if getOutput != putOutput {
		t.Fatalf("get output differs from put output:\n got %q\nwant %q", getOutput, putOutput)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(getOutput), &decoded); err != nil {
		t.Fatalf("get emitted unreadable JSON: %v", err)
	}
	if decoded["schema"] != mousa.SupersessionDeclarationSchema || decoded["id"] != declarationID.String() ||
		decoded["source_id"] != sourceIdentity.ID.String() || decoded["author"] != "example.operations" ||
		decoded["basis"] != "successor replaces the predecessor" {
		t.Fatalf("get emitted an unexpected declaration: %v", decoded)
	}

	// A read never migrates or rewrites: canonical rows, declaration rows and the store file are
	// unchanged.
	canonicalAfterRead, countAfterRead, digestAfterRead := declarationState(t, run.store)
	if canonicalAfterRead != afterPutCanonical || countAfterRead != count || digestAfterRead != digest {
		t.Fatal("read changed the store")
	}
	sizeAfterRead := storeSize(t, run.store)

	// An exact retry in a fresh process succeeds without adding a row.
	code, retryOutput, stderr := run.runExitInput(string(declaration), "supersession", "declaration", "put")
	if code != 0 {
		t.Fatalf("retry exit = %d, stderr = %s", code, stderr)
	}
	if retryOutput != putOutput {
		t.Fatalf("retry output differs:\n got %q\nwant %q", retryOutput, putOutput)
	}
	canonicalAfterRetry, retryCount, retryDigest := declarationState(t, run.store)
	if canonicalAfterRetry != canonicalAfterRead || retryCount != 1 || retryDigest != digestAfterRead {
		t.Fatalf("exact retry changed the store: canonical=%s rows=%d", canonicalAfterRetry, retryCount)
	}
	if sizeAfterRetry := storeSize(t, run.store); sizeAfterRetry != sizeAfterRead {
		t.Fatalf("exact retry changed the store size: %d, want %d", sizeAfterRetry, sizeAfterRead)
	}
}

func TestSupersessionDeclarationPutRejectsMalformedInput(t *testing.T) {
	run, _ := setup(t)
	const source = "declaration/malformed"
	sourceIdentity, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID := declarationPins(t, run, source)
	canonical := declarationJSON(t, sourceIdentity, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, "example.operations", "baseline basis")
	canonicalText := string(canonical)
	cases := []struct {
		name  string
		input string
	}{
		{"empty input", ""},
		{"malformed JSON", `{"schema": "mousa.supersession_declaration.v1"`},
		{"not an object", `"mousa.supersession_declaration.v1"`},
		{"duplicate field", strings.Replace(canonicalText, `{"schema"`, `{"basis":"duplicate","schema"`, 1)},
		{"unknown field", strings.Replace(canonicalText, `{"schema"`, `{"extra_field":"x","schema"`, 1)},
		{"trailing value", canonicalText + `{"schema":"mousa.supersession_declaration.v1"}`},
		{"identity disagrees with content", strings.Replace(canonicalText, `"basis":"baseline basis"`, `"basis":"tampered basis"`, 1)},
		{"oversized input", strings.Repeat("x", mousa.MaxSupersessionDeclarationBytes+1)},
	}
	beforeCanonical, beforeCount, beforeDigest := declarationState(t, run.store)
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, stdout, stderr := run.runExitInput(testCase.input, "supersession", "declaration", "put")
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr = %s", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("rejected input emitted output: %q", stdout)
			}
			if !strings.HasPrefix(stderr, "mousa: ") {
				t.Fatalf("stderr does not follow the CLI convention: %q", stderr)
			}
		})
	}
	canonicalAfter, count, digest := declarationState(t, run.store)
	if canonicalAfter != beforeCanonical || count != beforeCount || digest != beforeDigest {
		t.Fatalf("rejected input changed the store: canonical=%s rows=%d", canonicalAfter, count)
	}
}

func TestSupersessionDeclarationPutRejectsUnpinnedTargets(t *testing.T) {
	run, _ := setup(t)
	const source = "declaration/unpinned"
	sourceIdentity, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID := declarationPins(t, run, source)

	absentRepresentation := predecessorRepresentationID
	absentRepresentation[len(absentRepresentation)-1] ^= 0x01
	absentSource, err := mousa.NewSourceID("mousa-jsonl", "declaration/never-synced")
	if err != nil {
		t.Fatal(err)
	}
	foreignSource, foreignPredecessorItemID, foreignPredecessorRepresentationID, _, _ := declarationPins(t, run, "declaration/foreign")

	cases := []struct {
		name string
		data []byte
		code string
	}{
		{
			"nonexistent revision pin",
			declarationJSON(t, sourceIdentity, predecessorItemID, absentRepresentation, successorItemID, successorRepresentationID, "example.operations", "absent revision"),
			"not_found",
		},
		{
			"declared source does not exist",
			declarationJSON(t, mousa.Source{ID: absentSource}, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, "example.operations", "absent source"),
			"not_found",
		},
		{
			"revision belongs to another source",
			declarationJSON(t, sourceIdentity, foreignPredecessorItemID, foreignPredecessorRepresentationID, successorItemID, successorRepresentationID, "example.operations", "foreign revision"),
			"integrity",
		},
	}
	if foreignSource.ID == sourceIdentity.ID {
		t.Fatal("fixture sources must differ")
	}
	beforeCanonical, beforeCount, beforeDigest := declarationState(t, run.store)
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, stdout, stderr := run.runExitInput(string(testCase.data), "supersession", "declaration", "put")
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr = %s", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("rejected declaration emitted output: %q", stdout)
			}
			if !strings.Contains(stderr, "sqlite "+testCase.code) {
				t.Fatalf("stderr = %q, want the %s classification", stderr, testCase.code)
			}
		})
	}
	canonicalAfter, count, digest := declarationState(t, run.store)
	if canonicalAfter != beforeCanonical || count != beforeCount || digest != beforeDigest {
		t.Fatalf("rejected declarations changed the store: canonical=%s rows=%d", canonicalAfter, count)
	}
}

func TestSupersessionDeclarationGetExitsForMissingInvalidAndAbsentStore(t *testing.T) {
	run, _ := setup(t)
	const source = "declaration/get-errors"
	sourceIdentity, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID := declarationPins(t, run, source)
	declaration := declarationJSON(t, sourceIdentity, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, "example.operations", "missing record")
	declarationID, err := mousa.NewSupersessionDeclarationID(sourceIdentity.ID, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, "example.operations", "missing record")
	if err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := run.runExitInput(string(declaration), "supersession", "declaration", "put"); code != 0 {
		t.Fatalf("put exit = %d, stderr = %s", code, stderr)
	} else if stdout == "" {
		t.Fatal("put emitted nothing")
	}

	// An unknown but well-formed identity is a missing record, not an invalid invocation.
	beforeCanonical, beforeCount, beforeDigest := declarationState(t, run.store)
	unknown := declarationID
	unknown[0] ^= 0x01
	code, stdout, stderr := run.runExit("supersession", "declaration", "get", unknown.String())
	if code != 1 || !strings.Contains(stderr, "sqlite not_found") {
		t.Fatalf("missing declaration: exit = %d, stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("missing declaration emitted output: %q", stdout)
	}
	canonicalAfter, count, digest := declarationState(t, run.store)
	if canonicalAfter != beforeCanonical || count != beforeCount || digest != beforeDigest {
		t.Fatal("a missing-record read changed the store")
	}

	// Invalid identities and arity are invalid invocations.
	for _, testCase := range []struct {
		name string
		args []string
	}{
		{"short identity", []string{"supersession", "declaration", "get", "abcd"}},
		{"uppercase identity", []string{"supersession", "declaration", "get", strings.ToUpper(declarationID.String())}},
		{"no identity", []string{"supersession", "declaration", "get"}},
		{"two identities", []string{"supersession", "declaration", "get", declarationID.String(), declarationID.String()}},
		{"unknown supersession subcommand", []string{"supersession", "activation-state", "get"}},
		{"unknown declaration subcommand", []string{"supersession", "declaration", "list"}},
		{"put with an argument", []string{"supersession", "declaration", "put", declarationID.String()}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			code, stdout, stderr := run.runExit(testCase.args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stderr = %s", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("invalid invocation emitted output: %q", stdout)
			}
			if !strings.HasPrefix(stderr, "mousa: ") || !strings.Contains(stderr, "usage: mousa") {
				t.Fatalf("stderr does not report usage: %q", stderr)
			}
		})
	}

	// A read never creates a store: an absent path stays absent.
	absentPath := filepath.Join(t.TempDir(), "absent.sqlite")
	empty := testRun{t: t, binary: run.binary, store: absentPath}
	code, stdout, stderr = empty.runExit("supersession", "declaration", "get", declarationID.String())
	if code != 1 {
		t.Fatalf("absent store exit = %d, want 1; stderr = %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("absent store emitted output: %q", stdout)
	}
	if _, err := os.Stat(absentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created the store: %v", err)
	}
}

func TestSupersessionDeclarationGetRejectsCorruptRecordWithoutRepair(t *testing.T) {
	run, _ := setup(t)
	const source = "declaration/corrupt"
	sourceIdentity, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID := declarationPins(t, run, source)
	declaration := declarationJSON(t, sourceIdentity, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, "example.operations", "corrupt record")
	declarationID, err := mousa.NewSupersessionDeclarationID(sourceIdentity.ID, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, "example.operations", "corrupt record")
	if err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := run.runExitInput(string(declaration), "supersession", "declaration", "put"); code != 0 {
		t.Fatalf("put exit = %d, stderr = %s", code, stderr)
	}
	// Damage the stored record bytes directly; a rejected read must never repair them.
	rawExec(t, run.store, `UPDATE supersession_declarations SET record_json = x'6e6f742d6a736f6e'`)
	beforeCanonical, beforeCount, beforeDigest := declarationState(t, run.store)
	code, stdout, stderr := run.runExit("supersession", "declaration", "get", declarationID.String())
	if code != 1 {
		t.Fatalf("corrupt record exit = %d, want 1; stderr = %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("corrupt record emitted output: %q", stdout)
	}
	if !strings.Contains(stderr, "integrity") {
		t.Fatalf("stderr = %q, want an integrity failure", stderr)
	}
	canonicalAfter, count, digest := declarationState(t, run.store)
	if canonicalAfter != beforeCanonical || count != beforeCount || digest != beforeDigest {
		t.Fatal("a rejected read repaired or changed the damaged record")
	}
}

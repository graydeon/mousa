package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// Paths are verified through the public surface: query passages carry each
// item's current location, `mousa move` and the served POST /v1/moves log one
// path_move leaf, and `mousa verify` replays the chain.

// pathOfQuery runs one query and returns the first passage's path and
// representation id, or empty strings when nothing matched.
func pathOfQuery(t *testing.T, run testRun, source, text string) (path, representation string) {
	t.Helper()
	_, result := run.run(false, "query", "--full", "--source", source, text)
	passages, _ := result["passages"].([]any)
	if len(passages) == 0 {
		return "", ""
	}
	first, _ := passages[0].(map[string]any)
	path, _ = first["path"].(string)
	representation, _ = first["representation_id"].(string)
	return path, representation
}

func logLeafCount(t *testing.T, store, kind string) int {
	t.Helper()
	db := openRawStore(t, store)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM log WHERE kind = ?`, kind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func noteIDOf(t *testing.T, emitted map[string]any) string {
	t.Helper()
	id, _ := emitted["id"].(string)
	if id == "" {
		t.Fatalf("note emission has no id: %v", emitted)
	}
	return id
}

func creationPathCount(t *testing.T, store, path string) int {
	t.Helper()
	db := openRawStore(t, store)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM item_changes c
		JOIN log l ON l.kind = 'item_change' AND l.record_id = c.id
		JOIN notes n ON n.source_id = c.source_id AND n.item_id = json_extract(CAST(c.record_json AS TEXT), '$.item_id')
		WHERE json_extract(CAST(c.record_json AS TEXT), '$.schema') = 'mousa.item_change.v3'
		AND json_extract(CAST(c.record_json AS TEXT), '$.creation_path') = ?`, path).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPathRoundTrip(t *testing.T) {
	run, _ := setup(t)
	run.run(false, "access", "--source", "docs", "--create", "allow")
	run.run(false, "access", "--source", "notes", "--create", "allow")
	run.runInput(`{"id":"docs/one","text":"alpha beta gamma"}`+"\n", false, "sync", "--source", "docs", "--allow")

	// A bare note lands at the default "unsorted"; an explicit path wins.
	if path, _ := pathOfQuery(t, run, "docs", "alpha"); path != "docs/one" {
		t.Fatalf("default document path = %q, want docs/one", path)
	}
	run.run(false, "remember", "--source", "notes", "bare fixture note alpha")
	run.run(false, "remember", "--source", "notes", "--path", "work/notes", "placed fixture note alpha")
	if path, _ := pathOfQuery(t, run, "notes", "bare fixture note"); path != "unsorted" {
		t.Fatalf("bare note path = %q, want unsorted", path)
	}
	if path, _ := pathOfQuery(t, run, "notes", "placed fixture note"); path != "work/notes" {
		t.Fatalf("placed note path = %q, want work/notes", path)
	}
	if got := creationPathCount(t, run.store, "unsorted"); got != 1 {
		t.Fatalf("unsorted note creation records = %d, want 1", got)
	}
	if got := creationPathCount(t, run.store, "work/notes"); got != 1 {
		t.Fatalf("placed note creation records = %d, want 1", got)
	}

	// A JSONL record path locates a new item; replay without it keeps it.
	run.runInput(`{"id":"docs/two","text":"delta epsilon","path":"shelf/two"}`+"\n", false, "sync", "--source", "docs")
	if path, _ := pathOfQuery(t, run, "docs", "delta epsilon"); path != "shelf/two" {
		t.Fatalf("record path = %q, want shelf/two", path)
	}
	run.runInput(`{"id":"docs/two","text":"delta epsilon"}`+"\n", false, "sync", "--source", "docs")
	if path, _ := pathOfQuery(t, run, "docs", "delta epsilon"); path != "shelf/two" {
		t.Fatalf("replay changed path to %q, want shelf/two", path)
	}

	// Unchanged content with a different explicit path moves the item
	// without a new representation.
	_, beforeRepr := pathOfQuery(t, run, "docs", "delta epsilon")
	run.runInput(`{"id":"docs/two","text":"delta epsilon","path":"shelf/moved"}`+"\n", false, "sync", "--source", "docs")
	second, reprAfter := pathOfQuery(t, run, "docs", "delta epsilon")
	if second != "shelf/moved" {
		t.Fatalf("moved path = %q, want shelf/moved", second)
	}
	if beforeRepr == "" || reprAfter != beforeRepr {
		t.Fatalf("path-only move changed the representation: %q -> %q", beforeRepr, reprAfter)
	}
	run.run(false, "verify")

	// A resync carrying new content and a new path updates the revision and
	// logs one more move.
	movesBefore := logLeafCount(t, run.store, "path_move")
	run.runInput(`{"id":"docs/two","text":"another delta","path":"shelf/changed"}`+"\n", false, "sync", "--source", "docs")
	third, repr3 := pathOfQuery(t, run, "docs", "another delta")
	if third != "shelf/changed" {
		t.Fatalf("resynced moved item path = %q, want shelf/changed", third)
	}
	if repr3 == "" || repr3 == beforeRepr {
		t.Fatalf("resynced moved item kept representation %q", repr3)
	}
	if got := logLeafCount(t, run.store, "path_move"); got != movesBefore+1 {
		t.Fatalf("content+path resync logged %d moves, want one", got-movesBefore)
	}
	run.run(false, "verify")

	// Invalid explicit paths are rejected by every entry point; an
	// explicitly empty --path is invalid, not a default.
	for _, bad := range []string{"Work/x", "work//x", ".", "trailing/", strings.Repeat("a", 257), ""} {
		stderr, _ := run.runInput(`{"id":"docs/bad","text":"bad","path":"`+bad+`"}`+"\n", true, "sync", "--source", "docs")
		if !strings.Contains(stderr, "path") {
			t.Fatalf("sync accepted path %q", bad)
		}
		stderr, _ = run.run(true, "remember", "--source", "notes", "--path", bad, "bad path note")
		if !strings.Contains(stderr, "path") {
			t.Fatalf("remember accepted path %q", bad)
		}
	}
	stderr, _ := run.run(true, "remember", "--source", "notes", "--path", "", "bare fixture note alpha")
	if !strings.Contains(stderr, "path") {
		t.Fatalf("repeat remember accepted empty --path: %s", stderr)
	}

	// An offline move takes a directory root as its source.
	dir := t.TempDir()
	writeFile(t, dir, "note.md", "Directory note about anchors.\n")
	run.run(false, "sync", "--allow", dir)
	_, dirMoved := run.run(false, "move", "--item", "note.md", "--to", "catalog/note", dir)
	if lenOf(dirMoved, "moved") != 1 {
		t.Fatalf("directory move = %v", dirMoved)
	}
	_, dirQuery := run.run(false, "query", "--full", dir, "anchors")
	dirPassages, _ := dirQuery["passages"].([]any)
	if len(dirPassages) == 0 || dirPassages[0].(map[string]any)["path"] != "catalog/note" {
		t.Fatalf("directory query = %v", dirQuery)
	}
}

func TestMoveAudit(t *testing.T) {
	run, _ := setup(t)
	run.run(false, "access", "--source", "docs", "--create", "allow")
	run.run(false, "access", "--source", "notes", "--create", "allow")
	run.runInput(
		`{"id":"docs/one","text":"alpha document"}`+"\n"+
			`{"id":"team","text":"team root"}`+"\n"+
			`{"id":"x1","text":"team alpha entry","path":"team/a"}`+"\n"+
			`{"id":"x2","text":"team beta entry","path":"team/b"}`+"\n", false,
		"sync", "--source", "docs", "--allow")
	_, trailHolder := run.run(false, "query", "--full", "--source", "docs", "alpha")
	trailID, _ := trailHolder["trail"].(map[string]any)
	trail, _ := trailID["id"].(string)
	_, note := run.run(false, "remember", "--source", "notes", "--from", trail, "movable cited note")
	noteID := noteIDOf(t, note)

	// Offline moves: note by ID, item by ID, prefix with slash boundaries.
	_, moved := run.run(false, "move", "--source", "notes", "--note", noteID, "--to", "work/n")
	if lenOf(moved, "moved") != 1 {
		t.Fatalf("note move = %v", moved)
	}
	_, moved = run.run(false, "move", "--source", "docs", "--item", "docs/one", "--to", "shelf/one")
	if lenOf(moved, "moved") != 1 {
		t.Fatalf("item move = %v", moved)
	}
	before := logLeafCount(t, run.store, "path_move")
	_, moved = run.run(false, "move", "--source", "docs", "--prefix", "team/", "--to", "shelf/")
	entries, _ := moved["moved"].([]any)
	if len(entries) != 2 {
		t.Fatalf("prefix move = %v, want the two team/ items only", entries)
	}
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		if entry["new_path"] != "shelf/"+strings.TrimPrefix(entry["old_path"].(string), "team/") {
			t.Fatalf("bad rewrite: %v", entry)
		}
	}
	// One operation is one leaf covering every item, and the bare "team"
	// item was untouched.
	if got := logLeafCount(t, run.store, "path_move"); got != before+1 {
		t.Fatalf("prefix move wrote %d leaves, want one", got-before)
	}
	if path, _ := pathOfQuery(t, run, "docs", "team root"); path != "team" {
		t.Fatalf("bare team path = %q, want team", path)
	}
	if path, _ := pathOfQuery(t, run, "docs", "team alpha"); path != "shelf/a" {
		t.Fatalf("moved prefix item path = %q, want shelf/a", path)
	}

	// A same-path move is a no-op with no leaf.
	_, moved = run.run(false, "move", "--source", "docs", "--item", "docs/one", "--to", "shelf/one")
	if lenOf(moved, "moved") != 0 || logLeafCount(t, run.store, "path_move") != before+1 {
		t.Fatalf("same-path move = %v", moved)
	}

	// Prior trails still resolve and recall sees the current path.
	run.run(false, "trail", trail)
	if path, _ := pathOfQuery(t, run, "docs", "alpha document"); path != "shelf/one" {
		t.Fatalf("recalled path = %q, want shelf/one", path)
	}
	if path, _ := pathOfQuery(t, run, "notes", "movable cited note"); path != "work/n" {
		t.Fatalf("moved note path = %q, want work/n", path)
	}
	run.run(false, "verify")

	// A denied or withdrawn destination refuses the move but stays verifiable.
	run.run(false, "access", "--source", "docs", "deny")
	stderr, _ := run.run(true, "move", "--source", "docs", "--item", "x1", "--to", "elsewhere/a")
	if !strings.Contains(stderr, "denied") && !strings.Contains(stderr, "source") {
		t.Fatalf("denied-source move stderr = %q", stderr)
	}
	run.run(false, "verify")

	// Tampering with the move leaf or the projection is refused by verify.
	// The store runs WAL, so checkpoint before copying the file.
	rawExec(t, run.store, `PRAGMA wal_checkpoint(TRUNCATE)`)
	for _, statement := range []string{
		`UPDATE path_moves SET record_json = x'00' WHERE rowid = (SELECT max(rowid) FROM path_moves)`,
		`UPDATE local_items SET path = 'bogus/path' WHERE item_id = 'x1'`,
	} {
		scratch := filepath.Join(t.TempDir(), "scratch.sqlite")
		data, err := os.ReadFile(run.store)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(scratch, data, 0o600); err != nil {
			t.Fatal(err)
		}
		rawExec(t, scratch, statement)
		scratchRun := testRun{t: t, binary: cliBinary, store: scratch}
		if stderr, _ := scratchRun.run(true, "verify"); !strings.Contains(stderr, "integrity") {
			t.Fatalf("verify accepted tamper %q: %s", statement, stderr)
		}
	}
}

// testdata/schema7_fixture.sqlite.gz holds a schema-7 store written by the
// pre-migration binary: source fixture-docs with items docs/one and ".env",
// source fixture-notes with a standalone and a cited note, one offline trail,
// and one served request pair. Regenerate with the seed commands recorded in
// reports/task23-paths-feedback.md; the stored bytes below are the fixture.
func schema7Fixture(t *testing.T) string {
	t.Helper()
	compressed, err := os.Open(filepath.Join("testdata", "schema7_fixture.sqlite.gz"))
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
	path := filepath.Join(t.TempDir(), "schema7.sqlite")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMigrateSchema7(t *testing.T) {
	fixture := schema7Fixture(t)
	run := testRun{t: t, binary: cliBinary, store: fixture}

	// The durable history this migration must preserve byte for byte.
	tables := []string{"log", "notes", "note_citations", "trails", "request_admissions", "request_outcomes"}
	before := make(map[string]struct {
		count  int
		digest string
	})
	for _, table := range tables {
		count, digest := tableDigest(t, fixture, table)
		before[table] = struct {
			count  int
			digest string
		}{count, digest}
	}

	// A schema-7 store refuses every normal command until migrated.
	stderr, _ := run.run(true, "verify")
	if !strings.Contains(stderr, "incompatible_schema") || !strings.Contains(stderr, "mousa migrate") {
		t.Fatalf("verify on schema 7 stderr = %q", stderr)
	}
	_, migrated := run.run(false, "migrate")
	if migrated["schema"] != float64(10) {
		t.Fatalf("migrate = %v, want schema 10", migrated)
	}
	run.run(false, "verify")

	for _, table := range tables {
		count, digest := tableDigest(t, fixture, table)
		want := before[table]
		if count != want.count || digest != want.digest {
			t.Fatalf("%s changed across migration: %d rows %s -> %d rows %s", table, want.count, want.digest, count, digest)
		}
	}

	// Exported notes carry their new path and are otherwise byte-identical
	// to the schema-7 binary's own export recorded in testdata.
	baseline, err := os.ReadFile(filepath.Join("testdata", "schema7_notes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := run.runExit("notes", "export", "--source", "fixture-notes")
	if code != 0 {
		t.Fatalf("notes export failed: %s", out)
	}
	if !strings.Contains(out, `"path":"unsorted"`) {
		t.Fatalf("exported notes lack path: %s", out)
	}
	beforeLines := strings.Split(strings.TrimSpace(string(baseline)), "\n")
	afterLines := strings.Split(strings.TrimSpace(out), "\n")
	if len(afterLines) != len(beforeLines) {
		t.Fatalf("migrated export has %d notes, fixture export has %d", len(afterLines), len(beforeLines))
	}
	for i := range beforeLines {
		var want, got map[string]any
		if err := json.Unmarshal([]byte(beforeLines[i]), &want); err != nil {
			t.Fatalf("fixture export line %d is not JSON: %v", i+1, err)
		}
		if err := json.Unmarshal([]byte(afterLines[i]), &got); err != nil {
			t.Fatalf("migrated export line %d is not JSON: %v", i+1, err)
		}
		if got["path"] != "unsorted" {
			t.Fatalf("migrated note %d path = %v, want unsorted", i+1, got["path"])
		}
		delete(got, "path")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("note %d changed across migration: %v want %v", i+1, got, want)
		}
	}
	db := openRawStore(t, fixture)
	paths := map[string]string{}
	rows, err := db.Query(`SELECT i.item_id, i.path FROM local_items i JOIN sources s ON s.id = i.source_id
		WHERE json_extract(CAST(s.record_json AS TEXT), '$.external_source_id') = 'fixture-docs'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			t.Fatal(err)
		}
		paths[id] = path
	}
	rows.Close()
	if paths["docs/one"] != "docs/one" || paths[".env"] != ".env" {
		t.Fatalf("migrated document paths = %v", paths)
	}
	// A legacy document ID that is not a valid path stays movable.
	_, moved := run.run(false, "move", "--source", "fixture-docs", "--item", ".env", "--to", "legacy/env")
	if lenOf(moved, "moved") != 1 {
		t.Fatalf("legacy item move = %v", moved)
	}
	run.run(false, "verify")

	// A store missing one log index refuses to migrate and is left whole:
	// no path column, still version 7.
	broken := schema7Fixture(t)
	rawExec(t, broken, `DROP INDEX log_source_seq_idx`)
	brokenRun := testRun{t: t, binary: cliBinary, store: broken}
	if stderr, _ := brokenRun.run(true, "migrate"); stderr == "" {
		t.Fatal("broken migrate unexpectedly succeeded")
	}
	brokenDB := openRawStore(t, broken)
	var version int
	if err := brokenDB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	var brokenHasPath bool
	if err := brokenDB.QueryRow(`SELECT EXISTS(SELECT 1 FROM pragma_table_info('local_items') WHERE name = 'path')`).Scan(&brokenHasPath); err != nil {
		t.Fatal(err)
	}
	if version != 7 || brokenHasPath {
		t.Fatalf("failed migration left partial state: version=%d hasPath=%v", version, brokenHasPath)
	}
	// A current store is a no-op success.
	fresh, _ := setup(t)
	fresh.run(false, "access", "--source", "docs", "--create", "allow")
	_, again := fresh.run(false, "migrate")
	if again["schema"] != float64(10) {
		t.Fatalf("current-store migrate = %v", again)
	}
}

// postJSON posts a native body and decodes the envelope.
func postJSON(t *testing.T, url, token string, body []byte) (int, map[string]any) {
	t.Helper()
	status, _, data := postNative(t, url, token, body)
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("reply is not JSON: %v\n%s", err, data)
	}
	return status, envelope
}

// getDetail reads one request's monitor detail, with an optional credential.
func getDetail(t *testing.T, url, token string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func envelopeResultJSON(t *testing.T, envelope map[string]any) map[string]any {
	t.Helper()
	result, _ := envelope["result"].(map[string]any)
	return result
}

func TestServedPathsAndMoves(t *testing.T) {
	run, _ := setup(t)
	run.run(false, "access", "--source", "docs", "--create", "allow")
	run.run(false, "access", "--source", "notes", "--create", "allow")
	writerToken := "writer-token"
	writeOnlyToken := "write-only-token"
	deniedToken := "denied-token"
	config := fmt.Sprintf(`{"principals":[
		{"id":"writer","token_sha256":%q,"read_sources":["docs","notes"],"write_sources":["docs","notes"],"operator":true},
		{"id":"writeonly","token_sha256":%q,"read_sources":[],"write_sources":["docs","notes"]},
		{"id":"denied","token_sha256":%q,"read_sources":[],"write_sources":[]}
	]}`, tokenHash(writerToken), tokenHash(writeOnlyToken), tokenHash(deniedToken))
	configPath := filepath.Join(t.TempDir(), "serve-config.json")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := startDaemon(t, run.store, configPath)
	endpoint := daemon.url

	// Served CLI paths: remember --path and a sync record path.
	if code, _, stderr := runEndpoint(t, endpoint, writerToken, "", "remember", "--source", "notes", "--path", "work/n", "served placed note alpha"); code != 0 {
		t.Fatalf("served remember --path failed: %d %s", code, stderr)
	}
	status, envelope := postJSON(t, endpoint+"/v1/documents", writerToken,
		[]byte(`{"source":"docs","record":{"id":"docs/a","text":"served alpha document","path":"team/a"}}`))
	if status != http.StatusOK || envelopeResultJSON(t, envelope)["action"] != "added" {
		t.Fatalf("served document = %d %v", status, envelope)
	}

	// Item move, write-only principal move, denied principal refusal.
	status, envelope = postJSON(t, endpoint+"/v1/moves", writerToken,
		[]byte(`{"source":"docs","item":"docs/a","to":"shelf/a"}`))
	moved := envelopeResultJSON(t, envelope)
	if status != http.StatusOK || lenOf(moved, "moved") != 1 {
		t.Fatalf("writer move = %d %v", status, envelope)
	}
	moveID := envelope["request_id"].(string)
	status, envelope = postJSON(t, endpoint+"/v1/moves", writeOnlyToken,
		[]byte(`{"source":"docs","item":"docs/a","to":"shelf/b"}`))
	if status != http.StatusOK || lenOf(envelopeResultJSON(t, envelope), "moved") != 1 {
		t.Fatalf("write-only move = %d %v", status, envelope)
	}
	status, _ = postJSON(t, endpoint+"/v1/moves", deniedToken,
		[]byte(`{"source":"docs","item":"docs/a","to":"shelf/c"}`))
	if status != http.StatusForbidden {
		t.Fatalf("denied move status = %d, want 403", status)
	}

	// The served CLI runs the same move against the endpoint.
	code, movedOut, stderr := runEndpoint(t, endpoint, writerToken, "", "move", "--source", "docs", "--item", "docs/a", "--to", "shelf/cli")
	var cliMove struct {
		Moved []struct {
			NewPath string `json:"new_path"`
		} `json:"moved"`
	}
	if code != 0 || json.Unmarshal([]byte(movedOut), &cliMove) != nil ||
		len(cliMove.Moved) != 1 || cliMove.Moved[0].NewPath != "shelf/cli" {
		t.Fatalf("served CLI move = %d %s %s", code, movedOut, stderr)
	}

	// A native resync carrying only a new path reports moved and keeps the
	// representation; one carrying text and a path reports updated with a
	// new representation.
	status, envelope = postJSON(t, endpoint+"/v1/documents", writerToken,
		[]byte(`{"source":"docs","record":{"id":"docs/a","text":"served alpha document","path":"shelf/resync"}}`))
	resync := envelopeResultJSON(t, envelope)
	if status != http.StatusOK || resync["action"] != "moved" {
		t.Fatalf("path-only resync = %d %v", status, envelope)
	}
	reprBefore := resync["representation_id"]
	if reprBefore == nil {
		t.Fatalf("moved resync lost its representation: %v", envelope)
	}
	status, envelope = postJSON(t, endpoint+"/v1/documents", writerToken,
		[]byte(`{"source":"docs","record":{"id":"docs/a","text":"served alpha document revised","path":"shelf/v2"}}`))
	updated := envelopeResultJSON(t, envelope)
	if status != http.StatusOK || updated["action"] != "updated" ||
		updated["representation_id"] == nil || updated["representation_id"] == reprBefore {
		t.Fatalf("text+path resync = %d %v", status, envelope)
	}
	status, envelope = postJSON(t, endpoint+"/v1/query", writerToken,
		[]byte(`{"scope":{"source":"docs"},"text":"served alpha document revised"}`))
	resyncPassages, _ := envelopeResultJSON(t, envelope)["passages"].([]any)
	if len(resyncPassages) == 0 || resyncPassages[0].(map[string]any)["path"] != "shelf/v2" {
		t.Fatalf("text+path resync path = %v", envelope)
	}

	// An invalid note path is a 400 on a fresh note and on a repeat.
	for i, raw := range []string{`"Bad"`, `""`} {
		status, _ = postJSON(t, endpoint+"/v1/notes", writerToken,
			[]byte(fmt.Sprintf(`{"scope":{"source":"notes"},"text":"bad path note %d","path":%s}`, i, raw)))
		if status != http.StatusBadRequest {
			t.Fatalf("fresh note path %s status = %d, want 400", raw, status)
		}
		status, _ = postJSON(t, endpoint+"/v1/notes", writerToken,
			[]byte(`{"scope":{"source":"notes"},"text":"served placed note alpha","path":`+raw+`}`))
		if status != http.StatusBadRequest {
			t.Fatalf("repeat note path %s status = %d, want 400", raw, status)
		}
	}

	// Queries show the current path for items and notes.
	status, envelope = postJSON(t, endpoint+"/v1/query", writerToken,
		[]byte(`{"scope":{"source":"docs"},"text":"served alpha document"}`))
	passages, _ := envelopeResultJSON(t, envelope)["passages"].([]any)
	if len(passages) == 0 {
		t.Fatalf("query = %v", envelope)
	}
	if path, _ := passages[0].(map[string]any)["path"].(string); path != "shelf/v2" {
		t.Fatalf("served query path = %q, want shelf/v2", path)
	}
	status, envelope = postJSON(t, endpoint+"/v1/query", writerToken,
		[]byte(`{"scope":{"source":"notes"},"text":"served placed note"}`))
	passages, _ = envelopeResultJSON(t, envelope)["passages"].([]any)
	if len(passages) == 0 || passages[0].(map[string]any)["path"] != "work/n" {
		t.Fatalf("note query = %v", envelope)
	}

	// A repeated move admits but commits no path_move: no_op.
	status, envelope = postJSON(t, endpoint+"/v1/moves", writerToken,
		[]byte(`{"source":"docs","item":"docs/a","to":"shelf/v2"}`))
	if status != http.StatusOK || lenOf(envelopeResultJSON(t, envelope), "moved") != 0 {
		t.Fatalf("no-op move = %d %v", status, envelope)
	}
	noopID := envelope["request_id"].(string)
	status, body := getDetail(t, endpoint+"/v1/requests/"+noopID, writerToken)
	if status != http.StatusOK || !strings.Contains(string(body), `"state":"no_op"`) {
		t.Fatalf("no-op detail = %d %s", status, body)
	}

	// The committed move's detail carries the verified record identity, and
	// the closed monitor keeps old/new path text out of it.
	status, body = getDetail(t, endpoint+"/v1/requests/"+moveID, writerToken)
	if status != http.StatusOK || !strings.Contains(string(body), `"kind":"path_move"`) {
		t.Fatalf("move detail = %d %s", status, body)
	}
	if strings.Contains(string(body), `"moves"`) || strings.Contains(string(body), `"old_path"`) ||
		strings.Contains(string(body), `"content"`) {
		t.Fatalf("closed monitor committed move discloses paths: %s", body)
	}

	// An open monitor shows old/new path text inside content only. A serve
	// owns its store, so the closed daemon must stop before the open one.
	daemon.stop(t)
	open := startDaemonFlags(t, run.store, configPath, "--open-monitor")
	status, body = getDetail(t, open.url+"/v1/requests/"+moveID, "")
	if status != http.StatusOK {
		t.Fatalf("open monitor detail = %d %s", status, body)
	}
	if !strings.Contains(string(body), `"old_path":"team/a"`) || !strings.Contains(string(body), `"new_path":"shelf/a"`) {
		t.Fatalf("open monitor moves = %s", body)
	}
	open.stop(t)
	run.run(false, "verify")
}

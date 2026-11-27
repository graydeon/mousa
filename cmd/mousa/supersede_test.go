package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Supersession is verified through the public surface: a new durable note can
// declare the notes it replaces; default queries then omit the replaced
// notes, --include-superseded returns them marked, and the trail records the
// effect. Edges are immutable, replay-checked and preserved through export
// and import.

func passageNoteIDs(t *testing.T, result map[string]any) (ids []string, supersededBy map[string][]any) {
	t.Helper()
	passages, _ := result["passages"].([]any)
	supersededBy = map[string][]any{}
	for _, raw := range passages {
		passage, _ := raw.(map[string]any)
		note, _ := passage["note"].(map[string]any)
		if note == nil {
			continue
		}
		id, _ := note["id"].(string)
		ids = append(ids, id)
		if by, ok := note["superseded_by"].([]any); ok {
			supersededBy[id] = by
		}
	}
	return ids, supersededBy
}

func TestNoteSupersession(t *testing.T) {
	run, _ := setup(t)
	run.run(false, "access", "--source", "notes", "--create", "allow")
	run.run(false, "access", "--source", "other", "--create", "allow")
	_, orig := run.run(false, "remember", "--source", "notes", "dock original")
	oldA := noteIDOf(t, orig)
	_, second := run.run(false, "remember", "--source", "notes", "dock second")
	oldB := noteIDOf(t, second)
	_, beforeQuery := run.run(false, "query", "--full", "--source", "notes", "dock")
	preTrail, _ := beforeQuery["trail"].(map[string]any)["id"].(string)

	// A new note can supersede both old ones.
	_, revised := run.run(false, "remember", "--source", "notes",
		"--supersedes", oldA, "--supersedes", oldB, "dock revised")
	newID := noteIDOf(t, revised)

	// The default query returns only the replacement, and the stored trail
	// names each old note ID, its segment and the replacement.
	_, queried := run.run(false, "query", "--full", "--source", "notes", "dock")
	ids, marked := passageNoteIDs(t, queried)
	if !slices.Equal(ids, []string{newID}) {
		t.Fatalf("default query note ids = %v, want only %s", ids, newID)
	}
	if len(marked) != 0 {
		t.Fatalf("default query marked passages: %v", marked)
	}
	trail, _ := queried["trail"].(map[string]any)
	effects, _ := trail["supersession"].([]any)
	covered := map[string]bool{}
	for _, raw := range effects {
		effect, _ := raw.(map[string]any)
		old, _ := effect["note_id"].(string)
		by, _ := effect["superseded_by"].([]any)
		segment, _ := effect["segment_id"].(string)
		if segment == "" || len(by) != 1 || by[0] != newID {
			t.Fatalf("supersession effect = %v, want replacement %s", effect, newID)
		}
		covered[old] = true
	}
	if !covered[oldA] || !covered[oldB] || len(covered) != 2 {
		t.Fatalf("trail supersession covers %v, want both old notes", covered)
	}
	omitted := map[string]bool{}
	candidates, _ := trail["candidates"].([]any)
	for _, raw := range candidates {
		candidate, _ := raw.(map[string]any)
		omitted[candidate["segment_id"].(string)] = candidate["omitted"] == "superseded"
	}
	for _, raw := range effects {
		effect := raw.(map[string]any)
		if !omitted[effect["segment_id"].(string)] {
			t.Fatalf("superseded candidate not marked omitted: %v", effect)
		}
	}

	// --include-superseded returns the old notes, each marked, packed when
	// the budget admits them.
	_, included := run.run(false, "query", "--full", "--source", "notes", "--include-superseded", "dock")
	ids, marked = passageNoteIDs(t, included)
	if len(ids) != 3 {
		t.Fatalf("include-superseded note ids = %v, want all three", ids)
	}
	for _, old := range []string{oldA, oldB} {
		by := marked[old]
		if len(by) != 1 || by[0] != newID {
			t.Fatalf("included old note %s superseded_by = %v", old, marked[old])
		}
	}

	// The trail recorded before the write still resolves, before and after
	// another move of the replacement note.
	run.run(false, "trail", preTrail)
	run.run(false, "verify")
	_, moved := run.run(false, "move", "--source", "notes", "--note", newID, "--to", "work/n")
	if lenOf(moved, "moved") != 1 {
		t.Fatalf("replacement move = %v", moved)
	}
	run.run(false, "trail", preTrail)
	run.run(false, "verify")

	// A repeat with the same targets in reverse order returns the stored
	// note without writing.
	_, repeated := run.run(false, "remember", "--source", "notes",
		"--supersedes", oldB, "--supersedes", oldA, "dock revised")
	if noteIDOf(t, repeated) != newID {
		t.Fatalf("repeat supersede wrote a different note: %v", repeated)
	}

	// A repeat without --supersedes keeps the stored edges too; only an
	// explicitly different set conflicts.
	_, noFlags := run.run(false, "remember", "--source", "notes", "dock revised")
	if noteIDOf(t, noFlags) != newID {
		t.Fatalf("no-flag replay wrote a different note: %v", noFlags)
	}
	stderr, _ := run.run(true, "remember", "--source", "notes",
		"--supersedes", oldA, "dock revised")
	if !strings.Contains(stderr, "different") {
		t.Fatalf("differing supersedes replay error = %q, want a conflict", stderr)
	}

	// Immutable export bytes: the old notes' records are unchanged by the
	// edges that now point at them.
	code, exported, _ := run.runExit("notes", "export", "--source", "notes")
	if code != 0 {
		t.Fatalf("notes export failed: %s", exported)
	}
	lines := strings.Split(strings.TrimSpace(exported), "\n")
	if len(lines) != 3 {
		t.Fatalf("export = %d notes, want 3", len(lines))
	}

	// Failed edges never become note writes.
	before := logLeafCount(t, run.store, "note")
	for label, args := range map[string][]string{
		// Replaying an old note with a new edge attempts a cycle.
		"cycle":        {"remember", "--source", "notes", "--supersedes", newID, "dock original"},
		"cross-source": {"remember", "--source", "other", "--supersedes", oldA, "cross source supersede"},
		"missing":      {"remember", "--source", "notes", "--supersedes", strings.Repeat("1", 64), "supersede missing note"},
		"self":         {"remember", "--source", "notes", "--supersedes", oldB, "dock second"},
		"zero":         {"remember", "--source", "notes", "--supersedes", strings.Repeat("0", 64), "supersede zero note"},
		"repeated":     {"remember", "--source", "notes", "--supersedes", oldA, "--supersedes", oldA, "supersede repeated note"},
	} {
		stderr, _ := run.run(true, args...)
		if stderr == "" {
			t.Fatalf("%s supersede produced no error output", label)
		}
	}
	if got := logLeafCount(t, run.store, "note"); got != before {
		t.Fatalf("failed supersedes wrote %d note leaves", got-before)
	}

	// Import into a fresh store preserves the relationships even when the
	// replacement line precedes its targets in the file.
	fresh, _ := setup(t)
	fresh.run(false, "access", "--source", "notes", "--create", "allow")
	slices.Reverse(lines)
	// A verbatim repeat line is an identical retry, not a conflict.
	lines = append(lines, lines[0])
	code, out, stderr := fresh.runExitInput(strings.Join(lines, "\n")+"\n", "notes", "import", "--source", "notes")
	if code != 0 {
		t.Fatalf("reversed import failed: %d %s %s", code, out, stderr)
	}
	var imported map[string]any
	if err := json.Unmarshal([]byte(out), &imported); err != nil || imported["imported"] != float64(3) {
		t.Fatalf("import with repeat line = %v, want 3 notes", out)
	}
	_, freshQuery := fresh.run(false, "query", "--full", "--source", "notes", "dock")
	ids, _ = passageNoteIDs(t, freshQuery)
	if !slices.Equal(ids, []string{newID}) {
		t.Fatalf("imported query note ids = %v, want only %s", ids, newID)
	}
	_, freshInclude := fresh.run(false, "query", "--full", "--source", "notes", "--include-superseded", "dock")
	ids, marked = passageNoteIDs(t, freshInclude)
	if len(ids) != 3 || len(marked[oldA]) != 1 || len(marked[oldB]) != 1 {
		t.Fatalf("imported include query = %v marked %v", ids, marked)
	}
	fresh.run(false, "verify")
}

// A forged replacement leaf must fail the query's canonical check: the
// durable record, not the lookup projection, vouches for every effect.
func TestNoteSupersessionLeafTamper(t *testing.T) {
	run, _ := setup(t)
	run.run(false, "access", "--source", "notes", "--create", "allow")
	_, orig := run.run(false, "remember", "--source", "notes", "dock original")
	old := noteIDOf(t, orig)
	_, revised := run.run(false, "remember", "--source", "notes",
		"--supersedes", old, "delta revised")
	replacement, err := hex.DecodeString(noteIDOf(t, revised))
	if err != nil {
		t.Fatal(err)
	}
	// The replacement neither matches the query nor heads the source, so the
	// corruption is reachable only through the supersession-effect check.
	run.run(false, "remember", "--source", "notes", "other latest")
	rawExec(t, run.store, fmt.Sprintf(`UPDATE log SET leaf_hash = x'%s' WHERE kind = 'note' AND record_id = x'%x'`,
		hex.EncodeToString(bytes.Repeat([]byte{0x55}, 32)), replacement))
	stderr, _ := run.run(true, "query", "--source", "notes", "dock")
	if stderr == "" {
		t.Fatal("query accepted a tampered replacement leaf")
	}
}

func TestServedNoteSupersession(t *testing.T) {
	run, _ := setup(t)
	run.run(false, "access", "--source", "notes", "--create", "allow")
	writerToken := "sup-writer-token"
	deniedToken := "sup-denied-token"
	config := fmt.Sprintf(`{"principals":[
		{"id":"writer","token_sha256":%q,"read_sources":["notes"],"write_sources":["notes"],"operator":true},
		{"id":"denied","token_sha256":%q,"read_sources":[],"write_sources":[]}
	]}`, tokenHash(writerToken), tokenHash(deniedToken))
	configPath := filepath.Join(t.TempDir(), "serve-config.json")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := startDaemon(t, run.store, configPath)
	endpoint := daemon.url

	status, envelope := postJSON(t, endpoint+"/v1/notes", writerToken,
		[]byte(`{"scope":{"source":"notes"},"text":"dock original"}`))
	old, _ := envelopeResultJSON(t, envelope)["id"].(string)
	if status != http.StatusOK || old == "" {
		t.Fatalf("seed note = %d %v", status, envelope)
	}
	status, envelope = postJSON(t, endpoint+"/v1/notes", writerToken,
		[]byte(`{"scope":{"source":"notes"},"text":"dock revised","supersedes":["`+old+`"]}`))
	revised, _ := envelopeResultJSON(t, envelope)["id"].(string)
	if status != http.StatusOK || revised == "" || revised == old {
		t.Fatalf("superseding note = %d %v", status, envelope)
	}

	// A principal without the write grant is refused.
	status, _ = postJSON(t, endpoint+"/v1/notes", deniedToken,
		[]byte(`{"scope":{"source":"notes"},"text":"dock again","supersedes":["`+old+`"]}`))
	if status != http.StatusForbidden {
		t.Fatalf("denied supersede status = %d, want 403", status)
	}

	// Default exclusion shows in the caller trail; include_superseded
	// returns the old note marked.
	status, envelope = postJSON(t, endpoint+"/v1/query", writerToken,
		[]byte(`{"scope":{"source":"notes"},"text":"dock"}`))
	result := envelopeResultJSON(t, envelope)
	ids, _ := passageNoteIDs(t, result)
	if !slices.Equal(ids, []string{revised}) {
		t.Fatalf("served default note ids = %v, want only %s", ids, revised)
	}
	callerTrail, _ := result["trail"].(map[string]any)
	effects, _ := callerTrail["supersession"].([]any)
	if len(effects) != 1 {
		t.Fatalf("served trail supersession = %v", callerTrail)
	}
	status, envelope = postJSON(t, endpoint+"/v1/query", writerToken,
		[]byte(`{"scope":{"source":"notes"},"text":"dock","include_superseded":true}`))
	result = envelopeResultJSON(t, envelope)
	ids, marked := passageNoteIDs(t, result)
	if len(ids) != 2 || len(marked[old]) != 1 {
		t.Fatalf("served include note ids = %v marked %v", ids, marked)
	}

	// The served CLI flag shows the same marked old note.
	code, cliOut, cliErr := runEndpoint(t, endpoint, writerToken, "", "query", "--full", "--source", "notes", "--include-superseded", "dock")
	if code != 0 || !strings.Contains(cliOut, old) || !strings.Contains(cliOut, "superseded_by") {
		t.Fatalf("served CLI include query = %d %s %s", code, cliOut, cliErr)
	}
	daemon.stop(t)
	run.run(false, "verify")
}

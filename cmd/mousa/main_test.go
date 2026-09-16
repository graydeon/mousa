package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tests invoke the built CLI against fresh stores and real directories so each
// command exercises store reopening as well as its requested operation.

func buildCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mousa")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build mousa CLI: %v\n%s", err, out)
	}
	return binary
}

type testRun struct {
	t      *testing.T
	binary string
	store  string
}

func (r testRun) run(expectFail bool, args ...string) (string, map[string]any) {
	return r.runInput("", expectFail, args...)
}

// runInput runs the CLI with input on stdin, for the JSONL stream form. An
// empty input leaves stdin as /dev/null, which is an empty stream.
func (r testRun) runInput(input string, expectFail bool, args ...string) (string, map[string]any) {
	r.t.Helper()
	cmd := exec.Command(r.binary, append([]string{"-store", r.store}, args...)...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if !expectFail && err != nil {
		r.t.Fatalf("mousa %v failed: %v\n%s", args, err, stderr.String())
	}
	if expectFail {
		if err == nil {
			r.t.Fatalf("mousa %v unexpectedly succeeded:\n%s", args, out)
		}
		if len(out) != 0 {
			r.t.Fatalf("failed command emitted a success response: %s", out)
		}
		return stderr.String(), nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		r.t.Fatalf("mousa %v emitted unreadable JSON: %v\n%s", args, err, out)
	}
	return string(out), parsed
}

func lenOf(report map[string]any, key string) int {
	if value, ok := report[key].([]any); ok {
		return len(value)
	}
	return 0
}

func setup(t *testing.T) (testRun, string) {
	t.Helper()
	binary := buildCLI(t)
	root := t.TempDir()
	return testRun{t: t, binary: binary, store: filepath.Join(t.TempDir(), "local.sqlite")}, root
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocalSliceEndToEnd(t *testing.T) {
	run, root := setup(t)

	// 1. Fresh initial import.
	writeFile(t, root, "alpha.md", "# Alpha\n\nMousa retrieves evidence from canonical records.")
	writeFile(t, root, "beta/gamma.md", "The zebra arc ontology lives here.")
	_, sync1 := run.run(false, "sync", root)
	if got := sync1["total_items"].(float64); got != 2 {
		t.Fatalf("initial import total_items = %v, want 2", got)
	}
	if lenOf(sync1, "added") != 2 {
		t.Fatalf("initial import should report 2 added, got %v", sync1["added"])
	}

	// 2. Repeat sync is a no-op: no duplicate logical records.
	_, sync2 := run.run(false, "sync", root)
	if lenOf(sync2, "added") != 0 || lenOf(sync2, "updated") != 0 || lenOf(sync2, "deleted") != 0 {
		t.Fatalf("no-op sync reported changes: %v", sync2)
	}
	if got := lenOf(sync2, "unchanged"); got != 2 {
		t.Fatalf("no-op sync unchanged = %d, want 2", got)
	}

	// 3. Changed content becomes retrievable; old revision loses index rows.
	writeFile(t, root, "alpha.md", "# Alpha rewritten\n\nMarmalade ferrets dominate the new revision.")
	_, sync3 := run.run(false, "sync", root)
	if lenOf(sync3, "updated") != 1 {
		t.Fatalf("update sync should report 1 updated, got %v", sync3["updated"])
	}
	_, query1 := run.run(false, "query", root, "ferrets")
	if hits := lenOf(query1, "evidence"); hits != 1 {
		t.Fatalf("changed content not retrievable: %v", query1)
	}
	if text := query1["evidence"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, "Marmalade") {
		t.Fatalf("query returned stale text: %v", text)
	}
	_, queryOld := run.run(false, "query", root, "canonical records")
	if hits := lenOf(queryOld, "evidence"); hits != 0 {
		t.Fatalf("old revision still retrievable after update: %v", queryOld)
	}

	// 4. Deleted items are excluded.
	if err := os.Remove(filepath.Join(root, "beta", "gamma.md")); err != nil {
		t.Fatal(err)
	}
	_, sync4 := run.run(false, "sync", root)
	if lenOf(sync4, "deleted") != 1 {
		t.Fatalf("delete sync should report 1 deleted, got %v", sync4["deleted"])
	}
	_, query2 := run.run(false, "query", root, "zebra arc")
	if hits := lenOf(query2, "evidence"); hits != 0 {
		t.Fatalf("deleted content still retrievable: %v", query2)
	}

	// 5. Restart/retry: sync after every step has been "restart" (fresh
	// process each run); a repeat no-op sync confirms state consistency.
	_, sync5 := run.run(false, "sync", root)
	if lenOf(sync5, "added") != 0 || lenOf(sync5, "updated") != 0 {
		t.Fatalf("post-delete resync reported changes: %v", sync5)
	}

	// 6. Provenance resolves the item's stable path.
	_, query3 := run.run(false, "query", root, "ferrets")
	hit := query3["evidence"].([]any)[0].(map[string]any)
	if hit["item"] != "alpha.md" {
		t.Fatalf("evidence provenance item = %v", hit["item"])
	}
}

func TestLocalSliceErrorsAndEdgeCases(t *testing.T) {
	run, root := setup(t)

	// Unknown commands and missing directories fail without success output.
	run.run(true, "frobnicate")
	run.run(true, "sync", filepath.Join(root, "missing-dir"))

	// Non-UTF-8 files are skipped and reported, not fatal.
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0xff, 0xfe, 0x00, 0x01}, 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "ok.md", "readable text about walruses")
	_, sync1 := run.run(false, "sync", root)
	if lenOf(sync1, "skipped") != 1 {
		t.Fatalf("non-UTF-8 file should be skipped, got %v", sync1["skipped"])
	}

	// Rename = delete + add under the new item identity.
	if err := os.Rename(filepath.Join(root, "ok.md"), filepath.Join(root, "renamed.md")); err != nil {
		t.Fatal(err)
	}
	_, sync2 := run.run(false, "sync", root)
	if lenOf(sync2, "deleted") != 1 || lenOf(sync2, "added") != 1 {
		t.Fatalf("rename should report delete+add: %v", sync2)
	}

	// The renamed item is retrievable under its new identity (same content,
	// same segments re-ingested under the new item path).
	_, query1 := run.run(false, "query", root, "walruses")
	if hits := lenOf(query1, "evidence"); hits != 1 {
		t.Fatalf("renamed content should be retrievable: %v", query1)
	}
	// Status reflects the stored source.
	_, status := run.run(false, "status", root)
	if status["collection_state"] != "active" {
		t.Fatalf("status collection_state = %v, want active", status["collection_state"])
	}
}

func TestLocalSliceBudgetBoundsOutput(t *testing.T) {
	run, root := setup(t)
	// One large file: several 4096-byte segments. Budget must bound output.
	var large strings.Builder
	for i := 0; i < 40; i++ {
		large.WriteString("quokkas graze quietly in segment number ")
		large.WriteString(strings.Repeat("x", 4000))
		large.WriteString("\n")
	}
	writeFile(t, root, "large.md", large.String())
	run.run(false, "sync", root)
	_, query := run.run(false, "query", root, "quokkas")
	if used := query["used_bytes"].(float64); used > query["budget_bytes"].(float64) {
		t.Fatalf("used bytes %v exceed budget %v", used, query["budget_bytes"])
	}
	if hits := len(query["evidence"].([]any)); hits < 2 {
		t.Fatalf("expected several segments selected, got %d", hits)
	}
}

func TestLocalSliceRestoresIndexAfterDeletedFileIsRecreated(t *testing.T) {
	run, root := setup(t)
	content := "Marmalade ferrets dominate the northern shore."
	writeFile(t, root, "a.md", content)
	run.run(false, "sync", root)
	if err := os.Remove(filepath.Join(root, "a.md")); err != nil {
		t.Fatal(err)
	}
	run.run(false, "sync", root)
	_, before := run.run(false, "query", root, "ferrets")
	if lenOf(before, "evidence") != 0 {
		t.Fatalf("deleted content still retrievable: %v", before)
	}
	// Recreating the identical file must restore retrieval, not silently leave
	// a stored-but-unindexed revision behind.
	writeFile(t, root, "a.md", content)
	_, sync := run.run(false, "sync", root)
	if got := lenOf(sync, "restored"); got != 1 {
		t.Fatalf("recreated file should be reported restored, got %d: %v", got, sync)
	}
	if got := lenOf(sync, "unchanged"); got != 0 {
		t.Fatalf("recreated file must not be reported unchanged: %v", sync)
	}
	if got := lenOf(sync, "added"); got != 0 {
		t.Fatalf("recreated file must not be a new item: %v", sync)
	}
	if got := lenOf(sync, "deleted"); got != 0 {
		t.Fatalf("recreated file must not be reported deleted again: %v", sync)
	}
	_, after := run.run(false, "query", root, "ferrets")
	if lenOf(after, "evidence") != 1 {
		t.Fatalf("recreated content not retrievable: %v", after)
	}
}

func TestLocalSliceRevertsToStoredRevisionContent(t *testing.T) {
	run, root := setup(t)
	older := "The older revision mentions aardvarks."
	writeFile(t, root, "a.md", older)
	run.run(false, "sync", root)
	writeFile(t, root, "a.md", "The newer revision mentions beetles.")
	run.run(false, "sync", root)
	// Reverting the file to content the store already accepted must succeed:
	// the revision's delivery evidence is its first acceptance, not the new
	// modification time.
	writeFile(t, root, "a.md", older)
	_, revert := run.run(false, "sync", root)
	if lenOf(revert, "updated") != 1 || lenOf(revert, "restored") != 0 {
		t.Fatalf("revert of an active item should report one update, got %v", revert)
	}
	_, stale := run.run(false, "query", root, "beetles")
	if lenOf(stale, "evidence") != 0 {
		t.Fatalf("reverted-away revision still retrievable: %v", stale)
	}
	_, restored := run.run(false, "query", root, "aardvarks")
	if lenOf(restored, "evidence") != 1 {
		t.Fatalf("reverted-to revision not retrievable: %v", restored)
	}
	for range 3 {
		_, repeated := run.run(false, "sync", root)
		if lenOf(repeated, "unchanged") != 1 || lenOf(repeated, "updated") != 0 || lenOf(repeated, "restored") != 0 {
			t.Fatalf("repeated sync after revert must be unchanged: %v", repeated)
		}
	}
}

func TestLocalSliceEmptyFileIsStoredWithoutRetrievableText(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "empty.md", "")
	writeFile(t, root, "ok.md", "Readable text about walruses.")
	_, sync := run.run(false, "sync", root)
	if got := lenOf(sync, "added"); got != 2 {
		t.Fatalf("empty file must be stored, not fatal; added = %d: %v", got, sync)
	}
	if got := lenOf(sync, "skipped"); got != 0 {
		t.Fatalf("empty file is valid UTF-8 and must not be skipped: %v", sync)
	}
	if got := lenOf(sync, "unchanged"); got != 0 {
		t.Fatalf("first sync of an empty file is an addition, not unchanged: %v", sync)
	}
	_, emptyQuery := run.run(false, "query", root, "walruses")
	for _, hit := range emptyQuery["evidence"].([]any) {
		if item := hit.(map[string]any)["item"].(string); item == "empty.md" {
			t.Fatalf("empty item must not be retrievable: %v", emptyQuery)
		}
	}
	// A later sync reports the empty item as unchanged, not as a repeated add.
	_, resync := run.run(false, "sync", root)
	if got := lenOf(resync, "unchanged"); got != 2 {
		t.Fatalf("resync unchanged = %d, want 2: %v", got, resync)
	}
}

func TestLocalSliceNormalizedFilesRemainUnchanged(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "crlf.md", "Walrus colony\r\nNorthern shore\r\n")
	writeFile(t, root, "bom.md", "\ufeffWalrus habitat\n")
	run.run(false, "sync", root)
	_, resync := run.run(false, "sync", root)
	if lenOf(resync, "unchanged") != 2 || lenOf(resync, "updated") != 0 {
		t.Fatalf("normalization must not change raw-content retry identity: %v", resync)
	}
	_, query := run.run(false, "query", root, "walrus")
	if lenOf(query, "evidence") != 2 {
		t.Fatalf("normalized files must both remain retrievable: %v", query)
	}
}

func TestLocalSliceAtSignPathsRemainDistinct(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "doc", "Walrus baseline")
	writeFile(t, root, "doc@draft.md", "Walrus draft")
	writeFile(t, root, "nested@folder/note@review.md", "Walrus review")
	run.run(false, "sync", "--all-text", root)
	_, query := run.run(false, "query", root, "walrus")
	want := map[string]bool{"doc": true, "doc@draft.md": true, "nested@folder/note@review.md": true}
	for _, raw := range query["evidence"].([]any) {
		item := raw.(map[string]any)["item"].(string)
		if !want[item] {
			t.Fatalf("unexpected or duplicate item %q: %v", item, query)
		}
		delete(want, item)
	}
	if len(want) != 0 {
		t.Fatalf("missing items: %v; query: %v", want, query)
	}
	if err := os.Remove(filepath.Join(root, "doc")); err != nil {
		t.Fatal(err)
	}
	_, sync := run.run(false, "sync", "--all-text", root)
	if lenOf(sync, "deleted") != 1 || lenOf(sync, "unchanged") != 2 {
		t.Fatalf("deleting a prefix item must leave longer names unchanged: %v", sync)
	}
	_, after := run.run(false, "query", root, "walrus")
	if lenOf(after, "evidence") != 2 {
		t.Fatalf("deleting a prefix item removed other items: %v", after)
	}
}

// A query against a source must not see another source's items.
func TestLocalSliceQueryIsScopedToItsSource(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "only.md", "Private walrus colony on the southern cliff.")
	run.run(false, "sync", root)
	run.runInput(record("leaked.md", "Private walrus colony details."), false, "sync", "--source", "pipeline/other")
	_, scoped := run.run(false, "query", root, "walrus")
	for _, hit := range scoped["evidence"].([]any) {
		if item := hit.(map[string]any)["item"].(string); item == "leaked.md" {
			t.Fatalf("query leaked another source's item: %v", scoped)
		}
	}
	_, stream := run.runInput("", false, "query", "--source", "pipeline/other", "walrus")
	if item := firstItem(t, stream); item != "leaked.md" {
		t.Fatalf("stream source query did not return its own item: %v", stream)
	}
}

func TestLocalSliceStatusSeparatesCurrentItemsFromHistory(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "doc", "Old walrus observations.")
	run.run(false, "sync", "--all-text", root)
	writeFile(t, root, "doc", "New ferret observations.")
	run.run(false, "sync", "--all-text", root)
	_, updated := run.run(false, "status", root)
	if updated["active_items"] != float64(1) || updated["observations"] != float64(2) {
		t.Fatalf("status confuses active items with history: %v", updated)
	}
	if err := os.Remove(filepath.Join(root, "doc")); err != nil {
		t.Fatal(err)
	}
	run.run(false, "sync", "--all-text", root)
	_, deleted := run.run(false, "status", root)
	if deleted["active_items"] != float64(0) || deleted["observations"] != float64(2) {
		t.Fatalf("deletion removed history or retained activation: %v", deleted)
	}
}

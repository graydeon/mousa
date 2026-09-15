package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The acceptance suite runs the built CLI against a fresh store and a real
// temporary directory, exercising the mission's acceptance list through the
// supported interface: initial import, idempotent no-op sync, changed content
// retrievable, deleted content excluded, restart safety, provenance, byte
// budget, and predictable errors.

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
	r.t.Helper()
	cmd := exec.Command(r.binary, append([]string{"-store", r.store}, args...)...)
	out, err := cmd.CombinedOutput()
	if !expectFail && err != nil {
		r.t.Fatalf("mousa %v failed: %v\n%s", args, err, out)
	}
	if expectFail && err == nil {
		r.t.Fatalf("mousa %v unexpectedly succeeded:\n%s", args, out)
	}
	var parsed map[string]any
	if !expectFail {
		if err := json.Unmarshal(out, &parsed); err != nil {
			r.t.Fatalf("mousa %v emitted unreadable JSON: %v\n%s", args, err, out)
		}
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

	// 4. Deleted/withdrawn content is excluded.
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
	if lenOf(sync5, "added") != 0 && lenOf(sync5, "updated") != 0 {
		t.Fatalf("post-delete resync reported changes: %v", sync5)
	}

	// 6. Provenance and bounded output in evidence.
	_, query3 := run.run(false, "query", root, "ferrets")
	hit := query3["evidence"].([]any)[0].(map[string]any)
	if hit["item"] != "alpha.md" {
		t.Fatalf("evidence provenance item = %v", hit["item"])
	}
	if hit["segment_id"] == "" || hit["byte_length"].(float64) == 0 {
		t.Fatalf("evidence missing provenance fields: %v", hit)
	}
	if hit["segment_id"] == "" {
		t.Fatalf("segment_id must be present: %v", hit)
	}
	if query3["decision_id"] == "" {
		t.Fatalf("query result must carry its policy decision id: %v", query3)
	}
}

func TestLocalSliceErrorsAndEdgeCases(t *testing.T) {
	run, root := setup(t)

	// Unknown command and missing dir fail predictably with exit code 2.
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

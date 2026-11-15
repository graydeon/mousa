package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func manifestExit(t *testing.T, run testRun, want int, args ...string) map[string]any {
	t.Helper()
	code, stdout, stderr := run.runExit(append([]string{"manifest"}, args...)...)
	if code != want {
		t.Fatalf("manifest exit=%d want=%d: %s", code, want, stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("manifest output %q: %v", stdout, err)
	}
	return result
}

func manifestID(t *testing.T, result map[string]any) string {
	t.Helper()
	manifest, ok := result["manifest"].(map[string]any)
	if !ok {
		t.Fatalf("missing manifest: %v", result)
	}
	id, ok := manifest["id"].(string)
	if !ok || len(id) != 64 {
		t.Fatalf("invalid manifest ID: %v", manifest)
	}
	return id
}

func assertManifestDenied(t *testing.T, result map[string]any) {
	t.Helper()
	if len(result) != 3 || result["decision_outcome"] != "deny" || result["decision_id"] == nil || result["decision_reasons"] == nil {
		t.Fatalf("denial disclosed manifest facts: %v", result)
	}
}

func TestNativeManifestIndexedSnapshot(t *testing.T) {
	for _, policy := range []string{"fixed-v1", "passage-v1"} {
		t.Run(policy, func(t *testing.T) {
			run, root := setup(t)
			writeFile(t, root, "doc.md", "# Indexed\n\nOriginal content.")
			writeFile(t, root, "empty.txt", "")
			run.run(false, "sync", "--segment-policy", policy, root)
			first := manifestExit(t, run, 0, root)
			id := manifestID(t, first)
			if _, present := first["matches"]; present {
				t.Fatal("export unexpectedly includes matches")
			}
			writeFile(t, root, "doc.md", "Changed live content.")
			matched := manifestExit(t, run, 0, "--expect", id, root)
			if matched["matches"] != true || !reflect.DeepEqual(first["manifest"], matched["manifest"]) || first["decision_id"] == matched["decision_id"] {
				t.Fatal("unsynced input changed manifest or authorization was reused")
			}
			run.run(false, "sync", "--segment-policy", policy, root)
			changed := manifestExit(t, run, 1, "--expect", id, root)
			if changed["matches"] != false || manifestID(t, changed) == id {
				t.Fatal("sync did not invalidate expectation")
			}
			run.run(false, "access", root, "deny")
			assertManifestDenied(t, manifestExit(t, run, 1, "--expect", id, root))
			assertManifestDenied(t, manifestExit(t, run, 1, root))
		})
	}
}

func TestNativeManifestJSONLTransitions(t *testing.T) {
	run, _ := setup(t)
	records := "{\"id\":\"a\",\"text\":\"alpha\"}\n{\"id\":\"b\",\"text\":\"\"}\n"
	run.runInput(records, false, "sync", "--source", "alpha")
	first := manifestExit(t, run, 0, "--source", "alpha")
	id := manifestID(t, first)
	run.runInput("{\"id\":\"a\",\"text\":\"alpha\"}\n", false, "sync", "--source", "alpha")
	if manifestExit(t, run, 0, "--source", "alpha", "--expect", id)["matches"] != true {
		t.Fatal("JSONL omission deleted a current item")
	}
	run.runInput("{\"id\":\"a\",\"deleted\":true}\n", false, "sync", "--source", "alpha")
	if manifestExit(t, run, 1, "--source", "alpha", "--expect", id)["matches"] != false {
		t.Fatal("deletion preserved identity")
	}
	run.runInput(records, false, "sync", "--source", "alpha")
	manifestExit(t, run, 0, "--source", "alpha", "--expect", id)
	run.run(false, "withdraw", "--source", "alpha")
	assertManifestDenied(t, manifestExit(t, run, 1, "--source", "alpha", "--expect", id))
}

func TestNativeManifestSyntaxBeforeStore(t *testing.T) {
	run, root := setup(t)
	run.store = filepath.Join(t.TempDir(), "absent.sqlite")
	cases := [][]string{{}, {"--source", "alpha", root}, {"--unknown"}, {"--source", "alpha", "--expect", strings.Repeat("a", 64), "--expect", strings.Repeat("b", 64)}}
	for _, id := range []string{"", "null", "1", strings.Repeat("0", 64), strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 65), " " + strings.Repeat("a", 64)} {
		cases = append(cases, []string{"--source", "alpha", "--expect", id})
	}
	for _, args := range cases {
		code, stdout, _ := run.runExit(append([]string{"manifest"}, args...)...)
		if code != 2 || stdout != "" {
			t.Fatalf("invalid args %q: exit=%d stdout=%s", args, code, stdout)
		}
		if _, err := os.Stat(run.store); !os.IsNotExist(err) {
			t.Fatalf("invalid invocation opened store: %v", err)
		}
	}
}

func TestNativeManifestEmptyAndUnknownSources(t *testing.T) {
	run, root := setup(t)
	item := filepath.Join(root, "empty.txt")
	if err := os.WriteFile(item, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	run.run(false, "sync", root)
	if err := os.Remove(item); err != nil {
		t.Fatal(err)
	}
	run.run(false, "sync", root)
	empty := manifestExit(t, run, 0, root)
	if len(empty["manifest"].(map[string]any)["items"].([]any)) != 0 {
		t.Fatal("empty directory manifest has items")
	}
	manifestExit(t, run, 0, "--expect", manifestID(t, empty), root)
	assertManifestDenied(t, manifestExit(t, run, 1, "--source", "unknown"))
}

func TestNativeManifestDistinctDirectoryRoots(t *testing.T) {
	run, firstRoot := setup(t)
	secondRoot := t.TempDir()
	for _, root := range []string{firstRoot, secondRoot} {
		writeFile(t, root, "doc.md", "Identical indexed content.")
		run.run(false, "sync", root)
	}
	first := manifestExit(t, run, 0, firstRoot)
	second := manifestExit(t, run, 0, secondRoot)
	firstID, secondID := manifestID(t, first), manifestID(t, second)
	if firstID == secondID {
		t.Fatal("different directory roots produced the same manifest identity")
	}
	if manifestExit(t, run, 1, "--expect", firstID, secondRoot)["matches"] != false {
		t.Fatal("another directory's manifest was accepted")
	}
	for _, root := range []string{firstRoot, secondRoot} {
		run.run(false, "sync", root)
	}
	if manifestID(t, manifestExit(t, run, 0, firstRoot)) != firstID || manifestID(t, manifestExit(t, run, 0, secondRoot)) != secondID {
		t.Fatal("unchanged directory roots did not retain their own identities")
	}
}

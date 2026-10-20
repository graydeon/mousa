package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestDirectorySupersessionWorkflow(t *testing.T) {
	run, root := setup(t)
	const text = "cedar directory evidence"
	writeFile(t, root, "old.md", text)
	writeFile(t, root, "new.txt", text)
	run.run(false, "sync", root)
	source, _, err := directorySource(root)
	if err != nil {
		t.Fatal(err)
	}
	query := func(packing, mode string, budget int) map[string]any {
		t.Helper()
		args := []string{"query", "--packing-policy", packing, "--budget-bytes", fmt.Sprint(budget)}
		if mode != "default" {
			args = append(args, "--supersession="+mode)
		}
		_, report := run.run(false, append(args, root, "cedar")...)
		return report
	}
	history := func(report map[string]any) map[string]any {
		t.Helper()
		_, inspected := run.run(false, "trail", root, report["trail_id"].(string))
		return inspected["historical"].(map[string]any)
	}
	baseline := query(mousa.PackingOriginal, "default", 8192)
	noHistory := query(mousa.PackingOriginal, "true", 8192)
	requireSupersessionCounts(t, noHistory, "evidence", 2, 0, 0, 0, 2)
	if baseline["packet_id"] != noHistory["packet_id"] || history(noHistory)["supersession"].(map[string]any)["activation_id"] != nil {
		t.Fatalf("no-history changed selection: %v", noHistory)
	}
	first, second := hits(baseline)[0].(map[string]any), hits(baseline)[1].(map[string]any)
	predecessor, err := mousa.ParseRepresentationID(first["representation_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	successor, err := mousa.ParseRepresentationID(second["representation_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	declaration := storedDeclaration(t, run, declarationJSON(t, source, first["item"].(string), predecessor, second["item"].(string), successor, "example.operator", "explicit replacement"))
	declared := query(mousa.PackingOriginal, "true", 8192)
	requireSupersessionCounts(t, declared, "evidence", 2, 0, 0, 0, 2)
	active := activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	var produced map[string]any
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		for _, mode := range []string{"default", "false"} {
			unchanged := query(packing, mode, len(text))
			if _, present := unchanged["supersession_excluded"]; present || hits(unchanged)[0].(map[string]any)["segment_id"] != first["segment_id"] {
				t.Fatalf("administration changed %s query: %v", mode, unchanged)
			}
		}
		selected := query(packing, "true", len(text))
		requireSupersessionCounts(t, selected, "evidence", 2, 1, 0, 0, 1)
		if hits(selected)[0].(map[string]any)["segment_id"] != second["segment_id"] || selected["used_bytes"] != float64(len(text)) {
			t.Fatalf("withholding did not free survivor budget: %v", selected)
		}
		produced = history(selected)
		selection := produced["supersession"].(map[string]any)
		row := selection["dispositions"].([]any)[0].(map[string]any)
		if produced["schema"] != "mousa.source_trail.v4" || selection["activation_id"] != active.String() || selection["declaration_id"] != declaration.String() || row["segment_id"] != first["segment_id"] || row["predecessor_item_id"] != first["item"] || row["predecessor_representation_id"] != predecessor.String() || row["successor_item_id"] != second["item"] || row["successor_representation_id"] != successor.String() {
			t.Fatalf("wrong directory consultation pins: %v", produced)
		}
	}
	_, saved := run.run(false, "query", "--supersession", root, "cedar")
	savedSelection := history(saved)["supersession"]
	deactivated := activateQueryDeclaration(t, run, source, &active, nil, 2)
	restored := query(mousa.PackingOriginal, "true", 8192)
	requireSupersessionCounts(t, restored, "evidence", 2, 0, 0, 0, 2)
	if history(restored)["supersession"].(map[string]any)["activation_id"] != deactivated.String() {
		t.Fatal("deactivation not recorded")
	}
	activateQueryDeclaration(t, run, source, &deactivated, &declaration, 3)
	requireSupersessionCounts(t, query(mousa.PackingOriginal, "true", 8192), "evidence", 2, 1, 0, 0, 1)
	writeFile(t, root, "unrelated.md", text)
	if err := os.Remove(filepath.Join(root, second["item"].(string))); err != nil {
		t.Fatal(err)
	}
	run.run(false, "sync", root)
	requireSupersessionCounts(t, query(mousa.PackingOriginal, "true", 8192), "evidence", 2, 1, 0, 0, 1)
	peer := t.TempDir()
	writeFile(t, peer, first["item"].(string), text)
	run.run(false, "sync", peer)
	_, isolated := run.run(false, "query", "--supersession", peer, "cedar")
	requireSupersessionCounts(t, isolated, "evidence", 1, 0, 0, 0, 1)
	// The same label in the JSONL namespace must not resolve to this directory.
	jsonlSync(t, run, root, record(first["item"].(string), text))
	_, stream := supersessionQuery(t, run, root, mousa.PackingOriginal, "cedar", 8192, true)
	requireSupersessionCounts(t, stream, "evidence", 1, 0, 0, 0, 1)
	writeFile(t, root, first["item"].(string), text+" revised")
	run.run(false, "sync", root)
	requireSupersessionCounts(t, query(mousa.PackingOriginal, "true", 8192), "evidence", 2, 0, 0, 0, 2)
	old := history(saved)
	if !reflect.DeepEqual(old["supersession"], savedSelection) {
		t.Fatalf("historical consultation reinterpreted: %v", old)
	}
	for _, value := range old["candidates"].([]any) {
		if row := value.(map[string]any); row["segment_id"] == first["segment_id"] && row["indexed_now"] != false {
			t.Fatal("retired pin still indexed")
		}
	}
	run.run(false, "access", root, "deny")
	requireSupersessionCounts(t, query(mousa.PackingOriginal, "true", 8192), "policy_excluded", 0, 0, 0, 0, 0)
	_, denied := run.run(false, "trail", root, saved["trail_id"].(string))
	if denied["historical"] != nil {
		t.Fatal("denied directory history released")
	}
	run.run(false, "access", root, "allow")
	run.run(false, "withdraw", root)
	requireSupersessionCounts(t, query(mousa.PackingOriginal, "true", 8192), "lifecycle_excluded", 0, 0, 0, 0, 0)
	rawExec(t, run.store, `UPDATE supersession_activation_state SET active_declaration_id = NULL`)
	before, audit := snapshotActivationStore(t, run.store), supersessionAuditSnapshot(t, run.store)
	code, stdout, stderr := run.runExit("query", "--supersession", root, "cedar")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "integrity") {
		t.Fatalf("damaged directory administration exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	requireUnchanged(t, "directory startup refusal", before, snapshotActivationStore(t, run.store))
	if !reflect.DeepEqual(audit, supersessionAuditSnapshot(t, run.store)) {
		t.Fatal("directory startup refusal wrote audit records")
	}
}

package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

func supersessionQuery(t *testing.T, run testRun, source, packing, text string, budget int, opted bool) (string, map[string]any) {
	t.Helper()
	args := []string{"query", "--source", source, "--packing-policy", packing, "--budget-bytes", fmt.Sprint(budget)}
	if opted {
		args = append(args, "--supersession")
	}
	return run.run(false, append(args, text)...)
}

func supersessionHistory(t *testing.T, run testRun, source string, report map[string]any) map[string]any {
	t.Helper()
	_, inspected := run.run(false, "trail", "--source", source, report["trail_id"].(string))
	return inspected["historical"].(map[string]any)
}

func requireSupersessionCounts(t *testing.T, report map[string]any, outcome string, matched, excluded, budget, duplicate, evidence int) {
	t.Helper()
	for key, want := range map[string]int{"matched_candidates": matched, "supersession_excluded": excluded, "budget_omitted": budget} {
		if report[key] != float64(want) {
			t.Fatalf("%s = %v, want %d: %v", key, report[key], want, report)
		}
	}
	if report["outcome"] != outcome || len(hits(report)) != evidence {
		t.Fatalf("want outcome %s with %d evidence: %v", outcome, evidence, report)
	}
	if report["packing_policy"] == mousa.PackingExactV1 && report["duplicate_omitted"] != float64(duplicate) {
		t.Fatalf("duplicate count = %v, want %d: %v", report["duplicate_omitted"], duplicate, report)
	}
}

func activateQueryDeclaration(t *testing.T, run testRun, source mousa.Source, previous *mousa.SupersessionActivationID, declaration *mousa.SupersessionDeclarationID, at int64) mousa.SupersessionActivationID {
	t.Helper()
	canonical := activationJSON(t, source, previous, declaration, "example.operator", "1", at, nil)
	requireApplied(t, run, canonical)
	return activationIdentity(t, canonical)
}

func TestSupersessionQueryNoHistoryPreservesDefaultPacketAndLegacyTrail(t *testing.T) {
	run, _ := setup(t)
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			const source = "query/no-history"
			jsonlSync(t, run, source, record("doc", "cedar evidence"))
			_, legacy := supersessionQuery(t, run, source, packing, "cedar", 8192, false)
			if _, present := legacy["supersession_excluded"]; present {
				t.Fatalf("default gained supersession field: %v", legacy)
			}
			_, opted := supersessionQuery(t, run, source, packing, "cedar", 8192, true)
			requireSupersessionCounts(t, opted, "evidence", 1, 0, 0, 0, 1)
			if legacy["packet_id"] != opted["packet_id"] || !reflect.DeepEqual(legacy["evidence"], opted["evidence"]) || legacy["used_bytes"] != opted["used_bytes"] {
				t.Fatalf("no-history opt-in changed packet: default=%v opted=%v", legacy, opted)
			}
			for _, key := range []string{"request_id", "decision_id", "trail_id"} {
				if legacy[key] == opted[key] {
					t.Fatalf("fresh invocation reused %s", key)
				}
			}
			oldHistory := supersessionHistory(t, run, source, legacy)
			if _, present := oldHistory["supersession"]; present {
				t.Fatalf("legacy history gained consultation: %v", oldHistory)
			}
			wantSchema := any(nil)
			if packing == mousa.PackingExactV1 {
				wantSchema = "mousa.source_trail.v2"
			}
			if oldHistory["schema"] != wantSchema {
				t.Fatalf("legacy schema = %v, want %v", oldHistory["schema"], wantSchema)
			}
			history := supersessionHistory(t, run, source, opted)
			selection := history["supersession"].(map[string]any)
			if history["schema"] != "mousa.source_trail.v4" || selection["consulted"] != true || selection["activation_id"] != nil || selection["declaration_id"] != nil || len(selection["dispositions"].([]any)) != 0 {
				t.Fatalf("no-history consultation not recorded explicitly: %v", history)
			}
			_, empty := supersessionQuery(t, run, source, packing, "unmatched", 8192, true)
			requireSupersessionCounts(t, empty, "no_matches", 0, 0, 0, 0, 0)
		})
	}
}

func TestSupersessionQueryExactPinTransitionsHistoryAndSourceIsolation(t *testing.T) {
	run, _ := setup(t)
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			sourceName := "query/transitions/" + packing
			source, declaration, _ := activationPins(t, run, sourceName)
			active := activateQueryDeclaration(t, run, source, nil, &declaration, 1)
			raw, suppressed := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, true)
			requireSupersessionCounts(t, suppressed, "supersession_excluded", 1, 1, 0, 0, 0)
			if strings.Contains(raw, "oldterm predecessor evidence") {
				t.Fatal("withheld text leaked")
			}
			history := supersessionHistory(t, run, sourceName, suppressed)
			historyJSON, err := json.Marshal(history)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(historyJSON), "oldterm predecessor evidence") {
				t.Fatal("historical trail leaked withheld text")
			}
			selection := history["supersession"].(map[string]any)
			if selection["activation_id"] != active.String() || selection["declaration_id"] != declaration.String() || len(selection["dispositions"].([]any)) != 1 {
				t.Fatalf("wrong consultation evidence: %v", history)
			}
			candidate := history["candidates"].([]any)[0].(map[string]any)
			if candidate["selected"] != false || candidate["text_bytes"] != float64(len("oldterm predecessor evidence")) || candidate["rank"] != float64(1) {
				t.Fatalf("suppression changed size/rank: %v", candidate)
			}
			_, defaultResult := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, false)
			if len(hits(defaultResult)) != 1 {
				t.Fatalf("active administration changed default: %v", defaultResult)
			}
			deactivated := activateQueryDeclaration(t, run, source, &active, nil, 2)
			_, restored := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, true)
			requireSupersessionCounts(t, restored, "evidence", 1, 0, 0, 0, 1)
			deactivationHistory := supersessionHistory(t, run, sourceName, restored)["supersession"].(map[string]any)
			if deactivationHistory["activation_id"] != deactivated.String() || deactivationHistory["declaration_id"] != nil {
				t.Fatalf("deactivation not explicit: %v", deactivationHistory)
			}
			activateQueryDeclaration(t, run, source, &deactivated, &declaration, 3)
			_, again := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, true)
			requireSupersessionCounts(t, again, "supersession_excluded", 1, 1, 0, 0, 0)
			if again["packet_id"] != suppressed["packet_id"] || again["request_id"] == suppressed["request_id"] {
				t.Fatalf("restart changed deterministic packet or reused request: %v", again)
			}

			jsonlSync(t, run, sourceName, record("equal", "oldterm predecessor evidence"))
			_, equal := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, true)
			requireSupersessionCounts(t, equal, "evidence", 2, 1, 0, 0, 1)
			if firstItem(t, equal) != "equal" {
				t.Fatalf("equal-byte unrelated item was suppressed: %v", equal)
			}
			peer := sourceName + "/peer"
			jsonlSync(t, run, peer, record("doc", "oldterm predecessor evidence"))
			_, isolated := supersessionQuery(t, run, peer, packing, "oldterm", 8192, true)
			requireSupersessionCounts(t, isolated, "evidence", 1, 0, 0, 0, 1)

			// Successor withdrawal and revision do not revoke the exact predecessor pin.
			jsonlSync(t, run, sourceName, `{"id":"corrected","deleted":true}`)
			_, unavailable := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, true)
			requireSupersessionCounts(t, unavailable, "evidence", 2, 1, 0, 0, 1)
			jsonlSync(t, run, sourceName, record("corrected", "newterm revised successor"))
			_, revisedSuccessor := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, true)
			requireSupersessionCounts(t, revisedSuccessor, "evidence", 2, 1, 0, 0, 1)
			jsonlSync(t, run, sourceName, record("doc", "oldterm later predecessor revision"))
			_, stale := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, true)
			requireSupersessionCounts(t, stale, "evidence", 2, 0, 0, 0, 2)
			oldHistory := supersessionHistory(t, run, sourceName, suppressed)
			if !reflect.DeepEqual(oldHistory["supersession"], history["supersession"]) || oldHistory["packet_id"] != suppressed["packet_id"] || oldHistory["candidates"].([]any)[0].(map[string]any)["indexed_now"] != false {
				t.Fatalf("history reinterpreted against current state: %v", oldHistory)
			}
		})
	}
}

func TestSupersessionQueryBudgetPrecedenceFreedBytesAndDuplicates(t *testing.T) {
	run, _ := setup(t)
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			sourceName := "query/budget/" + packing
			source, declaration, _ := activationPins(t, run, sourceName)
			activateQueryDeclaration(t, run, source, nil, &declaration, 1)
			_, all := supersessionQuery(t, run, sourceName, packing, "oldterm", 1, true)
			requireSupersessionCounts(t, all, "supersession_excluded", 1, 1, 0, 0, 0)
			_, mixed := supersessionQuery(t, run, sourceName, packing, "oldterm newterm", 1, true)
			requireSupersessionCounts(t, mixed, "budget_omitted", 2, 1, 1, 0, 0)
			jsonlSync(t, run, sourceName, strings.Join([]string{record("equal-a", "oldterm predecessor evidence"), record("equal-b", "oldterm predecessor evidence")}, "\n"))
			_, freed := supersessionQuery(t, run, sourceName, packing, "oldterm", len("oldterm predecessor evidence"), true)
			if packing == mousa.PackingExactV1 {
				requireSupersessionCounts(t, freed, "evidence", 3, 1, 0, 1, 1)
			} else {
				requireSupersessionCounts(t, freed, "evidence", 3, 1, 1, 0, 1)
			}
			history := supersessionHistory(t, run, sourceName, freed)
			withheld := history["supersession"].(map[string]any)["dispositions"].([]any)[0].(map[string]any)["segment_id"]
			for _, value := range history["candidates"].([]any) {
				row := value.(map[string]any)
				if row["duplicate_of"] == withheld {
					t.Fatalf("withheld candidate retained as duplicate: %v", history)
				}
			}
		})
	}
}

func TestSupersessionQueryDeniedHistoryUsesFreshAuthorization(t *testing.T) {
	run, _ := setup(t)
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			sourceName := "query/deny/" + packing
			source, declaration, _ := activationPins(t, run, sourceName)
			active := activateQueryDeclaration(t, run, source, nil, &declaration, 1)
			_, allowed := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, true)
			run.run(false, "access", "--source", sourceName, "deny")
			_, denied := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, true)
			requireSupersessionCounts(t, denied, "policy_excluded", 0, 0, 0, 0, 0)
			_, deniedRead := run.run(false, "trail", "--source", sourceName, allowed["trail_id"].(string))
			if deniedRead["historical"] != nil || deniedRead["authorization_outcome"] != "deny" {
				t.Fatalf("history escaped fresh denial: %v", deniedRead)
			}
			run.run(false, "access", "--source", sourceName, "allow")
			denyHistory := supersessionHistory(t, run, sourceName, denied)
			selection := denyHistory["supersession"].(map[string]any)
			if denyHistory["schema"] != "mousa.source_trail.v4" || selection["consulted"] != false || selection["activation_id"] != nil || selection["declaration_id"] != nil || len(selection["dispositions"].([]any)) != 0 {
				t.Fatalf("denial consulted administration: %v", denyHistory)
			}
			activateQueryDeclaration(t, run, source, &active, nil, 2)
			old := supersessionHistory(t, run, sourceName, allowed)
			if old["supersession"].(map[string]any)["activation_id"] != active.String() {
				t.Fatalf("history changed after deactivation: %v", old)
			}
			run.run(false, "withdraw", "--source", sourceName)
			_, withdrawn := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, true)
			requireSupersessionCounts(t, withdrawn, "lifecycle_excluded", 0, 0, 0, 0, 0)
		})
	}
}

func TestSupersessionQueryInvalidOptionsDoNotCreateStore(t *testing.T) {
	run, root := setup(t)
	cases := [][]string{
		{"--source", "x", "--supersession=invalid", "text"},
		{"--source", "x", "--supersession", "--policy", "invalid", "text"},
		{"--source", "x", "--supersession", "--packing-policy", "invalid", "text"},
		{"--source", "x", "--supersession", "--budget-bytes", "0", "text"},
		{"--source", "x", "--supersession", "--budget-bytes", "-1", "text"},
		{"--source", "x", "--supersession", "--budget-bytes", "18446744073709551616", "text"},
		{"--source", "x", "--supersession", ""},
		{"--source", "x", "--supersession", "!!!"},
		{"--source", "x", "--supersession", strings.Repeat("a", mousa.MaxLexicalExpressionBytes+1)},
		{"--source", "x", "--supersession"},
		{"--supersession", root, "text"},
		{"--source", "x", "--supersession", root, "text"},
		{"--source", "x", "--supersession", "--associations=", "text"},
		{"--source", "x", "--supersession", "--associations", "missing.json", "text"},
		{"--source", "x", "--supersession", "--unknown", "text"},
	}
	for index, args := range cases {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			absent := testRun{t: t, binary: run.binary, store: filepath.Join(t.TempDir(), "absent.sqlite")}
			code, stdout, stderr := absent.runExit(append([]string{"query"}, args...)...)
			if code != 2 || stdout != "" {
				t.Fatalf("invalid invocation exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if _, err := os.Stat(absent.store); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid invocation created store: %v", err)
			}
		})
	}
}

func TestSupersessionQueryUnknownSourceAndNativeCorruptionBoundaries(t *testing.T) {
	run, _ := setup(t)
	// Valid unknown-source queries follow the existing writable-open boundary: they create
	// and migrate the store before source lookup fails, but never commit a decision or trail.
	code, stdout, stderr := run.runExit("query", "--source", "genuinely-unknown", "--supersession", "oldterm")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "integrity") {
		t.Fatalf("unknown source exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Stat(run.store); err != nil {
		t.Fatalf("valid unknown-source query did not reach writable open: %v", err)
	}
	for _, table := range []string{"policy_decisions", "source_trails"} {
		count, _ := tableDigest(t, run.store, table)
		if count != 0 {
			t.Fatalf("unknown source committed %s", table)
		}
	}
	source, declaration, _ := activationPins(t, run, "query/corrupt")
	activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	// Damage occurs before subprocess open. This demonstrates startup refusal, not a
	// transaction rollback after an already-open handle (covered by the store tests).
	rawExec(t, run.store, `UPDATE supersession_activation_state SET active_declaration_id = NULL`)
	before := snapshotActivationStore(t, run.store)
	decisions, decisionsDigest := tableDigest(t, run.store, "policy_decisions")
	trails, trailsDigest := tableDigest(t, run.store, "source_trails")
	code, stdout, stderr = run.runExit("query", "--source", "query/corrupt", "--supersession", "oldterm")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "integrity") {
		t.Fatalf("damaged startup exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	requireUnchanged(t, "rejected native query", before, snapshotActivationStore(t, run.store))
	if count, digest := tableDigest(t, run.store, "policy_decisions"); count != decisions || digest != decisionsDigest {
		t.Fatal("damaged startup wrote a decision")
	}
	if count, digest := tableDigest(t, run.store, "source_trails"); count != trails || digest != trailsDigest {
		t.Fatal("damaged startup wrote a trail")
	}
	// Validate that no success-shaped JSON was emitted on the fail-closed path.
	var packet map[string]any
	if json.Unmarshal([]byte(stdout), &packet) == nil {
		t.Fatal("corrupt native request emitted JSON")
	}
}

func TestSupersessionQueryFreesBudgetBeforeDuplicateClassification(t *testing.T) {
	run, _ := setup(t)
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			sourceName := "query/freed/" + packing
			const text = "cedar equal evidence"
			jsonlSync(t, run, sourceName, strings.Join([]string{
				record("a", text), record("b", text), record("c", text),
				record("successor", "replacement unmatched"),
			}, "\n"))
			_, baseline := supersessionQuery(t, run, sourceName, packing, "cedar", len(text), false)
			first := hits(baseline)[0].(map[string]any)
			predecessor, err := mousa.ParseRepresentationID(first["representation_id"].(string))
			if err != nil {
				t.Fatal(err)
			}
			successorItem, successor := revisionPin(t, run, sourceName, "replacement")
			source := declarationSource(t, sourceName)
			declaration := storedDeclaration(t, run, declarationJSON(t, source, first["item"].(string), predecessor, successorItem, successor, "example.operator", "replacement"))
			activateQueryDeclaration(t, run, source, nil, &declaration, 1)
			_, result := supersessionQuery(t, run, sourceName, packing, "cedar", len(text), true)
			budget, duplicates := 1, 0
			if packing == mousa.PackingExactV1 {
				budget, duplicates = 0, 1
			}
			requireSupersessionCounts(t, result, "evidence", 3, 1, budget, duplicates, 1)
			selected := hits(result)[0].(map[string]any)
			if selected["segment_id"] == first["segment_id"] || selected["rank"] != float64(2) || result["used_bytes"] != float64(len(text)) {
				t.Fatalf("withheld rank 1 did not free budget for rank 2: %v", result)
			}
			for _, value := range supersessionHistory(t, run, sourceName, result)["candidates"].([]any) {
				row := value.(map[string]any)
				if row["segment_id"] == first["segment_id"] && (row["omission"] != nil || row["duplicate_of"] != nil) {
					t.Fatalf("suppression misclassified as packing: %v", row)
				}
				if row["omission"] == "duplicate" && row["duplicate_of"] != selected["segment_id"] {
					t.Fatalf("duplicate does not name retained survivor: %v", row)
				}
			}
		})
	}
}

func TestSupersessionQuerySuccessorOutsideCandidateLimitStillSuppresses(t *testing.T) {
	run, _ := setup(t)
	const sourceName = "query/candidate-limit"
	records := []string{record("successor", "replacement cedar "+strings.Repeat("filler ", 1000))}
	for index := range 101 {
		records = append(records, record(fmt.Sprint(index), "cedar"))
	}
	jsonlSync(t, run, sourceName, strings.Join(records, "\n"))
	_, baseline := supersessionQuery(t, run, sourceName, mousa.PackingOriginal, "cedar", 8192, false)
	if baseline["matched_candidates"] != float64(100) {
		t.Fatalf("fixture did not exceed candidate limit: %v", baseline)
	}
	for _, value := range hits(baseline) {
		if value.(map[string]any)["item"] == "successor" {
			t.Fatal("fixture successor unexpectedly inside candidate limit")
		}
	}
	first := hits(baseline)[0].(map[string]any)
	predecessor, err := mousa.ParseRepresentationID(first["representation_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	successorItem, successor := revisionPin(t, run, sourceName, "replacement")
	source := declarationSource(t, sourceName)
	declaration := storedDeclaration(t, run, declarationJSON(t, source, first["item"].(string), predecessor, successorItem, successor, "example.operator", "replacement"))
	activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		_, result := supersessionQuery(t, run, sourceName, packing, "cedar", 8192, true)
		evidence, duplicates := 99, 0
		if packing == mousa.PackingExactV1 {
			evidence, duplicates = 1, 98
		}
		requireSupersessionCounts(t, result, "evidence", 100, 1, 0, duplicates, evidence)
	}
}

func TestSupersessionQueryWritableOpenMigrationAndDeniedCorruptionBoundary(t *testing.T) {
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "query/open-boundary")
	activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	run.run(false, "access", "--source", "query/open-boundary", "deny")
	rawExec(t, run.store, `DROP INDEX supersession_activations_source_idx; DELETE FROM schema_migrations WHERE version = 14`)
	if version := schemaVersion(t, run.store); version != 13 {
		t.Fatalf("older fixture version = %d", version)
	}
	_, denied := supersessionQuery(t, run, "query/open-boundary", mousa.PackingOriginal, "oldterm", 8192, true)
	requireSupersessionCounts(t, denied, "policy_excluded", 0, 0, 0, 0, 0)
	if version := schemaVersion(t, run.store); version != 14 {
		t.Fatalf("valid query did not migrate at writable open: %d", version)
	}
	if _, err := os.Stat(run.store + ".pre-migrate-v13-to-v14.sqlite"); err != nil {
		t.Fatalf("writable-open migration backup absent: %v", err)
	}
	// Denial skips consultation only after successful startup verification. A native
	// invocation cannot bypass damaged administration at startup, even when denied.
	rawExec(t, run.store, `UPDATE supersession_activation_state SET active_declaration_id = NULL`)
	before := snapshotActivationStore(t, run.store)
	audit := supersessionAuditSnapshot(t, run.store)
	code, stdout, stderr := run.runExit("query", "--source", "query/open-boundary", "--supersession", "oldterm")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "integrity") {
		t.Fatalf("denied damaged startup exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	requireUnchanged(t, "denied startup refusal", before, snapshotActivationStore(t, run.store))
	if !reflect.DeepEqual(audit, supersessionAuditSnapshot(t, run.store)) {
		t.Fatal("denied startup refusal changed audit rows")
	}
}

func TestSupersessionQueryInvalidExistingStoreAndUnknownTrailDoNotWrite(t *testing.T) {
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "query/rejected")
	activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	before := snapshotActivationStore(t, run.store)
	audit := supersessionAuditSnapshot(t, run.store)
	for _, args := range [][]string{
		{"query", "--source", "query/rejected", "--supersession", "--associations=", "oldterm"},
		{"query", "--source", "query/rejected", "--supersession", "--packing-policy", "invalid", "oldterm"},
		{"query", "--source", "query/rejected", "--supersession", ""},
	} {
		code, stdout, stderr := run.runExit(args...)
		if code != 2 || stdout != "" {
			t.Fatalf("invalid existing-store invocation exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		requireUnchanged(t, "invalid existing-store query", before, snapshotActivationStore(t, run.store))
		if !reflect.DeepEqual(audit, supersessionAuditSnapshot(t, run.store)) {
			t.Fatal("invalid query changed audit rows")
		}
	}
	code, stdout, stderr := run.runExit("trail", "--source", "query/rejected", strings.Repeat("f", 64))
	if code != 1 || stdout != "" || !strings.Contains(stderr, "not_found") {
		t.Fatalf("unknown trail exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	requireUnchanged(t, "unknown trail", before, snapshotActivationStore(t, run.store))
	if !reflect.DeepEqual(audit, supersessionAuditSnapshot(t, run.store)) {
		t.Fatal("unknown trail changed audit rows")
	}
	_, disabled := run.run(false, "query", "--source", "query/rejected", "--supersession=false", "oldterm")
	if _, present := disabled["supersession_excluded"]; present || len(hits(disabled)) != 1 {
		t.Fatalf("explicit false did not preserve default path: %v", disabled)
	}
}

func supersessionAuditSnapshot(t *testing.T, path string) map[string]string {
	t.Helper()
	result := make(map[string]string)
	for _, table := range []string{"policy_decisions", "policy_decision_inputs", "source_trails", "source_trail_candidates"} {
		count, digest := tableDigest(t, path, table)
		result[table] = fmt.Sprintf("%d:%s", count, digest)
	}
	return result
}

func TestSupersessionQueryMixedLifecyclePrecedence(t *testing.T) {
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			run, _ := setup(t)
			const sourceName = "query/mixed-lifecycle"
			const foreignName = "query/withdrawn-ancestor"
			source, declaration, _ := activationPins(t, run, sourceName)
			activateQueryDeclaration(t, run, source, nil, &declaration, 1)
			_, predecessor := revisionPin(t, run, sourceName, "oldterm")
			jsonlSync(t, run, foreignName, record("peer", "oldterm foreign evidence"))
			_, foreign := revisionPin(t, run, foreignName, "oldterm")

			// Native JSONL cannot author a multi-source derivation. Prepare a valid
			// canonical descendant through the store, then observe only CLI queries.
			ctx := context.Background()
			store, err := sqlite.Open(ctx, run.store)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			content := []byte("oldterm derived lifecycle evidence")
			inputs := []mousa.DerivationInput{mousa.NewRepresentationDerivationInput(predecessor), mousa.NewRepresentationDerivationInput(foreign)}
			parameters := mousa.SHA256(sha256.Sum256(nil))
			digest := mousa.SHA256(sha256.Sum256(content))
			id, err := mousa.NewRepresentationID(inputs, "supersession-test.mixed", "1", parameters, mousa.UTF8TextMediaType, digest)
			if err != nil {
				t.Fatal(err)
			}
			representation := mousa.Representation{
				Schema: mousa.RepresentationSchema, ID: id, Inputs: inputs,
				ProcessorID: "supersession-test.mixed", ProcessorVersion: "1", ParametersSHA256: parameters,
				MediaType: mousa.UTF8TextMediaType, ContentSHA256: digest, ByteLength: uint64(len(content)),
			}
			if err := store.PutRepresentation(ctx, representation); err != nil {
				t.Fatal(err)
			}
			segments, err := mousa.SegmentUTF8Text(representation, content)
			if err != nil {
				t.Fatal(err)
			}
			for _, segment := range segments {
				if err := store.PutSegment(ctx, segment); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.IndexTextRepresentation(ctx, id, content); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			run.run(false, "withdraw", "--source", foreignName)
			raw, result := supersessionQuery(t, run, sourceName, packing, "oldterm", 8192, true)
			requireSupersessionCounts(t, result, "supersession_excluded", 2, 1, 0, 0, 0)
			if result["lifecycle_excluded"] != float64(1) || strings.Contains(raw, string(content)) || strings.Contains(raw, "oldterm predecessor evidence") {
				t.Fatalf("mixed lifecycle/suppression accounting or text release: %s", raw)
			}
			jsonlSync(t, run, sourceName, record("survivor", "oldterm oversized surviving evidence"))
			raw, result = supersessionQuery(t, run, sourceName, packing, "oldterm", 1, true)
			requireSupersessionCounts(t, result, "budget_omitted", 3, 1, 1, 0, 0)
			if result["lifecycle_excluded"] != float64(1) || strings.Contains(raw, string(content)) || strings.Contains(raw, "oldterm predecessor evidence") {
				t.Fatalf("surviving budget did not take precedence over suppression/lifecycle: %s", raw)
			}
		})
	}
}

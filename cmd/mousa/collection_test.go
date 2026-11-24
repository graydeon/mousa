package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCollectionPersistentScope(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "first", record("a", "harbor pilot")+"\n")
	jsonlSync(t, run, "second", record("b", "harbor pilot")+"\n")
	run.run(false, "access", "--source", "second", "deny")
	run.run(true, "query", "--collection", "fleet", "harbor")
	run.run(false, "collection", "create", "fleet")
	_, empty := run.run(false, "query", "--collection", "fleet", "harbor")
	if len(hits(empty)) != 0 || len(trailOf(empty)["sources"].([]any)) != 0 {
		t.Fatalf("an empty collection released evidence or source decisions: %v", empty)
	}
	run.run(false, "collection", "add", "--source", "first", "fleet")
	run.run(false, "collection", "add", "--source", "second", "fleet")
	_, list := run.run(false, "collection", "list")
	sets := list["collections"].([]any)
	if len(sets) != 1 || len(sets[0].(map[string]any)["sources"].([]any)) != 2 {
		t.Fatalf("collection membership did not persist: %v", list)
	}
	for _, args := range [][]string{
		{"query", "--collection", "fleet", "harbor"},
		{"query", "--source", "first", "--source", "second", "harbor"},
	} {
		_, query := run.run(false, args...)
		passages := hits(query)
		if len(passages) != 1 || passages[0].(map[string]any)["item"] != "a" ||
			len(trailOf(query)["sources"].([]any)) != 2 {
			t.Fatalf("scope did not enforce the mixed source decisions: %v", query)
		}
	}
	run.run(true, "query", "--collection", "fleet", "--source", "first", "harbor")
	run.run(false, "collection", "remove", "--source", "second", "fleet")
	run.run(false, "collection", "remove", "--source", "second", "fleet")
	_, query := run.run(false, "query", "--collection", "fleet", "harbor")
	if len(trailOf(query)["sources"].([]any)) != 1 {
		t.Fatalf("remove did not change the resolved scope: %v", query)
	}
}

func TestVerifyRefusesMissingScopedIndexRow(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("a", "harbor pilot")+"\n")
	run.run(false, "verify")
	rawExec(t, run.store, `DELETE FROM lexical_scope_1 WHERE rowid = 1`)
	err, _ := run.run(true, "verify")
	if !strings.Contains(err, "shard text is missing") {
		t.Fatalf("missing shard row not reported: %s", err)
	}
}

func TestDeniedCorpusDoesNotShiftScores(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "first", record("one", "harbor pilot weather")+"\n"+record("two", "harbor pilot tides")+"\n")
	run.runInput(record("old", "harbor harbor")+"\n", false, "sync", "--source", "second")
	run.run(false, "access", "--source", "second", "deny")
	args := []string{"query", "--source", "first", "--source", "second", "harbor pilot"}
	_, before := run.run(false, args...)
	run.runInput(record("old", "harbor pilot pilot pilot")+"\n"+record("new", "tides tides")+"\n", false, "sync", "--source", "second")
	_, after := run.run(false, args...)
	a, b := trailOf(before), trailOf(after)
	candidates := func(trail map[string]any) string {
		t.Helper()
		data, err := json.Marshal(trail["candidates"])
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	if len(hits(before)) != 2 || a["result_sha256"] != b["result_sha256"] || candidates(a) != candidates(b) {
		t.Fatalf("denied corpus changed score or result digest: before=%v after=%v", a, b)
	}
}

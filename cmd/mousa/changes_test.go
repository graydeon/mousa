package main

import (
	"strconv"
	"testing"
)

func TestChangesAndRecheckAfterWithdrawal(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("ship", "harbor plan")+"\n"+record("old", "retire this item")+"\n")
	run.run(false, "access", "--source", "beta", "--create", "allow")
	_, query := run.run(false, "query", "--full", "--source", "alpha", "harbor")
	trailID := trailIDOf(query)
	_, note := run.run(false, "remember", "--source", "beta", "--from", trailID, "Harbor plan approved.")
	noteID := note["id"].(string)
	_, before := run.run(false, "changes", "--since", "0")
	since := strconv.FormatUint(uint64(before["next_index"].(float64)), 10)
	_, fresh := run.run(false, "recheck", "--source", "beta", noteID)
	if fresh["status"] != "fresh" {
		t.Fatalf("new citation is not fresh: %v", fresh)
	}

	run.runInput(`{"id":"old","deleted":true}`+"\n"+record("new", "add this item")+"\n", false, "sync", "--source", "alpha")
	_, active := run.run(false, "changes", "--since", since, "--source", "alpha")
	entries := active["changes"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["kind"] != "retired" || entries[1].(map[string]any)["kind"] != "added" {
		t.Fatalf("expected exactly the retirement and addition in log order: %v", active)
	}
	run.runInput(record("private", "do not enumerate")+"\n", false, "sync", "--source", "outside")
	run.run(false, "access", "--source", "outside", "deny")
	run.run(false, "withdraw", "--source", "alpha")
	_, changed := run.run(false, "changes", "--since", since)
	entries = changed["changes"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["kind"] != "withdrawn" {
		t.Fatalf("expected withdrawal only, no denied or withdrawn document details: %v", changed)
	}
	_, stale := run.run(false, "recheck", "--source", "beta", noteID)
	_, trail := run.run(false, "recheck", "--trail", trailID)
	for _, result := range []map[string]any{stale, trail} {
		if result["status"] != "stale" {
			t.Fatalf("withdrawal did not mark evidence stale: %v", result)
		}
		staleEntry(t, result, "source_withdrawn", trailID)
	}
	_, replay := run.run(false, "remember", "--source", "beta", "Harbor plan approved.")
	if replay["id"] != noteID || replay["author"] != note["author"] || replay["created_at_usec"] != note["created_at_usec"] {
		t.Fatalf("recheck changed note identity: %v vs %v", replay, note)
	}
}

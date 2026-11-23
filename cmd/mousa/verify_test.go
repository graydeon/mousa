package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestVerifyReadOnlyScopes(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "doc.md", "Harbor pilot checks the seal.")
	run.run(false, "sync", "--allow", root)
	_, query := run.run(false, "query", root, "harbor")
	id := trailIDOf(query)
	read := func() []byte {
		t.Helper()
		data, err := os.ReadFile(run.store)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := read()
	if err := os.Chmod(run.store, 0444); err != nil {
		t.Fatal(err)
	}
	_, full := run.run(false, "verify")
	_, log := run.run(false, "verify", "--log")
	_, trail := run.run(false, "verify", "--trail", id)
	_, stdin := run.runInput(id+"\n", false, "verify", "--stdin")
	for _, result := range []map[string]any{full, log, trail, stdin} {
		if result["record_counts"].(map[string]any)["trail"] != float64(1) {
			t.Fatalf("wrong trail count: %v", result)
		}
	}
	if full["record_counts"].(map[string]any)["segment"] != float64(1) ||
		log["record_counts"].(map[string]any)["segment"] != nil {
		t.Fatalf("store and log checks do not report their own record types: %v and %v", full, log)
	}
	run.runInput(id+"\nnot-an-id\n", true, "verify", "--stdin")
	run.run(true, "verify", "--trail", strings.Repeat("b", 64))
	if !bytes.Equal(before, read()) {
		t.Fatal("verification changed the database bytes")
	}
}

func TestVerifyCorruptionMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, damage string
		trail, log   bool
	}{
		{"trail bytes", `UPDATE trails SET record_json = X'00'`, true, true},
		{"leaf hash", `UPDATE log SET leaf_hash = randomblob(32) WHERE kind = 'trail'`, true, true},
		{"leaf source", `UPDATE log SET source_id = (SELECT id FROM sources WHERE id != log.source_id LIMIT 1) WHERE kind = 'access_event' AND seq = 3`, false, true},
		{"deleted leaf", `DELETE FROM log WHERE kind = 'trail'`, true, true},
		{"access leaves swapped", `UPDATE log SET seq = 100 WHERE seq = 3; UPDATE log SET seq = 3 WHERE seq = 4; UPDATE log SET seq = 4 WHERE seq = 100`, false, true},
		{"item change bytes", `UPDATE item_changes SET record_json = X'00'`, false, true},
		{"item change disagrees with active projection", `UPDATE local_items SET active = 0 WHERE source_id IN (SELECT source_id FROM trail_sources)`, false, true},
		{"trail source lookup", `DELETE FROM trail_sources`, true, false},
		{"trail segment lookup", `DELETE FROM trail_segments`, true, false},
		{"FTS content", `UPDATE segment_lexical_fts_content SET c0 = 'different'`, true, false},
		{"FTS docsize", `UPDATE segment_lexical_fts_docsize SET sz = X'0400' WHERE id = 1`, false, false},
		{"note projection", `UPDATE notes SET item_id = 'wrong'`, false, true},
		{"missing cited segment", `DELETE FROM segments WHERE id IN (SELECT segment_id FROM trail_segments)`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, root := setup(t)
			writeFile(t, root, "doc.md", "Harbor pilot checks the seal.")
			run.run(false, "sync", "--allow", root)
			run.run(false, "access", root, "deny")
			run.run(false, "access", root, "allow")
			_, query := run.run(false, "query", root, "harbor")
			run.run(false, "access", "--source", "memory", "--create", "allow")
			run.run(false, "remember", "--source", "memory", "Seal rules.")
			rawExec(t, run.store, tc.damage)
			// An unrelated status operation must not perform a whole-store scan.
			run.run(false, "status", "--source", "memory")
			stderr, _ := run.run(true, "verify")
			if !strings.Contains(stderr, "integrity") {
				t.Fatalf("unexpected failure: %s", stderr)
			}
			if tc.trail {
				run.run(true, "trail", trailIDOf(query))
				run.run(true, "verify", "--trail", trailIDOf(query))
			}
			if tc.log {
				run.run(true, "verify", "--log")
			}
			if tc.name == "access leaves swapped" {
				run.run(true, "query", root, "harbor")
			}
		})
	}
}

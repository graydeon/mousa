package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestQueryPolicyBudgetAndCanonicalTrail(t *testing.T) {
	run, _ := setup(t)
	const source = "query-policy"
	jsonlSync(t, run, source, strings.Join([]string{
		record("a", "apple"), record("b", "banana filler"), record("c", "orange distractor"),
	}, "\n"))
	original := querySource(t, run, source, "apple banana banana")
	if firstItem(t, original) != "b" {
		t.Fatalf("original policy did not preserve repetition's ranking contribution: %v", original)
	}
	_, dedup := run.run(false, "query", "--policy", "dedup", "--source", source, "apple banana banana")
	if firstItem(t, dedup) != "a" {
		t.Fatalf("explicit dedup policy did not change the ranking: %v", dedup)
	}
	_, packed := run.run(false, "query", "--budget-bytes", "5", "--source", source, "apple banana banana")
	if len(hits(packed)) != 1 || firstItem(t, packed) != "a" || packed["used_bytes"] != float64(5) || packed["budget_omitted"] != float64(1) {
		t.Fatalf("packing did not skip the oversized first candidate and continue: %v", packed)
	}
	hit := hits(packed)[0].(map[string]any)
	if hit["text"] != "apple" || hit["content_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte("apple"))) {
		t.Fatalf("released text disagrees with its content digest: %v", hit)
	}
	_, inspected := run.run(false, "trail", "--source", source, packed["trail_id"].(string))
	history := inspected["historical"].(map[string]any)
	for _, key := range []string{"request_id", "decision_id", "packet_id", "budget_bytes", "used_bytes"} {
		if history[key] != packed[key] {
			t.Fatalf("query %s is not the canonical stored value: query=%v trail=%v", key, packed, inspected)
		}
	}
	selected := 0
	for _, value := range history["candidates"].([]any) {
		candidate := value.(map[string]any)
		if candidate["selected"] == true {
			selected++
			if candidate["segment_id"] != hit["segment_id"] || candidate["content_sha256"] != hit["content_sha256"] || candidate["indexed_now"] != true {
				t.Fatalf("released evidence does not identify the selected stored segment: %v", inspected)
			}
		}
	}
	if selected != 1 {
		t.Fatalf("trail selected %d candidates for a one-segment packet", selected)
	}
	jsonlSync(t, run, source, record("unicode", "é"))
	_, tooSmall := run.run(false, "query", "--budget-bytes", "1", "--source", source, "é")
	if tooSmall["outcome"] != "budget_omitted" || len(hits(tooSmall)) != 0 || tooSmall["used_bytes"] != float64(0) || tooSmall["budget_omitted"] != float64(1) {
		t.Fatalf("byte budget was treated as a character budget or a no-match result: %v", tooSmall)
	}
	_, fits := run.run(false, "query", "--budget-bytes", "2", "--source", source, "é")
	if firstItem(t, fits) != "unicode" || fits["used_bytes"] != float64(2) {
		t.Fatalf("exact UTF-8 byte boundary did not fit: %v", fits)
	}
	missing := querySource(t, run, source, "absentword")
	if missing["outcome"] != "no_matches" || len(hits(missing)) != 0 {
		t.Fatalf("empty lexical result was not distinguished: %v", missing)
	}
	run.run(true, "query", "--policy", "drop-floor", "--source", source, "apple")
	run.run(true, "query", "--budget-bytes", "0", "--source", source, "apple")
}

func TestTrailAuthorizationHistoryAndWithdrawal(t *testing.T) {
	run, root := setup(t)
	const oldText = "Cedar launch is Tuesday."
	const newText = "Cedar launch moved to Friday."
	writeFile(t, root, "schedule.md", oldText)
	run.run(false, "sync", root)
	_, original := run.run(false, "query", root, "cedar")
	oldHit := hits(original)[0].(map[string]any)
	trailID := original["trail_id"].(string)
	jsonlSync(t, run, "peer", record("peer-note", "Cedar peer collection."))
	writeFile(t, root, "schedule.md", newText)
	run.run(false, "sync", root)
	_, revised := run.run(false, "query", root, "cedar")
	if len(hits(revised)) != 1 || hits(revised)[0].(map[string]any)["text"] != newText {
		t.Fatalf("revision query mixed stale or peer-source evidence: %v", revised)
	}
	raw, inspected := run.run(false, "trail", root, trailID)
	history := inspected["historical"].(map[string]any)
	candidate := history["candidates"].([]any)[0].(map[string]any)
	if candidate["segment_id"] != oldHit["segment_id"] || candidate["indexed_now"] != false || strings.Contains(raw, oldText) {
		t.Fatalf("history was confused with current activation or archived text: %v", inspected)
	}
	run.run(false, "access", root, "deny")
	deniedJSON, denied := run.run(false, "query", root, "cedar")
	if denied["outcome"] != "policy_excluded" || len(hits(denied)) != 0 || denied["matched_candidates"] != float64(0) {
		t.Fatalf("source denial did not stop retrieval: %v", denied)
	}
	deniedTrailJSON, deniedTrail := run.run(false, "trail", root, trailID)
	if deniedTrail["authorization_outcome"] != "deny" || deniedTrail["historical"] != nil {
		t.Fatalf("current denial released historical metadata: %v", deniedTrail)
	}
	for _, forbidden := range []string{oldText, newText, oldHit["segment_id"].(string), oldHit["content_sha256"].(string)} {
		if strings.Contains(deniedJSON, forbidden) || strings.Contains(deniedTrailJSON, forbidden) {
			t.Fatalf("denied command released source evidence: %s\n%s", deniedJSON, deniedTrailJSON)
		}
	}
	_, hiddenMissing := run.run(false, "trail", root, strings.Repeat("1", 64))
	if hiddenMissing["authorization_outcome"] != "deny" || hiddenMissing["historical"] != nil {
		t.Fatalf("denied inspection distinguished a nonexistent trail: %v", hiddenMissing)
	}
	run.run(false, "access", root, "allow")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	_, afterRemoval := run.run(false, "trail", root, trailID)
	if afterRemoval["historical"].(map[string]any)["packet_id"] != original["packet_id"] {
		t.Fatalf("historical inspection depended on the source directory's continued existence: %v", afterRemoval)
	}
	run.run(true, "sync", root)
	wrongSource, _ := run.run(true, "trail", "--source", "peer", trailID)
	missingTrail, _ := run.run(true, "trail", "--source", "peer", strings.Repeat("1", 64))
	if wrongSource != missingTrail {
		t.Fatalf("source scoping exposed another source's trail existence: %q versus %q", wrongSource, missingTrail)
	}
	run.run(false, "withdraw", root)
	run.run(false, "access", root, "allow")
	_, withdrawn := run.run(false, "query", root, "cedar")
	if withdrawn["outcome"] != "lifecycle_excluded" || len(hits(withdrawn)) != 0 {
		t.Fatalf("policy allow overrode source withdrawal: %v", withdrawn)
	}
	_, sealedTrail := run.run(false, "trail", root, trailID)
	if sealedTrail["authorization_outcome"] != "deny" || sealedTrail["historical"] != nil {
		t.Fatalf("withdrawal released historical metadata: %v", sealedTrail)
	}
	if firstItem(t, querySource(t, run, "peer", "cedar")) != "peer-note" {
		t.Fatal("withdrawal affected a different source")
	}
}

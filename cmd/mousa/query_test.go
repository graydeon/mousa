package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestQueryBudgetAndCanonicalTrail(t *testing.T) {
	run, _ := setup(t)
	const source = "query-policy"
	jsonlSync(t, run, source, strings.Join([]string{
		record("a", "apple"), record("b", "banana filler"), record("c", "orange distractor"),
	}, "\n"))
	original := querySource(t, run, source, "apple banana banana")
	if firstItem(t, original) != "b" {
		t.Fatalf("repetition did not contribute to the ranking: %v", original)
	}
	_, packed := run.run(false, "query", "--full", "--budget-bytes", "5", "--source", source, "apple banana banana")
	if len(hits(packed)) != 1 || firstItem(t, packed) != "a" || packingOf(packed)["used"] != float64(5) {
		t.Fatalf("packing did not skip the oversized first candidate and continue: %v", packed)
	}
	hit := hits(packed)[0].(map[string]any)
	if hit["text"] != "apple" || hit["content_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte("apple"))) {
		t.Fatalf("released text disagrees with its content digest: %v", hit)
	}
	_, inspected := run.run(false, "trail", trailIDOf(packed))
	if !reflect.DeepEqual(inspected, trailOf(packed)) {
		t.Fatalf("trail read differs from the query's recorded trail: %v vs %v", inspected, packed)
	}
	packedCount := 0
	for _, value := range inspected["candidates"].([]any) {
		candidate := value.(map[string]any)
		if candidate["packed"] == "full" {
			packedCount++
			if candidate["segment_id"] != hit["segment_id"] || candidate["content_sha256"] != hit["content_sha256"] {
				t.Fatalf("released evidence does not identify the packed stored segment: %v", inspected)
			}
		} else if candidate["omitted"] != "budget" {
			t.Fatalf("unpacked candidate lacks a budget omission: %v", candidate)
		}
	}
	if packedCount != 1 {
		t.Fatalf("trail packed %d candidates for a one-segment release", packedCount)
	}
	jsonlSync(t, run, source, record("unicode", "é"))
	_, tooSmall := run.run(false, "query", "--full", "--budget-bytes", "1", "--source", source, "é")
	if len(hits(tooSmall)) != 0 || packingOf(tooSmall)["used"] != float64(0) {
		t.Fatalf("byte budget was treated as a character budget: %v", tooSmall)
	}
	_, fits := run.run(false, "query", "--full", "--budget-bytes", "2", "--source", source, "é")
	if firstItem(t, fits) != "unicode" || packingOf(fits)["used"] != float64(2) {
		t.Fatalf("exact UTF-8 byte boundary did not fit: %v", fits)
	}
	missing := querySource(t, run, source, "absentword")
	if len(hits(missing)) != 0 {
		t.Fatalf("empty lexical result was not empty: %v", missing)
	}
	run.run(true, "query", "--mode", "vector", "--source", source, "apple")
	run.run(true, "query", "--budget-bytes", "0", "--source", source, "apple")
	run.run(true, "query", "--policy", "dedup", "--source", source, "apple")
}

func TestTrailAuthorizationHistoryAndWithdrawal(t *testing.T) {
	run, root := setup(t)
	const oldText = "Cedar launch is Tuesday."
	const newText = "Cedar launch moved to Friday."
	writeFile(t, root, "schedule.md", oldText)
	run.run(false, "sync", "--allow", root)
	_, original := run.run(false, "query", "--full", root, "cedar")
	oldHit := hits(original)[0].(map[string]any)
	trailID := trailIDOf(original)
	jsonlSync(t, run, "peer", record("peer-note", "Cedar peer collection."))
	writeFile(t, root, "schedule.md", newText)
	run.run(false, "sync", root)
	_, revised := run.run(false, "query", "--full", root, "cedar")
	if len(hits(revised)) != 1 || hits(revised)[0].(map[string]any)["text"] != newText {
		t.Fatalf("revision query mixed stale or peer-source evidence: %v", revised)
	}
	raw, inspected := run.run(false, "trail", trailID)
	if !reflect.DeepEqual(inspected, trailOf(original)) {
		t.Fatalf("trail read differs from the recorded query trail: %v", inspected)
	}
	candidate := inspected["candidates"].([]any)[0].(map[string]any)
	if candidate["segment_id"] != oldHit["segment_id"] || strings.Contains(raw, oldText) {
		t.Fatalf("history was confused with current activation or archived text: %v", inspected)
	}
	run.run(false, "access", root, "deny")
	deniedJSON, denied := run.run(false, "query", "--full", root, "cedar")
	if sourceDecision(t, denied)["outcome"] != "deny" || len(hits(denied)) != 0 || len(candidatesOf(denied)) != 0 {
		t.Fatalf("source denial did not stop retrieval: %v", denied)
	}
	for _, forbidden := range []string{oldText, newText, oldHit["segment_id"].(string), oldHit["content_sha256"].(string)} {
		if strings.Contains(deniedJSON, forbidden) {
			t.Fatalf("denied query released source evidence: %s", deniedJSON)
		}
	}
	// The denied trail is still recorded; the owner read returns it unchanged.
	if _, sealed := run.run(false, "trail", trailID); !reflect.DeepEqual(sealed, trailOf(original)) {
		t.Fatalf("denial changed the recorded trail: %v", sealed)
	}
	run.run(true, "trail", strings.Repeat("1", 64))
	run.run(false, "access", root, "allow")
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	_, afterRemoval := run.run(false, "trail", trailID)
	if !reflect.DeepEqual(afterRemoval, trailOf(original)) {
		t.Fatalf("historical read depended on the source directory's existence: %v", afterRemoval)
	}
	run.run(true, "sync", root)
	// Owner trail reads are global: a peer source's trail is readable too.
	peerQuery := querySource(t, run, "peer", "cedar")
	if _, peerTrail := run.run(false, "trail", trailIDOf(peerQuery)); !reflect.DeepEqual(peerTrail, trailOf(peerQuery)) {
		t.Fatalf("peer trail read differs from its recorded trail: %v", peerTrail)
	}
	run.run(false, "withdraw", root)
	run.run(false, "access", root, "allow")
	_, withdrawn := run.run(false, "query", "--full", root, "cedar")
	decision := sourceDecision(t, withdrawn)
	reasons, _ := decision["reasons"].([]any)
	if decision["outcome"] != "deny" || len(hits(withdrawn)) != 0 || !slices.Contains(reasons, any("source_withdrawn")) {
		t.Fatalf("access allow overrode source withdrawal: %v", withdrawn)
	}
	if _, sealedTrail := run.run(false, "trail", trailID); !reflect.DeepEqual(sealedTrail, trailOf(original)) {
		t.Fatalf("withdrawal changed the recorded trail: %v", sealedTrail)
	}
	if firstItem(t, querySource(t, run, "peer", "cedar")) != "peer-note" {
		t.Fatal("withdrawal affected a different source")
	}
}

func TestCLIQueryTrailDigestStable(t *testing.T) {
	run, _ := setup(t)
	const source = "digest-stable"
	jsonlSync(t, run, source, record("doc", "Stable digest evidence about otters."))
	first := querySource(t, run, source, "otters")
	second := querySource(t, run, source, "otters")
	if trailIDOf(first) == trailIDOf(second) {
		t.Fatalf("identical queries shared a trail ID: %v", second)
	}
	if trailOf(first)["result_sha256"] != trailOf(second)["result_sha256"] {
		t.Fatalf("identical requests produced different result digests: %v vs %v", trailOf(first), trailOf(second))
	}
	if !reflect.DeepEqual(hits(first), hits(second)) {
		t.Fatalf("identical queries released different passages: %v vs %v", first, second)
	}
	// The owner read returns each full recorded trail.
	for _, query := range []map[string]any{first, second} {
		_, inspected := run.run(false, "trail", trailIDOf(query))
		if !reflect.DeepEqual(inspected, trailOf(query)) {
			t.Fatalf("owner read did not return the recorded trail: %v", inspected)
		}
	}
}

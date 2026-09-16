package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPassagePolicyResyncAndHistory(t *testing.T) {
	run, root := setup(t)
	const required = "# Recovery\n\nRestart the amber service with `service amber restart`.\n\n"
	text := required + strings.Repeat("Ordinary background material without the recovery keyword.\n\n", 60)
	writeFile(t, root, "runbook.md", text)
	run.run(false, "sync", root)
	_, old := run.run(false, "query", root, "amber")
	_, omitted := run.run(false, "query", "--budget-bytes", "1024", root, "amber")
	if omitted["outcome"] != "budget_omitted" {
		t.Fatalf("fixed evidence unexpectedly fits: %v", omitted)
	}
	_, changed := run.run(false, "sync", "--segment-policy", "passage-v1", root)
	if lenOf(changed, "updated") != 1 {
		t.Fatalf("policy change was not an update: %v", changed)
	}
	_, got := run.run(false, "query", "--budget-bytes", "1024", root, "amber")
	if len(hits(got)) != 1 || !strings.Contains(hits(got)[0].(map[string]any)["text"].(string), required) || got["used_bytes"].(float64) > 1024 {
		t.Fatalf("missing complete bounded passage: %v", got)
	}
	_, replay := run.run(false, "sync", "--segment-policy", "passage-v1", root)
	if lenOf(replay, "unchanged") != 1 {
		t.Fatalf("same policy retry: %v", replay)
	}
	_, history := run.run(false, "trail", root, old["trail_id"].(string))
	for _, row := range history["historical"].(map[string]any)["candidates"].([]any) {
		if row.(map[string]any)["indexed_now"] != false {
			t.Fatalf("old segment still indexed: %v", history)
		}
	}
	if err := os.Remove(filepath.Join(root, "runbook.md")); err != nil {
		t.Fatal(err)
	}
	run.run(false, "sync", "--segment-policy", "passage-v1", root)
	_, deleted := run.run(false, "query", root, "amber")
	if len(hits(deleted)) != 0 {
		t.Fatalf("deleted passage released: %v", deleted)
	}
	writeFile(t, root, "runbook.md", text)
	_, restored := run.run(false, "sync", "--segment-policy", "passage-v1", root)
	if lenOf(restored, "restored") != 1 {
		t.Fatalf("restore: %v", restored)
	}
	_, again := run.run(false, "query", "--budget-bytes", "1024", root, "amber")
	if hits(again)[0].(map[string]any)["segment_id"] != hits(got)[0].(map[string]any)["segment_id"] {
		t.Fatal("restore changed passage identity")
	}
	_, fixed := run.run(false, "sync", root)
	if lenOf(fixed, "updated") != 1 {
		t.Fatalf("default did not restore fixed policy: %v", fixed)
	}
	_, back := run.run(false, "query", root, "amber")
	if hits(back)[0].(map[string]any)["segment_id"] != hits(old)[0].(map[string]any)["segment_id"] {
		t.Fatal("historical fixed identity changed")
	}
	run.run(true, "sync", "--segment-policy", "unknown", root)
}

func TestPassageJSONLAccessAndWithdrawal(t *testing.T) {
	run, _ := setup(t)
	const source = "passage-access"
	const original = "Café secret schedule Tuesday."
	sync := func(text string) map[string]any {
		_, result := run.runInput(record("private-note", text), false, "sync", "--source", source, "--segment-policy", "passage-v1")
		return result
	}
	sync(original)
	_, tiny := run.run(false, "query", "--source", source, "--budget-bytes", "1", "café")
	if tiny["outcome"] != "budget_omitted" || len(hits(tiny)) != 0 {
		t.Fatalf("tiny budget released a fragment: %v", tiny)
	}
	old := querySource(t, run, source, "café")
	hit := hits(old)[0].(map[string]any)
	const revised = "Café secret schedule Friday."
	if lenOf(sync(revised), "updated") != 1 {
		t.Fatal("changed passage not updated")
	}
	current := querySource(t, run, source, "café")
	if len(hits(current)) != 1 || hits(current)[0].(map[string]any)["text"] != revised {
		t.Fatalf("stale passage returned: %v", current)
	}
	run.run(false, "access", "--source", source, "deny")
	for _, withdrawn := range []bool{false, true} {
		if withdrawn {
			run.run(false, "access", "--source", source, "allow")
			run.run(false, "withdraw", "--source", source)
			sync(revised)
		}
		raw, denied := run.run(false, "query", "--source", source, "café")
		historical, trail := run.run(false, "trail", "--source", source, old["trail_id"].(string))
		if len(hits(denied)) != 0 || denied["matched_candidates"] != float64(0) || trail["historical"] != nil {
			t.Fatalf("restricted evidence released: %v %v", denied, trail)
		}
		for _, secret := range []string{original, revised, "private-note", hit["segment_id"].(string), hit["content_sha256"].(string)} {
			if strings.Contains(raw, secret) || strings.Contains(historical, secret) {
				t.Fatalf("restricted metadata released: %q", secret)
			}
		}
	}
}

func TestEvidenceNormalizedLocations(t *testing.T) {
	for _, policy := range []string{"fixed-v1", "passage-v1"} {
		t.Run(policy, func(t *testing.T) {
			run, root := setup(t)
			raw := "\ufeff" + strings.Repeat("Café amber 東京.\r\n\r\n", 300) + "amber end\r"
			normalized := strings.ReplaceAll(strings.ReplaceAll(strings.TrimPrefix(raw, "\ufeff"), "\r\n", "\n"), "\r", "\n")
			writeFile(t, root, "locations.md", raw)
			run.run(false, "sync", "--segment-policy", policy, root)
			_, result := run.run(false, "query", "--budget-bytes", "16384", root, "amber")
			if len(hits(result)) < 2 {
				t.Fatalf("expected multiple selected segments: %v", result)
			}
			var representation any
			for _, value := range hits(result) {
				hit := value.(map[string]any)
				start, startOK := hit["byte_start"].(float64)
				end, endOK := hit["byte_end"].(float64)
				if !startOK || !endOK || start < 0 || end <= start || end > float64(len(normalized)) {
					t.Fatalf("invalid normalized range: %v", hit)
				}
				text := hit["text"].(string)
				if normalized[int(start):int(end)] != text || int(end-start) != len(text) ||
					hit["content_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte(text))) ||
					hit["representation_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte(normalized))) ||
					hit["segment_policy"] != policy {
					t.Fatalf("location disagrees with normalized content: %v", hit)
				}
				if id, ok := hit["representation_id"].(string); !ok || len(id) != 64 {
					t.Fatalf("missing representation identity: %v", hit)
				}
				if representation != nil && representation != hit["representation_id"] {
					t.Fatal("one source revision produced inconsistent representation identities")
				}
				representation = hit["representation_id"]
			}
		})
	}
}

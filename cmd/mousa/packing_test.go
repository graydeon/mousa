package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExactPackingDisplacement(t *testing.T) {
	for _, policy := range []string{"fixed-v1", "passage-v1"} {
		t.Run(policy, func(t *testing.T) {
			run, root := setup(t)
			writeFile(t, root, "copy-a.md", "amber amber")
			writeFile(t, root, "copy-b.md", "amber amber")
			writeFile(t, root, "repair.md", "amber repair code ZX17")
			run.run(false, "sync", "--segment-policy", policy, root)
			_, original := run.run(false, "query", "--budget-bytes", "33", root, "amber")
			if original["used_bytes"] != float64(22) || original["budget_omitted"] != float64(1) {
				t.Fatalf("baseline: %v", original)
			}
			_, explicit := run.run(false, "query", "--packing-policy", "original", "--budget-bytes", "33", root, "amber")
			if !reflect.DeepEqual(hits(original), hits(explicit)) || original["packet_id"] != explicit["packet_id"] || explicit["packing_policy"] != nil || explicit["duplicate_omitted"] != nil {
				t.Fatal("explicit original packing changed the default output contract")
			}
			_, exact := run.run(false, "query", "--packing-policy", "exact-v1", "--budget-bytes", "33", root, "amber")
			if exact["used_bytes"] != float64(33) || exact["duplicate_omitted"] != float64(1) || exact["budget_omitted"] != float64(0) || len(hits(exact)) != 2 {
				t.Fatalf("exact packing: %v", exact)
			}
			if hits(exact)[0].(map[string]any)["segment_id"] != hits(original)[0].(map[string]any)["segment_id"] || hits(exact)[1].(map[string]any)["text"] != "amber repair code ZX17" {
				t.Fatalf("retained wrong evidence: %v", exact)
			}
			_, inspected := run.run(false, "trail", root, exact["trail_id"].(string))
			history := inspected["historical"].(map[string]any)
			if history["schema"] != "mousa.source_trail.v2" || history["packing_policy"] != "exact-v1" {
				t.Fatalf("missing explanation version: %v", history)
			}
			candidates := history["candidates"].([]any)
			if candidates[1].(map[string]any)["omission"] != "duplicate" || candidates[1].(map[string]any)["duplicate_of"] != candidates[0].(map[string]any)["segment_id"] {
				t.Fatalf("missing retained relation: %v", candidates)
			}
			_, tiny := run.run(false, "query", "--packing-policy", "exact-v1", "--budget-bytes", "10", root, "amber")
			if len(hits(tiny)) != 0 || tiny["duplicate_omitted"] != float64(0) || tiny["budget_omitted"] != float64(3) {
				t.Fatalf("oversized copies reserved text: %v", tiny)
			}
			retained := hits(exact)[0].(map[string]any)
			if err := os.Remove(filepath.Join(root, retained["item"].(string))); err != nil {
				t.Fatal(err)
			}
			run.run(false, "sync", "--segment-policy", policy, root)
			_, deleted := run.run(false, "query", "--packing-policy", "exact-v1", "--budget-bytes", "33", root, "amber")
			if deleted["duplicate_omitted"] != float64(0) || deleted["used_bytes"] != float64(33) || hits(deleted)[0].(map[string]any)["segment_id"] == retained["segment_id"] {
				t.Fatalf("deleted retained passage suppressed its surviving copy: %v", deleted)
			}
			_, retired := run.run(false, "trail", root, exact["trail_id"].(string))
			old := retired["historical"].(map[string]any)["candidates"].([]any)
			if old[0].(map[string]any)["indexed_now"] != false || old[1].(map[string]any)["duplicate_of"] != retained["segment_id"] {
				t.Fatal("history lost its retired retained relationship")
			}
			writeFile(t, root, retained["item"].(string), "amber amber")
			run.run(false, "sync", "--segment-policy", policy, root)
			_, restored := run.run(false, "query", "--packing-policy", "exact-v1", "--budget-bytes", "33", root, "amber")
			if restored["packet_id"] != exact["packet_id"] {
				t.Fatal("restoration changed exact packet identity")
			}
			run.run(true, "query", "--packing-policy", "unknown", root, "amber")
		})
	}
}

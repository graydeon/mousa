package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// First-fit packing always suppresses byte-equal copies of a packed segment and
// skips candidates that do not fit the remaining budget.
func TestFirstFitPackingSuppressionAndHistory(t *testing.T) {
	for _, policy := range []string{"fixed-v1", "passage-v1"} {
		t.Run(policy, func(t *testing.T) {
			run, root := setup(t)
			writeFile(t, root, "copy-a.md", "amber amber")
			writeFile(t, root, "copy-b.md", "amber amber")
			writeFile(t, root, "repair.md", "amber repair code ZX17")
			run.run(false, "sync", "--segment-policy", policy, "--allow", root)
			_, result := run.run(false, "query", "--full", "--budget-bytes", "33", root, "amber")
			if packingOf(result)["used"] != float64(33) || len(hits(result)) != 2 {
				t.Fatalf("first-fit packing: %v", result)
			}
			retained := hits(result)[0].(map[string]any)
			if retained["text"] != "amber amber" || hits(result)[1].(map[string]any)["text"] != "amber repair code ZX17" {
				t.Fatalf("retained wrong evidence: %v", result)
			}
			var duplicate map[string]any
			for _, raw := range candidatesOf(result) {
				candidate := raw.(map[string]any)
				if candidate["omitted"] == "duplicate" {
					duplicate = candidate
				}
			}
			if duplicate == nil || duplicate["duplicate_of"] != retained["segment_id"] {
				t.Fatalf("byte-equal copy was not suppressed against the packed segment: %v", result)
			}
			_, inspected := run.run(false, "trail", trailIDOf(result))
			if !reflect.DeepEqual(inspected, trailOf(result)) {
				t.Fatalf("trail read differs from the recorded trail: %v", inspected)
			}
			if err := os.Remove(filepath.Join(root, retained["item"].(string))); err != nil {
				t.Fatal(err)
			}
			run.run(false, "sync", "--segment-policy", policy, root)
			_, deleted := run.run(false, "query", "--full", "--budget-bytes", "33", root, "amber")
			if packingOf(deleted)["used"] != float64(33) || hits(deleted)[0].(map[string]any)["segment_id"] == retained["segment_id"] {
				t.Fatalf("deleted packed passage suppressed its surviving copy: %v", deleted)
			}
			if _, retired := run.run(false, "trail", trailIDOf(result)); !reflect.DeepEqual(retired, trailOf(result)) {
				t.Fatal("deletion changed the recorded trail")
			}
			writeFile(t, root, retained["item"].(string), "amber amber")
			run.run(false, "sync", "--segment-policy", policy, root)
			_, restored := run.run(false, "query", "--full", "--budget-bytes", "33", root, "amber")
			if !reflect.DeepEqual(hits(restored), hits(result)) {
				t.Fatal("restoration changed the released passages")
			}
			if trailOf(restored)["result_sha256"] == trailOf(result)["result_sha256"] {
				t.Fatal("restoration did not advance the source history")
			}
			_, repeated := run.run(false, "query", "--full", "--budget-bytes", "33", root, "amber")
			if trailOf(restored)["result_sha256"] != trailOf(repeated)["result_sha256"] {
				t.Fatal("an unchanged restored source did not repeat its result digest")
			}
			run.run(true, "query", "--mode", "unknown", root, "amber")
		})
	}
}

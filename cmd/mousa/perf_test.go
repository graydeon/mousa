package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLocalSlicePerformance is the small fixed performance workload: it
// measures import, no-op sync, update, delete, startup-adjacent status, and
// query latency plus store size on a fixed synthetic corpus through the
// supported CLI. It is a regression workload, not a benchmark result; numbers
// are recorded per run and asserted only against generous functional bounds.

func writeCorpus(t *testing.T, root string, count int, tag string) {
	t.Helper()
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("doc-%03d.md", i)
		content := fmt.Sprintf("# Document %d (%s)\n\n%s\n", i, tag,
			strings.Repeat(fmt.Sprintf("Mousa segment %d talks about walruses and quokkas in habitat %d.\n", i, i%7), 3))
		writeFile(t, root, name, content)
	}
}

func timeSync(t *testing.T, run testRun, root string) time.Duration {
	t.Helper()
	started := time.Now()
	run.run(false, "sync", root)
	return time.Since(started)
}

func timeQuery(t *testing.T, run testRun, root, query string) (time.Duration, map[string]any) {
	t.Helper()
	started := time.Now()
	_, report := run.run(false, "query", root, query)
	return time.Since(started), report
}

func TestLocalSlicePerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("performance workload skipped in -short")
	}
	run, root := setup(t)
	const docs = 60
	writeCorpus(t, root, docs, "v1")

	importTime := timeSync(t, run, root)
	noOpTime := timeSync(t, run, root)

	// Update: rewrite half the corpus.
	for i := 0; i < docs/2; i++ {
		path := filepath.Join(root, fmt.Sprintf("doc-%03d.md", i))
		content := fmt.Sprintf("# Document %d (v2)\n\n%s\n", i,
			strings.Repeat(fmt.Sprintf("Revised segment %d now mentions pangolins in habitat %d.\n", i, i%7), 3))
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	updateTime := timeSync(t, run, root)

	// Delete: remove a quarter of the corpus.
	for i := docs / 2; i < docs*3/4; i++ {
		if err := os.Remove(filepath.Join(root, fmt.Sprintf("doc-%03d.md", i))); err != nil {
			t.Fatal(err)
		}
	}
	deleteTime := timeSync(t, run, root)

	queryTime, report := timeQuery(t, run, root, "pangolins")
	_, statusReport := run.run(false, "status", root)

	var storeBytes float64
	info, err := os.Stat(run.store)
	if err == nil {
		storeBytes = float64(info.Size())
	} else if _, ok := report["store_bytes"]; ok {
		storeBytes = report["store_bytes"].(float64)
	}

	results := map[string]any{
		"documents":         docs,
		"import_seconds":    importTime.Seconds(),
		"noop_sync_seconds": noOpTime.Seconds(),
		"update_seconds":    updateTime.Seconds(),
		"delete_seconds":    deleteTime.Seconds(),
		"query_seconds":     queryTime.Seconds(),
		"query_matches":     report["total_matches"],
		"query_used_bytes":  report["used_bytes"],
		"store_bytes":       storeBytes,
		"status":            statusReport,
	}
	encoded, _ := json.MarshalIndent(results, "", "  ")
	t.Logf("performance workload:\n%s", encoded)

	// Generous functional bounds (not performance claims): the fixed workload
	// must complete within these on the slowest supported hardware.
	const bound = 120 * time.Second
	for name, d := range map[string]time.Duration{
		"import": importTime, "noop": noOpTime, "update": updateTime, "delete": deleteTime, "query": queryTime,
	} {
		if d > bound {
			t.Fatalf("%s took %s, over the %s functional bound", name, d, bound)
		}
	}
	if lenOf(report, "evidence") == 0 {
		t.Fatalf("performance workload query returned no evidence: %v", report)
	}
}

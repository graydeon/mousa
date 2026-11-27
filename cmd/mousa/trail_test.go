package main

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestTrailListFiltersReleasedSegments(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("a", "harbor pilot")+"\n"+record("b", "harbor tides")+"\n")
	jsonlSync(t, run, "beta", record("c", "harbor bell")+"\n")
	_, first := run.run(false, "query", "--full", "--budget-bytes", "12", "--source", "alpha", "harbor")
	_, other := run.run(false, "query", "--full", "--source", "beta", "harbor")
	_, last := run.run(false, "query", "--full", "--source", "alpha", "harbor")
	var omitted string
	for _, raw := range trailOf(first)["candidates"].([]any) {
		candidate := raw.(map[string]any)
		if candidate["packed"] == "none" {
			omitted = candidate["segment_id"].(string)
		}
	}
	if omitted == "" || len(hits(first)) != 1 || len(hits(last)) != 2 {
		t.Fatalf("fixture does not cover an omitted segment: %v / %v", first, last)
	}

	list := func(args []string, want ...map[string]any) uint64 {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), run.binary, append([]string{"-store", run.store, "trail", "list"}, args...)...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("trail list %v: %v", args, err)
		}
		lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if len(lines) != len(want) {
			t.Fatalf("trail list %v returned %d lines, want %d: %s", args, len(lines), len(want), out)
		}
		var firstSeq, prev uint64
		for i, line := range lines {
			var row struct {
				ID           string `json:"id"`
				Seq          uint64 `json:"seq"`
				ResultSHA256 string `json:"result_sha256"`
			}
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatal(err)
			}
			if row.ID != trailIDOf(want[i]) || row.ResultSHA256 != trailOf(want[i])["result_sha256"] || row.Seq <= prev {
				t.Fatalf("trail list %v returned wrong row at %d: %s", args, i, line)
			}
			if i == 0 {
				firstSeq = row.Seq
			}
			prev = row.Seq
		}
		return firstSeq
	}
	list(nil, first, other, last)
	firstSeq := list([]string{"--source", "alpha"}, first, last)
	list([]string{"--source", "alpha", "--since", strconv.FormatUint(firstSeq, 10)}, last)
	list([]string{"--segment", omitted}, last)
	run.run(true, "trail", "list", "--since", "-1")
}

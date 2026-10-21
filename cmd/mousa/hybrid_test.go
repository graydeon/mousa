package main

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestNativeHybridDirectoryAndJSONL(t *testing.T) {
	run, root := setup(t)
	const vector = `{"schema":"mousa.vector_query.v1","values":[1,0]}`
	for _, directory := range []bool{false, true} {
		selector := []string{"--source", fmt.Sprintf("hybrid/%v", directory)}
		if directory {
			selector = []string{root}
			writeFile(t, root, "a.md", "Drain sockets before stopping the host.")
			writeFile(t, root, "b.md", "Radishes prefer damp soil.")
			writeFile(t, root, "c.md", "Drain sockets.")
			run.run(false, "sync", root)
		} else {
			run.runInput("{\"id\":\"a.md\",\"text\":\"Drain sockets before stopping the host.\"}\n{\"id\":\"b.md\",\"text\":\"Radishes prefer damp soil.\"}\n{\"id\":\"c.md\",\"text\":\"Drain sockets.\"}\n", false, append([]string{"sync"}, selector...)...)
		}
		_, exported := run.run(false, append([]string{"vector", "inputs"}, selector...)...)
		embeddings := []map[string]any{}
		for _, raw := range exported["passages"].([]any) {
			p := raw.(map[string]any)
			if p["item"] == "c.md" {
				continue
			}
			e := map[string]any{"values": []float64{0, 1}}
			if p["item"] == "b.md" {
				e["values"] = []float64{1, 0}
			}
			for _, key := range []string{"source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256"} {
				e[key] = p[key]
			}
			embeddings = append(embeddings, e)
		}
		input, _ := json.Marshal(map[string]any{"schema": "mousa.vector_import.v1", "space": map[string]any{"schema": "mousa.vector_space.v1", "provider": "synthetic", "model": "hybrid-arithmetic", "version": "1", "dimensions": 2, "metric": "cosine"}, "embeddings": embeddings})
		_, imported := run.runInput(string(input), false, append([]string{"vector", "import"}, selector...)...)
		space := imported["space_id"].(string)
		args := append(append([]string{"query", "--hybrid-space", space, "--budget-bytes", "40"}, selector...), "Drain")
		_, got := run.runInput(vector, false, args...)
		evidence := got["evidence"].([]any)
		h := got["hybrid"].(map[string]any)
		if got["mode"] != "hybrid" || h["eligible"] != float64(3) || h["indexed"] != float64(2) || got["missing"] != float64(1) || len(evidence) != 1 || evidence[0].(map[string]any)["item"] != "a.md" {
			t.Fatalf("hybrid selection: %v", got)
		}
		first := h["candidates"].([]any)[0].(map[string]any)
		if first["lexical"].(map[string]any)["rank"] != float64(2) || first["vector"].(map[string]any)["rank"] != float64(2) || math.Abs(first["rrf"].(float64)-0.03225806451612903) > 1e-16 {
			t.Fatalf("RRF: %v", h)
		}
		_, lexical := run.run(false, append(append([]string{"query", "--budget-bytes", "40"}, selector...), "Drain")...)
		if hits(lexical)[0].(map[string]any)["item"] == "a.md" {
			t.Fatal("fixture did not change lexical selection")
		}
		_, onlyVector := run.runInput(vector, false, append([]string{"query", "--vector-space", space, "--budget-bytes", "40"}, selector...)...)
		if onlyVector["evidence"].([]any)[0].(map[string]any)["item"] != "b.md" {
			t.Fatal("fixture did not change vector selection")
		}
		_, again := run.runInput(vector, false, args...)
		if again["packet_id"] != got["packet_id"] {
			t.Fatal("restart changed packet")
		}
		for _, packing := range []string{"original", "exact-v1"} {
			options := append(append([]string{"query", "--hybrid-space", space, "--packing-policy", packing, "--policy", "dedup"}, selector...), "Drain Drain")
			_, packed := run.runInput(vector, false, options...)
			if len(packed["hybrid"].(map[string]any)["terms"].([]any)) != 1 {
				t.Fatal("dedup not used")
			}
		}
		for _, bad := range []string{`{"schema":"mousa.vector_query.v1","values":[1]}`, `{"schema":"mousa.vector_query.v1","values":[1,0],"extra":true}`, `{"schema":"mousa.vector_query.v1","values":[1,0],"values":[0,1]}`, `{"schema":"mousa.vector_query.v1","values":[0,0]}`, `[]`, `{"schema":`, strings.Repeat(" ", 128<<10) + vector} {
			run.runInput(bad, true, args...)
		}
		for _, option := range []string{"--supersession=false", "--associations=unused", "--vector-space=" + space} {
			raw, _ := run.runInput(vector, true, append(append([]string{"query", "--hybrid-space", space, option}, selector...), "Drain")...)
			if !strings.Contains(raw, "cannot be combined") {
				t.Fatalf("combination: %s", raw)
			}
		}
		run.runInput(vector, true, append(append([]string{"query", "--hybrid-space", strings.Repeat("f", 64)}, selector...), "Drain")...)
		for _, text := range []string{"!!!", strings.Repeat("x", 4096)} {
			run.runInput(vector, true, append(append([]string{"query", "--hybrid-space", space}, selector...), text)...)
		}
		_, noMatch := run.runInput(vector, false, append(append([]string{"query", "--hybrid-space", space}, selector...), "nonmatching")...)
		if len(noMatch["evidence"].([]any)) != 2 {
			t.Fatal("empty lexical branch suppressed vectors")
		}
		_, history := run.run(false, append(append([]string{"trail"}, selector...), got["trail_id"].(string))...)
		historical := history["historical"].(map[string]any)
		if historical["schema"] != "mousa.source_trail.v6" || !reflect.DeepEqual(historical["hybrid"], h) {
			t.Fatalf("history: %v", history)
		}
		if directory {
			writeFile(t, root, "a.md", "Drain replacement.")
			run.run(false, "sync", root)
		} else {
			run.runInput("{\"id\":\"a.md\",\"text\":\"Drain replacement.\"}\n", false, append([]string{"sync"}, selector...)...)
		}
		raw, changed := run.runInput(vector, false, args...)
		if strings.Contains(raw, "before stopping") || changed["hybrid"].(map[string]any)["indexed"] != float64(1) {
			t.Fatalf("stale release: %s", raw)
		}
		_, old := run.run(false, append(append([]string{"trail"}, selector...), got["trail_id"].(string))...)
		if !reflect.DeepEqual(old["historical"].(map[string]any)["hybrid"], h) {
			t.Fatal("history reinterpreted current state")
		}
		run.run(false, append(append([]string{"access"}, selector...), "deny")...)
		_, denied := run.runInput(vector, false, args...)
		dh := denied["hybrid"].(map[string]any)
		if len(denied["evidence"].([]any)) != 0 || dh["eligible"] != float64(0) || dh["indexed"] != float64(0) || len(dh["candidates"].([]any)) != 0 {
			t.Fatalf("denied facts leaked: %v", denied)
		}
		_, hidden := run.run(false, append(append([]string{"trail"}, selector...), got["trail_id"].(string))...)
		if hidden["historical"] != nil {
			t.Fatal("denied history disclosed")
		}
	}
}

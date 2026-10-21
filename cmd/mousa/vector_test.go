package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNativeVectorWorkflowDirectoryAndJSONL(t *testing.T) {
	run, root := setup(t)
	for _, directory := range []bool{false, true} {
		name := fmt.Sprint(directory)
		selector := []string{"--source", "vector/" + name}
		if directory {
			selector = []string{root}
			writeFile(t, root, "runbook.md", "Drain sockets before stopping the host.\r\n")
			writeFile(t, root, "garden.md", "Radishes prefer damp soil.\n")
			run.run(false, "sync", root)
		} else {
			run.runInput("{\"id\":\"runbook.md\",\"text\":\"Drain sockets before stopping the host.\\r\\n\"}\n{\"id\":\"garden.md\",\"text\":\"Radishes prefer damp soil.\\n\"}\n", false, append([]string{"sync"}, selector...)...)
		}
		_, lexical := run.run(false, append(append([]string{"query"}, selector...), "unplug")...)
		if len(hits(lexical)) != 0 {
			t.Fatal("synthetic example unexpectedly had lexical overlap")
		}
		_, exported := run.run(false, append([]string{"vector", "inputs"}, selector...)...)
		passages := exported["passages"].([]any)
		var supplied []map[string]any
		for _, raw := range passages {
			p := raw.(map[string]any)
			values := []float64{0, 1}
			if p["item"] == "runbook.md" {
				values = []float64{1, 0}
				if p["text"] != "Drain sockets before stopping the host.\n" {
					t.Fatalf("not normalized bytes: %v", p)
				}
			}
			e := map[string]any{"values": values}
			for _, key := range []string{"source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256"} {
				e[key] = p[key]
			}
			supplied = append(supplied, e)
		}
		input := map[string]any{"schema": "mousa.vector_import.v1", "space": map[string]any{"schema": "mousa.vector_space.v1", "provider": "synthetic", "model": "two-axis-fixture", "version": "1", "dimensions": 2, "metric": "cosine"}, "embeddings": supplied}
		encoded, _ := json.Marshal(input)
		_, imported := run.runInput(string(encoded), false, append([]string{"vector", "import"}, selector...)...)
		space := imported["space_id"].(string)
		queryArgs := append([]string{"query", "--vector-space", space, "--budget-bytes", "40"}, selector...)
		_, result := run.runInput(`{"schema":"mousa.vector_query.v1","values":[1,0]}`, false, queryArgs...)
		evidence := result["evidence"].([]any)
		if result["mode"] != "vector" || result["eligible"] != float64(2) || result["indexed"] != float64(2) || result["used_bytes"] != float64(40) || len(evidence) != 1 || evidence[0].(map[string]any)["item"] != "runbook.md" || evidence[0].(map[string]any)["cosine"] != float64(1) {
			t.Fatalf("vector result: %v", result)
		}
		if _, ok := result["expression"]; ok {
			t.Fatal("vector mode manufactured FTS expression")
		}
		if _, ok := evidence[0].(map[string]any)["bm25_score"]; ok {
			t.Fatal("cosine mislabeled as BM25")
		}
		_, history := run.run(false, append(append([]string{"trail"}, selector...), result["trail_id"].(string))...)
		h := history["historical"].(map[string]any)
		v := h["vector"].(map[string]any)
		if h["schema"] != "mousa.source_trail.v5" || v["query_vector_id"] != result["query_vector_id"] || h["packet_id"] != result["packet_id"] {
			t.Fatalf("trail bindings: %v", history)
		}
		// Every invocation reopens the store: this also checks restart persistence.
		_, again := run.runInput(`{"schema":"mousa.vector_query.v1","values":[1,0]}`, false, queryArgs...)
		if again["packet_id"] != result["packet_id"] {
			t.Fatal("restart changed packet")
		}
		if directory {
			writeFile(t, root, "runbook.md", "Use the replacement switch.\n")
			run.run(false, "sync", root)
		} else {
			run.runInput("{\"id\":\"runbook.md\",\"text\":\"Use the replacement switch.\\n\"}\n", false, append([]string{"sync"}, selector...)...)
		}
		_, stale := run.runInput(`{"schema":"mousa.vector_query.v1","values":[1,0]}`, false, queryArgs...)
		if stale["indexed"] != float64(1) || stale["missing"] != float64(1) || strings.Contains(fmt.Sprint(stale), "Drain sockets") {
			t.Fatalf("stale vector released: %v", stale)
		}
		run.runInput(string(encoded), true, append([]string{"vector", "import"}, selector...)...)
		run.run(false, append(append([]string{"access"}, selector...), "deny")...)
		raw, denied := run.run(false, append([]string{"vector", "inputs"}, selector...)...)
		if strings.Contains(raw, "Drain sockets") || len(denied["passages"].([]any)) != 0 {
			t.Fatal("denied export leaked")
		}
		_, deniedQuery := run.runInput(`{"schema":"mousa.vector_query.v1","values":[1,0]}`, false, queryArgs...)
		if len(deniedQuery["evidence"].([]any)) != 0 || deniedQuery["eligible"] != float64(0) {
			t.Fatal("denied query disclosed source")
		}
		_, deniedHistory := run.run(false, append(append([]string{"trail"}, selector...), result["trail_id"].(string))...)
		if deniedHistory["historical"] != nil {
			t.Fatal("denied history leaked")
		}
		run.runInput(`{"schema":"mousa.vector_query.v1","values":[1,0]}`, true,
			append(append([]string{"query", "--vector-space="}, selector...), "unplug")...)
		for _, option := range []string{"--supersession", "--associations=unused", "--policy=original"} {
			args := append([]string{"query", "--vector-space", space, option}, selector...)
			message, _ := run.runInput(`{"schema":"mousa.vector_query.v1","values":[1,0]}`, true, args...)
			if !strings.Contains(message, "cannot be combined") {
				t.Fatalf("wrong combination rejection: %s", message)
			}
		}
	}
}

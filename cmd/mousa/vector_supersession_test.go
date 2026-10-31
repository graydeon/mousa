package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestVectorHybridSupersessionNativeAndMCP(t *testing.T) {
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "alpha")
	space := prepareMCPVectors(t, run, map[string][]float64{"doc": {1, 0}})
	client := startMCPClient(t, run, "cli", false)
	for _, mode := range []string{"vector", "hybrid"} {
		args := mcpRetrievalArgs(space, mode, "original", 8192)
		if mode == "hybrid" {
			args["query"] = "oldterm"
		}
		args["supersession"] = true
		result := client.call("mousa_"+mode+"_query", args, "")
		if result["supersession_excluded"] != float64(0) {
			t.Fatalf("no-history count: %v", result)
		}
		args["supersession"] = false
		optedOut := client.call("mousa_"+mode+"_query", args, "")
		delete(args, "supersession")
		ordinary := client.call("mousa_"+mode+"_query", args, "")
		if !reflect.DeepEqual(ordinary["evidence"], optedOut["evidence"]) || ordinary["packet_id"] != optedOut["packet_id"] || optedOut["supersession_excluded"] != nil {
			t.Fatal("false changed legacy selection")
		}
	}
	active := activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	var saved map[string]any
	for _, mode := range []string{"vector", "hybrid"} {
		for _, packing := range []string{"original", "exact-v1"} {
			args := mcpRetrievalArgs(space, mode, packing, 8192)
			if mode == "hybrid" {
				args["query"] = "oldterm"
			}
			args["supersession"] = true
			got := client.call("mousa_"+mode+"_query", args, "")
			if got["outcome"] != "supersession_excluded" || got["supersession_excluded"] != float64(1) || len(hits(got)) != 0 || got["budget_omitted"] != float64(0) || got["eligible"] != float64(3) || got["indexed"] != float64(1) {
				t.Fatalf("all withheld: %v", got)
			}
			inspection := map[string]any{"source": "alpha", "trail_id": got["trail_id"]}
			for _, ss := range []bool{false, true} {
				for _, details := range []bool{false, true} {
					inspection["supersession"], inspection["retrieval_details"] = ss, details
					h := client.call("mousa_trail", inspection, "")["historical"].(map[string]any)
					if h["schema"] != "mousa.source_trail.v9" || (h["supersession"] != nil) != ss || (h[mode] != nil) != details {
						t.Fatalf("independent projections: %v", h)
					}
					if details && !reflect.DeepEqual(h[mode], got[mode]) {
						t.Fatal("history changed producing facts")
					}
					if ss {
						saved = h
					}
				}
			}
		}
	}
	activateQueryDeclaration(t, run, source, &active, nil, 2)
	old := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": saved["id"], "supersession": true}, "")["historical"].(map[string]any)
	if !reflect.DeepEqual(old["supersession"], saved["supersession"]) {
		t.Fatal("deactivation reinterpreted history")
	}
	run.run(false, "access", "--source", "alpha", "deny")
	for _, mode := range []string{"vector", "hybrid"} {
		args := mcpRetrievalArgs(space, mode, "original", 8192)
		args["supersession"] = true
		got := client.call("mousa_"+mode+"_query", args, "")
		if got["outcome"] != "policy_excluded" || got["supersession_excluded"] != float64(0) || got["eligible"] != float64(0) {
			t.Fatalf("denial: %v", got)
		}
	}
}

func TestNativeVectorHybridSupersessionSelectorsAndSurvivors(t *testing.T) {
	run, root := setup(t)
	for _, directory := range []bool{false, true} {
		var source mousa.Source
		selector := []string{"--source", "native-vector-supersession"}
		if directory {
			writeFile(t, root, "old.md", "Drain precursor evidence")
			writeFile(t, root, "new.md", "Separate successor evidence")
			writeFile(t, root, "twin.md", "Drain precursor evidence")
			run.run(false, "sync", root)
			var err error
			source, _, err = directorySource(root)
			if err != nil {
				t.Fatal(err)
			}
			selector = []string{root}
		} else {
			jsonlSync(t, run, selector[1], record("old.md", "Drain precursor evidence")+"\n"+record("new.md", "Separate successor evidence")+"\n"+record("twin.md", "Drain precursor evidence"))
			var err error
			source, err = streamSource(selector[1])
			if err != nil {
				t.Fatal(err)
			}
		}
		_, exported := run.run(false, append([]string{"vector", "inputs"}, selector...)...)
		pins := map[string]map[string]any{}
		embeddings := []map[string]any{}
		for _, raw := range exported["passages"].([]any) {
			p := raw.(map[string]any)
			pins[p["item"].(string)] = p
			if p["item"] == "new.md" {
				continue
			}
			e := map[string]any{"values": []float64{0, 1}}
			if p["item"] == "old.md" {
				e["values"] = []float64{1, 0}
			}
			for _, key := range []string{"source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256"} {
				e[key] = p[key]
			}
			embeddings = append(embeddings, e)
		}
		input, err := json.Marshal(map[string]any{"schema": "mousa.vector_import.v1", "space": map[string]any{"schema": "mousa.vector_space.v1", "provider": "synthetic", "model": "arithmetic", "version": "1", "dimensions": 2, "metric": "cosine"}, "embeddings": embeddings})
		if err != nil {
			t.Fatal(err)
		}
		_, imported := run.runInput(string(input), false, append([]string{"vector", "import"}, selector...)...)
		predecessor, err := mousa.ParseRepresentationID(pins["old.md"]["representation_id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		successor, err := mousa.ParseRepresentationID(pins["new.md"]["representation_id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		declaration := storedDeclaration(t, run, declarationJSON(t, source, "old.md", predecessor, "new.md", successor, "example.operator", "replacement"))
		active := activateQueryDeclaration(t, run, source, nil, &declaration, 1)
		for _, mode := range []string{"vector", "hybrid"} {
			for _, packing := range []string{"original", "exact-v1"} {
				for _, token := range []bool{false, true} {
					t.Run(fmt.Sprintf("directory=%v/%s/%s/token=%v", directory, mode, packing, token), func(t *testing.T) {
						var tokenArgs []string
						version := "mousa.source_trail.v9"
						if token {
							tokenArgs = append([]string{"--context-tokens", "4096"}, mcpTokenStartup(t)...)
							version = "mousa.source_trail.v10"
						}
						var legacy map[string]any
						for _, opt := range []string{"", "false", "true"} {
							args := []string{"query", "--" + mode + "-space", imported["space_id"].(string), "--packing-policy", packing, "--budget-bytes", "24"}
							args = append(args, tokenArgs...)
							if opt != "" {
								args = append(args, "--supersession="+opt)
							}
							args = append(args, selector...)
							if mode == "hybrid" {
								args = append(args, "unmatched")
							}
							_, got := run.runInput(`{"schema":"mousa.vector_query.v1","values":[1,0]}`, false, args...)
							if opt == "" {
								legacy = got
							}
							want := "old.md"
							if opt == "true" {
								want = "twin.md"
								if got["supersession_excluded"] != float64(1) {
									t.Fatalf("count: %v", got)
								}
							} else if got["supersession_excluded"] != nil {
								t.Fatal("legacy property grew")
							}
							if len(hits(got)) != 1 {
								t.Fatalf("selection: %v", got)
							}
							hit := hits(got)[0].(map[string]any)
							if token {
								assertMCPTokenRendering(t, got)
								if hit["segment"].(map[string]any)["id"] != pins[want]["segment_id"] {
									t.Fatalf("token survivor: %v", got)
								}
							} else if hit["item"] != want {
								t.Fatalf("byte survivor: %v", got)
							}
							if opt == "false" && (!reflect.DeepEqual(got["evidence"], legacy["evidence"]) || got["packet_id"] != legacy["packet_id"]) {
								t.Fatal("false changed canonical selection")
							}
							historyArgs := append([]string{"trail"}, selector...)
							_, history := run.run(false, append(historyArgs, got["trail_id"].(string))...)
							if opt == "true" && history["historical"].(map[string]any)["schema"] != version {
								t.Fatalf("native history lost %s", version)
							}
						}
					})
				}
			}
		}
		activateQueryDeclaration(t, run, source, &active, nil, 2)
	}
}

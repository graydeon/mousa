package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPRetrievalDiscoveryAndDispatch(t *testing.T) {
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", false)
	tools, err := client.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mousa_vector_query", "mousa_hybrid_query"} {
		found := false
		for _, tool := range tools.Tools {
			if tool.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("tools/list omits %s", name)
		}
		response, err := client.session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil {
			t.Errorf("%s not dispatched: %v", name, err)
			continue
		}
		if !response.IsError {
			t.Errorf("%s accepted missing arguments", name)
		}
	}
	// Both and neither payloads must fail the discovered exactly-one contract.
	for _, name := range []string{"mousa_vector_query", "mousa_hybrid_query"} {
		for _, envelope := range []map[string]any{
			{"schema": mcpResultVersion, "operation": name, "source": "alpha"},
			{"schema": mcpResultVersion, "operation": name, "source": "alpha", "result": nil, "error": map[string]any{"code": "x", "message": "x", "completed_items": 0}},
		} {
			if err := client.schemas[name].Validate(envelope); err == nil {
				t.Fatalf("invalid envelope accepted: %v", envelope)
			}
		}
	}
}

func prepareMCPVectors(t *testing.T, run testRun, values map[string][]float64) string {
	t.Helper()
	_, exported := run.run(false, "vector", "inputs", "--source", "alpha")
	embeddings := []map[string]any{}
	for _, raw := range exported["passages"].([]any) {
		p := raw.(map[string]any)
		v, ok := values[p["item"].(string)]
		if !ok {
			continue
		}
		e := map[string]any{"values": v}
		for _, key := range []string{"source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256"} {
			e[key] = p[key]
		}
		embeddings = append(embeddings, e)
	}
	input := map[string]any{"schema": "mousa.vector_import.v1", "space": map[string]any{"schema": "mousa.vector_space.v1", "provider": "synthetic", "model": "mcp-arithmetic", "version": "1", "dimensions": 2, "metric": "cosine"}, "embeddings": embeddings}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	_, imported := run.runInput(string(encoded), false, "vector", "import", "--source", "alpha")
	return imported["space_id"].(string)
}

func mcpRetrievalArgs(space, mode, packing string, budget int) map[string]any {
	args := map[string]any{"source": "alpha", "space_id": space, "values": []float64{1, 0}, "budget_bytes": budget}
	if mode == "hybrid" {
		args["query"], args["policy"] = "Drain", "original"
	}
	if packing != "" {
		args["packing_policy"] = packing
	}
	return args
}

func assertMCPIdentities(t *testing.T, value any) {
	t.Helper()
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if strings.HasSuffix(key, "_id") || strings.HasSuffix(key, "_sha256") {
				if _, ok := item.(string); !ok || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(fmt.Sprint(item)) {
					t.Fatalf("%s is not canonical hex: %v", key, item)
				}
			}
			assertMCPIdentities(t, item)
		}
	case []any:
		for _, item := range v {
			assertMCPIdentities(t, item)
		}
	}
}

func TestMCPRetrievalRankingAndRecordedHistory(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", strings.Join([]string{record("a.md", "Drain sockets before stopping the host."), record("b.md", "Radishes prefer damp soil."), record("c.md", "Drain sockets.")}, "\n"))
	space := prepareMCPVectors(t, run, map[string][]float64{"a.md": {0, 1}, "b.md": {1, 0}})
	client := startMCPClient(t, run, "cli", false)
	type savedQuery struct {
		mode   string
		result map[string]any
		facts  map[string]any
	}
	var saved []savedQuery
	for _, mode := range []string{"vector", "hybrid"} {
		var defaultPacket any
		for _, packing := range []string{"", "original", "exact-v1"} {
			result := client.call("mousa_"+mode+"_query", mcpRetrievalArgs(space, mode, packing, 40), "")
			if result["eligible"] != float64(3) || result["indexed"] != float64(2) || result["missing"] != float64(1) || result["packing_policy"] != "original" && packing != "exact-v1" {
				t.Fatalf("coverage/default: %v", result)
			}
			if packing == "" {
				defaultPacket = result["packet_id"]
			}
			if packing == "original" && result["packet_id"] != defaultPacket {
				t.Fatal("omitted packing changed packet")
			}
			e := result["evidence"].([]any)
			if len(e) != 1 {
				t.Fatalf("budget selection: %v", e)
			}
			first := e[0].(map[string]any)
			if mode == "vector" {
				if first["item"] != "b.md" || first["cosine"] != float64(1) || first["rank"] != float64(1) {
					t.Fatalf("cosine order: %v", e)
				}
			} else {
				if first["item"] != "a.md" || first["lexical"].(map[string]any)["rank"] != float64(2) || first["vector"].(map[string]any)["rank"] != float64(2) || math.Abs(first["rrf"].(float64)-0.03225806451612903) > 1e-16 {
					t.Fatalf("RRF overlap must win: %v", e)
				}
			}
			assertMCPIdentities(t, result)
			trailID := result["trail_id"].(string)
			inspect := map[string]any{"source": "alpha", "trail_id": trailID}
			defaultView := client.call("mousa_trail", inspect, "")["historical"].(map[string]any)
			inspect["retrieval_details"] = false
			falseView := client.call("mousa_trail", inspect, "")["historical"].(map[string]any)
			if !reflect.DeepEqual(defaultView, falseView) || defaultView["vector"] != nil || defaultView["hybrid"] != nil || defaultView["token"] != nil {
				t.Fatalf("default history grew: %v", defaultView)
			}
			inspect["retrieval_details"] = true
			history := client.call("mousa_trail", inspect, "")["historical"].(map[string]any)
			if history["packet_id"] != result["packet_id"] || history["decision_id"] != result["decision_id"] || !reflect.DeepEqual(history[mode], result[mode]) {
				t.Fatalf("producing facts changed: %v", history)
			}
			saved = append(saved, savedQuery{mode, result, history[mode].(map[string]any)})
		}
		// Native-created v5/v6 trails are inspectable through the same MCP boundary.
		args := []string{"query", "--" + mode + "-space", space, "--source", "alpha"}
		if mode == "hybrid" {
			args = append(args, "Drain")
		}
		_, native := run.runInput(`{"schema":"mousa.vector_query.v1","values":[1,0]}`, false, args...)
		h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": native["trail_id"], "retrieval_details": true}, "")["historical"].(map[string]any)
		if h[mode] == nil || h["packet_id"] != native["packet_id"] {
			t.Fatalf("native history missing: %v", h)
		}
	}
	client.close()
	jsonlSync(t, run, "alpha", record("a.md", "Replacement revision."))
	client = startMCPClient(t, run, "cli", false)
	for _, s := range saved {
		h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": s.result["trail_id"], "retrieval_details": true, "supersession": true}, "")["historical"].(map[string]any)
		if !reflect.DeepEqual(h[s.mode], s.facts) || h["supersession"] != nil {
			t.Fatalf("history reinterpreted current state: %v", h)
		}
		fresh := client.call("mousa_"+s.mode+"_query", mcpRetrievalArgs(space, s.mode, "", 40), "")
		if fresh["indexed"] != float64(1) || fresh["missing"] != float64(2) || strings.Contains(fmt.Sprint(fresh), "before stopping") {
			t.Fatalf("stale revision released: %v", fresh)
		}
	}
	for _, mode := range []string{"vector", "hybrid"} {
		args := mcpRetrievalArgs(space, mode, "", 40)
		args["values"] = []float64{1}
		client.call("mousa_"+mode+"_query", args, "invalid_query")
		args["values"], args["space_id"] = []float64{1, 0}, strings.Repeat("f", 64)
		client.call("mousa_"+mode+"_query", args, "not_found")
		args["source"] = "/not/a/permitted/directory"
		client.call("mousa_"+mode+"_query", args, "source_not_permitted")
	}
	run.run(false, "access", "--source", "alpha", "deny")
	for _, mode := range []string{"vector", "hybrid"} {
		denied := client.call("mousa_"+mode+"_query", mcpRetrievalArgs(space, mode, "", 40), "")
		if denied["eligible"] != float64(0) || denied["indexed"] != float64(0) || len(denied["evidence"].([]any)) != 0 || len(denied[mode].(map[string]any)["candidates"].([]any)) != 0 {
			t.Fatalf("denied source facts: %v", denied)
		}
	}
	for _, s := range saved {
		hidden := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": s.result["trail_id"], "retrieval_details": true}, "")
		if hidden["historical"] != nil {
			t.Fatalf("denied history leaked: %v", hidden)
		}
	}
	run.run(false, "access", "--source", "alpha", "allow")
	run.run(false, "withdraw", "--source", "alpha")
	for _, mode := range []string{"vector", "hybrid"} {
		denied := client.call("mousa_"+mode+"_query", mcpRetrievalArgs(space, mode, "", 40), "")
		if denied["eligible"] != float64(0) || len(denied["evidence"].([]any)) != 0 {
			t.Fatalf("withdrawn source leaked: %v", denied)
		}
	}
}

func TestMCPRetrievalStrictInputsBeforeMutation(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("a", "Drain"))
	space := prepareMCPVectors(t, run, map[string][]float64{"a": {1, 0}})
	client := startMCPClient(t, run, "cli", false)
	for _, mode := range []string{"vector", "hybrid"} {
		base := mcpRetrievalArgs(space, mode, "", 40)
		encoded, _ := json.Marshal(base)
		var bad []string
		for _, key := range []string{"source", "space_id", "values", "budget_bytes"} {
			clone := make(map[string]any)
			for k, v := range base {
				clone[k] = v
			}
			delete(clone, key)
			raw, _ := json.Marshal(clone)
			bad = append(bad, string(raw))
			clone[key] = nil
			raw, _ = json.Marshal(clone)
			bad = append(bad, string(raw))
		}
		for key, values := range map[string][]string{
			"values":         {`[true,0]`, `[null,1]`, `[0,0]`, `[]`, `[1e999,0]`, `"1,0"`, `[` + strings.Repeat("1,", 2048) + `1]`},
			"budget_bytes":   {`true`, `0`, `-1`, `1.5`, `262145`},
			"space_id":       {`"bad"`, `"` + strings.Repeat("A", 64) + `"`, `"` + strings.Repeat("0", 64) + `"`},
			"packing_policy": {`"unknown"`, `false`},
			"supersession":   {`false`}, "associations": {`[]`}, "context_tokens": {`0`, `null`, `true`, `524289`}, "tokenizer_python": {`"/bin/false"`},
		} {
			for _, value := range values {
				clone := make(map[string]json.RawMessage)
				json.Unmarshal(encoded, &clone)
				clone[key] = json.RawMessage(value)
				raw, _ := json.Marshal(clone)
				bad = append(bad, string(raw))
			}
		}
		bad = append(bad, string(encoded[:len(encoded)-1])+`,"space_id":"`+space+`"}`, `[]`)
		if mode == "hybrid" {
			for _, field := range []string{`"query":null`, `"query":"!!!"`, `"policy":null`, `"policy":"unknown"`} {
				clone := make(map[string]json.RawMessage)
				json.Unmarshal(encoded, &clone)
				parts := strings.SplitN(field, ":", 2)
				delete(clone, strings.Trim(parts[0], `"`))
				raw, _ := json.Marshal(clone)
				bad = append(bad, string(raw[:len(raw)-1])+","+field+"}")
			}
		} else {
			bad = append(bad, string(encoded[:len(encoded)-1])+`,"policy":"original"}`)
		}
		for _, raw := range bad {
			before := mcpCanonicalInspectionState(t, run.store)
			client.call("mousa_"+mode+"_query", json.RawMessage(raw), "invalid_arguments")
			if after := mcpCanonicalInspectionState(t, run.store); !reflect.DeepEqual(before, after) {
				t.Fatalf("invalid request mutated store: %s", raw)
			}
		}
	}
	for _, raw := range []string{`null`, `1`, `"true"`} {
		client.call("mousa_trail", json.RawMessage(`{"source":"alpha","trail_id":"`+strings.Repeat("a", 64)+`","retrieval_details":`+raw+`}`), "invalid_arguments")
	}
}

func TestMCPRetrievalDuplicateAndBudgetPacking(t *testing.T) {
	for _, mode := range []string{"vector", "hybrid"} {
		for _, packing := range []string{"original", "exact-v1"} {
			t.Run(mode+"/"+packing, func(t *testing.T) {
				run, _ := setup(t)
				jsonlSync(t, run, "alpha", strings.Join([]string{record("large", strings.Repeat("Drain ", 20)), record("first", "Drain"), record("copy", "Drain"), record("small", "x")}, "\n"))
				space := prepareMCPVectors(t, run, map[string][]float64{"large": {1, 0}, "first": {0, 1}, "copy": {0, 1}, "small": {-1, 0}})
				client := startMCPClient(t, run, "cli", false)
				got := client.call("mousa_"+mode+"_query", mcpRetrievalArgs(space, mode, packing, 11), "")
				want, dup := 3, float64(0)
				if packing == "exact-v1" {
					want, dup = 2, 1
				}
				if len(got["evidence"].([]any)) != want || got["duplicate_omitted"] != dup || got["budget_omitted"] != float64(1) {
					t.Fatalf("packing: %v", got)
				}
				args := mcpRetrievalArgs(space, mode, packing, 11)
				args["source"] = "beta"
				client.call("mousa_"+mode+"_query", args, "integrity")
				jsonlSync(t, run, "beta", record("missing", "Drain unembedded"))
				missing := client.call("mousa_"+mode+"_query", args, "")
				wantOutcome := "no_indexed_passages"
				if mode == "hybrid" {
					wantOutcome = "budget_omitted"
				}
				if missing["outcome"] != wantOutcome || missing["missing"] != float64(1) {
					t.Fatalf("missing coverage: %v", missing)
				}
				jsonlSync(t, run, "beta", `{"id":"missing","deleted":true}`)
				empty := client.call("mousa_"+mode+"_query", args, "")
				if empty["outcome"] != "no_passages" || empty["eligible"] != float64(0) {
					t.Fatalf("empty coverage: %v", empty)
				}
			})
		}
	}
}

func TestMCPRetrievalTokenHistorySafeProjection(t *testing.T) {
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("a", "Drain sockets."))
	space := prepareMCPVectors(t, run, map[string][]float64{"a": {1, 0}})
	client := startMCPClient(t, run, "cli", false)
	for _, mode := range []string{"lexical", "vector", "hybrid"} {
		args := []string{"query", "--source", "alpha", "--context-tokens", "1000", "--tokenizer-python", python, "--tokenizer-cache", cache}
		vector := ""
		if mode != "lexical" {
			args = append(args, "--"+mode+"-space", space)
			vector = `{"schema":"mousa.vector_query.v1","values":[1,0]}`
		}
		if mode != "vector" {
			args = append(args, "Drain")
		}
		_, produced := run.runInput(vector, false, args...)
		h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": produced["trail_id"], "retrieval_details": true}, "")["historical"].(map[string]any)
		if h["schema"] != "mousa.source_trail.v7" || h["token"] != nil || h["token_verification"] != nil || h["packet_id"] != produced["packet_id"] {
			t.Fatalf("unsafe token extension: %v", h)
		}
		if mode != "lexical" {
			facts := produced[mode].(map[string]any)
			delete(facts, "query_values")
			if !reflect.DeepEqual(h[mode], facts) {
				t.Fatalf("v7 retrieval differs from verified native facts: %v", h)
			}
		} else if h["vector"] != nil || h["hybrid"] != nil {
			t.Fatalf("lexical history invented ranking: %v", h)
		}
		opted := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": produced["trail_id"], "token_details": true}, "")["historical"].(map[string]any)
		if !reflect.DeepEqual(opted["token"], produced["token"]) || opted["vector"] != nil || opted["hybrid"] != nil {
			t.Fatal("native-created token history opt-in changed producing facts")
		}
	}
}

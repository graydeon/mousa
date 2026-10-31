package main

import (
	"reflect"
	"testing"
)

func TestMCPVectorHybridTokenSupersession(t *testing.T) {
	startup := mcpTokenStartup(t)
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "alpha")
	jsonlSync(t, run, "alpha", record("peer", "oldterm survivor"))
	space := prepareMCPVectors(t, run, map[string][]float64{"doc": {1, 0}, "peer": {0, 1}})
	client := startMCPClient(t, run, "cli", false, startup...)
	defer client.close()
	for _, mode := range []string{"vector", "hybrid"} {
		args := mcpRetrievalArgs(space, mode, "original", 28)
		if mode == "hybrid" {
			args["query"] = "oldterm"
		}
		args["context_tokens"], args["supersession"] = 4096, true
		got := client.call("mousa_"+mode+"_query", args, "")
		assertMCPTokenRendering(t, got)
		if got["supersession_excluded"] != float64(0) {
			t.Fatal("no-history consultation", got)
		}
	}
	active := activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	var saved map[string]any
	for _, mode := range []string{"vector", "hybrid"} {
		for _, packing := range []string{"original", "exact-v1"} {
			args := mcpRetrievalArgs(space, mode, packing, 28)
			if mode == "hybrid" {
				args["query"] = "oldterm"
			}
			args["context_tokens"] = 4096
			ordinary := client.call("mousa_"+mode+"_query", args, "")
			args["supersession"] = false
			disabled := client.call("mousa_"+mode+"_query", args, "")
			if !reflect.DeepEqual(ordinary["evidence"], disabled["evidence"]) || ordinary["packet_id"] != disabled["packet_id"] || disabled["supersession_excluded"] != nil {
				t.Fatal("false changed v7")
			}
			args["supersession"] = true
			got := client.call("mousa_"+mode+"_query", args, "")
			n := assertMCPTokenRendering(t, got)
			if got["supersession_excluded"] != float64(1) || got["context"] == ordinary["context"] || len(hits(got)) != 1 || hits(got)[0].(map[string]any)["item"] != "peer" {
				t.Fatal("later survivor not packed", got)
			}
			if !reflect.DeepEqual(got[mode], ordinary[mode]) {
				t.Fatal("producing facts changed")
			}
			args["context_tokens"] = n
			exact := client.call("mousa_"+mode+"_query", args, "")
			if exact["context"] != got["context"] {
				t.Fatal("exact-fit rendering changed")
			}
			args["context_tokens"] = n - 1
			short := client.call("mousa_"+mode+"_query", args, "")
			assertMCPTokenRendering(t, short)
			if short["context"] != "" || short["outcome"] != "budget_omitted" {
				t.Fatal("one-short fit", short)
			}
			for _, sup := range []bool{false, true} {
				for _, retrieval := range []bool{false, true} {
					for _, token := range []bool{false, true} {
						h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": got["trail_id"], "supersession": sup, "retrieval_details": retrieval, "token_details": token}, "")["historical"].(map[string]any)
						if h["schema"] != "mousa.source_trail.v10" || (h["supersession"] != nil) != sup || (h[mode] != nil) != retrieval || (h["token"] != nil) != token {
							t.Fatal("independent history projections", h)
						}
						if retrieval && !reflect.DeepEqual(h[mode], got[mode]) {
							t.Fatal("history reranked")
						}
						if token && !reflect.DeepEqual(h["token"], got["token"]) {
							t.Fatal("history recounted or changed facts")
						}
					}
				}
			}
			saved = got
		}
	}
	jsonlSync(t, run, "alpha", `{"id":"peer","deleted":true}`)
	for _, mode := range []string{"vector", "hybrid"} {
		args := mcpRetrievalArgs(space, mode, "original", 1)
		if mode == "hybrid" {
			args["query"] = "oldterm"
		}
		args["context_tokens"], args["supersession"] = 1, true
		got := client.call("mousa_"+mode+"_query", args, "")
		assertMCPTokenRendering(t, got)
		if got["context"] != "" || got["outcome"] != "supersession_excluded" || got["budget_omitted"] != float64(0) {
			t.Fatal("all-withheld precedence", got)
		}
	}
	activateQueryDeclaration(t, run, source, &active, nil, 2)
	h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": saved["trail_id"], "supersession": true, "token_details": true}, "")["historical"].(map[string]any)
	if h["supersession"].(map[string]any)["activation_id"] != active.String() {
		t.Fatal("deactivation reinterpreted history", h)
	}
	run.run(false, "access", "--source", "alpha", "deny")
	for _, mode := range []string{"vector", "hybrid"} {
		args := mcpRetrievalArgs(space, mode, "original", 28)
		args["context_tokens"], args["supersession"] = 1, true
		got := client.call("mousa_"+mode+"_query", args, "")
		assertMCPTokenRendering(t, got)
		if got["outcome"] != "policy_excluded" || got["context"] != "" || got["supersession_excluded"] != float64(0) {
			t.Fatal("denied query leaked", got)
		}
		hidden := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": saved["trail_id"], "supersession": true, "token_details": true, "retrieval_details": true}, "")
		if hidden["historical"] != nil {
			t.Fatal("denied history leaked")
		}
	}
}

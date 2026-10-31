package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func mcpAssociation(from, to string) map[string]any {
	return map[string]any{"from_item": from, "to_item": to, "basis": "connection cleanup qualification", "author": "example.maintainer"}
}

func mcpAssociationDocument(declarations ...map[string]any) map[string]any {
	return map[string]any{"schema": mousa.AssociationDeclarationSchema, "associations": declarations}
}

func TestMCPAssociatedLexicalEvidence(t *testing.T) {
	for _, policy := range []string{"original", "dedup"} {
		for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
			t.Run(policy+"/"+packing, func(t *testing.T) {
				run, _ := setup(t)
				const primary = "Run the backup inside the connection context manager."
				const qualification = "The context manager does not close the connection; close it explicitly."
				jsonlSync(t, run, "alpha", record("backup", primary)+"\n"+record("closure", qualification))
				client := startMCPClient(t, run, "cli", false)
				args := mcpQuery("alpha", "backup", 8192)
				args["policy"], args["packing_policy"] = policy, packing
				control := client.call("mousa_query", args, "")
				if len(hits(control)) != 1 || hits(control)[0].(map[string]any)["item"] != "backup" {
					t.Fatalf("ordinary query = %v", control)
				}
				args["associations"] = mcpAssociationDocument(mcpAssociation("backup", "closure"))
				query := client.call("mousa_query", args, "")
				if len(hits(query)) != 2 || mcpCanonicalJSON(t, hits(query)[0]) != mcpCanonicalJSON(t, hits(control)[0]) {
					t.Fatalf("association displaced primary: %v", query)
				}
				associated := hits(query)[1].(map[string]any)
				if associated["item"] != "closure" || associated["text"] != qualification || associated["origin"] != "association" ||
					associated["content_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte(qualification))) ||
					associated["byte_start"] != float64(0) || associated["byte_end"] != float64(len(qualification)) ||
					associated["representation_id"] == hits(query)[0].(map[string]any)["representation_id"] ||
					mcpCanonicalJSON(t, associated["association"]) != mcpCanonicalJSON(t, mcpAssociation("backup", "closure")) {
					t.Fatalf("missing independent provenance/attribution: %v", associated)
				}
				for _, key := range []string{"rank", "bm25_score"} {
					if _, present := associated[key]; present {
						t.Fatalf("association has lexical %s", key)
					}
				}
				before := inspectionRows(t, run.store, nil)
				trail := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": query["trail_id"]}, "")
				requireInspectionAudit(t, run, before, trail, "alpha", "allow")
				history := trail["historical"].(map[string]any)
				if history["schema"] != mousa.SourceTrailSchemaV3 || history["id"] != query["trail_id"] {
					t.Fatalf("wrong v3 history: %v", history)
				}
				for _, key := range []string{"request_id", "decision_id", "packet_id"} {
					if history[key] != query[key] {
						t.Fatalf("producing %s differs", key)
					}
				}
				rows := history["associated"].([]any)
				if len(rows) != 1 || rows[0].(map[string]any)["segment_id"] != associated["segment_id"] || rows[0].(map[string]any)["selected"] != true || rows[0].(map[string]any)["author"] != "example.maintainer" {
					t.Fatalf("wrong recorded association: %v", rows)
				}
				client.close()
			})
		}
	}
}

func TestMCPAssociationSelectionBoundaries(t *testing.T) {
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			run, _ := setup(t)
			jsonlSync(t, run, "alpha", record("backup", "backup instruction")+"\n"+record("closure", "close explicitly")+"\n"+record("next", "never transitively released")+"\n"+record("duplicate", "close explicitly"))
			jsonlSync(t, run, "beta", record("foreign", "foreign qualification"))
			client := startMCPClient(t, run, "cli", false)
			args := mcpQuery("alpha", "backup", 8192)
			args["packing_policy"] = packing
			control := client.call("mousa_query", args, "")
			args["supersession"] = false
			explicitFalse := client.call("mousa_query", args, "")
			if !reflect.DeepEqual(mcpStableQueryValues(t, control), mcpStableQueryValues(t, explicitFalse)) {
				t.Fatal("explicit false changed default response")
			}
			args["associations"] = mcpAssociationDocument(mcpAssociation("unselected", "closure"))
			unfired := client.call("mousa_query", args, "")
			if !reflect.DeepEqual(mcpStableQueryValues(t, control), mcpStableQueryValues(t, unfired)) {
				t.Fatal("unfired declaration changed default response")
			}
			unfiredTrail := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": unfired["trail_id"]}, "")["historical"].(map[string]any)
			var expectedSchema any
			if packing == mousa.PackingExactV1 {
				expectedSchema = mousa.SourceTrailSchemaV2
			}
			if unfiredTrail["schema"] != expectedSchema {
				t.Fatalf("unfired schema = %v", unfiredTrail)
			}

			args["associations"] = mcpAssociationDocument(mcpAssociation("backup", "closure"), mcpAssociation("closure", "next"))
			expanded := client.call("mousa_query", args, "")
			if len(hits(expanded)) != 2 || hits(expanded)[1].(map[string]any)["item"] != "closure" {
				t.Fatalf("not depth one: %v", expanded)
			}
			args["budget_bytes"] = len("backup instruction")
			budget := client.call("mousa_query", args, "")
			if len(hits(budget)) != 1 || hits(budget)[0].(map[string]any)["item"] != "backup" || budget["used_bytes"] != float64(len("backup instruction")) {
				t.Fatalf("primary priority lost: %v", budget)
			}
			if omission := budget["association_omissions"].([]any); len(omission) != 1 || omission[0].(map[string]any)["reason"] != "budget" {
				t.Fatalf("missing budget omission: %v", budget)
			}
			args["budget_bytes"] = 1
			unselected := client.call("mousa_query", args, "")
			if len(hits(unselected)) != 0 || unselected["association_omissions"] != nil {
				t.Fatalf("unselected primary expanded: %v", unselected)
			}
			args["budget_bytes"] = 8192
			args["associations"] = mcpAssociationDocument(mcpAssociation("backup", "closure"), mcpAssociation("backup", "duplicate"))
			duplicate := client.call("mousa_query", args, "")
			want := 3
			if packing == mousa.PackingExactV1 {
				want = 2
				omissions := duplicate["association_omissions"].([]any)
				if len(omissions) != 1 || omissions[0].(map[string]any)["reason"] != "duplicate" {
					t.Fatalf("missing duplicate omission: %v", duplicate)
				}
			}
			if len(hits(duplicate)) != want {
				t.Fatalf("wrong exact-content selection: %v", duplicate)
			}
			for _, target := range []string{"missing", "foreign", "closure"} {
				reason := "target_unknown"
				if target == "closure" {
					jsonlSync(t, run, "alpha", `{"id":"closure","deleted":true}`)
					reason = "target_inactive"
				}
				args["associations"] = mcpAssociationDocument(mcpAssociation("backup", target))
				query := client.call("mousa_query", args, "")
				if len(hits(query)) != 1 {
					t.Fatalf("released missing/inactive/foreign target: %v", query)
				}
				omissions := query["association_omissions"].([]any)
				if len(omissions) != 1 || omissions[0].(map[string]any)["reason"] != reason {
					t.Fatalf("wrong target omission: %v", query)
				}
			}
			run.run(false, "access", "--source", "alpha", "deny")
			denied := client.call("mousa_query", args, "")
			if denied["decision_outcome"] != "deny" || len(hits(denied)) != 0 || denied["association_omissions"] != nil {
				t.Fatalf("declaration bypassed denial: %v", denied)
			}
			before := inspectionRows(t, run.store, nil)
			history := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": expanded["trail_id"]}, "")
			requireInspectionAudit(t, run, before, history, "alpha", "deny")
			if history["historical"] != nil {
				t.Fatalf("denied history released facts: %v", history)
			}
			client.close()
		})
	}
}

func TestMCPAssociationArgumentFailuresDoNotProduceState(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("backup", "backup instruction"))
	client := startMCPClient(t, run, "cli", false)
	valid := `{"schema":"mousa.association_declarations.v1","associations":[{"from_item":"backup","to_item":"closure","basis":"qualification","author":"maintainer"}]}`
	invalid := []string{
		`null`, `true`, `[]`, `"file.json"`, `{}`,
		`{"schema":"wrong","associations":[]}`,
		`{"schema":"mousa.association_declarations.v1","associations":[]}`,
		strings.Replace(valid, `"schema":`, `"unknown":0,"schema":`, 1),
		strings.Replace(valid, `"schema":`, `"schema":"ignored","schema":`, 1),
		strings.Replace(valid, `"from_item":`, `"unknown":0,"from_item":`, 1),
		strings.Replace(valid, `"author":`, `"author":"ignored","author":`, 1),
		strings.Replace(valid, `"associations":[`, `"associations":[null,`, 1),
		`{"schema":"mousa.association_declarations.v1","associations":{}}`,
		strings.Replace(valid, `"to_item":"closure"`, `"to_item":3`, 1),
		strings.Replace(valid, `"closure"`, `"backup"`, 1),
		strings.Replace(valid, `"qualification"`, `""`, 1),
		strings.Replace(valid, `"maintainer"`, `"`+strings.Repeat("é", 65)+`"`, 1),
		strings.Replace(valid, `"maintainer"`, `"\u0000"`, 1),
	}
	var tooMany []map[string]any
	for index := range 33 {
		tooMany = append(tooMany, mcpAssociation("backup", fmt.Sprintf("target-%d", index)))
	}
	encoded, err := json.Marshal(mcpAssociationDocument(tooMany...))
	if err != nil {
		t.Fatal(err)
	}
	invalid = append(invalid, string(encoded))
	repeated := mcpAssociationDocument(mcpAssociation("backup", "closure"), mcpAssociation("backup", "closure"))
	encoded, err = json.Marshal(repeated)
	if err != nil {
		t.Fatal(err)
	}
	invalid = append(invalid, string(encoded))
	before := inspectionRows(t, run.store, nil)
	for _, document := range invalid {
		raw := json.RawMessage(`{"source":"alpha","query":"backup","policy":"original","budget_bytes":8192,"associations":` + document + `}`)
		client.call("mousa_query", raw, "invalid_arguments")
	}
	for _, extra := range []string{`"supersession":true`, `"context_tokens":100`, `"window_tokens":0`, `"prompt_reserve_tokens":0`, `"output_reserve_tokens":0`, `"associations":` + valid} {
		client.call("mousa_query", json.RawMessage(`{"source":"alpha","query":"backup","policy":"original","budget_bytes":8192,"associations":`+valid+`,`+extra+`}`), "invalid_arguments")
	}
	for _, tool := range []string{"mousa_sync", "mousa_status", "mousa_trail", "mousa_vector_inputs", "mousa_vector_import", "mousa_vector_query", "mousa_hybrid_query"} {
		client.call(tool, map[string]any{"source": "alpha", "associations": mcpAssociationDocument(mcpAssociation("backup", "closure"))}, "invalid_arguments")
	}
	if after := inspectionRows(t, run.store, nil); !reflect.DeepEqual(before, after) {
		t.Fatal("argument failure mutated store")
	}
	client.close()
}

func TestMCPAssociationDiscoveryIsAdditive(t *testing.T) {
	raw, err := os.ReadFile("testdata/mcp_association_baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline map[string]any
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatal(err)
	}
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", false)
	listed, err := client.session.ListTools(t.Context(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	current := make(map[string]any)
	for _, tool := range listed.Tools {
		encoded, err := json.Marshal(tool)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if tool.Name == "mousa_query" {
			properties := wire["inputSchema"].(map[string]any)["properties"].(map[string]any)
			if properties["associations"] == nil {
				t.Fatal("missing inline declaration schema")
			}
			delete(properties, "associations")
		}
		current[tool.Name] = wire
	}
	for _, entry := range baseline["tools"].([]any) {
		tool := entry.(map[string]any)
		tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["source"].(map[string]any)["enum"] = []string{"alpha", "beta"}
		name := tool["name"].(string)
		if mcpCanonicalJSON(t, current[name]) != mcpCanonicalJSON(t, tool) {
			t.Fatalf("%s changed beyond query associations", name)
		}
	}
	client.close()
}

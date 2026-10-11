package main

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/graydeon/mousa/internal/mousa"
)

func requireMCPSchemaProperties(t *testing.T, schema *jsonschema.Schema, want []string) {
	t.Helper()
	got := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		got = append(got, name)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MCP schema properties = %v, want published set %v", got, want)
	}
	for _, name := range schema.PropertyOrder {
		if schema.Properties[name] == nil {
			t.Fatalf("schema property order retains removed property %s", name)
		}
	}
	if schema.AdditionalProperties == nil || schema.AdditionalProperties.Not == nil {
		t.Fatal("MCP schema lost its closed additionalProperties boundary")
	}
}

func TestMCPSupersessionKeepsPublishedSchemaPropertySets(t *testing.T) {
	query, err := mcpOutputSchema[evidenceResult]("mousa_query")
	if err != nil {
		t.Fatal(err)
	}
	requireMCPSchemaProperties(t, query.Properties["result"], []string{
		"query", "source", "query_policy", "expression", "request_id", "decision_id",
		"decision_outcome", "decision_reasons", "evaluated_at_usec", "trail_id", "packet_id",
		"outcome", "matched_candidates", "candidate_limit", "lifecycle_excluded", "budget_omitted",
		"duplicate_omitted", "packing_policy", "used_bytes", "budget_bytes", "association_omissions",
		"evidence", "latency_micros",
	})
	trail, err := mcpOutputSchema[mcpTrailResult]("mousa_trail")
	if err != nil {
		t.Fatal(err)
	}
	requireMCPSchemaProperties(t, trail.Properties["result"].Properties["historical"], []string{
		"id", "request_id", "decision_id", "decision_outcome", "decision_reasons", "expression",
		"budget_bytes", "used_bytes", "packet_id", "lifecycle_excluded", "candidates", "schema",
		"packing_policy", "associated", "association_omissions",
	})
	for _, schema := range []*jsonschema.Schema{query, trail} {
		encoded, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if wire["$defs"] != nil || wire["definitions"] != nil {
			t.Fatalf("native fields introduced MCP definitions: %s", encoded)
		}
		if _, err := schema.Resolve(nil); err != nil {
			t.Fatalf("MCP output schema is not resolvable: %v", err)
		}
	}
	tools, err := mcpToolDefinitions([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name == "mousa_query" {
			properties := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
			if properties["supersession"] != nil {
				t.Fatal("MCP input schema exposed native opt-in")
			}
		}
	}
}

func TestMCPSupersessionRejectsOptInAndPreservesFilteredV4History(t *testing.T) {
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "alpha")
	activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	_, native := supersessionQuery(t, run, "alpha", mousa.PackingOriginal, "oldterm", 8192, true)
	requireSupersessionCounts(t, native, "supersession_excluded", 1, 1, 0, 0, 0)
	client := startMCPClient(t, run, "cli", false)
	args := mcpQuery("alpha", "oldterm", 8192)
	args["supersession"] = true
	client.call("mousa_query", args, "invalid_arguments")
	query := client.call("mousa_query", mcpQuery("alpha", "oldterm", 8192), "")
	if _, present := query["supersession_excluded"]; present || len(hits(query)) != 1 {
		t.Fatalf("MCP default query changed under active administration: %v", query)
	}
	trail := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": native["trail_id"]}, "")
	history := trail["historical"].(map[string]any)
	if _, present := history["supersession"]; present || history["schema"] != "mousa.source_trail.v4" || history["used_bytes"] != float64(0) || history["candidates"].([]any)[0].(map[string]any)["selected"] != false {
		t.Fatalf("MCP changed its filtered history shape or reinterpreted v4 selection: %v", trail)
	}
	if nativeHistory := supersessionHistory(t, run, "alpha", native); nativeHistory["supersession"] == nil {
		t.Fatalf("MCP projection removed native recorded consultation: %v", nativeHistory)
	}
}

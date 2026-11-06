package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/graydeon/mousa/internal/mousa"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func supersessionDraftFixture(activation bool) map[string]any {
	if activation {
		return map[string]any{"schema": mcpSupersessionActivationInputSchema, "expected_previous_activation_id": nil, "declaration_id": strings.Repeat("a", 64), "actor_id": "operator", "actor_version": "v1", "occurred_at_usec": int64(1), "reason": nil}
	}
	return map[string]any{"schema": mcpSupersessionDeclarationInputSchema, "predecessor_item_id": "old", "predecessor_representation_id": strings.Repeat("a", 64), "successor_item_id": "new", "successor_representation_id": strings.Repeat("b", 64), "author": "maintainer", "basis": "explicit replacement"}
}

func marshalSupersessionTest(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeSupersessionTest(t *testing.T, activation bool, raw []byte) error {
	t.Helper()
	source, err := streamSource("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if activation {
		_, err = decodeMCPSupersessionActivation(raw, source.ID)
	} else {
		_, err = decodeMCPSupersessionDeclaration(raw, source.ID)
	}
	return err
}

func TestMCPSupersessionDraftStrictFields(t *testing.T) {
	for _, activation := range []bool{false, true} {
		fixture := supersessionDraftFixture(activation)
		raw := marshalSupersessionTest(t, fixture)
		if err := decodeSupersessionTest(t, activation, raw); err != nil {
			t.Fatal(err)
		}
		for key := range fixture {
			for _, invalid := range []any{false, 42, []any{}, map[string]any{"x": 1}, nil} {
				if activation && (key == "occurred_at_usec" && invalid == 42) {
					continue
				}
				if invalid == nil && activation && (key == "reason" || key == "expected_previous_activation_id" || key == "declaration_id") {
					continue
				}
				draft := supersessionDraftFixture(activation)
				draft[key] = invalid
				if err := decodeSupersessionTest(t, activation, marshalSupersessionTest(t, draft)); err == nil {
					t.Errorf("activation=%v accepted %s=%v", activation, key, invalid)
				}
			}
			draft := supersessionDraftFixture(activation)
			delete(draft, key)
			if err := decodeSupersessionTest(t, activation, marshalSupersessionTest(t, draft)); err == nil {
				t.Errorf("accepted missing %s", key)
			}
			draft[strings.ToUpper(key)] = fixture[key]
			if err := decodeSupersessionTest(t, activation, marshalSupersessionTest(t, draft)); err == nil {
				t.Errorf("accepted case-folded %s", key)
			}
			duplicate := append([]byte(nil), raw[:len(raw)-1]...)
			duplicate = append(duplicate, []byte(`,"`+key+`":`+string(marshalSupersessionTest(t, fixture[key]))+`}`)...)
			if err := decodeSupersessionTest(t, activation, duplicate); err == nil {
				t.Errorf("accepted duplicate %s", key)
			}
		}
		for _, key := range []string{"id", "source_id", "unknown"} {
			draft := supersessionDraftFixture(activation)
			draft[key] = strings.Repeat("c", 64)
			if err := decodeSupersessionTest(t, activation, marshalSupersessionTest(t, draft)); err == nil {
				t.Errorf("accepted forbidden %s", key)
			}
		}
		for _, invalid := range [][]byte{[]byte("null"), []byte("[]"), append(append([]byte(nil), raw...), []byte(" {}")...), append(append([]byte(nil), raw...), 0xff)} {
			if err := decodeSupersessionTest(t, activation, invalid); err == nil {
				t.Error("accepted invalid framing or UTF-8")
			}
		}
		padded := append(append([]byte(nil), raw[:len(raw)-1]...), bytes.Repeat([]byte(" "), (64<<10)-len(raw))...)
		padded = append(padded, '}')
		if err := decodeSupersessionTest(t, activation, padded); err != nil {
			t.Fatalf("64 KiB boundary: %v", err)
		}
		if err := decodeSupersessionTest(t, activation, append(padded, ' ')); err == nil {
			t.Fatal("accepted over 64 KiB")
		}
	}
}

func TestMCPSupersessionDraftCanonicalBoundaries(t *testing.T) {
	for _, test := range []struct {
		activation bool
		field      string
		limit      int
	}{
		{false, "predecessor_item_id", mousa.MaxSupersessionItemBytes}, {false, "successor_item_id", mousa.MaxSupersessionItemBytes},
		{false, "author", mousa.MaxSupersessionAuthorBytes}, {false, "basis", mousa.MaxSupersessionBasisBytes},
		{true, "actor_id", mousa.MaxSupersessionActorBytes}, {true, "actor_version", mousa.MaxSupersessionActorBytes}, {true, "reason", mousa.MaxSupersessionReasonBytes},
	} {
		for _, value := range []string{strings.Repeat("x", test.limit), strings.Repeat("é", test.limit/2)} {
			draft := supersessionDraftFixture(test.activation)
			draft[test.field] = value
			if err := decodeSupersessionTest(t, test.activation, marshalSupersessionTest(t, draft)); err != nil {
				t.Fatalf("%s maximum: %v", test.field, err)
			}
		}
		for _, value := range []string{"", "a\x00b", strings.Repeat("x", test.limit+1), strings.Repeat("é", test.limit/2+1)} {
			draft := supersessionDraftFixture(test.activation)
			draft[test.field] = value
			if err := decodeSupersessionTest(t, test.activation, marshalSupersessionTest(t, draft)); err == nil {
				t.Errorf("accepted invalid %s", test.field)
			}
		}
	}
	for _, activation := range []bool{false, true} {
		fields := []string{"predecessor_representation_id", "successor_representation_id"}
		if activation {
			fields = []string{"declaration_id", "expected_previous_activation_id"}
		}
		for _, field := range fields {
			for _, value := range []string{strings.Repeat("0", 64), strings.Repeat("A", 64), strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("g", 64)} {
				draft := supersessionDraftFixture(activation)
				draft[field] = value
				if err := decodeSupersessionTest(t, activation, marshalSupersessionTest(t, draft)); err == nil {
					t.Errorf("accepted invalid identity %s", field)
				}
			}
		}
	}
	for _, literal := range []string{"0", "-1", "1.0", "1e0", "true", `"1"`, "9223372036854775808", "null"} {
		draft := supersessionDraftFixture(true)
		draft["occurred_at_usec"] = json.RawMessage(literal)
		if err := decodeSupersessionTest(t, true, marshalSupersessionTest(t, draft)); err == nil {
			t.Errorf("accepted timestamp %s", literal)
		}
	}
	draft := supersessionDraftFixture(true)
	draft["occurred_at_usec"] = int64(math.MaxInt64)
	if err := decodeSupersessionTest(t, true, marshalSupersessionTest(t, draft)); err != nil {
		t.Fatal(err)
	}
	draft["declaration_id"] = nil
	if err := decodeSupersessionTest(t, true, marshalSupersessionTest(t, draft)); err == nil {
		t.Fatal("accepted initial deactivation")
	}
	draft["expected_previous_activation_id"] = strings.Repeat("b", 64)
	if err := decodeSupersessionTest(t, true, marshalSupersessionTest(t, draft)); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"successor_item_id", "successor_representation_id"} {
		draft := supersessionDraftFixture(false)
		draft[field] = draft[strings.Replace(field, "successor", "predecessor", 1)]
		if err := decodeSupersessionTest(t, false, marshalSupersessionTest(t, draft)); err == nil {
			t.Errorf("accepted same %s", field)
		}
	}
	for _, activation := range []bool{false, true} {
		raw := marshalSupersessionTest(t, supersessionDraftFixture(activation))
		old, replacement := `"maintainer"`, `"\ud800"`
		if activation {
			old = `"operator"`
		}
		if err := decodeSupersessionTest(t, activation, bytes.Replace(raw, []byte(old), []byte(replacement), 1)); err == nil {
			t.Fatal("accepted lossy surrogate")
		}
	}
}

func TestMCPSupersessionDraftCanonicalIdentity(t *testing.T) {
	source, err := streamSource("alpha")
	if err != nil {
		t.Fatal(err)
	}
	declaration, err := decodeMCPSupersessionDeclaration(marshalSupersessionTest(t, supersessionDraftFixture(false)), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := mousa.NewSupersessionDeclarationID(source.ID, "old", mousa.RepresentationID(bytes.Repeat([]byte{0xaa}, 32)), "new", mousa.RepresentationID(bytes.Repeat([]byte{0xbb}, 32)), "maintainer", "explicit replacement")
	if err != nil || declaration.ID != expected {
		t.Fatalf("canonical declaration identity mismatch: %v", err)
	}
	encoded, err := mousa.EncodeSupersessionDeclaration(declaration)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := mousa.DecodeSupersessionDeclaration(encoded)
	if err != nil || !reflect.DeepEqual(decoded, declaration) {
		t.Fatalf("canonical declaration round trip: %v", err)
	}
	draft := supersessionDraftFixture(true)
	draft["declaration_id"] = declaration.ID.String()
	activation, err := decodeMCPSupersessionActivation(marshalSupersessionTest(t, draft), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	activationID, err := mousa.NewSupersessionActivationID(source.ID, nil, &declaration.ID)
	if err != nil || activation.ID != activationID {
		t.Fatalf("canonical activation identity mismatch: %v", err)
	}
	encoded, err = mousa.EncodeSupersessionActivation(activation)
	if err != nil {
		t.Fatal(err)
	}
	decodedActivation, err := mousa.DecodeSupersessionActivation(encoded)
	if err != nil || !reflect.DeepEqual(decodedActivation, activation) {
		t.Fatalf("canonical activation round trip: %v", err)
	}
	draft["actor_id"], draft["occurred_at_usec"], draft["reason"] = "another", int64(2), "changed metadata"
	changed, err := decodeMCPSupersessionActivation(marshalSupersessionTest(t, draft), source.ID)
	if err != nil || changed.ID != activation.ID || reflect.DeepEqual(changed, activation) {
		t.Fatalf("metadata must change bytes, not identity: %v", err)
	}
	other, _ := streamSource("beta")
	foreign, err := decodeMCPSupersessionActivation(marshalSupersessionTest(t, draft), other.ID)
	if err != nil || foreign.ID == activation.ID {
		t.Fatalf("source must bind identity: %v", err)
	}
}

func TestMCPSupersessionArguments(t *testing.T) {
	for _, activation := range []bool{false, true} {
		operation, field := "mousa_supersession_declare", "declaration"
		if activation {
			operation, field = "mousa_supersession_activate", "activation"
		}
		raw := marshalSupersessionTest(t, map[string]any{"source": "alpha", field: supersessionDraftFixture(activation)})
		args, err := decodeMCPArguments(raw, operation)
		if err != nil {
			t.Fatal(err)
		}
		source, _ := streamSource("alpha")
		if activation && args.Activation.SourceID != source.ID || !activation && args.Declaration.SourceID != source.ID {
			t.Fatal("alias did not bind canonical source")
		}
		for _, invalid := range []string{`{"source":"alpha"}`, `{"source":"alpha","` + field + `":null}`, `{"Source":"alpha","` + field + `":{}}`, string(raw[:len(raw)-1]) + `,"source":"beta"}`, string(raw) + ` {}`, string(raw[:len(raw)-1]) + `,"source_id":"x"}`} {
			if _, err := decodeMCPArguments([]byte(invalid), operation); err == nil {
				t.Errorf("accepted outer arguments %s", invalid)
			}
		}
	}
	if _, err := decodeMCPArguments([]byte(`{"source":"alpha"}`), "mousa_supersession_status"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`{}`, `{"source":null}`, `{"source":"alpha","activation":null}`, `{"source":"alpha","source":"beta"}`} {
		if _, err := decodeMCPArguments([]byte(invalid), "mousa_supersession_status"); err == nil {
			t.Errorf("accepted status arguments %s", invalid)
		}
	}
}

func TestMCPSupersessionDiscoverySchemas(t *testing.T) {
	definitions, err := mcpToolDefinitions([]string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 13 {
		t.Fatalf("discovery contains %d tools, want 13", len(definitions))
	}
	seen := make(map[string]bool)
	for _, tool := range definitions {
		if seen[tool.Name] {
			t.Fatalf("duplicate tool %s", tool.Name)
		}
		seen[tool.Name] = true
		if !strings.HasPrefix(tool.Name, "mousa_supersession_") {
			continue
		}
		var input, output jsonschema.Schema
		if err := json.Unmarshal(marshalSupersessionTest(t, tool.InputSchema), &input); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(marshalSupersessionTest(t, tool.OutputSchema), &output); err != nil {
			t.Fatal(err)
		}
		resolvedInput, err := input.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		resolvedOutput, err := output.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"source": "alpha"}
		switch tool.Name {
		case "mousa_supersession_declare":
			args["declaration"] = supersessionDraftFixture(false)
		case "mousa_supersession_activate":
			args["activation"] = supersessionDraftFixture(true)
		case "mousa_supersession_status":
		default:
			t.Fatalf("unexpected added tool %s", tool.Name)
		}
		var wire any
		if err := json.Unmarshal(marshalSupersessionTest(t, args), &wire); err != nil {
			t.Fatal(err)
		}
		if err := resolvedInput.Validate(wire); err != nil {
			t.Fatalf("%s input schema: %v", tool.Name, err)
		}
		args["source_id"] = strings.Repeat("a", 64)
		if err := resolvedInput.Validate(args); err == nil {
			t.Fatalf("%s input schema is open", tool.Name)
		}
		if tool.Name != "mousa_supersession_status" {
			decoded, err := decodeMCPArguments(marshalSupersessionTest(t, wire), tool.Name)
			if err != nil {
				t.Fatal(err)
			}
			var result any = mcpSupersessionDeclareResult{Declaration: decoded.Declaration}
			if tool.Name == "mousa_supersession_activate" {
				result = mcpSupersessionActivateResult{Activation: decoded.Activation}
			}
			var envelope any
			if err := json.Unmarshal(marshalSupersessionTest(t, mcpEnvelope[any]{Schema: mcpResultVersion, Operation: tool.Name, Source: "alpha", Result: &result}), &envelope); err != nil {
				t.Fatal(err)
			}
			if err := resolvedOutput.Validate(envelope); err != nil {
				t.Fatalf("%s canonical output: %v", tool.Name, err)
			}
			continue
		}
		result := map[string]any{"decision_id": strings.Repeat("a", 64), "decision_outcome": "allow", "decision_reasons": []any{}, "current": map[string]any{"state": "no_history", "activation": nil, "declaration": nil}}
		envelope := map[string]any{"schema": mcpResultVersion, "operation": tool.Name, "source": "alpha", "result": result}
		if err := resolvedOutput.Validate(envelope); err != nil {
			t.Fatalf("no-history schema: %v", err)
		}
		current := result["current"].(map[string]any)
		for _, field := range []string{"state", "activation", "declaration"} {
			value := current[field]
			delete(current, field)
			if err := resolvedOutput.Validate(envelope); err == nil {
				t.Errorf("status schema accepts missing %s", field)
			}
			current[field] = value
		}
		current["state"] = "unknown"
		if err := resolvedOutput.Validate(envelope); err == nil {
			t.Fatal("status schema accepts unknown state")
		}
		delete(result, "current")
		result["decision_outcome"] = "deny"
		if err := resolvedOutput.Validate(envelope); err != nil {
			t.Fatalf("denial schema: %v", err)
		}
	}
}

func TestMCPSupersessionDiscoveryPreservesAcceptedTools(t *testing.T) {
	raw, err := os.ReadFile("testdata/mcp_supersession_admin_baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline mcp.ListToolsResult
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatal(err)
	}
	if len(baseline.Tools) != 8 {
		t.Fatalf("accepted baseline has %d tools, want eight", len(baseline.Tools))
	}
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", false)
	listed, err := client.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	current := make(map[string]*mcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		if current[tool.Name] != nil {
			t.Fatalf("duplicate tool %s", tool.Name)
		}
		current[tool.Name] = tool
	}
	for _, original := range baseline.Tools {
		actual := current[original.Name]
		if actual == nil {
			t.Fatalf("lost accepted tool %s", original.Name)
		}
		var expectedWire, actualWire map[string]any
		if err := json.Unmarshal(marshalSupersessionTest(t, original), &expectedWire); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(marshalSupersessionTest(t, actual), &actualWire); err != nil {
			t.Fatal(err)
		}
		properties := expectedWire["inputSchema"].(map[string]any)["properties"].(map[string]any)
		properties["source"].(map[string]any)["enum"] = []any{"alpha", "beta"}
		if original.Name == "mousa_query" {
			supersession := properties["supersession"].(map[string]any)
			const old = "Withhold an accepted candidate whose own representation is the selected supersession declaration's exact predecessor pin for this source. Requires native declaration and activation administration for the same source; omitted or false keeps the uncurated default."
			if supersession["description"] != old {
				t.Fatal("accepted query description differs from the permitted correction")
			}
			supersession["description"] = "Withhold an accepted candidate whose own representation is the selected supersession declaration's exact predecessor pin for this source. Requires declaration and activation administration for the same source through native commands or permitted MCP tools; omitted or false keeps the uncurated default."
		}
		if !reflect.DeepEqual(expectedWire, actualWire) {
			t.Fatalf("%s changed beyond the exact permitted administration description correction", original.Name)
		}
		delete(current, original.Name)
	}
	for _, name := range []string{"mousa_supersession_declare", "mousa_supersession_activate", "mousa_supersession_status", "mousa_classification_put", "mousa_classification_get"} {
		if current[name] == nil {
			t.Fatalf("missing added tool %s", name)
		}
		delete(current, name)
	}
	if len(current) != 0 {
		t.Fatalf("unexpected added tools: %v", current)
	}
	client.close()
}

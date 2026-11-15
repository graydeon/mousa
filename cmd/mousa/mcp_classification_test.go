package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/graydeon/mousa/internal/mousa"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func classificationDraft(subject any) map[string]any {
	return map[string]any{"schema": mousa.ClassificationInputSchema, "subject": subject, "taxonomy": "review", "taxonomy_version": "1", "label": "asserted", "asserter_id": "supplied.actor", "asserter_version": "1", "basis": []any{subject}, "asserted_at_usec": 1, "confidence_ppm": 0}
}

func TestMCPClassificationStrictDraft(t *testing.T) {
	subject := map[string]any{"kind": "source", "id": strings.Repeat("a", 64)}
	decode := func(raw []byte) error {
		_, err := decodeMCPArguments(append(append([]byte(`{"source":"alpha","classification":`), raw...), '}'), "mousa_classification_put")
		return err
	}
	fixture := classificationDraft(subject)
	raw := mustJSON(t, fixture)
	if err := decode(raw); err != nil {
		t.Fatal(err)
	}
	for key := range fixture {
		for _, mode := range []string{"omitted", "null", "case", "duplicate", "boolean"} {
			t.Run(key+"/"+mode, func(t *testing.T) {
				draft := classificationDraft(subject)
				switch mode {
				case "omitted":
					delete(draft, key)
				case "null":
					draft[key] = nil
				case "case":
					delete(draft, key)
					draft[strings.ToUpper(key)] = fixture[key]
				case "boolean":
					draft[key] = false
				}
				invalid := mustJSON(t, draft)
				if mode == "duplicate" {
					invalid = append(append([]byte(nil), raw[:len(raw)-1]...), []byte(fmt.Sprintf(",%q:%s}", key, mustJSON(t, fixture[key])))...)
				}
				if err := decode(invalid); err == nil {
					t.Fatal("accepted invalid draft")
				}
			})
		}
	}
	for _, location := range []string{"subject", "basis"} {
		for _, invalid := range []string{`{"kind":"source","id":"` + strings.Repeat("a", 64) + `","id":"` + strings.Repeat("b", 64) + `"}`, `{"Kind":"source","id":"` + strings.Repeat("a", 64) + `"}`, `{"kind":null,"id":"` + strings.Repeat("a", 64) + `"}`, `{"kind":"source","id":false}`, `{"kind":"source","id":"` + strings.Repeat("0", 64) + `"}`, `{"kind":"source","id":"` + strings.Repeat("A", 64) + `"}`, `{"kind":"source","id":"` + strings.Repeat("a", 64) + `","extra":1}`} {
			draft := classificationDraft(subject)
			if location == "basis" {
				draft[location] = []any{json.RawMessage(invalid)}
			} else {
				draft[location] = json.RawMessage(invalid)
			}
			if err := decode(mustJSON(t, draft)); err == nil {
				t.Fatalf("accepted invalid %s", location)
			}
		}
	}
	for _, key := range []string{"asserted_at_usec", "confidence_ppm"} {
		for _, literal := range []string{`"0"`, "true", "0.0", "1e0", "-1", "18446744073709551616"} {
			draft := classificationDraft(subject)
			draft[key] = json.RawMessage(literal)
			if err := decode(mustJSON(t, draft)); err == nil {
				t.Fatalf("accepted %s=%s", key, literal)
			}
		}
	}
	for _, test := range []struct {
		field string
		value any
		valid bool
	}{
		{"asserted_at_usec", int64(9223372036854775807), true},
		{"asserted_at_usec", 0, false},
		{"asserted_at_usec", json.RawMessage("9223372036854775808"), false},
		{"confidence_ppm", 1000000, true},
		{"confidence_ppm", 1000001, false},
		{"label", "caller\u0000assertion", true},
		{"taxonomy", strings.Repeat("é", 32768), true},
		{"label", 42, false},
	} {
		draft := classificationDraft(subject)
		draft[test.field] = test.value
		if err := decode(mustJSON(t, draft)); (err == nil) != test.valid {
			t.Fatalf("%s boundary: %v", test.field, err)
		}
	}
	for _, key := range []string{"id", "source_id", "unknown"} {
		draft := classificationDraft(subject)
		draft[key] = strings.Repeat("a", 64)
		if err := decode(mustJSON(t, draft)); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	for _, kind := range []string{"source", "observation", "artifact", "representation", "segment"} {
		draft := classificationDraft(map[string]any{"kind": kind, "id": strings.Repeat("a", 64)})
		if err := decode(mustJSON(t, draft)); err != nil {
			t.Fatal(err)
		}
	}
	for _, count := range []int{4096, 4097} {
		draft := classificationDraft(subject)
		basis := make([]any, count)
		for i := range basis {
			basis[i] = map[string]any{"kind": "source", "id": fmt.Sprintf("%064x", i+1)}
		}
		draft["basis"] = basis
		if err := decode(mustJSON(t, draft)); (err == nil) != (count == 4096) {
			t.Fatalf("basis count %d: %v", count, err)
		}
	}
	duplicate := classificationDraft(subject)
	duplicate["basis"] = []any{subject, subject}
	if decode(mustJSON(t, duplicate)) == nil {
		t.Fatal("accepted duplicate basis")
	}
	padded := append(append([]byte(nil), raw[:len(raw)-1]...), bytes.Repeat([]byte(" "), mousa.MaxClassificationInputBytes-len(raw))...)
	padded = append(padded, '}')
	if err := decode(padded); err != nil {
		t.Fatal(err)
	}
	oversized := append(append([]byte(nil), padded[:len(padded)-1]...), ' ', '}')
	if decode(oversized) == nil {
		t.Fatal("accepted oversized draft")
	}
	for _, raw := range []string{`{}`, `{"source":null,"classification_id":"` + strings.Repeat("a", 64) + `"}`, `{"source":"alpha","classification_id":"` + strings.Repeat("0", 64) + `"}`, `{"source":"alpha","classification_id":"` + strings.Repeat("a", 64) + `","source":"beta"}`, `{"source":"alpha","classification_id":"` + strings.Repeat("a", 64) + `"} {}`} {
		if _, err := decodeMCPArguments([]byte(raw), "mousa_classification_get"); err == nil {
			t.Fatal("accepted invalid get")
		}
	}
}

func TestMCPClassificationDiscoveryBaseline(t *testing.T) {
	raw, err := os.ReadFile("testdata/mcp_classification_baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatal(err)
	}
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", false)
	defer client.close()
	listed, err := client.session.ListTools(t.Context(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Tools) != 11 || len(listed.Tools) != 14 {
		t.Fatal("unexpected discovery counts")
	}
	current := map[string]map[string]any{}
	for _, tool := range listed.Tools {
		var wire map[string]any
		if err := json.Unmarshal(mustJSON(t, tool), &wire); err != nil {
			t.Fatal(err)
		}
		current[tool.Name] = wire
	}
	for _, tool := range baseline.Tools {
		tool["inputSchema"].(map[string]any)["properties"].(map[string]any)["source"].(map[string]any)["enum"] = []any{"alpha", "beta"}
		name := tool["name"].(string)
		if !reflect.DeepEqual(tool, current[name]) {
			t.Fatalf("existing tool %s changed beyond the allowed two additions", name)
		}
	}
	definitions, err := mcpToolDefinitions([]string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range definitions {
		if !strings.HasPrefix(tool.Name, "mousa_classification_") {
			continue
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(mustJSON(t, tool.InputSchema), &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"source": "alpha", "classification_id": strings.Repeat("a", 64)}
		if tool.Name == "mousa_classification_put" {
			delete(args, "classification_id")
			args["classification"] = classificationDraft(map[string]any{"kind": "source", "id": strings.Repeat("a", 64)})
		}
		var wire any
		json.Unmarshal(mustJSON(t, args), &wire)
		if err := resolved.Validate(wire); err != nil {
			t.Fatal(err)
		}
		args["caller_id"] = "forbidden"
		if err := resolved.Validate(args); err == nil {
			t.Fatal("open schema")
		}
	}
}

func TestMCPClassificationFlagValidation(t *testing.T) {
	for _, flags := range [][]string{{"--classification-admin-source", "beta"}, {"--classification-admin-source", "alpha", "--classification-admin-source", "alpha"}} {
		run, _ := setup(t)
		run.store = filepath.Join(t.TempDir(), "absent.sqlite")
		args := append([]string{"mcp", "--caller", "cli", "--source", "alpha"}, flags...)
		code, _, stderr := run.runExit(args...)
		if code != 2 || !strings.Contains(stderr, "distinct permitted --source") {
			t.Fatalf("exit=%d: %s", code, stderr)
		}
		if _, err := os.Stat(run.store); !os.IsNotExist(err) {
			t.Fatalf("invalid flags created store: %v", err)
		}
	}
}

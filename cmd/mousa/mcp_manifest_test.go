package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/graydeon/mousa/internal/mousa"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func manifestAdapterState(t *testing.T, path string) string {
	t.Helper()
	state := classificationState(t, path)
	for _, table := range []string{"policy_decisions", "policy_decision_inputs", "source_trails"} {
		count, digest := tableDigest(t, path, table)
		state += fmt.Sprintf("%s:%d/%s;", table, count, digest)
	}
	return state
}

func invalidManifestArguments() []string {
	valid := strings.Repeat("a", 64)
	cases := []string{`{}`, `[]`, `null`, `{"source":null}`, `{"source":1}`, `{"Source":"alpha"}`, `{"source":"alpha","source":"beta"}`, `{"source":"alpha","unexpected":true}`, `{"source":"alpha","caller_id":"cli"}`, `{"source":"alpha","expected_manifest_id":"` + valid + `","expected_manifest_id":"` + valid + `"}`}
	for _, value := range []string{`null`, `true`, `1`, `[]`, `{}`, `""`, `"` + strings.Repeat("0", 64) + `"`, `"` + strings.Repeat("A", 64) + `"`, `"` + strings.Repeat("a", 63) + `"`, `"` + strings.Repeat("a", 65) + `"`, `" ` + valid + `"`, `"\ud800"`} {
		cases = append(cases, `{"source":"alpha","expected_manifest_id":`+value+`}`)
	}
	return cases
}

func TestMCPManifestStrictArguments(t *testing.T) {
	for _, raw := range append(invalidManifestArguments(), `{"source":"alpha"} {}`) {
		if _, err := decodeMCPArguments([]byte(raw), "mousa_source_manifest"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"source":"alpha"}`, `{"source":"alpha","expected_manifest_id":"` + strings.Repeat("a", 64) + `"}`} {
		if _, err := decodeMCPArguments([]byte(raw), "mousa_source_manifest"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMCPManifestStdioSnapshotAndPermissions(t *testing.T) {
	run, _ := setup(t)
	run.runInput("{\"id\":\"a\",\"text\":\"cedar\"}\n", false, "sync", "--source", "alpha")
	_, native := run.run(false, "manifest", "--source", "alpha")
	id := manifestID(t, native)
	for mask := range 8 {
		var startup []string
		if mask&2 != 0 {
			startup = append(startup, "--supersession-admin-source", "alpha")
		}
		if mask&4 != 0 {
			startup = append(startup, "--classification-admin-source", "alpha")
		}
		client := startMCPClient(t, run, "cli", mask&1 != 0, startup...)
		result := client.call("mousa_source_manifest", map[string]any{"source": "alpha"}, "")
		if !reflect.DeepEqual(result["manifest"], native["manifest"]) || result["matches"] != nil || result["decision_id"] == native["decision_id"] {
			t.Fatalf("mask %d changed manifest/export/fresh authorization: %v", mask, result)
		}
		matched := client.call("mousa_source_manifest", map[string]any{"source": "alpha", "expected_manifest_id": id}, "")
		if matched["matches"] != true {
			t.Fatal("expected match missing")
		}
		mismatch := client.call("mousa_source_manifest", map[string]any{"source": "alpha", "expected_manifest_id": strings.Repeat("a", 64)}, "")
		if mismatch["matches"] != false || manifestID(t, mismatch) != id {
			t.Fatal("mismatch is not an ordinary complete result")
		}
		client.close()
	}
	unauthorized := startMCPClient(t, run, "untrusted", true, "--supersession-admin-source", "alpha", "--classification-admin-source", "alpha")
	assertManifestDenied(t, unauthorized.call("mousa_source_manifest", map[string]any{"source": "alpha", "expected_manifest_id": id}, ""))
	unauthorized.close()
	client := startMCPClient(t, run, "cli", false)
	defer client.close()
	before := manifestAdapterState(t, run.store)
	for _, raw := range invalidManifestArguments() {
		client.call("mousa_source_manifest", json.RawMessage(raw), "invalid_arguments")
	}
	if manifestAdapterState(t, run.store) != before {
		t.Fatal("invalid arguments mutated store")
	}
	client.call("mousa_source_manifest", map[string]any{"source": "/not/configured"}, "source_not_permitted")
	unknown := client.call("mousa_source_manifest", map[string]any{"source": "beta", "expected_manifest_id": id}, "")
	assertManifestDenied(t, unknown)
	run.run(false, "access", "--source", "alpha", "deny")
	rawExec(t, run.store, `UPDATE representations SET record_json=x'7b7d0a'`)
	for _, expected := range []string{id, strings.Repeat("b", 64)} {
		assertManifestDenied(t, client.call("mousa_source_manifest", map[string]any{"source": "alpha", "expected_manifest_id": expected}, ""))
	}
	assertManifestDenied(t, client.call("mousa_source_manifest", map[string]any{"source": "alpha"}, ""))
}

func TestMCPManifestSerializedEnvelopeBound(t *testing.T) {
	for _, character := range []string{"x", "\"", "<", "\u2028"} {
		t.Run(fmt.Sprintf("%x", character), func(t *testing.T) {
			run, _ := setup(t)
			client := startMCPClient(t, run, "cli", true)
			defer client.close()
			var input strings.Builder
			for i := range 90 {
				id := fmt.Sprintf("%03d", i) + strings.Repeat(character, (4090-3)/len(character))
				input.Write(mustJSON(t, map[string]any{"id": id, "text": ""}))
				input.WriteByte('\n')
			}
			run.runInput(input.String(), false, "sync", "--source", "alpha")
			response, err := client.session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mousa_source_manifest", Arguments: map[string]any{"source": "alpha"}})
			if err != nil {
				t.Fatal(err)
			}
			wire := mustJSON(t, map[string]any{"jsonrpc": "2.0", "id": 1, "result": response})
			if len(wire)+1 > mcpMaxFrameBytes {
				t.Fatalf("serialized MCP envelope exceeds frame bound: %d", len(wire)+1)
			}
			var envelope map[string]any
			if err := json.Unmarshal(mustJSON(t, response.StructuredContent), &envelope); err != nil {
				t.Fatal(err)
			}
			if response.IsError {
				if envelope["error"].(map[string]any)["code"] != "resource_limit" || envelope["result"] != nil {
					t.Fatalf("oversized manifest did not fail closed: %v", envelope)
				}
				if character == "x" {
					t.Fatal("ordinary near-limit manifest unexpectedly rejected")
				}
				return
			}
			payload := envelope["result"].(map[string]any)
			manifest := payload["manifest"].(map[string]any)
			if len(manifest["items"].([]any)) != 90 {
				t.Fatal("manifest truncated to fit transport")
			}
			if character == "x" && len(mustJSON(t, manifest)) < mousa.MaxSourceManifestBytes*3/4 {
				t.Fatal("fixture did not exercise near-bound serialization")
			}
			if err := client.schemas["mousa_source_manifest"].Validate(envelope); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMCPManifestDiscovery(t *testing.T) {
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", false)
	defer client.close()
	listed, err := client.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var found *mcp.Tool
	for _, tool := range listed.Tools {
		if tool.Name == "mousa_source_manifest" {
			found = tool
		}
	}
	if found == nil || found.Title == "" || found.Annotations == nil || found.Annotations.DestructiveHint == nil || *found.Annotations.DestructiveHint || mcpWriteTool(found.Name) {
		t.Fatalf("manifest missing or advertised as administration: %+v", found)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(mustJSON(t, found.InputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{{"source": "alpha"}, {"source": "beta", "expected_manifest_id": strings.Repeat("a", 64)}} {
		if err := resolved.Validate(args); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range []map[string]any{{}, {"source": "unconfigured"}, {"source": "alpha", "expected_manifest_id": nil}, {"source": "alpha", "expected_manifest_id": strings.Repeat("0", 64)}, {"source": "alpha", "expected_manifest_id": strings.Repeat("A", 64)}, {"source": "alpha", "caller_id": "cli"}} {
		if err := resolved.Validate(args); err == nil {
			t.Fatalf("discovery schema accepts invalid arguments: %v", args)
		}
	}
}

func TestMCPManifestPreservesAcceptedDiscovery(t *testing.T) {
	run, _ := setup(t)
	command := exec.Command(run.binary, "-store", run.store, "mcp", "--caller", "cli", "--source", "alpha")
	client := mcp.NewClient(&mcp.Implementation{Name: "manifest-discovery", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: command, TerminateDuration: 2 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/mcp_manifest_baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	var baseline struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal(raw, &baseline); err != nil {
		t.Fatal(err)
	}
	if len(baseline.Tools) != 13 || len(listed.Tools) != 14 {
		t.Fatalf("wrong baseline/current discovery sizes: %d/%d", len(baseline.Tools), len(listed.Tools))
	}
	current := make(map[string]map[string]any)
	for _, tool := range listed.Tools {
		var object map[string]any
		if err := json.Unmarshal(mustJSON(t, tool), &object); err != nil {
			t.Fatal(err)
		}
		current[tool.Name] = object
	}
	for _, tool := range baseline.Tools {
		name := tool["name"].(string)
		if !reflect.DeepEqual(current[name], tool) {
			t.Errorf("accepted discovery object changed: %s", name)
		}
	}
	if current["mousa_source_manifest"] == nil {
		t.Fatal("new manifest tool missing")
	}
}

func TestMCPManifestStdioLargeStringRequestID(t *testing.T) {
	for _, id := range []string{strings.Repeat("x", 1023), strings.Repeat("x", 3*mcpMaxFrameBytes/4), strings.Repeat("<", 200)} {
		t.Run(fmt.Sprintf("encoded-%d", len(mustJSON(t, id))), func(t *testing.T) {
			run, _ := setup(t)
			run.runInput("{\"id\":\"a\",\"text\":\"cedar\"}\n", false, "sync", "--source", "alpha")
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, run.binary, "-store", run.store, "mcp", "--caller", "cli", "--source", "alpha")
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { input.Close(); command.Process.Kill() })
			send := json.NewEncoder(input)
			if err := send.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": mcpProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "raw-manifest", "version": "1"}}}); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(output)
			if _, err := reader.ReadBytes('\n'); err != nil {
				t.Fatal(err)
			}
			if err := send.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
				t.Fatal(err)
			}
			allowedID := strings.Repeat("x", 1022)
			if err := send.Encode(map[string]any{"jsonrpc": "2.0", "id": allowedID, "method": "tools/call", "params": map[string]any{"name": "mousa_source_manifest", "arguments": map[string]any{"source": "alpha"}}}); err != nil {
				t.Fatal(err)
			}
			accepted, err := reader.ReadBytes('\n')
			if err != nil || len(accepted) > mcpMaxFrameBytes {
				t.Fatalf("maximum permitted ID response: %d bytes, %v", len(accepted), err)
			}
			var response struct {
				ID     string             `json:"id"`
				Result mcp.CallToolResult `json:"result"`
			}
			if err := json.Unmarshal(accepted, &response); err != nil || response.ID != allowedID || response.Result.IsError || response.Result.StructuredContent == nil {
				t.Fatalf("maximum permitted ID did not round-trip: %v", err)
			}
			before := manifestAdapterState(t, run.store)
			if err := send.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "mousa_source_manifest", "arguments": map[string]any{"source": "alpha"}}}); err != nil {
				t.Fatal(err)
			}
			wire, readErr := reader.ReadBytes('\n')
			if len(wire) != 0 || readErr == nil {
				t.Fatalf("oversized ID reached response serialization: %d bytes, %v", len(wire), readErr)
			}
			if err := command.Wait(); err == nil || ctx.Err() != nil || !strings.Contains(stderr.String(), "manifest request ID exceeds transport limit") {
				t.Fatalf("wrong transport rejection: %v, %s", err, stderr.String())
			}
			if manifestAdapterState(t, run.store) != before {
				t.Fatal("oversized request ID reached manifest inspection")
			}
		})
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPEmbeddingDiscoveryAndDispatch(t *testing.T) {
	run, _ := setup(t)
	command := exec.Command(run.binary, "-store", run.store, "mcp", "--caller", "cli", "--source", "alpha")
	client := mcp.NewClient(&mcp.Implementation{Name: "embedding-client", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: command, TerminateDuration: 2 * time.Second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mousa_vector_inputs", "mousa_vector_import"} {
		found := false
		for _, tool := range tools.Tools {
			if tool.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("tools/list omits %s", name)
		}
		response, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err != nil {
			t.Errorf("%s not dispatched: %v", name, err)
			continue
		}
		if !response.IsError {
			t.Errorf("%s accepted missing arguments", name)
		}
	}
}

func embeddingImport(t *testing.T, passages []any) map[string]any {
	t.Helper()
	entries := []any{}
	for _, raw := range passages {
		p := raw.(map[string]any)
		e := map[string]any{"values": []float64{0, 1}}
		if p["item"] == "vector" {
			e["values"] = []float64{1, 0}
		}
		for _, key := range []string{"source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256"} {
			e[key] = p[key]
		}
		entries = append(entries, e)
	}
	return map[string]any{"schema": "mousa.vector_import.v1", "space": map[string]any{
		"schema": "mousa.vector_space.v1", "provider": "synthetic", "model": "arithmetic-only", "version": "1", "dimensions": 2, "metric": "cosine",
	}, "embeddings": entries}
}

func embeddingState(t *testing.T, path string) string {
	t.Helper()
	var state string
	for _, table := range []string{"vector_spaces", "embeddings"} {
		count, digest := tableDigest(t, path, table)
		state += fmt.Sprintf("%s:%d/%s;", table, count, digest)
	}
	return state
}

func TestMCPEmbeddingWorkflowAndWriteBoundary(t *testing.T) {
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", true)
	client.call("mousa_sync", mcpItems("alpha",
		map[string]any{"id": "overlap", "text": "\ufeffDrain sockets before stopping the host.\r\n"},
		map[string]any{"id": "vector", "text": "Radishes prefer damp soil."},
		map[string]any{"id": "lexical", "text": "Drain sockets."}), "")
	exported := client.call("mousa_vector_inputs", map[string]any{"source": "alpha"}, "")
	_, native := run.run(false, "vector", "inputs", "--source", "alpha")
	if !reflect.DeepEqual(exported["passages"], native["passages"]) || exported["decision_outcome"] != "allow" || exported["decision_id"] == native["decision_id"] {
		t.Fatalf("export is not exact/fresh: %v versus %v", exported, native)
	}
	store, err := sqlite.Open(t.Context(), run.store)
	if err != nil {
		t.Fatal(err)
	}
	id, err := mousa.ParsePolicyDecisionID(exported["decision_id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	decision, err := store.GetPolicyDecision(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	source, _ := streamSource("alpha")
	expected, err := callerRetrievalRequest(source.ID, "cli")
	if err != nil {
		t.Fatal(err)
	}
	request := decision.Request
	if request.SourceID != source.ID || request.Action != "source.retrieve" ||
		request.CallerNamespace != expected.CallerNamespace || request.ExternalCallerID != expected.ExternalCallerID ||
		request.PurposeNamespace != expected.PurposeNamespace || request.ExternalPurposeID != expected.ExternalPurposeID {
		t.Fatalf("export authorization scope changed: %+v", request)
	}
	passages := exported["passages"].([]any)
	// Partial coverage makes the known second-ranked overlap win RRF.
	selected := []any{}
	for _, raw := range passages {
		p := raw.(map[string]any)
		if p["item"] == "overlap" && p["text"] != "Drain sockets before stopping the host.\n" {
			t.Fatalf("normalized bytes changed: %v", p)
		}
		if p["item"] != "lexical" {
			selected = append(selected, raw)
		}
	}
	input := embeddingImport(t, selected)
	args := map[string]any{"source": "alpha", "import": input}
	client.close()
	read := startMCPClient(t, run, "cli", false)
	before := embeddingState(t, run.store)
	read.call("mousa_vector_import", args, "ingestion_not_permitted")
	read.call("mousa_vector_inputs", map[string]any{"source": "alpha"}, "")
	if embeddingState(t, run.store) != before {
		t.Fatal("read-only client inserted vectors")
	}
	read.close()
	client = startMCPClient(t, run, "cli", true)
	imported := client.call("mousa_vector_import", args, "")
	if imported["imported"] != float64(2) {
		t.Fatalf("submitted count: %v", imported)
	}
	state := embeddingState(t, run.store)
	retry := client.call("mousa_vector_import", args, "")
	if !reflect.DeepEqual(imported, retry) || embeddingState(t, run.store) != state {
		t.Fatal("immutable retry changed rows or identity")
	}
	entries := input["embeddings"].([]any)
	entry := entries[0].(map[string]any)
	old := entry["values"]
	entry["values"] = []float64{-1, 0}
	client.call("mousa_vector_import", args, "conflict")
	entry["values"] = old
	if embeddingState(t, run.store) != state {
		t.Fatal("conflict changed rows")
	}
	for _, mode := range []string{"vector", "hybrid"} {
		for _, packing := range []string{"original", "exact-v1"} {
			query := mcpRetrievalArgs(imported["space_id"].(string), mode, packing, 40)
			result := client.call("mousa_"+mode+"_query", query, "")
			want := "vector"
			if mode == "hybrid" {
				want = "overlap"
			}
			if result["evidence"].([]any)[0].(map[string]any)["item"] != want {
				t.Fatalf("ranking: %v", result)
			}
			h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": result["trail_id"], "retrieval_details": true}, "")["historical"].(map[string]any)
			if h["packet_id"] != result["packet_id"] || !reflect.DeepEqual(h[mode], result[mode]) {
				t.Fatal("producing history changed")
			}
		}
	}
	client.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "overlap", "text": "Replacement revision."}), "")
	input["space"].(map[string]any)["version"] = "stale"
	client.call("mousa_vector_import", args, "invalid_record")
	if embeddingState(t, run.store) != state {
		t.Fatal("mixed stale batch inserted embedding or space")
	}
	fresh := client.call("mousa_vector_inputs", map[string]any{"source": "alpha"}, "")
	freshInput := embeddingImport(t, fresh["passages"].([]any))
	freshInput["space"].(map[string]any)["version"] = "fresh"
	client.call("mousa_vector_import", map[string]any{"source": "alpha", "import": freshInput}, "")
	client.call("mousa_vector_inputs", map[string]any{"source": "/unconfigured/path"}, "source_not_permitted")
	run.run(false, "access", "--source", "alpha", "deny")
	denied := client.call("mousa_vector_inputs", map[string]any{"source": "alpha"}, "")
	if denied["decision_outcome"] != "deny" || len(denied["passages"].([]any)) != 0 {
		t.Fatalf("denied export leaked: %v", denied)
	}
	// Explicit administration stays possible despite retrieval denial.
	freshInput["space"].(map[string]any)["version"] = "denied-admin"
	client.call("mousa_vector_import", map[string]any{"source": "alpha", "import": freshInput}, "")
	run.run(false, "withdraw", "--source", "alpha")
	denied = client.call("mousa_vector_inputs", map[string]any{"source": "alpha"}, "")
	if denied["decision_outcome"] != "deny" || len(denied["passages"].([]any)) != 0 {
		t.Fatalf("withdrawn export leaked: %v", denied)
	}
	state = embeddingState(t, run.store)
	client.call("mousa_vector_import", map[string]any{"source": "alpha", "import": freshInput}, "invalid_record")
	if embeddingState(t, run.store) != state {
		t.Fatal("withdrawn import changed rows")
	}
}

func TestMCPEmbeddingStrictImportAtomicity(t *testing.T) {
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", true)
	client.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "a", "text": "Drain"}), "")
	exported := client.call("mousa_vector_inputs", map[string]any{"source": "alpha"}, "")
	input := embeddingImport(t, exported["passages"].([]any))
	base := string(mustJSON(t, input))
	bad := []string{`null`, `[]`, `{}`, strings.Replace(base, `"space":{`, `"space":{"unknown":1,`, 1),
		strings.Replace(base, `"schema":"mousa.vector_import.v1"`, `"schema":"mousa.vector_import.v1","schema":"mousa.vector_import.v1"`, 1),
		strings.Replace(base, `"provider":"synthetic"`, `"provider":"synthetic","provider":"synthetic"`, 1),
		strings.Replace(base, `"values":[0,1]`, `"values":[0,1],"values":[0,1]`, 1),
		strings.Replace(base, `"dimensions":2`, `"dimensions":1`, 1),
		strings.Replace(base, `"dimensions":2`, `"dimensions":2049`, 1),
		strings.Replace(base, `"metric":"cosine"`, `"metric":"other"`, 1)}
	for _, vector := range []string{`[true,1]`, `[null,1]`, `[0,0]`, `[1e999,0]`, `[]`, `null`, `[` + strings.Repeat("1,", 2048) + `1]`} {
		bad = append(bad, strings.Replace(base, `"values":[0,1]`, `"values":`+vector, 1))
	}
	for _, key := range []string{"source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256"} {
		clone := embeddingImport(t, exported["passages"].([]any))
		e := clone["embeddings"].([]any)[0].(map[string]any)
		delete(e, key)
		bad = append(bad, string(mustJSON(t, clone)))
		e[key] = nil
		bad = append(bad, string(mustJSON(t, clone)))
	}
	clone := embeddingImport(t, exported["passages"].([]any))
	entries := clone["embeddings"].([]any)
	clone["embeddings"] = append(entries, entries[0])
	bad = append(bad, string(mustJSON(t, clone)))
	state := embeddingState(t, run.store)
	for _, value := range bad {
		client.call("mousa_vector_import", json.RawMessage(`{"source":"alpha","import":`+value+`}`), "invalid_arguments")
		if embeddingState(t, run.store) != state {
			t.Fatal("malformed import mutated vectors")
		}
	}
	// A valid foreign-source pin fails inside native atomic current-pin validation.
	client.call("mousa_sync", mcpItems("beta", map[string]any{"id": "b", "text": "Foreign"}), "")
	foreign := client.call("mousa_vector_inputs", map[string]any{"source": "beta"}, "")
	mixed := append(exported["passages"].([]any), foreign["passages"].([]any)...)
	client.call("mousa_vector_import", map[string]any{"source": "alpha", "import": embeddingImport(t, mixed)}, "invalid_record")
	if embeddingState(t, run.store) != state {
		t.Fatal("foreign mixed batch partially committed")
	}
}

func TestMCPEmbeddingBoundsAndAdmission(t *testing.T) {
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", true)
	items := []any{}
	for i := range 100 {
		items = append(items, map[string]any{"id": fmt.Sprintf("item-%03d", i), "text": "Drain"})
	}
	client.call("mousa_sync", mcpItems("alpha", items...), "")
	exported := client.call("mousa_vector_inputs", map[string]any{"source": "alpha"}, "")
	input := embeddingImport(t, exported["passages"].([]any))
	input["space"].(map[string]any)["dimensions"] = 2048
	for _, raw := range input["embeddings"].([]any) {
		values := make([]float64, 2048)
		values[0] = 1
		raw.(map[string]any)["values"] = values
	}
	args := map[string]any{"source": "alpha", "import": input}
	result := client.call("mousa_vector_import", args, "")
	if result["imported"] != float64(100) {
		t.Fatalf("exact item/dimension bound: %v", result)
	}
	state := embeddingState(t, run.store)
	entries := input["embeddings"].([]any)
	input["embeddings"] = append(entries, entries[0])
	client.call("mousa_vector_import", args, "invalid_arguments")
	if embeddingState(t, run.store) != state {
		t.Fatal("oversized batch mutated vectors")
	}
	input["embeddings"] = entries
	// Whitespace can exceed the codec bound independently of transport framing.
	raw := mustJSON(t, input)
	raw = []byte(`{"source":"alpha","import":` + string(raw[:len(raw)-1]) + strings.Repeat(" ", 8<<20) + "}}")
	if _, err := decodeMCPArguments(raw, "mousa_vector_import"); err == nil {
		t.Fatal("codec oversized input accepted")
	}
	client.close()
	// A frame can be below the codec bound but above the unchanged stdio bound.
	frame := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mousa_vector_import","arguments":` +
		string(mustJSON(t, args)) + strings.Repeat(" ", mcpMaxFrameBytes) + `}}` + "\n"
	command := exec.Command(run.binary, "-store", run.store, "mcp", "--caller", "cli", "--source", "alpha", "--ingest-source", "alpha")
	command.Stdin = strings.NewReader(frame)
	output, err := command.Output()
	if err == nil || len(output) != 0 {
		t.Fatalf("process frame accepted: %v %q", err, output)
	}
	if embeddingState(t, run.store) != state {
		t.Fatal("frame refusal mutated vectors")
	}
	// Unconfigured source rejection must not touch a store, even for path-like aliases.
	app := &mcpApplication{}
	response, err := app.call(t.Context(), "mousa_vector_inputs", []byte(`{"source":"/not/configured"}`))
	if err != nil || !response.IsError || !strings.Contains(string(mustJSON(t, response.StructuredContent)), "source_not_permitted") {
		t.Fatalf("source admission: %v %v", response, err)
	}
}

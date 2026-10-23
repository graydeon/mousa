package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/sqlite"
)

func TestHTTPSubjectAudienceScopeAndRevocation(t *testing.T) {
	run, _ := setup(t)
	run.runInput(`{"id":"note","text":"Cedar HTTP evidence."}`+"\n", false, "sync", "--source", "alpha")
	store, err := sqlite.Open(t.Context(), run.store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	source, _ := streamSource("alpha")
	application := &mcpApplication{store: store, path: run.store, caller: "cli", sources: map[string]mcpSource{"alpha": {source: source, write: true}}, gate: make(chan struct{}, 1), slots: make(chan struct{}, 8), openai: true, http: true}
	server, err := application.server(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	config := &mcpHTTPConfig{Resource: "https://mousa.example/mcp", Subject: "account-one", ClientID: "resource-client", secret: "fixture-secret"}
	var revoked atomic.Bool
	identity := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != config.ClientID || secret != config.secret {
			t.Error("introspection credentials not supplied correctly")
			http.Error(w, "unauthorized", 401)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		token := r.Form.Get("token")
		claims := map[string]any{"active": !revoked.Load(), "iss": config.Issuer, "sub": config.Subject, "aud": config.Resource, "exp": time.Now().Unix() + 3600, "scope": "mousa:read"}
		switch token {
		case "wrong-subject":
			claims["sub"] = "account-two"
		case "wrong-audience":
			claims["aud"] = "https://other.example/mcp"
		case "wrong-issuer":
			claims["iss"] = "https://other.example"
		case "expired":
			claims["exp"] = time.Now().Unix() - 1
		case "inactive":
			claims["active"] = false
		case "no-scope":
			claims["scope"] = "unrelated"
		case "write":
			claims["scope"] = "mousa:read mousa:write"
		case "read", "oversized":
		default:
			claims["active"] = false
		}
		json.NewEncoder(w).Encode(claims)
		if token == "oversized" {
			io.WriteString(w, strings.Repeat(" ", 16385))
		}
	}))
	t.Cleanup(identity.Close)
	config.Issuer, config.IntrospectionURL = identity.URL, identity.URL
	endpoint := httptest.NewServer(mcpHTTPHandler(server, config, identity.Client()))
	t.Cleanup(endpoint.Close)
	config.Listen = strings.TrimPrefix(endpoint.URL, "http://")
	post := func(token, operation string, args any) (int, map[string]any) {
		encoded, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": operation, "arguments": args}})
		request, _ := http.NewRequest(http.MethodPost, endpoint.URL+"/mcp", bytes.NewReader(encoded))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := endpoint.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		var result map[string]any
		if response.StatusCode == 200 {
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatalf("HTTP JSON: %v %s", err, body)
			}
		}
		return response.StatusCode, result
	}
	for _, token := range []string{"", "wrong-subject", "wrong-audience", "wrong-issuer", "expired", "inactive", "no-scope", "oversized"} {
		code, _ := post(token, "mousa_status", map[string]any{"source": "alpha"})
		wanted := 401
		if token == "no-scope" {
			wanted = 403
		}
		if code != wanted {
			t.Fatalf("token %s: status %d want %d", token, code, wanted)
		}
	}
	code, result := post("read", "mousa_query", mcpQuery("alpha", "cedar", 256))
	if code != 200 || !strings.Contains(string(mustJSON(t, result)), "Cedar HTTP evidence.") {
		t.Fatalf("authorized HTTP query: %d %v", code, result)
	}
	_, result = post("read", "mousa_sync", mcpItems("alpha", map[string]any{"id": "write", "text": "Cannot write with read scope"}))
	if !strings.Contains(string(mustJSON(t, result)), "insufficient_scope") {
		t.Fatalf("read token changed content: %v", result)
	}
	_, result = post("write", "mousa_sync", mcpItems("alpha", map[string]any{"id": "write", "text": "Authorized HTTP ingestion"}))
	if !strings.Contains(string(mustJSON(t, result)), `"added":["write"]`) {
		t.Fatalf("write scope not enforced correctly: %v", result)
	}
	_, exported := post("read", "mousa_vector_inputs", map[string]any{"source": "alpha"})
	inputs := exported["result"].(map[string]any)["structuredContent"].(map[string]any)["result"].(map[string]any)
	importArgs := map[string]any{"source": "alpha", "import": embeddingImport(t, inputs["passages"].([]any))}
	before := embeddingState(t, run.store)
	_, result = post("read", "mousa_vector_import", importArgs)
	if !strings.Contains(string(mustJSON(t, result)), "insufficient_scope") || embeddingState(t, run.store) != before {
		t.Fatalf("read token imported vectors: %v", result)
	}
	_, result = post("write", "mousa_vector_import", importArgs)
	if !strings.Contains(string(mustJSON(t, result)), `"imported":2`) || embeddingState(t, run.store) == before {
		t.Fatalf("write token failed to import vectors: %v", result)
	}
	revoked.Store(true)
	if code, _ := post("read", "mousa_status", map[string]any{"source": "alpha"}); code != 401 {
		t.Fatalf("revoked token remained usable: %d", code)
	}
	for header, value := range map[string]string{"Host": "attacker.example", "Origin": "https://attacker.example"} {
		request, _ := http.NewRequest(http.MethodGet, endpoint.URL+"/mcp", nil)
		if header == "Host" {
			request.Host = value
		} else {
			request.Header.Set(header, value)
		}
		response, err := endpoint.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 403 {
			t.Fatalf("untrusted %s accepted: %d", header, response.StatusCode)
		}
	}
	request, _ := http.NewRequest(http.MethodGet, endpoint.URL+"/.well-known/oauth-protected-resource/mcp", nil)
	request.Header.Set("Origin", "https://client.example")
	response, err := endpoint.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var metadata map[string]any
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Access-Control-Allow-Origin") != "*" || metadata["resource"] != config.Resource {
		t.Fatalf("public authentication discovery failed: %d %v", response.StatusCode, metadata)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

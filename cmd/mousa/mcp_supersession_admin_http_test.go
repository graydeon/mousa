package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/sqlite"
)

func TestHTTPSupersessionAdministrationPermissionMatrix(t *testing.T) {
	run, _ := setup(t)
	run.runInput("{\"id\":\"old\",\"text\":\"Old HTTP evidence.\"}\n{\"id\":\"new\",\"text\":\"New HTTP evidence.\"}\n", false, "sync", "--source", "alpha")
	client := startMCPClient(t, run, "cli", false)
	inputs := client.call("mousa_vector_inputs", map[string]any{"source": "alpha"}, "")
	draft := adminDeclarationDraft(t, inputs["passages"].([]any))
	client.close()
	store, err := sqlite.Open(t.Context(), run.store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, err := streamSource("alpha")
	if err != nil {
		t.Fatal(err)
	}
	config := &mcpHTTPConfig{Resource: "https://mousa.example/mcp", Subject: "account-one", ClientID: "resource-client", secret: "fixture-secret"}
	identity := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != config.ClientID || secret != config.secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		scope := "mousa:read"
		if r.Form.Get("token") == "write" {
			scope += " mousa:write"
		}
		json.NewEncoder(w).Encode(map[string]any{"active": true, "iss": config.Issuer, "sub": config.Subject, "aud": config.Resource, "exp": time.Now().Unix() + 3600, "scope": scope})
	}))
	defer identity.Close()
	config.Issuer, config.IntrospectionURL = identity.URL, identity.URL
	for _, permission := range []struct {
		name   string
		ingest bool
		admin  bool
	}{
		{"readable", false, false},
		{"ingestion-only", true, false},
		{"admin-only", false, true},
	} {
		t.Run(permission.name, func(t *testing.T) {
			application := &mcpApplication{store: store, path: run.store, caller: "cli", sources: map[string]mcpSource{"alpha": {source: source, write: permission.ingest, supersessionAdmin: permission.admin}}, gate: make(chan struct{}, 1), slots: make(chan struct{}, 8), http: true}
			server, err := application.server(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			endpoint := httptest.NewServer(mcpHTTPHandler(server, config, identity.Client()))
			defer endpoint.Close()
			config.Listen = strings.TrimPrefix(endpoint.URL, "http://")
			post := func(token, tool string, arguments any) map[string]any {
				t.Helper()
				encoded := mustJSON(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": arguments}})
				request, err := http.NewRequest(http.MethodPost, endpoint.URL+"/mcp", bytes.NewReader(encoded))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Accept", "application/json, text/event-stream")
				request.Header.Set("MCP-Protocol-Version", mcpProtocolVersion)
				request.Header.Set("Authorization", "Bearer "+token)
				response, err := endpoint.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil || response.StatusCode != http.StatusOK {
					t.Fatalf("HTTP status %d: %s, %v", response.StatusCode, body, err)
				}
				var result map[string]any
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			declare := map[string]any{"source": "alpha", "declaration": draft}
			before := adminState(t, run.store)
			for _, tool := range []string{"mousa_supersession_declare", "mousa_supersession_activate"} {
				args := declare
				if tool == "mousa_supersession_activate" {
					args = map[string]any{"source": "alpha", "activation": adminActivationDraft(nil, nil, 1)}
				}
				result := post("read", tool, args)
				if !strings.Contains(string(mustJSON(t, result)), "insufficient_scope") || adminState(t, run.store) != before {
					t.Fatalf("read principal mutated administration: %v", result)
				}
			}
			result := post("read", "mousa_supersession_status", map[string]any{"source": "alpha"})
			if !strings.Contains(string(mustJSON(t, result)), `"state":"no_history"`) {
				t.Fatalf("read scope could not inspect: %v", result)
			}
			result = post("write", "mousa_supersession_declare", declare)
			if !permission.admin {
				if !strings.Contains(string(mustJSON(t, result)), "supersession_admin_not_permitted") || adminState(t, run.store) != before {
					t.Fatalf("write scope bypassed alias allowlist: %v", result)
				}
				return
			}
			envelope := result["result"].(map[string]any)["structuredContent"].(map[string]any)
			declaration := envelope["result"].(map[string]any)["declaration"].(map[string]any)
			result = post("write", "mousa_supersession_activate", map[string]any{"source": "alpha", "activation": adminActivationDraft(nil, declaration["id"], 1)})
			if !strings.Contains(string(mustJSON(t, result)), `"actor_id":"supplied.actor"`) {
				t.Fatalf("HTTP principal replaced supplied actor or activation failed: %v", result)
			}
			result = post("write", "mousa_sync", mcpItems("alpha", map[string]any{"id": "blocked", "text": "not authorized"}))
			if !strings.Contains(string(mustJSON(t, result)), "ingestion_not_permitted") {
				t.Fatalf("administration granted ingestion: %v", result)
			}
			run.run(false, "access", "--source", "alpha", "deny")
			result = post("read", "mousa_supersession_status", map[string]any{"source": "alpha"})
			envelope = result["result"].(map[string]any)["structuredContent"].(map[string]any)
			status := envelope["result"].(map[string]any)
			if status["decision_outcome"] != "deny" || len(status) != 3 || status["current"] != nil {
				t.Fatalf("HTTP denied status leaked: %v", status)
			}
		})
	}
}

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

func TestHTTPClassificationPermissionMatrix(t *testing.T) {
	run, _ := setup(t)
	run.runInput("{\"id\":\"doc\",\"text\":\"cedar\"}\n", false, "sync", "--source", "alpha")
	store, err := sqlite.Open(t.Context(), run.store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, err := streamSource("alpha")
	if err != nil {
		t.Fatal(err)
	}
	draft := classificationDraft(map[string]any{"kind": "source", "id": source.ID.String()})
	config := &mcpHTTPConfig{Resource: "https://mousa.example/mcp", Subject: "account-one", ClientID: "resource-client", secret: "fixture-secret"}
	identity := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != config.ClientID || secret != config.secret {
			http.Error(w, "unauthorized", 401)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		token := r.Form.Get("token")
		scope := "mousa:read"
		if token == "write" {
			scope += " mousa:write"
		}
		subject := config.Subject
		if token == "foreign" {
			subject = "someone-else"
		}
		json.NewEncoder(w).Encode(map[string]any{"active": token != "inactive", "iss": config.Issuer, "sub": subject, "aud": config.Resource, "exp": time.Now().Unix() + 3600, "scope": scope})
	}))
	defer identity.Close()
	config.Issuer, config.IntrospectionURL = identity.URL, identity.URL
	for mask := range 8 {
		application := &mcpApplication{store: store, path: run.store, caller: "cli", sources: map[string]mcpSource{"alpha": {source: source, write: mask&1 != 0, supersessionAdmin: mask&2 != 0, classificationAdmin: mask&4 != 0}}, gate: make(chan struct{}, 1), slots: make(chan struct{}, 8), http: true}
		server, err := application.server(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		endpoint := httptest.NewServer(mcpHTTPHandler(server, config, identity.Client()))
		config.Listen = strings.TrimPrefix(endpoint.URL, "http://")
		post := func(token, tool string, args any) (int, map[string]any) {
			t.Helper()
			encoded := mustJSON(t, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
			request, err := http.NewRequest(http.MethodPost, endpoint.URL+"/mcp", bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
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
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]any
			if response.StatusCode == 200 {
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatalf("decode %s: %v", body, err)
				}
			}
			return response.StatusCode, result
		}
		put := map[string]any{"source": "alpha", "classification": draft}
		before := classificationState(t, run.store)
		for _, token := range []string{"", "foreign", "inactive"} {
			status, _ := post(token, "mousa_classification_put", put)
			if status == 200 || classificationState(t, run.store) != before {
				t.Fatalf("unvalidated principal %q reached mutation", token)
			}
		}
		status, result := post("read", "mousa_classification_put", put)
		if status != 200 || !strings.Contains(string(mustJSON(t, result)), "insufficient_scope") || classificationState(t, run.store) != before {
			t.Fatalf("read scope bypassed mutation gate: %v", result)
		}
		_, result = post("write", "mousa_classification_put", put)
		if mask&4 == 0 {
			if !strings.Contains(string(mustJSON(t, result)), "classification_admin_not_permitted") || classificationState(t, run.store) != before {
				t.Fatalf("write scope bypassed alias gate: %v", result)
			}
		} else {
			envelope := result["result"].(map[string]any)["structuredContent"].(map[string]any)
			saved := envelope["result"].(map[string]any)["classification"].(map[string]any)
			if saved["asserter_id"] != "supplied.actor" || saved["confidence_ppm"] != float64(0) {
				t.Fatal("transport identity replaced caller assertion")
			}
			_, result = post("read", "mousa_classification_get", map[string]any{"source": "alpha", "classification_id": saved["id"]})
			if !strings.Contains(string(mustJSON(t, result)), `"classification"`) {
				t.Fatalf("read scope could not fetch: %v", result)
			}
			if mask == 7 {
				run.run(false, "access", "--source", "alpha", "deny")
				for _, id := range []any{saved["id"], strings.Repeat("a", 64)} {
					_, denied := post("read", "mousa_classification_get", map[string]any{"source": "alpha", "classification_id": id})
					payload := denied["result"].(map[string]any)["structuredContent"].(map[string]any)["result"].(map[string]any)
					if len(payload) != 3 || payload["decision_outcome"] != "deny" {
						t.Fatalf("HTTP denial leaked: %v", payload)
					}
				}
				draft["asserter_version"] = "denied-retrieval"
				_, accepted := post("write", "mousa_classification_put", put)
				if !strings.Contains(string(mustJSON(t, accepted)), `"classification"`) {
					t.Fatalf("retrieval denial removed explicit write grant: %v", accepted)
				}
			}
		}
		endpoint.Close()
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/sqlite"
)

func TestHTTPManifestReadPrincipalAndPermissions(t *testing.T) {
	run, _ := setup(t)
	run.runInput("{\"id\":\"doc\",\"text\":\"cedar\"}\n", false, "sync", "--source", "alpha")
	_, native := run.run(false, "manifest", "--source", "alpha")
	id := manifestID(t, native)
	store, err := sqlite.Open(t.Context(), run.store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source, err := streamSource("alpha")
	if err != nil {
		t.Fatal(err)
	}
	unknown, err := streamSource("beta")
	if err != nil {
		t.Fatal(err)
	}
	config := &mcpHTTPConfig{Resource: "https://mousa.example/mcp", Subject: "account-one", ClientID: "resource-client", secret: "fixture-secret"}
	identity := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client, secret, ok := r.BasicAuth()
		if !ok || client != config.ClientID || secret != config.secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		token := r.Form.Get("token")
		scope := "mousa:read"
		if token == "write-only" {
			scope = "mousa:write"
		}
		subject := config.Subject
		if token == "foreign" {
			subject = "someone-else"
		}
		audience := config.Resource
		if token == "wrong-audience" {
			audience = "https://other.example/mcp"
		}
		expires := time.Now().Unix() + 3600
		if token == "expired" {
			expires = time.Now().Unix() - 3600
		}
		json.NewEncoder(w).Encode(map[string]any{"active": token != "inactive", "iss": config.Issuer, "sub": subject, "aud": audience, "exp": expires, "scope": scope})
	}))
	defer identity.Close()
	config.Issuer, config.IntrospectionURL = identity.URL, identity.URL
	for mask := range 8 {
		application := &mcpApplication{store: store, path: run.store, caller: "cli", sources: map[string]mcpSource{
			"alpha": {source: source, write: mask&1 != 0, supersessionAdmin: mask&2 != 0, classificationAdmin: mask&4 != 0},
			"beta":  {source: unknown},
		}, gate: make(chan struct{}, 1), slots: make(chan struct{}, 8), http: true}
		server, err := application.server(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		endpoint := httptest.NewServer(mcpHTTPHandler(server, config, identity.Client()))
		config.Listen = strings.TrimPrefix(endpoint.URL, "http://")
		var requestID any = 1
		post := func(token string, args any) (int, map[string]any) {
			t.Helper()
			encoded := mustJSON(t, map[string]any{"jsonrpc": "2.0", "id": requestID, "method": "tools/call", "params": map[string]any{"name": "mousa_source_manifest", "arguments": args}})
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
			if len(body) > mcpMaxFrameBytes {
				t.Fatal("HTTP response exceeded frame bound")
			}
			var result map[string]any
			if response.StatusCode == http.StatusOK {
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatalf("decode %s: %v", body, err)
				}
			}
			return response.StatusCode, result
		}
		args := map[string]any{"source": "alpha", "expected_manifest_id": id}
		before := manifestAdapterState(t, run.store)
		for _, token := range []string{"", "foreign", "inactive", "wrong-audience", "expired", "write-only"} {
			status, _ := post(token, args)
			if status == http.StatusOK || manifestAdapterState(t, run.store) != before {
				t.Fatalf("unvalidated/no-read principal %q reached inspection", token)
			}
		}
		if mask == 0 {
			t.Run("LargeStringRequestID", func(t *testing.T) {
				requestID = strings.Repeat("x", 1022)
				status, response := post("read", args)
				if status != http.StatusOK || response["id"] != requestID || response["result"].(map[string]any)["isError"] == true {
					t.Fatal("maximum permitted string ID did not round-trip")
				}
				before := manifestAdapterState(t, run.store)
				for _, id := range []string{strings.Repeat("x", 1023), strings.Repeat("x", 3*mcpMaxFrameBytes/4), strings.Repeat("<", 200)} {
					requestID = id
					status, _ := post("read", args)
					if status != http.StatusBadRequest || manifestAdapterState(t, run.store) != before {
						t.Fatal("oversized ID reached inspection or was not rejected")
					}
				}
				requestID = 1
			})
		}
		status, response := post("read", args)
		if status != http.StatusOK {
			t.Fatalf("read scope rejected for mask %d: %d", mask, status)
		}
		result := response["result"].(map[string]any)
		payload := result["structuredContent"].(map[string]any)["result"].(map[string]any)
		if result["isError"] == true || payload["matches"] != true || !reflect.DeepEqual(payload["manifest"], native["manifest"]) {
			t.Fatalf("HTTP read/admin independence failed: %v", response)
		}
		_, response = post("read", map[string]any{"source": "alpha", "expected_manifest_id": strings.Repeat("a", 64)})
		result = response["result"].(map[string]any)
		if result["isError"] == true || result["structuredContent"].(map[string]any)["result"].(map[string]any)["matches"] != false {
			t.Fatalf("HTTP mismatch not ordinary result: %v", response)
		}
		before = manifestAdapterState(t, run.store)
		for _, raw := range invalidManifestArguments() {
			_, response = post("read", json.RawMessage(raw))
			if !strings.Contains(string(mustJSON(t, response)), "invalid_arguments") || manifestAdapterState(t, run.store) != before {
				t.Fatalf("HTTP strict parser accepted/mutated: %s %v", raw, response)
			}
		}
		if mask == 7 {
			run.run(false, "access", "--source", "alpha", "deny")
			rawExec(t, run.store, `UPDATE representations SET record_json=x'7b7d0a'`)
			for _, alias := range []string{"alpha", "beta"} {
				_, response = post("read", map[string]any{"source": alias, "expected_manifest_id": id})
				assertManifestDenied(t, response["result"].(map[string]any)["structuredContent"].(map[string]any)["result"].(map[string]any))
			}
		}
		endpoint.Close()
	}
}

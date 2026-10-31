package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/sqlite"
)

func TestMCPTokenRequiresConfiguredBackend(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("a", "Drain sockets."))
	client := startMCPClient(t, run, "cli", false)
	args := mcpQuery("alpha", "Drain", 4096)
	baseline := client.call("mousa_query", args, "")
	if _, ok := baseline["token"]; ok {
		t.Fatal("default query exposed token metadata")
	}
	args["context_tokens"] = 256
	client.call("mousa_query", args, "tokenizer_unavailable")
	delete(args, "context_tokens")
	after := client.call("mousa_query", args, "")
	if after["packet_id"] != baseline["packet_id"] {
		t.Fatal("token refusal changed byte selection")
	}
	client.close()
	marker := filepath.Join(t.TempDir(), "invoked")
	backend := filepath.Join(t.TempDir(), "unusable")
	if err := os.WriteFile(backend, []byte("#!/bin/sh\nprintf invoked > "+marker+"\nexit 42\n"), 0700); err != nil {
		t.Fatal(err)
	}
	client = startMCPClient(t, run, "cli", false, "--tokenizer-python", backend, "--tokenizer-cache", t.TempDir())
	client.call("mousa_query", args, "")
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("byte query invoked configured tokenizer")
	}
	args["context_tokens"] = 256
	client.call("mousa_query", args, "tokenizer_unavailable")
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("token request never exercised unusable configured backend")
	}
}

func mcpTokenStartup(t *testing.T) []string {
	t.Helper()
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	return []string{"--tokenizer-python", python, "--tokenizer-cache", cache}
}

func assertMCPTokenRendering(t *testing.T, result map[string]any) int {
	t.Helper()
	text := result["context"].(string)
	token := result["token"].(map[string]any)
	n := officialTokenCount(t, text)
	if token["measured_tokens"] != float64(n) || token["rendered_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte(text))) || token["rendered_bytes"] != float64(len(text)) || token["rendering"] != "mousa.context_text.v1" || float64(n) > token["limit"].(float64) {
		t.Fatalf("whole-rendering count/digest/boundary: %v", result)
	}
	bytes := 0
	for _, raw := range result["evidence"].([]any) {
		passage := raw.(map[string]any)
		if !strings.Contains(text, passage["text"].(string)) {
			t.Fatalf("selected evidence absent from context: %v", passage)
		}
		bytes += len(passage["text"].(string))
	}
	if float64(bytes) != result["used_bytes"] || float64(bytes) > result["budget_bytes"].(float64) {
		t.Fatal("byte budget accounting differs from token survivors")
	}
	assertMCPIdentities(t, result)
	return n
}

func TestMCPTokenModesBoundariesAndHistory(t *testing.T) {
	startup := mcpTokenStartup(t)
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", strings.Join([]string{record("a", "Drain 你好 مرحبا café é <|endoftext|>"), record("b", "Drain concise"), record("c", "Drain concise")}, "\n"))
	space := prepareMCPVectors(t, run, map[string][]float64{"a": {1, 0}, "b": {1, 0}, "c": {1, 0}})
	client := startMCPClient(t, run, "cli", false, startup...)
	type saved struct {
		id     any
		token  any
		packet any
		mode   string
		facts  any
	}
	var history []saved
	for _, mode := range []string{"lexical", "vector", "hybrid"} {
		for _, packing := range []string{"original", "exact-v1"} {
			tool := "mousa_" + mode + "_query"
			args := mcpRetrievalArgs(space, mode, packing, 4096)
			if mode == "lexical" {
				tool = "mousa_query"
				args = mcpQuery("alpha", "Drain", 4096)
				args["packing_policy"] = packing
			}
			baseline := client.call(tool, args, "")
			if baseline["token"] != nil || baseline["context"] != nil {
				t.Fatal("omitted token mode grew byte-only response")
			}
			args["context_tokens"] = 524288
			full := client.call(tool, args, "")
			n := assertMCPTokenRendering(t, full)
			want := 3
			if packing == "exact-v1" {
				want = 2
			}
			if len(full["evidence"].([]any)) != want {
				t.Fatalf("duplicate policy: %v", full)
			}
			args["context_tokens"] = n
			exact := client.call(tool, args, "")
			if exact["context"] != full["context"] {
				t.Fatal("exact whole-context boundary did not fit")
			}
			args["context_tokens"] = n - 1
			short := client.call(tool, args, "")
			if short["context"] == full["context"] || assertMCPTokenRendering(t, short) > n-1 {
				t.Fatal("one-token-short selection")
			}
			args["context_tokens"], args["window_tokens"], args["prompt_reserve_tokens"], args["output_reserve_tokens"] = 524288, n+50, 20, 31
			reserved := client.call(tool, args, "")
			if reserved["context"] != short["context"] || reserved["token"].(map[string]any)["limit"] != float64(n-1) {
				t.Fatal("reservation effective limit")
			}
			args["budget_bytes"] = 1
			empty := client.call(tool, args, "")
			if empty["context"] != "" || len(empty["evidence"].([]any)) != 0 {
				t.Fatal("token request bypassed byte limit")
			}
			assertMCPTokenRendering(t, empty)
			inspect := map[string]any{"source": "alpha", "trail_id": full["trail_id"]}
			defaultView := client.call("mousa_trail", inspect, "")["historical"].(map[string]any)
			inspect["token_details"] = false
			if !reflect.DeepEqual(defaultView, client.call("mousa_trail", inspect, "")["historical"]) || defaultView["token"] != nil {
				t.Fatal("default history grew")
			}
			inspect["token_details"] = true
			h := client.call("mousa_trail", inspect, "")["historical"].(map[string]any)
			if !reflect.DeepEqual(h["token"], full["token"]) || h["decision_id"] != full["decision_id"] || h["packet_id"] != full["packet_id"] || h["vector"] != nil || h["hybrid"] != nil || h["token_verification"] != nil {
				t.Fatal("token history did not bind producing selection independently")
			}
			history = append(history, saved{full["trail_id"], full["token"], full["packet_id"], mode, full[mode]})
			delete(args, "context_tokens")
			delete(args, "window_tokens")
			delete(args, "prompt_reserve_tokens")
			delete(args, "output_reserve_tokens")
			args["budget_bytes"] = 4096
			after := client.call(tool, args, "")
			for _, field := range []string{"request_id", "decision_id", "trail_id", "evaluated_at_usec", "latency_micros"} {
				delete(baseline, field)
				delete(after, field)
			}
			if !reflect.DeepEqual(baseline, after) {
				t.Fatal("token mode changed omitted-mode values or property set")
			}
		}
	}
	client.close()
	jsonlSync(t, run, "alpha", record("a", "Drain replacement."))
	// No backend is configured on restart: recorded history must not recount.
	client = startMCPClient(t, run, "cli", false)
	for _, s := range history {
		h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": s.id, "token_details": true, "retrieval_details": true}, "")["historical"].(map[string]any)
		if !reflect.DeepEqual(h["token"], s.token) || h["packet_id"] != s.packet || !reflect.DeepEqual(h[s.mode], s.facts) {
			t.Fatal("restart/replacement reinterpreted producing history")
		}
	}
}

func TestMCPTokenStrictInputs(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("a", "Drain sockets."))
	client := startMCPClient(t, run, "cli", false)
	before := inspectionRows(t, run.store, nil)
	for _, field := range []string{"context_tokens", "window_tokens", "prompt_reserve_tokens", "output_reserve_tokens"} {
		for _, value := range []string{"null", "true", "1.5", "\"1\"", "-1", "18446744073709551616", "1e2"} {
			raw := json.RawMessage(fmt.Sprintf(`{"source":"alpha","query":"Drain","policy":"original","budget_bytes":1000,"context_tokens":100,"%s":%s}`, field, value))
			if field == "context_tokens" {
				raw = json.RawMessage(fmt.Sprintf(`{"source":"alpha","query":"Drain","policy":"original","budget_bytes":1000,"%s":%s}`, field, value))
			}
			client.call("mousa_query", raw, "invalid_arguments")
		}
	}
	for _, extra := range []string{`"context_tokens":0`, `"context_tokens":524289`, `"window_tokens":0`, `"prompt_reserve_tokens":0`, `"output_reserve_tokens":0`, `"context_tokens":100,"prompt_reserve_tokens":1`, `"context_tokens":100,"window_tokens":10,"prompt_reserve_tokens":7,"output_reserve_tokens":4`, `"context_tokens":100,"window_tokens":1000000001`, `"context_tokens":100,"context_tokens":100`, `"context_tokens":100,"tokenizer_python":"bad"`, `"context_tokens":100,"tokenizer_cache":"bad"`, `"context_tokens":100,"store":"bad"`, `"context_tokens":100,"caller":"bad"`} {
		client.call("mousa_query", json.RawMessage(`{"source":"alpha","query":"Drain","policy":"original","budget_bytes":1000,`+extra+`}`), "invalid_arguments")
	}
	for _, value := range []string{"null", "0", "1", "\"true\""} {
		client.call("mousa_trail", json.RawMessage(`{"source":"alpha","trail_id":"`+strings.Repeat("1", 64)+`","token_details":`+value+`}`), "invalid_arguments")
	}
	if !reflect.DeepEqual(before, inspectionRows(t, run.store, nil)) {
		t.Fatal("invalid token arguments mutated audit records")
	}
}

func TestMCPTokenSkippedCandidatesAndAuthorization(t *testing.T) {
	startup := mcpTokenStartup(t)
	run, _ := setup(t)
	large := "Drain " + strings.Repeat("large ", 400)
	jsonlSync(t, run, "alpha", strings.Join([]string{record("large", large), record("small", "Drain concise"), record("copy", "Drain concise")}, "\n"))
	space := prepareMCPVectors(t, run, map[string][]float64{"large": {1, 0}, "small": {0, 1}, "copy": {0, 1}})
	client := startMCPClient(t, run, "cli", false, startup...)
	var producingTrail any
	for _, mode := range []string{"lexical", "vector", "hybrid"} {
		for _, packing := range []string{"original", "exact-v1"} {
			args := mcpRetrievalArgs(space, mode, packing, 4096)
			tool := "mousa_" + mode + "_query"
			if mode == "lexical" {
				args = mcpQuery("alpha", "Drain", 4096)
				args["packing_policy"] = packing
				tool = "mousa_query"
			}
			args["context_tokens"] = 240
			got := client.call(tool, args, "")
			producingTrail = got["trail_id"]
			assertMCPTokenRendering(t, got)
			if got["outcome"] != "evidence" || strings.Contains(got["context"].(string), "large ") {
				t.Fatal("oversized candidate blocked later fit")
			}
			args["source"] = "not-configured"
			client.call(tool, args, "source_not_permitted")
		}
	}
	client.close()
	for _, state := range []string{"deny", "withdraw"} {
		if state == "withdraw" {
			run.run(false, "access", "--source", "alpha", "allow")
			run.run(false, "withdraw", "--source", "alpha")
		}
		if state == "deny" {
			run.run(false, "access", "--source", "alpha", "deny")
		}
		client = startMCPClient(t, run, "cli", false, startup...)
		for _, mode := range []string{"lexical", "vector", "hybrid"} {
			args := mcpRetrievalArgs(space, mode, "exact-v1", 4096)
			tool := "mousa_" + mode + "_query"
			if mode == "lexical" {
				args = mcpQuery("alpha", "Drain", 4096)
				tool = "mousa_query"
			}
			args["context_tokens"] = 240
			denied := client.call(tool, args, "")
			if denied["context"] != "" || len(denied["evidence"].([]any)) != 0 || len(denied["token"].(map[string]any)["candidate_ids"].([]any)) != 0 {
				t.Fatal("denial exposed evidence/metadata")
			}
			if mode != "lexical" && (denied["eligible"] != float64(0) || denied["indexed"] != float64(0)) {
				t.Fatal("denial leaked coverage")
			}
			hidden := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": producingTrail, "token_details": true, "retrieval_details": true}, "")
			if hidden["historical"] != nil {
				t.Fatal("denial exposed history")
			}
		}
		client.close()
	}
}

func TestMCPTokenFailureAndCancellationReapWithoutProducingTrail(t *testing.T) {
	startup := mcpTokenStartup(t)
	for _, mode := range []string{"lexical", "vector", "hybrid"} {
		for _, scenario := range []struct{ cancelled, supersession bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
			cancelled, supersession := scenario.cancelled, scenario.supersession
			t.Run(fmt.Sprintf("%s/cancelled=%v/supersession=%v", mode, cancelled, supersession), func(t *testing.T) {
				run, _ := setup(t)
				jsonlSync(t, run, "alpha", record("a", "Drain sockets."))
				if supersession {
					newMCPWithholdingTrailFixture(t, run, "alpha", "Drain predecessor", "Drain successor")
				}
				tool := "mousa_query"
				args := mcpQuery("alpha", "Drain", 4096)
				if mode != "lexical" {
					space := prepareMCPVectors(t, run, map[string][]float64{"a": {1, 0}})
					args = mcpRetrievalArgs(space, mode, "original", 4096)
					tool = "mousa_" + mode + "_query"
				}
				args["context_tokens"], args["supersession"] = 256, supersession
				input, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				store, err := sqlite.Open(t.Context(), run.store)
				if err != nil {
					t.Fatal(err)
				}
				defer store.Close()
				root := t.TempDir()
				path, pidPath, ready := filepath.Join(root, "counter"), filepath.Join(root, "pid"), filepath.Join(root, "count")
				tail := "sys.stdout.buffer.write(b'\\x00');sys.stdout.buffer.flush()"
				if cancelled {
					tail = "time.sleep(30)"
				}
				// Fault injection starts successfully and reads a real proposed rendering
				// before failure/cancellation, rather than refusing at startup.
				script := fmt.Sprintf("#!%s\nimport os,sys,struct,time\nopen(%q,'w').write(str(os.getpid()))\nsys.stdout.buffer.write(b'mousa.tokens.v1/tiktoken/0.12.0/o200k_base/ordinary\\n');sys.stdout.buffer.flush()\nn=struct.unpack('>I',sys.stdin.buffer.read(4))[0]\nsys.stdin.buffer.read(n)\nopen(%q,'w').write('counting')\n%s\n", startup[1], pidPath, ready, tail)
				if err := os.WriteFile(path, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				source := declarationSource(t, "alpha")
				app := mcpApplication{store: store, caller: "cli", sources: map[string]mcpSource{"alpha": {source: source}}, gate: make(chan struct{}, 1), tokenizerPython: path, tokenizerCache: startup[3]}
				before := inspectionRows(t, run.store, nil)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					response, err := app.call(ctx, tool, input)
					if err == nil && !response.IsError {
						err = fmt.Errorf("worker failure reported success")
					}
					done <- err
				}()
				if cancelled {
					deadline := time.After(5 * time.Second)
					for {
						if _, err := os.Stat(ready); err == nil {
							break
						}
						select {
						case <-deadline:
							t.Fatal("worker never received rendering")
						case <-time.After(5 * time.Millisecond):
						}
					}
					cancel()
				}
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("unbounded tokenizer error")
				}
				if _, err := os.Stat(ready); err != nil {
					t.Fatal("failure did not reach count seam")
				}
				raw, err := os.ReadFile(pidPath)
				if err != nil {
					t.Fatal(err)
				}
				pid, err := strconv.Atoi(string(raw))
				if err != nil {
					t.Fatal(err)
				}
				if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
					t.Fatalf("child not reaped: %v", err)
				}
				if !reflect.DeepEqual(before, inspectionRows(t, run.store, nil)) {
					t.Fatal("failed token production committed records")
				}
			})
		}
	}
}

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestNativeTokenSupersessionNoHistory(t *testing.T) {
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	binary := cliBinary
	for _, directory := range []bool{false, true} {
		for _, packing := range []string{"original", "exact-v1"} {
			t.Run(fmt.Sprintf("directory=%v/%s", directory, packing), func(t *testing.T) {
				t.Parallel()
				run := testRun{t: t, binary: binary, store: filepath.Join(t.TempDir(), "local.sqlite")}
				root := t.TempDir()
				selector := []string{"--source", "alpha"}
				if directory {
					writeFile(t, root, "a.md", "Drain concise")
					run.run(false, "sync", root)
					selector = []string{root}
				} else {
					jsonlSync(t, run, "alpha", record("a", "Drain concise"))
				}
				args := []string{"query", "--supersession", "--context-tokens", "4096", "--tokenizer-python", python, "--tokenizer-cache", cache, "--packing-policy", packing}
				args = append(append(args, selector...), "Drain")
				_, got := run.run(false, args...)
				if got["outcome"] != "evidence" || got["token"].(map[string]any)["measured_tokens"] != float64(officialTokenCount(t, got["context"].(string))) {
					t.Fatalf("combined query: %v", got)
				}
				_, history := run.run(false, append(append([]string{"trail"}, selector...), got["trail_id"].(string))...)
				h := history["historical"].(map[string]any)
				if h["schema"] != "mousa.source_trail.v8" || h["supersession"].(map[string]any)["consulted"] != true {
					t.Fatalf("combined history: %v", h)
				}
			})
		}
	}
}

func TestMCPTokenSupersessionActivePin(t *testing.T) {
	startup := mcpTokenStartup(t)
	binary := cliBinary
	for _, packing := range []string{"original", "exact-v1"} {
		t.Run(packing, func(t *testing.T) {
			t.Parallel()
			run := testRun{t: t, binary: binary, store: filepath.Join(t.TempDir(), "local.sqlite")}
			newMCPWithholdingTrailFixture(t, run, "alpha", "Drain predecessor", "Drain successor")
			client := startMCPClient(t, run, "cli", false, startup...)
			defer client.close()
			args := mcpQuery("alpha", "Drain", 4096)
			args["context_tokens"], args["supersession"], args["packing_policy"] = 4096, true, packing
			got := client.call("mousa_query", args, "")
			assertMCPTokenRendering(t, got)
			if got["supersession_excluded"] != float64(1) || len(got["evidence"].([]any)) != 1 {
				t.Fatalf("exact pin not withheld: %v", got)
			}
			h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": got["trail_id"], "supersession": true, "token_details": true}, "")["historical"].(map[string]any)
			if h["schema"] != "mousa.source_trail.v8" || len(h["supersession"].(map[string]any)["dispositions"].([]any)) != 1 || h["token"] == nil {
				t.Fatalf("combined history: %v", h)
			}
		})
	}
}

func TestNativeTokenSupersessionLifecycle(t *testing.T) {
	startup := mcpTokenStartup(t)
	binary := cliBinary
	for _, directory := range []bool{false, true} {
		for _, packing := range []string{"original", "exact-v1"} {
			t.Run(fmt.Sprintf("%v/%s", directory, packing), func(t *testing.T) {
				t.Parallel()
				run := testRun{t: t, binary: binary, store: filepath.Join(t.TempDir(), "local.sqlite")}
				root := t.TempDir()
				selector := []string{"--source", "alpha"}
				source := declarationSource(t, "alpha")
				if directory {
					selector = []string{root}
					var err error
					source, _, err = directorySource(root)
					if err != nil {
						t.Fatal(err)
					}
				}
				sync := func(old, new string) {
					t.Helper()
					if directory {
						writeFile(t, root, "old.md", old)
						writeFile(t, root, "new.md", new)
						run.run(false, "sync", root)
					} else {
						jsonlSync(t, run, "alpha", record("old.md", old)+"\n"+record("new.md", new))
					}
				}
				sync("Drain predecessor", "Drain successor")
				_, baseline := run.run(false, append(append([]string{"query"}, selector...), "Drain")...)
				a, b := hits(baseline)[0].(map[string]any), hits(baseline)[1].(map[string]any)
				predecessor, err := mousa.ParseRepresentationID(a["representation_id"].(string))
				if err != nil {
					t.Fatal(err)
				}
				successor, err := mousa.ParseRepresentationID(b["representation_id"].(string))
				if err != nil {
					t.Fatal(err)
				}
				d := storedDeclaration(t, run, declarationJSON(t, source, a["item"].(string), predecessor, b["item"].(string), successor, "example.operator", "explicit replacement"))
				state := activateQueryDeclaration(t, run, source, nil, &d, 1)
				query := func(flag string, cap int, reserve bool, text string) map[string]any {
					t.Helper()
					args := []string{"query", "--context-tokens", strconv.Itoa(cap), "--packing-policy", packing}
					args = append(args, startup...)
					if flag != "" {
						args = append(args, "--supersession="+flag)
					}
					if reserve {
						args = append(args, "--context-window", strconv.Itoa(cap+50), "--prompt-reserve", "20", "--output-reserve", "31")
					}
					_, got := run.run(false, append(append(args, selector...), text)...)
					n := officialTokenCount(t, got["context"].(string))
					if got["token"].(map[string]any)["measured_tokens"] != float64(n) {
						t.Fatal("whole context count")
					}
					return got
				}
				omitted, disabled := query("", 4096, false, "Drain"), query("false", 4096, false, "Drain")
				if omitted["packet_id"] != disabled["packet_id"] || !reflect.DeepEqual(omitted["token"], disabled["token"]) || disabled["supersession_excluded"] != nil {
					t.Fatal("false changed v7 output")
				}
				active := query("true", 4096, false, "Drain")
				if active["supersession_excluded"] != float64(1) || len(active["evidence"].([]any)) != 1 {
					t.Fatal(active)
				}
				n := officialTokenCount(t, active["context"].(string))
				if query("true", n, false, "Drain")["context"] != active["context"] {
					t.Fatal("exact boundary")
				}
				short := query("true", n-1, false, "Drain")
				if short["context"] != "" || short["outcome"] != "budget_omitted" || query("true", n, true, "Drain")["context"] != "" {
					t.Fatal("reservation/whole header budget")
				}
				_, recounted := run.run(false, append(append(append([]string{"trail", "--verify-tokens"}, startup...), selector...), active["trail_id"].(string))...)
				if recounted["historical"].(map[string]any)["token_verification"] != "official_recount_verified" {
					t.Fatal("recount")
				}
				activateQueryDeclaration(t, run, source, &state, nil, 2)
				if query("true", 4096, false, "Drain")["supersession_excluded"] != float64(0) {
					t.Fatal("deactivation")
				}
				_, old := run.run(false, append(append([]string{"trail"}, selector...), active["trail_id"].(string))...)
				if len(old["historical"].(map[string]any)["supersession"].(map[string]any)["dispositions"].([]any)) != 1 {
					t.Fatal("historical producing selection changed")
				}
			})
		}
	}
}

func TestMCPTokenSupersessionAllWithheldAndDenial(t *testing.T) {
	startup := mcpTokenStartup(t)
	run, _ := setup(t)
	fixture := newMCPWithholdingTrailFixture(t, run, "alpha", "oldterm predecessor", "newterm successor")
	client := startMCPClient(t, run, "cli", false, startup...)
	args := mcpQuery("alpha", "oldterm", 1)
	args["supersession"], args["context_tokens"] = true, 1
	got := client.call("mousa_query", args, "")
	if got["outcome"] != "supersession_excluded" || got["context"] != "" || got["budget_omitted"] != float64(0) {
		t.Fatal(got)
	}
	assertMCPTokenRendering(t, got)
	client.close()
	jsonlSync(t, run, "alpha", `{"id":"corrected","deleted":true}`)
	client = startMCPClient(t, run, "cli", false, startup...)
	absent := client.call("mousa_query", args, "")
	if absent["outcome"] != "supersession_excluded" {
		t.Fatal("successor absence reinstated predecessor")
	}
	client.close()
	jsonlSync(t, run, "alpha", record("doc", "oldterm revised"))
	client = startMCPClient(t, run, "cli", false, startup...)
	args["context_tokens"], args["budget_bytes"] = 4096, 4096
	updated := client.call("mousa_query", args, "")
	if updated["supersession_excluded"] != float64(0) || updated["outcome"] != "evidence" {
		t.Fatal("revision inherited old pin")
	}
	delete(args, "supersession")
	omitted := client.call("mousa_query", args, "")
	args["supersession"] = false
	disabled := client.call("mousa_query", args, "")
	if !reflect.DeepEqual(mcpStableQueryValues(t, omitted), mcpStableQueryValues(t, disabled)) {
		t.Fatal("false changed v7")
	}
	client.close()
	run.run(false, "access", "--source", "alpha", "deny")
	client = startMCPClient(t, run, "cli", false, startup...)
	defer client.close()
	args["supersession"] = true
	denied := client.call("mousa_query", args, "")
	if denied["context"] != "" || denied["supersession_excluded"] != float64(0) {
		t.Fatal("denial released selection")
	}
	h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": got["trail_id"], "supersession": true, "token_details": true}, "")
	if h["historical"] != nil {
		t.Fatal("denial released history")
	}
	raw := mcpCanonicalJSON(t, denied)
	if strings.Contains(raw, fixture.declaration.String()) || strings.Contains(raw, fixture.activation.String()) {
		t.Fatal("denial leaked consultation identity")
	}
}

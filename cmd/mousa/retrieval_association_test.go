package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const retrievalAssociationVector = `{"schema":"mousa.vector_query.v1","values":[1,0]}`

func prepareAssociatedRetrieval(t *testing.T, run testRun, selector []string) string {
	t.Helper()
	_, exported := run.run(false, append([]string{"vector", "inputs"}, selector...)...)
	var embeddings []map[string]any
	for _, raw := range exported["passages"].([]any) {
		p := raw.(map[string]any)
		if p["item"] != "primary.md" {
			continue
		}
		e := map[string]any{"values": []float64{1, 0}}
		for _, key := range []string{"source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256"} {
			e[key] = p[key]
		}
		embeddings = append(embeddings, e)
	}
	input := map[string]any{"schema": "mousa.vector_import.v1", "space": map[string]any{"schema": "mousa.vector_space.v1", "provider": "synthetic", "model": "support-arithmetic", "version": "1", "dimensions": 2, "metric": "cosine"}, "embeddings": embeddings}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	_, imported := run.runInput(string(raw), false, append([]string{"vector", "import"}, selector...)...)
	return imported["space_id"].(string)
}

func assertRetrievalSupport(t *testing.T, got map[string]any) map[string]any {
	t.Helper()
	rows, ok := got["associated_evidence"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("missing separate support: %v", got)
	}
	support := rows[0].(map[string]any)
	if support["item"] != "support.md" || support["text"] != "Separate qualification." || support["origin"] != "association" || !reflect.DeepEqual(support["association"], mcpAssociation("primary.md", "support.md")) {
		t.Fatalf("support attribution changed: %v", support)
	}
	for _, key := range []string{"rank", "cosine", "rrf", "embedding_id", "bm25_score", "lexical", "vector"} {
		if _, ok := support[key]; ok {
			t.Fatalf("support fabricated %s: %v", key, support)
		}
	}
	return support
}

func TestNativeRetrievalAssociations(t *testing.T)      { nativeRetrievalAssociations(t, false) }
func TestNativeTokenRetrievalAssociations(t *testing.T) { nativeRetrievalAssociations(t, true) }

func nativeRetrievalAssociations(t *testing.T, token bool) {
	var startup []string
	if token {
		startup = mcpTokenStartup(t)
	}
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			run, root := setup(t)
			selector := []string{"--source", "alpha"}
			if directory {
				writeFile(t, root, "primary.md", "Drain instruction.")
				writeFile(t, root, "support.md", "Separate qualification.")
				run.run(false, "sync", root)
				selector = []string{root}
			} else {
				jsonlSync(t, run, "alpha", record("primary.md", "Drain instruction.")+"\n"+record("support.md", "Separate qualification."))
			}
			space := prepareAssociatedRetrieval(t, run, selector)
			raw, _ := json.Marshal(mcpAssociationDocument(mcpAssociation("primary.md", "support.md")))
			path := filepath.Join(t.TempDir(), "associations.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"vector", "hybrid"} {
				for _, packing := range []string{"original", "exact-v1"} {
					query := func(association bool, supersession string, budget, cap int) map[string]any {
						args := []string{"query", "--" + mode + "-space", space, "--packing-policy", packing, "--budget-bytes", strconv.Itoa(budget)}
						if association {
							args = append(args, "--associations", path)
						}
						if supersession != "" {
							args = append(args, "--supersession="+supersession)
						}
						if token {
							args = append(append(args, startup...), "--context-tokens", strconv.Itoa(cap))
						}
						args = append(args, selector...)
						if mode == "hybrid" {
							args = append(args, "Drain")
						}
						_, got := run.runInput(retrievalAssociationVector, false, args...)
						return got
					}
					control := query(false, "", 4096, 4096)
					for _, supersession := range []string{"", "false", "true"} {
						full := query(true, supersession, 4096, 4096)
						assertRetrievalSupport(t, full)
						if !reflect.DeepEqual(full["evidence"], control["evidence"]) {
							t.Fatal("support changed primary ranking", full, control)
						}
						budget := len("Drain instruction.") + len("Separate qualification.")
						count := 4096
						if token {
							count = officialTokenCount(t, full["context"].(string))
						}
						exact := query(true, supersession, budget, count)
						assertRetrievalSupport(t, exact)
						short := query(true, supersession, budget-1, count)
						if short["associated_evidence"] != nil || !reflect.DeepEqual(short["evidence"], control["evidence"]) {
							t.Fatal("one-short bytes displaced primary", short)
						}
						if token {
							short = query(true, supersession, budget, count-1)
							if short["associated_evidence"] != nil || short["association_omissions"].([]any)[0].(map[string]any)["reason"] != "tokens" {
								t.Fatal("one-short token support", short)
							}
						}
						empty := query(true, supersession, 1, 4096)
						if len(empty["evidence"].([]any)) != 0 || empty["associated_evidence"] != nil || empty["association_omissions"] != nil {
							t.Fatal("unselected primary fired", empty)
						}
						trailArgs := []string{"trail"}
						if token {
							trailArgs = append(append(trailArgs, startup...), "--verify-tokens")
						}
						trailArgs = append(append(trailArgs, selector...), exact["trail_id"].(string))
						_, inspection := run.run(false, trailArgs...)
						h := inspection["historical"].(map[string]any)
						version := "mousa.source_trail.v14"
						if token {
							version = "mousa.source_trail.v15"
						}
						facts := h[mode].(map[string]any)
						if h["schema"] != version || h["packet_id"] != exact["packet_id"] || facts["eligible"] != float64(2) || facts["indexed"] != float64(1) || len(facts["candidates"].([]any)) != 1 {
							t.Fatal("history coverage/ranks changed", h)
						}
						if token && !reflect.DeepEqual(h["token"], exact["token"]) {
							t.Fatal("native recount changed token facts")
						}
					}
				}
			}
		})
	}
}

func TestMCPRetrievalAssociations(t *testing.T)      { mcpRetrievalAssociations(t, false) }
func TestMCPTokenRetrievalAssociations(t *testing.T) { mcpRetrievalAssociations(t, true) }

func mcpRetrievalAssociations(t *testing.T, token bool) {
	var startup []string
	if token {
		startup = mcpTokenStartup(t)
	}
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("primary.md", "Drain instruction.")+"\n"+record("support.md", "Separate qualification."))
	space := prepareAssociatedRetrieval(t, run, []string{"--source", "alpha"})
	client := startMCPClient(t, run, "cli", false, startup...)
	var saved map[string]any
	for _, mode := range []string{"vector", "hybrid"} {
		for _, packing := range []string{"original", "exact-v1"} {
			args := mcpRetrievalArgs(space, mode, packing, 4096)
			if token {
				args["context_tokens"] = 4096
			}
			control := client.call("mousa_"+mode+"_query", args, "")
			args["associations"] = mcpAssociationDocument(mcpAssociation("primary.md", "support.md"))
			for _, ss := range []string{"", "false", "true"} {
				if ss != "" {
					args["supersession"] = ss == "true"
				}
				got := client.call("mousa_"+mode+"_query", args, "")
				assertRetrievalSupport(t, got)
				if !reflect.DeepEqual(got[mode], control[mode]) || !reflect.DeepEqual(got["evidence"], control["evidence"]) || got["missing"] != float64(1) {
					t.Fatal("support changed producing retrieval", got)
				}
				if token {
					assertMCPTokenRendering(t, got)
				}
				for _, retrieval := range []bool{false, true} {
					for _, tokens := range []bool{false, true} {
						h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": got["trail_id"], "retrieval_details": retrieval, "token_details": tokens, "supersession": true}, "")["historical"].(map[string]any)
						if (h[mode] != nil) != retrieval || (h["token"] != nil) != (tokens && token) || len(h["associated"].([]any)) != 1 {
							t.Fatal("history opt-ins coupled", h)
						}
						if retrieval && !reflect.DeepEqual(h[mode], got[mode]) {
							t.Fatal("history reranked")
						}
					}
				}
				saved = got
			}
		}
	}
	client.close()
	jsonlSync(t, run, "alpha", record("support.md", "Replacement qualification."))
	client = startMCPClient(t, run, "cli", false, startup...)
	h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": saved["trail_id"], "retrieval_details": true, "token_details": true}, "")["historical"].(map[string]any)
	if h["associated"].([]any)[0].(map[string]any)["indexed_now"] != false {
		t.Fatal("history lost retired support", h)
	}
	args := mcpRetrievalArgs(space, "vector", "original", 4096)
	args["associations"] = mcpAssociationDocument(mcpAssociation("primary.md", "support.md"))
	if token {
		args["context_tokens"] = 4096
	}
	fresh := client.call("mousa_vector_query", args, "")
	if fresh["associated_evidence"].([]any)[0].(map[string]any)["text"] != "Replacement qualification." || fresh["indexed"] != float64(1) {
		t.Fatal("revised unembedded target not resolved from current source", fresh)
	}
	jsonlSync(t, run, "alpha", `{"id":"support.md","deleted":true}`)
	inactive := client.call("mousa_vector_query", args, "")
	if inactive["associated_evidence"] != nil || inactive["association_omissions"].([]any)[0].(map[string]any)["reason"] != "target_inactive" {
		t.Fatal("inactive target released", inactive)
	}
	run.run(false, "access", "--source", "alpha", "deny")
	denied := client.call("mousa_vector_query", args, "")
	if denied["associated_evidence"] != nil || len(denied["evidence"].([]any)) != 0 || denied["eligible"] != float64(0) {
		t.Fatal("denial leaked evidence", denied)
	}
	if client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": saved["trail_id"]}, "")["historical"] != nil {
		t.Fatal("denial leaked historical support")
	}
	client.close()
}

func TestRetrievalAssociationStrictArguments(t *testing.T) {
	run, root := setup(t)
	jsonlSync(t, run, "alpha", record("primary.md", "Drain instruction."))
	space := prepareAssociatedRetrieval(t, run, []string{"--source", "alpha"})
	client := startMCPClient(t, run, "cli", false)
	valid := `{"schema":"mousa.association_declarations.v1","associations":[{"from_item":"primary.md","to_item":"support.md","basis":"qualification","author":"maintainer"}]}`
	invalid := []string{`null`, `[]`, `{}`, strings.Replace(valid, `"associations":[`, `"associations":[],"associations":[`, 1), strings.Replace(valid, `"author":`, `"author":"first","author":`, 1), strings.Replace(valid, `"author":`, `"unknown":0,"author":`, 1), strings.Replace(valid, `"associations":[`, `"associations":[null,`, 1), strings.Replace(valid, `"support.md"`, `"primary.md"`, 1)}
	before := inspectionRows(t, run.store, nil)
	for _, mode := range []string{"vector", "hybrid"} {
		for i, document := range invalid {
			args := mcpRetrievalArgs(space, mode, "original", 4096)
			args["associations"] = json.RawMessage(document)
			client.call("mousa_"+mode+"_query", args, "invalid_arguments")
			path := filepath.Join(root, fmt.Sprintf("invalid-%d.json", i))
			if err := os.WriteFile(path, []byte(document), 0600); err != nil {
				t.Fatal(err)
			}
			native := []string{"query", "--source", "alpha", "--" + mode + "-space", space, "--associations", path}
			if mode == "hybrid" {
				native = append(native, "Drain")
			}
			run.runInput(retrievalAssociationVector, true, native...)
		}
		args := mcpRetrievalArgs(space, mode, "original", 4096)
		args["associations"], args["context_tokens"] = json.RawMessage(valid), 4096
		client.call("mousa_"+mode+"_query", args, "tokenizer_unavailable")
	}
	if !reflect.DeepEqual(before, inspectionRows(t, run.store, nil)) {
		t.Fatal("invalid arguments/backend absence produced state")
	}
	client.close()
}

func TestMCPRetrievalAssociationSuppression(t *testing.T) {
	mcpRetrievalAssociationSuppression(t, false)
}
func TestMCPTokenRetrievalAssociationSuppression(t *testing.T) {
	mcpRetrievalAssociationSuppression(t, true)
}

func mcpRetrievalAssociationSuppression(t *testing.T, token bool) {
	var startup []string
	if token {
		startup = mcpTokenStartup(t)
	}
	for _, target := range []bool{false, true} {
		run, _ := setup(t)
		source, declaration, _ := activationPins(t, run, "alpha")
		jsonlSync(t, run, "alpha", record("primary.md", "Drain instruction."))
		from, to, query := "doc", "primary.md", "oldterm"
		if target {
			from, to, query = "primary.md", "doc", "Drain"
		}
		space := prepareMCPVectors(t, run, map[string][]float64{from: {1, 0}})
		active := activateQueryDeclaration(t, run, source, nil, &declaration, 1)
		client := startMCPClient(t, run, "cli", false, startup...)
		for _, mode := range []string{"vector", "hybrid"} {
			for _, packing := range []string{"original", "exact-v1"} {
				args := mcpRetrievalArgs(space, mode, packing, 4096)
				if mode == "hybrid" {
					args["query"] = query
				}
				args["associations"] = mcpAssociationDocument(mcpAssociation(from, to))
				if token {
					args["context_tokens"] = 4096
				}
				control := client.call("mousa_"+mode+"_query", args, "")
				if len(control["associated_evidence"].([]any)) != 1 {
					t.Fatal("control did not fire")
				}
				args["supersession"] = true
				got := client.call("mousa_"+mode+"_query", args, "")
				if got["associated_evidence"] != nil || !reflect.DeepEqual(control[mode], got[mode]) {
					t.Fatal("suppression changed ranks or released support", got)
				}
				if target {
					if len(got["evidence"].([]any)) != 1 || got["association_omissions"].([]any)[0].(map[string]any)["reason"] != "superseded" {
						t.Fatal("target suppression lost attribution", got)
					}
				} else if len(got["evidence"].([]any)) != 0 || got["association_omissions"] != nil {
					t.Fatal("suppressed primary fired support", got)
				}
			}
		}
		client.close()
		activateQueryDeclaration(t, run, source, &active, nil, 2)
	}
}

func TestMCPRetrievalAssociationTargetBoundaries(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", strings.Join([]string{
		record("primary.md", "Drain instruction."),
		record("support.md", "Separate qualification."),
		record("duplicate.md", "Separate qualification."),
		record("primary-copy.md", "Drain instruction."),
	}, "\n"))
	jsonlSync(t, run, "beta", record("foreign.md", "Foreign secret."))
	space := prepareAssociatedRetrieval(t, run, []string{"--source", "alpha"})
	client := startMCPClient(t, run, "cli", false)
	for _, mode := range []string{"vector", "hybrid"} {
		for _, packing := range []string{"original", "exact-v1"} {
			args := mcpRetrievalArgs(space, mode, packing, 4096)
			// A vector-only query isolates unembedded target duplicates from primaries.
			if mode == "hybrid" {
				args["query"] = "absent"
			}
			args["associations"] = mcpAssociationDocument(mcpAssociation("primary.md", "support.md"), mcpAssociation("primary.md", "duplicate.md"))
			got := client.call("mousa_"+mode+"_query", args, "")
			want := 2
			if packing == "exact-v1" {
				want = 1
			}
			if len(got["associated_evidence"].([]any)) != want {
				t.Fatal("support byte equality changed", got)
			}
			args["associations"] = mcpAssociationDocument(mcpAssociation("primary.md", "primary-copy.md"))
			got = client.call("mousa_"+mode+"_query", args, "")
			if packing == "exact-v1" && (got["associated_evidence"] != nil || got["association_omissions"].([]any)[0].(map[string]any)["reason"] != "duplicate") {
				t.Fatal("primary/support duplicate union changed", got)
			}
			for _, missing := range []string{"foreign.md", "missing.md"} {
				args["associations"] = mcpAssociationDocument(mcpAssociation("primary.md", missing))
				got = client.call("mousa_"+mode+"_query", args, "")
				if got["associated_evidence"] != nil || len(got["association_omissions"].([]any)) != 1 {
					t.Fatal("foreign/missing target released", got)
				}
			}
			args["associations"] = mcpAssociationDocument(mcpAssociation("support.md", "duplicate.md"))
			got = client.call("mousa_"+mode+"_query", args, "")
			if got["associated_evidence"] != nil || got["association_omissions"] != nil {
				t.Fatal("unselected source declaration fired", got)
			}
		}
	}
	jsonlSync(t, run, "alpha", record("primary.md", "Revised instruction."))
	for _, mode := range []string{"vector", "hybrid"} {
		args := mcpRetrievalArgs(space, mode, "original", 4096)
		if mode == "hybrid" {
			args["query"] = "absent"
		}
		args["associations"] = mcpAssociationDocument(mcpAssociation("primary.md", "support.md"))
		got := client.call("mousa_"+mode+"_query", args, "")
		if got["indexed"] != float64(0) || got["missing"] != float64(4) || len(got["evidence"].([]any)) != 0 || got["associated_evidence"] != nil || got["association_omissions"] != nil {
			t.Fatal("zero coverage fired support or changed source admission", got)
		}
	}
	client.close()
}

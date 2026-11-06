package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestNativeTokenAssociationSupersession(t *testing.T) {
	startup := mcpTokenStartup(t)
	for _, directory := range []bool{false, true} {
		for _, packing := range []string{"original", "exact-v1"} {
			t.Run(fmt.Sprintf("directory=%v/%s", directory, packing), func(t *testing.T) {
				run, root := setup(t)
				selector := []string{"--source", "alpha"}
				source := declarationSource(t, "alpha")
				texts := map[string]string{"primary.txt": "needle primary", "old.txt": "predecessor private", "new.txt": "successor support", "extra.txt": "untriggered extra"}
				if directory {
					selector = []string{root}
					var err error
					source, _, err = directorySource(root)
					if err != nil {
						t.Fatal(err)
					}
					for item, text := range texts {
						writeFile(t, root, item, text)
					}
					run.run(false, "sync", root)
				} else {
					var records []string
					for item, text := range texts {
						records = append(records, record(item, text))
					}
					jsonlSync(t, run, "alpha", strings.Join(records, "\n"))
				}
				pin := func(term string) mousa.RepresentationID {
					_, got := run.run(false, append(append([]string{"query"}, selector...), term)...)
					id, err := mousa.ParseRepresentationID(hits(got)[0].(map[string]any)["representation_id"].(string))
					if err != nil {
						t.Fatal(err)
					}
					return id
				}
				d := storedDeclaration(t, run, declarationJSON(t, source, "old.txt", pin("predecessor"), "new.txt", pin("successor"), "example.operator", "replacement"))
				activateQueryDeclaration(t, run, source, nil, &d, 1)
				document := mcpAssociationDocument(mcpAssociation("primary.txt", "old.txt"), mcpAssociation("primary.txt", "new.txt"), mcpAssociation("old.txt", "extra.txt"))
				data, err := json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
				path := writeDeclarationFile(t, t.TempDir(), string(data))
				query := func(tokens, bytes int, text string) map[string]any {
					args := append([]string{"query", "--supersession", "--associations", path, "--packing-policy", packing, "--context-tokens", strconv.Itoa(tokens), "--budget-bytes", strconv.Itoa(bytes)}, startup...)
					_, got := run.run(false, append(append(args, selector...), text)...)
					return got
				}
				full := query(4096, 8192, "needle")
				assertTripleSupport(t, full)
				count := officialTokenCount(t, full["context"].(string))
				bytes := len(texts["primary.txt"]) + len(texts["new.txt"])
				exact := query(count, bytes, "needle")
				if exact["context"] != full["context"] {
					t.Fatal("exact token and byte fit changed rendering", exact)
				}
				for reason, short := range map[string]map[string]any{"tokens": query(count-1, bytes, "needle"), "bytes": query(count, bytes-1, "needle")} {
					if len(hits(short)) != 1 || hits(short)[0].(map[string]any)["item"] != "primary.txt" || short["association_omissions"].([]any)[1].(map[string]any)["reason"] != reason {
						t.Fatal("one-under budget displaced primary or lost omission", reason, short)
					}
				}
				historyArgs := append([]string{"trail", "--verify-tokens"}, startup...)
				_, history := run.run(false, append(append(historyArgs, selector...), exact["trail_id"].(string))...)
				h := history["historical"].(map[string]any)
				if h["schema"] != "mousa.source_trail.v13" || h["token_verification"] != "official_recount_verified" || !reflect.DeepEqual(h["token"], exact["token"]) {
					t.Fatal("triple history/recount disagreement", h)
				}
				withheld := query(4096, 8192, "predecessor")
				if withheld["context"] != "" || withheld["association_omissions"] != nil || withheld["supersession_excluded"] != float64(1) {
					t.Fatal("suppressed primary fired support", withheld)
				}
				if counts := withheld["token"].(map[string]any)["proposed_tokens"].([]any); len(counts) != 1 || counts[0] != nil {
					t.Fatal("suppressed primary was counted", withheld)
				}
				_, history = run.run(false, append(append([]string{"trail"}, selector...), withheld["trail_id"].(string))...)
				if history["historical"].(map[string]any)["schema"] != "mousa.source_trail.v8" {
					t.Fatal("non-firing declarations changed legacy version", history)
				}
			})
		}
	}
}

func assertTripleSupport(t *testing.T, got map[string]any) {
	t.Helper()
	evidence := hits(got)
	if len(evidence) != 2 || evidence[0].(map[string]any)["origin"] != "lexical" || evidence[1].(map[string]any)["origin"] != "association" || strings.Contains(got["context"].(string), "predecessor") {
		t.Fatal("suppressed target released or support lost", got)
	}
	omissions := got["association_omissions"].([]any)
	if len(omissions) != 1 || omissions[0].(map[string]any)["reason"] != "superseded" {
		t.Fatal("suppressed target omission lost", got)
	}
	token := got["token"].(map[string]any)
	association := token["associations"].(map[string]any)
	if len(association["associated_ids"].([]any)) != 2 || len(association["proposed_tokens"].([]any)) != 2 || association["proposed_tokens"].([]any)[0] != nil || association["proposed_tokens"].([]any)[1] == nil {
		t.Fatal("withheld target counted or survivor count lost", token)
	}
}

func TestMCPTokenAssociationSupersession(t *testing.T) {
	startup := mcpTokenStartup(t)
	run, _ := setup(t)
	newMCPWithholdingTrailFixture(t, run, "alpha", "predecessor private", "successor support")
	jsonlSync(t, run, "alpha", record("primary", "needle primary"))
	client := startMCPClient(t, run, "cli", false, startup...)
	defer client.close()
	for _, packing := range []string{"original", "exact-v1"} {
		args := mcpQuery("alpha", "needle", 8192)
		args["packing_policy"], args["context_tokens"], args["supersession"] = packing, 4096, true
		args["associations"] = mcpAssociationDocument(mcpAssociation("primary", "doc"), mcpAssociation("primary", "corrected"), mcpAssociation("doc", "primary"))
		delete(args, "supersession")
		omitted := client.call("mousa_query", args, "")
		args["supersession"] = false
		disabled := client.call("mousa_query", args, "")
		if len(hits(disabled)) != 3 || !reflect.DeepEqual(mcpStableQueryValues(t, omitted), mcpStableQueryValues(t, disabled)) {
			t.Fatal("omitted/false associations changed the uncurated default", omitted, disabled)
		}
		args["supersession"] = true
		full := client.call("mousa_query", args, "")
		assertTripleSupport(t, full)
		count := assertMCPTokenRendering(t, full)
		args["context_tokens"], args["budget_bytes"] = count, len("needle primary")+len("successor support")
		exact := client.call("mousa_query", args, "")
		if exact["context"] != full["context"] {
			t.Fatal("MCP exact fit changed context")
		}
		for _, reason := range []string{"tokens", "bytes"} {
			if reason == "tokens" {
				args["context_tokens"] = count - 1
			} else {
				args["context_tokens"], args["budget_bytes"] = count, len("needle primary")+len("successor support")-1
			}
			short := client.call("mousa_query", args, "")
			if len(hits(short)) != 1 || short["association_omissions"].([]any)[1].(map[string]any)["reason"] != reason {
				t.Fatal("MCP independent budget not enforced", reason, short)
			}
		}
		for _, supersession := range []bool{false, true} {
			for _, tokens := range []bool{false, true} {
				h := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": exact["trail_id"], "supersession": supersession, "token_details": tokens}, "")["historical"].(map[string]any)
				if h["schema"] != "mousa.source_trail.v13" || (h["supersession"] != nil) != supersession || (h["token"] != nil) != tokens || len(h["associated"].([]any)) != 2 {
					t.Fatal("history opt-ins coupled", h)
				}
				if tokens && !reflect.DeepEqual(h["token"], exact["token"]) {
					t.Fatal("history count projection differs", h)
				}
			}
		}
		for _, field := range []string{"tokenizer_python", "tokenizer_cache"} {
			args[field] = "/client/supplied/backend"
			client.call("mousa_query", args, "invalid_arguments")
			delete(args, field)
		}
	}
}

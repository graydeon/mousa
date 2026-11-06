package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestNativeAndMCPAssociatedSupersession(t *testing.T) {
	t.Parallel()
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			run, root := setup(t)
			source, declaration, _ := activationPins(t, run, "alpha")
			activateQueryDeclaration(t, run, source, nil, &declaration, 1)
			document := mcpAssociationDocument(mcpAssociation("amended", "doc"), mcpAssociation("amended", "corrected"), mcpAssociation("doc", "corrected"))
			data, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			path := writeDeclarationFile(t, root, string(data))
			raw, native := run.run(false, "query", "--source", "alpha", "--supersession", "--associations", path, "--packing-policy", packing, "amended")
			check := func(r map[string]any) {
				t.Helper()
				h := hits(r)
				if len(h) != 2 || h[0].(map[string]any)["item"] != "amended" || h[1].(map[string]any)["item"] != "corrected" || h[1].(map[string]any)["origin"] != "association" {
					t.Fatalf("composed release: %v", r)
				}
				omissions := r["association_omissions"].([]any)
				if len(omissions) != 1 || omissions[0].(map[string]any)["reason"] != "superseded" {
					t.Fatalf("suppressed target reason: %v", r)
				}
			}
			check(native)
			if strings.Contains(raw, "oldterm predecessor evidence") {
				t.Fatal("native leaked suppressed support")
			}
			history := supersessionHistory(t, run, "alpha", native)
			if history["schema"] != "mousa.source_trail.v12" || len(history["associated"].([]any)) != 2 {
				t.Fatalf("native history: %v", history)
			}
			client := startMCPClient(t, run, "cli", false)
			args := map[string]any{"source": "alpha", "query": "amended", "policy": "original", "budget_bytes": 8192, "packing_policy": packing, "supersession": true, "associations": document}
			got := client.call("mousa_query", args, "")
			check(got)
			opted := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": got["trail_id"], "supersession": true}, "")
			h := opted["historical"].(map[string]any)
			if h["schema"] != "mousa.source_trail.v12" || h["supersession"] == nil || len(h["associated"].([]any)) != 2 {
				t.Fatalf("opted history: %v", h)
			}
			hidden := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": got["trail_id"]}, "")
			if hidden["historical"].(map[string]any)["supersession"] != nil {
				t.Fatal("default history exposed consultation")
			}
			args["context_tokens"] = 4096
			args["tokenizer_python"] = "/client/supplied/python"
			client.call("mousa_query", args, "invalid_arguments")
			delete(args, "context_tokens")
			delete(args, "tokenizer_python")
			run.run(false, "access", "--source", "alpha", "deny")
			denied := client.call("mousa_query", args, "")
			if denied["decision_outcome"] != "deny" || len(hits(denied)) != 0 || denied["association_omissions"] != nil {
				t.Fatalf("denied composed query disclosed evidence: %v", denied)
			}
			deniedHistory := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": got["trail_id"], "supersession": true}, "")
			if deniedHistory["historical"] != nil {
				t.Fatal("denied composed history disclosed metadata")
			}
			client.close()
		})
	}
}

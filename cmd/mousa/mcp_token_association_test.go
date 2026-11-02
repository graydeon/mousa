package main

import (
	"reflect"
	"testing"
)

func TestMCPTokenAssociationsAndIndependentHistory(t *testing.T) {
	t.Parallel()
	startup := mcpTokenStartup(t)
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("backup", "Run backup inside the connection context manager.")+"\n"+record("closure", "The context manager does not close the connection."))
	client := startMCPClient(t, run, "cli", false, startup...)
	defer client.close()
	for _, policy := range []string{"original", "dedup"} {
		for _, packing := range []string{"original", "exact-v1"} {
			args := mcpQuery("alpha", "backup", 8192)
			args["policy"], args["packing_policy"], args["associations"], args["context_tokens"] = policy, packing, mcpAssociationDocument(mcpAssociation("backup", "closure")), 4096
			full := client.call("mousa_query", args, "")
			count := assertMCPTokenRendering(t, full)
			args["context_tokens"] = count
			args["supersession"] = false
			exact := client.call("mousa_query", args, "")
			args["context_tokens"] = count - 1
			short := client.call("mousa_query", args, "")
			if exact["context"] != full["context"] || len(exact["evidence"].([]any)) != 2 || len(short["evidence"].([]any)) != 1 || !reflect.DeepEqual(exact["evidence"].([]any)[0], short["evidence"].([]any)[0]) {
				t.Fatal("support displaced primary or boundary changed", exact, short)
			}
			associated := exact["evidence"].([]any)[1].(map[string]any)
			if associated["origin"] != "association" || associated["rank"] != nil || associated["bm25_score"] != nil {
				t.Fatal("associated evidence gained lexical facts")
			}
			for _, details := range []bool{false, true} {
				history := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": exact["trail_id"], "token_details": details}, "")["historical"].(map[string]any)
				if history["schema"] != "mousa.source_trail.v11" || len(history["associated"].([]any)) != 1 || (history["token"] != nil) != details {
					t.Fatal("independent association/token history", history)
				}
				if details && !reflect.DeepEqual(history["token"], exact["token"]) {
					t.Fatal("producing token facts changed")
				}
			}
			args["context_tokens"] = 1
			omitted := client.call("mousa_query", args, "")
			if omitted["context"] != "" || omitted["association_omissions"] != nil {
				t.Fatal("unselected primary triggered association", omitted)
			}
			args["supersession"] = true
			client.call("mousa_query", args, "invalid_arguments")
		}
	}
}

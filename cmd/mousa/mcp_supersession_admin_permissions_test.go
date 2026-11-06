package main

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func adminDeclarationDraft(t *testing.T, passages []any) map[string]any {
	t.Helper()
	pins := make(map[string]any)
	for _, raw := range passages {
		p := raw.(map[string]any)
		pins[p["item"].(string)] = p["representation_id"]
	}
	if pins["old"] == nil || pins["new"] == nil {
		t.Fatal("missing declaration fixture pins")
	}
	return map[string]any{"schema": "mousa.supersession_declaration_input.v1", "predecessor_item_id": "old", "predecessor_representation_id": pins["old"], "successor_item_id": "new", "successor_representation_id": pins["new"], "author": "supplied.author", "basis": "supplied replacement assertion"}
}

func adminActivationDraft(previous, declaration any, occurred int64) map[string]any {
	return map[string]any{"schema": "mousa.supersession_activation_input.v1", "expected_previous_activation_id": previous, "declaration_id": declaration, "actor_id": "supplied.actor", "actor_version": "console/1", "occurred_at_usec": occurred, "reason": nil}
}

func adminState(t *testing.T, path string) string {
	t.Helper()
	var state string
	for _, table := range []string{"supersession_declarations", "supersession_activations", "supersession_activation_state", "sources", "representations", "local_items"} {
		count, digest := tableDigest(t, path, table)
		state += fmt.Sprintf("%s:%d/%s;", table, count, digest)
	}
	return state
}

func TestMCPSupersessionAdministrationPermissionsAndLifecycle(t *testing.T) {
	run, _ := setup(t)
	ingest := startMCPClient(t, run, "cli", true)
	ingest.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "old", "text": "Old assertion."}, map[string]any{"id": "new", "text": "Replacement assertion."}), "")
	ingest.call("mousa_sync", mcpItems("beta", map[string]any{"id": "old", "text": "Other old."}, map[string]any{"id": "new", "text": "Other new."}), "")
	inputs := ingest.call("mousa_vector_inputs", map[string]any{"source": "alpha"}, "")
	draft := adminDeclarationDraft(t, inputs["passages"].([]any))
	declare := map[string]any{"source": "alpha", "declaration": draft}
	before := adminState(t, run.store)
	ingest.call("mousa_supersession_declare", declare, "supersession_admin_not_permitted")
	ingest.call("mousa_supersession_activate", map[string]any{"source": "alpha", "activation": adminActivationDraft(nil, strings.Repeat("1", 64), 1)}, "supersession_admin_not_permitted")
	if adminState(t, run.store) != before {
		t.Fatal("ingestion permission authorized administration")
	}
	ingest.close()
	reader := startMCPClient(t, run, "cli", false)
	reader.call("mousa_supersession_declare", declare, "supersession_admin_not_permitted")
	status := reader.call("mousa_supersession_status", map[string]any{"source": "alpha"}, "")
	if status["current"].(map[string]any)["state"] != "no_history" {
		t.Fatal(status)
	}
	reader.close()
	run.runInput("{\"id\":\"old\",\"text\":\"A later unpinned revision.\"}\n", false, "sync", "--source", "alpha")
	admin := startMCPClient(t, run, "cli", false, "--supersession-admin-source", "alpha", "--supersession-admin-source", "beta")
	admin.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "forbidden", "text": "no ingestion"}), "ingestion_not_permitted")
	admin.call("mousa_supersession_status", map[string]any{"source": "/unconfigured/path"}, "source_not_permitted")
	admin.call("mousa_supersession_declare", map[string]any{"source": "/unconfigured/path", "declaration": draft}, "source_not_permitted")
	declaration := admin.call("mousa_supersession_declare", declare, "")["declaration"].(map[string]any)
	before = adminState(t, run.store)
	if retry := admin.call("mousa_supersession_declare", declare, "")["declaration"]; !reflect.DeepEqual(retry, declaration) || adminState(t, run.store) != before {
		t.Fatal("declaration retry changed stored state")
	}
	activate := map[string]any{"source": "alpha", "activation": adminActivationDraft(nil, declaration["id"], 1)}
	initial := admin.call("mousa_supersession_activate", activate, "")["activation"].(map[string]any)
	before = adminState(t, run.store)
	if retry := admin.call("mousa_supersession_activate", activate, "")["activation"]; !reflect.DeepEqual(retry, initial) || adminState(t, run.store) != before {
		t.Fatal("current activation retry changed stored state")
	}
	for _, field := range []string{"actor_id", "actor_version", "occurred_at_usec", "reason"} {
		changed := adminActivationDraft(nil, declaration["id"], 1)
		changed[field] = "changed"
		if field == "occurred_at_usec" {
			changed[field] = int64(2)
		}
		admin.call("mousa_supersession_activate", map[string]any{"source": "alpha", "activation": changed}, "conflict")
	}
	admin.call("mousa_supersession_activate", map[string]any{"source": "beta", "activation": adminActivationDraft(nil, declaration["id"], 2)}, "conflict")
	admin.call("mousa_supersession_declare", map[string]any{"source": "beta", "declaration": draft}, "integrity")
	if adminState(t, run.store) != before {
		t.Fatal("conflicting or cross-source request changed canonical state")
	}
	status = admin.call("mousa_supersession_status", map[string]any{"source": "alpha"}, "")
	current := status["current"].(map[string]any)
	if current["state"] != "active" || !reflect.DeepEqual(current["activation"], initial) || !reflect.DeepEqual(current["declaration"], declaration) {
		t.Fatal(status)
	}
	deactivate := map[string]any{"source": "alpha", "activation": adminActivationDraft(initial["id"], nil, 3)}
	inactive := admin.call("mousa_supersession_activate", deactivate, "")["activation"].(map[string]any)
	before = adminState(t, run.store)
	admin.call("mousa_supersession_activate", activate, "conflict")
	if adminState(t, run.store) != before {
		t.Fatal("older-event retry reactivated state")
	}
	status = admin.call("mousa_supersession_status", map[string]any{"source": "alpha"}, "")
	current = status["current"].(map[string]any)
	if current["state"] != "deactivated" || current["declaration"] != nil || !reflect.DeepEqual(current["activation"], inactive) {
		t.Fatal(status)
	}
	run.run(false, "access", "--source", "alpha", "deny")
	reactivated := admin.call("mousa_supersession_activate", map[string]any{"source": "alpha", "activation": adminActivationDraft(inactive["id"], declaration["id"], 4)}, "")
	if reactivated["activation"] == nil {
		t.Fatal("denied-read administrator could not mutate supplied records")
	}
	status = admin.call("mousa_supersession_status", map[string]any{"source": "alpha"}, "")
	if status["decision_outcome"] != "deny" || len(status) != 3 || status["current"] != nil {
		t.Fatalf("denied status leaked history: %v", status)
	}
	run.run(false, "withdraw", "--source", "alpha")
	admin.call("mousa_supersession_declare", declare, "")
	db, err := sql.Open("sqlite", run.store)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM supersession_activation_state`); err != nil {
		t.Fatal(err)
	}
	damaged := admin.call("mousa_supersession_status", map[string]any{"source": "alpha"}, "")
	if damaged["decision_outcome"] != "deny" || len(damaged) != 3 || damaged["current"] != nil {
		t.Fatalf("damaged hidden history leaked: %v", damaged)
	}
	admin.close()
}

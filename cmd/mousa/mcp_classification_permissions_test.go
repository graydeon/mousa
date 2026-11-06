package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func classificationState(t *testing.T, path string) string {
	t.Helper()
	state := adminState(t, path)
	for _, table := range []string{"classifications", "classification_bases", "observations", "artifacts", "segments"} {
		n, digest := tableDigest(t, path, table)
		state += fmt.Sprintf("%s:%d/%s;", table, n, digest)
	}
	return state
}

func TestMCPClassificationStdioPermissionsAndRetrieval(t *testing.T) {
	run, _ := setup(t)
	ingest := startMCPClient(t, run, "cli", true)
	ingest.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "doc", "text": "cedar evidence"}), "")
	ingest.call("mousa_sync", mcpItems("beta", map[string]any{"id": "doc", "text": "foreign evidence"}), "")
	inputs := ingest.call("mousa_vector_inputs", map[string]any{"source": "alpha"}, "")
	pin := inputs["passages"].([]any)[0].(map[string]any)
	draft := classificationDraft(map[string]any{"kind": "segment", "id": pin["segment_id"]})
	draft["basis"] = []any{map[string]any{"kind": "representation", "id": pin["representation_id"]}}
	put := map[string]any{"source": "alpha", "classification": draft}
	ingest.close()
	for mask := range 8 {
		t.Run(fmt.Sprintf("permissions-%d", mask), func(t *testing.T) {
			var flags []string
			if mask&2 != 0 {
				flags = append(flags, "--supersession-admin-source", "alpha")
			}
			if mask&4 != 0 {
				flags = append(flags, "--classification-admin-source", "alpha")
			}
			client := startMCPClient(t, run, "cli", mask&1 != 0, flags...)
			defer client.close()
			before := classificationState(t, run.store)
			if mask&4 == 0 {
				client.call("mousa_classification_put", put, "classification_admin_not_permitted")
				if classificationState(t, run.store) != before {
					t.Fatal("refused mutation changed state")
				}
				return
			}
			query := map[string]any{"source": "alpha", "query": "cedar", "policy": "original", "budget_bytes": 4096}
			beforeQuery := client.call("mousa_query", query, "")
			saved := client.call("mousa_classification_put", put, "")["classification"].(map[string]any)
			before = classificationState(t, run.store)
			retry := client.call("mousa_classification_put", put, "")["classification"]
			if !reflect.DeepEqual(saved, retry) || classificationState(t, run.store) != before {
				t.Fatal("retry changed record or rows")
			}
			get := map[string]any{"source": "alpha", "classification_id": saved["id"]}
			if got := client.call("mousa_classification_get", get, "")["classification"]; !reflect.DeepEqual(got, saved) {
				t.Fatal("readback mismatch")
			}
			afterQuery := client.call("mousa_query", query, "")
			for _, field := range []string{"packet_id", "evidence", "decision_outcome", "decision_reasons", "used_bytes", "budget_omitted", "matched_candidates", "packing_policy"} {
				if !reflect.DeepEqual(beforeQuery[field], afterQuery[field]) {
					t.Fatalf("labels changed query %s", field)
				}
			}
			if saved["confidence_ppm"] != float64(0) {
				t.Fatal("lost explicit confidence zero")
			}
			client.call("mousa_classification_get", map[string]any{"source": "beta", "classification_id": saved["id"]}, "not_found")
			client.call("mousa_classification_put", map[string]any{"source": "beta", "classification": draft}, "classification_admin_not_permitted")
			client.call("mousa_classification_put", map[string]any{"source": "/unconfigured/path", "classification": draft}, "source_not_permitted")
			if mask&1 == 0 {
				client.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "forbidden", "text": "no"}), "ingestion_not_permitted")
			}
			if mask&2 == 0 {
				client.call("mousa_supersession_declare", map[string]any{"source": "alpha", "declaration": supersessionDraftFixture(false)}, "supersession_admin_not_permitted")
			}
		})
	}
	reader := startMCPClient(t, run, "cli", false)
	// No administration permission is needed for a current authorized read.
	admin := startMCPClient(t, run, "cli", false, "--classification-admin-source", "alpha")
	saved := admin.call("mousa_classification_put", put, "")["classification"].(map[string]any)
	get := map[string]any{"source": "alpha", "classification_id": saved["id"]}
	reader.call("mousa_classification_get", get, "")
	draft["asserter_version"] = "2"
	changed := admin.call("mousa_classification_put", put, "")["classification"].(map[string]any)
	if changed["id"] == saved["id"] {
		t.Fatal("changed assertion overwrote identity")
	}
	reader.call("mousa_classification_get", get, "")
	run.run(false, "access", "--source", "alpha", "deny")
	for _, id := range []any{saved["id"], strings.Repeat("a", 64)} {
		denied := reader.call("mousa_classification_get", map[string]any{"source": "alpha", "classification_id": id}, "")
		if len(denied) != 3 || denied["decision_outcome"] != "deny" || denied["classification"] != nil {
			t.Fatalf("denial leaked: %v", denied)
		}
	}
	draft["asserter_version"] = "3"
	admin.call("mousa_classification_put", put, "")
	rawExec(t, run.store, `UPDATE classifications SET record_json=x'7b7d0a'`)
	denied := reader.call("mousa_classification_get", get, "")
	if len(denied) != 3 || denied["decision_outcome"] != "deny" {
		t.Fatal("hidden corruption changed denial")
	}
	admin.close()
	reader.close()
}

func TestMCPClassificationStrictFailuresPreserveSQLite(t *testing.T) {
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", true, "--classification-admin-source", "alpha")
	defer client.close()
	client.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "doc", "text": "cedar"}), "")
	source, err := streamSource("alpha")
	if err != nil {
		t.Fatal(err)
	}
	draft := classificationDraft(map[string]any{"kind": "source", "id": source.ID.String()})
	before := classificationState(t, run.store)
	for _, field := range []string{"schema", "subject", "taxonomy", "taxonomy_version", "label", "asserter_id", "asserter_version", "basis", "asserted_at_usec", "confidence_ppm"} {
		value := draft[field]
		delete(draft, field)
		client.call("mousa_classification_put", map[string]any{"source": "alpha", "classification": draft}, "invalid_arguments")
		draft[field] = nil
		client.call("mousa_classification_put", map[string]any{"source": "alpha", "classification": draft}, "invalid_arguments")
		draft[field] = value
	}
	if classificationState(t, run.store) != before {
		t.Fatal("invalid drafts changed SQLite")
	}
	draft["subject"] = map[string]any{"kind": "source", "id": strings.Repeat("b", 64)}
	client.call("mousa_classification_put", map[string]any{"source": "alpha", "classification": draft}, "not_found")
	if classificationState(t, run.store) != before {
		t.Fatal("unproven parent created placeholders")
	}
}

func TestMCPClassificationAllKindsHistoricalAndRollback(t *testing.T) {
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", true, "--classification-admin-source", "alpha")
	defer client.close()
	client.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "doc", "text": "cedar historical"}), "")
	db, err := sql.Open("sqlite", run.store)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	subjects := make([]any, 0, 5)
	for _, entry := range []struct{ kind, table string }{{"source", "sources"}, {"observation", "observations"}, {"artifact", "artifacts"}, {"representation", "representations"}, {"segment", "segments"}} {
		var id string
		if err := db.QueryRow("SELECT lower(hex(id)) FROM " + entry.table + " LIMIT 1").Scan(&id); err != nil {
			t.Fatal(err)
		}
		subjects = append(subjects, map[string]any{"kind": entry.kind, "id": id})
	}
	client.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "doc", "deleted": true}), "")
	for _, subject := range subjects {
		draft := classificationDraft(subject)
		draft["basis"] = subjects
		args := map[string]any{"source": "alpha", "classification": draft}
		saved := client.call("mousa_classification_put", args, "")["classification"].(map[string]any)
		if !reflect.DeepEqual(saved["basis"], subjects) {
			t.Fatal("ordered basis changed")
		}
		client.call("mousa_classification_get", map[string]any{"source": "alpha", "classification_id": saved["id"]}, "")
		draft["basis"] = []any{subjects[4], subjects[3], subjects[2], subjects[1], subjects[0]}
		reordered := client.call("mousa_classification_put", args, "")["classification"].(map[string]any)
		if reordered["id"] == saved["id"] {
			t.Fatal("basis order did not bind identity")
		}
	}
	rawExec(t, run.store, `CREATE TRIGGER reject_mcp_classification_basis BEFORE INSERT ON classification_bases BEGIN SELECT RAISE(ABORT, 'test rollback'); END`)
	before := classificationState(t, run.store)
	draft := classificationDraft(subjects[0])
	draft["label"] = "rollback"
	response, err := client.session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mousa_classification_put", Arguments: map[string]any{"source": "alpha", "classification": draft}})
	if err != nil || !response.IsError {
		t.Fatalf("trigger refusal: %v %v", response, err)
	}
	if classificationState(t, run.store) != before {
		t.Fatal("failed basis append committed partial state")
	}
}

func TestMCPClassificationConfiguredPathAliasAndCancellation(t *testing.T) {
	run, _ := setup(t)
	const alias = "/not/a/filesystem/source.jsonl"
	client := startMCPClient(t, run, "cli", false, "--source", alias, "--ingest-source", alias, "--classification-admin-source", alias)
	defer client.close()
	client.call("mousa_sync", mcpItems(alias, map[string]any{"id": "doc", "text": "cedar"}), "")
	source, err := streamSource(alias)
	if err != nil {
		t.Fatal(err)
	}
	draft := classificationDraft(map[string]any{"kind": "source", "id": source.ID.String()})
	args := map[string]any{"source": alias, "classification": draft}
	saved := client.call("mousa_classification_put", args, "")["classification"].(map[string]any)
	client.call("mousa_classification_get", map[string]any{"source": alias, "classification_id": saved["id"]}, "")
	store, err := sqlite.Open(t.Context(), run.store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	application := &mcpApplication{store: store, path: run.store, caller: "cli", sources: map[string]mcpSource{alias: {source: source, classificationAdmin: true}}, gate: make(chan struct{}, 1)}
	before := classificationState(t, run.store)
	draft["asserter_version"] = "cancelled"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := application.call(ctx, "mousa_classification_put", mustJSON(t, args))
	if err != nil || !result.IsError || !strings.Contains(string(mustJSON(t, result.StructuredContent)), "cancelled") {
		t.Fatalf("cancelled dispatch: %v %v", result, err)
	}
	if classificationState(t, run.store) != before {
		t.Fatal("cancelled call changed state")
	}
}

func TestMCPClassificationForeignBasisNativeFixture(t *testing.T) {
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", true, "--classification-admin-source", "alpha", "--classification-admin-source", "beta")
	defer client.close()
	for _, alias := range []string{"alpha", "beta"} {
		client.call("mousa_sync", mcpItems(alias, map[string]any{"id": "doc", "text": "cedar"}), "")
	}
	alpha, err := streamSource("alpha")
	if err != nil {
		t.Fatal(err)
	}
	beta, err := streamSource("beta")
	if err != nil {
		t.Fatal(err)
	}
	draft := classificationDraft(map[string]any{"kind": "source", "id": alpha.ID.String()})
	draft["basis"] = []any{map[string]any{"kind": "source", "id": beta.ID.String()}}
	args := map[string]any{"source": "alpha", "classification": draft}
	before := classificationState(t, run.store)
	client.call("mousa_classification_put", args, "not_found")
	if classificationState(t, run.store) != before {
		t.Fatal("foreign basis refusal changed rows")
	}
	// Native-only setup establishes a valid cross-source negative fixture.
	store, err := sqlite.Open(t.Context(), run.store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	decoded, err := decodeMCPArguments(mustJSON(t, args), "mousa_classification_put")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutClassification(t.Context(), decoded.Classification); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"alpha", "beta"} {
		client.call("mousa_classification_get", map[string]any{"source": alias, "classification_id": decoded.Classification.ID.String()}, "not_found")
	}
	rawExec(t, run.store, `UPDATE classifications SET record_json=x'7b7d0a'`)
	for _, alias := range []string{"alpha", "beta"} {
		client.call("mousa_classification_get", map[string]any{"source": alias, "classification_id": decoded.Classification.ID.String()}, "not_found")
	}
}

func TestMCPClassificationStdioRawStrictnessAndDraftLimit(t *testing.T) {
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", true, "--classification-admin-source", "alpha")
	defer client.close()
	client.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "doc", "text": "cedar"}), "")
	source, err := streamSource("alpha")
	if err != nil {
		t.Fatal(err)
	}
	draft := classificationDraft(map[string]any{"kind": "source", "id": source.ID.String()})
	raw := mustJSON(t, draft)
	wrap := func(draft []byte) json.RawMessage {
		return append(append([]byte(`{"source":"alpha","classification":`), draft...), '}')
	}
	call := func(arguments json.RawMessage, code string) {
		t.Helper()
		result, err := client.session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mousa_classification_put", Arguments: arguments})
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(mustJSON(t, result.StructuredContent), &envelope); err != nil {
			t.Fatal(err)
		}
		if result.IsError != (code != "") {
			t.Fatalf("unexpected error state: %v", envelope)
		}
		if code != "" && envelope["error"].(map[string]any)["code"] != code {
			t.Fatalf("wrong refusal: %v", envelope)
		}
		if err := client.schemas["mousa_classification_put"].Validate(envelope); err != nil {
			t.Fatal(err)
		}
	}
	before := classificationState(t, run.store)
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"source":"alpha","source":"beta","classification":` + string(raw) + `}`),
		json.RawMessage(`{"Source":"alpha","classification":` + string(raw) + `}`),
		wrap(bytes.Replace(raw, []byte(`"confidence_ppm":0`), []byte(`"confidence_ppm":0,"confidence_ppm":0`), 1)),
		wrap(bytes.Replace(raw, []byte(`"confidence_ppm":0`), []byte(`"confidence_ppm":false`), 1)),
		wrap(bytes.Replace(raw, []byte(`"confidence_ppm":0`), []byte(`"confidence_ppm":"0"`), 1)),
		wrap(bytes.Replace(raw, []byte(`"kind":"source"`), []byte(`"kind":"source","kind":"source"`), 1)),
		wrap(bytes.ReplaceAll(raw, []byte(`"kind":"source"`), []byte(`"Kind":"source"`))),
		wrap(bytes.Replace(raw, []byte(`"subject":{`), []byte(`"subject":{"unknown":1,`), 1)),
		wrap(bytes.Replace(raw, []byte(`"subject":{`), []byte(`"subject":{"kind":null,`), 1)),
	} {
		call(invalid, "invalid_arguments")
	}
	draft["label"] = strings.Repeat("x", 1<<20)
	call(wrap(mustJSON(t, draft)), "invalid_arguments")
	if classificationState(t, run.store) != before {
		t.Fatal("raw invalid drafts changed canonical tables")
	}
	call(wrap(raw), "")
	after := classificationState(t, run.store)
	call(wrap(raw), "")
	if classificationState(t, run.store) != after {
		t.Fatal("raw retry changed canonical rows")
	}
}

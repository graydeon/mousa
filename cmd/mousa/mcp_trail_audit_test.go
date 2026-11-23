package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// inspectionRows includes WAL-visible rows from every table: a read path must
// leave the store byte-identical.
func inspectionRows(t *testing.T, path string) map[string][]string {
	t.Helper()
	db := openRawStore(t, path)
	tables, err := db.Query(`SELECT name FROM sqlite_schema WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := tables.Err(); err != nil {
		t.Fatal(err)
	}
	tables.Close()
	state := make(map[string][]string, len(names))
	for _, name := range names {
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		state[name] = []string{}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal(err)
			}
			state[name] = append(state[name], mcpCanonicalJSON(t, values))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		slices.Sort(state[name])
	}
	return state
}

// The caller view is the only metadata contract a caller can inspect: denied
// decisions disclose exactly the scoped identity and outcome, and none of the
// retrieval snapshot facts ever leave the store.
func TestMCPAuditedDenialView(t *testing.T) {
	run, _ := setup(t)
	run.runInput(record("doc", "harbor seeded evidence")+"\n", false, "sync", "--source", "seeded")
	run.runInput(record("doc", "harbor allowed evidence")+"\n", false, "sync", "--source", "allowed", "--allow")
	run.runInput(record("doc", "harbor denied evidence")+"\n", false, "sync", "--source", "denied", "--allow")
	run.run(false, "access", "--source", "denied", "deny")
	run.runInput(record("doc", "harbor withdrawn evidence")+"\n", false, "sync", "--source", "withdrawn", "--allow")
	run.run(false, "withdraw", "--source", "withdrawn")
	run.runInput(record("doc", "harbor stateless evidence")+"\n", false, "sync", "--source", "stateless", "--allow")
	client := startMCPClient(t, run, "cli", false,
		"--source", "allowed", "--source", "seeded", "--source", "denied", "--source", "withdrawn", "--source", "stateless", "--source", "unknown")
	stateless, err := streamSource("stateless")
	if err != nil {
		t.Fatal(err)
	}
	rawExec(t, run.store, fmt.Sprintf(`DELETE FROM source_ingest_state WHERE source_id = X'%s'`, stateless.ID.String()))

	for _, source := range []string{"unknown", "seeded", "denied", "withdrawn", "stateless"} {
		t.Run(source, func(t *testing.T) {
			result := client.call("mousa_query", mcpQuery(source, "harbor", 4096), "")
			trail := trailOf(result)
			sources, _ := trail["sources"].([]any)
			if len(sources) != 1 {
				t.Fatalf("trail sources: %v", trail)
			}
			decision, _ := sources[0].(map[string]any)
			expected, err := streamSource(source)
			if err != nil {
				t.Fatal(err)
			}
			if len(decision) != 2 || decision["source_id"] != expected.ID.String() || decision["outcome"] != "deny" {
				t.Fatalf("denied view discloses more than the scoped identity and outcome: %v", decision)
			}
			for _, key := range []string{"as_of", "retrieval", "packing", "candidates"} {
				if _, present := trail[key]; present {
					t.Fatalf("denied view disclosed %s: %v", key, trail)
				}
			}
			if len(hits(result)) != 0 {
				t.Fatalf("denied query released passages: %v", result)
			}
		})
	}

	result := client.call("mousa_query", mcpQuery("allowed", "harbor", 4096), "")
	trail := trailOf(result)
	decision, _ := trail["sources"].([]any)[0].(map[string]any)
	if decision["outcome"] != "allow" {
		t.Fatalf("allowed source denied: %v", decision)
	}
	head, _ := decision["head"].(map[string]any)
	if head["leaf_sha256"] == nil {
		t.Fatalf("allowed view lacks head.leaf_sha256: %v", decision)
	}
	if _, present := head["seq"]; present {
		t.Fatalf("caller view disclosed global head seq: %v", head)
	}
	retrieval, _ := trail["retrieval"].(map[string]any)
	for _, raw := range retrieval["branches"].([]any) {
		branch, _ := raw.(map[string]any)
		for _, key := range []string{"dropped_unknown", "dropped_not_allowed"} {
			if _, present := branch[key]; present {
				t.Fatalf("caller view disclosed %s: %v", key, branch)
			}
		}
	}
	if _, present := trail["as_of"]; present {
		t.Fatalf("caller view disclosed as_of: %v", trail)
	}
	client.close()
}

// A trail whose allowed decision names a source the caller cannot read now —
// denied, withdrawn, or never configured — is indistinguishable from a random
// trail ID, and the read commits nothing.
func TestMCPTrailUnauthorizedIsNotFound(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("doc", "harbor hidden evidence"))
	client := startMCPClient(t, run, "cli", false)
	query := client.call("mousa_query", mcpQuery("alpha", "harbor", 4096), "")
	trailID := trailIDOf(query)
	read := client.call("mousa_trail", map[string]any{"trail_id": trailID}, "")
	if !reflect.DeepEqual(read, trailOf(query)) {
		t.Fatalf("trail read differs from the query's caller view: %v", read)
	}
	run.run(false, "access", "--source", "alpha", "deny")
	// A trail granted by a source outside the startup set is not_found too.
	jsonlSync(t, run, "outsider", record("doc", "harbor outsider evidence"))
	foreign := querySource(t, run, "outsider", "harbor outsider")
	before := inspectionRows(t, run.store)
	denied := client.call("mousa_trail", map[string]any{"trail_id": trailID}, "not_found")
	random := client.call("mousa_trail", map[string]any{"trail_id": strings.Repeat("1", 64)}, "not_found")
	if !reflect.DeepEqual(denied, random) {
		t.Fatalf("denial distinguishes a withheld trail from an unknown one: %v vs %v", denied, random)
	}
	if foreignRead := client.call("mousa_trail", map[string]any{"trail_id": trailIDOf(foreign)}, "not_found"); !reflect.DeepEqual(foreignRead, denied) {
		t.Fatalf("ungranted trail differs from withheld: %v vs %v", foreignRead, denied)
	}
	if after := inspectionRows(t, run.store); !reflect.DeepEqual(before, after) {
		t.Fatal("trail reads committed rows")
	}
	client.close()
}

func mcpCanonicalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

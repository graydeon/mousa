package main

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// inspectionRows includes WAL-visible rows from every table. Only the response-bound
// decision and its input children may be excluded from the post-command comparison.
func inspectionRows(t *testing.T, path string, exclude []byte) map[string][]string {
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
		query := `SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`
		var args []any
		if exclude != nil && (name == "policy_decisions" || name == "policy_decision_inputs") {
			column := "id"
			if name == "policy_decision_inputs" {
				column = "decision_id"
			}
			query += " WHERE " + column + " != ?"
			args = append(args, exclude)
		}
		rows, err := db.Query(query, args...)
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

func requireInspectionAudit(t *testing.T, run testRun, before map[string][]string, result map[string]any, sourceLabel, outcome string) {
	t.Helper()
	id, err := hex.DecodeString(result["authorization_decision_id"].(string))
	if err != nil || len(id) != 32 {
		t.Fatalf("invalid authorization identity: %v", result)
	}
	if after := inspectionRows(t, run.store, id); !reflect.DeepEqual(before, after) {
		t.Fatal("inspection changed rows outside its fresh authorization decision and inputs")
	}
	db := openRawStore(t, run.store)
	var sourceID, record []byte
	var storedOutcome string
	if err := db.QueryRow(`SELECT source_id, outcome, record_json FROM policy_decisions WHERE id = ?`, id).Scan(&sourceID, &storedOutcome, &record); err != nil {
		t.Fatal(err)
	}
	source := declarationSource(t, sourceLabel)
	decision, err := mousa.DecodePolicyDecision(record)
	if err != nil || decision.ID.String() != result["authorization_decision_id"] || decision.Request.SourceID != source.ID ||
		hex.EncodeToString(sourceID) != source.ID.String() || storedOutcome != outcome || string(decision.Outcome) != outcome || result["authorization_outcome"] != outcome {
		t.Fatalf("authorization row is not bound to response/source/outcome: %s; %v", record, err)
	}
	if mcpCanonicalJSON(t, decision.ReasonCodes) != mcpCanonicalJSON(t, result["authorization_reasons"]) {
		t.Fatal("authorization reasons differ from committed decision")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM policy_decision_inputs WHERE decision_id = ?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(decision.PolicyInputs) {
		t.Fatalf("authorization input count = %d, want %d", count, len(decision.PolicyInputs))
	}
	for ordinal, input := range decision.PolicyInputs {
		var layer, result string
		var activation, binding, definition []byte
		if err := db.QueryRow(`SELECT layer, activation_id, binding_id, definition_id, result FROM policy_decision_inputs WHERE decision_id = ? AND ordinal = ?`, id, ordinal).Scan(&layer, &activation, &binding, &definition, &result); err != nil {
			t.Fatal(err)
		}
		if layer != string(input.Layer) || result != string(input.Result) || hex.EncodeToString(activation) != input.PolicyActivationID.String() ||
			hex.EncodeToString(binding) != input.PolicyBindingID.String() || hex.EncodeToString(definition) != input.PolicyDefinitionID.String() {
			t.Fatalf("input %d differs from the committed decision", ordinal)
		}
	}
	logMCPReceipt(t, map[string]any{"audit_decision": decision, "input_rows": count, "other_rows_unchanged": true})
}

func TestMCPSupersessionTrailDeniedAudit(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("doc", "harbor audit evidence"))
	client := startMCPClient(t, run, "cli", false)
	query := client.call("mousa_query", mcpOptInQuery("alpha", "harbor", 8192, mousa.PackingOriginal), "")
	// The access change is preparation, not part of any inspection bracket.
	run.run(false, "access", "--source", "alpha", "deny")
	for _, mode := range []any{nil, false, true} {
		args := map[string]any{"source": "alpha", "trail_id": query["trail_id"]}
		if mode != nil {
			args["supersession"] = mode
		}
		before := inspectionRows(t, run.store, nil)
		denied := client.call("mousa_trail", args, "")
		if _, present := denied["historical"]; present {
			t.Fatalf("denial released historical payload: %v", denied)
		}
		requireInspectionAudit(t, run, before, denied, "alpha", "deny")
		encoded, err := json.Marshal(denied)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("successful denied inspection commits its response-bound authorization: %s", encoded)
	}
	client.close()
}

func TestMCPSupersessionTrailNativeAssociationHistory(t *testing.T) {
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			run, root := setup(t)
			jsonlSync(t, run, "alpha", record("release", "Announce the release tag.")+"\n"+record("freeze", "Keep rollback open one day."))
			declarations := writeDeclarationFile(t, root, `{"schema":"mousa.association_declarations.v1","associations":[{"from_item":"release","to_item":"freeze","basis":"tag caveat","author":"example.operations"}]}`)
			_, query := run.run(false, "query", "--source", "alpha", "--associations", declarations, "--packing-policy", packing, "--budget-bytes", "8192", "announce")
			trailID := query["trail_id"].(string)
			id := requireTrailID(t, trailID)
			if schema := mcpRecordedTrailMember(t, run.store, id, "schema"); schema != mousa.SourceTrailSchemaV3 {
				t.Fatalf("native association query produced %v", schema)
			}
			associated := mcpRecordedTrailMember(t, run.store, id, "associated").([]any)
			if len(associated) != 1 {
				t.Fatalf("native fixture must record its declared caveat: %v", associated)
			}
			expected := make(map[string]any)
			for key, value := range associated[0].(map[string]any) {
				expected[key] = value
			}
			expected["indexed_now"] = true
			if expected["from_item"] != "release" || expected["to_item"] != "freeze" || expected["author"] != "example.operations" || expected["basis"] != "tag caveat" || expected["selected"] != true {
				t.Fatalf("wrong native association: %v", expected)
			}
			client := startMCPClient(t, run, "cli", false)
			var baseline map[string]any
			for _, mode := range []any{nil, false, true} {
				args := map[string]any{"source": "alpha", "trail_id": trailID}
				if mode != nil {
					args["supersession"] = mode
				}
				before := inspectionRows(t, run.store, nil)
				result := client.call("mousa_trail", args, "")
				requireInspectionAudit(t, run, before, result, "alpha", "allow")
				history := result["historical"].(map[string]any)
				if history["schema"] != mousa.SourceTrailSchemaV3 || history["packing_policy"] != packing || history["id"] != trailID {
					t.Fatalf("inspection lost native record identity: %v", history)
				}
				for _, key := range []string{"request_id", "decision_id", "packet_id"} {
					if history[key] != query[key] {
						t.Fatalf("%s differs from producing query: %v versus %v", key, history[key], query[key])
					}
				}
				if _, present := history["supersession"]; present {
					t.Fatal("inspection fabricated a v3 supersession member")
				}
				if mcpCanonicalJSON(t, history["associated"]) != mcpCanonicalJSON(t, []any{expected}) {
					t.Fatalf("association projection differs from native record: %v", history["associated"])
				}
				stable := mcpStableTrailValues(t, result)
				if baseline == nil {
					baseline = stable
				} else if !reflect.DeepEqual(baseline, stable) {
					t.Fatalf("inspection input changed v3 properties or values: %v versus %v", baseline, stable)
				}
			}
			client.close()
		})
	}
}

func TestMCPSupersessionTrailOrphanSourceMasking(t *testing.T) {
	for _, deny := range []bool{false, true} {
		name := "allow"
		if deny {
			name = "deny"
		}
		t.Run(name, func(t *testing.T) {
			run, _ := setup(t)
			for _, source := range []string{"alpha", "beta"} {
				jsonlSync(t, run, source, record("doc", "harbor orphan evidence"))
			}
			client := startMCPClient(t, run, "cli", false)
			var ids []string
			for _, source := range []string{"alpha", "beta"} {
				query := client.call("mousa_query", mcpOptInQuery(source, "harbor", 8192, mousa.PackingOriginal), "")
				ids = append(ids, query["trail_id"].(string))
			}
			if deny {
				// Access preparation must finish before damage makes a fresh open fail.
				run.run(false, "access", "--source", "alpha", "deny")
			}
			canonical, err := sqlite.Open(t.Context(), run.store)
			if err != nil {
				t.Fatal(err)
			}
			defer canonical.Close()
			// Only these fresh synthetic stores receive FK-disabled out-of-band damage.
			rawExec(t, run.store, `DELETE FROM policy_decisions WHERE id IN (SELECT decision_id FROM source_trails)`)
			ids = append(ids, strings.Repeat("a", 64))
			var refusal map[string]any
			for _, id := range ids {
				for _, mode := range []any{nil, false, true} {
					args := map[string]any{"source": "alpha", "trail_id": id}
					if mode != nil {
						args["supersession"] = mode
					}
					before := inspectionRows(t, run.store, nil)
					if deny {
						result := client.call("mousa_trail", args, "")
						if _, present := result["historical"]; present {
							t.Fatalf("denial disclosed damaged history: %v", result)
						}
						requireInspectionAudit(t, run, before, result, "alpha", "deny")
					} else {
						result := client.call("mousa_trail", args, "not_found")
						if refusal == nil {
							refusal = result
						} else if !reflect.DeepEqual(refusal, result) {
							t.Fatalf("own/foreign/unknown refusal differs: %v versus %v", refusal, result)
						}
						if after := inspectionRows(t, run.store, nil); !reflect.DeepEqual(before, after) {
							t.Fatal("scoped refusal committed its in-flight authorization or repaired damage")
						}
					}
				}
			}
			for _, id := range ids[:2] {
				before := inspectionRows(t, run.store, nil)
				if _, err := canonical.GetSourceTrail(t.Context(), requireTrailID(t, id)); !sqlite.IsCode(err, sqlite.CodeIntegrity) {
					t.Fatalf("unscoped canonical orphan read = %v, want integrity", err)
				}
				if after := inspectionRows(t, run.store, nil); !reflect.DeepEqual(before, after) {
					t.Fatal("canonical refusal changed damaged state")
				}
			}
			client.close()
			before := inspectionRows(t, run.store, nil)
			message, _ := run.run(true, "status", "--source", "alpha")
			if !strings.Contains(message, "integrity") {
				t.Fatalf("fresh-open verification did not reject damage as integrity: %s", message)
			}
			if after := inspectionRows(t, run.store, nil); !reflect.DeepEqual(before, after) {
				t.Fatal("fresh-open verification repaired damaged state")
			}
		})
	}
}

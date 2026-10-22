package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/graydeon/mousa/internal/mousa"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func requireMCPSchemaProperties(t *testing.T, schema *jsonschema.Schema, want []string) {
	t.Helper()
	got := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		got = append(got, name)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MCP schema properties = %v, want published set %v", got, want)
	}
	for _, name := range schema.PropertyOrder {
		if schema.Properties[name] == nil {
			t.Fatalf("schema property order retains removed property %s", name)
		}
	}
	if schema.AdditionalProperties == nil || schema.AdditionalProperties.Not == nil {
		t.Fatal("MCP schema lost its closed additionalProperties boundary")
	}
}

func mcpToolPropertySets(t *testing.T, tools []*mcp.Tool) map[string]map[string]any {
	t.Helper()
	sets := make(map[string]map[string]any, len(tools))
	for _, tool := range tools {
		properties, ok := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no input properties", tool.Name)
		}
		sets[tool.Name] = properties
	}
	return sets
}

// mcpOptInQuery requests the query opt-in under the given packing policy. The empty
// policy keeps the tool's default.
func mcpOptInQuery(source, text string, budget int, packing string) map[string]any {
	args := mcpQuery(source, text, budget)
	if packing != "" {
		args["packing_policy"] = packing
	}
	args["supersession"] = true
	return args
}

// mcpStableQueryValues returns one query result without exactly the per-call identity and
// timing fields two independent calls must not share, validating those raw values on the
// way. Every other property, including any new or unknown one, stays in the comparison.
func mcpStableQueryValues(t *testing.T, report map[string]any) map[string]any {
	t.Helper()
	stable := make(map[string]any, len(report))
	for key, value := range report {
		stable[key] = value
	}
	for _, key := range []string{"request_id", "decision_id", "trail_id"} {
		raw, ok := stable[key].(string)
		decoded, err := hex.DecodeString(raw)
		if !ok || err != nil || len(decoded) != sha256.Size {
			t.Fatalf("invalid per-call %s = %v in %v", key, stable[key], report)
		}
		delete(stable, key)
	}
	for _, key := range []string{"evaluated_at_usec", "latency_micros"} {
		raw, ok := stable[key].(float64)
		if !ok || raw < 0 {
			t.Fatalf("invalid per-call %s = %v in %v", key, stable[key], report)
		}
		delete(stable, key)
	}
	return stable
}

// mcpQueryValuesExcept drops the enumerated per-call fields plus the named properties a
// specific comparison expects to differ, leaving every other property compared.
func mcpQueryValuesExcept(t *testing.T, report map[string]any, skip []string) map[string]any {
	t.Helper()
	values := mcpStableQueryValues(t, report)
	for _, key := range skip {
		delete(values, key)
	}
	return values
}

func TestMCPSupersessionQueryDeclaresOptInSchema(t *testing.T) {
	query, err := mcpOutputSchema[evidenceResult]("mousa_query")
	if err != nil {
		t.Fatal(err)
	}
	result := query.Properties["result"]
	requireMCPSchemaProperties(t, result, []string{
		"query", "source", "query_policy", "expression", "request_id", "decision_id",
		"decision_outcome", "decision_reasons", "evaluated_at_usec", "trail_id", "packet_id",
		"outcome", "matched_candidates", "candidate_limit", "lifecycle_excluded", "budget_omitted",
		"duplicate_omitted", "supersession_excluded", "packing_policy", "used_bytes", "budget_bytes",
		"association_omissions", "evidence", "latency_micros",
	})
	if slices.Contains(result.Required, "supersession_excluded") {
		t.Fatal("the optional query count became a required result property")
	}
	// One schema convention covers both optional counts, so a client cannot be asked to
	// accept the same kind of field under two declarations.
	withheld, err := json.Marshal(result.Properties["supersession_excluded"])
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := json.Marshal(result.Properties["duplicate_omitted"])
	if err != nil {
		t.Fatal(err)
	}
	if string(withheld) != string(duplicate) {
		t.Fatalf("optional count declarations disagree: %s versus %s", withheld, duplicate)
	}
	trail, err := mcpOutputSchema[mcpTrailResult]("mousa_trail")
	if err != nil {
		t.Fatal(err)
	}
	historical := trail.Properties["result"].Properties["historical"]
	requireMCPSchemaProperties(t, historical, []string{
		"id", "request_id", "decision_id", "decision_outcome", "decision_reasons", "expression",
		"budget_bytes", "used_bytes", "packet_id", "lifecycle_excluded", "candidates", "schema",
		"packing_policy", "associated", "association_omissions", "supersession", "vector", "hybrid",
	})
	for _, schema := range []*jsonschema.Schema{query, trail} {
		encoded, err := json.Marshal(schema)
		if err != nil {
			t.Fatal(err)
		}
		var wire map[string]any
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		if wire["$defs"] != nil || wire["definitions"] != nil {
			t.Fatalf("native fields introduced MCP definitions: %s", encoded)
		}
		if _, err := schema.Resolve(nil); err != nil {
			t.Fatalf("MCP output schema is not resolvable: %v", err)
		}
	}
	tools, err := mcpToolDefinitions([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	properties := mcpToolPropertySets(t, tools)
	for name, want := range map[string][]string{
		"mousa_sync":   {"items", "segment_policy", "source"},
		"mousa_status": {"source"},
		"mousa_query":  {"budget_bytes", "packing_policy", "policy", "query", "source", "supersession"},
		"mousa_trail":  {"retrieval_details", "source", "supersession", "trail_id"},
	} {
		got := make([]string, 0, len(properties[name]))
		for property := range properties[name] {
			got = append(got, property)
		}
		slices.Sort(got)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s input properties = %v, want %v", name, got, want)
		}
	}
	declared, ok := properties["mousa_query"]["supersession"].(map[string]any)
	if !ok || declared["type"] != "boolean" || declared["description"] == nil {
		t.Fatalf("query opt-in input is not a described boolean: %v", properties["mousa_query"]["supersession"])
	}
	for _, tool := range tools {
		required, ok := tool.InputSchema.(map[string]any)["required"].([]string)
		if !ok {
			t.Fatalf("%s has no required list", tool.Name)
		}
		if slices.Contains(required, "supersession") {
			t.Fatalf("%s made the opt-in a required argument", tool.Name)
		}
	}
}

func TestMCPSupersessionQueryOptInInputBoundaries(t *testing.T) {
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "alpha")
	activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	client := startMCPClient(t, run, "cli", false)

	// The opt-in field is strictly typed: every other JSON spelling of "true" or a
	// boolean field name is an argument error, not a truthiness decision.
	for _, raw := range []string{
		`{"source":"alpha","query":"oldterm","policy":"dedup","budget_bytes":8192,"supersession":null}`,
		`{"source":"alpha","query":"oldterm","policy":"dedup","budget_bytes":8192,"supersession":"true"}`,
		`{"source":"alpha","query":"oldterm","policy":"dedup","budget_bytes":8192,"supersession":1}`,
		`{"source":"alpha","query":"oldterm","policy":"dedup","budget_bytes":8192,"supersession":0}`,
		`{"source":"alpha","query":"oldterm","policy":"dedup","budget_bytes":8192,"supersession":true,"supersession":false}`,
		`{"source":"alpha","query":"oldterm","policy":"dedup","budget_bytes":8192,"supersession":true,"root":"/tmp"}`,
	} {
		client.call("mousa_query", json.RawMessage(raw), "invalid_arguments")
	}
	// Trailing data after the arguments object is refused by the decoder. No valid
	// JSON-RPC frame can carry it, so it is asserted at that boundary directly.
	if _, err := decodeMCPArguments([]byte(`{"source":"alpha","query":"oldterm","policy":"dedup","budget_bytes":8192,"supersession":true} {}`), "mousa_query"); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing arguments were not rejected: %v", err)
	}
	// A rejected argument still must not touch the store, because the store is opened
	// once before the server accepts requests.
	decisions, decisionsDigest := tableDigest(t, run.store, "policy_decisions")
	trails, trailsDigest := tableDigest(t, run.store, "source_trails")
	client.call("mousa_query", json.RawMessage(`{"source":"alpha","query":"oldterm","policy":"dedup","budget_bytes":8192,"supersession":"true"}`), "invalid_arguments")
	client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": strings.Repeat("a", 64), "supersession": "true"}, "invalid_arguments")
	client.call("mousa_status", map[string]any{"source": "alpha", "supersession": true}, "invalid_arguments")
	if after, digest := tableDigest(t, run.store, "policy_decisions"); after != decisions || digest != decisionsDigest {
		t.Fatalf("a rejected argument wrote a decision: %d/%s", after, digest)
	}
	if after, digest := tableDigest(t, run.store, "source_trails"); after != trails || digest != trailsDigest {
		t.Fatalf("a rejected argument wrote a trail: %d/%s", after, digest)
	}
	client.close()

	// The server opens (and here creates) the canonical store before any call, so no
	// argument can select a different store, migration or writability.
	fresh := testRun{t: t, binary: run.binary, store: filepath.Join(t.TempDir(), "fresh.sqlite")}
	client = startMCPClient(t, fresh, "cli", false)
	if _, err := os.Stat(fresh.store); err != nil {
		t.Fatalf("MCP server did not open the store before accepting calls: %v", err)
	}
	client.close()
}

func TestMCPSupersessionQueryOptInPreservesDefaultPropertySet(t *testing.T) {
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "alpha")
	activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	client := startMCPClient(t, run, "cli", false)

	omitted := client.call("mousa_query", mcpQuery("alpha", "oldterm", 8192), "")
	if _, present := omitted["supersession_excluded"]; present {
		t.Fatalf("a default query reported the native count: %v", omitted)
	}
	explicit := mcpQuery("alpha", "oldterm", 8192)
	explicit["supersession"] = false
	falseResult := client.call("mousa_query", explicit, "")
	if _, present := falseResult["supersession_excluded"]; present {
		t.Fatalf("explicit false reported the native count: %v", falseResult)
	}
	if !reflect.DeepEqual(mcpStableQueryValues(t, omitted), mcpStableQueryValues(t, falseResult)) {
		t.Fatalf("explicit false differs from omission: %v versus %v", omitted, falseResult)
	}

	opted := client.call("mousa_query", mcpOptInQuery("alpha", "oldterm", 8192, ""), "")
	requireSupersessionCounts(t, opted, "supersession_excluded", 1, 1, 0, 0, 0)
	// The opt-in adds the declared count and the packing policy the v4 record carries,
	// which the legacy record does not report for the default policy. Withholding also
	// changes the outcome, the released evidence and the used bytes; every other
	// property, including any new one, is compared unchanged.
	for name := range omitted {
		if _, present := opted[name]; !present {
			t.Fatalf("the opt-in dropped the default property %s: %v", name, opted)
		}
	}
	for name := range opted {
		if _, present := omitted[name]; !present && name != "supersession_excluded" && name != "packing_policy" {
			t.Fatalf("the opt-in added an undeclared property %s: %v", name, opted)
		}
	}
	// A different released selection necessarily changes the packet identity, the
	// evidence, the used bytes and the outcome. Every other property must be identical.
	withholding := []string{"supersession_excluded", "packing_policy", "outcome", "evidence", "used_bytes", "packet_id"}
	if !reflect.DeepEqual(mcpQueryValuesExcept(t, omitted, withholding), mcpQueryValuesExcept(t, opted, withholding)) {
		t.Fatalf("the opt-in changed a property beyond the withheld selection: %v versus %v", omitted, opted)
	}
}

func TestMCPSupersessionQueryOptInFreesBudgetUnderBothPolicies(t *testing.T) {
	const predecessorText = "harbor harbor predecessor evidence"
	const successorText = "newterm harbor successor evidence"
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			run, _ := setup(t)
			jsonlSync(t, run, "alpha", strings.Join([]string{
				record("doc", predecessorText),
				record("corrected", successorText),
			}, "\n"))
			predecessorItemID, predecessorRepresentationID := revisionPin(t, run, "alpha", "predecessor")
			successorItemID, successorRepresentationID := revisionPin(t, run, "alpha", "successor")
			source := declarationSource(t, "alpha")
			declaration := storedDeclaration(t, run, declarationJSON(t, source, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, "example.operations", "successor replaces the predecessor"))
			active := activateQueryDeclaration(t, run, source, nil, &declaration, 1)
			client := startMCPClient(t, run, "cli", false)

			// The budget fits the higher-ranked predecessor alone, so the default query
			// omits the later candidate it would otherwise release.
			budget := len(predecessorText)
			baseline := client.call("mousa_query", mcpQuery("alpha", "harbor", budget), "")
			if firstItem(t, baseline) != "doc" || baseline["budget_omitted"] != float64(1) {
				t.Fatalf("default selection was not the expected budget boundary: %v", baseline)
			}

			opted := client.call("mousa_query", mcpOptInQuery("alpha", "harbor", budget, packing), "")
			requireSupersessionCounts(t, opted, "evidence", 2, 1, 0, 0, 1)
			if firstItem(t, opted) != "corrected" || opted["used_bytes"] != float64(len(successorText)) {
				t.Fatalf("withholding did not release budget for the later rank: %v", opted)
			}
			encoded, err := json.Marshal(opted)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), predecessorText) {
				t.Fatalf("withheld text appeared in the response: %s", encoded)
			}
			history := supersessionHistory(t, run, "alpha", opted)
			selection, ok := history["supersession"].(map[string]any)
			if !ok || history["schema"] != "mousa.source_trail.v4" || selection["consulted"] != true || selection["activation_id"] != active.String() || selection["declaration_id"] != declaration.String() {
				t.Fatalf("the recorded consultation is not the one the response reported: %v", history)
			}
			dispositions := selection["dispositions"].([]any)
			if len(dispositions) != 1 {
				t.Fatalf("wrong disposition count: %v", history)
			}
			withheld := dispositions[0].(map[string]any)["segment_id"]
			found := false
			for _, value := range history["candidates"].([]any) {
				row := value.(map[string]any)
				if row["segment_id"] != withheld {
					continue
				}
				found = true
				if row["selected"] != false || row["rank"] != float64(1) || row["text_bytes"] != float64(len(predecessorText)) {
					t.Fatalf("the recorded candidate lost its rank or canonical size: %v", row)
				}
			}
			if !found {
				t.Fatalf("the withheld candidate is not recorded: %v", history)
			}

			// The pinned successor revision going historical does not revoke the exact
			// predecessor pin, and no replacement passage is synthesized.
			jsonlSync(t, run, "alpha", record("corrected", "rewritten harbor successor evidence"))
			stale := client.call("mousa_query", mcpOptInQuery("alpha", "harbor", 8192, packing), "")
			requireSupersessionCounts(t, stale, "evidence", 2, 1, 0, 0, 1)
			if firstItem(t, stale) != "corrected" || hits(stale)[0].(map[string]any)["text"] != "rewritten harbor successor evidence" {
				t.Fatalf("an unmatched successor changed suppression or supplied text: %v", stale)
			}
			client.close()
		})
	}
}

func TestMCPSupersessionQueryOptInConsultedNoHistoryAndDeactivation(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "beta", record("beta-doc", "harbor inspection beta evidence"))
	client := startMCPClient(t, run, "cli", false)

	defaultQuery := mcpQuery("beta", "harbor", 8192)
	baseline := client.call("mousa_query", defaultQuery, "")
	consulted := client.call("mousa_query", mcpOptInQuery("beta", "harbor", 8192, ""), "")
	requireSupersessionCounts(t, consulted, "evidence", 1, 0, 0, 0, 1)
	if consulted["packet_id"] != baseline["packet_id"] || !reflect.DeepEqual(hits(baseline), hits(consulted)) {
		t.Fatalf("verified no history changed the packet: %v versus %v", baseline, consulted)
	}
	history := supersessionHistory(t, run, "beta", consulted)
	selection := history["supersession"].(map[string]any)
	if history["schema"] != "mousa.source_trail.v4" || selection["consulted"] != true || selection["activation_id"] != nil || selection["declaration_id"] != nil || len(selection["dispositions"].([]any)) != 0 {
		t.Fatalf("consulted no history was not recorded explicitly: %v", history)
	}
	client.close()

	source, declaration, _ := activationPins(t, run, "alpha")
	active := activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	deactivated := activateQueryDeclaration(t, run, source, &active, nil, 2)
	client = startMCPClient(t, run, "cli", false)
	restored := client.call("mousa_query", mcpOptInQuery("alpha", "oldterm", 8192, ""), "")
	requireSupersessionCounts(t, restored, "evidence", 1, 0, 0, 0, 1)
	deactivation := supersessionHistory(t, run, "alpha", restored)["supersession"].(map[string]any)
	if deactivation["activation_id"] != deactivated.String() || deactivation["declaration_id"] != nil {
		t.Fatalf("deactivation was not recorded as an explicit null selection: %v", deactivation)
	}
	client.close()
}

func TestMCPSupersessionQueryOptInDenialConsultsNothing(t *testing.T) {
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "alpha")
	activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	client := startMCPClient(t, run, "cli", false)

	run.run(false, "access", "--source", "alpha", "deny")
	denied := client.call("mousa_query", mcpQuery("alpha", "oldterm", 8192), "")
	opted := client.call("mousa_query", mcpOptInQuery("alpha", "oldterm", 8192, ""), "")
	requireSupersessionCounts(t, opted, "policy_excluded", 0, 0, 0, 0, 0)
	if opted["decision_outcome"] != denied["decision_outcome"] || !reflect.DeepEqual(opted["decision_reasons"], denied["decision_reasons"]) {
		t.Fatalf("the opt-in changed a denied decision: %v versus %v", denied, opted)
	}
	if !reflect.DeepEqual(mcpQueryValuesExcept(t, denied, []string{"supersession_excluded", "packing_policy"}), mcpQueryValuesExcept(t, opted, []string{"supersession_excluded", "packing_policy"})) {
		t.Fatalf("denial released something the count did not account for: %v versus %v", denied, opted)
	}
	run.run(false, "access", "--source", "alpha", "allow")
	history := supersessionHistory(t, run, "alpha", opted)
	selection := history["supersession"].(map[string]any)
	if history["schema"] != "mousa.source_trail.v4" || selection["consulted"] != false || selection["activation_id"] != nil || selection["declaration_id"] != nil || len(selection["dispositions"].([]any)) != 0 {
		t.Fatalf("a denied request consulted administration: %v", history)
	}
	client.close()
}

func TestMCPSupersessionQueryOptInCorruptAdministrationFailsClosed(t *testing.T) {
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "alpha")
	activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	client := startMCPClient(t, run, "cli", false)

	// The projection survives while the current event it names disappears. Startup
	// checks ran before this damage, so the call observes it inside its own transaction.
	decisions, decisionsDigest := tableDigest(t, run.store, "policy_decisions")
	trails, trailsDigest := tableDigest(t, run.store, "source_trails")
	declarations, declarationsDigest := declarationRows(t, run.store)
	rawExec(t, run.store, "DELETE FROM supersession_activations")
	damaged, damagedDigest := tableDigest(t, run.store, "supersession_activations")
	client.call("mousa_query", mcpOptInQuery("alpha", "oldterm", 8192, ""), "integrity")
	if after, digest := tableDigest(t, run.store, "policy_decisions"); after != decisions || digest != decisionsDigest {
		t.Fatalf("a failed opt-in committed a decision: %d/%s", after, digest)
	}
	if after, digest := tableDigest(t, run.store, "source_trails"); after != trails || digest != trailsDigest {
		t.Fatalf("a failed opt-in committed a trail: %d/%s", after, digest)
	}
	if after, digest := tableDigest(t, run.store, "supersession_activations"); after != damaged || digest != damagedDigest {
		t.Fatalf("a failed opt-in repaired activation history: %d/%s", after, digest)
	}
	if after, digest := declarationRows(t, run.store); after != declarations || digest != declarationsDigest {
		t.Fatalf("a failed opt-in changed declarations: %d/%s", after, digest)
	}
	// The default path does not consult administration, so it keeps working.
	defaultResult := client.call("mousa_query", mcpQuery("alpha", "oldterm", 8192), "")
	if firstItem(t, defaultResult) != "doc" {
		t.Fatalf("corrupt administration changed the default query: %v", defaultResult)
	}
	client.close()
}

func TestMCPSupersessionQueryOptInRestartKeepsRecordedMember(t *testing.T) {
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "alpha")
	active := activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	client := startMCPClient(t, run, "cli", false)

	first := client.call("mousa_query", mcpOptInQuery("alpha", "oldterm", 8192, ""), "")
	requireSupersessionCounts(t, first, "supersession_excluded", 1, 1, 0, 0, 0)
	trailID := first["trail_id"].(string)
	recorded := supersessionHistory(t, run, "alpha", first)
	client.close()

	client = startMCPClient(t, run, "cli", false)
	second := client.call("mousa_query", mcpOptInQuery("alpha", "oldterm", 8192, ""), "")
	requireSupersessionCounts(t, second, "supersession_excluded", 1, 1, 0, 0, 0)
	if first["packet_id"] != second["packet_id"] {
		t.Fatalf("the same selection produced two packets: %v versus %v", first, second)
	}
	if first["request_id"] == second["request_id"] || first["decision_id"] == second["decision_id"] || first["trail_id"] == second["trail_id"] {
		t.Fatalf("a fresh call reused an identity: %v versus %v", first, second)
	}
	if after := supersessionHistory(t, run, "alpha", first); !reflect.DeepEqual(recorded, after) {
		t.Fatalf("restart reinterpreted the recorded member: %v versus %v", recorded, after)
	}
	if recorded["supersession"].(map[string]any)["activation_id"] != active.String() {
		t.Fatalf("the recorded member lost its activation: %v", recorded)
	}
	_, firstRead := run.run(false, "trail", "--source", "alpha", trailID)
	_, secondRead := run.run(false, "trail", "--source", "alpha", trailID)
	if firstRead["authorization_decision_id"] == secondRead["authorization_decision_id"] {
		t.Fatal("inspection reused an authorization identity")
	}
	if !reflect.DeepEqual(firstRead["historical"], secondRead["historical"]) {
		t.Fatalf("inspection changed the recorded history: %v versus %v", firstRead, secondRead)
	}
	client.close()
}

func TestMCPSupersessionQueryOptInKeepsFilteredV4History(t *testing.T) {
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "alpha")
	activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	_, native := supersessionQuery(t, run, "alpha", mousa.PackingOriginal, "oldterm", 8192, true)
	requireSupersessionCounts(t, native, "supersession_excluded", 1, 1, 0, 0, 0)
	client := startMCPClient(t, run, "cli", false)
	query := client.call("mousa_query", mcpQuery("alpha", "oldterm", 8192), "")
	if _, present := query["supersession_excluded"]; present || len(hits(query)) != 1 {
		t.Fatalf("MCP default query changed under active administration: %v", query)
	}
	// Ordinary inspection keeps filtering the native-only recorded member; the opted-in trail
	// inspection and its own boundaries are covered by the regressions below.
	trail := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": native["trail_id"]}, "")
	history := trail["historical"].(map[string]any)
	if _, present := history["supersession"]; present || history["schema"] != "mousa.source_trail.v4" || history["used_bytes"] != float64(0) || history["candidates"].([]any)[0].(map[string]any)["selected"] != false {
		t.Fatalf("MCP changed its filtered history shape or reinterpreted v4 selection: %v", trail)
	}
	if nativeHistory := supersessionHistory(t, run, "alpha", native); nativeHistory["supersession"] == nil {
		t.Fatalf("MCP projection removed native recorded consultation: %v", nativeHistory)
	}
	client.close()
}

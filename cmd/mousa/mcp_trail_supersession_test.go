package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

// mcpStableTrailValues returns one inspection without exactly the fresh per-call authorization
// decision identity two independent inspection calls must not share, validating that raw value on
// the way. Every other property, including the recorded historical identities, stays compared.
func mcpStableTrailValues(t *testing.T, inspection map[string]any) map[string]any {
	t.Helper()
	stable := make(map[string]any, len(inspection))
	for name, value := range inspection {
		stable[name] = value
	}
	raw, ok := stable["authorization_decision_id"].(string)
	decoded, err := hex.DecodeString(raw)
	if !ok || err != nil || len(decoded) != sha256.Size {
		t.Fatalf("invalid per-call authorization_decision_id = %v in %v", stable["authorization_decision_id"], inspection)
	}
	delete(stable, "authorization_decision_id")
	return stable
}

// requireMCPPropertyDelta asserts that "after" carries exactly the properties of "before" plus the
// named additions, so no other property can appear or disappear unnoticed.
func requireMCPPropertyDelta(t *testing.T, before, after map[string]any, added []string) {
	t.Helper()
	for name := range after {
		if _, present := before[name]; !present && !slices.Contains(added, name) {
			t.Fatalf("unexpected property %s: %v", name, after)
		}
	}
	for name := range before {
		if _, present := after[name]; !present {
			t.Fatalf("lost property %s: %v", name, after)
		}
	}
}

// mcpInspectTrail inspects one trail with an explicit opt-in choice.
func mcpInspectTrail(client *mcpTestClient, source, trailID string, supersession bool) map[string]any {
	return client.call("mousa_trail", map[string]any{"source": source, "trail_id": trailID, "supersession": supersession}, "")
}

func requireTrailID(t *testing.T, value string) mousa.SourceTrailID {
	t.Helper()
	id, err := mousa.ParseSourceTrailID(value)
	if err != nil || id == (mousa.SourceTrailID{}) {
		t.Fatalf("inspection returned an invalid trail id %q", value)
	}
	return id
}

// mcpRecordedTrailMember reads one property straight from the stored canonical trail record, so an
// expected value never depends on a reader, a projection or a second hash of the same bytes.
func mcpRecordedTrailMember(t *testing.T, path string, trail mousa.SourceTrailID, name string) any {
	t.Helper()
	db := openRawStore(t, path)
	var data []byte
	if err := db.QueryRow(`SELECT record_json FROM source_trails WHERE id = ?`, trail[:]).Scan(&data); err != nil {
		t.Fatalf("read stored trail %s: %v", trail.String(), err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("stored trail record is unreadable: %v", err)
	}
	member, present := record[name]
	if !present {
		t.Fatalf("stored trail record carries no %s member: %s", name, data)
	}
	return member
}

func mcpCanonicalJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// mcpCanonicalInspectionState fingerprints exactly the tables one inspection command may read and
// the tables a permitted authorization write may extend, so a bracket over one command separates an
// allowed decision insert from a forbidden canonical mutation.
func mcpCanonicalInspectionState(t *testing.T, path string) map[string]string {
	t.Helper()
	state := make(map[string]string, 6)
	for _, table := range []string{
		"source_trails", "source_trail_candidates", "supersession_declarations",
		"supersession_activations", "policy_decisions",
	} {
		count, digest := tableDigest(t, path, table)
		state[table] = fmt.Sprintf("%d/%s", count, digest)
	}
	return state
}

// mcpWithholdingTrailFixture prepares a JSONL source with a predecessor and a successor revision,
// stores one declaration through the canonical codec and activates it, and returns the pins a
// recorded disposition has to name.
type mcpWithholdingTrailFixture struct {
	source                      mousa.Source
	declaration                 mousa.SupersessionDeclarationID
	activation                  mousa.SupersessionActivationID
	predecessorItemID           string
	predecessorRepresentationID mousa.RepresentationID
	successorItemID             string
	successorRepresentationID   mousa.RepresentationID
}

func newMCPWithholdingTrailFixture(t *testing.T, run testRun, externalSourceID, predecessorText, successorText string) mcpWithholdingTrailFixture {
	t.Helper()
	jsonlSync(t, run, externalSourceID, strings.Join([]string{
		record("doc", predecessorText),
		record("corrected", successorText),
	}, "\n"))
	predecessorItemID, predecessorRepresentationID := revisionPin(t, run, externalSourceID, "predecessor")
	successorItemID, successorRepresentationID := revisionPin(t, run, externalSourceID, "successor")
	source := declarationSource(t, externalSourceID)
	declaration := storedDeclaration(t, run, declarationJSON(t, source,
		predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID,
		"example.operations", "successor replaces the predecessor"))
	return mcpWithholdingTrailFixture{
		source: source, declaration: declaration,
		activation:                  activateQueryDeclaration(t, run, source, nil, &declaration, 1),
		predecessorItemID:           predecessorItemID,
		predecessorRepresentationID: predecessorRepresentationID,
		successorItemID:             successorItemID,
		successorRepresentationID:   successorRepresentationID,
	}
}

func TestMCPSupersessionTrailDeclaresOptInSchema(t *testing.T) {
	trail, err := mcpOutputSchema[mcpTrailResult]("mousa_trail")
	if err != nil {
		t.Fatal(err)
	}
	historical := trail.Properties["result"].Properties["historical"]
	member := historical.Properties["supersession"]
	if member == nil {
		t.Fatal("the trail schema does not declare the recorded consultation member")
	}
	if slices.Contains(historical.Required, "supersession") {
		t.Fatal("the inspection opt-in became a required historical property")
	}
	if member.Type != "" || !reflect.DeepEqual(member.Types, []string{"null", "object"}) {
		t.Fatalf("the recorded member is not a nullable object: %+v", member)
	}
	requireMCPSchemaProperties(t, member, []string{"consulted", "activation_id", "declaration_id", "dispositions"})
	if consulted := member.Properties["consulted"]; consulted == nil || consulted.Type != "boolean" {
		t.Fatalf("the recorded consultation flag is not a boolean: %+v", consulted)
	}
	// A recorded identity is a nullable lowercase hex string, exactly as the payload emits it.
	// Without this mapping the derived schema would describe a byte array.
	for _, name := range []string{"activation_id", "declaration_id"} {
		property := member.Properties[name]
		if property == nil || property.Type != "" || !reflect.DeepEqual(property.Types, []string{"null", "string"}) || property.Pattern != "^[0-9a-f]{64}$" {
			t.Fatalf("recorded %s is not a nullable lowercase hex identity: %+v", name, property)
		}
	}
	dispositions := member.Properties["dispositions"]
	// A Go slice derives a nullable array declaration, exactly as the sibling optional
	// collections in the accepted schemas do.
	if dispositions == nil || !reflect.DeepEqual(dispositions.Types, []string{"null", "array"}) || dispositions.Items == nil {
		t.Fatalf("the recorded dispositions are not a declared array: %+v", dispositions)
	}
	row := dispositions.Items
	requireMCPSchemaProperties(t, row, []string{
		"selection", "segment_id", "content_sha256", "declaration_id",
		"predecessor_item_id", "predecessor_representation_id",
		"successor_item_id", "successor_representation_id",
	})
	if selection := row.Properties["selection"]; selection == nil || selection.Type != "string" {
		t.Fatalf("the recorded selection value is not a string: %+v", selection)
	}
	for _, name := range []string{"segment_id", "content_sha256", "declaration_id", "predecessor_representation_id", "successor_representation_id"} {
		property := row.Properties[name]
		if property == nil || property.Type != "string" || property.Pattern != "^[0-9a-f]{64}$" {
			t.Fatalf("disposition %s is not a lowercase hex identity: %+v", name, property)
		}
	}
	// The declared member still validates the payload: no definition indirection or unresolved
	// reference may be introduced by the newly mapped identity types.
	encoded, err := json.Marshal(trail)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["$defs"] != nil || wire["definitions"] != nil {
		t.Fatalf("recorded identities introduced MCP definitions: %s", encoded)
	}
	if _, err := trail.Resolve(nil); err != nil {
		t.Fatalf("MCP trail output schema is not resolvable: %v", err)
	}

	tools, err := mcpToolDefinitions([]string{"alpha"})
	if err != nil {
		t.Fatal(err)
	}
	properties := mcpToolPropertySets(t, tools)
	declared, ok := properties["mousa_trail"]["supersession"].(map[string]any)
	if !ok || declared["type"] != "boolean" || declared["description"] == nil {
		t.Fatalf("trail opt-in input is not a described boolean: %v", properties["mousa_trail"]["supersession"])
	}
	for _, tool := range tools {
		required, ok := tool.InputSchema.(map[string]any)["required"].([]string)
		if !ok {
			t.Fatalf("%s has no required list", tool.Name)
		}
		if tool.Name == "mousa_trail" && slices.Contains(required, "supersession") {
			t.Fatal("the inspection opt-in became a required argument")
		}
	}
	// The inspection slice extends only the trail schema. The accepted query-only build's other
	// three output schemas stay byte-identical, so a shared declaration cannot silently change a
	// client contract this slice did not review.
	accepted := map[string]string{
		"mousa_sync":   "0c82ccb94556c70f41de8876e73842382978b4d94ad34f378361abbeb786bba3",
		"mousa_status": "b16e66e2a799e99fb0e2fb9ea969a407e68958907e1ddb055323e92550566589",
		"mousa_query":  "aeec1eb393f62f24aa0211e09f8aeae108f6ba956240d10dfed3a45a9310a2b6",
	}
	for _, tool := range tools {
		want, pinned := accepted[tool.Name]
		if !pinned {
			continue
		}
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != want {
			t.Fatalf("%s output schema = %s, want the accepted %s", tool.Name, got, want)
		}
	}
}

func TestMCPSupersessionTrailOptInPreservesDefaultPropertySet(t *testing.T) {
	const predecessorText = "harbor harbor predecessor evidence"
	const successorText = "newterm harbor successor evidence"
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			run, _ := setup(t)
			fixture := newMCPWithholdingTrailFixture(t, run, "alpha", predecessorText, successorText)
			client := startMCPClient(t, run, "cli", false)
			query := client.call("mousa_query", mcpOptInQuery("alpha", "harbor", 8192, packing), "")
			requireSupersessionCounts(t, query, "evidence", 2, 1, 0, 0, 1)
			trailID := query["trail_id"].(string)

			omitted := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": trailID}, "")
			explicit := mcpInspectTrail(client, "alpha", trailID, false)
			opted := mcpInspectTrail(client, "alpha", trailID, true)

			// Omission and explicit false are the same request: the only field two independent
			// inspections must not share is the fresh authorization decision identity.
			if !reflect.DeepEqual(mcpStableTrailValues(t, omitted), mcpStableTrailValues(t, explicit)) {
				t.Fatalf("explicit false differs from omission: %v versus %v", omitted, explicit)
			}
			if omitted["authorization_decision_id"] == explicit["authorization_decision_id"] {
				t.Fatal("two inspections reused an authorization decision identity")
			}
			for _, inspection := range []map[string]any{omitted, explicit} {
				if _, present := inspection["historical"].(map[string]any)["supersession"]; present {
					t.Fatalf("a default inspection released the native-only member: %v", inspection)
				}
			}
			// The opt-in adds exactly the declared member and nothing else, under both policies.
			requireMCPPropertyDelta(t, omitted, opted, nil)
			omittedHistorical := omitted["historical"].(map[string]any)
			optedHistorical := opted["historical"].(map[string]any)
			requireMCPPropertyDelta(t, omittedHistorical, optedHistorical, []string{"supersession"})
			selection := optedHistorical["supersession"].(map[string]any)

			// The returned member is the producing record's own member, compared as canonical JSON.
			recorded := mcpRecordedTrailMember(t, run.store, requireTrailID(t, trailID), "supersession")
			if mcpCanonicalJSON(t, selection) != mcpCanonicalJSON(t, recorded) {
				t.Fatalf("returned member %v is not the recorded %v", selection, recorded)
			}
			if opted["authorization_outcome"] != "allow" {
				t.Fatalf("an allowed inspection reported %v", opted["authorization_outcome"])
			}
			// Recorded identities belong to the query that wrote the record, not to this
			// inspection's fresh authorization decision.
			for name, want := range map[string]any{
				"id": trailID, "request_id": query["request_id"],
				"decision_id": query["decision_id"], "packet_id": query["packet_id"],
			} {
				if optedHistorical[name] != want {
					t.Fatalf("historical %s = %v, want the recorded %v", name, optedHistorical[name], want)
				}
			}
			if opted["authorization_decision_id"] == optedHistorical["decision_id"] {
				t.Fatal("inspection reported its own authorization decision as the recorded one")
			}

			// The recorded consultation and the recorded selection describe one decision: the
			// withheld candidate keeps its accepted disposition, rank and canonical size.
			if selection["consulted"] != true || selection["activation_id"] != fixture.activation.String() || selection["declaration_id"] != fixture.declaration.String() {
				t.Fatalf("recorded consultation is not the activated declaration: %v", selection)
			}
			dispositions := selection["dispositions"].([]any)
			if len(dispositions) != 1 {
				t.Fatalf("wrong recorded disposition count: %v", selection)
			}
			disposition := dispositions[0].(map[string]any)
			for name, want := range map[string]any{
				"selection":                     "superseded",
				"declaration_id":                fixture.declaration.String(),
				"predecessor_item_id":           fixture.predecessorItemID,
				"predecessor_representation_id": fixture.predecessorRepresentationID.String(),
				"successor_item_id":             fixture.successorItemID,
				"successor_representation_id":   fixture.successorRepresentationID.String(),
			} {
				if disposition[name] != want {
					t.Fatalf("recorded disposition %s = %v, want %v", name, disposition[name], want)
				}
			}
			withheld := false
			for _, candidate := range optedHistorical["candidates"].([]any) {
				row := candidate.(map[string]any)
				if row["segment_id"] != disposition["segment_id"] {
					continue
				}
				withheld = true
				if row["content_sha256"] != disposition["content_sha256"] || row["selected"] != false ||
					row["rank"] != float64(1) || row["text_bytes"] != float64(len(predecessorText)) {
					t.Fatalf("withheld recorded candidate lost its disposition, rank or canonical size: %v", row)
				}
			}
			if !withheld {
				t.Fatalf("the withheld candidate is not recorded beside the member: %v", optedHistorical)
			}
			client.close()
		})
	}
}

// requireMCPRecordedMember inspects one trail with the opt-in and returns the member after comparing
// it with the producing record's own bytes.
func requireMCPRecordedMember(t *testing.T, run testRun, client *mcpTestClient, source, trailID string) map[string]any {
	t.Helper()
	recorded := mcpRecordedTrailMember(t, run.store, requireTrailID(t, trailID), "supersession")
	inspection := mcpInspectTrail(client, source, trailID, true)
	if inspection["authorization_outcome"] != "allow" {
		t.Fatalf("inspection was not allowed: %v", inspection)
	}
	historical := inspection["historical"].(map[string]any)
	if mcpCanonicalJSON(t, historical["supersession"]) != mcpCanonicalJSON(t, recorded) {
		t.Fatalf("returned member %v is not the recorded %v", historical["supersession"], recorded)
	}
	return historical["supersession"].(map[string]any)
}

func TestMCPSupersessionTrailOptInRecordedMembers(t *testing.T) {
	const predecessorText = "harbor harbor predecessor evidence"
	const successorText = "newterm harbor successor evidence"

	t.Run("active declaration", func(t *testing.T) {
		run, _ := setup(t)
		fixture := newMCPWithholdingTrailFixture(t, run, "alpha", predecessorText, successorText)
		client := startMCPClient(t, run, "cli", false)
		query := client.call("mousa_query", mcpOptInQuery("alpha", "harbor", 8192, mousa.PackingOriginal), "")
		requireSupersessionCounts(t, query, "evidence", 2, 1, 0, 0, 1)
		member := requireMCPRecordedMember(t, run, client, "alpha", query["trail_id"].(string))
		if member["consulted"] != true || member["activation_id"] != fixture.activation.String() || member["declaration_id"] != fixture.declaration.String() {
			t.Fatalf("active declaration was not recorded: %v", member)
		}
		if len(member["dispositions"].([]any)) != 1 {
			t.Fatalf("active declaration recorded the wrong selection: %v", member)
		}
		client.close()
	})

	t.Run("verified no history", func(t *testing.T) {
		run, _ := setup(t)
		jsonlSync(t, run, "beta", record("beta-doc", "harbor inspection beta evidence"))
		client := startMCPClient(t, run, "cli", false)
		query := client.call("mousa_query", mcpOptInQuery("beta", "harbor", 8192, mousa.PackingOriginal), "")
		requireSupersessionCounts(t, query, "evidence", 1, 0, 0, 0, 1)
		member := requireMCPRecordedMember(t, run, client, "beta", query["trail_id"].(string))
		if member["consulted"] != true || member["activation_id"] != nil || member["declaration_id"] != nil {
			t.Fatalf("verified absence of history was not recorded explicitly: %v", member)
		}
		if len(member["dispositions"].([]any)) != 0 {
			t.Fatalf("a source without history recorded a disposition: %v", member)
		}
		client.close()
	})

	t.Run("deactivation", func(t *testing.T) {
		run, _ := setup(t)
		source, declaration, _ := activationPins(t, run, "alpha")
		active := activateQueryDeclaration(t, run, source, nil, &declaration, 1)
		deactivated := activateQueryDeclaration(t, run, source, &active, nil, 2)
		client := startMCPClient(t, run, "cli", false)
		query := client.call("mousa_query", mcpOptInQuery("alpha", "oldterm", 8192, mousa.PackingOriginal), "")
		requireSupersessionCounts(t, query, "evidence", 1, 0, 0, 0, 1)
		member := requireMCPRecordedMember(t, run, client, "alpha", query["trail_id"].(string))
		if member["consulted"] != true || member["activation_id"] != deactivated.String() || member["declaration_id"] != nil {
			t.Fatalf("deactivation was not recorded as an explicit null selection: %v", member)
		}
		if len(member["dispositions"].([]any)) != 0 {
			t.Fatalf("deactivation recorded a disposition: %v", member)
		}
		client.close()
	})

	t.Run("unconsulted denial", func(t *testing.T) {
		run, _ := setup(t)
		source, declaration, _ := activationPins(t, run, "alpha")
		activateQueryDeclaration(t, run, source, nil, &declaration, 1)
		client := startMCPClient(t, run, "cli", false)
		run.run(false, "access", "--source", "alpha", "deny")
		query := client.call("mousa_query", mcpOptInQuery("alpha", "oldterm", 8192, mousa.PackingOriginal), "")
		requireSupersessionCounts(t, query, "policy_excluded", 0, 0, 0, 0, 0)
		run.run(false, "access", "--source", "alpha", "allow")
		member := requireMCPRecordedMember(t, run, client, "alpha", query["trail_id"].(string))
		if member["consulted"] != false || member["activation_id"] != nil || member["declaration_id"] != nil {
			t.Fatalf("a denied request recorded a consultation: %v", member)
		}
		if len(member["dispositions"].([]any)) != 0 {
			t.Fatalf("a denied request recorded a disposition: %v", member)
		}
		client.close()
	})
}

func TestMCPSupersessionTrailOptInLegacyVersions(t *testing.T) {
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			run, _ := setup(t)
			jsonlSync(t, run, "alpha", record("doc", "harbor legacy evidence"))
			client := startMCPClient(t, run, "cli", false)
			args := mcpQuery("alpha", "harbor", 8192)
			args["packing_policy"] = packing
			query := client.call("mousa_query", args, "")
			trailID := query["trail_id"].(string)

			omitted := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": trailID}, "")
			opted := mcpInspectTrail(client, "alpha", trailID, true)
			historical := omitted["historical"].(map[string]any)
			// A default JSONL query writes the legacy record: it carries no schema property for the
			// original policy and v2 for exact-v1. Neither version has a consultation member, so the
			// opt-in adds none and fabricates no null object.
			var wantSchema any
			if packing == mousa.PackingExactV1 {
				wantSchema = mousa.SourceTrailSchemaV2
			}
			if historical["schema"] != wantSchema {
				t.Fatalf("legacy fixture recorded schema %v, want %v", historical["schema"], wantSchema)
			}
			requireMCPPropertyDelta(t, omitted, opted, nil)
			if _, present := opted["historical"].(map[string]any)["supersession"]; present {
				t.Fatalf("a legacy record gained a consultation member: %v", opted)
			}
			if !reflect.DeepEqual(mcpStableTrailValues(t, omitted), mcpStableTrailValues(t, opted)) {
				t.Fatalf("the opt-in changed a legacy inspection: %v versus %v", omitted, opted)
			}
			client.close()
		})
	}
}

func TestMCPSupersessionTrailOptInInputBoundaries(t *testing.T) {
	run, _ := setup(t)
	source, declaration, _ := activationPins(t, run, "alpha")
	activateQueryDeclaration(t, run, source, nil, &declaration, 1)
	client := startMCPClient(t, run, "cli", false)
	_, native := supersessionQuery(t, run, "alpha", mousa.PackingOriginal, "oldterm", 8192, true)
	trailID := native["trail_id"].(string)

	// The opt-in field is strictly typed: every other JSON spelling of a boolean flag is an
	// argument error, and each rejected call is bracketed on its own, so the comparison shows that
	// one rejected inspection neither wrote an audit row nor mutated a canonical record.
	for _, raw := range []string{
		`{"source":"alpha","trail_id":"` + trailID + `","supersession":null}`,
		`{"source":"alpha","trail_id":"` + trailID + `","supersession":"true"}`,
		`{"source":"alpha","trail_id":"` + trailID + `","supersession":1}`,
		`{"source":"alpha","trail_id":"` + trailID + `","supersession":0}`,
		`{"source":"alpha","trail_id":"` + trailID + `","supersession":true,"supersession":false}`,
		`{"source":"alpha","trail_id":"` + trailID + `","supersession":true,"root":"/tmp"}`,
	} {
		before := mcpCanonicalInspectionState(t, run.store)
		client.call("mousa_trail", json.RawMessage(raw), "invalid_arguments")
		if after := mcpCanonicalInspectionState(t, run.store); !reflect.DeepEqual(before, after) {
			t.Fatalf("a rejected inspection wrote store state: %v versus %v", before, after)
		}
	}
	// Trailing data and a non-object argument are refused at the decoder boundary. No valid
	// JSON-RPC frame can carry the first, so it is asserted directly.
	if _, err := decodeMCPArguments([]byte(`{"source":"alpha","trail_id":"`+trailID+`","supersession":true} {}`), "mousa_trail"); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing arguments were not rejected: %v", err)
	}
	if _, err := decodeMCPArguments([]byte(`["alpha"]`), "mousa_trail"); err == nil || !strings.Contains(err.Error(), "object") {
		t.Fatalf("a non-object argument was not rejected: %v", err)
	}
	// The other three tools keep their accepted field lists: sync and status reject the field even
	// though this server can ingest.
	writable := startMCPClient(t, run, "cli", true)
	writable.call("mousa_sync", map[string]any{
		"source": "alpha", "items": []any{map[string]any{"id": "probe", "text": "probe"}}, "supersession": true,
	}, "invalid_arguments")
	writable.call("mousa_status", map[string]any{"source": "alpha", "supersession": true}, "invalid_arguments")
	writable.call("mousa_query", map[string]any{
		"source": "alpha", "query": "oldterm", "policy": "dedup", "budget_bytes": 8192, "supersession": "true",
	}, "invalid_arguments")
	writable.close()
	client.close()
}

func TestMCPSupersessionTrailOptInAuthorizationBoundaries(t *testing.T) {
	const predecessorText = "harbor harbor predecessor evidence"
	const successorText = "newterm harbor successor evidence"
	run, _ := setup(t)
	newMCPWithholdingTrailFixture(t, run, "alpha", predecessorText, successorText)
	jsonlSync(t, run, "beta", record("beta-doc", "harbor inspection beta evidence"))
	client := startMCPClient(t, run, "cli", false)
	query := client.call("mousa_query", mcpOptInQuery("alpha", "harbor", 8192, mousa.PackingOriginal), "")
	trailID := query["trail_id"].(string)
	member := mcpRecordedTrailMember(t, run.store, requireTrailID(t, trailID), "supersession")

	// One allowed inspection writes exactly its own authorization decision and mutates no
	// canonical record: the bracket covers the trail and its ordered candidate rows, the
	// declaration and activation records, and the decisions table it legitimately extends.
	beforeAllowed := mcpCanonicalInspectionState(t, run.store)
	allowed := mcpInspectTrail(client, "alpha", trailID, true)
	afterAllowed := mcpCanonicalInspectionState(t, run.store)
	if allowed["authorization_outcome"] != "allow" {
		t.Fatalf("the bracketed inspection was not allowed: %v", allowed)
	}
	if beforeAllowed["policy_decisions"] == afterAllowed["policy_decisions"] {
		t.Fatalf("an allowed inspection committed no authorization decision: %v", afterAllowed)
	}
	for _, table := range []string{"source_trails", "source_trail_candidates", "supersession_declarations", "supersession_activations"} {
		if beforeAllowed[table] != afterAllowed[table] {
			t.Fatalf("an allowed inspection mutated %s: %v versus %v", table, beforeAllowed[table], afterAllowed[table])
		}
	}

	// An unknown trail and a trail of another configured source keep the existing not_found
	// boundary, and a refused read commits no audit row.
	unknown := "0" + trailID[1:]
	if unknown == trailID {
		unknown = "1" + trailID[1:]
	}
	for _, args := range []map[string]any{
		{"source": "alpha", "trail_id": unknown, "supersession": true},
		{"source": "beta", "trail_id": trailID, "supersession": true},
	} {
		before := mcpCanonicalInspectionState(t, run.store)
		client.call("mousa_trail", args, "not_found")
		if after := mcpCanonicalInspectionState(t, run.store); !reflect.DeepEqual(before, after) {
			t.Fatalf("a refused inspection wrote store state: %v versus %v", before, after)
		}
	}

	// A declined authorization is a successful result with the current denial reasons and no
	// historical payload: it neither becomes a tool error nor discloses the trail.
	run.run(false, "access", "--source", "alpha", "deny")
	denied := mcpInspectTrail(client, "alpha", trailID, true)
	deniedQuery := client.call("mousa_query", mcpQuery("alpha", "harbor", 8192), "")
	if denied["authorization_outcome"] != "deny" || denied["historical"] != nil {
		t.Fatalf("declined inspection released history: %v", denied)
	}
	if !reflect.DeepEqual(denied["authorization_reasons"], deniedQuery["decision_reasons"]) {
		t.Fatalf("declined inspection reported other reasons than the current decision: %v versus %v", denied, deniedQuery)
	}
	if mcpCanonicalJSON(t, mcpRecordedTrailMember(t, run.store, requireTrailID(t, trailID), "supersession")) != mcpCanonicalJSON(t, member) {
		t.Fatal("a declined inspection changed the recorded member")
	}
	run.run(false, "access", "--source", "alpha", "allow")
	restored := mcpInspectTrail(client, "alpha", trailID, true)
	if restored["authorization_outcome"] != "allow" {
		t.Fatalf("a re-allowed inspection was not allowed: %v", restored)
	}
	if mcpCanonicalJSON(t, restored["historical"].(map[string]any)["supersession"]) != mcpCanonicalJSON(t, member) {
		t.Fatalf("a fresh authorization did not release the recorded member: %v", restored)
	}
	client.close()
}

func TestMCPSupersessionTrailOptInRestartAndActivationChange(t *testing.T) {
	run, _ := setup(t)
	source, first, second := activationPins(t, run, "alpha")
	active := activateQueryDeclaration(t, run, source, nil, &first, 1)
	client := startMCPClient(t, run, "cli", false)
	query := client.call("mousa_query", mcpOptInQuery("alpha", "oldterm", 8192, mousa.PackingOriginal), "")
	requireSupersessionCounts(t, query, "supersession_excluded", 1, 1, 0, 0, 0)
	trailID := query["trail_id"].(string)
	before := mcpInspectTrail(client, "alpha", trailID, true)
	client.close()

	// A restarted server reads the same recorded member, and a later activation replacement and
	// deactivation do not reinterpret it.
	client = startMCPClient(t, run, "cli", false)
	restarted := mcpInspectTrail(client, "alpha", trailID, true)
	replacement := activateQueryDeclaration(t, run, source, &active, &second, 2)
	afterReplacement := mcpInspectTrail(client, "alpha", trailID, true)
	activateQueryDeclaration(t, run, source, &replacement, nil, 3)
	afterDeactivation := mcpInspectTrail(client, "alpha", trailID, true)
	recorded := mcpCanonicalJSON(t, mcpRecordedTrailMember(t, run.store, requireTrailID(t, trailID), "supersession"))
	for name, inspection := range map[string]map[string]any{
		"restart": restarted, "replacement": afterReplacement, "deactivation": afterDeactivation,
	} {
		historical := inspection["historical"].(map[string]any)
		if mcpCanonicalJSON(t, historical["supersession"]) != recorded {
			t.Fatalf("%s inspection returned %v, want the recorded member %s", name, historical["supersession"], recorded)
		}
		if !reflect.DeepEqual(mcpStableTrailValues(t, before), mcpStableTrailValues(t, inspection)) {
			t.Fatalf("%s changed the inspection beyond its fresh authorization: %v versus %v", name, before, inspection)
		}
	}
	client.close()
}

func TestMCPSupersessionTrailOptInRefusesDamagedRecords(t *testing.T) {
	const predecessorText = "harbor harbor predecessor evidence"
	const successorText = "newterm harbor successor evidence"
	for _, damage := range []struct {
		name      string
		statement string
	}{
		{name: "candidate row removed", statement: `DELETE FROM source_trail_candidates WHERE ordinal = 0`},
		{name: "candidate segment removed", statement: `DELETE FROM segments WHERE id = (SELECT segment_id FROM source_trail_candidates ORDER BY ordinal DESC LIMIT 1)`},
		{name: "recorded activation event removed", statement: `DELETE FROM supersession_activations`},
		{name: "recorded declaration removed", statement: `DELETE FROM supersession_declarations`},
		{name: "record bytes damaged", statement: `UPDATE source_trails SET record_json = X'7b22736368656d61223a226d6f7573612e736f757263655f747261696c2e7634227d'`},
	} {
		t.Run(damage.name, func(t *testing.T) {
			run, _ := setup(t)
			newMCPWithholdingTrailFixture(t, run, "alpha", predecessorText, successorText)
			client := startMCPClient(t, run, "cli", false)
			query := client.call("mousa_query", mcpOptInQuery("alpha", "harbor", 8192, mousa.PackingOriginal), "")
			requireSupersessionCounts(t, query, "evidence", 2, 1, 0, 0, 1)
			trailID := query["trail_id"].(string)
			rawExec(t, run.store, damage.statement)

			// Both the accepted default inspection and the opt-in refuse the damage with the
			// canonical reader's integrity code, release no partial member and commit no audit row.
			for name, args := range map[string]map[string]any{
				"default":  {"source": "alpha", "trail_id": trailID},
				"opted in": {"source": "alpha", "trail_id": trailID, "supersession": true},
			} {
				before := mcpCanonicalInspectionState(t, run.store)
				client.call("mousa_trail", args, "integrity")
				if after := mcpCanonicalInspectionState(t, run.store); !reflect.DeepEqual(before, after) {
					t.Fatalf("%s inspection repaired or committed store state: %v versus %v", name, before, after)
				}
			}
			client.close()
		})
	}
}

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

// The activation administration tests run the built CLI against real temporary SQLite stores in
// separate process invocations, so apply/get semantics, reopen behavior, exit conventions and
// rejected-operation mutation checks are exercised through the native binary.

// activationReason returns a pointer to one recorded reason label.
func activationReason(value string) *string { return &value }

// activationJSON returns the canonical encoding of one activation transition. Every nullable field
// is written explicitly, because the strict codec treats an absent key as different from null.
func activationJSON(t *testing.T, source mousa.Source, expected *mousa.SupersessionActivationID, declaration *mousa.SupersessionDeclarationID, actor, actorVersion string, occurredAtUsec int64, reason *string) []byte {
	t.Helper()
	id, err := mousa.NewSupersessionActivationID(source.ID, expected, declaration)
	if err != nil {
		t.Fatal(err)
	}
	data, err := mousa.EncodeSupersessionActivation(mousa.SupersessionActivation{
		Schema:                       mousa.SupersessionActivationSchema,
		ID:                           id,
		SourceID:                     source.ID,
		ExpectedPreviousActivationID: expected,
		DeclarationID:                declaration,
		ActorID:                      actor,
		ActorVersion:                 actorVersion,
		OccurredAtUsec:               occurredAtUsec,
		Reason:                       reason,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// activationIdentity reports the identity a canonical transition carries, as a caller would read it
// from the emitted record rather than by recomputing it.
func activationIdentity(t *testing.T, canonical []byte) mousa.SupersessionActivationID {
	t.Helper()
	var wire struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(canonical, &wire); err != nil {
		t.Fatalf("canonical activation unreadable: %v", err)
	}
	id, err := mousa.ParseSupersessionActivationID(wire.ID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// activationWire returns the strict wire form of one transition so a test can delete, duplicate or
// alter exactly one field.
func activationWire(t *testing.T, canonical []byte) map[string]any {
	t.Helper()
	var wire map[string]any
	if err := json.Unmarshal(canonical, &wire); err != nil {
		t.Fatalf("canonical activation unreadable: %v", err)
	}
	return wire
}

// marshalActivationWire encodes an altered wire object back to text.
func marshalActivationWire(t *testing.T, wire map[string]any) string {
	t.Helper()
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// storedDeclaration stores one canonical declaration through the native command and returns its
// identity.
func storedDeclaration(t *testing.T, run testRun, declaration []byte) mousa.SupersessionDeclarationID {
	t.Helper()
	code, stdout, stderr := run.runExitInput(string(declaration), "supersession", "declaration", "put")
	if code != 0 {
		t.Fatalf("declaration put exit = %d, stderr = %s", code, stderr)
	}
	var wire struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &wire); err != nil {
		t.Fatalf("declaration output unreadable: %v", err)
	}
	id, err := mousa.ParseSupersessionDeclarationID(wire.ID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// activationPins prepares one JSONL source with a predecessor and two successor revisions, then
// stores one declaration for each successor through the native declaration command. It returns the
// source identity and both stored declaration identities.
func activationPins(t *testing.T, run testRun, externalSourceID string) (mousa.Source, mousa.SupersessionDeclarationID, mousa.SupersessionDeclarationID) {
	t.Helper()
	jsonlSync(t, run, externalSourceID, strings.Join([]string{
		record("doc", "oldterm predecessor evidence"),
		record("corrected", "newterm successor evidence"),
		record("amended", "amended successor evidence"),
	}, "\n"))
	predecessorItemID, predecessorRepresentationID := revisionPin(t, run, externalSourceID, "oldterm")
	successorItemID, successorRepresentationID := revisionPin(t, run, externalSourceID, "newterm")
	amendedItemID, amendedRepresentationID := revisionPin(t, run, externalSourceID, "amended")
	source := declarationSource(t, externalSourceID)
	first := declarationJSON(t, source, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, "example.operations", "successor replaces the predecessor")
	second := declarationJSON(t, source, predecessorItemID, predecessorRepresentationID, amendedItemID, amendedRepresentationID, "example.operations", "amended revision replaces the predecessor")
	return source, storedDeclaration(t, run, first), storedDeclaration(t, run, second)
}

// tableDigest reports one table's row count and a digest of every column, so a test compares the
// exact stored bytes rather than a count alone.
func tableDigest(t *testing.T, path, table string) (int, string) {
	t.Helper()
	db := openRawStore(t, path)
	rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY 1`)
	if err != nil {
		t.Fatalf("scan %s: %v", table, err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	count := 0
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			t.Fatalf("scan %s row: %v", table, err)
		}
		for _, value := range values {
			switch typed := value.(type) {
			case nil:
				hash.Write([]byte{0})
			case []byte:
				fmt.Fprintf(hash, "%d:", len(typed))
				hash.Write(typed)
			case int64:
				fmt.Fprintf(hash, "i%d;", typed)
			case string:
				fmt.Fprintf(hash, "s%d:%s;", len(typed), typed)
			default:
				fmt.Fprintf(hash, "%v;", typed)
			}
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return count, hex.EncodeToString(hash.Sum(nil))
}

// activationStoreState digests everything an accepted or rejected activation command must leave
// byte-identical except the activation rows it is allowed to append.
type activationStoreState struct {
	canonicalRows     string
	declarationCount  int
	declarationDigest string
	eventCount        int
	eventDigest       string
	projectionCount   int
	projectionDigest  string
}

func snapshotActivationStore(t *testing.T, path string) activationStoreState {
	t.Helper()
	events, eventDigest := tableDigest(t, path, "supersession_activations")
	projections, projectionDigest := tableDigest(t, path, "supersession_activation_state")
	declarations, declarationDigest := declarationRows(t, path)
	return activationStoreState{
		canonicalRows:     canonicalDigest(t, path),
		declarationCount:  declarations,
		declarationDigest: declarationDigest,
		eventCount:        events,
		eventDigest:       eventDigest,
		projectionCount:   projections,
		projectionDigest:  projectionDigest,
	}
}

// requireUnchanged reports the first digest that moved, so a rejected command cannot pass by
// changing an unrelated table.
func requireUnchanged(t *testing.T, what string, before, after activationStoreState) {
	t.Helper()
	if before == after {
		return
	}
	switch {
	case after.canonicalRows != before.canonicalRows:
		t.Fatalf("%s changed canonical record rows", what)
	case after.declarationDigest != before.declarationDigest || after.declarationCount != before.declarationCount:
		t.Fatalf("%s changed stored declarations", what)
	case after.eventDigest != before.eventDigest || after.eventCount != before.eventCount:
		t.Fatalf("%s changed activation events: count %d, want %d", what, after.eventCount, before.eventCount)
	case after.projectionDigest != before.projectionDigest || after.projectionCount != before.projectionCount:
		t.Fatalf("%s changed the current projection", what)
	}
	t.Fatalf("%s changed an unclassified stored row", what)
}

// activationCurrentState reads the stored current projection for one source.
func activationCurrentState(t *testing.T, path string, sourceID mousa.SourceID) (mousa.SupersessionActivationID, *mousa.SupersessionDeclarationID) {
	t.Helper()
	db := openRawStore(t, path)
	var current, active []byte
	if err := db.QueryRow(`SELECT current_activation_id, active_declaration_id FROM supersession_activation_state WHERE source_id = ?`, sourceID[:]).Scan(&current, &active); err != nil {
		t.Fatalf("read activation current state: %v", err)
	}
	var id mousa.SupersessionActivationID
	copy(id[:], current)
	var declaration *mousa.SupersessionDeclarationID
	if active != nil {
		var value mousa.SupersessionDeclarationID
		copy(value[:], active)
		declaration = &value
	}
	return id, declaration
}

// applyActivation runs one native apply invocation and returns its exit code, stdout and stderr.
func applyActivation(run testRun, canonical []byte) (int, string, string) {
	return run.runExitInput(string(canonical), "supersession", "activation", "put")
}

func requireApplied(t *testing.T, run testRun, canonical []byte) string {
	t.Helper()
	code, stdout, stderr := applyActivation(run, canonical)
	if code != 0 {
		t.Fatalf("apply exit = %d, stderr = %s", code, stderr)
	}
	if stdout != string(canonical) {
		t.Fatalf("apply did not emit the canonical stored event:\n got %q\nwant %q", stdout, canonical)
	}
	return stdout
}

func TestSupersessionActivationApplyGetRetryReplacementDeactivationReactivation(t *testing.T) {
	run, _ := setup(t)
	const source = "activation/lifecycle"
	sourceIdentity, firstDeclaration, secondDeclaration := activationPins(t, run, source)
	baseline := snapshotActivationStore(t, run.store)
	if baseline.eventCount != 0 || baseline.projectionCount != 0 {
		t.Fatalf("fresh store holds %d events and %d projections", baseline.eventCount, baseline.projectionCount)
	}

	// Initial selection: the caller supplies the explicit null predecessor.
	initial := activationJSON(t, sourceIdentity, nil, &firstDeclaration, "example.operator", "console/1.0", 1759622400000000, activationReason("initial selection"))
	output := requireApplied(t, run, initial)
	initialID := activationIdentity(t, initial)
	afterInitial := snapshotActivationStore(t, run.store)
	if afterInitial.eventCount != 1 || afterInitial.projectionCount != 1 {
		t.Fatalf("after initial apply: %+v", afterInitial)
	}
	if afterInitial.canonicalRows != baseline.canonicalRows || afterInitial.declarationDigest != baseline.declarationDigest {
		t.Fatal("apply changed canonical records or stored declarations")
	}
	if current, active := activationCurrentState(t, run.store, sourceIdentity.ID); current != initialID || active == nil || *active != firstDeclaration {
		t.Fatalf("current state = %s, %v; want %s, %s", current, active, initialID, firstDeclaration)
	}

	// get returns the same canonical bytes from a fresh process.
	code, getOutput, stderr := run.runExit("supersession", "activation", "get", initialID.String())
	if code != 0 {
		t.Fatalf("get exit = %d, stderr = %s", code, stderr)
	}
	if getOutput != output {
		t.Fatalf("get output differs from apply output:\n got %q\nwant %q", getOutput, output)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(getOutput), &decoded); err != nil {
		t.Fatalf("get emitted unreadable JSON: %v", err)
	}
	if decoded["schema"] != mousa.SupersessionActivationSchema || decoded["id"] != initialID.String() ||
		decoded["source_id"] != sourceIdentity.ID.String() || decoded["declaration_id"] != firstDeclaration.String() ||
		decoded["expected_previous_activation_id"] != nil || decoded["actor_id"] != "example.operator" {
		t.Fatalf("get emitted an unexpected event: %v", decoded)
	}
	requireUnchanged(t, "get", afterInitial, snapshotActivationStore(t, run.store))
	sizeAfterGet := storeSize(t, run.store)

	// An exact retry succeeds without appending an event or touching the projection.
	if code, retryOutput, stderr := applyActivation(run, initial); code != 0 || retryOutput != output {
		t.Fatalf("exact retry: exit = %d, output = %q, stderr = %s", code, retryOutput, stderr)
	}
	requireUnchanged(t, "exact retry", afterInitial, snapshotActivationStore(t, run.store))
	if sizeAfterRetry := storeSize(t, run.store); sizeAfterRetry != sizeAfterGet {
		t.Fatalf("exact retry changed the store size: %d, want %d", sizeAfterRetry, sizeAfterGet)
	}

	// Replacement names the current event and selects the other declaration.
	replacement := activationJSON(t, sourceIdentity, &initialID, &secondDeclaration, "example.operator", "console/1.0", 1759622400000001, nil)
	replacementOutput := requireApplied(t, run, replacement)
	replacementID := activationIdentity(t, replacement)
	if replacementOutput == output {
		t.Fatal("replacement emitted the previous event bytes")
	}
	afterReplacement := snapshotActivationStore(t, run.store)
	if afterReplacement.eventCount != 2 || afterReplacement.projectionCount != 1 {
		t.Fatalf("after replacement: %+v", afterReplacement)
	}
	if current, active := activationCurrentState(t, run.store, sourceIdentity.ID); current != replacementID || active == nil || *active != secondDeclaration {
		t.Fatalf("current state = %s, %v; want %s, %s", current, active, replacementID, secondDeclaration)
	}

	// The historical event stays readable after the state advanced.
	code, historicalOutput, stderr := run.runExit("supersession", "activation", "get", initialID.String())
	if code != 0 || historicalOutput != output {
		t.Fatalf("historical get: exit = %d, output = %q, stderr = %s", code, historicalOutput, stderr)
	}

	// A retry succeeds only while its event is the verified current tip, so an event that a later
	// transition replaced is a conflict.
	beforeStaleRetry := snapshotActivationStore(t, run.store)
	code, stdout, stderr := applyActivation(run, initial)
	if code != 1 || !strings.Contains(stderr, "sqlite conflict") {
		t.Fatalf("historical retry: exit = %d, stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("historical retry emitted output: %q", stdout)
	}
	requireUnchanged(t, "historical retry", beforeStaleRetry, snapshotActivationStore(t, run.store))

	// Deactivation selects no declaration; the event it replaced stays in history.
	deactivation := activationJSON(t, sourceIdentity, &replacementID, nil, "example.operator", "console/1.0", 1759622400000002, activationReason("withdraw selection"))
	requireApplied(t, run, deactivation)
	deactivationID := activationIdentity(t, deactivation)
	if current, active := activationCurrentState(t, run.store, sourceIdentity.ID); current != deactivationID || active != nil {
		t.Fatalf("deactivated state = %s, %v; want %s, no declaration", current, active, deactivationID)
	}
	// Reactivation is another replacement whose expected predecessor is the deactivation event.
	reactivation := activationJSON(t, sourceIdentity, &deactivationID, &firstDeclaration, "example.operator", "console/1.0", 1759622400000003, activationReason("restore selection"))
	requireApplied(t, run, reactivation)
	reactivationID := activationIdentity(t, reactivation)
	if current, active := activationCurrentState(t, run.store, sourceIdentity.ID); current != reactivationID || active == nil || *active != firstDeclaration {
		t.Fatalf("reactivated state = %s, %v; want %s, %s", current, active, reactivationID, firstDeclaration)
	}
	final := snapshotActivationStore(t, run.store)
	if final.eventCount != 4 || final.projectionCount != 1 {
		t.Fatalf("after reactivation: %+v", final)
	}
	if final.canonicalRows != baseline.canonicalRows || final.declarationDigest != baseline.declarationDigest {
		t.Fatal("activation transitions changed canonical records or stored declarations")
	}
	for _, historical := range []struct {
		name string
		id   mousa.SupersessionActivationID
	}{{"initial", initialID}, {"replacement", replacementID}, {"deactivation", deactivationID}} {
		code, output, stderr := run.runExit("supersession", "activation", "get", historical.id.String())
		if code != 0 {
			t.Fatalf("historical get %s: exit = %d, stderr = %s", historical.name, code, stderr)
		}
		if got := activationIdentity(t, []byte(output)); got != historical.id {
			t.Fatalf("historical get %s returned %s", historical.name, got)
		}
	}
}

func TestSupersessionActivationApplyRejectsMalformedInput(t *testing.T) {
	run, _ := setup(t)
	const source = "activation/malformed"
	sourceIdentity, firstDeclaration, secondDeclaration := activationPins(t, run, source)
	canonical := activationJSON(t, sourceIdentity, nil, &firstDeclaration, "example.operator", "console/1.0", 1759622400000000, nil)
	canonicalText := string(canonical)
	wire := func() map[string]any { return activationWire(t, canonical) }
	without := func(field string) string {
		altered := wire()
		delete(altered, field)
		return marshalActivationWire(t, altered)
	}
	changed := func(field string, value any) string {
		altered := wire()
		altered[field] = value
		return marshalActivationWire(t, altered)
	}

	before := snapshotActivationStore(t, run.store)
	duplicateInput := strings.Replace(canonicalText, "{", `{"actor_id":"duplicate",`, 1)
	cases := []struct {
		name  string
		input string
	}{
		{"empty input", ""},
		{"malformed JSON", `{"schema": "mousa.supersession_activation.v1"`},
		{"not an object", `"mousa.supersession_activation.v1"`},
		{"wrong schema", changed("schema", "mousa.supersession_activation.v2")},
		{"duplicate field", duplicateInput},
		{"unknown field", changed("unexpected_field", "x")},
		{"absent nullable reason", without("reason")},
		{"absent nullable declaration", without("declaration_id")},
		{"absent nullable predecessor", without("expected_previous_activation_id")},
		{"null reason is a string field", changed("reason", 7)},
		{"declaration is not an identity", changed("declaration_id", 7)},
		{"malformed predecessor identity", changed("expected_previous_activation_id", "abcd")},
		{"zero event identity", changed("id", strings.Repeat("0", 64))},
		{"uppercase event identity", changed("id", strings.ToUpper(activationIdentity(t, canonical).String()))},
		{"identity disagrees with content", changed("declaration_id", secondDeclaration.String())},
		{"non-positive occurrence time", changed("occurred_at_usec", 0)},
		{"null declaration for a first activation", changed("declaration_id", nil)},
		{"missing source identity", without("source_id")},
		{"trailing value", canonicalText + `{"schema":"mousa.supersession_activation.v1"}`},
		{"oversized input", strings.Repeat("x", mousa.MaxSupersessionActivationBytes+1)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, stdout, stderr := applyActivation(run, []byte(testCase.input))
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr = %s", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("rejected input emitted output: %q", stdout)
			}
			if !strings.HasPrefix(stderr, "mousa: ") {
				t.Fatalf("stderr does not follow the CLI convention: %q", stderr)
			}
		})
	}
	// A duplicate key must be the duplicate rejection, not an accepted last-wins object.
	code, _, stderr := applyActivation(run, []byte(duplicateInput))
	if code != 1 || !strings.Contains(stderr, "duplicate") {
		t.Fatalf("duplicate field: exit = %d, stderr = %q", code, stderr)
	}
	requireUnchanged(t, "rejected malformed input", before, snapshotActivationStore(t, run.store))

	// Malformed and oversized input is rejected before the writable store opens, so an absent store
	// stays absent rather than being created and migrated.
	for _, testCase := range []string{"", cases[len(cases)-1].input} {
		absentPath := filepath.Join(t.TempDir(), "absent.sqlite")
		empty := testRun{t: t, binary: run.binary, store: absentPath}
		if code, _, stderr := applyActivation(empty, []byte(testCase)); code != 1 {
			t.Fatalf("absent store exit = %d, stderr = %s", code, stderr)
		}
		if _, err := os.Stat(absentPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("rejected input created the store: %v", err)
		}
	}
}

func TestSupersessionActivationApplyRejectsInvalidTargetsAndExpectations(t *testing.T) {
	run, _ := setup(t)
	const source = "activation/targets"
	sourceIdentity, firstDeclaration, secondDeclaration := activationPins(t, run, source)
	_, foreignDeclaration, _ := activationPins(t, run, "activation/foreign")

	initial := activationJSON(t, sourceIdentity, nil, &firstDeclaration, "example.operator", "console/1.0", 1759622400000000, nil)
	requireApplied(t, run, initial)
	initialID := activationIdentity(t, initial)

	absentSourceID, err := mousa.NewSourceID("mousa-jsonl", "activation/never-synced")
	if err != nil {
		t.Fatal(err)
	}
	absentDeclaration := firstDeclaration
	absentDeclaration[len(absentDeclaration)-1] ^= 0x01
	absentActivation := initialID
	absentActivation[0] ^= 0x01
	baseline := snapshotActivationStore(t, run.store)

	cases := []struct {
		name      string
		canonical []byte
		code      string
	}{
		{
			"source does not exist",
			activationJSON(t, mousa.Source{ID: absentSourceID}, nil, &firstDeclaration, "example.operator", "console/1.0", 1759622400000001, nil),
			"not_found",
		},
		{
			"selected declaration does not exist",
			activationJSON(t, sourceIdentity, &initialID, &absentDeclaration, "example.operator", "console/1.0", 1759622400000002, nil),
			"conflict",
		},
		{
			"selected declaration belongs to another source",
			activationJSON(t, sourceIdentity, &initialID, &foreignDeclaration, "example.operator", "console/1.0", 1759622400000003, nil),
			"conflict",
		},
		{
			"expected predecessor is stale",
			activationJSON(t, sourceIdentity, &absentActivation, &secondDeclaration, "example.operator", "console/1.0", 1759622400000004, nil),
			"conflict",
		},
		{
			"second root for the same source",
			activationJSON(t, sourceIdentity, nil, &secondDeclaration, "example.operator", "console/1.0", 1759622400000005, nil),
			"conflict",
		},
		{
			"redundant selection of the active declaration",
			activationJSON(t, sourceIdentity, &initialID, &firstDeclaration, "example.operator", "console/1.0", 1759622400000006, nil),
			"conflict",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, stdout, stderr := applyActivation(run, testCase.canonical)
			if code != 1 {
				t.Fatalf("exit = %d, want 1; stderr = %s", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("rejected transition emitted output: %q", stdout)
			}
			if !strings.Contains(stderr, "sqlite "+testCase.code) {
				t.Fatalf("stderr = %q, want the %s classification", stderr, testCase.code)
			}
			requireUnchanged(t, testCase.name, baseline, snapshotActivationStore(t, run.store))
			if current, active := activationCurrentState(t, run.store, sourceIdentity.ID); current != initialID || active == nil || *active != firstDeclaration {
				t.Fatalf("rejected transition moved the current state to %s, %v", current, active)
			}
		})
	}

	// A competing branch that names the same predecessor is refused once the state advanced.
	competitor := activationJSON(t, sourceIdentity, &initialID, &secondDeclaration, "example.operator", "console/1.0", 1759622400000007, nil)
	requireApplied(t, run, competitor)
	afterCompetitor := snapshotActivationStore(t, run.store)
	losing := activationJSON(t, sourceIdentity, &initialID, &firstDeclaration, "example.operator", "console/1.0", 1759622400000008, activationReason("competing branch"))
	code, stdout, stderr := applyActivation(run, losing)
	if code != 1 || !strings.Contains(stderr, "sqlite conflict") {
		t.Fatalf("competing transition: exit = %d, stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("competing transition emitted output: %q", stdout)
	}
	requireUnchanged(t, "competing transition", afterCompetitor, snapshotActivationStore(t, run.store))

	// Metadata is recorded evidence, not part of the identity: replaying an existing identity with a
	// different actor, time or reason is a conflict, not an exact retry.
	competitorID := activationIdentity(t, competitor)
	for _, replayed := range []struct {
		name      string
		canonical []byte
	}{
		{"actor change", activationJSON(t, sourceIdentity, &initialID, &secondDeclaration, "example.other", "console/1.0", 1759622400000007, nil)},
		{"time change", activationJSON(t, sourceIdentity, &initialID, &secondDeclaration, "example.operator", "console/1.0", 1759622409999999, nil)},
		{"reason change", activationJSON(t, sourceIdentity, &initialID, &secondDeclaration, "example.operator", "console/1.0", 1759622400000007, activationReason("different reason"))},
	} {
		t.Run("replay with "+replayed.name, func(t *testing.T) {
			if got := activationIdentity(t, replayed.canonical); got != competitorID {
				t.Fatalf("replayed identity = %s, want %s", got, competitorID)
			}
			code, stdout, stderr := applyActivation(run, replayed.canonical)
			if code != 1 || !strings.Contains(stderr, "sqlite conflict") {
				t.Fatalf("exit = %d, stderr = %q", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("replayed metadata emitted output: %q", stdout)
			}
			requireUnchanged(t, "metadata replay", afterCompetitor, snapshotActivationStore(t, run.store))
		})
	}
}

func TestSupersessionActivationGetExitsForMissingInvalidAndAbsentStore(t *testing.T) {
	run, _ := setup(t)
	const source = "activation/get-errors"
	sourceIdentity, firstDeclaration, _ := activationPins(t, run, source)
	initial := activationJSON(t, sourceIdentity, nil, &firstDeclaration, "example.operator", "console/1.0", 1759622400000000, nil)
	requireApplied(t, run, initial)
	initialID := activationIdentity(t, initial)
	before := snapshotActivationStore(t, run.store)

	// An unknown but well-formed identity is a missing record, not an invalid invocation.
	unknown := initialID
	unknown[0] ^= 0x01
	code, stdout, stderr := run.runExit("supersession", "activation", "get", unknown.String())
	if code != 1 || !strings.Contains(stderr, "sqlite not_found") {
		t.Fatalf("missing event: exit = %d, stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("missing event emitted output: %q", stdout)
	}
	requireUnchanged(t, "missing event read", before, snapshotActivationStore(t, run.store))

	for _, testCase := range []struct {
		name string
		args []string
	}{
		{"short identity", []string{"supersession", "activation", "get", "abcd"}},
		{"uppercase identity", []string{"supersession", "activation", "get", strings.ToUpper(initialID.String())}},
		{"no identity", []string{"supersession", "activation", "get"}},
		{"two identities", []string{"supersession", "activation", "get", initialID.String(), initialID.String()}},
		{"unknown activation subcommand", []string{"supersession", "activation", "list"}},
		{"unknown supersession subcommand", []string{"supersession", "activationx", "get"}},
		{"no supersession subcommand", []string{"supersession"}},
		{"no activation subcommand", []string{"supersession", "activation"}},
		{"apply with an argument", []string{"supersession", "activation", "put", initialID.String()}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			code, stdout, stderr := run.runExit(testCase.args...)
			if code != 2 {
				t.Fatalf("exit = %d, want 2; stderr = %s", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("invalid invocation emitted output: %q", stdout)
			}
			if !strings.HasPrefix(stderr, "mousa: ") || !strings.Contains(stderr, "usage: mousa") {
				t.Fatalf("stderr does not report usage: %q", stderr)
			}
		})
	}
	requireUnchanged(t, "invalid invocations", before, snapshotActivationStore(t, run.store))

	// A read never creates a store: an absent path stays absent.
	absentPath := filepath.Join(t.TempDir(), "absent.sqlite")
	empty := testRun{t: t, binary: run.binary, store: absentPath}
	code, stdout, stderr = empty.runExit("supersession", "activation", "get", initialID.String())
	if code != 1 {
		t.Fatalf("absent store exit = %d, want 1; stderr = %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("absent store emitted output: %q", stdout)
	}
	if _, err := os.Stat(absentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created the store: %v", err)
	}
}

func TestSupersessionActivationRejectsCorruptHistoryAndProjectionWithoutRepair(t *testing.T) {
	cases := []struct {
		name   string
		damage string
	}{
		{"corrupt event record", `UPDATE supersession_activations SET record_json = x'6e6f742d6a736f6e'`},
		{"projection disagrees with its tip", `UPDATE supersession_activation_state SET active_declaration_id = NULL`},
		{"history without a current projection", `DELETE FROM supersession_activation_state`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			run, _ := setup(t)
			const source = "activation/corrupt"
			sourceIdentity, firstDeclaration, _ := activationPins(t, run, source)
			initial := activationJSON(t, sourceIdentity, nil, &firstDeclaration, "example.operator", "console/1.0", 1759622400000000, nil)
			requireApplied(t, run, initial)
			initialID := activationIdentity(t, initial)

			rawExec(t, run.store, testCase.damage)
			damaged := snapshotActivationStore(t, run.store)

			// Both the read and the writable apply open the same damaged store and must fail closed.
			code, stdout, stderr := run.runExit("supersession", "activation", "get", initialID.String())
			if code != 1 {
				t.Fatalf("get exit = %d, want 1; stderr = %s", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("damaged read emitted output: %q", stdout)
			}
			if !strings.Contains(stderr, "integrity") {
				t.Fatalf("get stderr = %q, want an integrity failure", stderr)
			}
			replacement := activationJSON(t, sourceIdentity, &initialID, &firstDeclaration, "example.operator", "console/1.0", 1759622400000001, activationReason("repair attempt"))
			if code, stdout, stderr := applyActivation(run, replacement); code != 1 {
				t.Fatalf("apply over damage: exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
			}
			requireUnchanged(t, "damaged store commands", damaged, snapshotActivationStore(t, run.store))
		})
	}
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

// The activation state-view tests run the built CLI in separate processes against fresh temporary
// SQLite stores, so the read-only projection, its exit conventions and every stored byte it must
// leave untouched are exercised through the native binary rather than an in-process store handle.

// activationStateView runs one native state read and returns the decoded JSON object. The exact key
// set is part of the CLI view contract: a missing or unexpected field fails here.
func activationStateView(t *testing.T, run testRun, sourceID mousa.SourceID) map[string]any {
	t.Helper()
	code, stdout, stderr := run.runExit("supersession", "activation", "state", sourceID.String())
	if code != 0 {
		t.Fatalf("state exit = %d, stderr = %s", code, stderr)
	}
	var view map[string]any
	if err := json.Unmarshal([]byte(stdout), &view); err != nil {
		t.Fatalf("state emitted unreadable JSON: %v\n%s", err, stdout)
	}
	if len(view) != 3 {
		t.Fatalf("state emitted %d fields, want exactly source_id, current_activation_id and active_declaration_id: %v", len(view), view)
	}
	for _, key := range []string{"source_id", "current_activation_id", "active_declaration_id"} {
		if _, present := view[key]; !present {
			t.Fatalf("state view omits %q: %v", key, view)
		}
	}
	if view["source_id"] != sourceID.String() {
		t.Fatalf("state source_id = %v, want %s", view["source_id"], sourceID)
	}
	return view
}

// requireStateView compares one emitted view with the canonical transition the caller submitted, so
// the projection is checked against that record instead of a recomputed identity. A deactivated
// declaration must read as an explicit JSON null.
func requireStateView(t *testing.T, view map[string]any, submitted []byte) {
	t.Helper()
	record, err := mousa.DecodeSupersessionActivation(submitted)
	if err != nil {
		t.Fatal(err)
	}
	if view["current_activation_id"] != record.ID.String() {
		t.Fatalf("current_activation_id = %v, want the submitted event %s", view["current_activation_id"], record.ID)
	}
	active := view["active_declaration_id"]
	if record.DeclarationID == nil {
		if active != nil {
			t.Fatalf("deactivated view active_declaration_id = %v, want JSON null", active)
		}
		return
	}
	if active != record.DeclarationID.String() {
		t.Fatalf("active_declaration_id = %v, want the submitted declaration %s", active, record.DeclarationID)
	}
}

// requireReadOnlyRead fails when a state read moved any stored row or the store file itself.
func requireReadOnlyRead(t *testing.T, run testRun, before activationStoreState, sizeBefore int64) {
	t.Helper()
	requireUnchanged(t, "state read", before, snapshotActivationStore(t, run.store))
	if sizeAfter := storeSize(t, run.store); sizeAfter != sizeBefore {
		t.Fatalf("state read changed the store file size: %d, want %d", sizeAfter, sizeBefore)
	}
}

// schemaVersion reports the highest applied migration of one store outside the CLI.
func schemaVersion(t *testing.T, path string) int {
	t.Helper()
	db := openRawStore(t, path)
	var version int
	if err := db.QueryRow(`SELECT coalesce(max(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	return version
}

// foreignKeyViolations reports how many stored references SQLite rejects, so a corruption case can
// prove it damaged the projection rather than a foreign key reference.
func foreignKeyViolations(t *testing.T, path string) int {
	t.Helper()
	db := openRawStore(t, path)
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign key check: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read foreign key check rows: %v", err)
	}
	return count
}

// TestSupersessionActivationStateReportsEachVerifiedTransition checks the read-only projection
// against the transitions the caller submitted: initial selection, replacement, deactivation with
// an explicit null declaration, reactivation, and a second source that stays isolated.
func TestSupersessionActivationStateReportsEachVerifiedTransition(t *testing.T) {
	run, _ := setup(t)
	const source = "activation/state"
	sourceIdentity, firstDeclaration, secondDeclaration := activationPins(t, run, source)

	// The source exists with stored declarations but no activation history, so the read is the
	// store's missing record rather than an empty state.
	before := snapshotActivationStore(t, run.store)
	sizeBefore := storeSize(t, run.store)
	code, stdout, stderr := run.runExit("supersession", "activation", "state", sourceIdentity.ID.String())
	if code != 1 || !strings.Contains(stderr, "sqlite not_found") {
		t.Fatalf("state without history: exit = %d, stderr = %q", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("state without history emitted output: %q", stdout)
	}
	requireReadOnlyRead(t, run, before, sizeBefore)

	// Initial selection names the explicit null predecessor.
	initial := activationJSON(t, sourceIdentity, nil, &firstDeclaration, "example.operator", "console/1.0", 1759622400000000, activationReason("initial selection"))
	requireApplied(t, run, initial)
	requireStateView(t, activationStateView(t, run, sourceIdentity.ID), initial)

	// Replacement selects the other declaration.
	initialID := activationIdentity(t, initial)
	replacement := activationJSON(t, sourceIdentity, &initialID, &secondDeclaration, "example.operator", "console/1.0", 1759622400000001, nil)
	requireApplied(t, run, replacement)
	requireStateView(t, activationStateView(t, run, sourceIdentity.ID), replacement)

	// Deactivation selects no declaration, which the view reports as an explicit JSON null.
	replacementID := activationIdentity(t, replacement)
	deactivation := activationJSON(t, sourceIdentity, &replacementID, nil, "example.operator", "console/1.0", 1759622400000002, activationReason("withdraw selection"))
	requireApplied(t, run, deactivation)
	deactivated := activationStateView(t, run, sourceIdentity.ID)
	requireStateView(t, deactivated, deactivation)
	if value, present := deactivated["active_declaration_id"]; !present || value != nil {
		t.Fatalf("deactivated view = %v, want an explicit null active_declaration_id", deactivated)
	}

	// Reactivation names the deactivation event as its expected predecessor.
	deactivationID := activationIdentity(t, deactivation)
	reactivation := activationJSON(t, sourceIdentity, &deactivationID, &firstDeclaration, "example.operator", "console/1.0", 1759622400000003, activationReason("restore selection"))
	requireApplied(t, run, reactivation)

	// A second source keeps its own current event and declaration.
	otherIdentity, otherDeclaration, _ := activationPins(t, run, "activation/state-other")
	otherInitial := activationJSON(t, otherIdentity, nil, &otherDeclaration, "example.other", "console/1.0", 1759622400000004, nil)
	requireApplied(t, run, otherInitial)

	readBefore := snapshotActivationStore(t, run.store)
	readSize := storeSize(t, run.store)
	ownView := activationStateView(t, run, sourceIdentity.ID)
	requireStateView(t, ownView, reactivation)
	otherView := activationStateView(t, run, otherIdentity.ID)
	requireStateView(t, otherView, otherInitial)
	if ownView["current_activation_id"] == otherView["current_activation_id"] ||
		ownView["active_declaration_id"] == otherView["active_declaration_id"] {
		t.Fatalf("sources share current state: %v and %v", ownView, otherView)
	}
	requireReadOnlyRead(t, run, readBefore, readSize)

	// The historical event read keeps its own semantics alongside the state view.
	code, historicalOutput, stderr := run.runExit("supersession", "activation", "get", initialID.String())
	if code != 0 {
		t.Fatalf("historical get exit = %d, stderr = %s", code, stderr)
	}
	if historicalOutput != string(initial) {
		t.Fatalf("historical get output differs from the stored event:\n got %q\nwant %q", historicalOutput, initial)
	}
}

// TestSupersessionActivationStateExitsForInvalidInvocationAbsentStoreAndOlderStore covers the
// invocation and store-level exits: usage errors stay exit 2, storage failures exit 1 with empty
// stdout, a read creates no store, and an older supported store is never migrated by a read.
func TestSupersessionActivationStateExitsForInvalidInvocationAbsentStoreAndOlderStore(t *testing.T) {
	run, _ := setup(t)
	sourceIdentity, firstDeclaration, _ := activationPins(t, run, "activation/state-exits")
	initial := activationJSON(t, sourceIdentity, nil, &firstDeclaration, "example.operator", "console/1.0", 1759622400000000, nil)
	requireApplied(t, run, initial)

	// A synced source with stored declarations but no transition has no activation history.
	noHistory, _, _ := activationPins(t, run, "activation/state-none")
	before := snapshotActivationStore(t, run.store)
	sizeBefore := storeSize(t, run.store)
	code, stdout, stderr := run.runExit("supersession", "activation", "state", noHistory.ID.String())
	if code != 1 || !strings.Contains(stderr, "sqlite not_found") || stdout != "" {
		t.Fatalf("state without history: exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}

	for _, testCase := range []struct {
		name string
		args []string
	}{
		{"no source identity", []string{"supersession", "activation", "state"}},
		{"two source identities", []string{"supersession", "activation", "state", sourceIdentity.ID.String(), sourceIdentity.ID.String()}},
		{"short source identity", []string{"supersession", "activation", "state", "abcd"}},
		{"uppercase source identity", []string{"supersession", "activation", "state", strings.ToUpper(sourceIdentity.ID.String())}},
		{"unknown option", []string{"supersession", "activation", "state", "--all", sourceIdentity.ID.String()}},
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
	requireReadOnlyRead(t, run, before, sizeBefore)

	// A read never creates a store.
	absentPath := filepath.Join(t.TempDir(), "absent.sqlite")
	absent := testRun{t: t, binary: run.binary, store: absentPath}
	code, stdout, stderr = absent.runExit("supersession", "activation", "state", sourceIdentity.ID.String())
	if code != 1 {
		t.Fatalf("absent store exit = %d, want 1; stderr = %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("absent store emitted output: %q", stdout)
	}
	if _, err := os.Stat(absentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state read created the store: %v", err)
	}

	// An older supported store is refused by a read and stays at its own version, so nothing is
	// migrated, backed up or repaired.
	olderPath := filepath.Join(t.TempDir(), "version13.sqlite")
	older := testRun{t: t, binary: run.binary, store: olderPath}
	olderSource, olderDeclaration, _ := activationPins(t, older, "activation/state-version13")
	olderInitial := activationJSON(t, olderSource, nil, &olderDeclaration, "example.operator", "console/1.0", 1759622400000000, nil)
	requireApplied(t, older, olderInitial)
	rawExec(t, olderPath, `DROP INDEX supersession_activations_source_idx; DELETE FROM schema_migrations WHERE version = 14`)
	if version := schemaVersion(t, olderPath); version != 13 {
		t.Fatalf("older store fixture reports schema version %d, want 13", version)
	}
	olderSize := storeSize(t, olderPath)
	code, stdout, stderr = older.runExit("supersession", "activation", "state", olderSource.ID.String())
	if code != 1 {
		t.Fatalf("older store exit = %d, want 1; stderr = %s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("older store emitted output: %q", stdout)
	}
	if !strings.Contains(stderr, "read_only") {
		t.Fatalf("older store stderr = %q, want the read-only refusal", stderr)
	}
	if version := schemaVersion(t, olderPath); version != 13 {
		t.Fatalf("state read migrated the older store to schema version %d", version)
	}
	if _, err := os.Stat(olderPath + ".pre-migrate-v13-to-v14.sqlite"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state read created a migration backup: %v", err)
	}
	if after := storeSize(t, olderPath); after != olderSize {
		t.Fatalf("state read changed the older store size: %d, want %d", after, olderSize)
	}
}

// TestSupersessionActivationStateRejectsDamagedProjectionWithoutRepair damages one stored row per
// case outside the CLI: history without a current projection, a projection that disagrees with its
// tip, a projection that still names a replaced event, and a corrupt event record. Each read must
// fail through that corruption's own verification, not through an unrelated artifact such as a
// broken foreign key, and must leave every row, including the damaged one, exactly as found.
func TestSupersessionActivationStateRejectsDamagedProjectionWithoutRepair(t *testing.T) {
	cases := []struct {
		name   string
		damage func(initialID mousa.SupersessionActivationID, firstDeclaration mousa.SupersessionDeclarationID) string
		reason string
	}{
		{
			"history without a current projection",
			func(mousa.SupersessionActivationID, mousa.SupersessionDeclarationID) string {
				return `DELETE FROM supersession_activation_state`
			},
			"outside current chains",
		},
		{
			"projection disagrees with its tip",
			func(mousa.SupersessionActivationID, mousa.SupersessionDeclarationID) string {
				return `UPDATE supersession_activation_state SET active_declaration_id = NULL`
			},
			"current projection disagrees with the latest activation",
		},
		{
			"projection names a replaced event",
			func(initialID mousa.SupersessionActivationID, firstDeclaration mousa.SupersessionDeclarationID) string {
				return fmt.Sprintf("UPDATE supersession_activation_state SET current_activation_id = x'%x', active_declaration_id = x'%x'", initialID[:], firstDeclaration[:])
			},
			"a later activation succeeds the current one",
		},
		{
			"corrupt event record",
			func(mousa.SupersessionActivationID, mousa.SupersessionDeclarationID) string {
				return `UPDATE supersession_activations SET record_json = x'6e6f742d6a736f6e'`
			},
			"invalid or duplicate JSON object key",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			run, _ := setup(t)
			sourceIdentity, firstDeclaration, secondDeclaration := activationPins(t, run, "activation/state-damaged")
			initial := activationJSON(t, sourceIdentity, nil, &firstDeclaration, "example.operator", "console/1.0", 1759622400000000, nil)
			requireApplied(t, run, initial)
			initialID := activationIdentity(t, initial)
			replacement := activationJSON(t, sourceIdentity, &initialID, &secondDeclaration, "example.operator", "console/1.0", 1759622400000001, nil)
			requireApplied(t, run, replacement)
			if broken := foreignKeyViolations(t, run.store); broken != 0 {
				t.Fatalf("undamaged store reports %d foreign-key violations", broken)
			}

			rawExec(t, run.store, testCase.damage(initialID, firstDeclaration))
			damaged := snapshotActivationStore(t, run.store)
			damagedSize := storeSize(t, run.store)
			if broken := foreignKeyViolations(t, run.store); broken != 0 {
				t.Fatalf("the %s damage broke %d foreign key reference(s), so it is not a narrow projection corruption", testCase.name, broken)
			}

			code, stdout, stderr := run.runExit("supersession", "activation", "state", sourceIdentity.ID.String())
			if code != 1 {
				t.Fatalf("damaged state read exit = %d, want 1; stderr = %s", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("damaged state read emitted output: %q", stdout)
			}
			if !strings.Contains(stderr, "integrity") {
				t.Fatalf("damaged state read stderr = %q, want an integrity failure", stderr)
			}
			if !strings.Contains(stderr, testCase.reason) {
				t.Fatalf("damaged state read stderr = %q, want the %q verification", stderr, testCase.reason)
			}
			requireReadOnlyRead(t, run, damaged, damagedSize)
		})
	}
}

package main

import (
	"database/sql"
	"reflect"
	"strings"
	"testing"
)

// The access command and sync --allow drive the append-only event log through the real
// binary: a source with no event is denied, each change leaves one event, and a repeated
// change is a no-op that cites the same event.
func TestAccessEventsDefaultDenyAndSyncAllow(t *testing.T) {
	run, _ := setup(t)
	const source = "access/stream"
	run.runInput(record("doc", "Walrus access findings.")+"\n", false, "sync", "--source", source)

	requireDecision := func(report map[string]any, outcome, event string, reasons ...string) {
		t.Helper()
		if report["decision_outcome"] != outcome {
			t.Fatalf("decision_outcome = %v, want %s: %v", report["decision_outcome"], outcome, report)
		}
		var got []string
		for _, reason := range report["decision_reasons"].([]any) {
			got = append(got, reason.(string))
		}
		if !reflect.DeepEqual(got, reasons) {
			t.Fatalf("decision_reasons = %v, want %v", got, reasons)
		}
		if report["access_event"] != event {
			t.Fatalf("access_event = %v, want %s", report["access_event"], event)
		}
	}

	denied := querySource(t, run, source, "walrus")
	requireDecision(denied, "deny", "default", "access_default")
	if len(hits(denied)) != 0 {
		t.Fatalf("default-denied source released evidence: %v", denied)
	}

	access := func(want string, wantChanged bool) map[string]any {
		t.Helper()
		_, report := run.run(false, "access", "--source", source, want)
		if report["access"] != want || report["changed"] != wantChanged {
			t.Fatalf("access %s = %v, want changed %v", want, report, wantChanged)
		}
		return report
	}
	isEventID := func(value any) bool {
		id, ok := value.(string)
		return ok && len(id) == 64 && id == strings.ToLower(id)
	}

	allow := access("allow", true)
	if !isEventID(allow["access_event_id"]) {
		t.Fatalf("access_event_id = %v, want 64 lowercase hex", allow["access_event_id"])
	}
	allowed := querySource(t, run, source, "walrus")
	requireDecision(allowed, "allow", allow["access_event_id"].(string), "allow")
	if len(hits(allowed)) == 0 {
		t.Fatalf("allowed source returned no evidence: %v", allowed)
	}

	deny := access("deny", true)
	if !isEventID(deny["access_event_id"]) || deny["access_event_id"] == allow["access_event_id"] {
		t.Fatalf("deny event id = %v, want a new event id", deny["access_event_id"])
	}
	repeat := access("deny", false)
	if repeat["access_event_id"] != deny["access_event_id"] {
		t.Fatalf("deny repeat appended %v, want %v", repeat["access_event_id"], deny["access_event_id"])
	}
	redenied := querySource(t, run, source, "walrus")
	requireDecision(redenied, "deny", deny["access_event_id"].(string), "access_denied")
	if len(hits(redenied)) != 0 {
		t.Fatalf("denied source released evidence: %v", redenied)
	}

	db, err := sql.Open("sqlite", run.store)
	if err != nil {
		t.Fatal(err)
	}
	var eventRows, leafRows int
	if err := db.QueryRow(`SELECT count(*) FROM access_events`).Scan(&eventRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM log WHERE kind = 'access_event'`).Scan(&leafRows); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if eventRows != 2 || leafRows != 2 {
		t.Fatalf("allow then deny left %d events and %d leaves, want 2 each", eventRows, leafRows)
	}
}

// sync --allow records allow only for a source with no access event, so a scheduled resync
// cannot reverse an explicit deny; it reports the access already in force instead.
func TestSyncAllowKeepsExplicitDeny(t *testing.T) {
	run, _ := setup(t)
	const source = "access/resync"
	input := record("doc", "Walrus resync findings.") + "\n"
	_, first := run.runInput(input, false, "sync", "--source", source, "--allow")
	if first["access"] != "allow" {
		t.Fatalf("first sync --allow = %v", first)
	}
	_, deny := run.run(false, "access", "--source", source, "deny")
	_, resync := run.runInput(input, false, "sync", "--source", source, "--allow")
	if resync["access"] != "deny" || resync["access_event_id"] != deny["access_event_id"] {
		t.Fatalf("resync --allow = %v, want deny citing %v", resync, deny["access_event_id"])
	}
	denied := querySource(t, run, source, "walrus")
	reasons, _ := denied["decision_reasons"].([]any)
	if denied["decision_outcome"] != "deny" || len(reasons) != 1 || reasons[0] != "access_denied" || len(hits(denied)) != 0 {
		t.Fatalf("resync --allow reversed deny: %v", denied)
	}
	db, err := sql.Open("sqlite", run.store)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var events int
	if err := db.QueryRow(`SELECT count(*) FROM access_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("access events = %d, want 2", events)
	}
}

func TestAccessSyncAllowAndPreviewUsageError(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "note.md", "Cedar directory evidence.")
	_, synced := run.run(false, "sync", "--allow", root)
	if synced["access"] != "allow" || synced["access_event_id"] == "" {
		t.Fatalf("sync --allow = %v", synced)
	}
	_, allowed := run.run(false, "query", root, "cedar")
	if allowed["decision_outcome"] != "allow" || allowed["access_event"] != synced["access_event_id"] || len(hits(allowed)) == 0 {
		t.Fatalf("sync --allow source not retrievable: %v", allowed)
	}
	out, _ := run.run(true, "sync", "--preview", "--allow", root)
	if !strings.Contains(out, "--allow cannot be used with --preview") {
		t.Fatalf("preview --allow = %q, want usage error", out)
	}
}

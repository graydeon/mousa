package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"testing"
)

// Durable note tests exercise the built CLI and real MCP clients: explicit
// source bootstrap, exact note bytes, query-trail lineage, lifecycle staleness,
// rejection rollback, idempotent retry, exact export, and tamper detection.

func noteHitMeta(t *testing.T, hit map[string]any) map[string]any {
	t.Helper()
	meta, ok := hit["note"].(map[string]any)
	if !ok {
		t.Fatalf("evidence hit lacks note metadata: %v", hit)
	}
	return meta
}

func staleReasons(meta map[string]any) []string {
	reasons := []string{}
	for _, raw := range meta["stale"].([]any) {
		reasons = append(reasons, raw.(map[string]any)["reason"].(string))
	}
	return reasons
}

// noteHitByID selects the evidence hit carrying note metadata for id, and
// fails the test when the note is absent instead of silently passing.
func noteHitByID(t *testing.T, report map[string]any, id string) map[string]any {
	t.Helper()
	for _, candidate := range hits(report) {
		hit := candidate.(map[string]any)
		if meta, ok := hit["note"].(map[string]any); ok && meta["id"] == id {
			return hit
		}
	}
	t.Fatalf("note %s missing from evidence: %v", id, report)
	return nil
}

// staleEntry returns the stale flag matching reason, failing when the reason
// is absent and requiring the named IDs so a wrong-citation flag cannot pass.
func staleEntry(t *testing.T, meta map[string]any, reason, trailID string) map[string]any {
	t.Helper()
	for _, raw := range meta["stale"].([]any) {
		entry := raw.(map[string]any)
		if entry["reason"] == reason {
			if entry["trail_id"] != trailID {
				t.Fatalf("stale %s names trail %v, want %s: %v", reason, entry["trail_id"], trailID, meta)
			}
			return entry
		}
	}
	t.Fatalf("stale reason %s missing: %v", reason, meta)
	return nil
}

func exportNotes(t *testing.T, run testRun, sourceID string) []map[string]any {
	t.Helper()
	code, stdout, stderr := run.runExitInput("", "notes", "export", "--source", sourceID)
	if code != 0 {
		t.Fatalf("notes export failed: %s", stderr)
	}
	var exported []map[string]any
	for line := range strings.Lines(strings.TrimSpace(stdout)) {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("export line is not one JSON object: %q: %v", line, err)
		}
		exported = append(exported, record)
	}
	return exported
}

func memoryCounts(t *testing.T, path string) map[string]int {
	t.Helper()
	db := openRawStore(t, path)
	counts := map[string]int{}
	for _, table := range []string{"local_items", "notes", "note_citations", "ingest_receipts", "observations", "artifacts", "representations", "segments", "log"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		counts[table] = count
	}
	// Access events legitimately grow log; note leaves are the part a rejected
	// or retried write must never duplicate.
	var noteLeaves int
	if err := db.QueryRow(`SELECT count(*) FROM log WHERE kind = 'note'`).Scan(&noteLeaves); err != nil {
		t.Fatalf("count note leaves: %v", err)
	}
	counts["note_leaves"] = noteLeaves
	return counts
}

func TestMemoryCLI(t *testing.T) {
	run, root := setup(t)

	// A: document evidence trail a note can cite.
	writeFile(t, root, "doc.md", "The harbor inspection is on Friday.\n")
	run.run(false, "sync", "--allow", root)
	_, query := run.run(false, "query", root, "harbor inspection")
	if lenOf(query, "evidence") != 1 {
		t.Fatalf("document query returned no evidence: %v", query)
	}
	trailID := query["trail_id"].(string)
	segmentID := query["evidence"].([]any)[0].(map[string]any)["segment_id"].(string)
	noteSource, err := streamSource("memory")
	if err != nil {
		t.Fatal(err)
	}

	// Explicit bootstrap; without it the source is denied.
	run.runInput("Denied bootstrap text about harbors.", true, "remember", "--source", "memory")
	run.run(false, "access", "--source", "memory", "--create", "allow")

	// Unsourced note: stdin bytes are stored exactly, no trimming.
	const first = "  Harbor inspection moved to Monday.  \n"
	_, unsourced := run.runInput(first, false, "remember", "--source", "memory")
	unsourcedID := unsourced["id"].(string)
	if unsourced["schema"] != "mousa.note.v1" || unsourced["author"] != "mousa-local.cli" || unsourced["source_id"] != noteSource.ID.String() {
		t.Fatalf("wrong note identity fields: %v", unsourced)
	}
	if lineage, ok := unsourced["derived_from"].([]any); !ok || len(lineage) != 0 {
		t.Fatalf("unsourced note must carry an empty lineage: %v", unsourced)
	}
	decodedID, err := hex.DecodeString(unsourcedID)
	if err != nil || len(decodedID) != 32 {
		t.Fatalf("note id is not a 32-byte hex digest: %v", unsourced)
	}

	// Derived note: --from TRAIL cites every selected segment.
	_, derived := run.run(false, "remember", "--source", "memory", "--from", trailID, "Harbor report reviewed.")
	citations := derived["derived_from"].([]any)
	if len(citations) != 1 || citations[0].(map[string]any)["trail_id"] != trailID {
		t.Fatalf("derived lineage missing cited trail: %v", derived)
	}
	citedSegments := citations[0].(map[string]any)["segment_ids"].([]any)
	if len(citedSegments) != 1 || citedSegments[0] != segmentID {
		t.Fatalf("default citation should cover the selected segment: %v", derived)
	}

	// First writer wins: the same text without citations returns the stored
	// note unchanged instead of a conflict or an update.
	_, again := run.run(false, "remember", "--source", "memory", "Harbor report reviewed.")
	if !reflect.DeepEqual(again, derived) {
		t.Fatalf("retry without lineage rewrote the note: %v vs %v", again, derived)
	}

	// Explicit selection can repeat one segment across distinct trails; each
	// citation is a (trail, segment) pair and both trails must survive.
	_, secondQuery := run.run(false, "query", root, "harbor inspection")
	trail2ID := secondQuery["trail_id"].(string)
	if trail2ID == trailID {
		t.Fatalf("second query did not produce a distinct trail: %v", secondQuery)
	}
	_, explicit := run.run(false, "remember", "--source", "memory",
		"--from", trailID+":"+segmentID, "--from", trail2ID+":"+segmentID, "Inspection confirmed.")
	explicitCitations := explicit["derived_from"].([]any)
	explicitTrails := map[string]bool{}
	for _, raw := range explicitCitations {
		citation := raw.(map[string]any)
		explicitTrails[citation["trail_id"].(string)] = true
		if got := citation["segment_ids"].([]any); len(got) != 1 || got[0] != segmentID {
			t.Fatalf("explicit citation lost the segment: %v", explicit)
		}
	}
	if len(explicitCitations) != 2 || !explicitTrails[trailID] || !explicitTrails[trail2ID] {
		t.Fatalf("explicit note did not retain both cited trails: %v", explicit)
	}

	// B: raw bytes survive, including BOM and CRLF. The note was authored by a
	// separate CLI query/remember invocation, not by document sync.
	const raw = "\ufeffReviewed harbor plan\r\nSecond line.\r\n"
	_, rawNote := run.runInput(raw, false, "remember", "--source", "memory")
	recall := querySource(t, run, "memory", "harbor")
	var rawHit, unsourcedHit map[string]any
	for _, candidate := range hits(recall) {
		hit := candidate.(map[string]any)
		meta := noteHitMeta(t, hit)
		if meta["id"] == rawNote["id"] {
			rawHit = hit
		}
		if meta["id"] == unsourcedID {
			unsourcedHit = hit
		}
	}
	if rawHit == nil {
		t.Fatalf("raw note not retrievable: %v", recall)
	}
	if rawHit["text"] != raw {
		t.Fatalf("note text was normalized on recall: %q want %q", rawHit["text"], raw)
	}
	if rawHit["segment_policy"] != "note-v1" || rawHit["item"] != rawNote["item_id"] {
		t.Fatalf("note evidence lacks its internal policy/item identity: %v", rawHit)
	}
	if rawHit["note"].(map[string]any)["origin"] != "unsourced" {
		t.Fatalf("uncited note must not claim derived origin: %v", rawHit)
	}
	if unsourcedHit == nil || unsourcedHit["text"] != first {
		t.Fatalf("unsourced note recall wrong: %v", unsourcedHit)
	}
	meta := noteHitMeta(t, unsourcedHit)
	if meta["author"] != "mousa-local.cli" || meta["created_at_usec"].(float64) <= 0 || len(staleReasons(meta)) != 0 {
		t.Fatalf("fresh note metadata wrong: %v", meta)
	}
	derivedHit := querySource(t, run, "memory", "reviewed")
	derivedMeta := noteHitMeta(t, noteHitByID(t, derivedHit, derived["id"].(string)))
	if derivedMeta["origin"] != "derived" || len(derivedMeta["derived_from"].([]any)) != 1 {
		t.Fatalf("derived note metadata wrong: %v", derivedMeta)
	}
	if derivedMeta["author"] != "mousa-local.cli" || derivedMeta["created_at_usec"] != derived["created_at_usec"] {
		t.Fatalf("derived note identity changed on recall: %v vs %v", derivedMeta, derived)
	}

	// Deny the cited source: recall still releases the note but flags it.
	run.run(false, "access", root, "deny")
	afterDeny := querySource(t, run, "memory", "harbor report")
	deniedMeta := noteHitMeta(t, noteHitByID(t, afterDeny, derived["id"].(string)))
	staleEntry(t, deniedMeta, "source_denied", trailID)
	run.run(false, "access", root, "allow")
	restoredHit := noteHitByID(t, querySource(t, run, "memory", "harbor report"), derived["id"].(string))
	restoredMeta := noteHitMeta(t, restoredHit)
	if len(staleReasons(restoredMeta)) != 0 {
		t.Fatalf("re-allow did not clear staleness: %v", restoredMeta)
	}
	if restoredHit["text"] != "Harbor report reviewed." || restoredMeta["author"] != "mousa-local.cli" || restoredMeta["created_at_usec"] != derived["created_at_usec"] {
		t.Fatalf("re-allow changed the recalled note: %v vs %v", restoredMeta, derived)
	}

	// Resyncing the cited document retires its segment.
	writeFile(t, root, "doc.md", "The ferret census finished Tuesday.\n")
	run.run(false, "sync", root)
	retiredHit := noteHitByID(t, querySource(t, run, "memory", "harbor report"), derived["id"].(string))
	retiredMeta := noteHitMeta(t, retiredHit)
	if got := staleEntry(t, retiredMeta, "segment_retired", trailID)["segment_id"]; got != segmentID {
		t.Fatalf("retired flag names the wrong segment: %v", retiredMeta)
	}

	// C: withdrawing the cited source keeps the same note and text but
	// reports the withdrawal reason.
	run.run(false, "withdraw", root)
	withdrawnHit := noteHitByID(t, querySource(t, run, "memory", "harbor report"), derived["id"].(string))
	if withdrawnHit["text"] != "Harbor report reviewed." {
		t.Fatalf("withdrawal changed released note text: %v", withdrawnHit)
	}
	withdrawnMeta := noteHitMeta(t, withdrawnHit)
	staleEntry(t, withdrawnMeta, "source_withdrawn", trailID)

	// Denying the memory source itself releases nothing.
	run.run(false, "access", "--source", "memory", "deny")
	denied := querySource(t, run, "memory", "harbor")
	if len(hits(denied)) != 0 {
		t.Fatalf("denied memory released text: %v", denied)
	}
	run.run(false, "access", "--source", "memory", "allow")

	// A budget below one note omits the whole note; a note is never split.
	_, tiny := run.run(false, "query", "--source", "memory", "--budget-bytes", "4", "harbor")
	if lenOf(tiny, "evidence") != 0 {
		t.Fatalf("tiny budget released a partial note: %v", tiny)
	}

	// Exact export: one JSONL object per note. Every canonical field must
	// equal the stored record_json bytes, and text the remembered bytes.
	exported := exportNotes(t, run, "memory")
	if len(exported) != 4 {
		t.Fatalf("export returned %d notes, want 4", len(exported))
	}
	expectedText := map[string]string{
		unsourcedID:             first,
		rawNote["id"].(string):  raw,
		derived["id"].(string):  "Harbor report reviewed.",
		explicit["id"].(string): "Inspection confirmed.",
	}
	db := openRawStore(t, run.store)
	for _, record := range exported {
		id := record["id"].(string)
		text, ok := expectedText[id]
		if !ok {
			t.Fatalf("export returned an unexpected note: %v", record)
		}
		if record["text"] != text {
			t.Fatalf("export altered note %s text: %q want %q", id, record["text"], text)
		}
		rawID, err := hex.DecodeString(id)
		if err != nil {
			t.Fatalf("export id is not hex: %v", record)
		}
		var recordJSON []byte
		if err := db.QueryRow(`SELECT record_json FROM notes WHERE id = ?`, rawID).Scan(&recordJSON); err != nil {
			t.Fatalf("exported note %s missing from the store: %v", id, err)
		}
		var stored map[string]any
		if err := json.Unmarshal(recordJSON, &stored); err != nil {
			t.Fatalf("stored record_json is not an object: %v", err)
		}
		clone := maps.Clone(record)
		delete(clone, "text")
		if !reflect.DeepEqual(clone, stored) {
			t.Fatalf("export record %s differs from its canonical bytes: %v vs %v", id, clone, stored)
		}
	}
}

func TestMemoryRememberRejects(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "doc.md", "The harbor inspection is on Friday.\n")
	writeFile(t, root, "second.md", "Harbor budgets shrink annually.\n")
	run.run(false, "sync", "--allow", root)
	_, query := run.run(false, "query", root, "harbor")
	trailID := query["trail_id"].(string)
	run.run(false, "access", "--source", "memory", "--create", "allow")
	before := memoryCounts(t, run.store)
	reject := func(label string, args ...string) {
		t.Helper()
		now := memoryCounts(t, run.store)
		run.run(true, append([]string{"remember"}, args...)...)
		if got := memoryCounts(t, run.store); !maps.Equal(now, got) {
			t.Fatalf("%s mutated the store: %v -> %v", label, now, got)
		}
	}
	rejectInput := func(label, input string, args ...string) {
		t.Helper()
		now := memoryCounts(t, run.store)
		run.runInput(input, true, append([]string{"remember"}, args...)...)
		if got := memoryCounts(t, run.store); !maps.Equal(now, got) {
			t.Fatalf("%s mutated the store: %v -> %v", label, now, got)
		}
	}

	// Unknown trail.
	reject("unknown trail", "--source", "memory", "--from", strings.Repeat("a", 64), "Ghost lineage.")

	// Denied historical trail: issue a query while denied, then restore access.
	run.run(false, "access", root, "deny")
	_, deniedQuery := run.run(false, "query", root, "harbor")
	deniedTrail := deniedQuery["trail_id"].(string)
	run.run(false, "access", root, "allow")
	reject("denied historical trail", "--source", "memory", "--from", deniedTrail, "Denied history.")

	// Trail that allowed then: the cited source is denied now.
	run.run(false, "access", root, "deny")
	reject("currently denied cited source", "--source", "memory", "--from", trailID, "Currently denied.")
	run.run(false, "access", root, "allow")

	// Cited source withdrawn.
	const streamDoc = "pipeline/docs2"
	jsonlSync(t, run, streamDoc, record("d2", "Harbor ledgers reconcile cleanly."))
	streamQuery := querySource(t, run, streamDoc, "harbor ledgers")
	streamTrail := streamQuery["trail_id"].(string)
	run.run(false, "withdraw", "--source", streamDoc)
	reject("withdrawn cited source", "--source", "memory", "--from", streamTrail, "Withdrawn lineage.")

	// A candidate the trail omitted (budget) cannot be cited.
	_, omittedQuery := run.run(false, "query", "--budget-bytes", "1", root, "harbor")
	omittedTrail := omittedQuery["trail_id"].(string)
	_, inspection := run.run(false, "trail", root, omittedTrail)
	var omittedSegment string
	for _, candidate := range inspection["historical"].(map[string]any)["candidates"].([]any) {
		row := candidate.(map[string]any)
		if row["selected"] == false {
			omittedSegment = row["segment_id"].(string)
		}
	}
	if omittedSegment == "" {
		t.Fatalf("budget-omitted trail has no omitted candidate: %v", inspection)
	}
	reject("unselected segment", "--source", "memory", "--from", omittedTrail+":"+omittedSegment, "Unselected evidence.")

	// A second source's trail cannot cite the first source's segments.
	selected := query["evidence"].([]any)[0].(map[string]any)["segment_id"].(string)
	jsonlSync(t, run, "pipeline/docs3", record("d3", "Harbor cranes idle."))
	other := querySource(t, run, "pipeline/docs3", "harbor cranes")
	reject("cross-source segment", "--source", "memory", "--from", other["trail_id"].(string)+":"+selected, "Cross-source segment.")

	// Malformed citations.
	reject("empty segment list", "--source", "memory", "--from", trailID+":", "Empty segment list.")
	reject("bad trail id", "--source", "memory", "--from", "not-hex", "Bad trail id.")
	reject("bad segment id", "--source", "memory", "--from", trailID+":xyz", "Bad segment id.")
	reject("dup segment", "--source", "memory", "--from", trailID+":"+selected+","+selected, "Dup segment.")
	reject("dup trail", "--source", "memory", "--from", trailID, "--from", trailID, "Dup trail.")

	// Idempotent retry: the stored note wins; one leaf per note.
	const remembered = "Harbor inspections are Fridays."
	_, first := run.run(false, "remember", "--source", "memory", "--from", trailID, remembered)
	_, second := run.run(false, "remember", "--source", "memory", "--from", trailID, remembered)
	if first["id"] != second["id"] || first["created_at_usec"] != second["created_at_usec"] {
		t.Fatalf("retry created a second note: %v vs %v", first, second)
	}
	db := openRawStore(t, run.store)
	var leaves int
	if err := db.QueryRow(`SELECT count(*) FROM log WHERE kind = 'note'`).Scan(&leaves); err != nil || leaves != 1 {
		t.Fatalf("retry wrote %d note leaves, want 1: %v", leaves, err)
	}
	var notes int
	if err := db.QueryRow(`SELECT count(*) FROM notes`).Scan(&notes); err != nil || notes != 1 {
		t.Fatalf("retry wrote %d notes, want 1: %v", notes, err)
	}
	// First writer wins: the same text with different submitted lineage
	// returns the stored note, not a conflict and not an update.
	_, retry := run.run(false, "remember", "--source", "memory", remembered)
	if !reflect.DeepEqual(retry, first) {
		t.Fatalf("first note did not win: %v vs %v", retry, first)
	}
	afterRetry := memoryCounts(t, run.store)
	if afterRetry["notes"] != before["notes"]+1 || afterRetry["note_leaves"] != before["note_leaves"]+1 {
		t.Fatalf("accepted write or retry touched extra rows: %v vs %v", before, afterRetry)
	}

	// Document sources refuse notes, including inactive document items.
	reject("document source", "--source", "pipeline/docs3", "Not a memory source.")
	run.runInput(`{"id":"d3","deleted":true}`+"\n", false, "sync", "--source", "pipeline/docs3")
	reject("inactive document source", "--source", "pipeline/docs3", "Inactive document source.")

	// Document sync refuses note sources, even a no-op empty stream.
	syncNow := memoryCounts(t, run.store)
	run.runInput(record("x", "document text"), true, "sync", "--source", "memory")
	run.runInput("", true, "sync", "--source", "memory")
	if got := memoryCounts(t, run.store); !maps.Equal(syncNow, got) {
		t.Fatalf("sync on note source mutated the store: %v -> %v", syncNow, got)
	}

	// Text validation: invalid UTF-8, empty, and oversize inputs fail.
	rejectInput("invalid utf-8", "\xff\xfe", "--source", "memory")
	rejectInput("empty note", "", "--source", "memory")
	rejectInput("oversize note", strings.Repeat("x", 4097), "--source", "memory")
	reject("two arguments", "--source", "memory", "one", "two")

	// Rollback at note insertion: a trigger rejects the notes row; every
	// earlier write of the transaction (item, receipt, indexes) must vanish.
	rawExec(t, run.store, `CREATE TRIGGER reject_notes BEFORE INSERT ON notes BEGIN SELECT RAISE(ABORT, 'rejected'); END`)
	beforeInsert := memoryCounts(t, run.store)
	run.run(true, "remember", "--source", "memory", "Transactional note.")
	if got := memoryCounts(t, run.store); !maps.Equal(beforeInsert, got) {
		t.Fatalf("failed insert left rows behind: %v -> %v", beforeInsert, got)
	}
	rawExec(t, run.store, `DROP TRIGGER reject_notes`)
	run.run(false, "remember", "--source", "memory", "Transactional note.")
}

func TestMemoryMCP(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("doc", "Cedar harbor plan approved."))

	// Phase A: an agent remembers a fact derived from its own query trail.
	client := startMCPClient(t, run, "agent-1", true)
	run.run(false, "access", "--source", "beta", "--create", "allow")
	query := client.call("mousa_query", mcpQuery("alpha", "harbor", 4096), "")
	trailID := query["trail_id"].(string)
	note := client.call("mousa_remember", map[string]any{
		"source":       "beta",
		"text":         "The harbor plan is approved.",
		"derived_from": []any{map[string]any{"trail_id": trailID}},
	}, "")
	if note["author"] != "agent-1" || note["schema"] != "mousa.note.v1" {
		t.Fatalf("MCP note must carry the trusted caller identity: %v", note)
	}
	citations := note["derived_from"].([]any)
	if len(citations) != 1 || citations[0].(map[string]any)["trail_id"] != trailID {
		t.Fatalf("MCP note lost its lineage: %v", note)
	}
	if len(citations[0].(map[string]any)["segment_ids"].([]any)) == 0 {
		t.Fatalf("default citation did not expand to selected segments: %v", note)
	}
	noteID := note["id"].(string)

	// A document write into the note source is refused.
	client.call("mousa_sync", mcpItems("beta", map[string]any{"id": "doc", "text": "document"}), "conflict")

	// Recall over MCP returns the note metadata on the evidence hit.
	recall := client.call("mousa_query", mcpQuery("beta", "harbor plan", 4096), "")
	if len(hits(recall)) != 1 {
		t.Fatalf("MCP recall missed the note: %v", recall)
	}
	meta := noteHitMeta(t, hits(recall)[0].(map[string]any))
	if meta["id"] != noteID || meta["author"] != "agent-1" || meta["origin"] != "derived" {
		t.Fatalf("MCP note metadata wrong: %v", meta)
	}
	if len(staleReasons(meta)) != 0 {
		t.Fatalf("fresh MCP note reported stale: %v", meta)
	}

	// Rejects: unknown source, unknown trail, malformed lineage.
	client.call("mousa_remember", map[string]any{"source": "gamma", "text": "x"}, "source_not_permitted")
	client.call("mousa_remember", map[string]any{
		"source": "beta", "text": "x",
		"derived_from": []any{map[string]any{"trail_id": strings.Repeat("b", 64)}},
	}, "not_found")
	client.call("mousa_remember", json.RawMessage(`{"source":"beta","text":"x","derived_from":[{"trail_id":`+fmt.Sprintf("%q", trailID)+`,"bogus":1}]}`), "invalid_arguments")
	client.call("mousa_remember", json.RawMessage(`{"source":"beta","text":"x","text":"y"}`), "invalid_arguments")
	client.call("mousa_remember", map[string]any{"source": "beta"}, "invalid_arguments")

	// Phase B: a second agent retries the same text without lineage; the
	// first writer's identity and lineage win, and a fresh process recalls
	// the same note with the same identity.
	client.close()
	client = startMCPClient(t, run, "agent-2", true)
	retry := client.call("mousa_remember", map[string]any{
		"source": "beta",
		"text":   "The harbor plan is approved.",
	}, "")
	if retry["id"] != noteID || retry["author"] != "agent-1" || retry["created_at_usec"] != note["created_at_usec"] {
		t.Fatalf("first note did not win on retry: %v vs %v", retry, note)
	}
	retryLineage := retry["derived_from"].([]any)
	if len(retryLineage) != 1 || retryLineage[0].(map[string]any)["trail_id"] != trailID {
		t.Fatalf("retry dropped the stored lineage: %v", retry)
	}
	persisted := client.call("mousa_query", mcpQuery("beta", "harbor plan", 4096), "")
	persistedHit := noteHitByID(t, persisted, noteID)
	persistedMeta := noteHitMeta(t, persistedHit)
	if persistedMeta["author"] != "agent-1" || persistedMeta["created_at_usec"] != note["created_at_usec"] || persistedHit["text"] != "The harbor plan is approved." {
		t.Fatalf("restart changed note identity: %v vs %v", persistedMeta, note)
	}

	// Phase C: a third caller sees the same evidence after the operator
	// withdraws the cited source, flagged with a withdrawal reason naming
	// the trail. A non-ingestion client still cannot write notes.
	client.close()
	run.run(false, "withdraw", "--source", "alpha")
	client = startMCPClient(t, run, "agent-3", false)
	client.call("mousa_remember", map[string]any{"source": "beta", "text": "unauthorized"}, "ingestion_not_permitted")
	withdrawn := client.call("mousa_query", mcpQuery("beta", "harbor plan", 4096), "")
	withdrawnHit := noteHitByID(t, withdrawn, noteID)
	if withdrawnHit["text"] != "The harbor plan is approved." {
		t.Fatalf("withdrawal changed released note text: %v", withdrawnHit)
	}
	withdrawnMeta := noteHitMeta(t, withdrawnHit)
	if withdrawnMeta["author"] != "agent-1" || withdrawnMeta["created_at_usec"] != note["created_at_usec"] {
		t.Fatalf("withdrawal changed note identity: %v vs %v", withdrawnMeta, note)
	}
	withdrawnLineage := withdrawnMeta["derived_from"].([]any)
	if len(withdrawnLineage) != 1 || withdrawnLineage[0].(map[string]any)["trail_id"] != trailID {
		t.Fatalf("withdrawal changed note lineage: %v", withdrawnMeta)
	}
	staleEntry(t, withdrawnMeta, "source_withdrawn", trailID)
	client.close()
}

func TestMemoryIntegrityTamper(t *testing.T) {
	mutations := []struct {
		name      string
		statement string
	}{
		{"canonical record bytes", `UPDATE notes SET record_json = X'00'`},
		{"citation lookup row deleted", `DELETE FROM note_citations`},
		{"citation lookup row falsified", `UPDATE note_citations SET citation_ordinal = citation_ordinal + 9`},
		{"log leaf hash", `UPDATE log SET leaf_hash = randomblob(32) WHERE kind = 'note'`},
		{"log leaf source", `UPDATE log SET source_id = (SELECT id FROM sources WHERE id != log.source_id LIMIT 1) WHERE kind = 'note'`},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			run, root := setup(t)
			writeFile(t, root, "doc.md", "Harbor seals bask at noon.\n")
			run.run(false, "sync", "--allow", root)
			_, query := run.run(false, "query", root, "harbor seals")
			run.run(false, "access", "--source", "memory", "--create", "allow")
			run.run(false, "remember", "--source", "memory", "--from", query["trail_id"].(string), "Seals at noon.")

			// The store stays open while a second connection tampers; the
			// very next read must fail, not serve the altered bytes.
			client := startMCPClient(t, run, "cli", false, "--source", "memory")
			rawExec(t, run.store, mutation.statement)
			recall := client.call("mousa_query", mcpQuery("memory", "seals", 4096), "integrity")
			if recall["message"] == nil {
				t.Fatalf("tampered note read did not report integrity: %v", recall)
			}
			client.close()

			// A fresh open performs full verification and refuses too.
			run.run(true, "notes", "export", "--source", "memory")
			run.run(true, "status", "--source", "memory")
		})
	}
}

package main

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func documentPassage(t *testing.T, report map[string]any) map[string]any {
	t.Helper()
	passages := hits(report)
	if len(passages) != 1 {
		t.Fatalf("document query returned %d passages, want 1: %v", len(passages), report)
	}
	passage, ok := passages[0].(map[string]any)
	if !ok {
		t.Fatalf("document passage is not an object: %v", passages[0])
	}
	return passage
}

func passageActivation(t *testing.T, passage map[string]any) int64 {
	t.Helper()
	raw, ok := passage["activated_at_usec"].(float64)
	if !ok || raw <= 0 {
		t.Fatalf("document passage lacks positive activated_at_usec: %v", passage)
	}
	return int64(raw)
}

func documentActivation(t *testing.T, report map[string]any) int64 {
	t.Helper()
	return passageActivation(t, documentPassage(t, report))
}

func compactDocument(t *testing.T, raw string) map[string]any {
	t.Helper()
	var reply struct {
		Passages []map[string]any `json:"passages"`
	}
	if err := json.Unmarshal([]byte(raw), &reply); err != nil {
		t.Fatalf("compact query output is not JSON: %v\n%s", err, raw)
	}
	if len(reply.Passages) != 1 {
		t.Fatalf("compact query returned %d passages, want 1: %s", len(reply.Passages), raw)
	}
	return reply.Passages[0]
}

func requireDocumentDate(t *testing.T, run testRun, source, query, path string, usec int64) {
	t.Helper()
	raw, _ := run.run(false, "query", "--source", source, query)
	passage := compactDocument(t, raw)
	wantDate := time.UnixMicro(usec).Local().Format(time.DateOnly)
	if passage["path"] != path || passage["kind"] != "document" || passage["date"] != wantDate {
		t.Fatalf("compact document passage = %v, want path %q kind document date %q", passage, path, wantDate)
	}
}

func TestDocumentActivationDates(t *testing.T) {
	run, _ := setup(t)
	const source = "dated-docs"
	const item = "docs/cedar"
	syncText := func(text string) (before, after int64) {
		before = time.Now().UnixMicro()
		run.runInput(record(item, text)+"\n", false, "sync", "--source", source, "--allow")
		after = time.Now().UnixMicro()
		return before, after
	}
	activation := func() int64 {
		t.Helper()
		_, full := run.run(false, "query", "--full", "--source", source, "cedar")
		return documentActivation(t, full)
	}
	requireFresh := func(usec, before, after int64) {
		t.Helper()
		if usec < before || usec > after {
			t.Fatalf("activation time %d outside sync bounds [%d, %d]", usec, before, after)
		}
	}

	before, after := syncText("Cedar evidence original.")
	original := activation()
	requireFresh(original, before, after)
	requireDocumentDate(t, run, source, "cedar", item, original)

	syncText("Cedar evidence original.")
	unchanged := activation()
	if unchanged != original {
		t.Fatalf("unchanged sync changed activation time: %d -> %d", original, unchanged)
	}

	run.run(false, "move", "--source", source, "--item", item, "--to", "docs/moved")
	moved := activation()
	if moved != original {
		t.Fatalf("path move changed activation time: %d -> %d", original, moved)
	}
	_, movedFull := run.run(false, "query", "--full", "--source", source, "cedar")
	if path := documentPassage(t, movedFull)["path"]; path != "docs/moved" {
		t.Fatalf("moved passage path = %v, want docs/moved", path)
	}
	requireDocumentDate(t, run, source, "cedar", "docs/moved", original)

	time.Sleep(time.Millisecond)
	before, after = syncText("Cedar evidence second.")
	second := activation()
	requireFresh(second, before, after)
	if second <= original {
		t.Fatalf("updated activation time %d is not after original %d", second, original)
	}
	requireDocumentDate(t, run, source, "second", "docs/moved", second)

	time.Sleep(time.Millisecond)
	before, after = syncText("Cedar evidence original.")
	restored := activation()
	requireFresh(restored, before, after)
	if restored <= second {
		t.Fatalf("restored activation time %d is not after update %d", restored, second)
	}
	requireDocumentDate(t, run, source, "cedar", "docs/moved", restored)

	run.runInput(`{"id":"docs/cedar","deleted":true}`+"\n", false, "sync", "--source", source)
	if passages := hits(querySource(t, run, source, "cedar")); len(passages) != 0 {
		t.Fatalf("deleted document still returned: %v", passages)
	}

	time.Sleep(time.Millisecond)
	before, after = syncText("Cedar evidence original.")
	readded := activation()
	requireFresh(readded, before, after)
	if readded <= restored {
		t.Fatalf("re-added activation time %d is not after restoration %d", readded, restored)
	}
	requireDocumentDate(t, run, source, "cedar", "docs/moved", readded)
	run.run(false, "verify")
}

func TestServedDocumentActivationDate(t *testing.T) {
	run, _ := setup(t)
	run.run(false, "access", "--source", "served-docs", "--create", "allow")
	writerToken := rand.Text()
	config := fmt.Sprintf(`{"principals":[{"id":"writer","token_sha256":%q,"read_sources":["served-docs"],"write_sources":["served-docs"]}]}`, tokenHash(writerToken))
	configPath := filepath.Join(t.TempDir(), "serve-config.json")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := startDaemon(t, run.store, configPath)

	before := time.Now().UnixMicro()
	status, _, data := postNative(t, daemon.url+"/v1/documents", writerToken,
		[]byte(`{"source":"served-docs","record":{"id":"docs/served","text":"Cedar evidence served."}}`))
	after := time.Now().UnixMicro()
	if status != 200 {
		t.Fatalf("served document write = %d: %s", status, data)
	}

	code, full, stderr := runEndpoint(t, daemon.url, writerToken, "", "query", "--full", "--source", "served-docs", "cedar")
	if code != 0 {
		t.Fatalf("served full query failed: %d %s", code, stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(full), &report); err != nil {
		t.Fatalf("served full query is not JSON: %v\n%s", err, full)
	}
	activated := documentActivation(t, report)
	if activated < before || activated > after {
		t.Fatalf("served activation time %d outside write bounds [%d, %d]", activated, before, after)
	}
	if path := documentPassage(t, report)["path"]; path != "docs/served" {
		t.Fatalf("served passage path = %v, want docs/served", path)
	}

	code, compact, stderr := runEndpoint(t, daemon.url, writerToken, "", "query", "--source", "served-docs", "cedar")
	if code != 0 {
		t.Fatalf("served compact query failed: %d %s", code, stderr)
	}
	passage := compactDocument(t, compact)
	wantDate := time.UnixMicro(activated).Local().Format(time.DateOnly)
	if passage["path"] != "docs/served" || passage["kind"] != "document" || passage["date"] != wantDate {
		t.Fatalf("served compact document passage = %v, want path docs/served kind document date %q", passage, wantDate)
	}
}

func schema9Fixture(t *testing.T) string {
	t.Helper()
	compressed, err := os.Open(filepath.Join("testdata", "schema9_fixture.sqlite.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	reader, err := gzip.NewReader(compressed)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "schema9.sqlite")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func schema9RestoredActivationProof(t *testing.T, path string) (finishedAtUsec int64, trailID string) {
	t.Helper()
	db := openRawStore(t, path)
	if err := db.QueryRow(`SELECT json_extract(CAST(o.record_json AS TEXT), '$.finished_at_usec')
		FROM log cl
		JOIN item_changes c ON c.id = cl.record_id
		JOIN log ol ON ol.seq = cl.seq + 1 AND ol.kind = 'request_outcome'
		JOIN request_outcomes o ON o.id = ol.record_id
		WHERE cl.kind = 'item_change'
		  AND json_extract(CAST(c.record_json AS TEXT), '$.item_id') = 'docs/served'
		ORDER BY cl.seq DESC LIMIT 1`).Scan(&finishedAtUsec); err != nil {
		t.Fatalf("read schema-9 activation proof: %v", err)
	}
	if finishedAtUsec <= 0 {
		t.Fatalf("schema-9 fixture has no positive restored activation proof: %d", finishedAtUsec)
	}
	var rawTrail []byte
	if err := db.QueryRow(`SELECT t.id FROM trails t JOIN log l ON l.kind = 'trail' AND l.record_id = t.id
		ORDER BY l.seq DESC LIMIT 1`).Scan(&rawTrail); err != nil {
		t.Fatalf("read schema-9 trail: %v", err)
	}
	trailID = hex.EncodeToString(rawTrail)
	return finishedAtUsec, trailID
}

func TestMigrateSchema9DocumentDates(t *testing.T) {
	fixture := schema9Fixture(t)
	run := testRun{t: t, binary: cliBinary, store: fixture}
	wantActivated, trailID := schema9RestoredActivationProof(t, fixture)

	tables := []string{"item_changes", "log", "notes", "note_citations", "trails", "request_admissions", "request_outcomes"}
	before := make(map[string]struct {
		count  int
		digest string
	})
	for _, table := range tables {
		count, digest := tableDigest(t, fixture, table)
		before[table] = struct {
			count  int
			digest string
		}{count, digest}
	}

	_, migrated := run.run(false, "migrate")
	if migrated["schema"] != float64(10) {
		t.Fatalf("migrate = %v, want schema 10", migrated)
	}
	run.run(false, "verify")
	run.run(false, "verify", "--trail", trailID)

	for _, table := range tables {
		count, digest := tableDigest(t, fixture, table)
		want := before[table]
		if count != want.count || digest != want.digest {
			t.Fatalf("%s changed across migration: %d rows %s -> %d rows %s", table, want.count, want.digest, count, digest)
		}
	}

	_, unknownFull := run.run(false, "query", "--full", "--source", "fixture-docs", "unknown")
	if _, present := documentPassage(t, unknownFull)["activated_at_usec"]; present {
		t.Fatalf("offline schema-9 document gained an unproven activation time: %v", unknownFull)
	}
	raw, _ := run.run(false, "query", "--source", "fixture-docs", "unknown")
	if _, present := compactDocument(t, raw)["date"]; present {
		t.Fatalf("offline schema-9 document gained a compact date: %s", raw)
	}

	_, servedFull := run.run(false, "query", "--full", "--source", "fixture-docs", "restored")
	if got := documentActivation(t, servedFull); got != wantActivated {
		t.Fatalf("restored schema-9 activation = %d, want proven outcome time %d", got, wantActivated)
	}
	raw, _ = run.run(false, "query", "--source", "fixture-docs", "restored")
	passage := compactDocument(t, raw)
	wantDate := time.UnixMicro(wantActivated).Local().Format(time.DateOnly)
	if passage["path"] != "docs/served" || passage["kind"] != "document" || passage["date"] != wantDate {
		t.Fatalf("migrated compact document passage = %v, want path docs/served kind document date %q", passage, wantDate)
	}
}

func TestVerifyDocumentActivationEvidence(t *testing.T) {
	run, _ := setup(t)
	const source = "tamper-docs"
	const item = "docs/tamper"
	jsonlSync(t, run, source, record(item, "Cedar evidence original.")+"\n")
	_, initial := run.run(false, "query", "--full", "--source", source, "cedar")
	documentActivation(t, initial)
	jsonlSync(t, run, source, record(item, "Cedar evidence replacement.")+"\n")
	jsonlSync(t, run, source, record(item, "Cedar evidence original.")+"\n")
	run.run(false, "verify", "--log")

	db := openRawStore(t, run.store)
	var earlier, restored int64
	if err := db.QueryRow(`SELECT min(l.seq), max(l.seq)
		FROM log l JOIN item_changes i ON i.id = l.record_id
		WHERE l.kind = 'item_change' AND json_extract(CAST(i.record_json AS TEXT), '$.item_id') = ?
		  AND json_type(CAST(i.record_json AS TEXT), '$.current') = 'text'
		GROUP BY json_extract(CAST(i.record_json AS TEXT), '$.current') HAVING count(*) = 2`, item).
		Scan(&earlier, &restored); err != nil {
		t.Fatalf("read reused activation seqs: %v", err)
	}
	rawExec(t, run.store, fmt.Sprintf(`UPDATE local_items SET activation_seq = %d WHERE item_id = %q`, earlier, item))
	if stderr, _ := run.run(true, "verify", "--log"); !strings.Contains(stderr, "integrity") {
		t.Fatalf("verify --log accepted an earlier activation pointer: %q", stderr)
	}

	rawExec(t, run.store, fmt.Sprintf(`UPDATE local_items SET activation_seq = NULL WHERE item_id = %q`, item))
	if stderr, _ := run.run(true, "verify", "--log"); !strings.Contains(stderr, "integrity") {
		t.Fatalf("verify --log accepted a lost activation pointer: %q", stderr)
	}
	rawExec(t, run.store, fmt.Sprintf(`UPDATE local_items SET activation_seq = %d WHERE item_id = %q`, restored, item))
	run.run(false, "verify", "--log")

	rawExec(t, run.store, fmt.Sprintf(`UPDATE item_changes SET record_json = CAST(json_set(CAST(record_json AS TEXT),
		'$.activated_at_usec', json_extract(CAST(record_json AS TEXT), '$.activated_at_usec') + 1) AS BLOB)
		WHERE id = (SELECT record_id FROM log WHERE seq = %d)`, restored))
	if stderr, _ := run.run(true, "verify", "--log"); !strings.Contains(stderr, "integrity") {
		t.Fatalf("verify --log accepted a changed canonical activation time: %q", stderr)
	}
}

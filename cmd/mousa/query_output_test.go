package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

func compactTrailID(t *testing.T, raw string) string {
	t.Helper()
	var reply struct {
		Trail string `json:"trail"`
	}
	if err := json.Unmarshal([]byte(raw), &reply); err != nil || reply.Trail == "" {
		t.Fatalf("compact query output does not start from a trail string: %v\n%s", err, raw)
	}
	return reply.Trail
}

func noteFixtureDate(t *testing.T, note map[string]any) string {
	t.Helper()
	usec, ok := note["created_at_usec"].(float64)
	if !ok || usec <= 0 {
		t.Fatalf("note fixture has no positive created_at_usec: %v", note)
	}
	return time.UnixMicro(int64(usec)).Local().Format(time.DateOnly)
}

func exactCompactLine(t *testing.T, raw, expected string) string {
	t.Helper()
	trail := compactTrailID(t, raw)
	expected = fmt.Sprintf(expected, trail)
	if raw != expected {
		t.Fatalf("compact query output mismatch:\n got: %q\nwant: %q", raw, expected)
	}
	return trail
}

func TestCompactQueryLocal(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "doc.md", "Cedar trail reference.")
	run.run(false, "sync", "--allow", root)
	seedRaw, _ := run.run(false, "query", root, "cedar")
	docTrail := compactTrailID(t, seedRaw)
	run.run(false, "access", "--source", "notes", "--create", "allow")
	_, original := run.run(false, "remember", "--source", "notes", "--path", "notes/cedar", "--from", docTrail, "Cedar note original.")
	oldID := noteIDOf(t, original)
	oldDate := noteFixtureDate(t, original)

	// Retiring the cited document segment makes the original note stale.
	writeFile(t, root, "doc.md", "Cedar trail replacement.")
	run.run(false, "sync", root)
	_, updated := run.run(false, "remember", "--source", "notes", "--path", "notes/cedar", "--supersedes", oldID, "Cedar note updated.")
	updatedID := noteIDOf(t, updated)
	updatedDate := noteFixtureDate(t, updated)

	raw, _ := run.run(false, "query", "--source", "notes", "--include-superseded", "original")
	trail := exactCompactLine(t, raw, `{"trail":"%s","passages":[{"path":"notes/cedar","date":"`+oldDate+`","kind":"note","author":"mousa-local.cli","note":"`+oldID+`","text":"Cedar note original.","stale":true,"superseded":true}]}`+"\n")
	run.run(false, "trail", trail)

	raw, _ = run.run(false, "query", "--source", "notes", "updated")
	exactCompactLine(t, raw, `{"trail":"%s","passages":[{"path":"notes/cedar","date":"`+updatedDate+`","kind":"note","author":"mousa-local.cli","note":"`+updatedID+`","text":"Cedar note updated."}]}`+"\n")

	raw, _ = run.run(false, "query", root, "replacement")
	exactCompactLine(t, raw, `{"trail":"%s","passages":[{"path":"doc.md","kind":"document","text":"Cedar trail replacement."}]}`+"\n")

	run.run(false, "access", "--source", "notes", "deny")
	raw, _ = run.run(false, "query", "--source", "notes", "updated")
	exactCompactLine(t, raw, `{"trail":"%s","passages":[],"access":"deny"}`+"\n")
	run.run(false, "access", "--source", "notes", "allow")

	raw, _ = run.run(false, "query", "--source", "notes", "--source", "absent", "no-such-term")
	exactCompactLine(t, raw, `{"trail":"%s","passages":[]}`+"\n")

	run.run(false, "collection", "create", "empty")
	raw, _ = run.run(false, "query", "--collection", "empty", "no-such-term")
	exactCompactLine(t, raw, `{"trail":"%s","passages":[]}`+"\n")
}

func TestFullQueryLocal(t *testing.T) {
	run, _ := setup(t)
	const external = "docs"
	const item = "full.md"
	const text = "Full output fixture evidence."
	jsonlSync(t, run, external, record(item, text)+"\n")
	full, reply := run.run(false, "query", "--full", "--source", external, "fixture")
	trailRaw, _ := run.run(false, "trail", trailIDOf(reply))

	source, err := streamSource(external)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(t.Context(), run.store)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	expected, err := store.Query(t.Context(), mousa.QueryRequest{
		Scope:  mousa.Scope{Source: source.ID},
		Texts:  []string{"fixture"},
		Mode:   mousa.QueryLexical,
		Budget: mousa.Budget{Unit: mousa.BudgetBytes, Limit: 8192},
		Caller: accessActor,
	})
	if err != nil {
		t.Fatal(err)
	}
	expectedBytes, err := json.MarshalIndent(struct {
		Trail    json.RawMessage `json:"trail"`
		Passages []mousa.Passage `json:"passages"`
	}{Trail: json.RawMessage(strings.TrimSpace(trailRaw)), Passages: expected.Passages}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if want := string(expectedBytes) + "\n"; full != want {
		t.Fatalf("full query output mismatch:\n got:\n%s\nwant:\n%s", full, want)
	}
}

func endpointFixtureID(char string) string {
	return strings.Repeat(char, 64)
}

func endpointQueryResultFixture() string {
	id := endpointFixtureID
	return `{"trail":{"future_trail_field":"kept","candidates":[{"content_sha256":"` + id("6") + `","cost":20,"extracts":[{"range":{"end":20,"start":0},"sha256":"` + id("6") + `"}],"packed":"full","range":{"end":20,"start":0},"rank":1,"representation_id":"` + id("7") + `","scores":[{"bm25":{"frequencies":[1],"length":4},"branch":0,"level":"block","rank":1,"value_bits":"0000000000000000"}],"segment_id":"` + id("8") + `","source_ids":["` + id("3") + `"]}],"id":"` + id("1") + `","packing":{"policy":"first-fit-v1","used":20},"request":{"budget":{"limit":8192,"unit":"bytes"},"caller":"writer","include_superseded":true,"limit":100,"mode":"lexical","purpose":"fixture","request_id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","requested_at_usec":1700000000000000,"scope":{"kind":"source","sources":["` + id("3") + `"]},"texts":["cedar"]},"result_sha256":"` + id("2") + `","retrieval":{"branches":[{"kind":"lexical","proposed":1,"scoring":"bm25-shared-v1","term_policy":"original","terms":["cedar"],"text":0}]},"schema":"mousa.trail.v1","sources":[{"access":"allow","access_event_id":"` + id("4") + `","head":{"leaf_sha256":"` + id("5") + `"},"outcome":"allow","reasons":["allow"],"source_id":"` + id("3") + `"}],"store_id":"store-fixture","supersession":[{"note_id":"` + id("a") + `","segment_id":"` + id("8") + `","superseded_by":["` + id("b") + `"]}]},"passages":[{"source_id":"` + id("3") + `","item":"cedar-note","segment_id":"` + id("8") + `","representation_id":"` + id("7") + `","representation_sha256":"` + id("9") + `","segment_policy":"note-v1","rank":1,"range":{"end":20,"start":0},"extract":false,"content_sha256":"` + id("6") + `","text":"Cedar \u003c\u0026\u003e note text.","path":"notes/cedar","note":{"id":"` + id("a") + `","author":"writer","created_at_usec":1700006400000000,"derived_from":[{"segment_ids":["` + id("d") + `"],"trail_id":"` + id("c") + `"}],"origin":"derived","stale":[{"trail_id":"` + id("c") + `","segment_id":"` + id("d") + `","reason":"segment_retired"}],"superseded_by":["` + id("b") + `"]}}],"future_result_field":"kept"}`
}

func indentedFixture(t *testing.T, compact string) string {
	t.Helper()
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(compact), "", "  "); err != nil {
		t.Fatal(err)
	}
	out.WriteByte('\n')
	return out.String()
}

func TestCompactAndFullQueryEndpoint(t *testing.T) {
	t.Setenv("TZ", "America/Los_Angeles")
	result := endpointQueryResultFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/query" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"request_id":"AAAAAAAAAAAAAAAAAAAAAAAAAA","result":` + result + `}`))
	}))
	defer server.Close()

	code, full, stderr := runEndpoint(t, server.URL, "fixture-token", "", "query", "--full", "--source", "notes", "cedar")
	if code != 0 {
		t.Fatalf("endpoint full query failed: %d %s", code, stderr)
	}
	if want := indentedFixture(t, result); full != want {
		t.Fatalf("endpoint full query mismatch:\n got:\n%s\nwant:\n%s", full, want)
	}

	code, compact, stderr := runEndpoint(t, server.URL, "fixture-token", "", "query", "--source", "notes", "cedar")
	if code != 0 {
		t.Fatalf("endpoint compact query failed: %d %s", code, stderr)
	}
	want := `{"trail":"` + endpointFixtureID("1") + `","passages":[{"path":"notes/cedar","date":"2023-11-14","kind":"note","author":"writer","note":"` + endpointFixtureID("a") + `","text":"Cedar <&> note text.","stale":true,"superseded":true}]}` + "\n"
	if compact != want {
		t.Fatalf("endpoint compact query mismatch:\n got: %q\nwant: %q", compact, want)
	}
}

package main

import (
	"fmt"
	"strings"
	"testing"
)

// The JSONL stream tests run the built CLI against a fresh store and exercise
// the stream input form through the supported interface: identity by record
// id, digest-driven replay, revision replacement, explicit deletion, malformed
// records, partial failure, and status reporting.

func record(id, text string) string {
	return fmt.Sprintf(`{"id":%q,"text":%q}`, id, text)
}

func jsonlSync(t *testing.T, run testRun, sourceID, stream string) map[string]any {
	t.Helper()
	_, report := run.runInput(stream, false, "sync", "--source", sourceID)
	return report
}

func querySource(t *testing.T, run testRun, sourceID, query string) map[string]any {
	t.Helper()
	_, report := run.runInput("", false, "query", "--source", sourceID, query)
	return report
}

func hits(report map[string]any) []any {
	if evidence, ok := report["evidence"].([]any); ok {
		return evidence
	}
	return nil
}

func firstItem(t *testing.T, report map[string]any) string {
	t.Helper()
	evidence := hits(report)
	if len(evidence) == 0 {
		t.Fatalf("query returned no evidence: %v", report)
	}
	item, _ := evidence[0].(map[string]any)["item"].(string)
	return item
}

func TestJSONLStreamImportsRecordsAndRetrievesProvenance(t *testing.T) {
	run, _ := setup(t)
	const source = "pipeline/notes"
	stream := strings.Join([]string{
		record("doc-1", "Walruses gather on the northern shore."),
		record("doc-2", "Quokkas prefer dense brush at dusk."),
		record("doc-3", ""),
	}, "\n") + "\n"
	report := jsonlSync(t, run, source, stream)
	if got := report["input"].(string); got != "jsonl" {
		t.Fatalf("sync input = %q, want jsonl", got)
	}
	if got := lenOf(report, "added"); got != 3 {
		t.Fatalf("import should report 3 added, got %d: %v", got, report)
	}
	if got := report["total_items"].(float64); got != 3 {
		t.Fatalf("total_items = %v, want 3", got)
	}
	found := querySource(t, run, source, "walruses")
	if item := firstItem(t, found); item != "doc-1" {
		t.Fatalf("provenance item = %q, want doc-1", item)
	}
}

func TestJSONLStreamResyncOfSameRecordsIsNoOp(t *testing.T) {
	run, _ := setup(t)
	const source = "pipeline/replay"
	stream := strings.Join([]string{
		record("a", "Replay safety depends on content digests."),
		record("b", "The second record mentions pangolins."),
	}, "\n") + "\n"
	jsonlSync(t, run, source, stream)
	replay := jsonlSync(t, run, source, stream)
	if got := lenOf(replay, "unchanged"); got != 2 {
		t.Fatalf("replay unchanged = %d, want 2: %v", got, replay)
	}
	if lenOf(replay, "added") != 0 || lenOf(replay, "updated") != 0 || lenOf(replay, "deleted") != 0 {
		t.Fatalf("replay reported changes: %v", replay)
	}
}

func TestJSONLStreamChangedTextReplacesRetrievableContent(t *testing.T) {
	run, _ := setup(t)
	const source = "pipeline/updates"
	jsonlSync(t, run, source, record("a", "The first revision mentions walruses."))
	jsonlSync(t, run, source, record("a", "The second revision mentions ferrets."))
	stale := querySource(t, run, source, "walruses")
	if len(hits(stale)) != 0 {
		t.Fatalf("old revision still retrievable: %v", stale)
	}
	fresh := querySource(t, run, source, "ferrets")
	if item := firstItem(t, fresh); item != "a" {
		t.Fatalf("provenance item = %q, want a", item)
	}
}

func TestJSONLStreamTombstoneRemovesContentAndReaddRestoresIt(t *testing.T) {
	run, _ := setup(t)
	const source = "pipeline/lifecycle"
	content := record("doc-9", "Zebra herds cross the flooded plain each spring.")
	jsonlSync(t, run, source, content)
	jsonlSync(t, run, source, `{"id":"doc-9","deleted":true}`)
	if len(hits(querySource(t, run, source, "zebra"))) != 0 {
		t.Fatalf("tombstoned content still retrievable")
	}
	// Re-adding the same record restores retrievability instead of silently
	// leaving a stored-but-unindexed revision behind.
	restored := jsonlSync(t, run, source, content)
	if got := lenOf(restored, "restored"); got != 1 {
		t.Fatalf("re-add should report restored, got %d: %v", got, restored)
	}
	if item := firstItem(t, querySource(t, run, source, "zebra")); item != "doc-9" {
		t.Fatalf("restored content not retrievable under doc-9")
	}
}

func TestJSONLStreamTombstoneForUnknownItemIsReportedAbsent(t *testing.T) {
	run, _ := setup(t)
	report := jsonlSync(t, run, "pipeline/absent", `{"id":"ghost","deleted":true}`+"\n")
	if got := lenOf(report, "absent"); got != 1 {
		t.Fatalf("unknown tombstone should be reported absent, got %d: %v", got, report)
	}
	if report["total_items"].(float64) != 1 {
		t.Fatalf("tombstone should count as one item: %v", report)
	}
}

func TestJSONLStreamRejectsInvalidRecords(t *testing.T) {
	cases := []struct {
		name    string
		stream  string
		wantErr []string
	}{
		{
			name:    "not json",
			stream:  "hello\n",
			wantErr: []string{"line 1"},
		},
		{
			name:    "unknown field",
			stream:  `{"id":"a","text":"x","extra":1}` + "\n",
			wantErr: []string{"line 1", "extra"},
		},
		{
			name:    "missing id",
			stream:  `{"text":"x"}` + "\n",
			wantErr: []string{"line 1", "id"},
		},
		{
			name:    "empty id",
			stream:  `{"id":"","text":"x"}` + "\n",
			wantErr: []string{"line 1", "id"},
		},
		{
			name:    "duplicate field",
			stream:  `{"id":"first","id":"second","text":"x"}`,
			wantErr: []string{"line 1"},
		},
		{
			name:    "unpaired Unicode surrogate",
			stream:  `{"id":"\ud800","text":"x"}`,
			wantErr: []string{"line 1"},
		},
		{
			name:    "null deletion",
			stream:  `{"id":"a","deleted":null}`,
			wantErr: []string{"line 1"},
		},
		{
			name:    "null text",
			stream:  `{"id":"a","text":null}`,
			wantErr: []string{"line 1"},
		},
		{
			name:    "text without deleted flag",
			stream:  `{"id":"a"}` + "\n",
			wantErr: []string{"line 1", "text"},
		},
		{
			name:    "deleted record carrying text",
			stream:  `{"id":"a","text":"x","deleted":true}` + "\n",
			wantErr: []string{"line 1", "text"},
		},
		{
			name:    "invalid utf-8",
			stream:  "{\"id\":\"a\",\"text\":\"\xff\xfe\"}\n",
			wantErr: []string{"line 1", "UTF-8"},
		},
		{
			name:    "trailing data on one line",
			stream:  `{"id":"a","text":"x"} {"id":"b","text":"y"}` + "\n",
			wantErr: []string{"line 1", "trailing"},
		},
		{
			name:    "duplicate item id",
			stream:  record("a", "first") + "\n" + record("a", "second") + "\n",
			wantErr: []string{"line 2", "already named on line 1"},
		},
		{
			name:    "error names the offending line",
			stream:  record("a", "fine") + "\n" + "\n" + "{oops}\n",
			wantErr: []string{"line 3"},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			run, _ := setup(t)
			out, _ := run.runInput(test.stream, true, "sync", "--source", "pipeline/invalid")
			for _, fragment := range test.wantErr {
				if !strings.Contains(out, fragment) {
					t.Fatalf("stderr %q does not contain %q", out, fragment)
				}
			}
		})
	}
}

func TestJSONLStreamPartialFailureKeepsCommittedPrefixAndReplayCompletes(t *testing.T) {
	run, _ := setup(t)
	const source = "pipeline/partial"
	broken := strings.Join([]string{
		record("doc-1", "Walruses arrive first."),
		record("doc-2", "Quokkas follow the walruses."),
		"{not a record}",
	}, "\n") + "\n"
	out, _ := run.runInput(broken, true, "sync", "--source", source)
	if !strings.Contains(out, "line 3") {
		t.Fatalf("failure should name line 3, got %q", out)
	}
	// The prefix stays committed and retrievable.
	if item := firstItem(t, querySource(t, run, source, "walruses")); item != "doc-1" {
		t.Fatalf("committed prefix lost: %v", out)
	}
	out, _ = run.runInput(broken, true, "sync", "--source", source)
	if !strings.Contains(out, "line 3") {
		t.Fatalf("replay should fail on line 3 again, got %q", out)
	}
	// A repaired stream completes the remainder.
	fixed := strings.Replace(broken, "{not a record}", record("doc-3", "Ferrets arrive last."), 1)
	final := jsonlSync(t, run, source, fixed)
	if got := lenOf(final, "added"); got != 1 {
		t.Fatalf("repaired stream should add only doc-3, got %d: %v", got, final)
	}
	if got := lenOf(final, "unchanged"); got != 2 {
		t.Fatalf("repaired stream should leave the prefix unchanged, got %d: %v", got, final)
	}
	if item := firstItem(t, querySource(t, run, source, "ferrets")); item != "doc-3" {
		t.Fatalf("repaired record not retrievable")
	}
}

func TestJSONLStreamStatusReportsSourceState(t *testing.T) {
	run, _ := setup(t)
	const source = "pipeline/status"
	_, status := run.runInput("", false, "status", "--source", source)
	if status["collection_state"] != "absent" {
		t.Fatalf("unknown source status = %v, want absent", status)
	}
	jsonlSync(t, run, source, strings.Join([]string{
		record("a", "one"),
		record("b", "two"),
	}, "\n")+"\n")
	_, status = run.runInput("", false, "status", "--source", source)
	if status["collection_state"] != "active" {
		t.Fatalf("status collection_state = %v, want active", status)
	}
	if got := status["observations"].(float64); got != 2 {
		t.Fatalf("observations = %v, want 2", got)
	}
}

func TestJSONLSourceCannotCollideWithDirectoryPath(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "doc", "Directory walruses.")
	run.run(false, "sync", root)
	jsonlSync(t, run, root, record("doc@draft", "Stream ferrets."))
	_, directory := run.run(false, "query", root, "walruses ferrets")
	if firstItem(t, directory) != "doc" || len(hits(directory)) != 1 {
		t.Fatalf("directory source mixed with stream source: %v", directory)
	}
	stream := querySource(t, run, root, "walruses ferrets")
	if firstItem(t, stream) != "doc@draft" || len(hits(stream)) != 1 {
		t.Fatalf("stream source mixed with directory source: %v", stream)
	}
}

func TestJSONLStreamUnicodeIdentitySurvivesReplay(t *testing.T) {
	run, _ := setup(t)
	const input = `{"id":"\ud83d\udc0b@draft","text":"Walrus observations."}`
	jsonlSync(t, run, "unicode", input)
	replayed := jsonlSync(t, run, "unicode", input)
	if lenOf(replayed, "unchanged") != 1 || firstItem(t, querySource(t, run, "unicode", "walrus")) != "\U0001f40b@draft" {
		t.Fatalf("Unicode identity did not round-trip: %v", replayed)
	}
}

func TestJSONLRecordLimitRetainsOnlyAcceptedPrefix(t *testing.T) {
	run, _ := setup(t)
	var input strings.Builder
	for i := range maxItemRecords - 1 {
		fmt.Fprintf(&input, "{\"id\":\"absent-%d\",\"deleted\":true}\n", i)
	}
	input.WriteString(record("accepted", "Walrus observations.") + "\n")
	input.WriteString(record("late", "Walrus sightings after the limit.") + "\n")
	run.runInput(input.String(), true, "sync", "--source", "bounded")
	found := querySource(t, run, "bounded", "walrus")
	if len(hits(found)) != 1 || firstItem(t, found) != "accepted" {
		t.Fatalf("record cap did not preserve exactly the accepted prefix: %v", found)
	}
}

func TestJSONLLineLimitRetainsCommittedPrefix(t *testing.T) {
	run, _ := setup(t)
	input := record("accepted", "Walrus observations.") + "\n" +
		record("oversized", strings.Repeat("x", maxItemRecordBytes)) + "\n"
	run.runInput(input, true, "sync", "--source", "line-limit")
	found := querySource(t, run, "line-limit", "walrus")
	if len(hits(found)) != 1 || firstItem(t, found) != "accepted" {
		t.Fatalf("oversized record discarded accepted evidence: %v", found)
	}
}

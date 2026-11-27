package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crypto/rand"
)

// historyArg runs the opt-in revision-history flag against one source.
func historyResult(t *testing.T, run testRun, source, setting string) map[string]any {
	t.Helper()
	_, result := run.run(false, "access", "--source", source, "--history", setting)
	return result
}

// recheckText runs the text-bearing trail recheck and returns the released list.
func recheckReleased(t *testing.T, run testRun, trailID string) []any {
	t.Helper()
	_, result := run.run(false, "recheck", "--trail", "--text", trailID)
	released, _ := result["released"].([]any)
	return released
}

func releasedStatuses(t *testing.T, released []any) []string {
	t.Helper()
	statuses := make([]string, 0, len(released))
	for _, entry := range released {
		status, _ := entry.(map[string]any)["status"].(string)
		statuses = append(statuses, status)
	}
	return statuses
}

func releasedTexts(t *testing.T, released []any) string {
	t.Helper()
	var text strings.Builder
	for _, entry := range released {
		segment, _ := entry.(map[string]any)["text"].(string)
		text.WriteString(segment)
	}
	return text.String()
}

func TestSourceRevisionHistory(t *testing.T) {
	run, _ := setup(t)
	long := strings.Repeat("Otters cache their shells in the riverbank silt. ", 120)
	jsonlSync(t, run, "histdocs", record("small", "Otters dive for shellfish at dawn.")+"\n"+
		record("gone", "Retired otter habitat notes.")+"\n"+
		record("big", long)+"\n")

	// With the default (unset) setting every retirement drops its text.
	_, beforeGone := run.run(false, "query", "--full", "--source", "histdocs", "habitat")
	goneTrail := trailIDOf(beforeGone)
	jsonlSync(t, run, "histdocs", record("gone", "Replacement otter habitat notes.")+"\n"+
		record("small", "Otters dive for shellfish at dusk.")+"\n")
	t.Run("default off keeps nothing", func(t *testing.T) {
		released := recheckReleased(t, run, goneTrail)
		if len(released) == 0 {
			t.Fatal("retired trail released no segments")
		}
		for _, status := range releasedStatuses(t, released) {
			if status != "not_kept" {
				t.Fatalf("default-off segment status = %q, want not_kept: %v", status, released)
			}
		}
	})

	t.Run("setting", func(t *testing.T) {
		first := historyResult(t, run, "histdocs", "on")
		if first["history"] != "on" || first["changed"] != true || first["history_event_id"] == "" || first["source"] != "histdocs" {
			t.Fatalf("history on result = %v", first)
		}
		again := historyResult(t, run, "histdocs", "on")
		if again["changed"] != false || again["history_event_id"] != first["history_event_id"] {
			t.Fatalf("repeat history on was not idempotent: %v", again)
		}
		off := historyResult(t, run, "histdocs", "off")
		if off["history"] != "off" || off["changed"] != true {
			t.Fatalf("history off result = %v", off)
		}
		if back := historyResult(t, run, "histdocs", "on"); back["changed"] != true {
			t.Fatalf("history back on result = %v", back)
		}
		for _, args := range [][]string{
			{"--source", "histdocs", "--history", "maybe"},
			{"--source", "histdocs", "--history="},
			{"--history", "on"},
			{"--source", "histdocs", "--history", "on", "extra"},
			{"--source", "histdocs", "--history", "on", "allow"},
			{"--source", "histdocs", "--create", "--history", "on"},
		} {
			if code, _, _ := run.runExit(append([]string{"access"}, args...)...); code == 0 {
				t.Fatalf("access %v unexpectedly succeeded", args)
			}
		}
		if stderr, _ := run.run(true, "access", "--source", "missing", "--history", "on"); !strings.Contains(stderr, "not_found") {
			t.Fatalf("history on a missing source stderr = %q", stderr)
		}
	})

	t.Run("toggling does not change retrieval", func(t *testing.T) {
		first := querySource(t, run, "histdocs", "shellfish")
		historyResult(t, run, "histdocs", "off")
		historyResult(t, run, "histdocs", "on")
		second := querySource(t, run, "histdocs", "shellfish")
		if trailOf(first)["result_sha256"] != trailOf(second)["result_sha256"] {
			t.Fatalf("history toggling changed the result digest: %v vs %v", trailOf(first), trailOf(second))
		}
		if firstItem(t, first) != firstItem(t, second) {
			t.Fatalf("history toggling changed released evidence")
		}
	})

	var smallTrail string
	t.Run("kept update and delete", func(t *testing.T) {
		_, smallV2 := run.run(false, "query", "--full", "--source", "histdocs", "dusk")
		smallTrail = trailIDOf(smallV2)
		_, bigV1 := run.run(false, "query", "--full", "--source", "histdocs", "riverbank")
		bigTrail := trailIDOf(bigV1)
		jsonlSync(t, run, "histdocs", record("small", "Otters dive for shellfish at noon.")+"\n"+
			record("big", "Herons wade the estuary at low tide.")+"\n")

		small := recheckReleased(t, run, smallTrail)
		if len(small) == 0 || releasedStatuses(t, small)[0] != "kept" || releasedTexts(t, small) != "Otters dive for shellfish at dusk." {
			t.Fatalf("kept update text = %v", small)
		}
		big := recheckReleased(t, run, bigTrail)
		if len(big) < 2 {
			t.Fatalf("multi-segment retirement kept %d segments, want several: %v", len(big), big)
		}
		for _, status := range releasedStatuses(t, big) {
			if status != "kept" {
				t.Fatalf("multi-segment kept status = %q: %v", status, big)
			}
		}
		if releasedTexts(t, big) != long {
			t.Fatalf("multi-segment kept text did not rebuild the retired item")
		}

		_, goneV2 := run.run(false, "query", "--full", "--source", "histdocs", "replacement")
		goneV2Trail := trailIDOf(goneV2)
		run.runInput(`{"id":"gone","deleted":true}`+"\n", false, "sync", "--source", "histdocs")
		deleted := recheckReleased(t, run, goneV2Trail)
		if len(deleted) == 0 || releasedStatuses(t, deleted)[0] != "kept" || releasedTexts(t, deleted) != "Replacement otter habitat notes." {
			t.Fatalf("kept delete text = %v", deleted)
		}
	})

	t.Run("off again keeps old archive only", func(t *testing.T) {
		historyResult(t, run, "histdocs", "off")
		_, smallV3 := run.run(false, "query", "--full", "--source", "histdocs", "noon")
		smallV3Trail := trailIDOf(smallV3)
		_, bigV2 := run.run(false, "query", "--full", "--source", "histdocs", "herons")
		bigV2Trail := trailIDOf(bigV2)
		jsonlSync(t, run, "histdocs", record("small", "Otters dive for shellfish at midnight.")+"\n")
		run.runInput(`{"id":"big","deleted":true}`+"\n", false, "sync", "--source", "histdocs")
		latest := recheckReleased(t, run, smallV3Trail)
		if len(latest) == 0 || releasedStatuses(t, latest)[0] != "not_kept" {
			t.Fatalf("disabled history still archived an update: %v", latest)
		}
		deleted := recheckReleased(t, run, bigV2Trail)
		if len(deleted) == 0 || releasedStatuses(t, deleted)[0] != "not_kept" {
			t.Fatalf("disabled history still archived a delete: %v", deleted)
		}
		// Disabling stops new archives but leaves earlier kept text readable.
		kept := recheckReleased(t, run, smallTrail)
		if len(kept) == 0 || releasedStatuses(t, kept)[0] != "kept" || releasedTexts(t, kept) != "Otters dive for shellfish at dusk." {
			t.Fatalf("disabling hid an earlier kept revision: %v", kept)
		}
	})

	t.Run("retired text stays unsearchable", func(t *testing.T) {
		if got := len(hits(querySource(t, run, "histdocs", "riverbank"))); got != 0 {
			t.Fatalf("retired text still searchable: %d hits", got)
		}
	})

	t.Run("notes are not revision history", func(t *testing.T) {
		run.run(false, "access", "--source", "histnotes", "--create", "allow")
		run.run(false, "access", "--source", "histnotes", "--history", "on")
		run.run(false, "remember", "--source", "histnotes", "Otters are diligent hunters.")
		code, out, _ := run.runExit("notes", "export", "--source", "histnotes")
		if code != 0 || !strings.Contains(out, "Otters are diligent hunters.") {
			t.Fatalf("notes export after history enable: %d %s", code, out)
		}
	})
	run.run(false, "verify")
}

func TestRevisionHistoryTamper(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "docs", record("doc", "Otters guard their shells.")+"\n")
	run.run(false, "access", "--source", "docs", "--history", "on")
	_, before := run.run(false, "query", "--full", "--source", "docs", "shells")
	trailID := trailIDOf(before)

	// A served update request gives request content a revision to resolve.
	writerToken := rand.Text()
	config := fmt.Sprintf(`{"principals":[
		{"id":"writer","token_sha256":%q,"read_sources":["docs"],"write_sources":["docs"]}
	]}`, tokenHash(writerToken))
	configPath := filepath.Join(t.TempDir(), "serve-config.json")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := startDaemonFlags(t, run.store, configPath, "--open-monitor")
	status, header, data := postNative(t, daemon.url+"/v1/documents", writerToken,
		[]byte(`{"source":"docs","record":{"id":"doc","text":"Otters guard their caches."}}`))
	if status != http.StatusOK {
		t.Fatalf("document update = %d: %s", status, data)
	}
	updateRequest := header.Get("X-Mousa-Request-ID")
	status, data = getDetail(t, daemon.url+"/v1/requests/"+updateRequest, "")
	if status != http.StatusOK {
		t.Fatalf("request detail = %d: %s", status, data)
	}
	var detail map[string]any
	if err := json.Unmarshal(data, &detail); err != nil {
		t.Fatalf("request detail is not JSON: %s", data)
	}
	if content, _ := envelopeResultJSON(t, detail)["content"].(map[string]any); content["previous"] != "Otters guard their shells." {
		t.Fatalf("request content before tamper = %v", content)
	}
	if released := recheckReleased(t, run, trailID); releasedStatuses(t, released)[0] != "kept" {
		t.Fatalf("archive missing before tamper: %v", released)
	}
	daemon.stop(t)

	// A same-length edit leaves the row shape intact but breaks the digest.
	rawExec(t, run.store, `UPDATE segment_history SET text = 'Otters guard their shellx.'`)
	if stderr, _ := run.run(true, "verify"); !strings.Contains(stderr, "integrity") {
		t.Fatalf("verify on tampered history stderr = %q", stderr)
	}
	if stderr, _ := run.run(true, "recheck", "--trail", "--text", trailID); !strings.Contains(stderr, "integrity") {
		t.Fatalf("recheck on tampered history stderr = %q", stderr)
	}
	damaged := startDaemonFlags(t, run.store, configPath, "--open-monitor")
	defer damaged.stop(t)
	if status, body := getDetail(t, damaged.url+"/v1/requests/"+updateRequest, ""); status != http.StatusInternalServerError {
		t.Fatalf("tampered request content = %d, want 500: %s", status, body)
	}
}

func TestServedRevisionHistory(t *testing.T) {
	run, _ := setup(t)
	run.run(false, "access", "--source", "docs", "--create", "allow")
	run.run(false, "access", "--source", "plain", "--create", "allow")
	run.run(false, "access", "--source", "docs", "--history", "on")

	writerToken := rand.Text()
	readerToken := rand.Text()
	strangerToken := rand.Text()
	operatorToken := rand.Text()
	config := fmt.Sprintf(`{"principals":[
		{"id":"writer","token_sha256":%q,"read_sources":["docs","plain"],"write_sources":["docs","plain"]},
		{"id":"reader","token_sha256":%q,"read_sources":["docs"],"write_sources":[]},
		{"id":"stranger","token_sha256":%q,"read_sources":["plain"],"write_sources":[]},
		{"id":"operator","operator":true,"token_sha256":%q,"read_sources":["docs","plain"],"write_sources":[]}
	]}`, tokenHash(writerToken), tokenHash(readerToken), tokenHash(strangerToken), tokenHash(operatorToken))
	configPath := filepath.Join(t.TempDir(), "serve-config.json")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	daemon := startDaemonFlags(t, run.store, configPath, "--open-monitor")
	endpoint := daemon.url
	docURL := endpoint + "/v1/documents"
	requestIDs := map[string]string{}
	post := func(body string) (int, string) {
		status, header, data := postNative(t, docURL, writerToken, []byte(body))
		if status != http.StatusOK {
			t.Fatalf("document write = %d: %s", status, data)
		}
		return status, header.Get("X-Mousa-Request-ID")
	}
	contentOf := func(t *testing.T, requestID string) map[string]any {
		t.Helper()
		status, data := getDetail(t, endpoint+"/v1/requests/"+requestID, "")
		if status != http.StatusOK {
			t.Fatalf("request detail = %d: %s", status, data)
		}
		var reply map[string]any
		if err := json.Unmarshal(data, &reply); err != nil {
			t.Fatalf("request detail is not JSON: %s", data)
		}
		content, _ := envelopeResultJSON(t, reply)["content"].(map[string]any)
		return content
	}

	var trailID, plainTrail string
	t.Run("add update delete content", func(t *testing.T) {
		_, requestIDs["add"] = post(`{"source":"docs","record":{"id":"d","text":"Otters surface at dawn."}}`)
		add := contentOf(t, requestIDs["add"])
		if add["current"] != "Otters surface at dawn." {
			t.Fatalf("add content = %v", add)
		}
		if _, has := add["previous"]; has {
			t.Fatalf("add carried a previous revision: %v", add)
		}
		_, requestIDs["update"] = post(`{"source":"docs","record":{"id":"d","text":"Otters surface at dusk."}}`)
		update := contentOf(t, requestIDs["update"])
		if update["previous"] != "Otters surface at dawn." || update["current"] != "Otters surface at dusk." {
			t.Fatalf("update content = %v", update)
		}
		// Seed the trail on the live dusk revision, then retire it.
		status, _, data := postNative(t, endpoint+"/v1/query", writerToken, []byte(`{"scope":{"source":"docs"},"text":"surface"}`))
		if status != http.StatusOK {
			t.Fatalf("served query = %d: %s", status, data)
		}
		trailID = trailIDOf(envelopeResultJSON(t, mustEnvelope(t, data)))
		if trailID == "" {
			t.Fatalf("served query recorded no trail: %s", data)
		}
		_, requestIDs["delete"] = post(`{"source":"docs","record":{"id":"d","deleted":true}}`)
		deleted := contentOf(t, requestIDs["delete"])
		if deleted["previous"] != "Otters surface at dusk." {
			t.Fatalf("delete content = %v", deleted)
		}
		if _, has := deleted["current"]; has {
			t.Fatalf("delete carried a current revision: %v", deleted)
		}
		// After the delete the update request's current revision exists only
		// in the archive; the item has advanced past it.
		archived := contentOf(t, requestIDs["update"])
		if archived["previous"] != "Otters surface at dawn." || archived["current"] != "Otters surface at dusk." {
			t.Fatalf("update content after delete = %v", archived)
		}
	})

	t.Run("multi-segment revisions rebuild in selector order", func(t *testing.T) {
		longDawn := strings.Repeat("Dawn river survey counts otter slides along the bank. ", 110) + "Dawn tail."
		longDusk := strings.Repeat("Dusk river survey counts otter slides along the bank. ", 110) + "Dusk tail."
		doc := func(item, text string) string {
			body, err := json.Marshal(map[string]any{"source": "docs", "record": map[string]any{"id": item, "text": text}})
			if err != nil {
				t.Fatal(err)
			}
			return string(body)
		}
		post(doc("m", longDawn))
		_, requestIDs["multi"] = post(doc("m", longDusk))
		post(`{"source":"docs","record":{"id":"m","deleted":true}}`)
		// Both revisions are multi-segment archive reads now: the update
		// request must rebuild each in selector order.
		archived := contentOf(t, requestIDs["multi"])
		if archived["previous"] != longDawn || archived["current"] != longDusk {
			t.Fatalf("multi-segment update content after delete = %v", archived)
		}
	})

	t.Run("empty revision stays distinguishable", func(t *testing.T) {
		post(`{"source":"docs","record":{"id":"e","text":""}}`)
		_, requestIDs["empty"] = post(`{"source":"docs","record":{"id":"e","text":"Now the otter has a note."}}`)
		empty := contentOf(t, requestIDs["empty"])
		previous, has := empty["previous"]
		if !has || previous != "" {
			t.Fatalf("empty previous revision = %v", empty)
		}
	})

	t.Run("history off reports not kept", func(t *testing.T) {
		post(`{"source":"plain","record":{"id":"p","text":"Plain item first text."}}`)
		status, _, data := postNative(t, endpoint+"/v1/query", writerToken, []byte(`{"scope":{"source":"plain"},"text":"first"}`))
		if status != http.StatusOK {
			t.Fatalf("served query = %d: %s", status, data)
		}
		plainTrail = trailIDOf(envelopeResultJSON(t, mustEnvelope(t, data)))
		if plainTrail == "" {
			t.Fatalf("served query recorded no trail: %s", data)
		}
		_, requestIDs["plain"] = post(`{"source":"plain","record":{"id":"p","text":"Plain item second text."}}`)
		plain := contentOf(t, requestIDs["plain"])
		if _, has := plain["previous"]; has {
			t.Fatalf("unset history released previous text: %v", plain)
		}
		if plain["previous_not_kept"] != true {
			t.Fatalf("unset history did not mark previous_not_kept: %v", plain)
		}
		if plain["current"] != "Plain item second text." {
			t.Fatalf("current revision missing: %v", plain)
		}
	})

	t.Run("served recheck", func(t *testing.T) {
		if trailID == "" {
			t.Fatal("the dusk revision trail was never seeded")
		}
		released := recheckReleased(t, run, trailID)
		if len(released) == 0 || releasedTexts(t, released) != "Otters surface at dusk." {
			t.Fatalf("recheck of the retired dusk revision = %v", released)
		}
		for _, status := range releasedStatuses(t, released) {
			if status != "kept" {
				t.Fatalf("dusk revision status = %q, want kept: %v", status, released)
			}
		}
		released = recheckReleased(t, run, plainTrail)
		for _, status := range releasedStatuses(t, released) {
			if status != "not_kept" {
				t.Fatalf("plain revision status = %q, want not_kept: %v", status, released)
			}
		}
	})

	t.Run("cli recheck arg layouts", func(t *testing.T) {
		for _, args := range [][]string{
			{"recheck", "--trail", "--text", trailID},
			{"recheck", "--trail", trailID, "--text"},
		} {
			_, result := run.run(false, args...)
			released, _ := result["released"].([]any)
			if len(released) == 0 {
				t.Fatalf("recheck %v released no segments: %v", args, result)
			}
		}
		if code, _, stderr := run.runExit("recheck", "--source", "histnotes", "--text", strings.Repeat("a", 64)); code == 0 {
			t.Fatalf("recheck --text without --trail succeeded: %s", stderr)
		}
	})

	t.Run("endpoint recheck releases text to readers", func(t *testing.T) {
		code, out, stderr := runEndpoint(t, endpoint, readerToken, "", "recheck", "--trail", "--text", trailID)
		if code != 0 {
			t.Fatalf("reader recheck --text failed: %d %s %s", code, out, stderr)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("recheck reply is not JSON: %s", out)
		}
		released, _ := result["released"].([]any)
		if len(released) == 0 || releasedTexts(t, released) != "Otters surface at dusk." {
			t.Fatalf("endpoint recheck released = %v", released)
		}
		for _, status := range releasedStatuses(t, released) {
			if status != "kept" {
				t.Fatalf("endpoint recheck status = %q, want kept: %v", status, released)
			}
		}
		if _, exposed := result["as_of"]; exposed {
			t.Fatalf("served recheck exposed the store index: %v", result)
		}
		// A reader of the plain source sees not_kept, not leaked text.
		code, out, _ = runEndpoint(t, endpoint, writerToken, "", "recheck", "--trail", "--text", plainTrail)
		if code != 0 {
			t.Fatalf("writer recheck of plain trail failed: %d %s", code, out)
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("recheck reply is not JSON: %s", out)
		}
		released, _ = result["released"].([]any)
		for _, entry := range released {
			row, _ := entry.(map[string]any)
			if row["status"] != "not_kept" {
				t.Fatalf("plain trail released = %v", released)
			}
			if _, has := row["text"]; has {
				t.Fatalf("not_kept released text: %v", released)
			}
		}
	})

	t.Run("endpoint recheck refuses strangers and no token", func(t *testing.T) {
		if code, _, stderr := runEndpoint(t, endpoint, strangerToken, "", "recheck", "--trail", "--text", trailID); code == 0 || !strings.Contains(stderr, "not_found") {
			t.Fatalf("stranger recheck = %d %s, want not_found", code, stderr)
		}
		if code, _, _ := runEndpoint(t, endpoint, "", "", "recheck", "--trail", "--text", trailID); code == 0 {
			t.Fatal("unauthenticated recheck unexpectedly succeeded")
		}
	})

	// The serving daemon stops so a closed monitor can own the same store; a
	// closed monitor still hides content while the source is allowed.
	daemon.stop(t)
	t.Run("closed monitor shows metadata only", func(t *testing.T) {
		closed := startDaemon(t, run.store, configPath)
		defer closed.stop(t)
		status, data := getDetail(t, closed.url+"/v1/requests/"+requestIDs["update"], operatorToken)
		if status != http.StatusOK {
			t.Fatalf("operator request detail = %d: %s", status, data)
		}
		var reply map[string]any
		if err := json.Unmarshal(data, &reply); err != nil {
			t.Fatalf("request detail is not JSON: %s", data)
		}
		if _, has := envelopeResultJSON(t, reply)["content"]; has {
			t.Fatalf("closed monitor exposed request content: %s", data)
		}
		if status, _ := getDetail(t, closed.url+"/v1/requests/"+requestIDs["update"], readerToken); status != http.StatusForbidden {
			t.Fatalf("non-operator request detail = %d, want 403", status)
		}
	})

	// Deny and withdrawal commit on the same store offline; restarting must
	// not resurrect retired text anywhere.
	releaseCheck := func(t *testing.T, word string) {
		t.Helper()
		reopened := startDaemonFlags(t, run.store, configPath, "--open-monitor")
		defer reopened.stop(t)
		endpoint = reopened.url
		content := contentOf(t, requestIDs["update"])
		if _, has := content["previous"]; has {
			t.Fatalf("%s source released kept text: %v", word, content)
		}
		if _, has := content["current"]; has {
			t.Fatalf("%s source released current text: %v", word, content)
		}
		if code, _, _ := runEndpoint(t, reopened.url, readerToken, "", "recheck", "--trail", "--text", trailID); code == 0 {
			t.Fatalf("%s source served recheck unexpectedly succeeded", word)
		}
		if code, _, _ := run.runExit("recheck", "--trail", "--text", trailID); code == 0 {
			t.Fatalf("%s source offline recheck --text unexpectedly succeeded", word)
		}
		// The plain recheck still reports the trail's staleness.
		run.run(false, "recheck", "--trail", trailID)
	}

	run.run(false, "access", "--source", "docs", "deny")
	t.Run("deny releases no history", func(t *testing.T) {
		releaseCheck(t, "denied")
	})

	run.run(false, "access", "--source", "docs", "allow")
	run.run(false, "withdraw", "--source", "docs")
	t.Run("withdraw releases no history", func(t *testing.T) {
		releaseCheck(t, "withdrawn")
	})
}

// mustEnvelope decodes a native reply envelope for tests that need its result.
func mustEnvelope(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatalf("reply is not JSON: %v\n%s", err, data)
	}
	return envelope
}

// schema8Fixture unpacks the frozen schema-8 store for migration tests.
func schema8Fixture(t *testing.T) string {
	t.Helper()
	compressed, err := os.Open(filepath.Join("testdata", "schema8_fixture.sqlite.gz"))
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
	path := filepath.Join(t.TempDir(), "schema8.sqlite")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMigrateSchema8(t *testing.T) {
	fixture := schema8Fixture(t)
	run := testRun{t: t, binary: cliBinary, store: fixture}

	tables := []string{"log", "local_items", "path_moves", "note_supersessions", "notes",
		"note_citations", "trails", "request_admissions", "request_outcomes", "segments"}
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

	stderr, _ := run.run(true, "verify")
	if !strings.Contains(stderr, "incompatible_schema") || !strings.Contains(stderr, "mousa migrate") {
		t.Fatalf("verify on schema 8 stderr = %q", stderr)
	}
	_, migrated := run.run(false, "migrate")
	if migrated["schema"] != float64(9) {
		t.Fatalf("migrate = %v, want schema 9", migrated)
	}
	run.run(false, "verify")

	for _, table := range tables {
		count, digest := tableDigest(t, fixture, table)
		want := before[table]
		if count != want.count || digest != want.digest {
			t.Fatalf("%s changed across migration: %d rows %s -> %d rows %s", table, want.count, want.digest, count, digest)
		}
	}

	// Exported notes are byte-identical to the schema-8 binary's own export.
	baseline, err := os.ReadFile(filepath.Join("testdata", "schema8_notes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	code, out, _ := run.runExit("notes", "export", "--source", "fixture-notes")
	if code != 0 {
		t.Fatalf("notes export failed: %s", out)
	}
	if out != string(baseline) {
		t.Fatalf("notes export changed across migration:\n%s\nwant\n%s", out, baseline)
	}

	// A trail whose released segments were retired before the fixture was
	// frozen reports them unretained with the default-off setting.
	db := openRawStore(t, fixture)
	var rawID []byte
	if err := db.QueryRow(`SELECT DISTINCT t.id FROM trails t
		JOIN trail_segments ts ON ts.trail_id = t.id
		WHERE NOT EXISTS (SELECT 1 FROM segment_lexical_rows r WHERE r.segment_id = ts.segment_id) LIMIT 1`).Scan(&rawID); err != nil {
		t.Fatalf("fixture holds no trail citing a retired segment: %v", err)
	}
	retiredTrail := fmt.Sprintf("%x", rawID)
	released := recheckReleased(t, run, retiredTrail)
	if len(released) == 0 {
		t.Fatal("retired trail released no segments")
	}
	for _, status := range releasedStatuses(t, released) {
		if status != "not_kept" {
			t.Fatalf("migrated retired segment status = %q, want not_kept: %v", status, released)
		}
	}

	// With history still unset, a retirement after migration keeps nothing.
	jsonlSync(t, run, "fixture-docs", record("docs/one", "Migrated fixture document one, now about otters again.")+"\n")
	run.run(false, "verify")
	var archived int
	if err := db.QueryRow(`SELECT count(*) FROM segment_history`).Scan(&archived); err != nil {
		t.Fatalf("count segment_history: %v", err)
	}
	if archived != 0 {
		t.Fatalf("default-off migration archived %d segment texts", archived)
	}
	var events int
	if err := db.QueryRow(`SELECT count(*) FROM source_history_events`).Scan(&events); err != nil {
		t.Fatalf("count source_history_events: %v", err)
	}
	if events != 0 {
		t.Fatalf("migration wrote %d source history events", events)
	}
}

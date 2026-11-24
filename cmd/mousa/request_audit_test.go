package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// requestEntry is the test-side view of one served-request audit record. Field
// names mirror the public history read; the assertions below never see note or
// query text because the records must not carry it.
type requestEntry struct {
	Admission struct {
		RequestID      string `json:"request_id"`
		Authenticated  bool   `json:"authenticated"`
		Principal      string `json:"principal"`
		Operation      string `json:"operation"`
		ReceivedAtUsec int64  `json:"received_at_usec"`
		Client         struct {
			Harness string `json:"harness"`
			Agent   string `json:"agent"`
			Model   string `json:"model"`
			RunID   string `json:"run_id"`
		} `json:"client"`
	} `json:"admission"`
	Outcome struct {
		RequestID      string `json:"request_id"`
		Seq            uint64 `json:"seq"`
		FinishedAtUsec int64  `json:"finished_at_usec"`
		State          string `json:"state"`
		ErrorCode      string `json:"error_code"`
		Evidence       struct {
			TrailID      string `json:"trail_id"`
			NoteID       string `json:"note_id"`
			ItemChangeID string `json:"item_change_id"`
		} `json:"evidence"`
	} `json:"outcome"`
}

type requestPage struct {
	Requests  []requestEntry `json:"requests"`
	NextAfter uint64         `json:"next_after"`
	More      bool           `json:"more"`
}

type requestDetail struct {
	requestEntry
	Evidence *struct {
		Kind        string   `json:"kind"`
		ID          string   `json:"id"`
		Sources     []string `json:"sources"`
		SourceID    string   `json:"source_id"`
		Source      string   `json:"source"`
		ItemID      string   `json:"item_id"`
		Current     string   `json:"current"`
		Previous    string   `json:"previous"`
		NoteAuthor  string   `json:"note_author"`
		CitedTrails []string `json:"cited_trails"`
	} `json:"evidence"`
}

// getNative runs one GET against the served API and requires the status.
func getNative(t *testing.T, url, token string, wantStatus int, label string) (http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("%s: build request: %v", label, err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s: GET %s: %v", label, url, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		t.Fatalf("%s: read reply: %v", label, err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s: GET %s: status = %d, want %d: %s", label, url, response.StatusCode, wantStatus, body)
	}
	return response.Header, body
}

// postAudit sends one JSON body, requires the status, and returns the raw
// envelope bytes plus the server-issued request ID.
func postAudit(t *testing.T, label, url, token string, body any, wantStatus int) (string, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("%s: marshal body: %v", label, err)
	}
	status, header, data := postNative(t, url, token, raw)
	if status != wantStatus {
		t.Fatalf("%s: status = %d, want %d: %s", label, status, wantStatus, data)
	}
	requestID := header.Get("X-Mousa-Request-ID")
	if requestID == "" {
		t.Fatalf("%s: reply has no request ID: %s", label, data)
	}
	return requestID, data
}

func auditResult(t *testing.T, data []byte) json.RawMessage {
	t.Helper()
	var reply struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatalf("reply is not an envelope: %s", data)
	}
	return reply.Result
}

// requestHistory reads a history page as an operator.
func requestHistory(t *testing.T, endpoint, token, query string) requestPage {
	t.Helper()
	url := endpoint + "/v1/requests"
	if query != "" {
		url += "?" + query
	}
	_, body := getNative(t, url, token, http.StatusOK, "request history")
	var page requestPage
	if err := json.Unmarshal(auditResult(t, body), &page); err != nil {
		t.Fatalf("request history decode: %v: %s", err, body)
	}
	return page
}

func requestDetailByID(t *testing.T, endpoint, token, requestID string, wantStatus int) requestDetail {
	t.Helper()
	_, body := getNative(t, endpoint+"/v1/requests/"+requestID, token, wantStatus, "request detail")
	var detail requestDetail
	if wantStatus != http.StatusOK {
		return detail
	}
	if err := json.Unmarshal(auditResult(t, body), &detail); err != nil {
		t.Fatalf("request detail decode: %v: %s", err, body)
	}
	return detail
}

// requestHistoryStatus returns the bare status of one history read.
func requestHistoryStatus(t *testing.T, endpoint, token string) int {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+"/v1/requests", nil)
	if err != nil {
		t.Fatalf("history build: %v", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("history request: %v", err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	return response.StatusCode
}

// TestServeRequestAudit requires every served request to bind its authenticated
// principal, operation, and outcome to durable evidence. Unauthorized and
// malformed attempts are recorded without claimed effects.
func TestServeRequestAudit(t *testing.T) {
	run, _ := setup(t)
	run.run(false, "access", "--source", "docs", "--create", "allow")
	run.run(false, "access", "--source", "memory", "--create", "allow")
	run.run(false, "access", "--source", "secret", "--create", "allow")
	run.run(false, "access", "--source", "stale", "--create", "allow")
	run.runInput(record("hidden.md", "# Hidden\n\nSecret ledger.\n")+"\n", false, "sync", "--source", "secret")
	run.runInput(record("stale.md", "# Stale\n\nRetired evidence.\n")+"\n", false, "sync", "--source", "stale")
	run.run(false, "withdraw", "--source", "stale")

	tokenA := rand.Text()
	tokenB := rand.Text()
	config := fmt.Sprintf(`{"principals":[
		{"id":"writer-a","token_sha256":%q,"read_sources":["docs","memory","stale","secret"],"write_sources":["docs","memory","stale"],"operator":true},
		{"id":"writer-b","token_sha256":%q,"read_sources":["docs","memory"],"write_sources":["docs","memory"]}
	]}`, tokenHash(tokenA), tokenHash(tokenB))
	configPath := filepath.Join(t.TempDir(), "native.json")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	daemon := startDaemon(t, run.store, configPath)
	endpoint := daemon.url

	type expectation struct {
		principal string
		operation string
		state     string
		code      string
		evidence  string // "", "trail", "note", "item_change"
	}
	var expected []expectation
	var requestIDs []string

	// 1-3: writer-a commits a document, a query, and a note.
	docAdd := record("audit.md", "# Audit\n\nThe walrus protocol keeps requests honest.\n")
	_, docResult := postAudit(t, "writer doc add", endpoint+"/v1/documents", tokenA,
		map[string]any{"source": "docs", "record": json.RawMessage(docAdd)}, http.StatusOK)
	var docAdded struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(auditResult(t, docResult), &docAdded); err != nil || docAdded.Action != "added" {
		t.Fatalf("writer doc add result: %v: %s", err, docResult)
	}
	expected = append(expected, expectation{principal: "writer-a", operation: "document", state: "committed", evidence: "item_change"})

	queryA, queryData := postAudit(t, "writer query", endpoint+"/v1/query", tokenA,
		map[string]any{"scope": map[string]any{"source": "docs"}, "text": "walrus"}, http.StatusOK)
	requestIDs = append(requestIDs, queryA)
	var resultA struct {
		Trail struct {
			Request struct {
				Caller    string `json:"caller"`
				RequestID string `json:"request_id"`
			} `json:"request"`
			ResultSHA256 string `json:"result_sha256"`
		} `json:"trail"`
	}
	if err := json.Unmarshal(auditResult(t, queryData), &resultA); err != nil {
		t.Fatalf("writer query decode: %v: %s", err, queryData)
	}
	if resultA.Trail.Request.Caller != "writer-a" || resultA.Trail.Request.RequestID != queryA {
		t.Fatalf("trail attribution = %q/%q, want writer-a/%q",
			resultA.Trail.Request.Caller, resultA.Trail.Request.RequestID, queryA)
	}
	expected = append(expected, expectation{principal: "writer-a", operation: "query", state: "committed", evidence: "trail"})

	// writer-b runs the identical query before any store change; the result
	// digest must not shift with actor.
	_, queryDataB := postAudit(t, "peer query", endpoint+"/v1/query", tokenB,
		map[string]any{"scope": map[string]any{"source": "docs"}, "text": "walrus"}, http.StatusOK)
	var resultB struct {
		Trail struct {
			ResultSHA256 string `json:"result_sha256"`
		} `json:"trail"`
	}
	if err := json.Unmarshal(auditResult(t, queryDataB), &resultB); err != nil {
		t.Fatalf("peer query decode: %v: %s", err, queryDataB)
	}
	if resultB.Trail.ResultSHA256 != resultA.Trail.ResultSHA256 {
		t.Fatalf("identical query digests differ across actors: %q vs %q",
			resultA.Trail.ResultSHA256, resultB.Trail.ResultSHA256)
	}
	expected = append(expected, expectation{principal: "writer-b", operation: "query", state: "committed", evidence: "trail"})

	noteText := "operators remember durable request evidence"
	_, noteData := postAudit(t, "writer note", endpoint+"/v1/notes", tokenA,
		map[string]any{"scope": map[string]any{"source": "memory"}, "text": noteText}, http.StatusOK)
	var noteCreated struct {
		ID     string `json:"id"`
		Author string `json:"author"`
	}
	if err := json.Unmarshal(auditResult(t, noteData), &noteCreated); err != nil || noteCreated.Author != "writer-a" {
		t.Fatalf("writer note result: %v: %s", err, noteData)
	}
	expected = append(expected, expectation{principal: "writer-a", operation: "note", state: "committed", evidence: "note"})

	// 4: writer-b replays the same note: no-op attributed to B, original kept.
	_, replayData := postAudit(t, "replay note", endpoint+"/v1/notes", tokenB,
		map[string]any{"scope": map[string]any{"source": "memory"}, "text": noteText}, http.StatusOK)
	var noteReplay struct {
		ID     string `json:"id"`
		Author string `json:"author"`
	}
	if err := json.Unmarshal(auditResult(t, replayData), &noteReplay); err != nil ||
		noteReplay.ID != noteCreated.ID || noteReplay.Author != "writer-a" {
		t.Fatalf("replay note result: %v: %s (original %v)", err, replayData, noteCreated)
	}
	expected = append(expected, expectation{principal: "writer-b", operation: "note", state: "no_op", evidence: "note"})

	// 5-7: writer-b updates, replays, and deletes-absent a document.
	docUpdate := record("audit.md", "# Audit\n\nRequests carry verified outcomes.\n")
	postAudit(t, "writer update", endpoint+"/v1/documents", tokenB,
		map[string]any{"source": "docs", "record": json.RawMessage(docUpdate)}, http.StatusOK)
	expected = append(expected, expectation{principal: "writer-b", operation: "document", state: "committed", evidence: "item_change"})

	postAudit(t, "writer replay doc", endpoint+"/v1/documents", tokenB,
		map[string]any{"source": "docs", "record": json.RawMessage(docUpdate)}, http.StatusOK)
	expected = append(expected, expectation{principal: "writer-b", operation: "document", state: "no_op"})

	postAudit(t, "writer delete absent", endpoint+"/v1/documents", tokenB,
		map[string]any{"source": "docs", "record": json.RawMessage(`{"id":"ghost.md","deleted":true}`)}, http.StatusOK)
	expected = append(expected, expectation{principal: "writer-b", operation: "document", state: "no_op"})

	// 9: writer-a queries a granted but withdrawn source: denied with its trail.
	deniedQuery, deniedData := postAudit(t, "withdrawn query", endpoint+"/v1/query", tokenA,
		map[string]any{"scope": map[string]any{"source": "stale"}, "text": "anything"}, http.StatusOK)
	var deniedTrail struct {
		Trail struct {
			Request struct {
				RequestID string `json:"request_id"`
			} `json:"request"`
			Sources []struct {
				Outcome string `json:"outcome"`
			} `json:"sources"`
		} `json:"trail"`
		Passages []any `json:"passages"`
	}
	if err := json.Unmarshal(auditResult(t, deniedData), &deniedTrail); err != nil ||
		deniedTrail.Trail.Request.RequestID != deniedQuery || len(deniedTrail.Passages) != 0 {
		t.Fatalf("withdrawn query reply: %v: %s", err, deniedData)
	}
	for _, source := range deniedTrail.Trail.Sources {
		if source.Outcome != "deny" {
			t.Fatalf("withdrawn query trail source outcome = %q", source.Outcome)
		}
	}
	expected = append(expected, expectation{principal: "writer-a", operation: "query", state: "denied", code: "source_denied", evidence: "trail"})

	// 10-11: grant-scoped denials record denied with no evidence.
	postAudit(t, "ungranted query", endpoint+"/v1/query", tokenB,
		map[string]any{"scope": map[string]any{"source": "secret"}, "text": "ledger"}, http.StatusForbidden)
	expected = append(expected, expectation{principal: "writer-b", operation: "query", state: "denied", code: "source_denied"})

	postAudit(t, "ungranted write", endpoint+"/v1/documents", tokenB,
		map[string]any{"source": "secret", "record": json.RawMessage(docAdd)}, http.StatusForbidden)
	expected = append(expected, expectation{principal: "writer-b", operation: "document", state: "denied", code: "source_denied"})

	// A granted write to a withdrawn source is denied by the persisted
	// withdrawal itself, not by the grant check.
	postAudit(t, "withdrawn write", endpoint+"/v1/documents", tokenA,
		map[string]any{"source": "stale", "record": json.RawMessage(docAdd)}, http.StatusForbidden)
	expected = append(expected, expectation{principal: "writer-a", operation: "document", state: "denied", code: "source_denied"})

	// 12-16: spoofed identity, missing/wrong credentials, malformed, oversized.
	postAudit(t, "caller spoof", endpoint+"/v1/query", tokenA,
		map[string]any{"caller": "admin", "scope": map[string]any{"source": "docs"}, "text": "x"}, http.StatusBadRequest)
	expected = append(expected, expectation{principal: "writer-a", operation: "query", state: "invalid", code: "invalid_request"})

	postAudit(t, "missing credential", endpoint+"/v1/query", "",
		map[string]any{"scope": map[string]any{"source": "docs"}, "text": "x"}, http.StatusUnauthorized)
	expected = append(expected, expectation{principal: "", operation: "query", state: "denied", code: "unauthorized"})

	postAudit(t, "wrong credential", endpoint+"/v1/query", rand.Text(),
		map[string]any{"scope": map[string]any{"source": "docs"}, "text": "x"}, http.StatusUnauthorized)
	expected = append(expected, expectation{principal: "", operation: "query", state: "denied", code: "unauthorized"})

	status, _, _ := postNative(t, endpoint+"/v1/query", tokenA, []byte(`{"scope":`))
	if status != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", status)
	}
	expected = append(expected, expectation{principal: "writer-a", operation: "query", state: "invalid", code: "invalid_request"})

	postAudit(t, "oversized body", endpoint+"/v1/query", tokenA,
		json.RawMessage(`{"scope":{"source":"docs"},"text":"`+strings.Repeat("x", 64*1024)+`"}`),
		http.StatusRequestEntityTooLarge)
	expected = append(expected, expectation{principal: "writer-a", operation: "query", state: "invalid", code: "too_large"})

	// Non-operator history reads are refused; missing credentials stay denied.
	getNative(t, endpoint+"/v1/requests", tokenB, http.StatusForbidden, "nonoperator history")
	getNative(t, endpoint+"/v1/requests", "", http.StatusUnauthorized, "unauthenticated history")
	getNative(t, endpoint+"/v1/requests/not-a-request", tokenA, http.StatusNotFound, "malformed request id")
	getNative(t, endpoint+"/v1/requests?after=abc", tokenA, http.StatusBadRequest, "malformed after")
	getNative(t, endpoint+"/v1/requests?limit=10000", tokenA, http.StatusBadRequest, "oversized limit")

	// 17: one request carries reported client metadata; "<" must survive.
	metadataQuery, _ := postAudit(t, "metadata query", endpoint+"/v1/query", tokenA,
		map[string]any{"scope": map[string]any{"source": "docs"}, "text": "walrus",
			"client": map[string]any{"harness": "suite<2", "agent": "devin", "model": "swe-2", "run_id": "r-7"}},
		http.StatusOK)
	expected = append(expected, expectation{principal: "writer-a", operation: "query", state: "committed", evidence: "trail"})

	// 18: a canceled request still records its attempt. The raw client closes
	// the connection immediately after sending so the daemon observes it.
	rawConn, err := net.Dial("tcp", strings.TrimPrefix(endpoint, "http://"))
	if err != nil {
		t.Fatalf("canceled dial: %v", err)
	}
	cancelBody := `{"scope":{"source":"docs"},"text":"walrus"}`
	if _, err := fmt.Fprintf(rawConn, "POST /v1/query HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nAuthorization: Bearer %s\r\nContent-Length: %d\r\n\r\n%s",
		strings.TrimPrefix(endpoint, "http://"), tokenA, len(cancelBody), cancelBody); err != nil {
		t.Fatalf("canceled write: %v", err)
	}
	rawConn.Close()

	// 19-21: writer-a cites a trail from secret — which writer-b cannot
	// read — inside a shared memory note. The note text stays shared,
	// but restricted lineage must not travel with it.
	_, secretTrailData := postAudit(t, "secret query", endpoint+"/v1/query", tokenA,
		map[string]any{"scope": map[string]any{"source": "secret"}, "text": "ledger"}, http.StatusOK)
	var secretTrail struct {
		Trail struct {
			ID         string `json:"id"`
			Candidates []struct {
				SegmentID string `json:"segment_id"`
				Packed    string `json:"packed"`
			} `json:"candidates"`
		} `json:"trail"`
	}
	if err := json.Unmarshal(auditResult(t, secretTrailData), &secretTrail); err != nil || secretTrail.Trail.ID == "" {
		t.Fatalf("secret query trail: %v: %s", err, secretTrailData)
	}
	var citedSegment string
	for _, candidate := range secretTrail.Trail.Candidates {
		if candidate.Packed == "full" {
			citedSegment = candidate.SegmentID
			break
		}
	}
	if citedSegment == "" {
		t.Fatal("secret query packed no segment")
	}
	expected = append(expected, expectation{principal: "writer-a", operation: "query", state: "committed", evidence: "trail"})

	citedText := "a shared note with restricted lineage"
	_, sharedNoteData := postAudit(t, "cited note", endpoint+"/v1/notes", tokenA,
		map[string]any{"scope": map[string]any{"source": "memory"}, "text": citedText,
			"citations": []map[string]any{{"trail_id": secretTrail.Trail.ID, "segment_ids": []string{citedSegment}}}},
		http.StatusOK)
	var sharedNote struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(auditResult(t, sharedNoteData), &sharedNote); err != nil || sharedNote.ID == "" {
		t.Fatalf("cited note result: %v: %s", err, sharedNoteData)
	}
	expected = append(expected, expectation{principal: "writer-a", operation: "note", state: "committed", evidence: "note"})

	// writer-b still reads the shared note text, but the restricted trail
	// and its segment lineage are withheld — derived_from and stale carry
	// nothing the caller could not resolve itself.
	_, blindQueryData := postAudit(t, "restricted lineage query", endpoint+"/v1/query", tokenB,
		map[string]any{"scope": map[string]any{"source": "memory"}, "text": "shared note"}, http.StatusOK)
	var blindNote struct {
		Passages []struct {
			Note *struct {
				Origin      string `json:"origin"`
				DerivedFrom []any  `json:"derived_from"`
				Stale       []any  `json:"stale"`
			} `json:"note"`
		} `json:"passages"`
	}
	blindRaw := auditResult(t, blindQueryData)
	if err := json.Unmarshal(blindRaw, &blindNote); err != nil {
		t.Fatalf("restricted lineage query decode: %v: %s", err, blindRaw)
	}
	foundShared := false
	for _, passage := range blindNote.Passages {
		if passage.Note == nil {
			continue
		}
		foundShared = true
		if passage.Note.Origin != "derived" || len(passage.Note.DerivedFrom) != 0 || len(passage.Note.Stale) != 0 {
			t.Fatalf("restricted note lineage leaked: %+v", passage.Note)
		}
	}
	if !foundShared {
		t.Fatalf("shared note passage missing for writer-b: %s", blindRaw)
	}
	if strings.Contains(string(blindRaw), secretTrail.Trail.ID) || strings.Contains(string(blindRaw), citedSegment) {
		t.Fatal("restricted query reply leaked trail or segment lineage")
	}
	expected = append(expected, expectation{principal: "writer-b", operation: "query", state: "committed", evidence: "trail"})

	// A same-text replay with omitted citations would return the original
	// note and its restricted lineage; it is denied without content.
	_, deniedReplay := postAudit(t, "restricted replay", endpoint+"/v1/notes", tokenB,
		map[string]any{"scope": map[string]any{"source": "memory"}, "text": citedText}, http.StatusForbidden)
	if strings.Contains(string(deniedReplay), sharedNote.ID) || strings.Contains(string(deniedReplay), secretTrail.Trail.ID) {
		t.Fatalf("restricted replay leaked identifiers: %s", deniedReplay)
	}
	expected = append(expected, expectation{principal: "writer-b", operation: "note", state: "denied", code: "source_denied"})

	// The fully authorized caller still receives the original lineage and
	// an authorized replay resolves to the same canonical note.
	_, lineageData := postAudit(t, "authorized lineage query", endpoint+"/v1/query", tokenA,
		map[string]any{"scope": map[string]any{"source": "memory"}, "text": "shared note"}, http.StatusOK)
	var lineageNote struct {
		Passages []struct {
			Note *struct {
				DerivedFrom []struct {
					TrailID    string   `json:"trail_id"`
					SegmentIDs []string `json:"segment_ids"`
				} `json:"derived_from"`
			} `json:"note"`
		} `json:"passages"`
	}
	if err := json.Unmarshal(auditResult(t, lineageData), &lineageNote); err != nil {
		t.Fatalf("authorized lineage decode: %v", err)
	}
	lineageFound := false
	for _, passage := range lineageNote.Passages {
		if passage.Note == nil {
			continue
		}
		for _, citation := range passage.Note.DerivedFrom {
			if citation.TrailID == secretTrail.Trail.ID && len(citation.SegmentIDs) == 1 && citation.SegmentIDs[0] == citedSegment {
				lineageFound = true
			}
		}
	}
	if !lineageFound {
		t.Fatalf("authorized caller lost note lineage: %s", auditResult(t, lineageData))
	}
	expected = append(expected, expectation{principal: "writer-a", operation: "query", state: "committed", evidence: "trail"})

	_, replayOK := postAudit(t, "authorized replay", endpoint+"/v1/notes", tokenA,
		map[string]any{"scope": map[string]any{"source": "memory"}, "text": citedText}, http.StatusOK)
	var replayNote struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(auditResult(t, replayOK), &replayNote); err != nil || replayNote.ID != sharedNote.ID {
		t.Fatalf("authorized replay = %v: %s, want note %s", err, replayOK, sharedNote.ID)
	}
	expected = append(expected, expectation{principal: "writer-a", operation: "note", state: "no_op", evidence: "note"})

	// Reconcile history against every expectation.
	var all []requestEntry
	var after uint64
	for i := 0; i < 64; i++ {
		page := requestHistory(t, endpoint, tokenA, fmt.Sprintf("after=%d&limit=4", after))
		if len(page.Requests) == 0 {
			if i == 0 {
				t.Fatal("history is empty")
			}
			break
		}
		for _, entry := range page.Requests {
			if entry.Outcome.Seq <= after {
				t.Fatalf("history seq did not advance: %d <= %d", entry.Outcome.Seq, after)
			}
			after = entry.Outcome.Seq
		}
		all = append(all, page.Requests...)
		if !page.More {
			break
		}
	}
	if first := requestHistory(t, endpoint, tokenA, "limit=1"); len(first.Requests) != 1 || !first.More {
		t.Fatalf("first page = %d requests more=%v, want 1 with more", len(first.Requests), first.More)
	}

	canceledFound := false
	for _, entry := range all {
		if entry.Admission.RequestID != entry.Outcome.RequestID || entry.Admission.RequestID == "" {
			t.Fatalf("admission/outcome request mismatch: %q vs %q", entry.Admission.RequestID, entry.Outcome.RequestID)
		}
		if entry.Outcome.FinishedAtUsec < entry.Admission.ReceivedAtUsec {
			t.Fatalf("outcome finished before admission: %v", entry)
		}
		if entry.Outcome.State == "canceled" {
			if entry.Outcome.ErrorCode != "canceled" {
				t.Fatalf("canceled outcome code = %q", entry.Outcome.ErrorCode)
			}
			canceledFound = true
		}
		if !entry.Admission.Authenticated && entry.Admission.Principal != "" {
			t.Fatalf("unauthenticated admission claims principal %q", entry.Admission.Principal)
		}
	}

	matched := map[string]bool{}
	for i, want := range expected {
		found := false
		for _, entry := range all {
			if matched[entry.Outcome.RequestID] {
				continue
			}
			if entry.Admission.Principal == want.principal &&
				entry.Admission.Operation == want.operation &&
				entry.Outcome.State == want.state &&
				entry.Outcome.ErrorCode == want.code {
				got := ""
				switch {
				case entry.Outcome.Evidence.TrailID != "":
					got = "trail"
				case entry.Outcome.Evidence.NoteID != "":
					got = "note"
				case entry.Outcome.Evidence.ItemChangeID != "":
					got = "item_change"
				}
				if got == want.evidence {
					matched[entry.Outcome.RequestID] = true
					found = true
					break
				}
			}
		}
		if !found {
			t.Fatalf("expectation %d unmatched: %+v\nhistory: %+v", i, want, all)
		}
	}
	if !canceledFound {
		t.Fatal("no canceled outcome recorded")
	}

	// Client metadata appears verbatim where reported.
	var metadataEntry *requestEntry
	for i := range all {
		if all[i].Outcome.RequestID == metadataQuery {
			metadataEntry = &all[i]
		}
	}
	if metadataEntry == nil {
		t.Fatal("metadata query missing from history")
	}
	if metadataEntry.Admission.Client.Harness != "suite<2" ||
		metadataEntry.Admission.Client.Agent != "devin" ||
		metadataEntry.Admission.Client.Model != "swe-2" ||
		metadataEntry.Admission.Client.RunID != "r-7" {
		t.Fatalf("client metadata = %+v", metadataEntry.Admission.Client)
	}

	// Detail checks on requests that carried evidence.
	detail := requestDetailByID(t, endpoint, tokenA, queryA, http.StatusOK)
	if detail.Evidence == nil || detail.Evidence.Kind != "trail" ||
		len(detail.Evidence.Sources) != 1 || detail.Evidence.Sources[0] == "" {
		t.Fatalf("query detail evidence = %+v", detail.Evidence)
	}
	if detail.Outcome.RequestID != queryA {
		t.Fatalf("detail request = %q, want %q", detail.Outcome.RequestID, queryA)
	}

	deniedDetail := requestDetailByID(t, endpoint, tokenA, deniedQuery, http.StatusOK)
	if deniedDetail.Outcome.State != "denied" || deniedDetail.Evidence == nil || deniedDetail.Evidence.Kind != "trail" {
		t.Fatalf("denied detail = %+v", deniedDetail)
	}

	replayFound := false
	for _, entry := range all {
		if entry.Outcome.State == "no_op" && entry.Admission.Operation == "note" && entry.Admission.Principal == "writer-b" {
			replayFound = true
			d := requestDetailByID(t, endpoint, tokenA, entry.Outcome.RequestID, http.StatusOK)
			if d.Evidence == nil || d.Evidence.Kind != "note" ||
				d.Evidence.ID != noteCreated.ID || d.Evidence.NoteAuthor != "writer-a" {
				t.Fatalf("no-op note detail evidence = %+v", d.Evidence)
			}
			evidenceJSON, _ := json.Marshal(d.Evidence)
			if strings.Contains(string(evidenceJSON), noteText) {
				t.Fatal("note evidence leaks note text")
			}
		}
	}
	if !replayFound {
		t.Fatal("no-op note outcome not found")
	}

	// No request payload may contain text, terms, or spoofed labels.
	historyJSON, _ := json.Marshal(all)
	for _, leak := range []string{noteText, "walrus protocol", "\"admin\"", "hidden.md"} {
		if strings.Contains(string(historyJSON), leak) {
			t.Fatalf("history leaks %q", leak)
		}
	}

	daemon.stop(t)

	// Seed one stale admission, then restart: recovery marks it interrupted.
	staleID := rand.Text()
	staleRecord := fmt.Sprintf(`{"authenticated":true,"operation":"note","principal":"writer-a","received_at_usec":%d,"request_id":%q}`,
		time.Now().UnixMicro(), staleID)
	staleLeaf := sha256.Sum256(append([]byte{0}, staleRecord...))
	staleRecordID := sha256.Sum256([]byte(staleID))
	rawExec(t, run.store, fmt.Sprintf(`INSERT INTO request_admissions(id, record_json) VALUES(x'%s', x'%s');
		INSERT INTO log(seq, kind, record_id, source_id, leaf_hash)
		SELECT COALESCE(MAX(seq), 0) + 1, 'request_admission', x'%s', NULL, x'%s' FROM log;`,
		hex.EncodeToString(staleRecordID[:]), hex.EncodeToString([]byte(staleRecord)),
		hex.EncodeToString(staleRecordID[:]), hex.EncodeToString(staleLeaf[:])))

	daemon = startDaemon(t, run.store, configPath)
	endpoint = daemon.url

	staleDetail := requestDetailByID(t, endpoint, tokenA, staleID, http.StatusOK)
	if staleDetail.Outcome.State != "interrupted" || staleDetail.Outcome.ErrorCode != "interrupted" {
		t.Fatalf("stale admission outcome = %q/%q, want interrupted",
			staleDetail.Outcome.State, staleDetail.Outcome.ErrorCode)
	}
	daemon.stop(t)

	// The injected outcome fault rolls back the note, its fallback outcome
	// write also fails, and the health probe reports coverage loss.
	rawExec(t, run.store, `CREATE TRIGGER outcome_fault BEFORE INSERT ON request_outcomes
		BEGIN SELECT RAISE(ABORT, 'injected outcome fault'); END;`)
	daemon = startDaemon(t, run.store, configPath)
	endpoint = daemon.url
	getNative(t, endpoint+"/healthz", "", http.StatusOK, "health before fault")
	faultRequestID, _ := postAudit(t, "faulted note", endpoint+"/v1/notes", tokenA,
		map[string]any{"scope": map[string]any{"source": "memory"}, "text": "this note must not commit"},
		http.StatusServiceUnavailable)
	getNative(t, endpoint+"/healthz", "", http.StatusServiceUnavailable, "health after fault")

	daemon.stop(t)
	rawExec(t, run.store, `DROP TRIGGER outcome_fault`)

	// The faulted note never committed.
	_, notesOut, _ := run.runExit("notes", "export", "--source", "memory")
	if strings.Contains(notesOut, "this note must not commit") {
		t.Fatalf("faulted note committed: %s", notesOut)
	}

	daemon = startDaemon(t, run.store, configPath)
	endpoint = daemon.url

	// Restart resolves the faulted admission to interrupted; health recovers.
	faultDetail := requestDetailByID(t, endpoint, tokenA, faultRequestID, http.StatusOK)
	if faultDetail.Outcome.State != "interrupted" {
		t.Fatalf("faulted request outcome = %q, want interrupted", faultDetail.Outcome.State)
	}
	getNative(t, endpoint+"/healthz", "", http.StatusOK, "health after restart")

	// Pick one committed document change for the representation corruption
	// below; its detail carries the current representation identity.
	var changeRequestID string
	for _, entry := range all {
		if entry.Outcome.State == "committed" && entry.Outcome.Evidence.ItemChangeID != "" {
			changeRequestID = entry.Outcome.RequestID
			break
		}
	}
	if changeRequestID == "" {
		t.Fatal("no committed document change in history")
	}
	changeDetail := requestDetailByID(t, endpoint, tokenA, changeRequestID, http.StatusOK)
	if changeDetail.Evidence == nil || changeDetail.Evidence.Current == "" {
		t.Fatalf("document evidence = %+v", changeDetail.Evidence)
	}

	// A missing outcome record named by the log is corruption, not absence:
	// verify and startup recovery refuse rather than skipping the sequence.
	daemon.stop(t)
	missingOutcomeID := sha256.Sum256([]byte(queryA))
	rawExec(t, run.store, fmt.Sprintf(`CREATE TABLE outcome_backup AS SELECT * FROM request_outcomes WHERE id = x'%s';
		DELETE FROM request_outcomes WHERE id = x'%s'`,
		hex.EncodeToString(missingOutcomeID[:]), hex.EncodeToString(missingOutcomeID[:])))
	if code, _, _ := run.runExit("verify"); code == 0 {
		t.Fatal("verify passed with a missing outcome record")
	}
	if code, _, _ := run.runExit("verify", "--log"); code == 0 {
		t.Fatal("verify --log passed with a missing outcome record")
	}
	code, _, serveErr := run.runExit("serve", "--listen", "127.0.0.1:0", "--config", configPath)
	if code == 0 || !strings.Contains(serveErr, "integrity") {
		t.Fatalf("serve with missing outcome = %d, want integrity refusal: %s", code, serveErr)
	}
	rawExec(t, run.store, `INSERT INTO request_outcomes SELECT * FROM outcome_backup;
		DROP TABLE outcome_backup`)

	// A committed document whose named representation is gone refuses detail
	// as corruption, never as a missing request.
	rawExec(t, run.store, fmt.Sprintf(`CREATE TABLE representation_backup AS SELECT * FROM representations WHERE id = x'%s';
		DELETE FROM representations WHERE id = x'%s'`,
		changeDetail.Evidence.Current, changeDetail.Evidence.Current))
	daemon = startDaemon(t, run.store, configPath)
	endpoint = daemon.url
	getNative(t, endpoint+"/v1/requests/"+changeRequestID, tokenA, http.StatusInternalServerError,
		"missing representation detail")
	daemon.stop(t)
	rawExec(t, run.store, `INSERT INTO representations SELECT * FROM representation_backup;
		DROP TABLE representation_backup`)
	daemon = startDaemon(t, run.store, configPath)
	endpoint = daemon.url
	if status := requestHistoryStatus(t, endpoint, tokenA); status != http.StatusOK {
		t.Fatalf("restored history status = %d, want 200", status)
	}

	// Tampering with outcome bytes refuses both history and verify.
	daemon.stop(t)
	rawExec(t, run.store, `UPDATE request_outcomes SET record_json = X'7b22' WHERE rowid = 1`)
	if code, _, _ := run.runExit("verify"); code == 0 {
		t.Fatal("verify passed on a tampered outcome")
	}
	if code, _, _ := run.runExit("verify", "--log"); code == 0 {
		t.Fatal("verify --log passed on a tampered outcome")
	}

	daemon = startDaemon(t, run.store, configPath)
	endpoint = daemon.url
	if status := requestHistoryStatus(t, endpoint, tokenA); status != http.StatusInternalServerError {
		t.Fatalf("tampered history status = %d, want 500", status)
	}
	daemon.stop(t)
}

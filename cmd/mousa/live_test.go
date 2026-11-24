package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// liveConfig writes a native config with an operator and a plain principal and
// returns their tokens.
func liveConfig(t *testing.T) (configPath, operatorToken, memberToken string) {
	t.Helper()
	operatorToken = rand.Text()
	memberToken = rand.Text()
	config := fmt.Sprintf(`{"principals":[
		{"id":"op","token_sha256":%q,"read_sources":["docs","memory"],"write_sources":["docs","memory"],"operator":true},
		{"id":"member","token_sha256":%q,"read_sources":["docs"],"write_sources":[]}
	]}`, tokenHash(operatorToken), tokenHash(memberToken))
	configPath = filepath.Join(t.TempDir(), "live.json")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath, operatorToken, memberToken
}

// liveReply is one served response captured inside a goroutine; helpers that
// may run off the test goroutine report failures instead of calling t.Fatal.
type liveReply struct {
	status int
	body   []byte
	err    error
}

// postLive sends one JSON body to the served API without test-only helpers so
// it is safe to run inside a goroutine.
func postLive(ctx context.Context, url, token string, body []byte) liveReply {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return liveReply{err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return liveReply{err: err}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	return liveReply{status: response.StatusCode, body: data, err: err}
}

// sseEvent is one parsed Server-Sent-Events record; comments arrive with kind
// "comment" so tests can confirm the connection preamble.
type sseEvent struct {
	id   string
	kind string
	data string
}

// openStream attaches one SSE viewer and pumps parsed events onto a channel.
// The returned response body must be closed to unsubscribe.
func openStream(t *testing.T, endpoint, token, lastEventID string, wantStatus int) (*http.Response, chan sseEvent) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if response.StatusCode != wantStatus {
		response.Body.Close()
		t.Fatalf("stream status = %d, want %d", response.StatusCode, wantStatus)
	}
	if wantStatus != http.StatusOK {
		response.Body.Close()
		return response, nil
	}
	if media := response.Header.Get("Content-Type"); !strings.HasPrefix(media, "text/event-stream") {
		response.Body.Close()
		t.Fatalf("stream content type = %q", media)
	}
	events := make(chan sseEvent, 512)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(response.Body)
		var id, kind string
		var data []string
		dispatch := func() {
			if kind == "" && id == "" && len(data) == 0 {
				return
			}
			events <- sseEvent{id: id, kind: kind, data: strings.Join(data, "\n")}
			id, kind, data = "", "", nil
		}
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				dispatch()
			case strings.HasPrefix(line, ":"):
				dispatch()
				events <- sseEvent{kind: "comment", data: line}
			default:
				field, value, _ := strings.Cut(line, ":")
				value = strings.TrimPrefix(value, " ")
				switch field {
				case "id":
					id = value
				case "event":
					kind = value
				case "data":
					data = append(data, value)
				}
			}
		}
	}()
	return response, events
}

func nextEvent(t *testing.T, events chan sseEvent, label string) sseEvent {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatalf("stream closed while waiting for %s", label)
		}
		return event
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return sseEvent{}
	}
}

// seqOf extracts the numeric sequence from an epoch:seq event id.
func seqOf(id string) uint64 {
	_, raw, _ := strings.Cut(id, ":")
	seq, _ := strconv.ParseUint(raw, 10, 64)
	return seq
}

// frameOf decodes one stage frame's data payload.
func frameOf(t *testing.T, event sseEvent) stageFrame {
	t.Helper()
	var frame stageFrame
	if err := json.Unmarshal([]byte(event.data), &frame); err != nil {
		t.Fatalf("stage frame is not JSON: %v: %s", err, event.data)
	}
	return frame
}

// awaitFrame returns the first stage frame matching keep, draining earlier
// events. It fails after a bounded number of events so a missing stage cannot
// stall the test.
func awaitFrame(t *testing.T, events chan sseEvent, keep func(stageFrame) bool, label string) stageFrame {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("stream closed while waiting for %s", label)
			}
			if event.kind != "stage" {
				continue
			}
			frame := frameOf(t, event)
			if keep(frame) {
				return frame
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", label)
		}
	}
}

// collectUntilOutcome drains stage frames until the terminal outcome for one
// operation, returning every frame seen along with that outcome.
func collectUntilOutcome(t *testing.T, events chan sseEvent, operation mousa.RequestOperation, label string) ([]stageFrame, stageFrame) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	var frames []stageFrame
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("stream closed while waiting for %s", label)
			}
			if event.kind != "stage" {
				continue
			}
			frame := frameOf(t, event)
			frames = append(frames, frame)
			if frame.Stage == mousa.StageOutcome && frame.Phase == mousa.StagePhaseTerminal && frame.Operation == operation {
				return frames, frame
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", label)
		}
	}
}

func hasStage(frames []stageFrame, requestID string, stage mousa.Stage) bool {
	for _, frame := range frames {
		if frame.RequestID == requestID && frame.Stage == stage {
			return true
		}
	}
	return false
}

// startLiveService runs one in-process service on a real loopback listener.
func startLiveService(t *testing.T, service *nativeService, addr string) (*http.Server, net.Listener) {
	t.Helper()
	var listener net.Listener
	var err error
	for range 40 {
		listener, err = net.Listen("tcp", addr)
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	server := &http.Server{Handler: service.routes()}
	go func() { _ = server.Serve(listener) }()
	return server, listener
}

func stopLiveService(t *testing.T, server *http.Server, listener net.Listener) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
	_ = server.Close()
	_ = listener.Close()
}

// TestServeLiveStagesAndResume requires the operator stream to show measured
// stage boundaries while a request is in flight, mark ring losses with
// explicit gaps, and resume across a service restart by epoch.
func TestServeLiveStagesAndResume(t *testing.T) {
	run, _ := setup(t)
	run.run(false, "access", "--source", "docs", "--create", "allow")
	run.run(false, "access", "--source", "memory", "--create", "allow")
	run.runInput(record("seed.md", "Walruses stage live request evidence.")+"\n", false, "sync", "--source", "docs")
	configPath, operatorToken, memberToken := liveConfig(t)
	principals, err := loadNativeConfig(configPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	store, err := sqlite.Open(t.Context(), run.store)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	service := newNativeService(store, principals)
	server, listener := startLiveService(t, service, "127.0.0.1:0")
	defer stopLiveService(t, server, listener)
	endpoint := "http://" + listener.Addr().String()

	// Nonoperator and unauthenticated viewers are refused before streaming;
	// a wrong credential is denied separately from a missing one.
	openStream(t, endpoint, memberToken, "", http.StatusForbidden)
	openStream(t, endpoint, "", "", http.StatusUnauthorized)
	openStream(t, endpoint, rand.Text(), "", http.StatusUnauthorized)

	// An open monitor serves the metadata-only monitor reads without a
	// credential; request endpoints still authenticate.
	open := newNativeService(store, principals)
	open.openMonitor = true
	openServer, openListener := startLiveService(t, open, "127.0.0.1:0")
	openEndpoint := "http://" + openListener.Addr().String()
	openViewer, _ := openStream(t, openEndpoint, "", "", http.StatusOK)
	openViewer.Body.Close()
	openQuery, err := http.Post(openEndpoint+"/v1/query", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("open monitor query: %v", err)
	}
	openQuery.Body.Close()
	if openQuery.StatusCode != http.StatusUnauthorized {
		t.Fatalf("open monitor query without credential: status = %d, want 401", openQuery.StatusCode)
	}
	// An open monitor also shows what was asked and what came back.
	asked := postLive(t.Context(), openEndpoint+"/v1/query", memberToken, []byte(`{"scope":{"source":"docs"},"text":"walruses"}`))
	if asked.err != nil || asked.status != http.StatusOK {
		t.Fatalf("open monitor member query: %+v", asked)
	}
	var askedReply nativeReply
	if err := json.Unmarshal(asked.body, &askedReply); err != nil {
		t.Fatalf("open monitor query reply: %v", err)
	}
	detailResponse, err := http.Get(openEndpoint + "/v1/requests/" + askedReply.RequestID)
	if err != nil {
		t.Fatalf("open monitor detail: %v", err)
	}
	detailBody, _ := io.ReadAll(detailResponse.Body)
	detailResponse.Body.Close()
	for _, want := range []string{`"query":"walruses"`, "Walruses stage live request evidence."} {
		if !strings.Contains(string(detailBody), want) {
			t.Fatalf("open monitor detail = %s, want %s", detailBody, want)
		}
	}
	stopLiveService(t, openServer, openListener)

	// The stream attaches before the request; its preamble is a comment.
	viewer, events := openStream(t, endpoint, operatorToken, "", http.StatusOK)
	preamble := nextEvent(t, events, "connected comment")
	if preamble.kind != "comment" || !strings.Contains(preamble.data, "connected") {
		t.Fatalf("stream preamble = %+v, want connected comment", preamble)
	}

	// A stage boundary inside the live request blocks it; the stream still
	// observes the stage start because the ring append precedes the hook.
	hold := make(chan struct{})
	service.stageBoundary = func(frame stageFrame) {
		if frame.Stage == mousa.StageRetrieval && frame.Phase == mousa.StagePhaseStart {
			<-hold
		}
	}
	resultCh := make(chan liveReply, 1)
	go func() {
		resultCh <- postLive(t.Context(), endpoint+"/v1/query", operatorToken,
			[]byte(`{"scope":{"source":"docs"},"text":"walruses"}`))
	}()
	// Collect every stage frame in stream order; the query is the only
	// request in flight so all frames belong to it.
	var queryStages []stageFrame
	var lastEventID string
	var seen stageFrame
	for seen.RequestID == "" {
		event := nextEvent(t, events, "retrieval start")
		if event.kind != "stage" {
			continue
		}
		if event.id != "" {
			lastEventID = event.id
		}
		frame := frameOf(t, event)
		queryStages = append(queryStages, frame)
		if frame.Stage == mousa.StageRetrieval && frame.Phase == mousa.StagePhaseStart {
			seen = frame
		}
	}
	select {
	case <-resultCh:
		t.Fatal("query completed while its retrieval stage was held")
	default:
	}
	close(hold)
	result := <-resultCh
	if result.err != nil || result.status != http.StatusOK {
		t.Fatalf("query reply = %v/%d: %s", result.err, result.status, result.body)
	}
	service.stageBoundary = nil

	// The same request completes in stream order: measured boundaries, the
	// terminal committed outcome, then the response write.
	for {
		event := nextEvent(t, events, "query terminal frames")
		if event.kind != "stage" {
			continue
		}
		if event.id != "" {
			lastEventID = event.id
		}
		frame := frameOf(t, event)
		if frame.RequestID != seen.RequestID {
			continue
		}
		queryStages = append(queryStages, frame)
		if frame.Stage == mousa.StageOutcome {
			if frame.Phase != mousa.StagePhaseTerminal || frame.State != mousa.RequestStateCommitted {
				t.Fatalf("query outcome frame = %+v", frame)
			}
		}
		if frame.Stage == mousa.StageResponse && frame.Phase == mousa.StagePhaseFinish {
			break
		}
	}
	sequence := []struct {
		stage mousa.Stage
		phase mousa.StagePhase
	}{
		{mousa.StageReceive, mousa.StagePhaseStart},
		{mousa.StageReceive, mousa.StagePhaseFinish},
		{mousa.StageAdmission, mousa.StagePhaseFinish},
		{mousa.StageAccess, mousa.StagePhaseFinish},
		{mousa.StageRetrieval, mousa.StagePhaseStart},
		{mousa.StageRetrieval, mousa.StagePhaseFinish},
		{mousa.StageRank, mousa.StagePhaseFinish},
		{mousa.StagePack, mousa.StagePhaseFinish},
		{mousa.StageCommit, mousa.StagePhaseFinish},
		{mousa.StageOutcome, mousa.StagePhaseTerminal},
		{mousa.StageResponse, mousa.StagePhaseFinish},
	}
	position := 0
	for _, want := range sequence {
		found := false
		for ; position < len(queryStages); position++ {
			if queryStages[position].Stage == want.stage && queryStages[position].Phase == want.phase {
				found = true
				position++
				break
			}
		}
		if !found {
			t.Fatalf("missing ordered stage %s/%s in %+v", want.stage, want.phase, queryStages)
		}
	}

	// A note reports its measured write stages.
	noteCh := make(chan liveReply, 1)
	go func() {
		noteCh <- postLive(t.Context(), endpoint+"/v1/notes", operatorToken,
			[]byte(`{"scope":{"source":"memory"},"text":"operators measure every served stage"}`))
	}()
	noteFrames, noteOutcome := collectUntilOutcome(t, events, mousa.RequestOperationNote, "note outcome")
	if reply := <-noteCh; reply.err != nil || reply.status != http.StatusOK {
		t.Fatalf("note reply = %v/%d", reply.err, reply.status)
	}
	if noteOutcome.State != mousa.RequestStateCommitted {
		t.Fatalf("note outcome state = %q", noteOutcome.State)
	}
	for _, want := range []mousa.Stage{mousa.StageCitations, mousa.StageNote, mousa.StageRepresent, mousa.StageIndex, mousa.StageCommit} {
		if !hasStage(noteFrames, noteOutcome.RequestID, want) {
			t.Fatalf("note request %s missing %s frame in %+v", noteOutcome.RequestID, want, noteFrames)
		}
	}

	// A document add reports access, ingest, represent, index, commit.
	docCh := make(chan liveReply, 1)
	go func() {
		docCh <- postLive(t.Context(), endpoint+"/v1/documents", operatorToken,
			[]byte(`{"source":"docs","record":{"id":"live.md","text":"Live documents carry stages."}}`))
	}()
	docFrames, docOutcome := collectUntilOutcome(t, events, mousa.RequestOperationDocument, "document outcome")
	if reply := <-docCh; reply.err != nil || reply.status != http.StatusOK {
		t.Fatalf("document reply = %v/%d: %s", reply.err, reply.status, reply.body)
	}
	if docOutcome.State != mousa.RequestStateCommitted {
		t.Fatalf("document outcome state = %q", docOutcome.State)
	}
	for _, want := range []mousa.Stage{mousa.StageAccess, mousa.StageIngest, mousa.StageRepresent, mousa.StageIndex, mousa.StageCommit} {
		if !hasStage(docFrames, docOutcome.RequestID, want) {
			t.Fatalf("document request %s missing %s frame in %+v", docOutcome.RequestID, want, docFrames)
		}
	}

	// A denied request records the denied outcome and never releases content.
	deniedCh := make(chan liveReply, 1)
	go func() {
		deniedCh <- postLive(t.Context(), endpoint+"/v1/documents", memberToken,
			[]byte(`{"source":"docs","record":{"id":"denied.md","text":"member must not write"}}`))
	}()
	deniedOutcome := awaitFrame(t, events, func(f stageFrame) bool {
		return f.Stage == mousa.StageOutcome && f.Principal == "member"
	}, "denied outcome")
	denied := <-deniedCh
	if denied.err != nil || denied.status != http.StatusForbidden {
		t.Fatalf("member document reply = %v/%d: %s", denied.err, denied.status, denied.body)
	}
	if deniedOutcome.State != mousa.RequestStateDenied {
		t.Fatalf("denied outcome state = %q", deniedOutcome.State)
	}
	if strings.Contains(string(denied.body), "member must not write") || strings.Contains(string(denied.body), "denied.md") {
		t.Fatalf("denied reply leaked content: %s", denied.body)
	}

	// The subscriber cap refuses extra viewers without touching operations.
	var held []*http.Response
	for range stageMaxSubscribers - 1 {
		extra, _ := openStream(t, endpoint, operatorToken, "", http.StatusOK)
		held = append(held, extra)
	}
	openStream(t, endpoint, operatorToken, "", http.StatusServiceUnavailable)
	for _, extra := range held {
		extra.Body.Close()
	}

	// A small ring overflows behind a non-reading viewer; writers still
	// complete and a resuming viewer sees an explicit gap.
	service.hub = newStageHub(4)
	staleViewer, _ := openStream(t, endpoint, operatorToken, "", http.StatusOK)
	for range 3 {
		reply := postLive(t.Context(), endpoint+"/v1/query", operatorToken,
			[]byte(`{"scope":{"source":"docs"},"text":"walruses"}`))
		if reply.err != nil || reply.status != http.StatusOK {
			t.Fatalf("query under overflow = %v/%d: %s", reply.err, reply.status, reply.body)
		}
	}
	resume, resumedEvents := openStream(t, endpoint, operatorToken, service.hub.epoch+":1", http.StatusOK)
	nextEvent(t, resumedEvents, "resumed preamble")
	gapEvent := nextEvent(t, resumedEvents, "overflow gap")
	if gapEvent.kind != "gap" || !strings.Contains(gapEvent.data, "overflow") {
		t.Fatalf("resumed stream first event = %+v, want overflow gap", gapEvent)
	}
	// The gap marker is advisory: its id names the skipped-to position and
	// sorts before every retained frame that follows it.
	var firstRetained sseEvent
	for firstRetained.kind != "stage" {
		firstRetained = nextEvent(t, resumedEvents, "first retained frame")
	}
	if seqOf(gapEvent.id) == 0 || seqOf(gapEvent.id) >= seqOf(firstRetained.id) {
		t.Fatalf("gap id %q does not precede first retained id %q", gapEvent.id, firstRetained.id)
	}
	if frame := frameOf(t, firstRetained); frame.Stage == mousa.StageOutcome && frame.State != mousa.RequestStateCommitted {
		t.Fatalf("retained outcome = %+v", frame)
	}
	// Writes keep flowing while the stale viewer never reads.
	reply := postLive(t.Context(), endpoint+"/v1/query", operatorToken,
		[]byte(`{"scope":{"source":"docs"},"text":"walruses"}`))
	if reply.err != nil || reply.status != http.StatusOK {
		t.Fatalf("query with stalled viewer = %v/%d: %s", reply.err, reply.status, reply.body)
	}
	var lastResumedID string
	for lastResumedID == "" {
		event := nextEvent(t, resumedEvents, "continued writes")
		if event.kind != "stage" {
			continue
		}
		frame := frameOf(t, event)
		if frame.Stage == mousa.StageResponse && frame.Phase == mousa.StagePhaseFinish {
			lastResumedID = event.id
		}
	}
	// Resuming from the last seen id replays nothing: the tiny ring may
	// honestly gap once more under a whole new request batch, but every
	// delivered stage frame carries a strictly newer sequence.
	againResume, againResumeEvents := openStream(t, endpoint, operatorToken, lastResumedID, http.StatusOK)
	nextEvent(t, againResumeEvents, "post-gap preamble")
	reply = postLive(t.Context(), endpoint+"/v1/query", operatorToken,
		[]byte(`{"scope":{"source":"docs"},"text":"walruses"}`))
	if reply.err != nil || reply.status != http.StatusOK {
		t.Fatal("query after gap resume failed")
	}
	lastSeq := seqOf(lastResumedID)
	for {
		event := nextEvent(t, againResumeEvents, "post-gap frames")
		if event.kind == "gap" {
			continue
		}
		if event.kind != "stage" {
			continue
		}
		if seq := seqOf(event.id); seq <= lastSeq {
			t.Fatalf("post-gap resume replayed seq %d at/before %s", seq, lastResumedID)
		}
		break
	}
	againResume.Body.Close()
	staleViewer.Body.Close()
	resume.Body.Close()
	viewer.Body.Close()

	// A retained cursor resumes with no gap: the first record after the
	// preamble is a stage frame, not a gap marker.
	service.hub = newStageHub(256)
	dropped, droppedEvents := openStream(t, endpoint, operatorToken, "", http.StatusOK)
	nextEvent(t, droppedEvents, "tail preamble")
	reply = postLive(t.Context(), endpoint+"/v1/query", operatorToken,
		[]byte(`{"scope":{"source":"docs"},"text":"walruses"}`))
	if reply.err != nil || reply.status != http.StatusOK {
		t.Fatal("query before cursor resume failed")
	}
	var tailID string
	for tailID == "" {
		event := nextEvent(t, droppedEvents, "tail frame")
		if event.kind == "stage" && event.id != "" {
			tailID = event.id
		}
	}
	dropped.Body.Close()
	caught, caughtEvents := openStream(t, endpoint, operatorToken, tailID, http.StatusOK)
	nextEvent(t, caughtEvents, "resume preamble")
	resumed := nextEvent(t, caughtEvents, "resumed frames")
	if resumed.kind != "stage" {
		t.Fatalf("retained resume delivered %+v, want stage frames without a gap", resumed)
	}
	caught.Body.Close()

	// A restarted service announces a new epoch; last-seen ids produce a
	// restart gap and durable request ids stay verifiable.
	stopLiveService(t, server, listener)
	restarted := newNativeService(store, principals)
	server, listener = startLiveService(t, restarted, listener.Addr().String())
	again, againEvents := openStream(t, endpoint, operatorToken, lastEventID, http.StatusOK)
	nextEvent(t, againEvents, "restart preamble")
	restartGap := nextEvent(t, againEvents, "restart gap")
	if restartGap.kind != "gap" || !strings.Contains(restartGap.data, "restarted") {
		t.Fatalf("restart resume first event = %+v, want restarted gap", restartGap)
	}
	reply = postLive(t.Context(), endpoint+"/v1/query", operatorToken,
		[]byte(`{"scope":{"source":"docs"},"text":"walruses"}`))
	if reply.err != nil || reply.status != http.StatusOK {
		t.Fatalf("post-restart query = %v/%d: %s", reply.err, reply.status, reply.body)
	}
	postOutcome := awaitFrame(t, againEvents, func(f stageFrame) bool {
		return f.Stage == mousa.StageOutcome && f.Phase == mousa.StagePhaseTerminal
	}, "post-restart outcome")
	if postOutcome.State != mousa.RequestStateCommitted {
		t.Fatalf("post-restart outcome = %+v", postOutcome)
	}
	// The durable record of the pre-restart request verifies after restart.
	getNative(t, endpoint+"/v1/requests/"+seen.RequestID, operatorToken, http.StatusOK, "durable request detail")
	again.Body.Close()
}

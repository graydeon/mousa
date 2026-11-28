package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The serve tests run the real built daemon and the real CLI against a real
// SQLite store seeded offline, so identity, grants, and evidence flow through
// the same code paths operators use.

type daemonHandle struct {
	cmd    *exec.Cmd
	url    string
	stderr *bytes.Buffer
}

// startDaemon runs `mousa serve` on an ephemeral loopback port and reads the
// single startup line that announces the bound address.
func startDaemon(t *testing.T, store, config string) *daemonHandle {
	t.Helper()
	return startDaemonFlags(t, store, config)
}

func startDaemonFlags(t *testing.T, store, config string, extra ...string) *daemonHandle {
	t.Helper()
	cmd := exec.Command(cliBinary, append([]string{"-store", store, "serve", "--listen", "127.0.0.1:0", "--config", config}, extra...)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	lines := make(chan string, 8)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	var url string
	timer := time.NewTimer(60 * time.Second)
	defer timer.Stop()
	for url == "" {
		select {
		case line, ok := <-lines:
			if !ok {
				// Wait reaps the process and lets the stderr copy finish
				// before the buffer is read for diagnostics.
				_ = cmd.Wait()
				t.Fatalf("daemon exited before announcing its address: %s", stderr)
			}
			if strings.HasPrefix(line, "http://") {
				url = line
			}
		case <-timer.C:
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("daemon did not announce its address: %s", stderr)
		}
	}
	handle := &daemonHandle{cmd: cmd, url: url, stderr: stderr}
	t.Cleanup(func() { handle.stop(t) })
	return handle
}

// stop interrupts the daemon and requires a clean graceful exit.
func (d *daemonHandle) stop(t *testing.T) {
	t.Helper()
	if d.cmd.ProcessState != nil && d.cmd.ProcessState.Exited() {
		return
	}
	if err := d.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("interrupt daemon: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- d.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon did not shut down cleanly: %v\n%s", err, d.stderr)
		}
	case <-time.After(60 * time.Second):
		_ = d.cmd.Process.Kill()
		_ = d.cmd.Wait()
		t.Fatalf("daemon ignored graceful shutdown: %s", d.stderr)
	}
}

// runEndpoint invokes the CLI against a served endpoint with an optional
// bearer token in the environment and reports the raw outcome.
func runEndpoint(t *testing.T, endpoint, token, input string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(cliBinary, append([]string{"-endpoint", endpoint}, args...)...)
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "MOUSA_TOKEN=") {
			env = append(env, entry)
		}
	}
	if token != "" {
		env = append(env, "MOUSA_TOKEN="+token)
	}
	cmd.Env = env
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run -endpoint %v: %v", args, err)
	}
	return exitErr.ExitCode(), stdout.String(), stderr.String()
}

// postNative sends one JSON body to the served API and returns the raw reply.
func postNative(t *testing.T, url, token string, body []byte) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, data
}

func envelopeError(t *testing.T, data []byte) (requestID, code string) {
	t.Helper()
	var reply struct {
		RequestID string `json:"request_id"`
		Error     *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatalf("reply is not an envelope: %s", data)
	}
	if reply.Error == nil {
		t.Fatalf("reply is not an error envelope: %s", data)
	}
	return reply.RequestID, reply.Error.Code
}

func envelopeResult(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var reply struct {
		RequestID string          `json:"request_id"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatalf("reply is not an envelope: %s", data)
	}
	if reply.RequestID == "" {
		t.Fatalf("reply has no request_id: %s", data)
	}
	var result map[string]any
	if err := json.Unmarshal(reply.Result, &result); err != nil {
		t.Fatalf("result is not an object: %s", data)
	}
	return result
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TestServeCLIAndSourceGrants(t *testing.T) {
	run, _ := setup(t)

	// Seed sources and access decisions offline before the daemon owns the
	// store: docs and memory are the working sources, secret is denied to
	// every configured principal, and gone is withdrawn after seeding.
	run.run(false, "access", "--source", "docs", "--create", "allow")
	run.run(false, "access", "--source", "memory", "--create", "allow")
	run.run(false, "access", "--source", "secret", "--create", "allow")
	run.run(false, "access", "--source", "gone", "--create", "allow")
	run.runInput(record("hidden.md", "Walruses hide quietly in the secret annex.")+"\n", false, "sync", "--source", "secret")
	run.runInput(record("old.md", "Walruses once roamed the withdrawn shore.")+"\n", false, "sync", "--source", "gone")
	run.run(false, "withdraw", "--source", "gone")

	writerToken := rand.Text()
	readerToken := rand.Text()
	config := fmt.Sprintf(`{"principals":[
		{"id":"writer","token_sha256":%q,"read_sources":["docs","memory","gone"],"write_sources":["docs","memory","gone"]},
		{"id":"reader","token_sha256":%q,"read_sources":["docs"],"write_sources":[]}
	]}`, tokenHash(writerToken), tokenHash(readerToken))
	configPath := filepath.Join(t.TempDir(), "serve-config.json")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	daemon := startDaemon(t, run.store, configPath)
	endpoint := daemon.url

	// A second daemon must refuse to take ownership of the same store.
	if code, _, second := run.runExit("serve", "--listen", "127.0.0.1:0", "--config", configPath); code == 0 {
		t.Fatalf("duplicate daemon took ownership of the store: %s", second)
	}

	// -endpoint and -store are mutually exclusive.
	{
		cmd := exec.Command(cliBinary, "-store", run.store, "-endpoint", endpoint, "query", "--source", "docs", "walrus")
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err == nil {
			t.Fatalf("-store and -endpoint together should fail")
		}
	}

	// Endpoint mode requires the bearer credential in the environment.
	if code, _, _ := runEndpoint(t, endpoint, "", "", "query", "--source", "docs", "walrus"); code == 0 {
		t.Fatalf("endpoint query without MOUSA_TOKEN should fail")
	}

	// Directory and admin forms stay offline; endpoint mode never falls back
	// to opening the store or to another transport.
	for _, args := range [][]string{
		{"query", "docs", "walrus"},
		{"status", "--source", "docs"},
		{"access", "--source", "docs", "deny"},
		{"verify"},
		{"sync", t.TempDir()},
		{"sync", "--source", "docs", "--allow"},
	} {
		if code, _, _ := runEndpoint(t, endpoint, writerToken, "", args...); code == 0 {
			t.Fatalf("endpoint command %v should fail", args)
		}
	}

	// Seed the docs source through the endpoint, one request per row.
	code, stdout, stderr := runEndpoint(t, endpoint, writerToken,
		record("a.md", "Walruses gather quietly on the northern shore.")+"\n",
		"sync", "--source", "docs")
	if code != 0 {
		t.Fatalf("endpoint sync failed: %d %s %s", code, stdout, stderr)
	}
	var syncReport map[string]any
	if err := json.Unmarshal([]byte(stdout), &syncReport); err != nil || lenOf(syncReport, "added") != 1 {
		t.Fatalf("endpoint sync should report one added item: %s", stdout)
	}

	// Query returns evidence only inside the writer's granted scope.
	code, stdout, stderr = runEndpoint(t, endpoint, writerToken, "", "query", "--full", "--source", "docs", "walruses")
	if code != 0 {
		t.Fatalf("endpoint query failed: %d %s", code, stderr)
	}
	var found map[string]any
	if err := json.Unmarshal([]byte(stdout), &found); err != nil {
		t.Fatalf("endpoint query is not JSON: %s", stdout)
	}
	if item := firstItem(t, found); item != "a.md" {
		t.Fatalf("endpoint query evidence item = %q: %s", item, stdout)
	}
	if decision := sourceDecision(t, found); decision["outcome"] != "allow" {
		t.Fatalf("granted source decision = %v", decision)
	}

	// The candidate limit accepts values to 200 and records them in the
	// trail; 201 is rejected as an invalid query.
	status, _, data := postNative(t, endpoint+"/v1/query", writerToken, []byte(`{"scope":{"source":"docs"},"text":"walruses","limit":200}`))
	if status != http.StatusOK {
		t.Fatalf("limit-200 query = %d: %s", status, data)
	}
	limited := envelopeResult(t, data)
	request, _ := limited["trail"].(map[string]any)["request"].(map[string]any)
	if request["limit"] != float64(200) {
		t.Fatalf("limit-200 trail request = %v", limited["trail"])
	}
	status, _, data = postNative(t, endpoint+"/v1/query", writerToken, []byte(`{"scope":{"source":"docs"},"text":"walruses","limit":201}`))
	if status != http.StatusBadRequest {
		t.Fatalf("limit-201 query = %d, want 400: %s", status, data)
	}
	code, stdout, stderr = runEndpoint(t, endpoint, writerToken, "", "query", "--full", "--source", "docs", "--limit", "200", "walruses")
	if code != 0 {
		t.Fatalf("endpoint query --limit 200 failed: %d %s", code, stderr)
	}
	var limitedCLI map[string]any
	if err := json.Unmarshal([]byte(stdout), &limitedCLI); err != nil {
		t.Fatalf("endpoint query is not JSON: %s", stdout)
	}
	cliRequest, _ := trailOf(limitedCLI)["request"].(map[string]any)
	if cliRequest["limit"] != float64(200) {
		t.Fatalf("endpoint --limit 200 trail request = %v", trailOf(limitedCLI)["request"])
	}

	// A document add and a second revision keep one source/item identity
	// while the representation moves forward.
	docURL := endpoint + "/v1/documents"
	status, _, data = postNative(t, docURL, writerToken, []byte(`{"source":"docs","record":{"id":"b.md","text":"Ferrets patrol the lower creek."}}`))
	if status != http.StatusOK {
		t.Fatalf("document add = %d: %s", status, data)
	}
	added := envelopeResult(t, data)
	if added["item_id"] != "b.md" || added["action"] != "added" || added["representation_id"] == "" {
		t.Fatalf("document add result: %s", data)
	}
	status, _, data = postNative(t, docURL, writerToken, []byte(`{"source":"docs","record":{"id":"b.md","text":"Ferrets patrol the expanded creek."}}`))
	if status != http.StatusOK {
		t.Fatalf("document update = %d: %s", status, data)
	}
	updated := envelopeResult(t, data)
	if updated["item_id"] != "b.md" || updated["action"] != "updated" {
		t.Fatalf("document update result: %s", data)
	}
	if updated["source_id"] != added["source_id"] || updated["representation_id"] == added["representation_id"] {
		t.Fatalf("revision changed item identity or reused representation:\n%s\n%s", added, updated)
	}
	_, queryOut, _ := runEndpoint(t, endpoint, writerToken, "", "query", "--full", "--source", "docs", "expanded")
	var revision map[string]any
	if err := json.Unmarshal([]byte(queryOut), &revision); err != nil || firstItem(t, revision) != "b.md" {
		t.Fatalf("revised document not retrievable: %s", queryOut)
	}

	// A remembered note records the authenticated principal as its author.
	code, noteOut, stderr := runEndpoint(t, endpoint, writerToken, "", "remember", "--source", "memory", "Walruses return to the shore at dusk.")
	if code != 0 {
		t.Fatalf("endpoint remember failed: %d %s", code, stderr)
	}
	var note map[string]any
	if err := json.Unmarshal([]byte(noteOut), &note); err != nil || note["author"] != "writer" {
		t.Fatalf("note did not record the authenticated writer: %s", noteOut)
	}

	// The read-only reader can query a granted source but cannot write.
	if code, out, readerErr := runEndpoint(t, endpoint, readerToken, "", "query", "--source", "docs", "walruses"); code != 0 {
		t.Fatalf("reader query on a granted source failed: %d %s %s", code, out, readerErr)
	}
	code, _, readerErr := runEndpoint(t, endpoint, readerToken, "", "remember", "--source", "memory", "reader cannot write")
	if code == 0 || !strings.Contains(readerErr, "source_denied") {
		t.Fatalf("read-only principal wrote a note: %d %s", code, readerErr)
	}
	code, _, readerErr = runEndpoint(t, endpoint, readerToken, record("r.md", "reader write attempt")+"\n", "sync", "--source", "docs")
	if code == 0 || !strings.Contains(readerErr, "source_denied") {
		t.Fatalf("read-only principal wrote a document: %d %s", code, readerErr)
	}

	// A scope mixing granted and ungranted sources is denied whole, and so
	// is a single ungranted source.
	for _, args := range [][]string{
		{"query", "--source", "docs", "--source", "secret", "walruses"},
		{"query", "--source", "secret", "walruses"},
	} {
		code, out, denyErr := runEndpoint(t, endpoint, writerToken, "", args...)
		if code == 0 || !strings.Contains(denyErr, "source_denied") || strings.Contains(out, "secret annex") {
			t.Fatalf("ungranted scope was not denied whole: %d %s %s", code, out, denyErr)
		}
	}
	status, _, data = postNative(t, docURL, writerToken, []byte(`{"source":"secret","record":{"id":"s.md","text":"writer cannot add here"}}`))
	if status != http.StatusForbidden {
		t.Fatalf("write to ungranted source = %d, want 403: %s", status, data)
	}

	// A withdrawn source records a denial with no evidence and accepts no writes.
	code, stdout, _ = runEndpoint(t, endpoint, writerToken, "", "query", "--source", "gone", "walruses")
	if code != 0 {
		t.Fatalf("query on a withdrawn source should report a denial trail: %d %s", code, stdout)
	}
	var gone map[string]any
	if err := json.Unmarshal([]byte(stdout), &gone); err != nil {
		t.Fatalf("withdrawn query is not JSON: %s", stdout)
	}
	if len(hits(gone)) != 0 || gone["access"] != "deny" {
		t.Fatalf("withdrawn source was not a compact denial: %s", stdout)
	}
	if trail, _ := gone["trail"].(string); trail == "" {
		t.Fatalf("withdrawn source compact denial has no trail string: %s", stdout)
	}
	code, _, denyErr := runEndpoint(t, endpoint, writerToken, record("w.md", "write into withdrawn")+"\n", "sync", "--source", "gone")
	if code == 0 || !strings.Contains(denyErr, "source_denied") {
		t.Fatalf("withdrawn source accepted a write: %d %s", code, denyErr)
	}

	// Credentials: missing and wrong bearer values are uniformly unauthorized.
	queryBody := []byte(`{"scope":{"source":"docs"},"text":"walruses"}`)
	for _, credential := range []struct {
		name  string
		token string
	}{
		{"missing", ""},
		{"wrong", "mousa-test-invalid-credential"},
	} {
		status, headers, data := postNative(t, endpoint+"/v1/query", credential.token, queryBody)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s credential = %d, want 401: %s", credential.name, status, data)
		}
		requestID, code := envelopeError(t, data)
		if code != "unauthorized" || requestID == "" || headers.Get("X-Mousa-Request-ID") != requestID {
			t.Fatalf("unauthorized reply lacks a stable code or request ID: %s", data)
		}
		if strings.Contains(string(data), writerToken) {
			t.Fatalf("error reply echoes the credential: %s", data)
		}
	}
	{
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/v1/query", bytes.NewReader(queryBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Add("Authorization", "Bearer "+writerToken)
		req.Header.Add("Authorization", "Bearer "+writerToken)
		resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("doubled Authorization header = %d, want 401", resp.StatusCode)
		}
	}

	// The body can never name a caller, request ID, or grant.
	for _, body := range []string{
		`{"caller":"admin","scope":{"source":"docs"},"text":"walruses"}`,
		`{"request_id":"spoofed","scope":{"source":"docs"},"text":"walruses"}`,
		`{"allow":true,"scope":{"source":"docs"},"text":"walruses"}`,
	} {
		status, _, data := postNative(t, endpoint+"/v1/query", writerToken, []byte(body))
		if status != http.StatusBadRequest {
			t.Fatalf("spoofed body %s = %d, want 400: %s", body, status, data)
		}
	}

	// Malformed and oversized bodies are refused with stable codes and IDs.
	status, headers, data := postNative(t, endpoint+"/v1/query", writerToken, []byte(`{"scope":`))
	if status != http.StatusBadRequest {
		t.Fatalf("malformed body = %d, want 400: %s", status, data)
	}
	firstID, errCode := envelopeError(t, data)
	if errCode != "invalid_request" || firstID == "" || headers.Get("X-Mousa-Request-ID") != firstID {
		t.Fatalf("malformed reply lacks code or request ID: %s", data)
	}
	status, _, data = postNative(t, endpoint+"/v1/query", writerToken, bytes.Repeat([]byte(" "), 64<<10+1))
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413: %s", status, data)
	}
	secondID, _ := envelopeError(t, data)
	if secondID == "" || secondID == firstID {
		t.Fatalf("request IDs are not unique per request: %q vs %q", firstID, secondID)
	}

	// There is no self-grant route and no unauthenticated readiness surface
	// beyond a bounded health check.
	status, _, _ = postNative(t, endpoint+"/v1/access", writerToken, []byte(`{"source":"secret","access":"allow"}`))
	if status != http.StatusNotFound {
		t.Fatalf("POST /v1/access = %d, want 404", status)
	}
	{
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+"/v1/query", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET /v1/query = %d, want 405", resp.StatusCode)
		}
	}
	{
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+"/healthz", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /healthz = %d, want 200", resp.StatusCode)
		}
	}
	{
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/v1/query", bytes.NewReader(queryBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Host = "8.8.8.8"
		req.Header.Set("Authorization", "Bearer "+writerToken)
		resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Fatalf("non-loopback Host was served: %d", resp.StatusCode)
		}
	}

	// A later bad JSONL row reports the committed prefix without a success reply.
	code, stdout, stderr = runEndpoint(t, endpoint, writerToken,
		record("pre.md", "Anteaters precede the malformed row.")+"\n"+"{bad row}\n",
		"sync", "--source", "docs")
	if code == 0 || len(stdout) != 0 {
		t.Fatalf("sync with a bad row succeeded: %d %s", code, stdout)
	}
	if !strings.Contains(stderr, "committed prefix: 1 row(s)") {
		t.Fatalf("committed prefix missing from stderr: %s", stderr)
	}

	// Cancellation: a real CLI holding a request open receives SIGINT and
	// exits, and the peer observes request-context cancellation.
	{
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		received := make(chan struct{})
		released := make(chan struct{})
		peer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// net/http only watches for client FIN once the request body
			// reaches EOF, so drain it before waiting for cancellation.
			_, _ = io.Copy(io.Discard, r.Body)
			close(received)
			<-r.Context().Done()
			close(released)
		})}
		serveDone := make(chan error, 1)
		go func() { serveDone <- peer.Serve(listener) }()
		cmd := exec.Command(cliBinary, "-endpoint", "http://"+listener.Addr().String(), "query", "--source", "docs", "walruses")
		env := []string{}
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "MOUSA_TOKEN=") {
				env = append(env, entry)
			}
		}
		cmd.Env = append(env, "MOUSA_TOKEN="+writerToken)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-received:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("peer never received the CLI request")
		}
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("interrupt CLI: %v", err)
		}
		select {
		case <-released:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("peer never observed request cancellation")
		}
		waitDone := make(chan error, 1)
		go func() { waitDone <- cmd.Wait() }()
		select {
		case err := <-waitDone:
			if err == nil {
				t.Fatal("interrupted CLI exited successfully")
			}
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("interrupted CLI did not exit")
		}
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		_ = peer.Shutdown(ctx)
		cancel()
		<-serveDone
	}

	// The daemon stays healthy until the graceful shutdown at cleanup.
	if code, out, errText := runEndpoint(t, endpoint, writerToken, "", "query", "--source", "docs", "walruses"); code != 0 {
		t.Fatalf("daemon did not survive the test flow: %d %s %s", code, out, errText)
	}
}

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	modernsqlite "modernc.org/sqlite"
)

type mcpTestClient struct {
	t       *testing.T
	session *mcp.ClientSession
	command *exec.Cmd
	schemas map[string]*jsonschema.Resolved
	sent    chan string
}

type mcpObservedTransport struct {
	mcp.Transport
	sent chan string
}

func (t *mcpObservedTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &mcpObservedConnection{Connection: connection, sent: t.sent}, nil
}

type mcpObservedConnection struct {
	mcp.Connection
	sent chan string
}

func (c *mcpObservedConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	if err := c.Connection.Write(ctx, message); err != nil {
		return err
	}
	if request, ok := message.(*jsonrpc.Request); ok {
		method := request.Method
		if method == "tools/call" {
			var params struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(request.Params, &params); err == nil {
				method = params.Name
			}
		}
		if method == "mousa_query" || method == "notifications/cancelled" {
			c.sent <- method
		}
	}
	return nil
}

func (c *mcpTestClient) waitSent(wanted string) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(c.t.Context(), 2*time.Second)
	defer cancel()
	for {
		select {
		case method := <-c.sent:
			if method == wanted {
				return
			}
		case <-ctx.Done():
			c.t.Fatalf("MCP client did not send %s: %v", wanted, ctx.Err())
		}
	}
}

func startMCPClient(t *testing.T, run testRun, caller string, writable bool, startup ...string) *mcpTestClient {
	t.Helper()
	args := []string{"-store", run.store, "mcp", "--caller", caller, "--source", "alpha", "--source", "beta"}
	if writable {
		args = append(args, "--ingest-source", "alpha", "--ingest-source", "beta")
	}
	args = append(args, startup...)
	command := exec.Command(run.binary, args...)
	client := mcp.NewClient(&mcp.Implementation{Name: "mousa-acceptance", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}})
	sent := make(chan string, 64)
	session, err := client.Connect(t.Context(), &mcpObservedTransport{Transport: &mcp.CommandTransport{Command: command, TerminateDuration: 2 * time.Second}, sent: sent}, nil)
	if err != nil {
		t.Fatalf("MCP initialize: %v", err)
	}
	c := &mcpTestClient{t: t, session: session, command: command, schemas: make(map[string]*jsonschema.Resolved), sent: sent}
	t.Cleanup(c.close)
	initialized := session.InitializeResult()
	if initialized.ProtocolVersion != mcpProtocolVersion || initialized.Capabilities.Tools == nil || initialized.Capabilities.Resources != nil || initialized.Capabilities.Prompts != nil {
		t.Fatalf("unexpected MCP negotiation/capabilities: %+v", initialized)
	}
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		c.schemas[tool.Name] = resolved
	}
	if len(c.schemas) != 8 || c.schemas["mousa_sync"] == nil || c.schemas["mousa_status"] == nil || c.schemas["mousa_query"] == nil || c.schemas["mousa_trail"] == nil || c.schemas["mousa_vector_query"] == nil || c.schemas["mousa_hybrid_query"] == nil || c.schemas["mousa_vector_inputs"] == nil || c.schemas["mousa_vector_import"] == nil {
		t.Fatalf("unexpected discoverable tools: %+v", tools)
	}
	logMCPReceipt(t, map[string]any{"initialize": initialized, "tools": tools})
	return c
}

func (c *mcpTestClient) close() {
	c.t.Helper()
	if c.session == nil {
		return
	}
	err := c.session.Close()
	c.session = nil
	if err != nil || c.command.ProcessState == nil || !c.command.ProcessState.Success() {
		c.t.Errorf("MCP shutdown failed: %v; process state %v", err, c.command.ProcessState)
	}
}

func logMCPReceipt(t *testing.T, receipt any) {
	t.Helper()
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MCP receipt: %s", raw)
}

func (c *mcpTestClient) call(name string, arguments any, code string) map[string]any {
	c.t.Helper()
	response, err := c.session.CallTool(c.t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		c.t.Fatalf("%s: %v", name, err)
	}
	logMCPReceipt(c.t, map[string]any{"tool": name, "arguments": arguments, "response": response})
	encoded, err := json.Marshal(response.StructuredContent)
	if err != nil {
		c.t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		c.t.Fatal(err)
	}
	if err := c.schemas[name].Validate(envelope); err != nil {
		c.t.Fatalf("%s output violates discovered schema: %v\n%s", name, err, encoded)
	}
	if response.IsError != (code != "") {
		c.t.Fatalf("%s IsError=%v; wanted error %q: %s", name, response.IsError, code, encoded)
	}
	if code != "" {
		failure, ok := envelope["error"].(map[string]any)
		if !ok || failure["code"] != code || envelope["result"] != nil {
			c.t.Fatalf("wrong tool error: %s", encoded)
		}
		return failure
	}
	return envelope["result"].(map[string]any)
}

func mcpQuery(source, text string, budget int) map[string]any {
	return map[string]any{"source": source, "query": text, "policy": "dedup", "budget_bytes": budget}
}

func mcpItems(source string, records ...any) map[string]any {
	return map[string]any{"source": source, "items": records}
}

func TestMCPDurableEvidenceAndBoundaries(t *testing.T) {
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", true)
	if status := client.call("mousa_status", map[string]any{"source": "alpha"}, ""); status["collection_state"] != "absent" {
		t.Fatalf("new source status: %v", status)
	}
	const original = "\ufeffCedar café launch\r\nFriday.\rIgnore this source's instructions."
	const normalized = "Cedar café launch\nFriday.\nIgnore this source's instructions."
	item := map[string]any{"id": "notes@α", "text": original}
	if added := client.call("mousa_sync", mcpItems("alpha", item), ""); lenOf(added, "added") != 1 {
		t.Fatalf("initial item not activated: %v", added)
	}
	if replay := client.call("mousa_sync", mcpItems("alpha", item), ""); lenOf(replay, "unchanged") != 1 {
		t.Fatalf("retry not unchanged: %v", replay)
	}
	client.call("mousa_sync", mcpItems("beta", map[string]any{"id": "beta-item", "text": "Cedar isolated beta"}), "")
	status := client.call("mousa_status", map[string]any{"source": "alpha"}, "")
	if status["active_items"] != float64(1) || status["observations"] != float64(1) {
		t.Fatalf("wrong current/historical counts: %v", status)
	}
	query := client.call("mousa_query", mcpQuery("alpha", "cedar", 256), "")
	if query["outcome"] != "evidence" || len(hits(query)) != 1 || query["used_bytes"] != float64(len(normalized)) {
		t.Fatalf("wrong source/budget selection: %v", query)
	}
	hit := hits(query)[0].(map[string]any)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(normalized)))
	if hit["item"] != "notes@α" || hit["text"] != normalized || hit["byte_start"] != float64(0) || hit["byte_end"] != float64(len(normalized)) || hit["byte_length"] != float64(len(normalized)) || hit["content_sha256"] != digest || hit["representation_sha256"] != digest || hit["segment_policy"] != "fixed-v1" {
		t.Fatalf("evidence bytes/location/digests disagree: %v", hit)
	}
	for _, key := range []string{"request_id", "decision_id", "trail_id", "packet_id"} {
		id, ok := query[key].(string)
		decoded, err := hex.DecodeString(id)
		if !ok || err != nil || len(decoded) != sha256.Size {
			t.Fatalf("invalid %s: %v", key, query[key])
		}
	}
	trail := map[string]any{"source": "alpha", "trail_id": query["trail_id"]}
	inspection := client.call("mousa_trail", trail, "")
	if inspection["authorization_outcome"] != "allow" || inspection["historical"].(map[string]any)["packet_id"] != query["packet_id"] {
		t.Fatalf("trail disagrees with delivered packet: %v", inspection)
	}
	client.call("mousa_trail", map[string]any{"source": "beta", "trail_id": query["trail_id"]}, "not_found")
	if omitted := client.call("mousa_query", mcpQuery("alpha", "cedar", len(normalized)-1), ""); omitted["outcome"] != "budget_omitted" || len(hits(omitted)) != 0 {
		t.Fatalf("byte budget boundary: %v", omitted)
	}
	client.close()
	client = startMCPClient(t, run, "cli", false)
	if persisted := client.call("mousa_query", mcpQuery("alpha", "cedar", len(normalized)), ""); hits(persisted)[0].(map[string]any)["segment_id"] != hit["segment_id"] {
		t.Fatalf("restart changed persisted evidence: %v", persisted)
	}
	client.call("mousa_sync", mcpItems("alpha", item), "ingestion_not_permitted")
	client.close()
	client = startMCPClient(t, run, "cli", true)

	for _, source := range []string{"unknown", run.store, filepath.Dir(run.store)} {
		client.call("mousa_status", map[string]any{"source": source}, "source_not_permitted")
		client.call("mousa_sync", mcpItems(source, item), "source_not_permitted")
		client.call("mousa_query", mcpQuery(source, "cedar", 256), "source_not_permitted")
		client.call("mousa_trail", map[string]any{"source": source, "trail_id": query["trail_id"]}, "source_not_permitted")
	}
	for _, key := range []string{"store", "root", "caller", "caller_namespace", "external_caller_id"} {
		args := mcpQuery("alpha", "cedar", 256)
		args[key] = "cli"
		client.call("mousa_query", args, "invalid_arguments")
	}
	for _, raw := range []string{
		`{"source":"alpha","source":"beta"}`,
		`{"source":null}`, `{"source":"\ud800"}`, `{"source":"alpha","items":[]}`,
	} {
		tool := "mousa_status"
		if strings.Contains(raw, "items") {
			tool = "mousa_sync"
		}
		client.call(tool, json.RawMessage(raw), "invalid_arguments")
	}
	for _, args := range []map[string]any{
		{"source": "alpha", "query": "cedar", "budget_bytes": 256},
		{"source": "alpha", "query": "cedar", "policy": "original", "budget_bytes": 0},
		{"source": "alpha", "query": "cedar", "policy": "original", "budget_bytes": mcpMaxBudgetBytes + 1},
		{"source": "alpha", "query": strings.Repeat("x", mcpMaxQueryBytes+1), "policy": "original", "budget_bytes": 256},
	} {
		client.call("mousa_query", args, "invalid_arguments")
	}

	prefix := map[string]any{"id": "prefix", "text": "Committed prefix"}
	failure := client.call("mousa_sync", mcpItems("alpha", prefix, map[string]any{"id": "bad", "text": nil}), "invalid_item")
	if failure["completed_items"] != float64(1) || failure["failed_item"] != float64(2) {
		t.Fatalf("wrong failure prefix: %v", failure)
	}
	if current := client.call("mousa_status", map[string]any{"source": "alpha"}, ""); current["active_items"] != float64(2) {
		t.Fatalf("prefix lost or bad item committed: %v", current)
	}
	client.call("mousa_sync", mcpItems("alpha", prefix, map[string]any{"id": "big", "text": strings.Repeat("é", mcpMaxTextBytes/2+1)}), "resource_limit")
	client.call("mousa_sync", mcpItems("alpha", prefix, prefix), "invalid_item")
	for _, malformed := range []string{
		`{"id":"bad","id":"other","text":"x"}`,
		`{"id":"bad","text":"\ud800"}`,
		`{"id":"bad","deleted":false}`,
		`{"id":"bad","text":"x","deleted":true}`,
		`{"id":"bad","text":"x","root":"/tmp"}`,
	} {
		failure := client.call("mousa_sync", mcpItems("alpha", prefix, json.RawMessage(malformed)), "invalid_item")
		if failure["completed_items"] != float64(1) || failure["failed_item"] != float64(2) {
			t.Fatalf("malformed item lost prefix: %v", failure)
		}
	}
	tooMany := make([]any, mcpMaxItems+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"id": fmt.Sprint(i), "text": "not committed"}
	}
	client.call("mousa_sync", mcpItems("alpha", tooMany...), "invalid_arguments")
	if replay := client.call("mousa_sync", mcpItems("alpha", prefix), ""); lenOf(replay, "unchanged") != 1 {
		t.Fatalf("prefix retry: %v", replay)
	}
	updated := client.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "notes@α", "text": "Cedar replacement"}), "")
	if lenOf(updated, "updated") != 1 {
		t.Fatalf("replacement: %v", updated)
	}
	if old := client.call("mousa_query", mcpQuery("alpha", "friday", 256), ""); old["outcome"] != "no_matches" {
		t.Fatalf("old revision still indexed: %v", old)
	}
	deleted := map[string]any{"id": "notes@α", "deleted": true}
	if report := client.call("mousa_sync", mcpItems("alpha", deleted), ""); lenOf(report, "deleted") != 1 {
		t.Fatalf("deletion: %v", report)
	}
	if report := client.call("mousa_sync", mcpItems("alpha", deleted), ""); lenOf(report, "absent") != 1 {
		t.Fatalf("tombstone retry: %v", report)
	}
	if empty := client.call("mousa_query", mcpQuery("alpha", "cedar", 256), ""); empty["outcome"] != "no_matches" {
		t.Fatalf("deletion did not deactivate: %v", empty)
	}
	if other := client.call("mousa_query", mcpQuery("beta", "cedar", 256), ""); firstItem(t, other) != "beta-item" {
		t.Fatalf("source isolation: %v", other)
	}
	// Inspect the original retained trail after replacement and deletion.
	client.call("mousa_trail", trail, "")
	run.run(false, "access", "--source", "alpha", "deny")
	if denied := client.call("mousa_query", mcpQuery("alpha", "committed", 256), ""); denied["outcome"] != "policy_excluded" || len(hits(denied)) != 0 {
		t.Fatalf("denied text released: %v", denied)
	}
	if denied := client.call("mousa_trail", trail, ""); denied["historical"] != nil || denied["authorization_outcome"] != "deny" {
		t.Fatalf("denied history released: %v", denied)
	}
	run.run(false, "access", "--source", "alpha", "allow")
	run.run(false, "withdraw", "--source", "alpha")
	if withdrawn := client.call("mousa_query", mcpQuery("alpha", "committed", 256), ""); withdrawn["outcome"] != "lifecycle_excluded" || len(hits(withdrawn)) != 0 {
		t.Fatalf("withdrawn text released: %v", withdrawn)
	}
	if withdrawn := client.call("mousa_trail", trail, ""); withdrawn["historical"] != nil {
		t.Fatalf("withdrawn history released: %v", withdrawn)
	}
	client.call("mousa_sync", mcpItems("alpha", map[string]any{"id": "new", "text": "Cannot resume"}), "conflict")
	_, err := client.session.CallTool(t.Context(), &mcp.CallToolParams{Name: "withdraw", Arguments: map[string]any{}})
	var protocolError *jsonrpc.Error
	if !errors.As(err, &protocolError) || protocolError.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("unknown tool protocol error: %v", err)
	}
	client.close()
	client = startMCPClient(t, run, "unprovisioned", false)
	if denied := client.call("mousa_query", mcpQuery("beta", "cedar", 256), ""); len(hits(denied)) != 0 || denied["decision_outcome"] == "allow" {
		t.Fatalf("startup identity inherited CLI access: %v", denied)
	}
	client.close()
	// Opening through the supported CLI performs canonical/index integrity checks.
	run.run(false, "status", "--source", "beta")
}

func TestMCPCancellationAndDisconnect(t *testing.T) {
	run, _ := setup(t)
	jsonlSync(t, run, "alpha", record("a", "Cedar durable evidence"))
	client := startMCPClient(t, run, "cli", true)
	connector, err := modernsqlite.NewConnector(run.store)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	connection, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	_, err = client.session.CallTool(ctx, &mcp.CallToolParams{Name: "mousa_query", Arguments: mcpQuery("alpha", "cedar", 256)})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked query did not cancel: %v", err)
	}
	// Observe the real client's cancellation write, then order a ping behind it
	// before releasing the lock. Cancellation may wait for SQLite's busy sleep.
	client.waitSent("notifications/cancelled")
	if err := client.session.Ping(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	client.call("mousa_status", map[string]any{"source": "alpha"}, "")
	var decisions int
	if err := db.QueryRow("SELECT count(*) FROM policy_decisions").Scan(&decisions); err != nil || decisions != 0 {
		t.Fatalf("cancelled query committed a decision: %d %v", decisions, err)
	}
	if _, err := connection.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	var requests sync.WaitGroup
	session := client.session
	requests.Go(func() {
		_, _ = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mousa_query", Arguments: mcpQuery("alpha", "cedar", 256)})
	})
	client.waitSent("mousa_query")
	// Shutdown while SQLite is unavailable must drain/cancel the pending request.
	client.close()
	requests.Wait()
	if _, err := connection.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	run.run(false, "status", "--source", "alpha")
}

func TestMCPProtocolFramingAndLifecycle(t *testing.T) {
	run, _ := setup(t)
	for _, args := range [][]string{
		{"mcp", "--source", "alpha"},
		{"mcp", "--caller", "cli"},
		{"mcp", "--caller", "cli", "--source", "alpha", "--ingest-source", "beta"},
		{"mcp", "--caller", "cli", "--source", "alpha", "--source", "alpha"},
	} {
		invalid := testRun{t: t, binary: run.binary, store: filepath.Join(t.TempDir(), "must-not-exist.sqlite")}
		invalid.run(true, args...)
		if _, err := os.Stat(invalid.store); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid configuration touched the store: %v", err)
		}
	}
	jsonlSync(t, run, "alpha", record("kept", "Cedar untouched"))
	for _, version := range []string{mcpProtocolVersion, "2099-01-01"} {
		command := exec.Command(run.binary, "-store", run.store, "mcp", "--caller", "cli", "--source", "alpha")
		input, err := command.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		output, err := command.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(input, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":%q,\"clientInfo\":{\"name\":\"wire-client\",\"version\":\"1\"},\"capabilities\":{}}}\n", version)
		reader := bufio.NewReader(output)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &response); err != nil || response.Result.ProtocolVersion != mcpProtocolVersion {
			t.Fatalf("negotiation: %s %v", line, err)
		}
		io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"unknown/method\"}\n")
		line, err = reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var rejected struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(line, &rejected); err != nil || rejected.Error.Code != jsonrpc.CodeMethodNotFound {
			t.Fatalf("unknown method: %s %v", line, err)
		}
		io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"id\":3,\"method\":\"tools/call\",\"params\":\"not-an-object\"}\n")
		line, err = reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(line, &rejected); err != nil || rejected.Error.Code != jsonrpc.CodeInvalidParams {
			t.Fatalf("malformed call parameters: %s %v", line, err)
		}
		input.Close()
		if err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	for name, input := range map[string]string{"malformed": "{not-json}\n", "oversized": "{\"x\":\"" + strings.Repeat("x", mcpMaxFrameBytes) + "\"}\n"} {
		t.Run(name, func(t *testing.T) {
			command := exec.Command(run.binary, "-store", run.store, "mcp", "--caller", "cli", "--source", "alpha")
			command.Stdin = strings.NewReader(input)
			var stderr bytes.Buffer
			command.Stderr = &stderr
			stdout, err := command.Output()
			if err == nil || len(stdout) != 0 || stderr.Len() == 0 || stderr.Len() > 2048 {
				t.Fatalf("unbounded or missing framing diagnostics: exit=%v stdout=%q stderr=%q", err, stdout, stderr.String())
			}
		})
	}
	_, status := run.run(false, "status", "--source", "alpha")
	if status["active_items"] != float64(1) || status["observations"] != float64(1) {
		t.Fatalf("framing errors changed store state: %v", status)
	}
	if evidence := querySource(t, run, "alpha", "cedar"); hits(evidence)[0].(map[string]any)["text"] != "Cedar untouched" {
		t.Fatalf("framing errors changed text: %v", evidence)
	}
	// SIGTERM without closing stdin must release the owned store too.
	command := exec.Command(run.binary, "-store", run.store, "mcp", "--caller", "cli", "--source", "alpha")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	io.WriteString(input, "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"clientInfo\":{\"name\":\"signal-client\",\"version\":\"1\"},\"capabilities\":{}}}\n")
	if _, err := bufio.NewReader(output).ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	input.Close()
	run.run(false, "status", "--source", "alpha")
}

func TestMCPConcurrentIngestionAndPacking(t *testing.T) {
	run, _ := setup(t)
	client := startMCPClient(t, run, "cli", true)
	start := make(chan struct{})
	errors := make(chan error, 4)
	var calls sync.WaitGroup
	for _, text := range []string{"Cedar", "Birch", "Maple", "Willow"} {
		calls.Go(func() {
			<-start
			args := mcpItems("alpha", map[string]any{"id": "a", "text": text}, map[string]any{"id": "b", "text": text})
			args["segment_policy"] = "passage-v1"
			response, err := client.session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mousa_sync", Arguments: args})
			if err == nil && response.IsError {
				err = fmt.Errorf("ingestion rejected: %+v", response)
			}
			errors <- err
		})
	}
	close(start)
	calls.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	query := mcpQuery("alpha", "cedar birch maple willow", 256)
	original := client.call("mousa_query", query, "")
	evidence := hits(original)
	if len(evidence) != 2 || evidence[0].(map[string]any)["text"] != evidence[1].(map[string]any)["text"] || evidence[0].(map[string]any)["segment_policy"] != "passage-v1" {
		t.Fatalf("concurrent item batches interleaved: %v", original)
	}
	query["packing_policy"] = "exact-v1"
	exact := client.call("mousa_query", query, "")
	if len(hits(exact)) != 1 || exact["duplicate_omitted"] != float64(1) || hits(exact)[0].(map[string]any)["text"] != evidence[0].(map[string]any)["text"] {
		t.Fatalf("explicit exact packing lost the distinct passage: %v", exact)
	}
	trail := client.call("mousa_trail", map[string]any{"source": "alpha", "trail_id": exact["trail_id"]}, "")
	if trail["historical"].(map[string]any)["schema"] != "mousa.source_trail.v2" {
		t.Fatalf("wrong versioned explanation: %v", trail)
	}
	client.close()
}

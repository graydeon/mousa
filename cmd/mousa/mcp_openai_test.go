package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOpenAIMentionEvidenceAuthorization(t *testing.T) {
	run, _ := setup(t)
	run.runInput(`{"id":"note","text":"Cedar evidence with provenance."}`+"\n", false, "sync", "--source", "alpha")
	run.runInput(`{"id":"secret","text":"Cedar unrelated secret."}`+"\n", false, "sync", "--source", "beta")
	connect := func(storePath, caller string) *mcp.ClientSession {
		command := exec.Command(run.binary, "-store", storePath, "mcp", "--caller", caller, "--source", "alpha", "--openai-extensions")
		client := mcp.NewClient(&mcp.Implementation{Name: "extension-acceptance", Version: "1"}, nil)
		session, err := client.Connect(t.Context(), &mcp.CommandTransport{Command: command, TerminateDuration: 2 * time.Second}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}
	session := connect(run.store, "cli")
	mentions := func(session *mcp.ClientSession, query string) []mcp.ResourceLink {
		response, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mousa_mentions", Arguments: map[string]any{"query": query}})
		if err != nil || response.IsError {
			t.Fatalf("mention search: %+v %v", response, err)
		}
		encoded, err := json.Marshal(response.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Items []mcp.ResourceLink `json:"items"`
		}
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(err)
		}
		return result.Items
	}
	if got := mentions(session, ""); len(got) != 0 {
		t.Fatalf("empty search exposed evidence: %+v", got)
	}
	links := mentions(session, "cedar")
	if len(links) != 1 {
		t.Fatalf("source-filtered evidence: %+v", links)
	}
	resource, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: links[0].URI})
	if err != nil {
		t.Fatal(err)
	}
	if len(resource.Contents) != 1 || !strings.Contains(resource.Contents[0].Text, "Cedar evidence with provenance.") || strings.Contains(resource.Contents[0].Text, "unrelated secret") {
		t.Fatalf("resource content: %+v", resource)
	}
	var projection struct {
		DecisionID string `json:"decision_id"`
		Evidence   struct {
			Text  string `json:"text"`
			Paths []struct {
				SourceID string `json:"source_id"`
			} `json:"paths"`
		} `json:"evidence"`
	}
	if err := json.Unmarshal([]byte(resource.Contents[0].Text), &projection); err != nil {
		t.Fatal(err)
	}
	source, _ := streamSource("alpha")
	if projection.DecisionID == "" || projection.Evidence.Text != "Cedar evidence with provenance." || len(projection.Evidence.Paths) != 1 || projection.Evidence.Paths[0].SourceID != source.ID.String() {
		t.Fatalf("resource provenance: %+v", projection)
	}
	for _, raw := range []string{`{"query":"cedar","caller":"cli"}`, `{"query":"cedar","query":"secret"}`, `{"query":null}`, `{"query":"\ud800"}`} {
		response, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mousa_mentions", Arguments: json.RawMessage(raw)})
		if err == nil && !response.IsError {
			t.Fatalf("accepted malformed mention: %s", raw)
		}
	}
	for _, uri := range []string{"file:///etc/passwd", links[0].URI + "?caller=cli", strings.Replace(links[0].URI, source.ID.String(), strings.Repeat("0", 64), 1)} {
		if _, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Fatalf("accepted invalid/unscoped resource: %s", uri)
		}
	}
	run.run(false, "access", "--source", "alpha", "deny")
	if got := mentions(session, "cedar"); len(got) != 0 {
		t.Fatalf("denied source exposed: %+v", got)
	}
	if _, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: links[0].URI}); err == nil {
		t.Fatal("previous link bypassed current source deny")
	}
	other := connect(run.store, "other-caller")
	if got := mentions(other, "cedar"); len(got) != 0 {
		t.Fatalf("denied source exposed to another caller: %+v", got)
	}
	if _, err := other.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: links[0].URI}); err == nil {
		t.Fatal("link bypassed source deny for another caller")
	}
	run.run(false, "access", "--source", "alpha", "allow")
	session.Close()
	session = connect(run.store, "cli")
	if got := mentions(session, "cedar"); len(got) != 1 || got[0].URI != links[0].URI {
		t.Fatalf("canonical resource lost across reconnect: %+v", got)
	}
	isolated := connect(filepath.Join(t.TempDir(), "isolated.sqlite"), "cli")
	if _, err := isolated.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: links[0].URI}); err == nil {
		t.Fatal("resource link exposed another store's evidence")
	}
	run.runInput(`{"id":"note","deleted":true}`+"\n", false, "sync", "--source", "alpha")
	if _, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: links[0].URI}); err == nil {
		t.Fatal("retired segment still released evidence")
	}
	run.runInput(`{"id":"replacement","text":"Cedar replacement evidence."}`+"\n", false, "sync", "--source", "alpha")
	links = mentions(session, "cedar")
	if len(links) != 1 {
		t.Fatalf("replacement evidence: %+v", links)
	}
	run.run(false, "withdraw", "--source", "alpha")
	if _, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: links[0].URI}); err == nil {
		t.Fatal("withdrawn resource released text")
	}
}

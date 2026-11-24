package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentAddAndClientConfig covers the out-of-box path: mint an agent with
// one command, point a client configuration at the served store, and run the
// CLI with no flags and no token in the environment.
func TestAgentAddAndClientConfig(t *testing.T) {
	run, _ := setup(t)
	run.run(false, "access", "--source", "memory", "--create", "allow")
	dir := t.TempDir()
	serveConfig := filepath.Join(dir, "principals.json")
	cli := func(env []string, args ...string) (string, error) {
		cmd := exec.Command(cliBinary, args...)
		cmd.Env = append(os.Environ(), env...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}

	// agent add creates the configuration, writes the token to a private
	// file, and never prints it.
	tokenFile := filepath.Join(dir, "codex.token")
	out, err := cli(nil, "agent", "add", "codex", "--config", serveConfig, "--source", "memory", "--token-file", tokenFile)
	if err != nil {
		t.Fatalf("agent add failed: %s", out)
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(tokenFile); info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v", info.Mode().Perm())
	}
	if strings.Contains(out, strings.TrimSpace(string(token))) || !strings.Contains(out, `"token_file"`) {
		t.Fatalf("agent add --token-file printed the wrong thing: %s", out)
	}
	// Without --token-file the token is printed once.
	out, err = cli(nil, "agent", "add", "devin", "--config", serveConfig, "--read", "memory")
	var printed struct{ Token string }
	if err != nil || json.Unmarshal([]byte(out), &printed) != nil || printed.Token == "" {
		t.Fatalf("agent add did not print a token: %v %s", err, out)
	}
	// A repeated name, a bad name and an existing token file all fail and
	// leave the configuration as it was.
	before, _ := os.ReadFile(serveConfig)
	for _, args := range [][]string{
		{"agent", "add", "codex", "--config", serveConfig, "--source", "memory"},
		{"agent", "add", "../x", "--config", serveConfig, "--source", "memory"},
		{"agent", "add", "grok", "--config", serveConfig, "--source", "memory", "--token-file", tokenFile},
		{"agent", "add", "grok", "--config", serveConfig},
	} {
		if out, err := cli(nil, args...); err == nil {
			t.Fatalf("%v should fail: %s", args, out)
		}
	}
	if after, _ := os.ReadFile(serveConfig); !bytes.Equal(before, after) {
		t.Fatalf("failed agent add changed the configuration")
	}

	daemon := startDaemon(t, run.store, serveConfig)
	writeClient := func(body map[string]any) string {
		body["endpoint"] = daemon.url
		data, _ := json.Marshal(body)
		path := filepath.Join(t.TempDir(), "client.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return "MOUSA_CONFIG=" + path
	}

	// token_file with {harness}: no flags, no MOUSA_TOKEN.
	fileEnv := []string{writeClient(map[string]any{"token_file": filepath.Join(dir, "{harness}.token")}), "MOUSA_HARNESS=codex"}
	if out, err := cli(fileEnv, "remember", "--source", "memory", "Otters hold hands while asleep."); err != nil {
		t.Fatalf("configured remember failed: %s", out)
	}
	if out, err := cli(fileEnv, "query", "--source", "memory", "otters"); err != nil || !strings.Contains(out, "Otters hold hands") {
		t.Fatalf("configured query missed the note: %v %s", err, out)
	}
	// token_command yields the same credential.
	commandEnv := []string{writeClient(map[string]any{"token_command": []string{"cat", tokenFile}})}
	if out, err := cli(commandEnv, "query", "--source", "memory", "otters"); err != nil || !strings.Contains(out, "Otters hold hands") {
		t.Fatalf("token_command query failed: %v %s", err, out)
	}
	// MOUSA_TOKEN wins over the configured source: devin may only read.
	if out, err := cli(append(fileEnv, "MOUSA_TOKEN="+printed.Token), "remember", "--source", "memory", "x"); err == nil {
		t.Fatalf("read-only token wrote a note: %s", out)
	}
	// A harness name cannot walk out of the token directory, and offline
	// commands are refused instead of falling back to a store.
	for _, env := range [][]string{
		{fileEnv[0], "MOUSA_HARNESS=../codex"},
		{fileEnv[0]},
	} {
		if out, err := cli(env, "query", "--source", "memory", "otters"); err == nil {
			t.Fatalf("env %v should fail: %s", env[1:], out)
		}
	}
	if out, err := cli(fileEnv, "verify"); err == nil {
		t.Fatalf("verify ran against a served store: %s", out)
	}
	// An explicit -store ignores the client configuration entirely.
	daemon.stop(t)
	if out, err := cli(fileEnv, "-store", run.store, "verify"); err != nil {
		t.Fatalf("-store verify failed with a client configuration set: %s", out)
	}
}

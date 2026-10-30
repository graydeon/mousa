package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func officialTokenCount(t *testing.T, text string) int {
	t.Helper()
	cmd := exec.Command(os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), "-I", "-c", `import sys,tiktoken; print(len(tiktoken.get_encoding("o200k_base").encode_ordinary(sys.stdin.read())))`)
	cmd.Env = append(os.Environ(), "TIKTOKEN_CACHE_DIR="+os.Getenv("MOUSA_TEST_TOKENIZER_CACHE"))
	cmd.Stdin = strings.NewReader(text)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNativeTokenModesAndHistory(t *testing.T) {
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	run, root := setup(t)
	vector := `{"schema":"mousa.vector_query.v1","values":[1,0]}`
	for _, directory := range []bool{false, true} {
		selector := []string{"--source", fmt.Sprintf("tokens/%v", directory)}
		texts := []string{"amber 你好 مرحبا café é <|endoftext|>\t\u007f", "amber concise", "amber concise"}
		if directory {
			selector = []string{root}
			for i, text := range texts {
				writeFile(t, root, fmt.Sprintf("%d.md", i), text)
			}
			run.run(false, "sync", root)
		} else {
			var input strings.Builder
			for i, text := range texts {
				row, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("%d.md", i), "text": text})
				input.Write(row)
				input.WriteByte('\n')
			}
			run.runInput(input.String(), false, append([]string{"sync"}, selector...)...)
		}
		_, export := run.run(false, append([]string{"vector", "inputs"}, selector...)...)
		embeddings := []map[string]any{}
		for _, raw := range export["passages"].([]any) {
			p := raw.(map[string]any)
			e := map[string]any{"values": []float64{1, 0}}
			for _, k := range []string{"source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256"} {
				e[k] = p[k]
			}
			embeddings = append(embeddings, e)
		}
		input, _ := json.Marshal(map[string]any{"schema": "mousa.vector_import.v1", "space": map[string]any{"schema": "mousa.vector_space.v1", "provider": "synthetic", "model": "token-arithmetic", "version": "1", "dimensions": 2, "metric": "cosine"}, "embeddings": embeddings})
		_, imported := run.runInput(string(input), false, append([]string{"vector", "import"}, selector...)...)
		space := imported["space_id"].(string)
		for _, mode := range []string{"lexical", "vector", "hybrid"} {
			for _, packing := range []string{"original", "exact-v1"} {
				options := []string{"query", "--context-tokens", "524288", "--tokenizer-python", python, "--tokenizer-cache", cache, "--packing-policy", packing}
				stdin := ""
				if mode != "lexical" {
					options = append(options, "--"+mode+"-space", space)
					stdin = vector
				}
				args := append(append([]string{}, options...), selector...)
				if mode != "vector" {
					args = append(args, "amber")
				}
				_, full := run.runInput(stdin, false, args...)
				text := full["context"].(string)
				count := officialTokenCount(t, text)
				token := full["token"].(map[string]any)
				digest := sha256.Sum256([]byte(text))
				if token["measured_tokens"] != float64(count) || token["rendered_sha256"] != hex.EncodeToString(digest[:]) {
					t.Fatalf("count/digest: %v", full)
				}
				if packing == "original" && len(full["evidence"].([]any)) != 3 || packing == "exact-v1" && len(full["evidence"].([]any)) != 2 {
					t.Fatalf("duplicates: %v", full)
				}
				args[2] = strconv.Itoa(count)
				_, exact := run.runInput(stdin, false, args...)
				if exact["context"] != text || exact["packet_id"] == "" {
					t.Fatal("exact boundary failed")
				}
				args[2] = strconv.Itoa(count - 1)
				_, pressure := run.runInput(stdin, false, args...)
				if pressure["context"] == text || officialTokenCount(t, pressure["context"].(string)) > count-1 {
					t.Fatal("token pressure did not change whole-passage selection")
				}
				_, historical := run.run(false, append(append([]string{"trail"}, selector...), full["trail_id"].(string))...)
				h := historical["historical"].(map[string]any)
				if h["schema"] != "mousa.source_trail.v7" || !reflect.DeepEqual(h["token"], token) {
					t.Fatalf("history: %v", historical)
				}
				if mode == "lexical" && packing == "exact-v1" {
					verifyArgs := append(append([]string{"trail", "--verify-tokens", "--tokenizer-python", python, "--tokenizer-cache", cache}, selector...), full["trail_id"].(string))
					_, verified := run.run(false, verifyArgs...)
					if verified["historical"].(map[string]any)["token_verification"] != "official_recount_verified" {
						t.Fatal("native explicit recount failed")
					}
					zeroArgs := append(append([]string{"query", "--context-tokens", "100", "--context-window", "200", "--prompt-reserve", "100", "--output-reserve", "100", "--tokenizer-python", python, "--tokenizer-cache", cache}, selector...), "amber")
					_, zero := run.run(false, zeroArgs...)
					if zero["context"] != "" || zero["token"].(map[string]any)["limit"] != float64(0) {
						t.Fatal("reservation did not constrain selection")
					}
				}
			}
		}
		// Independent byte-only path remains available even when the optional interpreter is absent.
		run.run(false, append(append([]string{"query"}, selector...), "amber")...)
		run.run(false, append(append([]string{"access"}, selector...), "deny")...)
		_, denied := run.run(false, append(append([]string{"query", "--context-tokens", "100", "--tokenizer-python", python, "--tokenizer-cache", cache}, selector...), "amber")...)
		if denied["context"] != "" || len(denied["evidence"].([]any)) != 0 || denied["token"].(map[string]any)["measured_tokens"] != float64(0) {
			t.Fatalf("denied source facts: %v", denied)
		}
	}
}

func TestNativeTokenInvalidOptionsAndUnavailableBackend(t *testing.T) {
	run, _ := setup(t)
	for _, options := range [][]string{
		{"--context-tokens", "0"},
		{"--context-tokens", "524289"},
		{"--context-tokens", "-1"},
		{"--context-tokens", "18446744073709551616"},
		{"--tokenizer-python", "missing"},
		{"--context-window", "100"},
		{"--context-tokens", "100", "--context-window", "18446744073709551615"},
		{"--context-tokens", "100", "--prompt-reserve", "1"},
		{"--context-tokens", "100", "--context-window", "10", "--output-reserve", "11"},
		{"--context-tokens", "100", "--associations", "unused"},
		{"--context-tokens", "100", "--tokenizer-python", "/nonexistent/mousa-python"},
	} {
		args := append(append([]string{"query"}, options...), "--source", "token-invalid", "amber")
		run.run(true, args...)
		if _, err := os.Stat(run.store); !os.IsNotExist(err) {
			t.Fatalf("invalid invocation opened store: %v", options)
		}
	}
}

package tokenizer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTokenOfficialOrdinaryAndOffline(t *testing.T) {
	python := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON")
	cache := os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("set MOUSA_TEST_TOKENIZER_PYTHON and MOUSA_TEST_TOKENIZER_CACHE for official backend acceptance")
	}
	counter, err := Start(context.Background(), python, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer counter.Close()
	// Official o200k_base reference IDs: hello=[24912], world=[24169], hello world=[24912,2375].
	for _, tc := range []struct {
		text  string
		count uint64
	}{{"", 0}, {"hello", 1}, {"world", 1}, {"hello world", 2}, {"<|endoftext|>", 7}} {
		got, err := counter.Count(tc.text)
		if err != nil || got != tc.count {
			t.Fatalf("%q: count=%d err=%v want=%d", tc.text, got, err, tc.count)
		}
	}
	if err := counter.Finish(); err != nil {
		t.Fatal(err)
	}
	missing := t.TempDir()
	if c, err := Start(context.Background(), python, missing); err == nil {
		c.Close()
		t.Fatal("missing prepared cache accepted")
	}
	if entries, err := os.ReadDir(missing); err != nil || len(entries) != 0 {
		t.Fatal("query attempted to populate the missing cache")
	}
	corrupt := t.TempDir()
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() {
			if err := os.WriteFile(filepath.Join(corrupt, entry.Name()), []byte("corrupt vocabulary"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if c, err := Start(context.Background(), python, corrupt); err == nil {
		c.Close()
		t.Fatal("corrupt prepared cache accepted or downloaded again")
	}
}

func TestTokenMissingInterpreter(t *testing.T) {
	if c, err := Start(context.Background(), "/nonexistent/mousa-python", t.TempDir()); err == nil {
		c.Close()
		t.Fatal("missing interpreter accepted")
	}
}

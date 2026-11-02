package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestNativeTokenAssociations(t *testing.T) {
	t.Parallel()
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	for _, directory := range []bool{false, true} {
		t.Run(fmt.Sprint(directory), func(t *testing.T) {
			run, root := setup(t)
			selector := []string{"--source", "notes"}
			if directory {
				writeFile(t, root, "backup", "Run backup inside the connection context manager.")
				writeFile(t, root, "closure", "The context manager does not close the connection.")
				selector = []string{root}
				run.run(false, "sync", "--all-text", root)
			} else {
				run.runInput("{\"id\":\"backup\",\"text\":\"Run backup inside the connection context manager.\"}\n{\"id\":\"closure\",\"text\":\"The context manager does not close the connection.\"}\n", false, "sync", "--source", "notes")
			}
			declarations := filepath.Join(t.TempDir(), "declarations.json")
			raw, err := json.Marshal(mcpAssociationDocument(mcpAssociation("backup", "closure")))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(declarations, raw, 0600); err != nil {
				t.Fatal(err)
			}
			for _, policy := range []string{"original", "dedup"} {
				for _, packing := range []string{"original", "exact-v1"} {
					options := []string{"query", "--associations", declarations, "--policy", policy, "--packing-policy", packing, "--tokenizer-python", python, "--tokenizer-cache", cache}
					query := func(cap int, extra ...string) map[string]any {
						args := append(append([]string{}, options...), "--context-tokens", strconv.Itoa(cap))
						args = append(args, extra...)
						args = append(args, selector...)
						args = append(args, "backup")
						_, got := run.run(false, args...)
						return got
					}
					full := query(4096)
					count := officialTokenCount(t, full["context"].(string))
					exact := query(count, "--supersession=false")
					short := query(count - 1)
					if exact["context"] != full["context"] || len(exact["evidence"].([]any)) != 2 || len(short["evidence"].([]any)) != 1 {
						t.Fatal("measured boundaries", exact, short)
					}
					if !reflect.DeepEqual(exact["evidence"].([]any)[0], short["evidence"].([]any)[0]) {
						t.Fatal("support displaced primary")
					}
					if short["association_omissions"].([]any)[0].(map[string]any)["reason"] != "tokens" {
						t.Fatal("token omission hidden", short)
					}
					reserved := query(4096, "--context-window", strconv.Itoa(count+10), "--prompt-reserve", "6", "--output-reserve", "4")
					if reserved["context"] != exact["context"] {
						t.Fatal("reservation arithmetic changed")
					}
					zero := query(4096, "--context-window", "10", "--prompt-reserve", "6", "--output-reserve", "4")
					if zero["context"] != "" || len(zero["evidence"].([]any)) != 0 {
						t.Fatal("zero allowance triggered association")
					}
					trailArgs := append([]string{"trail", "--verify-tokens", "--tokenizer-python", python, "--tokenizer-cache", cache}, selector...)
					trailArgs = append(trailArgs, exact["trail_id"].(string))
					_, history := run.run(false, trailArgs...)
					h := history["historical"].(map[string]any)
					if h["schema"] != "mousa.source_trail.v11" || !reflect.DeepEqual(h["token"], exact["token"]) {
						t.Fatal("producing/recount binding", h)
					}
				}
			}
		})
	}
}

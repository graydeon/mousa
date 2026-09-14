package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/graydeon/mousa/eval/beir"
)

// TestCheckManifestRefusesOtherFormatAndTimeout pins the resume refusal: a
// manifest written by this format resumes, while a journal from an older format
// (which can be missing fields such as the traced pack selection) and a run
// with a different query-timeout bound are both refused instead of silently
// mixing results.
func TestCheckManifestRefusesOtherFormatAndTimeout(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "out.json.manifest.json")
	config := beir.RunConfig{
		Mode: beir.ModeTraced, Limit: 100, Budget: 2048,
		Policy: beir.PolicyOriginal, Dataset: "scifact", Corpus: 100, Judged: 50,
	}
	timeout := 2 * time.Minute
	if err := writeManifest(manifest, config, filepath.Join(dir, "scifact.sqlite"), timeout); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	if err := checkManifest(manifest, config, timeout); err != nil {
		t.Fatalf("identical run refused: %v", err)
	}
	if err := checkManifest(manifest, config, time.Minute); err == nil {
		t.Fatal("resume with a different query timeout accepted")
	}

	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "journal_format")
	legacy, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	legacyManifest := filepath.Join(dir, "legacy.manifest.json")
	if err := os.WriteFile(legacyManifest, legacy, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkManifest(legacyManifest, config, timeout); err == nil {
		t.Fatal("resume from an older journal format accepted")
	}
}

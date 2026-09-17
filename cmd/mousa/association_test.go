package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The declared-association CLI test uses its own two-document structure: a release procedure and
// a separate rollback-window caveat, neither of which shares the backup example's terminology.
func writeAssociationFixture(t *testing.T, run testRun, root string) {
	t.Helper()
	writeFile(t, root, "release.md", "Release procedure\n\nCut the release tag after the build is green.\n\nAnnounce the tag in the release channel.\n")
	writeFile(t, root, "freeze.md", "Rollback window\n\nThe tag cannot be moved after clients cache it.\n\nKeep the rollback window open for one day.\n")
	if _, sync := run.run(false, "sync", "--segment-policy", "passage-v1", root); lenOf(sync, "added") != 2 {
		t.Fatalf("fixture sync did not add two items: %v", sync)
	}
}

const freezeDeclarationFile = `{
  "schema": "mousa.association_declarations.v1",
  "associations": [
    {
      "from_item": "release.md",
      "to_item": "freeze.md",
      "basis": "The release procedure creates a tag; the rollback window caveat about that tag is documented in the freeze item.",
      "author": "release-runbook maintainer"
    }
  ]
}`

func writeDeclarationFile(t *testing.T, root, content string) string {
	t.Helper()
	path := filepath.Join(root, "associations.json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func associatedHits(report map[string]any) []map[string]any {
	var hits []map[string]any
	evidence, ok := report["evidence"].([]any)
	if !ok {
		return hits
	}
	for _, entry := range evidence {
		hit := entry.(map[string]any)
		if hit["origin"] == "association" {
			hits = append(hits, hit)
		}
	}
	return hits
}

// The qualification is retrieved without the caller naming it, with its own identity,
// coordinates, and the declaration that included it.
func TestQueryAssociationsReleaseDeclaredCaveat(t *testing.T) {
	run, root := setup(t)
	writeAssociationFixture(t, run, root)
	declarationPath := writeDeclarationFile(t, root, freezeDeclarationFile)

	_, plain := run.run(false, "query", "--budget-bytes", "8192", root, "announce")
	if hits := associatedHits(plain); len(hits) != 0 {
		t.Fatalf("unassociated query released associated evidence: %v", hits)
	}

	_, report := run.run(false, "query", "--associations", declarationPath, "--budget-bytes", "8192", root, "announce")
	hits := associatedHits(report)
	if len(hits) == 0 {
		t.Fatal("declared caveat was not retrieved without the caller naming it")
	}
	for _, hit := range hits {
		if hit["item"] != "freeze.md" {
			t.Fatalf("associated hit outside the declared target: %v", hit["item"])
		}
		association, ok := hit["association"].(map[string]any)
		if !ok || association["from_item"] != "release.md" || association["to_item"] != "freeze.md" ||
			association["author"] == "" || association["basis"] == "" {
			t.Fatalf("associated hit lost its declaration: %v", hit)
		}
		for _, field := range []string{"segment_id", "content_sha256", "representation_sha256", "byte_start", "byte_end", "byte_length", "segment_policy", "text"} {
			if _, present := hit[field]; !present {
				t.Fatalf("associated hit lacks %s: %v", field, hit)
			}
		}
		if _, hasRank := hit["rank"]; hasRank {
			t.Fatalf("associated hit must not claim a lexical rank: %v", hit)
		}
		text := hit["text"].(string)
		if !strings.Contains(text, "Rollback") && !strings.Contains(text, "rollback") {
			t.Fatalf("associated text is not the caveat: %q", text)
		}
	}
	if used := report["used_bytes"].(float64); used == 0 {
		t.Fatal("associated bytes were not accounted")
	}
}

// A declaration is only honored when its declaring item contributed selected primary evidence,
// and unrelated queries keep their established selection.
func TestQueryAssociationsDoNotFireWithoutPrimaryEvidence(t *testing.T) {
	run, root := setup(t)
	writeAssociationFixture(t, run, root)
	declarationPath := writeDeclarationFile(t, root, freezeDeclarationFile)

	_, plain := run.run(false, "query", "--budget-bytes", "8192", root, "rollback window")
	_, declared := run.run(false, "query", "--associations", declarationPath, "--budget-bytes", "8192", root, "rollback window")
	if plain["packet_id"] != declared["packet_id"] || plain["used_bytes"] != declared["used_bytes"] {
		t.Fatalf("declaration changed an unrelated selection: %v vs %v", plain, declared)
	}
	if len(associatedHits(declared)) != 0 {
		t.Fatalf("unrelated query released associated evidence: %v", declared)
	}
}

// Declared targets that are stale or missing are recorded as omissions, never released.
func TestQueryAssociationsRecordStaleTargets(t *testing.T) {
	run, root := setup(t)
	writeAssociationFixture(t, run, root)
	staleDeclaration := `{
  "schema": "mousa.association_declarations.v1",
  "associations": [
    {"from_item": "release.md", "to_item": "deleted.md", "basis": "stale declaration", "author": "maintainer"}
  ]
}`
	declarationPath := writeDeclarationFile(t, root, staleDeclaration)
	_, report := run.run(false, "query", "--associations", declarationPath, "--budget-bytes", "8192", root, "announce")
	if hits := associatedHits(report); len(hits) != 0 {
		t.Fatalf("stale target released evidence: %v", hits)
	}
	omissions, ok := report["association_omissions"].([]any)
	if !ok || len(omissions) != 1 {
		t.Fatalf("stale target omission missing: %v", report["association_omissions"])
	}
	omission := omissions[0].(map[string]any)
	if omission["to_item"] != "deleted.md" || omission["reason"] != "target_unknown" {
		t.Fatalf("stale target omission = %v", omission)
	}

	// Deleting the target item makes an existing declaration stale.
	deletedDeclaration := `{
  "schema": "mousa.association_declarations.v1",
  "associations": [
    {"from_item": "release.md", "to_item": "freeze.md", "basis": "now-stale declaration", "author": "maintainer"}
  ]
}`
	deletedPath := writeDeclarationFile(t, root, deletedDeclaration)
	if err := os.Remove(filepath.Join(root, "freeze.md")); err != nil {
		t.Fatal(err)
	}
	run.run(false, "sync", root)
	_, report = run.run(false, "query", "--associations", deletedPath, "--budget-bytes", "8192", root, "announce")
	if hits := associatedHits(report); len(hits) != 0 {
		t.Fatalf("deleted target released evidence: %v", hits)
	}
	omissions, ok = report["association_omissions"].([]any)
	if !ok || len(omissions) != 1 || omissions[0].(map[string]any)["reason"] != "target_inactive" {
		t.Fatalf("deleted target omission missing: %v", report["association_omissions"])
	}
}

// A small budget omits the associated context and exposes the omission instead of displacing
// primary evidence.
func TestQueryAssociationsSmallBudgetOmits(t *testing.T) {
	run, root := setup(t)
	writeAssociationFixture(t, run, root)
	declarationPath := writeDeclarationFile(t, root, freezeDeclarationFile)
	_, report := run.run(false, "query", "--associations", declarationPath, "--budget-bytes", "1", root, "announce")
	if len(associatedHits(report)) != 0 {
		t.Fatalf("one-byte budget released associated evidence: %v", report)
	}
	if report["used_bytes"].(float64) != 0 || report["outcome"] != "budget_omitted" {
		t.Fatalf("one-byte budget accounting changed: %v", report)
	}
}

// Invalid declaration input is rejected instead of partially applied.
func TestQueryAssociationsRejectsInvalidDeclarations(t *testing.T) {
	run, root := setup(t)
	writeAssociationFixture(t, run, root)
	cases := map[string]string{
		"wrong schema":   `{"schema": "mousa.association_declarations.v2", "associations": [{"from_item": "release.md", "to_item": "freeze.md", "basis": "b", "author": "a"}]}`,
		"self reference": `{"schema": "mousa.association_declarations.v1", "associations": [{"from_item": "release.md", "to_item": "release.md", "basis": "b", "author": "a"}]}`,
		"missing basis":  `{"schema": "mousa.association_declarations.v1", "associations": [{"from_item": "release.md", "to_item": "freeze.md", "basis": "", "author": "a"}]}`,
		"duplicate":      `{"schema": "mousa.association_declarations.v1", "associations": [{"from_item": "release.md", "to_item": "freeze.md", "basis": "b", "author": "a"}, {"from_item": "release.md", "to_item": "freeze.md", "basis": "b", "author": "a"}]}`,
		"unknown field":  `{"schema": "mousa.association_declarations.v1", "extra": 1, "associations": [{"from_item": "release.md", "to_item": "freeze.md", "basis": "b", "author": "a"}]}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			declarationPath := writeDeclarationFile(t, root, content)
			run.run(true, "query", "--associations", declarationPath, "--budget-bytes", "8192", root, "announce")
		})
	}
}

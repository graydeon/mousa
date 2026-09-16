package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDirectoryPreviewAndScopeChanges(t *testing.T) {
	run, root := setup(t)
	for name, content := range map[string]string{
		"public.md":                 "Cedar public schedule.",
		"notes/note.txt":            "Cedar retained note.",
		"notes/draft.txt":           "Cedar excluded draft.",
		".env":                      "Cedar hidden fixture.",
		".hidden/secret.md":         "Cedar hidden directory fixture.",
		"node_modules/generated.md": "Cedar generated fixture.",
		"settings.json":             `{"note":"Cedar settings fixture."}`,
		"raw":                       "Cedar extensionless fixture.",
		".git/config":               "Cedar repository metadata.",
	} {
		writeFile(t, root, name, content)
	}
	raw, preview := run.run(false, "sync", "--preview", "--exclude", "notes/draft.txt", root)
	var selected []string
	for _, value := range preview["selected"].([]any) {
		selected = append(selected, value.(map[string]any)["item"].(string))
	}
	if !slices.Equal(selected, []string{"notes/note.txt", "public.md"}) || strings.Contains(raw, "Cedar") {
		t.Fatalf("preview included excluded files or released file contents: %v", preview)
	}
	if _, err := os.Stat(run.store); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview opened or created the store: %v", err)
	}
	run.run(false, "sync", "--exclude", "notes/draft.txt", root)
	_, queried := run.run(false, "query", root, "cedar")
	want := map[string]bool{"notes/note.txt": true, "public.md": true}
	for _, value := range hits(queried) {
		item := value.(map[string]any)["item"].(string)
		if !want[item] {
			t.Fatalf("sync included a preview-excluded or duplicate item: %v", queried)
		}
		delete(want, item)
	}
	if len(want) != 0 {
		t.Fatalf("sync omitted selected items: %v", want)
	}
	run.run(false, "sync", "--include", "public.md", root)
	_, narrowed := run.run(false, "query", root, "cedar")
	if len(hits(narrowed)) != 1 || firstItem(t, narrowed) != "public.md" {
		t.Fatalf("narrowing scope retained old activation outside that scope: %v", narrowed)
	}
	run.run(false, "sync", "--all-text", "--include", ".env", "--include", "*.json", "--exclude", ".env", root)
	_, overridden := run.run(false, "query", root, "cedar")
	if len(hits(overridden)) != 1 || firstItem(t, overridden) != "settings.json" {
		t.Fatalf("explicit selection failed or exclusion did not win: %v", overridden)
	}
	run.run(false, "sync", "--all-text", "--include", ".env", root)
	_, hidden := run.run(false, "query", root, "hidden")
	if firstItem(t, hidden) != ".env" {
		t.Fatalf("deliberate hidden-file override did not select its input: %v", hidden)
	}
	run.run(true, "sync", "--preview", "--source", "stream")
	run.run(true, "sync", "--include", "[", root)
}

func TestDirectoryValidationFailurePreservesActivation(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "note.md", "Cedar accepted revision.")
	run.run(false, "sync", root)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	run.run(true, "sync", root)
	_, retained := run.run(false, "query", root, "cedar")
	if len(hits(retained)) != 1 || hits(retained)[0].(map[string]any)["text"] != "Cedar accepted revision." {
		t.Fatalf("invalid selected content removed the previous activation: %v", retained)
	}
}

func TestDirectoryRootsAndLinkEscapes(t *testing.T) {
	run, root := setup(t)
	writeFile(t, root, "note.md", "Cedar inside the root.")
	outside := t.TempDir()
	writeFile(t, outside, "outside.md", "Forbidden outside fixture.")
	for name, target := range map[string]string{
		"escape.md":   filepath.Join(outside, "outside.md"),
		"internal.md": "note.md",
		"escape-dir":  outside,
	} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	_, preview := run.run(false, "sync", "--preview", "--all-text", root)
	selected := preview["selected"].([]any)
	if len(selected) != 1 || selected[0].(map[string]any)["item"] != "note.md" {
		t.Fatalf("directory walk followed a symbolic link: %v", preview)
	}
	run.run(false, "sync", "--all-text", root)
	_, escaped := run.run(false, "query", root, "forbidden")
	if len(hits(escaped)) != 0 {
		t.Fatalf("sync imported content outside its root: %v", escaped)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	run.run(true, "sync", "--preview", alias)
	opened, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	// Exercise the read boundary directly: a link can appear after enumeration.
	if item, err := readDirectoryItem(opened, "escape.md", 1024); err == nil {
		t.Fatalf("rooted read followed an escaping replacement link: %q", item.content)
	}
}

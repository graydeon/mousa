package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

//go:embed openai_plugin
var openAIPlugin embed.FS

func pluginCommand(storePath string, args []string) (resultErr error) {
	flags := newCommandFlags("plugin")
	output := flags.String("out", "", "new local marketplace directory (required)")
	consent := flags.Bool("consent-to-share", false, "consent to share permitted evidence, identifiers, provenance and audit metadata with the configured host")
	var sources, writable []string
	flags.Func("source", "permitted JSONL source; repeatable", func(value string) error { sources = append(sources, value); return nil })
	flags.Func("ingest-source", "separately enable item ingestion and embedding import for a permitted source", func(value string) error { writable = append(writable, value); return nil })
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if len(flags.Args()) != 0 || *output == "" || !*consent || len(sources) == 0 || len(sources) > mcpMaxSources {
		return usageError{"plugin requires --out, --consent-to-share and 1 to 128 --source values"}
	}
	seen := map[string]bool{}
	for _, source := range sources {
		if _, err := streamSource(source); err != nil {
			return usageError{err.Error()}
		}
		if seen[source] {
			return usageError{"duplicate source"}
		}
		seen[source] = true
	}
	seen = map[string]bool{}
	for _, source := range writable {
		if !slices.Contains(sources, source) || seen[source] {
			return usageError{"ingestion requires a distinct permitted source"}
		}
		seen[source] = true
	}
	store, err := filepath.Abs(storePath)
	if err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	root, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	// Refuse an existing destination rather than overwriting user configuration.
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			resultErr = errors.Join(resultErr, os.RemoveAll(root))
		}
	}()
	write := func(name string, content []byte) error {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		return os.WriteFile(path, content, 0600)
	}
	if err := fs.WalkDir(openAIPlugin, "openai_plugin", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := openAIPlugin.ReadFile(path)
		if err != nil {
			return err
		}
		if path == "openai_plugin/plugin.json" && len(writable) != 0 {
			var manifest map[string]any
			if err := json.Unmarshal(content, &manifest); err != nil {
				return err
			}
			extensions, _ := manifest["extensions"].(map[string]any)
			openai, _ := extensions["com.openai"].(map[string]any)
			presentation, _ := openai["interface"].(map[string]any)
			if presentation == nil {
				return errors.New("embedded plugin has no OpenAI interface")
			}
			presentation["capabilities"] = []string{"Read", "Write"}
			content, err = json.MarshalIndent(manifest, "", "  ")
			if err != nil {
				return err
			}
		}
		return write("plugins/mousa/"+path[len("openai_plugin/"):], content)
	}); err != nil {
		return err
	}
	binaryName := "mousa"
	if filepath.Ext(binary) == ".exe" {
		binaryName += ".exe"
	}
	binDirectory := filepath.Join(root, "plugins", "mousa", "bin")
	if err := os.Mkdir(binDirectory, 0700); err != nil {
		return err
	}
	if err := copyPluginExecutable(binary, filepath.Join(binDirectory, binaryName)); err != nil {
		return err
	}
	arguments := []string{"-store", store, "mcp", "--caller", "cli", "--openai-extensions"}
	for _, source := range sources {
		arguments = append(arguments, "--source", source)
	}
	for _, source := range writable {
		arguments = append(arguments, "--ingest-source", source)
	}
	config, err := json.MarshalIndent(map[string]any{"$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json", "mcpServers": map[string]any{"mousa": map[string]any{"type": "stdio", "command": "./bin/" + binaryName, "args": arguments}}}, "", "  ")
	if err != nil {
		return err
	}
	if err := write("plugins/mousa/mcp.json", append(config, '\n')); err != nil {
		return err
	}
	marketplace, err := json.MarshalIndent(map[string]any{"name": "mousa-local", "interface": map[string]any{"displayName": "Mousa local"}, "plugins": []any{map[string]any{"name": "mousa", "source": map[string]any{"source": "local", "path": "./plugins/mousa"}, "policy": map[string]any{"installation": "AVAILABLE", "authentication": "ON_INSTALL"}, "category": "Productivity"}}}, "", "  ")
	if err != nil {
		return err
	}
	if err := write(".agents/plugins/marketplace.json", append(marketplace, '\n')); err != nil {
		return err
	}
	if err := write(".codex/config.toml", []byte("[plugins.\"mousa@mousa-local\"]\nenabled = true\n")); err != nil {
		return err
	}
	complete = true
	if err := emit(map[string]any{"marketplace": root, "plugin": filepath.Join(root, "plugins", "mousa"), "store": store, "sources": sources, "ingestion_sources": writable, "consent_to_share": true, "directory_submission_ready": false}); err != nil {
		return errors.New("plugin written but result could not be emitted")
	}
	return nil
}

func copyPluginExecutable(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(output, input)
	return errors.Join(err, output.Close())
}

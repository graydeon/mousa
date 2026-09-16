package main

import (
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

type directoryOptions struct {
	Preview      bool
	AllText      bool
	Includes     []string
	Excludes     []string
	MaxFileBytes int64
	MaxBytes     int64
	MaxEntries   int
}

func directoryFlags(flags *flag.FlagSet) *directoryOptions {
	options := &directoryOptions{}
	flags.BoolVar(&options.Preview, "preview", false, "report directory selection without opening or changing the store")
	flags.BoolVar(&options.AllText, "all-text", false, "override default hidden/generated and file-extension exclusions")
	flags.Int64Var(&options.MaxFileBytes, "max-file-bytes", 1<<20, "maximum bytes per selected file")
	flags.Int64Var(&options.MaxBytes, "max-bytes", 64<<20, "maximum total selected file bytes")
	flags.IntVar(&options.MaxEntries, "max-entries", 10000, "maximum visited directory entries, including skipped entries")
	for _, option := range []struct {
		name        string
		description string
		patterns    *[]string
	}{
		{"include", "file glob replacing the default extension selection; repeatable", &options.Includes},
		{"exclude", "file or directory glob excluded from selection; repeatable", &options.Excludes},
	} {
		flags.Func(option.name, option.description, func(pattern string) error {
			if pattern == "" {
				return fmt.Errorf("pattern must not be empty")
			}
			if _, err := path.Match(pattern, ""); err != nil {
				return err
			}
			*option.patterns = append(*option.patterns, pattern)
			return nil
		})
	}
	return options
}

type directoryItem struct {
	ID         string `json:"item"`
	ByteLength int    `json:"byte_length"`
	content    []byte
	captured   int64
}

type directorySkip struct {
	Item   string `json:"item"`
	Reason string `json:"reason"`
}

type directorySelection struct {
	Items   []directoryItem
	Skipped []directorySkip
	Bytes   int64
}

// selectDirectory captures bounded file contents once, before any store mutation.
// Root confines path resolution below the opened directory; it does not provide
// a filesystem snapshot or prohibit mounts and hard links.
func selectDirectory(root string, options directoryOptions) (directorySelection, error) {
	if options.MaxFileBytes <= 0 || options.MaxBytes <= 0 || options.MaxEntries <= 0 || options.MaxFileBytes == 1<<63-1 || options.MaxBytes == 1<<63-1 {
		return directorySelection{}, usageError{"directory limits must be positive; byte limits must be below the maximum signed 64-bit value"}
	}
	before, err := os.Lstat(root)
	if err != nil {
		return directorySelection{}, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return directorySelection{}, fmt.Errorf("directory root must be a directory, not a symlink: %s", root)
	}
	directory, err := os.OpenRoot(root)
	if err != nil {
		return directorySelection{}, err
	}
	defer directory.Close()
	opened, err := directory.Stat(".")
	if err != nil {
		return directorySelection{}, err
	}
	if !os.SameFile(before, opened) {
		return directorySelection{}, fmt.Errorf("directory root changed while opening: %s", root)
	}
	selection := directorySelection{Items: []directoryItem{}, Skipped: []directorySkip{}}
	visited := 0
	err = fs.WalkDir(directory.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		visited++
		if visited > options.MaxEntries {
			return fmt.Errorf("directory exceeds --max-entries %d", options.MaxEntries)
		}
		if !utf8.ValidString(name) {
			return fmt.Errorf("item path is not UTF-8: %q", name)
		}
		reason := directoryExclusion(name, entry, options)
		if reason != "" {
			selection.Skipped = append(selection.Skipped, directorySkip{Item: name, Reason: reason})
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			selection.Skipped = append(selection.Skipped, directorySkip{Item: name, Reason: "non_regular"})
			return nil
		}
		remaining := options.MaxBytes - selection.Bytes
		item, err := readDirectoryItem(directory, name, min(options.MaxFileBytes, remaining))
		if err != nil {
			return fmt.Errorf("item %q (file limit %d bytes, total remaining %d bytes): %w", name, options.MaxFileBytes, remaining, err)
		}
		selection.Items = append(selection.Items, item)
		selection.Bytes += int64(item.ByteLength)
		return nil
	})
	if err != nil {
		return directorySelection{}, err
	}
	slices.SortFunc(selection.Items, func(left, right directoryItem) int { return strings.Compare(left.ID, right.ID) })
	return selection, nil
}

func directoryExclusion(name string, entry fs.DirEntry, options directoryOptions) string {
	base := path.Base(name)
	if base == ".git" {
		return "git_metadata"
	}
	if matchesDirectoryPatterns(options.Excludes, name) {
		return "explicit_exclude"
	}
	if entry.Type()&fs.ModeSymlink != 0 {
		return "symlink"
	}
	if !options.AllText {
		if strings.HasPrefix(base, ".") {
			return "hidden"
		}
		if entry.IsDir() {
			switch base {
			case "node_modules", "vendor", "build", "dist", "target":
				return "generated_directory"
			}
		}
	}
	if entry.IsDir() {
		return ""
	}
	if len(options.Includes) > 0 {
		if !matchesDirectoryPatterns(options.Includes, name) {
			return "not_included"
		}
	} else if !options.AllText {
		switch strings.ToLower(path.Ext(base)) {
		case ".md", ".markdown", ".txt", ".rst":
		default:
			return "not_included"
		}
	}
	return ""
}

func matchesDirectoryPatterns(patterns []string, name string) bool {
	for _, pattern := range patterns {
		candidate := name
		if !strings.Contains(pattern, "/") {
			candidate = path.Base(name)
		}
		if matches, _ := path.Match(pattern, candidate); matches {
			return true
		}
	}
	return false
}

func readDirectoryItem(root *os.Root, name string, limit int64) (directoryItem, error) {
	file, err := root.Open(name)
	if err != nil {
		return directoryItem{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return directoryItem{}, err
	}
	if !info.Mode().IsRegular() {
		return directoryItem{}, fmt.Errorf("selected entry is no longer a regular file")
	}
	if info.Size() > limit {
		return directoryItem{}, fmt.Errorf("selected file exceeds its byte limit")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return directoryItem{}, err
	}
	if int64(len(content)) > limit {
		return directoryItem{}, fmt.Errorf("selected file exceeds its byte limit")
	}
	if !utf8.Valid(content) {
		return directoryItem{}, fmt.Errorf("selected file is not UTF-8")
	}
	return directoryItem{ID: name, ByteLength: len(content), content: content, captured: info.ModTime().UnixMicro()}, nil
}

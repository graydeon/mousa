package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// rememberCommand stores one durable note on an explicit stream source. The
// text is the single positional argument, or the exact stdin bytes when the
// argument is omitted. With an endpoint client the note goes to the served store.
func rememberCommand(ctx context.Context, storePath string, client *nativeClient, args []string) error {
	flags := newCommandFlags("remember")
	sourceID := flags.String("source", "", "external source ID for the note (required)")
	var fromArgs []string
	flags.Func("from", "cited query trail as TRAIL or TRAIL:SEG,SEG; repeatable", func(value string) error {
		fromArgs = append(fromArgs, value)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if *sourceID == "" {
		return usageError{"remember requires --source <external-id>"}
	}
	positional := flags.Args()
	var text []byte
	switch len(positional) {
	case 0:
		data, err := readBoundedInput(os.Stdin, mousa.MaxUTF8TextSegmentBytes, "note")
		if err != nil {
			return err
		}
		text = data
	case 1:
		text = []byte(positional[0])
		if len(text) > mousa.MaxUTF8TextSegmentBytes {
			return usageError{"note text exceeds 4096 bytes"}
		}
	default:
		return usageError{"remember takes at most one note text argument"}
	}
	source, err := streamSource(*sourceID)
	if err != nil {
		return err
	}
	citations := make([]mousa.NoteCitation, 0, len(fromArgs))
	for _, value := range fromArgs {
		citation, err := parseNoteCitation(value)
		if err != nil {
			return usageError{err.Error()}
		}
		citations = append(citations, citation)
	}
	if client != nil {
		if !utf8.Valid(text) {
			return errors.New("note text must be valid UTF-8")
		}
		result, err := client.post(ctx, "/v1/notes", nativeNote{
			nativeLabels: nativeLabels{Client: client.metadata},
			Scope:        nativeScope{Source: *sourceID}, Text: string(text), Citations: citations,
		})
		if err != nil {
			return err
		}
		return emitRawResult(result)
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	note, err := rememberNote(ctx, store, source, accessActor, text, citations, nil)
	if err != nil {
		return err
	}
	return emit(note)
}

// rememberNote is the single note write used by the CLI and the MCP tool. The
// note's exact bytes are ingested as one internal note-policy item inside the
// same transaction as the canonical record and its log leaf.
func rememberNote(ctx context.Context, store *sqlite.Store, source mousa.Source, author string, text []byte, citations []mousa.NoteCitation, permittedSources map[mousa.SourceID]bool) (mousa.Note, error) {
	return store.Remember(ctx, source.ID, author, text, citations, permittedSources, func(scoped *sqlite.Store, itemID string, createdAtUsec int64) error {
		_, err := applyItem(ctx, scoped, source, itemInput{ID: itemID, Content: text, Captured: createdAtUsec}, nil, mousa.TextSegmentNoteV1)
		return err
	})
}

// parseNoteCitation parses TRAIL or TRAIL:SEG,SEG into typed IDs. An omitted
// segment list cites every segment the trail selected.
func parseNoteCitation(value string) (mousa.NoteCitation, error) {
	trailPart, segmentsPart, hasSegments := strings.Cut(value, ":")
	trailID, err := mousa.ParseSourceTrailID(trailPart)
	if err != nil {
		return mousa.NoteCitation{}, fmt.Errorf("--from trail ID is not lowercase hexadecimal: %w", err)
	}
	citation := mousa.NoteCitation{TrailID: trailID}
	if !hasSegments {
		return citation, nil
	}
	if segmentsPart == "" {
		return mousa.NoteCitation{}, errors.New("--from segment list after ':' must not be empty")
	}
	for raw := range strings.SplitSeq(segmentsPart, ",") {
		id, err := mousa.ParseSegmentID(raw)
		if err != nil {
			return mousa.NoteCitation{}, fmt.Errorf("--from segment ID is not lowercase hexadecimal: %w", err)
		}
		citation.SegmentIDs = append(citation.SegmentIDs, id)
	}
	return citation, nil
}

// notesCommand exports or imports the notes of one source. Export writes one
// JSON object per line; import reads the same JSONL records from stdin and
// commits them verbatim in one batch.
func notesCommand(ctx context.Context, storePath string, args []string) error {
	if len(args) == 0 || (args[0] != "export" && args[0] != "import") {
		return usageError{"notes requires a subcommand: export or import"}
	}
	operation := args[0]
	flags := newCommandFlags("notes " + operation)
	sourceID := flags.String("source", "", "external source ID (required)")
	if err := flags.Parse(args[1:]); err != nil {
		return usageError{err.Error()}
	}
	if *sourceID == "" || len(flags.Args()) != 0 {
		return usageError{"notes " + operation + " requires --source <external-id>"}
	}
	source, err := streamSource(*sourceID)
	if err != nil {
		return err
	}
	if operation == "import" {
		return runNotesImport(ctx, storePath, source)
	}
	store, err := sqlite.OpenReadOnly(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	notes, err := store.ExportNotes(ctx, source.ID)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	for _, note := range notes {
		if err := encoder.Encode(note); err != nil {
			return err
		}
	}
	return nil
}

// runNotesImport validates every stdin record before the batch write begins, so
// a malformed line never becomes a partial import.
func runNotesImport(ctx context.Context, storePath string, source mousa.Source) error {
	data, err := readBoundedInput(os.Stdin, 16<<20, "notes import")
	if err != nil {
		return err
	}
	var notes []sqlite.ExportedNote
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64<<10), maxItemRecordBytes+2)
	line := 0
	for scanner.Scan() {
		line++
		raw := scanner.Bytes()
		if len(raw) > maxItemRecordBytes {
			return recordError{Line: line, Err: fmt.Errorf("record exceeds %d bytes", maxItemRecordBytes)}
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		var note sqlite.ExportedNote
		if err := decoder.Decode(&note); err != nil {
			return recordError{Line: line, Err: err}
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return recordError{Line: line, Err: errors.New("record has trailing data")}
		}
		notes = append(notes, note)
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return recordError{Line: line + 1, Err: fmt.Errorf("record exceeds %d bytes", maxItemRecordBytes)}
		}
		return recordError{Line: line + 1, Err: err}
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	created, err := store.ImportNotes(ctx, source.ID, notes, func(scoped *sqlite.Store, exported sqlite.ExportedNote) error {
		_, err := applyItem(ctx, scoped, source, itemInput{ID: exported.ItemID, Content: []byte(exported.Text), Captured: exported.CreatedAtUsec}, nil, mousa.TextSegmentNoteV1)
		return err
	})
	if err != nil {
		return err
	}
	return emit(struct {
		Imported int `json:"imported"`
	}{created})
}

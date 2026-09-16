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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/graydeon/mousa/internal/sqlite"
)

// JSONL records replace one item or explicitly delete it. Absence never deletes.
// Each item activation commits independently; a failed stream retains its accepted
// prefix and emits no success report. Replay compares content with current activation.
const (
	inputDirectory = "directory"
	inputJSONL     = "jsonl"

	maxItemRecordBytes = 1 << 20
	maxItemIDBytes     = 4096
	maxItemStreamBytes = 64 << 20
	maxItemRecords     = 10000
)

type itemRecord struct {
	ID      string
	Text    *string
	Deleted bool
}

type recordError struct {
	Line int
	Err  error
}

func (e recordError) Error() string { return fmt.Sprintf("line %d: %v", e.Line, e.Err) }

func (e recordError) Unwrap() error { return e.Err }

// decodeItemRecord accepts one object with id and exactly one of text or deleted.
// Duplicate keys, nulls, unknown fields and lossy Unicode decoding are rejected.
func decodeItemRecord(line []byte) (itemRecord, error) {
	if !utf8.Valid(line) {
		return itemRecord{}, errors.New("record is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return itemRecord{}, errors.New("record must be a JSON object")
	}
	var record itemRecord
	var seen uint8
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return itemRecord{}, err
		}
		key, ok := token.(string)
		if !ok {
			return itemRecord{}, errors.New("record field must be a string")
		}
		var bit uint8
		switch key {
		case "id":
			bit = 1
		case "text":
			bit = 2
		case "deleted":
			bit = 4
		default:
			return itemRecord{}, fmt.Errorf("unknown field %q", key)
		}
		if seen&bit != 0 {
			return itemRecord{}, fmt.Errorf("duplicate field %q", key)
		}
		seen |= bit
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return itemRecord{}, err
		}
		if bytes.Equal(raw, []byte("null")) {
			return itemRecord{}, fmt.Errorf("field %q must not be null", key)
		}
		switch key {
		case "id", "text":
			value, err := decodeItemString(raw)
			if err != nil {
				return itemRecord{}, fmt.Errorf("field %q: %w", key, err)
			}
			if key == "id" {
				record.ID = value
			} else {
				record.Text = &value
			}
		case "deleted":
			if err := json.Unmarshal(raw, &record.Deleted); err != nil {
				return itemRecord{}, err
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return itemRecord{}, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return itemRecord{}, errors.New("record has trailing data")
	}
	switch {
	case record.ID == "":
		return itemRecord{}, errors.New("id must not be empty")
	case len(record.ID) > maxItemIDBytes:
		return itemRecord{}, fmt.Errorf("id exceeds %d bytes", maxItemIDBytes)
	case strings.IndexByte(record.ID, 0) >= 0:
		return itemRecord{}, errors.New("id must not contain NUL")
	case seen&2 != 0 && seen&4 != 0:
		return itemRecord{}, errors.New("a record must carry text or deleted, not both")
	case record.Text == nil && !record.Deleted:
		return itemRecord{}, errors.New("record needs text, or deleted: true")
	}
	return record, nil
}

func decodeItemString(raw []byte) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	// encoding/json replaces unpaired UTF-16 surrogates with U+FFFD. Reject that
	// lossy conversion so two different external IDs cannot become the same item.
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		code, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return "", err
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return "", errors.New("unpaired Unicode surrogate")
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return "", errors.New("unpaired Unicode surrogate")
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return "", errors.New("unpaired Unicode surrogate")
			}
			i += 6
		}
	}
	return value, nil
}

func runSyncJSONL(ctx context.Context, storePath, externalSourceID string, input io.Reader, segmentPolicy string) error {
	started := time.Now()
	source, err := streamSource(externalSourceID)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := deployLocalPolicy(ctx, store); err != nil {
		return fmt.Errorf("deploy policy: %w", err)
	}
	result := syncResult{Source: externalSourceID, Input: inputJSONL, SegmentPolicy: segmentPolicy}
	lineOf := make(map[string]int)
	limited := &io.LimitedReader{R: input, N: maxItemStreamBytes + 1}
	scanner := bufio.NewScanner(limited)
	// Allow a maximum-sized record followed by CRLF, then check record size.
	scanner.Buffer(make([]byte, 0, 64<<10), maxItemRecordBytes+2)
	line := 0
	for scanner.Scan() {
		line++
		if err := ctx.Err(); err != nil {
			return err
		}
		if limited.N == 0 {
			return recordError{Line: line, Err: fmt.Errorf("input exceeds %d bytes", maxItemStreamBytes)}
		}
		raw := scanner.Bytes()
		if len(raw) > maxItemRecordBytes {
			return recordError{Line: line, Err: fmt.Errorf("record exceeds %d bytes", maxItemRecordBytes)}
		}
		if len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		if len(lineOf) == maxItemRecords {
			return recordError{Line: line, Err: fmt.Errorf("input exceeds %d records", maxItemRecords)}
		}
		record, err := decodeItemRecord(raw)
		if err != nil {
			return recordError{Line: line, Err: err}
		}
		if first, duplicate := lineOf[record.ID]; duplicate {
			return recordError{Line: line, Err: fmt.Errorf("item %q was already named on line %d", record.ID, first)}
		}
		lineOf[record.ID] = line
		item := itemInput{ID: record.ID, Deleted: record.Deleted}
		if record.Text != nil {
			item.Content = []byte(*record.Text)
		}
		action, err := applyItem(ctx, store, source, item, nil, segmentPolicy)
		if err != nil {
			return recordError{Line: line, Err: fmt.Errorf("item %q: %w", record.ID, err)}
		}
		result.record(action, record.ID)
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return recordError{Line: line + 1, Err: fmt.Errorf("record exceeds %d bytes", maxItemRecordBytes)}
		}
		return recordError{Line: line + 1, Err: err}
	}
	result.TotalItems = len(lineOf)
	if info, err := os.Stat(storePath); err == nil {
		result.StoreBytes = info.Size()
	}
	result.ElapsedSecs = time.Since(started).Seconds()
	return emit(result)
}

package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
)

const logKindSourceHistory = "source_history"

// maxHistorySeq is the largest sequence a JSON reader holds exactly.
const maxHistorySeq = 1<<53 - 1

// sourceHistoryEvent is the immutable canonical record a source_history leaf
// names: whether the source retains retiring segment text. The struct's field
// order is the stored byte order, so a re-marshal byte check rejects
// duplicate, unknown, and out-of-order fields at once.
type sourceHistoryEvent struct {
	Actor    string         `json:"actor"`
	Enabled  bool           `json:"enabled"`
	Seq      uint64         `json:"seq"`
	SourceID mousa.SourceID `json:"source_id"`
}

// encodeSourceHistoryEvent validates the event and returns its stored bytes
// and record ID.
func encodeSourceHistoryEvent(event sourceHistoryEvent) ([]byte, mousa.SHA256, error) {
	if err := validHistoryActor(event.Actor); err != nil {
		return nil, mousa.SHA256{}, wrap(CodeInvalidRecord, "encode source history", err)
	}
	if event.Seq == 0 || event.Seq > maxHistorySeq {
		return nil, mousa.SHA256{}, wrap(CodeInvalidRecord, "encode source history", errors.New("seq must be a positive JSON-safe integer"))
	}
	if event.SourceID == (mousa.SourceID{}) {
		return nil, mousa.SHA256{}, wrap(CodeInvalidRecord, "encode source history", errors.New("source ID must be nonzero"))
	}
	data, err := json.Marshal(event)
	if err != nil {
		return nil, mousa.SHA256{}, wrap(CodeInvalidRecord, "encode source history", err)
	}
	return data, mousa.SHA256(sha256.Sum256(data)), nil
}

// decodeSourceHistoryEvent verifies a stored event against its leaf: strict
// canonical bytes — re-marshal equality rejects duplicate, unknown, and
// out-of-order fields — then the sequence, record ID, and source the leaf
// carries.
func decodeSourceHistoryEvent(seq uint64, recordID, sourceID, record []byte) (sourceHistoryEvent, error) {
	var event sourceHistoryEvent
	if err := json.Unmarshal(record, &event); err != nil {
		return sourceHistoryEvent{}, integrity("decode source history", "record is not a source history event")
	}
	canonical, id, err := encodeSourceHistoryEvent(event)
	if err != nil {
		return sourceHistoryEvent{}, integrity("decode source history", err.Error())
	}
	if !bytes.Equal(canonical, record) {
		return sourceHistoryEvent{}, integrity("decode source history", "record is not canonical")
	}
	if event.Seq != seq || !bytes.Equal(id[:], recordID) || !bytes.Equal(event.SourceID[:], sourceID) {
		return sourceHistoryEvent{}, integrity("decode source history", "record disagrees with its leaf")
	}
	return event, nil
}

// validHistoryActor keeps actors to the same printable-ASCII shape the other
// per-source events record.
func validHistoryActor(actor string) error {
	if actor == "" || len(actor) > 256 {
		return errors.New("actor must be 1 to 256 bytes")
	}
	for i := 0; i < len(actor); i++ {
		if actor[i] < 0x20 || actor[i] > 0x7e {
			return errors.New("actor must be printable ASCII")
		}
	}
	return nil
}

// sourceHistoryAt reads the latest source-history setting for the source at
// the given log sequence. The typed event row and the log leaf are checked
// independently: the newest row on either side must pair with the other, so a
// missing half is integrity damage, not an older setting. Two nils mean the
// default: history off.
func sourceHistoryAt(ctx context.Context, q queryer, source mousa.SourceID, asOf uint64) (*sourceHistoryEvent, error) {
	var seq int64
	var eventID, record []byte
	eventErr := q.QueryRowContext(ctx, `SELECT seq, id, record_json FROM source_history_events
		WHERE source_id = ? AND seq <= ? ORDER BY seq DESC LIMIT 1`, source[:], asOf).Scan(&seq, &eventID, &record)
	var leafSeq int64
	var leafSource, leaf, recordID []byte
	leafErr := q.QueryRowContext(ctx, `SELECT seq, source_id, record_id, leaf_hash FROM log
		WHERE kind = ? AND source_id = ? AND seq <= ? ORDER BY seq DESC LIMIT 1`,
		logKindSourceHistory, source[:], asOf).Scan(&leafSeq, &leafSource, &recordID, &leaf)
	switch {
	case errors.Is(eventErr, sql.ErrNoRows) && errors.Is(leafErr, sql.ErrNoRows):
		return nil, nil
	case errors.Is(eventErr, sql.ErrNoRows) || errors.Is(leafErr, sql.ErrNoRows):
		return nil, integrity("read source history", "event row and log leaf disagree about existence")
	case eventErr != nil:
		return nil, classify("read source history", eventErr)
	case leafErr != nil:
		return nil, classify("read source history leaf", leafErr)
	case seq != leafSeq || !bytes.Equal(eventID, recordID):
		return nil, integrity("read source history", "event row and log leaf disagree")
	}
	if err := checkLeaf(logKindSourceHistory, leafSource, leaf, record, source[:]); err != nil {
		return nil, integrity("read source history", err.Error())
	}
	event, err := decodeSourceHistoryEvent(uint64(seq), recordID, source[:], record)
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// SetSourceHistory appends a source-history event for an existing source
// unless its latest event already sets the same state. It returns the event's
// record ID and whether one was appended.
func (store *Store) SetSourceHistory(ctx context.Context, source mousa.SourceID, enabled bool, actor string) (mousa.SHA256, bool, error) {
	var recordID mousa.SHA256
	changed := false
	err := store.writeImmediate(ctx, "set source history", func(conn *sql.Conn) error {
		if _, err := getSource(ctx, conn, source); err != nil {
			return err
		}
		latest, err := sourceHistoryAt(ctx, conn, source, maxHistorySeq)
		if err != nil {
			return err
		}
		if latest != nil && latest.Enabled == enabled {
			_, id, err := encodeSourceHistoryEvent(*latest)
			if err != nil {
				return err
			}
			recordID = id
			return nil
		}
		var tail uint64
		if err := conn.QueryRowContext(ctx, logIndexSQL).Scan(&tail); err != nil {
			return classify("read log index", err)
		}
		data, id, err := encodeSourceHistoryEvent(sourceHistoryEvent{
			Actor: actor, Enabled: enabled, Seq: tail + 1, SourceID: source,
		})
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO source_history_events(seq, id, source_id, record_json) VALUES(?, ?, ?, ?)`,
			tail+1, id[:], source[:], data); err != nil {
			if sqliteConstraint(err) {
				return wrap(CodeConflict, "set source history", err)
			}
			return classify("set source history", err)
		}
		if err := appendLog(ctx, conn, logKindSourceHistory, id[:], source[:], data); err != nil {
			return err
		}
		stored, err := sourceHistoryAt(ctx, conn, source, maxHistorySeq)
		if err != nil {
			return err
		}
		if stored == nil || stored.Seq != tail+1 || stored.Enabled != enabled || stored.Actor != actor {
			return integrity("set source history", "event read-back disagrees with write")
		}
		recordID = id
		changed = true
		return nil
	})
	if err != nil {
		return mousa.SHA256{}, false, err
	}
	return recordID, changed, nil
}

// keptSegmentText reads one segment's archived text, verified against its
// canonical segment exactly as indexed text is. A nil result means no
// archived row.
func keptSegmentText(ctx context.Context, q queryer, segment mousa.Segment) (*string, error) {
	var text string
	err := q.QueryRowContext(ctx, `SELECT text FROM segment_history WHERE segment_id = ?`, segment.ID[:]).Scan(&text)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, classify("read segment history", err)
	}
	return &text, verifyKeptText(segment, text)
}

// verifyKeptText validates archived text against its canonical segment:
// valid UTF-8, the digest the segment pins, and exact selector coverage.
func verifyKeptText(segment mousa.Segment, text string) error {
	if err := verifyLexicalPayload(segment, []byte(text), segment.ContentSHA256[:]); err != nil {
		return err
	}
	selector, ok := segment.Selector.TextByteRange()
	if !ok || uint64(len(text)) != selector.End-selector.Start {
		return integrity("read segment history", "archived text does not cover its segment")
	}
	return nil
}

// verifySegmentHistory checks every archived payload against its canonical
// segment inside one read snapshot.
func verifySegmentHistory(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT h.segment_id, h.text, s.representation_id, s.selector_start, s.selector_end, s.record_json
		FROM segment_history h LEFT JOIN segments s ON s.id = h.segment_id ORDER BY h.segment_id`)
	if err != nil {
		return startupError("scan segment history", err)
	}
	defer rows.Close()
	for rows.Next() {
		var rawID, repr, start, end, data []byte
		var text string
		if err := rows.Scan(&rawID, &text, &repr, &start, &end, &data); err != nil {
			return startupError("scan segment history", err)
		}
		if len(rawID) != len(mousa.SegmentID{}) || data == nil {
			return integrity("scan segment history", "archived text names a missing segment")
		}
		segment, err := decodeSegmentProjection(mousa.SegmentID(rawID), repr, start, end, data)
		if err != nil {
			return err
		}
		if err := verifyKeptText(segment, text); err != nil {
			return err
		}
	}
	return startupError("scan segment history", rows.Err())
}

// revisionText rebuilds one representation's exact text from its segments in
// selector order. Indexed text comes strictly from the live index; archived
// text comes from segment_history and is nil when any segment was never
// archived. The rebuild is checked against the canonical representation.
func revisionText(ctx context.Context, q queryer, id mousa.RepresentationID, indexed bool) (*string, error) {
	representation, err := getRepresentation(ctx, q, id)
	if err != nil {
		return nil, err
	}
	segments, err := lexicalSegments(ctx, q, id)
	if err != nil {
		return nil, err
	}
	var text strings.Builder
	var offset uint64
	missing := false
	for _, segment := range segments {
		selector, ok := segment.Selector.TextByteRange()
		if !ok || selector.Start != offset {
			return nil, integrity("rebuild revision text", "segments are not contiguous")
		}
		if indexed {
			piece, err := indexedSegmentText(ctx, q, segment)
			if err != nil {
				return nil, err
			}
			text.WriteString(piece)
		} else {
			kept, err := keptSegmentText(ctx, q, segment)
			if err != nil {
				return nil, err
			}
			if kept == nil {
				missing = true
			} else {
				text.WriteString(*kept)
			}
		}
		offset = selector.End
	}
	if missing {
		return nil, nil
	}
	rebuilt := text.String()
	sum := sha256.Sum256([]byte(rebuilt))
	if uint64(len(rebuilt)) != representation.ByteLength || sum != representation.ContentSHA256 {
		return nil, integrity("rebuild revision text", "rebuilt text disagrees with its representation")
	}
	return &rebuilt, nil
}

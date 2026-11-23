package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

// NoteStale explains why one citation no longer matches current source or
// segment state. Reasons are emitted without releasing any cited text.
type NoteStale struct {
	TrailID   mousa.SourceTrailID `json:"trail_id"`
	SegmentID *mousa.SegmentID    `json:"segment_id,omitempty"`
	Reason    string              `json:"reason"`
}

// NoteEvidence is the note metadata attached to a selected note segment. An
// internal note-policy segment must never be emitted without it.
type NoteEvidence struct {
	ID            mousa.SHA256         `json:"id"`
	Author        string               `json:"author"`
	CreatedAtUsec int64                `json:"created_at_usec"`
	DerivedFrom   []mousa.NoteCitation `json:"derived_from"`
	Origin        string               `json:"origin"`
	Stale         []NoteStale          `json:"stale"`
}

// ExportedNote is the exact canonical note record plus its exact stored text.
type ExportedNote struct {
	mousa.Note
	Text string `json:"text"`
}

const (
	noteStaleSourceWithdrawn = "source_withdrawn"
	noteStaleSourceDenied    = "source_denied"
	noteStaleSegmentRetired  = "segment_retired"
)

// RequireDocumentSource refuses document writes on a source that holds notes.
// An absent or empty source is still a document source.
func (store *Store) RequireDocumentSource(ctx context.Context, id mousa.SourceID) error {
	return requireDocumentSource(ctx, store.db, id)
}

func requireDocumentSource(ctx context.Context, q queryRower, id mousa.SourceID) error {
	hasNotes, err := sourceHasNotes(ctx, q, id)
	if err != nil {
		return classify("check document source", err)
	}
	if hasNotes {
		return wrap(CodeConflict, "check document source", errors.New("source holds durable notes, not documents"))
	}
	return nil
}

func sourceHasNotes(ctx context.Context, q queryRower, id mousa.SourceID) (bool, error) {
	var has bool
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notes WHERE source_id = ?)`, id[:]).Scan(&has); err != nil {
		return false, classify("check source notes", err)
	}
	return has, nil
}

// getNote reads one note's verified canonical record and exact text. It
// verifies the record bytes against the id, the ordered citation projection,
// the note-owned item and representation, the single indexed segment, the
// indexed text digest, and the log leaf.
func getNote(ctx context.Context, q queryer, sourceID mousa.SourceID, itemID string) (mousa.Note, string, error) {
	var rawID, record []byte
	err := q.QueryRowContext(ctx, `SELECT id, record_json FROM notes WHERE source_id = ? AND item_id = ?`, sourceID[:], itemID).Scan(&rawID, &record)
	if err != nil {
		return mousa.Note{}, "", readError("get note", err)
	}
	note, err := decodeCanonical(record, mousa.DecodeNote, mousa.EncodeNote)
	if err != nil {
		return mousa.Note{}, "", wrap(CodeIntegrity, "get note", err)
	}
	if len(rawID) != len(note.ID) || !bytes.Equal(rawID, note.ID[:]) || note.SourceID != sourceID || note.ItemID != itemID {
		return mousa.Note{}, "", integrity("get note", "note projection disagrees with its record")
	}
	var leafSource, leaf []byte
	err = q.QueryRowContext(ctx, `SELECT source_id, leaf_hash FROM log WHERE kind = ? AND record_id = ?`, mousa.LogKindNote, note.ID[:]).Scan(&leafSource, &leaf)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return mousa.Note{}, "", integrity("get note", "note has no log leaf")
	case err != nil:
		return mousa.Note{}, "", classify("get note leaf", err)
	}
	if err := checkLeaf(mousa.LogKindNote, leafSource, leaf, record, note.SourceID[:]); err != nil {
		return mousa.Note{}, "", integrity("get note leaf", err.Error())
	}
	rows, err := q.QueryContext(ctx, `SELECT citation_ordinal, segment_ordinal, trail_id, segment_id
		FROM note_citations WHERE note_id = ? ORDER BY citation_ordinal, segment_ordinal`, note.ID[:])
	if err != nil {
		return mousa.Note{}, "", classify("get note citations", err)
	}
	defer rows.Close()
	citationOrdinal, segmentOrdinal := 0, 0
	for rows.Next() {
		var co, so int
		var rawTrail, rawSegment []byte
		if err := rows.Scan(&co, &so, &rawTrail, &rawSegment); err != nil {
			return mousa.Note{}, "", classify("scan note citations", err)
		}
		if co != citationOrdinal || so != segmentOrdinal || citationOrdinal >= len(note.DerivedFrom) || segmentOrdinal >= len(note.DerivedFrom[co].SegmentIDs) {
			return mousa.Note{}, "", integrity("get note citations", "lookup rows disagree with the canonical lineage")
		}
		citation := note.DerivedFrom[co]
		if !bytes.Equal(rawTrail, citation.TrailID[:]) || !bytes.Equal(rawSegment, citation.SegmentIDs[so][:]) {
			return mousa.Note{}, "", integrity("get note citations", "lookup rows disagree with the canonical lineage")
		}
		if segmentOrdinal+1 < len(citation.SegmentIDs) {
			segmentOrdinal++
		} else {
			citationOrdinal++
			segmentOrdinal = 0
		}
	}
	if err := rows.Err(); err != nil {
		return mousa.Note{}, "", classify("scan note citations", err)
	}
	if citationOrdinal != len(note.DerivedFrom) {
		return mousa.Note{}, "", integrity("get note citations", "lookup rows are missing cited segments")
	}
	item, err := getLocalItem(ctx, q, sourceID, itemID)
	if err != nil {
		return mousa.Note{}, "", err
	}
	if !item.Active {
		return mousa.Note{}, "", integrity("get note", "note item is not active")
	}
	if item.SegmentationPolicy != mousa.TextSegmentNoteV1 {
		return mousa.Note{}, "", integrity("get note", "note item is not a note representation")
	}
	if item.Artifact.ContentSHA256 != note.ContentSHA256 {
		return mousa.Note{}, "", integrity("get note", "note item digest disagrees with its record")
	}
	segments, err := lexicalSegments(ctx, q, item.RepresentationID)
	if err != nil {
		return mousa.Note{}, "", err
	}
	if len(segments) != 1 || segments[0].ContentSHA256 != note.ContentSHA256 {
		return mousa.Note{}, "", integrity("get note", "note does not own exactly one content segment")
	}
	span, ok := segments[0].Selector.TextByteRange()
	if !ok || span.Start != 0 || span.End != item.Artifact.ByteLength || span.End == 0 || span.End > mousa.MaxUTF8TextSegmentBytes {
		return mousa.Note{}, "", integrity("get note", "note segment does not cover its whole artifact")
	}
	text, err := indexedSegmentText(ctx, q, segments[0])
	if err != nil {
		return mousa.Note{}, "", err
	}
	return note, text, nil
}

// verifyNoteRows canonically verifies every note inside the startup
// verification transaction.
func verifyNoteRows(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT source_id, item_id FROM notes ORDER BY source_id, item_id`)
	if err != nil {
		return classify("verify notes", err)
	}
	type key struct {
		source mousa.SourceID
		item   string
	}
	var keys []key
	for rows.Next() {
		var raw []byte
		var item string
		if err := rows.Scan(&raw, &item); err != nil {
			rows.Close()
			return classify("scan notes", err)
		}
		if len(raw) != len(mousa.SourceID{}) || item == "" {
			rows.Close()
			return integrity("verify notes", "invalid note identity")
		}
		k := key{item: item}
		copy(k.source[:], raw)
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return classify("scan notes", err)
	}
	if err := rows.Close(); err != nil {
		return classify("finish notes scan", err)
	}
	for _, k := range keys {
		if _, _, err := getNote(ctx, tx, k.source, k.item); err != nil {
			return err
		}
	}
	return nil
}

// resolveNoteCitations verifies submitted lineage inside the write transaction
// and expands an omitted segment list to the trail's selected segments, in
// trail order. Every cited trail must have allowed retrieval and its source
// must currently allow access and not be withdrawn.
func resolveNoteCitations(ctx context.Context, conn *sql.Conn, citations []mousa.NoteCitation, permittedSources map[mousa.SourceID]bool) ([]mousa.NoteCitation, error) {
	if len(citations) == 0 {
		return []mousa.NoteCitation{}, nil
	}
	resolved := make([]mousa.NoteCitation, 0, len(citations))
	trails := make(map[mousa.SourceTrailID]bool, len(citations))
	for _, citation := range citations {
		// Citations are (trail, segment) pairs: a segment may repeat across
		// distinct trails, but never inside one citation.
		segments := make(map[mousa.SegmentID]bool)
		if citation.TrailID == (mousa.SourceTrailID{}) {
			return nil, wrap(CodeInvalidRecord, "remember note", errors.New("citation trail_id must not be zero"))
		}
		if trails[citation.TrailID] {
			return nil, wrap(CodeInvalidRecord, "remember note", errors.New("citation repeats a trail"))
		}
		trails[citation.TrailID] = true
		trail, err := getSourceTrail(ctx, conn, citation.TrailID)
		if err != nil {
			if IsCode(err, CodeNotFound) {
				return nil, wrap(CodeNotFound, "remember note", fmt.Errorf("cited trail %s does not exist", citation.TrailID))
			}
			return nil, err
		}
		decisionID, err := mousa.ParsePolicyDecisionID(trail.DecisionID)
		if err != nil {
			return nil, wrap(CodeIntegrity, "remember note", err)
		}
		decision, err := getPolicyDecision(ctx, conn, decisionID)
		if err != nil {
			return nil, err
		}
		if decision.Outcome != mousa.PolicyOutcomeAllow {
			return nil, wrap(CodeConflict, "remember note", errors.New("cited trail did not allow retrieval"))
		}
		cited := decision.Request.SourceID
		if permittedSources != nil && !permittedSources[cited] {
			return nil, wrap(CodeConflict, "remember note", errors.New("cited source is not a configured source"))
		}
		access, _, err := snapshotAccess(ctx, conn, cited)
		if err != nil {
			return nil, err
		}
		if access != mousa.SourceAccessAllow {
			return nil, wrap(CodeConflict, "remember note", errors.New("cited source access is denied"))
		}
		state, err := getIngestState(ctx, conn, cited)
		if err != nil {
			if IsCode(err, CodeNotFound) {
				return nil, wrap(CodeConflict, "remember note", errors.New("cited source has no ingest state"))
			}
			return nil, err
		}
		if state.CollectionState == mousa.CollectionWithdrawn {
			return nil, wrap(CodeConflict, "remember note", errors.New("cited source is withdrawn"))
		}
		var selected []mousa.SegmentID
		selectedSet := make(map[mousa.SegmentID]bool)
		for _, candidate := range trail.Candidates {
			if candidate.Selected {
				selected = append(selected, candidate.SegmentID)
				selectedSet[candidate.SegmentID] = true
			}
		}
		wanted := citation.SegmentIDs
		switch {
		case len(wanted) == 0:
			if len(selected) == 0 {
				return nil, wrap(CodeInvalidRecord, "remember note", errors.New("cited trail selected no segments"))
			}
			wanted = selected
		default:
			for _, segment := range wanted {
				if segment == (mousa.SegmentID{}) {
					return nil, wrap(CodeInvalidRecord, "remember note", errors.New("citation segment id must not be zero"))
				}
				if !selectedSet[segment] {
					return nil, wrap(CodeInvalidRecord, "remember note", fmt.Errorf("cited segment %s was not selected by its trail", segment))
				}
			}
		}
		for _, segment := range wanted {
			if segments[segment] {
				return nil, wrap(CodeInvalidRecord, "remember note", errors.New("citation repeats a segment"))
			}
			segments[segment] = true
		}
		resolved = append(resolved, mousa.NoteCitation{TrailID: citation.TrailID, SegmentIDs: wanted})
	}
	return resolved, nil
}

// Remember writes one durable note in a single transaction: the note's item,
// canonical ancestry and indexes are applied through the caller's writer, then
// the canonical record, its citation lookup rows, and its log leaf are written
// and read back before commit. An identical retry returns the stored note.
func (store *Store) Remember(ctx context.Context, sourceID mousa.SourceID, author string, text []byte, citations []mousa.NoteCitation, permittedSources map[mousa.SourceID]bool, apply func(*Store, string, int64) error) (mousa.Note, error) {
	if err := store.requireWritable("remember note"); err != nil {
		return mousa.Note{}, err
	}
	var result mousa.Note
	err := store.writeImmediate(ctx, "remember note", func(conn *sql.Conn) error {
		if _, err := getSource(ctx, conn, sourceID); err != nil {
			return err
		}
		access, _, err := snapshotAccess(ctx, conn, sourceID)
		if err != nil {
			return err
		}
		if access != mousa.SourceAccessAllow {
			return wrap(CodeConflict, "remember note", errors.New("source access is denied"))
		}
		state, err := getIngestState(ctx, conn, sourceID)
		switch {
		case err == nil:
			if state.CollectionState == mousa.CollectionWithdrawn {
				return wrap(CodeConflict, "remember note", errors.New("source is withdrawn"))
			}
		case IsCode(err, CodeNotFound):
		default:
			return err
		}
		var documents bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM local_items AS li
			WHERE li.source_id = ? AND NOT EXISTS (
				SELECT 1 FROM notes AS n WHERE n.source_id = li.source_id AND n.item_id = li.item_id
			))`, sourceID[:]).Scan(&documents); err != nil {
			return classify("remember note", err)
		}
		if documents {
			return wrap(CodeConflict, "remember note", errors.New("source holds document items, not notes"))
		}
		resolved, err := resolveNoteCitations(ctx, conn, citations, permittedSources)
		if err != nil {
			return err
		}
		contentDigest := mousa.SHA256(sha256.Sum256(text))
		itemID := mousa.NoteItemID(contentDigest)
		existing, _, err := getNote(ctx, conn, sourceID, itemID)
		switch {
		case err == nil:
			result = existing
			return nil
		case IsCode(err, CodeNotFound):
		default:
			return err
		}
		note, err := mousa.NewNote(sourceID, author, contentDigest, time.Now().UnixMicro(), resolved)
		if err != nil {
			return wrap(CodeInvalidRecord, "remember note", err)
		}
		scoped := *store
		scoped.noteConn = conn
		if err := apply(&scoped, note.ItemID, note.CreatedAtUsec); err != nil {
			return err
		}
		data, err := mousa.EncodeNote(note)
		if err != nil {
			return wrap(CodeInvalidRecord, "remember note", err)
		}
		if err := checkRecordSize(data); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO notes(id, source_id, item_id, record_json) VALUES (?, ?, ?, ?)`, note.ID[:], sourceID[:], note.ItemID, data); err != nil {
			if sqliteConstraint(err) {
				return wrap(CodeConflict, "remember note", err)
			}
			return classify("remember note", err)
		}
		for citationOrdinal, citation := range note.DerivedFrom {
			for segmentOrdinal, segment := range citation.SegmentIDs {
				if _, err := conn.ExecContext(ctx, `INSERT INTO note_citations(note_id, citation_ordinal, segment_ordinal, trail_id, segment_id) VALUES (?, ?, ?, ?, ?)`,
					note.ID[:], citationOrdinal, segmentOrdinal, citation.TrailID[:], segment[:]); err != nil {
					return classify("remember note citation", err)
				}
			}
		}
		if err := appendLog(ctx, conn, mousa.LogKindNote, note.ID[:], sourceID[:], data); err != nil {
			return err
		}
		written, _, err := getNote(ctx, conn, sourceID, note.ItemID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(written, note) {
			return integrity("remember note", "exact read-back disagrees with write")
		}
		result = note
		return nil
	})
	if err != nil {
		return mousa.Note{}, err
	}
	return result, nil
}

// noteStaleness evaluates every citation's current source and segment state.
// The note stays immutable; the reasons describe what changed underneath it.
func noteStaleness(ctx context.Context, conn *sql.Conn, note mousa.Note) ([]NoteStale, error) {
	stale := []NoteStale{}
	for _, citation := range note.DerivedFrom {
		trail, err := getSourceTrail(ctx, conn, citation.TrailID)
		if err != nil {
			return nil, err
		}
		decisionID, err := mousa.ParsePolicyDecisionID(trail.DecisionID)
		if err != nil {
			return nil, wrap(CodeIntegrity, "note staleness", err)
		}
		decision, err := getPolicyDecision(ctx, conn, decisionID)
		if err != nil {
			return nil, err
		}
		cited := decision.Request.SourceID
		state, err := getIngestState(ctx, conn, cited)
		switch {
		case err == nil:
			if state.CollectionState == mousa.CollectionWithdrawn {
				stale = append(stale, NoteStale{TrailID: citation.TrailID, Reason: noteStaleSourceWithdrawn})
			}
		case IsCode(err, CodeNotFound):
			return nil, integrity("note staleness", "cited source has no ingest state")
		default:
			return nil, err
		}
		access, _, err := snapshotAccess(ctx, conn, cited)
		if err != nil {
			return nil, err
		}
		if access != mousa.SourceAccessAllow {
			stale = append(stale, NoteStale{TrailID: citation.TrailID, Reason: noteStaleSourceDenied})
		}
		for _, segment := range citation.SegmentIDs {
			var active bool
			if err := conn.QueryRowContext(ctx, `SELECT EXISTS(
				SELECT 1 FROM segments AS s
				JOIN local_items AS li ON li.representation_id = s.representation_id AND li.active = 1
				WHERE s.id = ? AND li.source_id = ?)`, segment[:], cited[:]).Scan(&active); err != nil {
				return nil, classify("note staleness", err)
			}
			if !active {
				stale = append(stale, NoteStale{TrailID: citation.TrailID, SegmentID: &segment, Reason: noteStaleSegmentRetired})
			}
		}
	}
	return stale, nil
}

// traceNoteEvidence resolves each selected candidate that belongs to a note.
// It runs after the trail is recorded, inside the same transaction, so the
// evidence snapshot matches the recorded selection.
func traceNoteEvidence(ctx context.Context, conn *sql.Conn, sourceID mousa.SourceID, trail mousa.SourceTrail, candidates []mousa.VerifiedLexicalCandidate) (map[mousa.SegmentID]NoteEvidence, error) {
	var notes map[mousa.SegmentID]NoteEvidence
	selected := false
	for _, candidate := range trail.Candidates {
		if candidate.Selected {
			selected = true
			break
		}
	}
	if !selected {
		return nil, nil
	}
	hasNotes, err := sourceHasNotes(ctx, conn, sourceID)
	if err != nil {
		return nil, err
	}
	if !hasNotes {
		return nil, nil
	}
	for index, candidate := range trail.Candidates {
		if !candidate.Selected || index >= len(candidates) || candidates[index].Disposition != mousa.CandidateAccepted {
			continue
		}
		var itemID string
		err := conn.QueryRowContext(ctx, `SELECT n.item_id FROM notes AS n
			JOIN local_items AS li ON li.source_id = n.source_id AND li.item_id = n.item_id
			WHERE li.source_id = ? AND li.representation_id = ? AND li.active = 1`,
			sourceID[:], candidates[index].Segment.RepresentationID[:]).Scan(&itemID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, classify("trace note evidence", err)
		}
		note, _, err := getNote(ctx, conn, sourceID, itemID)
		if err != nil {
			return nil, err
		}
		stale, err := noteStaleness(ctx, conn, note)
		if err != nil {
			return nil, err
		}
		evidence := NoteEvidence{
			ID: note.ID, Author: note.Author, CreatedAtUsec: note.CreatedAtUsec,
			DerivedFrom: note.DerivedFrom, Origin: "unsourced", Stale: stale,
		}
		if len(note.DerivedFrom) > 0 {
			evidence.Origin = "derived"
		}
		if notes == nil {
			notes = make(map[mousa.SegmentID]NoteEvidence)
		}
		notes[candidate.SegmentID] = evidence
	}
	return notes, nil
}

// ExportNotes releases every note of one source in stable item order, with
// the exact canonical record and exact stored text, only while the source
// allows access and is not withdrawn.
func (store *Store) ExportNotes(ctx context.Context, sourceID mousa.SourceID) ([]ExportedNote, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, classify("begin note export", err)
	}
	defer tx.Rollback()
	if _, err := getSource(ctx, tx, sourceID); err != nil {
		return nil, err
	}
	access, _, err := snapshotAccess(ctx, tx, sourceID)
	if err != nil {
		return nil, err
	}
	if access != mousa.SourceAccessAllow {
		return nil, wrap(CodeConflict, "export notes", errors.New("source access is denied"))
	}
	state, err := getIngestState(ctx, tx, sourceID)
	switch {
	case err == nil:
		if state.CollectionState == mousa.CollectionWithdrawn {
			return nil, wrap(CodeConflict, "export notes", errors.New("source is withdrawn"))
		}
	case IsCode(err, CodeNotFound):
	default:
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT item_id FROM notes WHERE source_id = ? ORDER BY item_id`, sourceID[:])
	if err != nil {
		return nil, classify("export notes", err)
	}
	var itemIDs []string
	for rows.Next() {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			rows.Close()
			return nil, classify("scan notes", err)
		}
		itemIDs = append(itemIDs, itemID)
	}
	if err := rows.Err(); err != nil {
		return nil, classify("scan notes", err)
	}
	if err := rows.Close(); err != nil {
		return nil, classify("finish notes scan", err)
	}
	exported := make([]ExportedNote, 0, len(itemIDs))
	for _, itemID := range itemIDs {
		note, text, err := getNote(ctx, tx, sourceID, itemID)
		if err != nil {
			return nil, err
		}
		exported = append(exported, ExportedNote{Note: note, Text: text})
	}
	if err := tx.Commit(); err != nil {
		return nil, classify("finish note export", err)
	}
	return exported, nil
}

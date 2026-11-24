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

type NoteStale = mousa.NoteStale
type NoteEvidence = mousa.NoteEvidence

// ExportedNote is the exact canonical note record plus its exact stored text.
type ExportedNote struct {
	mousa.Note
	Text string `json:"text"`
}

const (
	noteStaleSourceWithdrawn = "source_withdrawn"
	noteStaleSourceDenied    = "source_denied"
	noteStaleSegmentRetired  = "segment_retired"
	noteCitationUnverifiable = "citation_unverifiable"
)

// RequireDocumentSource refuses document writes on a source that holds notes.
// An absent or empty source is still a document source. Inside a document
// batch it joins the scoped write transaction.
func (store *Store) RequireDocumentSource(ctx context.Context, id mousa.SourceID) error {
	if store.writeConn != nil {
		return requireDocumentSource(ctx, store.writeConn, id)
	}
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
		trail, _, err := readTrailRecord(ctx, conn, citation.TrailID)
		if err != nil {
			return nil, err
		}
		if err := checkTrailAccess(ctx, conn, trail, permittedSources); err != nil {
			return nil, err
		}
		if err := checkTrailReferences(ctx, conn, trail); err != nil {
			return nil, err
		}
		var selected []mousa.SegmentID
		selectedSet := make(map[mousa.SegmentID]bool)
		for _, candidate := range trail.Candidates {
			if candidate.Packed != "none" {
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
	note, _, err := store.remember(ctx, sourceID, author, text, citations, permittedSources, apply, nil)
	return note, err
}

// RememberAdmitted writes a note under a served request admission: the
// authenticated principal authors the note, a denied destination maps to
// access denied, and the request outcome commits with the note itself in the
// same transaction. An identical existing note records a no-op that links the
// original record while its author stays untouched; the returned flag reports
// whether the note was created by this request.
func (store *Store) RememberAdmitted(ctx context.Context, sourceID mousa.SourceID, text []byte, citations []mousa.NoteCitation, permittedSources map[mousa.SourceID]bool, admission mousa.RequestAdmission, apply func(*Store, string, int64) error) (mousa.Note, bool, error) {
	return store.remember(ctx, sourceID, admission.Principal, text, citations, permittedSources, apply, &admission)
}

func (store *Store) remember(ctx context.Context, sourceID mousa.SourceID, author string, text []byte, citations []mousa.NoteCitation, permittedSources map[mousa.SourceID]bool, apply func(*Store, string, int64) error, admission *mousa.RequestAdmission) (mousa.Note, bool, error) {
	if err := store.requireWritable("remember note"); err != nil {
		return mousa.Note{}, false, err
	}
	var result mousa.Note
	created := false
	err := store.writeImmediate(ctx, "remember note", func(conn *sql.Conn) error {
		if admission != nil {
			finishAccess := store.StartStage(mousa.StageAccess)
			accessErr := requireAllowedSource(ctx, conn, sourceID)
			if accessErr == nil {
				accessErr = requireNoteItems(ctx, conn, sourceID)
			}
			finishAccess(accessErr)
			if accessErr != nil {
				return accessErr
			}
		} else if err := requireNoteDestination(ctx, conn, sourceID); err != nil {
			return err
		}
		var finishCitations func(error)
		if len(citations) == 0 {
			store.ObserveMarker(mousa.StageCitations, mousa.StagePhaseSkip, 0)
			finishCitations = func(error) {}
		} else {
			finishCitations = store.StartStage(mousa.StageCitations)
		}
		resolved, err := resolveNoteCitations(ctx, conn, citations, permittedSources)
		finishCitations(err)
		if err != nil {
			return err
		}
		contentDigest := mousa.SHA256(sha256.Sum256(text))
		itemID := mousa.NoteItemID(contentDigest)
		finishNote := store.StartStage(mousa.StageNote)
		existing, _, err := getNote(ctx, conn, sourceID, itemID)
		switch {
		case err == nil:
			if admission != nil {
				// A replay returns the original canonical note; deny it
				// when any originally cited trail lies outside the
				// caller's read grants so the reply cannot reveal
				// lineage the caller cannot resolve. The denial carries
				// no note record or identifiers.
				for _, citation := range existing.DerivedFrom {
					trail, _, err := readTrailRecord(ctx, conn, citation.TrailID)
					if err != nil {
						finishNote(err)
						return err
					}
					if err := checkTrailAccess(ctx, conn, trail, permittedSources); err != nil {
						finishNote(err)
						return err
					}
				}
				if err := finishRequest(ctx, conn, *admission, mousa.RequestStateNoOp, "", mousa.RequestEvidence{NoteID: &existing.ID}); err != nil {
					finishNote(err)
					return err
				}
			}
			result = existing
			finishNote(nil)
			return nil
		case IsCode(err, CodeNotFound):
		default:
			finishNote(err)
			return err
		}
		note, err := mousa.NewNote(sourceID, author, contentDigest, time.Now().UnixMicro(), resolved)
		if err != nil {
			err = wrap(CodeInvalidRecord, "remember note", err)
			finishNote(err)
			return err
		}
		if err := store.writeNote(ctx, conn, note, func(scoped *Store) error {
			return apply(scoped, note.ItemID, note.CreatedAtUsec)
		}); err != nil {
			finishNote(err)
			return err
		}
		if admission != nil {
			if err := finishRequest(ctx, conn, *admission, mousa.RequestStateCommitted, "", mousa.RequestEvidence{NoteID: &note.ID}); err != nil {
				finishNote(err)
				return err
			}
		}
		result = note
		created = true
		finishNote(nil)
		return nil
	})
	if err != nil {
		return mousa.Note{}, false, err
	}
	return result, created, nil
}

func requireNoteDestination(ctx context.Context, q queryer, sourceID mousa.SourceID) error {
	if err := requireNoteAccess(ctx, q, sourceID); err != nil {
		return err
	}
	return requireNoteItems(ctx, q, sourceID)
}

// requireNoteAccess keeps the offline conflict-coded destination check so
// direct callers keep their existing error; admitted requests use the typed
// access-denied variant.
func requireNoteAccess(ctx context.Context, q queryer, sourceID mousa.SourceID) error {
	decision, err := currentSourceDecision(ctx, q, sourceID)
	if err != nil {
		return err
	}
	if decision.Access != mousa.SourceAccessAllow || decision.WithdrawalID != nil {
		return wrap(CodeConflict, "write note", errors.New("destination source must allow access and not be withdrawn"))
	}
	return nil
}

func requireNoteItems(ctx context.Context, q queryer, sourceID mousa.SourceID) error {
	var documents bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM local_items li WHERE li.source_id = ? AND NOT EXISTS (
			SELECT 1 FROM notes n WHERE n.source_id = li.source_id AND n.item_id = li.item_id
		))`, sourceID[:]).Scan(&documents)
	if err != nil {
		return classify("check note destination", err)
	}
	if documents {
		return wrap(CodeConflict, "write note", errors.New("source holds document items, not notes"))
	}
	return nil
}

func (store *Store) writeNote(ctx context.Context, conn *sql.Conn, note mousa.Note, apply func(*Store) error) error {
	data, err := mousa.EncodeNote(note)
	if err != nil {
		return wrap(CodeInvalidRecord, "write note", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	scoped := *store
	scoped.writeConn = conn
	scoped.noteWrite = true
	if err := apply(&scoped); err != nil {
		return err
	}
	sourceID := note.SourceID
	if _, err := conn.ExecContext(ctx, `INSERT INTO notes(id, source_id, item_id, record_json) VALUES (?, ?, ?, ?)`,
		note.ID[:], sourceID[:], note.ItemID, data); err != nil {
		if sqliteConstraint(err) {
			return wrap(CodeConflict, "write note", err)
		}
		return classify("write note", err)
	}
	for ci, citation := range note.DerivedFrom {
		for si, segment := range citation.SegmentIDs {
			if _, err := conn.ExecContext(ctx, `INSERT INTO note_citations(note_id, citation_ordinal, segment_ordinal, trail_id, segment_id) VALUES (?, ?, ?, ?, ?)`,
				note.ID[:], ci, si, citation.TrailID[:], segment[:]); err != nil {
				return classify("write note citation", err)
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
		return integrity("write note", "exact read-back disagrees with write")
	}
	return nil
}

// ImportNotes preserves note records verbatim and treats an identical existing
// note as a retry. A conflicting item or invalid record rolls the batch back.
func (store *Store) ImportNotes(ctx context.Context, sourceID mousa.SourceID, notes []ExportedNote, apply func(*Store, ExportedNote) error) (int, error) {
	created := 0
	err := store.writeImmediate(ctx, "import notes", func(conn *sql.Conn) error {
		if err := requireNoteDestination(ctx, conn, sourceID); err != nil {
			return err
		}
		for _, exported := range notes {
			note := exported.Note
			if note.SourceID != sourceID {
				return wrap(CodeInvalidRecord, "import notes", errors.New("record belongs to a different source"))
			}
			if _, err := mousa.EncodeNote(note); err != nil {
				return wrap(CodeInvalidRecord, "import notes", err)
			}
			if digest := mousa.SHA256(sha256.Sum256([]byte(exported.Text))); digest != note.ContentSHA256 {
				return wrap(CodeInvalidRecord, "import notes", errors.New("text digest disagrees with note"))
			}
			existing, text, err := getNote(ctx, conn, sourceID, note.ItemID)
			switch {
			case err == nil:
				if !reflect.DeepEqual(existing, note) || text != exported.Text {
					return wrap(CodeConflict, "import notes", errors.New("note item already has a different record"))
				}
				continue
			case !IsCode(err, CodeNotFound):
				return err
			}
			if err := store.writeNote(ctx, conn, note, func(scoped *Store) error {
				return apply(scoped, exported)
			}); err != nil {
				return err
			}
			created++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return created, nil
}

// noteStaleness evaluates every citation's current source and segment state.
// The note stays immutable; the reasons describe what changed underneath it.
func noteStaleness(ctx context.Context, q queryer, note mousa.Note) ([]NoteStale, error) {
	_, stale, err := noteLineage(ctx, q, note, nil)
	return stale, err
}

// noteLineage evaluates every citation against the caller's reach. A nil
// grants map exposes the full durable lineage for offline and MCP reads. A
// served caller sees only what its own read grants could resolve: a missing
// or unauthorized cited trail drops the citation and its stale details
// together, so the response never names lineage the caller could not reach.
func noteLineage(ctx context.Context, q queryer, note mousa.Note, grants map[mousa.SourceID]bool) ([]mousa.NoteCitation, []NoteStale, error) {
	citations := note.DerivedFrom
	stale := []NoteStale{}
	if grants != nil {
		citations = []mousa.NoteCitation{}
	}
	for _, citation := range note.DerivedFrom {
		trail, _, err := readTrailRecord(ctx, q, citation.TrailID)
		if IsCode(err, CodeNotFound) {
			if grants == nil {
				stale = append(stale, NoteStale{TrailID: citation.TrailID, Reason: noteCitationUnverifiable})
			}
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if grants != nil {
			if err := checkTrailAccess(ctx, q, trail, grants); err != nil {
				if IsCode(err, CodeNotFound) {
					continue
				}
				return nil, nil, err
			}
		}
		if err := checkTrailReferences(ctx, q, trail); err != nil {
			return nil, nil, err
		}
		reasons, err := citationStaleness(ctx, q, trail, citation.SegmentIDs)
		if err != nil {
			return nil, nil, err
		}
		stale = append(stale, reasons...)
		if grants != nil {
			citations = append(citations, citation)
		}
	}
	return citations, stale, nil
}

func citationStaleness(ctx context.Context, q queryer, trail mousa.Trail, segments []mousa.SegmentID) ([]NoteStale, error) {
	packed := make(map[mousa.SegmentID][]mousa.SourceID)
	for _, candidate := range trail.Candidates {
		if candidate.Packed != "none" {
			packed[candidate.SegmentID] = candidate.SourceIDs
		}
	}
	for _, segment := range segments {
		if len(packed[segment]) == 0 {
			return []NoteStale{{TrailID: trail.ID, Reason: noteCitationUnverifiable}}, nil
		}
	}
	stale := []NoteStale{}
	seen := make(map[mousa.SourceID]bool)
	withdrawn, denied := false, false
	for _, segment := range segments {
		for _, cited := range packed[segment] {
			if seen[cited] {
				continue
			}
			seen[cited] = true
			decision, err := currentSourceDecision(ctx, q, cited)
			if err != nil {
				return nil, err
			}
			withdrawn = withdrawn || decision.WithdrawalID != nil
			denied = denied || (decision.Outcome != "allow" &&
				(decision.WithdrawalID == nil || decision.Access != mousa.SourceAccessAllow))
		}
		var indexed bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM segment_lexical_rows WHERE segment_id = ?)`,
			segment[:]).Scan(&indexed); err != nil {
			return nil, classify("recheck cited segment", err)
		}
		if !indexed {
			stale = append(stale, NoteStale{TrailID: trail.ID, SegmentID: &segment, Reason: noteStaleSegmentRetired})
		}
	}
	if withdrawn {
		stale = append(stale, NoteStale{TrailID: trail.ID, Reason: noteStaleSourceWithdrawn})
	}
	if denied {
		stale = append(stale, NoteStale{TrailID: trail.ID, Reason: noteStaleSourceDenied})
	}
	return stale, nil
}

func queryNoteEvidence(ctx context.Context, q queryer, source mousa.SourceID, item string, grants map[mousa.SourceID]bool) (*mousa.NoteEvidence, error) {
	note, _, err := getNote(ctx, q, source, item)
	if err != nil {
		return nil, lexicalStructuralError("read note passage", err)
	}
	citations, stale, err := noteLineage(ctx, q, note, grants)
	if err != nil {
		return nil, err
	}
	origin := "unsourced"
	if len(note.DerivedFrom) > 0 {
		origin = "derived"
	}
	return &mousa.NoteEvidence{
		ID: note.ID, Author: note.Author, CreatedAtUsec: note.CreatedAtUsec,
		DerivedFrom: citations, Origin: origin, Stale: stale,
	}, nil
}

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

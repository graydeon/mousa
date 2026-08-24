package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"

	"github.com/graydeon/mousa/internal/mousa"
)

// LexicalCandidate is one verified raw FTS5 candidate.
type LexicalCandidate struct {
	Segment mousa.Segment
	Text    string
	BM25    float64
}

// IndexTextRepresentation indexes every canonical Segment of one text Representation.
func (store *Store) IndexTextRepresentation(ctx context.Context, id mousa.RepresentationID, content []byte) error {
	if err := store.requireWritable("index text representation"); err != nil {
		return err
	}
	return store.writeImmediate(ctx, "index text representation", func(conn *sql.Conn) error {
		representation, err := getRepresentation(ctx, conn, id)
		if err != nil {
			return err
		}
		if _, err := mousa.SegmentUTF8Text(representation, content); err != nil {
			return wrap(CodeInvalidRecord, "index text representation", err)
		}
		segments, err := lexicalSegments(ctx, conn, id)
		if err != nil {
			return err
		}
		if len(segments) == 0 {
			return wrap(CodeNotFound, "index text representation", errors.New("representation has no segments"))
		}
		for _, segment := range segments {
			if err := segment.ValidateContentAgainst(representation, content); err != nil {
				return wrap(CodeInvalidRecord, "index text representation", err)
			}
			selector, _ := segment.Selector.TextByteRange()
			if selector.End-selector.Start > mousa.MaxUTF8TextSegmentBytes {
				return wrap(CodeResourceLimit, "index text representation", errors.New("segment text exceeds maximum byte length"))
			}
			text := content[selector.Start:selector.End]
			if err := putLexicalRow(ctx, conn, segment, text); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO segment_lexical_fts(segment_lexical_fts) VALUES('integrity-check')`); err != nil {
			return classify("check lexical index", err)
		}
		return verifyLexicalRecords(ctx, conn)
	})
}

// searchLexical returns verified FTS5 candidates in raw BM25 order through q.
func searchLexical(ctx context.Context, q queryer, expression string, limit int) ([]LexicalCandidate, error) {
	if !utf8.ValidString(expression) || len(expression) < 1 || len(expression) > 4096 || containsNUL(expression) || limit < 1 || limit > 100 {
		return nil, wrap(CodeInvalidQuery, "search lexical", errors.New("query expression or limit is out of bounds"))
	}
	if err := verifyLexicalRecords(ctx, q); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
		SELECT r.segment_id, f.text, f.content_sha256, bm25(segment_lexical_fts)
		FROM segment_lexical_fts AS f
		JOIN segment_lexical_rows AS r ON r.rowid = f.rowid
		WHERE segment_lexical_fts MATCH ?
		ORDER BY bm25(segment_lexical_fts) ASC, r.segment_id ASC
		LIMIT ?`, expression, limit)
	if err != nil {
		return nil, lexicalQueryError(ctx, err)
	}
	type rawCandidate struct {
		id     []byte
		text   string
		digest []byte
		score  float64
	}
	rawCandidates := make([]rawCandidate, 0)
	for rows.Next() {
		var candidate rawCandidate
		if err := rows.Scan(&candidate.id, &candidate.text, &candidate.digest, &candidate.score); err != nil {
			rows.Close()
			return nil, lexicalIntegrityError(ctx, "scan lexical candidate", err)
		}
		candidate.id = append([]byte(nil), candidate.id...)
		candidate.digest = append([]byte(nil), candidate.digest...)
		rawCandidates = append(rawCandidates, candidate)
	}
	if err := rows.Close(); err != nil {
		return nil, lexicalQueryError(ctx, err)
	}
	candidates := make([]LexicalCandidate, 0, len(rawCandidates))
	seen := make(map[mousa.SegmentID]struct{}, len(rawCandidates))
	for _, candidate := range rawCandidates {
		segment, err := lexicalSegmentByRawID(ctx, q, candidate.id)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[segment.ID]; duplicate {
			return nil, integrity("search lexical", "duplicate candidate")
		}
		seen[segment.ID] = struct{}{}
		if math.IsNaN(candidate.score) || math.IsInf(candidate.score, 0) {
			return nil, integrity("search lexical", "non-finite BM25")
		}
		if err := verifyLexicalPayload(segment, []byte(candidate.text), candidate.digest); err != nil {
			return nil, err
		}
		candidates = append(candidates, LexicalCandidate{Segment: segment, Text: candidate.text, BM25: candidate.score})
	}
	return candidates, nil
}

func lexicalSegments(ctx context.Context, q queryer, id mousa.RepresentationID) ([]mousa.Segment, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id FROM segments
		WHERE representation_id = ?
		ORDER BY selector_start, selector_end, id`, id[:])
	if err != nil {
		return nil, classify("load lexical segments", err)
	}
	var ids [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return nil, classify("load lexical segments", err)
		}
		ids = append(ids, append([]byte(nil), raw...))
	}
	if err := rows.Close(); err != nil {
		return nil, classify("load lexical segments", err)
	}
	segments := make([]mousa.Segment, 0, len(ids))
	for _, raw := range ids {
		segment, err := lexicalSegmentByRawID(ctx, q, raw)
		if err != nil {
			return nil, err
		}
		segments = append(segments, segment)
	}
	return segments, nil
}

func putLexicalRow(ctx context.Context, conn *sql.Conn, segment mousa.Segment, text []byte) error {
	var rowID int64
	err := conn.QueryRowContext(ctx, `SELECT rowid FROM segment_lexical_rows WHERE segment_id = ?`, segment.ID[:]).Scan(&rowID)
	if errors.Is(err, sql.ErrNoRows) {
		result, insertErr := conn.ExecContext(ctx, `INSERT INTO segment_lexical_rows(segment_id) VALUES(?)`, segment.ID[:])
		if insertErr != nil {
			if sqliteConstraint(insertErr) {
				return wrap(CodeConflict, "index lexical relation", insertErr)
			}
			return classify("index lexical relation", insertErr)
		}
		rowID, err = result.LastInsertId()
		if err != nil {
			return wrap(CodeIntegrity, "index lexical relation", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO segment_lexical_fts(rowid, text, content_sha256) VALUES(?, ?, ?)`, rowID, string(text), segment.ContentSHA256[:]); err != nil {
			return classify("index lexical content", err)
		}
		return verifyLexicalRow(ctx, conn, rowID, segment)
	}
	if err != nil {
		return classify("load lexical relation", err)
	}
	var storedText string
	var storedDigest []byte
	if err := conn.QueryRowContext(ctx, `SELECT text, content_sha256 FROM segment_lexical_fts WHERE rowid = ?`, rowID).Scan(&storedText, &storedDigest); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return wrap(CodeConflict, "index lexical content", err)
		}
		return classify("load lexical content", err)
	}
	if storedText != string(text) || !equalBytes(storedDigest, segment.ContentSHA256[:]) {
		return wrap(CodeConflict, "index lexical content", errors.New("stored lexical row disagrees with canonical Segment"))
	}
	return verifyLexicalRow(ctx, conn, rowID, segment)
}

func verifyLexicalRecords(ctx context.Context, q queryer) error {
	var relationCount, ftsCount int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM segment_lexical_rows`).Scan(&relationCount); err != nil {
		return lexicalIntegrityError(ctx, "verify lexical relations", err)
	}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM segment_lexical_fts`).Scan(&ftsCount); err != nil {
		return lexicalIntegrityError(ctx, "verify lexical content", err)
	}
	if relationCount != ftsCount {
		return integrity("verify lexical index", fmt.Sprintf("relation count %d disagrees with FTS count %d", relationCount, ftsCount))
	}
	rows, err := q.QueryContext(ctx, `
		SELECT r.rowid, r.segment_id, f.text, f.content_sha256
		FROM segment_lexical_rows AS r
		LEFT JOIN segment_lexical_fts AS f ON f.rowid = r.rowid
		ORDER BY r.rowid`)
	if err != nil {
		return lexicalIntegrityError(ctx, "verify lexical index", err)
	}
	type lexicalRow struct {
		rowID  int64
		rawID  []byte
		text   sql.NullString
		digest []byte
	}
	stored := make([]lexicalRow, 0, relationCount)
	for rows.Next() {
		var row lexicalRow
		if err := rows.Scan(&row.rowID, &row.rawID, &row.text, &row.digest); err != nil {
			rows.Close()
			return lexicalIntegrityError(ctx, "verify lexical index", err)
		}
		row.rawID = append([]byte(nil), row.rawID...)
		row.digest = append([]byte(nil), row.digest...)
		stored = append(stored, row)
	}
	if err := rows.Close(); err != nil {
		return lexicalIntegrityError(ctx, "verify lexical index", err)
	}
	if len(stored) != relationCount {
		return integrity("verify lexical index", "relation scan count disagrees")
	}
	for _, row := range stored {
		if !row.text.Valid {
			return integrity("verify lexical index", "relation has no FTS row")
		}
		segment, err := lexicalSegmentByRawID(ctx, q, row.rawID)
		if err != nil {
			return err
		}
		if err := verifyLexicalPayload(segment, []byte(row.text.String), row.digest); err != nil {
			return err
		}
	}
	return nil
}

func verifyLexicalRow(ctx context.Context, q queryer, rowID int64, segment mousa.Segment) error {
	var rawID, digest []byte
	var text string
	if err := q.QueryRowContext(ctx, `
		SELECT r.segment_id, f.text, f.content_sha256
		FROM segment_lexical_rows AS r
		JOIN segment_lexical_fts AS f ON f.rowid = r.rowid
		WHERE r.rowid = ?`, rowID).Scan(&rawID, &text, &digest); err != nil {
		return lexicalIntegrityError(ctx, "verify lexical row", err)
	}
	if !equalBytes(rawID, segment.ID[:]) {
		return integrity("verify lexical row", "relation Segment ID disagrees")
	}
	return verifyLexicalPayload(segment, []byte(text), digest)
}

func lexicalSegmentByRawID(ctx context.Context, q queryRower, raw []byte) (mousa.Segment, error) {
	if len(raw) != sha256.Size {
		return mousa.Segment{}, integrity("verify lexical index", "invalid Segment ID length")
	}
	var id mousa.SegmentID
	copy(id[:], raw)
	segment, err := getSegment(ctx, q, id)
	if err != nil {
		if IsCode(err, CodeNotFound) {
			return mousa.Segment{}, integrity("verify lexical index", "orphan Segment ID")
		}
		return mousa.Segment{}, err
	}
	return segment, nil
}

func verifyLexicalPayload(segment mousa.Segment, text, digest []byte) error {
	if len(text) > mousa.MaxUTF8TextSegmentBytes || !utf8.Valid(text) {
		return integrity("verify lexical content", "stored text is malformed")
	}
	got := sha256.Sum256(text)
	if !equalBytes(digest, segment.ContentSHA256[:]) || !equalBytes(got[:], segment.ContentSHA256[:]) {
		return integrity("verify lexical content", "stored text digest disagrees with canonical Segment")
	}
	return nil
}

func lexicalQueryError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	classified := classify("search lexical", err)
	if IsCode(classified, CodeBusy) || IsCode(classified, CodeIntegrity) {
		return classified
	}
	return wrap(CodeInvalidQuery, "search lexical", err)
}

func lexicalIntegrityError(ctx context.Context, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	classified := classify(op, err)
	if IsCode(classified, CodeBusy) || IsCode(classified, CodeReadOnly) || IsCode(classified, CodeIntegrity) {
		return classified
	}
	return wrap(CodeIntegrity, op, err)
}

func containsNUL(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] == 0 {
			return true
		}
	}
	return false
}

// SearchLexical returns verified FTS5 candidates in raw BM25 order.
func (store *Store) SearchLexical(ctx context.Context, expression string, limit int) ([]LexicalCandidate, error) {
	return searchLexical(ctx, store.db, expression, limit)
}

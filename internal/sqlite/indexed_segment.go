package sqlite

import (
	"context"
	"database/sql"

	"github.com/graydeon/mousa/internal/mousa"
)

const indexedSegmentTextSQL = `
		SELECT f.text, f.content_sha256
		FROM segment_lexical_rows AS r
		JOIN segment_lexical_fts AS f ON f.rowid = r.rowid
		WHERE r.segment_id = ?`

// indexedSegmentText reads one segment's verified indexed text. The lexical row is the
// released-text source for retrieval, so an active item without its index rows is an
// integrity condition instead of a silent omission.
func indexedSegmentText(ctx context.Context, q queryer, segment mousa.Segment) (string, error) {
	var text string
	var digest []byte
	err := q.QueryRowContext(ctx, indexedSegmentTextSQL, segment.ID[:]).Scan(&text, &digest)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", integrity("indexed segment text", "active segment has no lexical row")
		}
		return "", classify("indexed segment text", err)
	}
	if err := verifyLexicalPayload(segment, []byte(text), digest); err != nil {
		return "", err
	}
	return text, nil
}

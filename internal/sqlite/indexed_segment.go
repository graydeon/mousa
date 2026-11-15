package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
)

// localItemPathOfRepresentation derives the item identity of one revision of a local item,
// verifying the observation identity that binds it. It is the store-side counterpart of the
// consumer's item derivation for selected evidence.
func localItemPathOfRepresentation(ctx context.Context, q queryer, sourceID mousa.SourceID, id mousa.RepresentationID) (string, error) {
	artifact, err := representationSourceArtifact(ctx, q, id)
	if err != nil {
		return "", err
	}
	observation, err := getObservation(ctx, q, artifact.ObservationID)
	if err != nil {
		return "", err
	}
	if observation.SourceID != sourceID {
		return "", integrity("local item path", "representation belongs to another source")
	}
	suffix := fmt.Sprintf("@%x", artifact.ContentSHA256)
	rest, ok := strings.CutPrefix(observation.ExternalObservationID, "item/")
	if !ok || !strings.HasSuffix(rest, suffix) {
		return "", integrity("local item path", "observation is not a local item")
	}
	item := strings.TrimSuffix(rest, suffix)
	if item == "" {
		return "", integrity("local item path", "item identity disagrees with artifact digest")
	}
	return item, nil
}

// indexedSegmentText reads one segment's verified indexed text. The lexical row is the
// released-text source for retrieval, so an active item without its index rows is an
// integrity condition instead of a silent omission.
func indexedSegmentText(ctx context.Context, q queryer, segment mousa.Segment) (string, error) {
	var text string
	var digest []byte
	err := q.QueryRowContext(ctx, `
		SELECT f.text, f.content_sha256
		FROM segment_lexical_rows AS r
		JOIN segment_lexical_fts AS f ON f.rowid = r.rowid
		WHERE r.segment_id = ?`, segment.ID[:]).Scan(&text, &digest)
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

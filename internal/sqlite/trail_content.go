package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/graydeon/mousa/internal/mousa"
)

// verifyExactTrailContent checks canonical ancestry and bytes still retained in the index.
// Retired text is not archived: its immutable digest and range remain checkable, not its bytes.
func verifyExactTrailContent(ctx context.Context, q queryer, trail mousa.SourceTrail, sourceID mousa.SourceID) error {
	type retainedText struct {
		id        string
		text      string
		available bool
	}
	retained := make(map[mousa.SHA256][]retainedText)
	// The enclosing trail read holds one transaction snapshot. Keep only source
	// membership here; each candidate still verifies its own segment and text.
	verifiedRepresentations := make(map[mousa.RepresentationID]struct{})
	for _, candidate := range trail.Candidates {
		segment, err := getSegment(ctx, q, candidate.SegmentID)
		if err != nil {
			return err
		}
		if segment.ContentSHA256 != candidate.ContentSHA256 {
			return integrity("verify exact trail", "candidate digest disagrees with canonical segment")
		}
		if candidate.Disposition != mousa.CandidateAccepted {
			continue
		}
		span, ok := segment.Selector.TextByteRange()
		if !ok || span.End-span.Start != candidate.TextBytes {
			return integrity("verify exact trail", "candidate size disagrees with canonical segment")
		}
		if _, verified := verifiedRepresentations[segment.RepresentationID]; !verified {
			paths, err := lexicalEvidencePaths(ctx, q, segment)
			if err != nil {
				return err
			}
			inSource := false
			for _, path := range paths {
				if path.SourceID == sourceID {
					inSource = true
					break
				}
			}
			if !inSource {
				return integrity("verify exact trail", "candidate does not derive from decision source")
			}
			verifiedRepresentations[segment.RepresentationID] = struct{}{}
		}
		var text string
		var digest []byte
		err = q.QueryRowContext(ctx, `SELECT f.text, f.content_sha256 FROM segment_lexical_rows r JOIN segment_lexical_fts f ON f.rowid = r.rowid WHERE r.segment_id = ?`, segment.ID[:]).Scan(&text, &digest)
		available := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return classify("verify exact trail text", err)
		}
		if available {
			if err := verifyLexicalPayload(segment, []byte(text), digest); err != nil {
				return err
			}
			if uint64(len(text)) != candidate.TextBytes {
				return integrity("verify exact trail", "indexed text length disagrees with candidate")
			}
		}
		for _, previous := range retained[candidate.ContentSHA256] {
			if !available || !previous.available {
				continue
			}
			if text == previous.text {
				if candidate.DuplicateOf != previous.id {
					return integrity("verify exact trail", "byte-equal candidate does not reference first retained passage")
				}
				break
			}
			if candidate.DuplicateOf == previous.id {
				return integrity("verify exact trail", "duplicate relationship disagrees with verified bytes")
			}
		}
		if candidate.Selected && candidate.Omission == "" {
			retained[candidate.ContentSHA256] = append(retained[candidate.ContentSHA256], retainedText{candidate.SegmentID.String(), text, available})
		}
	}
	return nil
}

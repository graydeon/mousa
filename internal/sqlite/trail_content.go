package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/graydeon/mousa/internal/mousa"
)

// retainedText is one passage a trail releases while it is being verified: identity, verified text
// and whether the store still retains the indexed bytes.
type retainedText struct {
	id        string
	text      string
	available bool
}

// verifyTrailContent checks a stored trail's released bytes against canonical store content: every
// recorded segment still decodes, still carries the recorded digest, still has the recorded size in
// its coordinates, still derives from the decision source, and its retained indexed text still
// hashes to that digest. Retired text is not archived, so its immutable digest and range remain
// checkable, not its bytes.
//
// The recorded packing policy decides which candidate relationships the trail may assert. Exact-v1
// names the first retained byte-equal passage and omits later ones; original keeps every accepted
// passage whose bytes fit and names no relationship, so byte-equal passages are ordinary there.
// This is content verification, not structural self-consistency: the records below are read from the
// store, not compared against fields the trail already carries.
func verifyTrailContent(ctx context.Context, q queryer, trail mousa.SourceTrail, sourceID mousa.SourceID) error {
	exact := trail.PackingPolicy == mousa.PackingExactV1
	retained := make(map[mousa.SHA256][]retainedText)
	// The enclosing trail read holds one transaction snapshot. A representation is read back once
	// per query; every segment still checks its own membership and coordinates against it.
	representations := make(map[mousa.RepresentationID]mousa.Representation)
	for _, candidate := range trail.Candidates {
		segment, err := getSegment(ctx, q, candidate.SegmentID)
		if err != nil {
			return err
		}
		if segment.ContentSHA256 != candidate.ContentSHA256 {
			return integrity("verify trail", "candidate digest disagrees with canonical segment")
		}
		if candidate.Disposition != mousa.CandidateAccepted {
			continue
		}
		span, ok := segment.Selector.TextByteRange()
		if !ok || span.End-span.Start != candidate.TextBytes {
			return integrity("verify trail", "candidate size disagrees with canonical segment")
		}
		if err := verifyTrailRepresentation(ctx, q, segment, sourceID, representations); err != nil {
			return err
		}
		text, available, err := retainedIndexedText(ctx, q, segment)
		if err != nil {
			return err
		}
		if available && uint64(len(text)) != candidate.TextBytes {
			return integrity("verify trail", "indexed text length disagrees with candidate")
		}
		if !exact {
			continue
		}
		for _, previous := range retained[candidate.ContentSHA256] {
			if !available || !previous.available {
				continue
			}
			if text == previous.text {
				if candidate.DuplicateOf != previous.id {
					return integrity("verify trail", "byte-equal candidate does not reference first retained passage")
				}
				break
			}
			if candidate.DuplicateOf == previous.id {
				return integrity("verify trail", "duplicate relationship disagrees with verified bytes")
			}
		}
		if candidate.Selected && candidate.Omission == "" {
			retained[candidate.ContentSHA256] = append(retained[candidate.ContentSHA256], retainedText{candidate.SegmentID.String(), text, available})
		}
	}
	return nil
}

// verifyTrailRepresentation checks one released segment against its canonical representation. The
// representation record and its ancestry are read once per query and reused, but membership and
// coordinates are checked for every segment, so a later passage of an already verified
// representation cannot escape the check. Nothing here consults the mutable current-item pointer,
// so historical trails stay readable after a revision, deactivation or deletion.
func verifyTrailRepresentation(ctx context.Context, q queryer, segment mousa.Segment, sourceID mousa.SourceID, representations map[mousa.RepresentationID]mousa.Representation) error {
	representation, verified := representations[segment.RepresentationID]
	if !verified {
		var err error
		representation, err = getRepresentation(ctx, q, segment.RepresentationID)
		if err != nil {
			return err
		}
		segmentPaths, err := lexicalEvidencePaths(ctx, q, segment)
		if err != nil {
			return err
		}
		inSource := false
		for _, path := range segmentPaths {
			if path.SourceID == sourceID {
				inSource = true
				break
			}
		}
		if !inSource {
			return integrity("verify trail", "candidate does not derive from decision source")
		}
		representations[segment.RepresentationID] = representation
	}
	if err := segment.ValidateAgainst(representation); err != nil {
		return wrap(CodeIntegrity, "verify trail representation", err)
	}
	return nil
}

// representationItem derives and caches the local item identity of one representation. The
// identity comes from the immutable representation, artifact and observation chain.
func representationItem(ctx context.Context, q queryer, sourceID mousa.SourceID, id mousa.RepresentationID, cache map[mousa.RepresentationID]string) (string, error) {
	if item, cached := cache[id]; cached {
		return item, nil
	}
	item, err := localItemPathOfRepresentation(ctx, q, sourceID, id)
	if err != nil {
		return "", err
	}
	cache[id] = item
	return item, nil
}

// retainedIndexedText checks indexed text against the segment digest. Retired indexed text is
// unavailable; its immutable digest and range remain checkable.
func retainedIndexedText(ctx context.Context, q queryer, segment mousa.Segment) (string, bool, error) {
	var text string
	var digest []byte
	err := q.QueryRowContext(ctx, `SELECT f.text, f.content_sha256 FROM segment_lexical_rows r JOIN segment_lexical_fts f ON f.rowid = r.rowid WHERE r.segment_id = ?`, segment.ID[:]).Scan(&text, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, classify("verify trail text", err)
	}
	if err := verifyLexicalPayload(segment, []byte(text), digest); err != nil {
		return "", false, err
	}
	return text, true, nil
}

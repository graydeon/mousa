package sqlite

import (
	"cmp"
	"context"
	"database/sql"
	"slices"

	"github.com/graydeon/mousa/internal/mousa"
)

type RecheckResult struct {
	ID                            mousa.SHA256        `json:"id"`
	AsOf                          uint64              `json:"as_of,omitempty"`
	Status                        string              `json:"status"`
	Stale                         []NoteStale         `json:"stale"`
	Released                      *[]RecheckedSegment `json:"released,omitempty"`
	UnverifiableMetaPathOmissions int64               `json:"unverifiable_meta_path_omissions,omitzero"`
}

// RecheckedSegment is one released passage's current text availability:
// indexed when the live index still serves it, kept when only the
// revision-history archive holds it, not_kept when neither does.
type RecheckedSegment struct {
	SegmentID mousa.SegmentID `json:"segment_id"`
	Text      *string         `json:"text,omitempty"`
	Status    string          `json:"status"`
}

func (store *Store) RecheckNote(ctx context.Context, source mousa.SourceID, id mousa.SHA256) (RecheckResult, error) {
	return store.recheck(ctx, id, func(q queryer, _ *RecheckResult) (string, []NoteStale, error) {
		decision, err := currentSourceDecision(ctx, q, source)
		if err != nil {
			return "", nil, err
		}
		if decision.Outcome != "allow" {
			return "", nil, readError("recheck note", sql.ErrNoRows)
		}
		var item string
		if err := q.QueryRowContext(ctx, `SELECT item_id FROM notes WHERE source_id = ? AND id = ?`,
			source[:], id[:]).Scan(&item); err != nil {
			return "", nil, readError("recheck note", err)
		}
		note, _, err := getNote(ctx, q, source, item)
		if err != nil {
			return "", nil, err
		}
		if note.ID != id {
			return "", nil, integrity("recheck note", "lookup and record disagree")
		}
		if len(note.DerivedFrom) == 0 {
			return "unsourced", []NoteStale{}, nil
		}
		stale, err := noteStaleness(ctx, q, note)
		return "fresh", stale, err
	})
}

func (store *Store) RecheckTrail(ctx context.Context, id mousa.SourceTrailID) (RecheckResult, error) {
	return store.recheckTrail(ctx, id, nil, false)
}

// RecheckTrailText is RecheckTrail plus each released passage's current text
// availability. All reads require current source access. Served reads also
// require caller grants. Both report the same not-found a missing trail
// reports.
func (store *Store) RecheckTrailText(ctx context.Context, id mousa.SourceTrailID, grants map[mousa.SourceID]bool) (RecheckResult, error) {
	return store.recheckTrail(ctx, id, grants, true)
}

func (store *Store) recheckTrail(ctx context.Context, id mousa.SourceTrailID, grants map[mousa.SourceID]bool, text bool) (RecheckResult, error) {
	return store.recheck(ctx, mousa.SHA256(id), func(q queryer, result *RecheckResult) (string, []NoteStale, error) {
		trail, _, err := readTrailRecord(ctx, q, id)
		if err != nil {
			if grants != nil && IsCode(err, CodeIntegrity) {
				return "", nil, missingTrail()
			}
			return "", nil, err
		}
		if grants != nil {
			// The served read reports the trail, not the store's log index.
			result.AsOf = 0
		}
		if text {
			// Nil grants skip the caller grant check, not source access.
			// Access is checked before references so an unauthorized caller
			// cannot distinguish integrity failures on the trail's records.
			if err := checkTrailAccess(ctx, q, trail, grants); err != nil {
				if grants != nil && IsCode(err, CodeIntegrity) {
					return "", nil, missingTrail()
				}
				return "", nil, err
			}
		}
		histories := pathHistories{}
		unverifiable, err := verifyTrailReferences(ctx, q, trail, histories)
		if err != nil {
			return "", nil, err
		}
		result.UnverifiableMetaPathOmissions = unverifiable
		var segments []mousa.SegmentID
		for _, candidate := range trail.Candidates {
			if candidate.Packed != "none" {
				segments = append(segments, candidate.SegmentID)
			}
		}
		if text {
			released := []RecheckedSegment{}
			packed := slices.DeleteFunc(slices.Clone(trail.Candidates), func(c mousa.Candidate) bool { return c.Packed != "full" })
			slices.SortFunc(packed, func(a, b mousa.Candidate) int { return cmp.Compare(a.Rank, b.Rank) })
			for _, candidate := range packed {
				segment, err := getSegment(ctx, q, candidate.SegmentID)
				if err != nil {
					return "", nil, lexicalStructuralError("recheck trail text", err)
				}
				entry := RecheckedSegment{SegmentID: candidate.SegmentID, Status: "not_kept"}
				var hasIndex bool
				if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM segment_lexical_rows WHERE segment_id = ?)`, segment.ID[:]).Scan(&hasIndex); err != nil {
					return "", nil, classify("recheck trail text", err)
				}
				if hasIndex {
					piece, err := indexedSegmentText(ctx, q, segment)
					if err != nil {
						return "", nil, err
					}
					entry.Text = &piece
					entry.Status = "indexed"
				} else {
					kept, err := keptSegmentText(ctx, q, segment)
					if err != nil {
						return "", nil, err
					}
					if kept != nil {
						entry.Text = kept
						entry.Status = "kept"
					}
				}
				released = append(released, entry)
			}
			result.Released = &released
		}
		stale := []NoteStale{}
		if trail.Retrieval != nil && len(trail.Retrieval.Branches) != 0 && trail.Retrieval.Branches[0].SharedBM25 != nil {
			changed, err := sharedBM25Changed(ctx, q, trail)
			if err != nil {
				return "", nil, err
			}
			if changed {
				stale = append(stale, NoteStale{TrailID: trail.ID, Reason: "bm25_statistics_changed"})
			}
		}
		if len(segments) == 0 {
			return "empty", stale, nil
		}
		cited, err := citationStaleness(ctx, q, trail, segments)
		return "fresh", append(stale, cited...), err
	})
}

func (store *Store) recheck(ctx context.Context, id mousa.SHA256, check func(queryer, *RecheckResult) (string, []NoteStale, error)) (RecheckResult, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RecheckResult{}, classify("begin recheck", err)
	}
	defer tx.Rollback()
	result := RecheckResult{ID: id}
	if err := tx.QueryRowContext(ctx, logIndexSQL).Scan(&result.AsOf); err != nil {
		return RecheckResult{}, classify("read recheck index", err)
	}
	result.Status, result.Stale, err = check(tx, &result)
	if err != nil {
		return RecheckResult{}, err
	}
	if len(result.Stale) != 0 {
		result.Status = "stale"
	}
	if err := tx.Commit(); err != nil {
		return RecheckResult{}, classify("finish recheck", err)
	}
	return result, nil
}

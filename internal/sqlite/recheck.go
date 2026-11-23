package sqlite

import (
	"context"
	"database/sql"

	"github.com/graydeon/mousa/internal/mousa"
)

type RecheckResult struct {
	ID     mousa.SHA256 `json:"id"`
	AsOf   uint64       `json:"as_of"`
	Status string       `json:"status"`
	Stale  []NoteStale  `json:"stale"`
}

func (store *Store) RecheckNote(ctx context.Context, source mousa.SourceID, id mousa.SHA256) (RecheckResult, error) {
	return store.recheck(ctx, id, func(q queryer) (string, []NoteStale, error) {
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
	return store.recheck(ctx, mousa.SHA256(id), func(q queryer) (string, []NoteStale, error) {
		trail, _, err := readTrailRecord(ctx, q, id)
		if err != nil {
			return "", nil, err
		}
		if err := checkTrailReferences(ctx, q, trail); err != nil {
			return "", nil, err
		}
		var segments []mousa.SegmentID
		for _, candidate := range trail.Candidates {
			if candidate.Packed != "none" {
				segments = append(segments, candidate.SegmentID)
			}
		}
		if len(segments) == 0 {
			return "empty", []NoteStale{}, nil
		}
		stale, err := citationStaleness(ctx, q, trail, segments)
		return "fresh", stale, err
	})
}

func (store *Store) recheck(ctx context.Context, id mousa.SHA256, check func(queryer) (string, []NoteStale, error)) (RecheckResult, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RecheckResult{}, classify("begin recheck", err)
	}
	defer tx.Rollback()
	result := RecheckResult{ID: id}
	if err := tx.QueryRowContext(ctx, logIndexSQL).Scan(&result.AsOf); err != nil {
		return RecheckResult{}, classify("read recheck index", err)
	}
	result.Status, result.Stale, err = check(tx)
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

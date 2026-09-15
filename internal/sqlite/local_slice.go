package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/graydeon/mousa/internal/mousa"
)

// Local-slice store support. These are read/de-index helpers the local
// vertical slice needs; they add no schema, no migration, and no new
// canonical record. De-indexing a segment removes its lexical (FTS5) row so
// the content stops being retrievable while every canonical record — segment,
// representation, artifact, observation — remains as historical evidence.

// RepresentationSourceArtifact returns the source Artifact of one
// Representation: the derivation input of kind 'artifact' that anchors the
// representation's provenance chain. A representation with several artifact
// inputs is a CodeIntegrity condition for this helper, because the local
// slice derives every representation from exactly one artifact.
func (store *Store) RepresentationSourceArtifact(ctx context.Context, id mousa.RepresentationID) (mousa.Artifact, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return mousa.Artifact{}, ctxErr
		}
		return mousa.Artifact{}, classify("begin representation artifact read", err)
	}
	defer tx.Rollback()
	artifact, err := representationSourceArtifact(ctx, tx, id)
	if err != nil {
		return mousa.Artifact{}, err
	}
	if err := tx.Commit(); err != nil {
		return mousa.Artifact{}, classify("finish representation artifact read", err)
	}
	return artifact, nil
}

func representationSourceArtifact(ctx context.Context, q queryer, id mousa.RepresentationID) (mousa.Artifact, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT a.id
		FROM representation_inputs AS ri
		JOIN artifacts AS a ON a.id = ri.artifact_id
		WHERE ri.representation_id = ? AND ri.input_kind = 'artifact'
		ORDER BY a.id`, id[:])
	if err != nil {
		return mousa.Artifact{}, classify("load representation inputs", err)
	}
	defer rows.Close()
	var artifactIDs [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return mousa.Artifact{}, classify("scan representation inputs", err)
		}
		artifactIDs = append(artifactIDs, raw)
	}
	if err := rows.Err(); err != nil {
		return mousa.Artifact{}, classify("load representation inputs", err)
	}
	if len(artifactIDs) == 0 {
		return mousa.Artifact{}, wrap(CodeNotFound, "representation source artifact", errors.New("representation has no artifact input"))
	}
	if len(artifactIDs) > 1 {
		return mousa.Artifact{}, wrap(CodeIntegrity, "representation source artifact", fmt.Errorf("representation has %d artifact inputs", len(artifactIDs)))
	}
	var artifactID mousa.ArtifactID
	copy(artifactID[:], artifactIDs[0])
	return getArtifact(ctx, q, artifactID)
}

// SourceObservationIDs returns every observation stored for one source.
func (store *Store) SourceObservationIDs(ctx context.Context, sourceID mousa.SourceID) ([]mousa.ObservationID, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, classify("begin source observations read", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM observations WHERE source_id = ? ORDER BY id`, sourceID[:])
	if err != nil {
		return nil, classify("load source observations", err)
	}
	defer rows.Close()
	var ids []mousa.ObservationID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, classify("scan source observations", err)
		}
		if len(raw) != len(mousa.ObservationID{}) {
			return nil, wrap(CodeIntegrity, "load source observations", errors.New("observation id has unexpected length"))
		}
		var id mousa.ObservationID
		copy(id[:], raw)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, classify("load source observations", err)
	}
	return ids, nil
}

// RemoveSegmentFromIndex removes one segment's lexical index row so the
// content stops being retrievable. Canonical records are untouched: the
// operation runs in one writer transaction, is idempotent for an already
// removed segment, and re-verifies the remaining lexical rows it can see.
func (store *Store) RemoveSegmentFromIndex(ctx context.Context, segmentID mousa.SegmentID) error {
	if err := store.requireWritable("remove segment from index"); err != nil {
		return err
	}
	return store.writeImmediate(ctx, "remove segment from index", func(conn *sql.Conn) error {
		var rowID int64
		err := conn.QueryRowContext(ctx, `SELECT rowid FROM segment_lexical_rows WHERE segment_id = ?`, segmentID[:]).Scan(&rowID)
		if errors.Is(err, sql.ErrNoRows) {
			// Nothing indexed for this segment: idempotent no-op.
			return nil
		}
		if err != nil {
			return classify("load lexical relation", err)
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM segment_lexical_fts WHERE rowid = ?`, rowID); err != nil {
			return lexicalQueryError(ctx, err)
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM segment_lexical_rows WHERE rowid = ?`, rowID); err != nil {
			return classify("remove lexical relation", err)
		}
		return nil
	})
}

// RepresentationSegments returns the canonical Segments of one representation
// in selector order.
func (store *Store) RepresentationSegments(ctx context.Context, id mousa.RepresentationID) ([]mousa.Segment, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, classify("begin representation segments read", err)
	}
	defer tx.Rollback()
	segments, err := lexicalSegments(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, classify("finish representation segments read", err)
	}
	return segments, nil
}

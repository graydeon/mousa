package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/graydeon/mousa/internal/mousa"
)

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

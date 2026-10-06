package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"github.com/graydeon/mousa/internal/mousa"
)

// maxSupersessionDeclarationBytes bounds one stored canonical declaration and mirrors the
// record_json check in migration 0012.
const maxSupersessionDeclarationBytes = 64 << 10

// PutSupersessionDeclaration appends one immutable supersession declaration.
//
// The append runs in the store's single writer transaction and validates the domain record and
// both pinned revisions inside it: the declared source must exist, and each pinned representation
// must be the canonical revision that the declaration attributes to that source and item. The
// current local-items pointer is never consulted, so a declaration may pin a revision that is
// updated, deactivated, deleted or never activated; a missing source or revision is never created
// implicitly.
//
// An exact retry verifies the stored bytes, projections and provenance and succeeds without adding
// a second row. A reused declaration identity whose stored bytes, projections or provenance
// disagree is a conflict, and the existing row is neither overwritten nor repaired. There is no
// update or delete path.
func (store *Store) PutSupersessionDeclaration(ctx context.Context, declaration mousa.SupersessionDeclaration) error {
	if err := store.requireWritable("put supersession declaration"); err != nil {
		return err
	}
	data, err := mousa.EncodeSupersessionDeclaration(declaration)
	if err != nil {
		return wrap(CodeInvalidRecord, "put supersession declaration", err)
	}
	if len(data) > maxSupersessionDeclarationBytes {
		return wrap(CodeResourceLimit, "put supersession declaration", fmt.Errorf("canonical declaration size %d exceeds %d", len(data), maxSupersessionDeclarationBytes))
	}
	return store.writeImmediate(ctx, "put supersession declaration", func(conn *sql.Conn) error {
		if err := verifySupersessionTargets(ctx, conn, declaration); err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `INSERT INTO supersession_declarations(id, source_id, predecessor_item_id, predecessor_representation_id, successor_item_id, successor_representation_id, record_json) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			declaration.ID[:], declaration.SourceID[:], declaration.PredecessorItemID, declaration.PredecessorRepresentationID[:], declaration.SuccessorItemID, declaration.SuccessorRepresentationID[:], data)
		if err != nil {
			if !sqliteConstraint(err) {
				return classify("put supersession declaration", err)
			}
			if verifyErr := verifySupersessionDeclaration(ctx, conn, declaration, data); verifyErr != nil {
				if IsCode(verifyErr, CodeNotFound) || IsCode(verifyErr, CodeIntegrity) {
					return wrap(CodeConflict, "put supersession declaration", verifyErr)
				}
				return verifyErr
			}
			return nil
		}
		if err := oneRow(result); err != nil {
			return wrap(CodeIntegrity, "put supersession declaration", err)
		}
		return verifySupersessionDeclaration(ctx, conn, declaration, data)
	})
}

// GetSupersessionDeclaration returns one stored declaration after verifying its canonical
// encoding, identity, every projected column and pinned provenance in a single read snapshot. It
// exposes no document text, decides no authorization and never consults the current local-items
// pointer, so historical declarations stay readable after an update, deactivation or deletion.
func (store *Store) GetSupersessionDeclaration(ctx context.Context, declarationID mousa.SupersessionDeclarationID) (mousa.SupersessionDeclaration, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return mousa.SupersessionDeclaration{}, classify("begin supersession declaration read", err)
	}
	defer tx.Rollback()
	declaration, err := readSupersessionDeclaration(ctx, tx, declarationID)
	if err != nil {
		return mousa.SupersessionDeclaration{}, err
	}
	if err := tx.Commit(); err != nil {
		return mousa.SupersessionDeclaration{}, classify("finish supersession declaration read", err)
	}
	return declaration, nil
}

// readSupersessionDeclaration reads one row and verifies it against its canonical record: the
// record must decode as canonical bytes, carry the requested identity, agree with every projected
// column, and still resolve to the source and item revisions it pins.
func readSupersessionDeclaration(ctx context.Context, q queryer, declarationID mousa.SupersessionDeclarationID) (mousa.SupersessionDeclaration, error) {
	var rawSource, rawPredecessorRepresentation, rawSuccessorRepresentation, data []byte
	var predecessorItemID, successorItemID string
	if err := q.QueryRowContext(ctx, `SELECT source_id, predecessor_item_id, predecessor_representation_id, successor_item_id, successor_representation_id, record_json FROM supersession_declarations WHERE id = ?`, declarationID[:]).
		Scan(&rawSource, &predecessorItemID, &rawPredecessorRepresentation, &successorItemID, &rawSuccessorRepresentation, &data); err != nil {
		return mousa.SupersessionDeclaration{}, readError("get supersession declaration", err)
	}
	record, err := decodeCanonical(data, mousa.DecodeSupersessionDeclaration, mousa.EncodeSupersessionDeclaration)
	if err != nil {
		return mousa.SupersessionDeclaration{}, wrap(CodeIntegrity, "get supersession declaration", err)
	}
	if len(rawSource) != len(mousa.SourceID{}) || len(rawPredecessorRepresentation) != len(mousa.RepresentationID{}) || len(rawSuccessorRepresentation) != len(mousa.RepresentationID{}) {
		return mousa.SupersessionDeclaration{}, integrity("get supersession declaration", "invalid identity projection")
	}
	var sourceID mousa.SourceID
	copy(sourceID[:], rawSource)
	var predecessorRepresentationID, successorRepresentationID mousa.RepresentationID
	copy(predecessorRepresentationID[:], rawPredecessorRepresentation)
	copy(successorRepresentationID[:], rawSuccessorRepresentation)
	if record.ID != declarationID ||
		record.SourceID != sourceID ||
		record.PredecessorItemID != predecessorItemID ||
		record.PredecessorRepresentationID != predecessorRepresentationID ||
		record.SuccessorItemID != successorItemID ||
		record.SuccessorRepresentationID != successorRepresentationID {
		return mousa.SupersessionDeclaration{}, integrity("get supersession declaration", "relational projection disagrees with record")
	}
	if err := verifySupersessionTargets(ctx, q, record); err != nil {
		return mousa.SupersessionDeclaration{}, err
	}
	return record, nil
}

// verifySupersessionDeclaration requires one stored declaration to match an exact write, including
// its canonical bytes, so a retry cannot silently accept a different record under the same identity.
func verifySupersessionDeclaration(ctx context.Context, q queryer, want mousa.SupersessionDeclaration, wantData []byte) error {
	got, err := readSupersessionDeclaration(ctx, q, want.ID)
	if err != nil {
		return err
	}
	encoded, err := mousa.EncodeSupersessionDeclaration(got)
	if err != nil || !bytes.Equal(encoded, wantData) || !reflect.DeepEqual(got, want) {
		return integrity("verify supersession declaration", "exact read-back disagrees with write")
	}
	return nil
}

// verifySupersessionTargets proves that the canonical source exists and that both pinned
// revisions are the exact source-item revisions the declaration names. Existence proves only that
// the revision is canonical history: local_items records the current pointer, not activation
// history, so this proves nothing about whether or when a revision was activated.
func verifySupersessionTargets(ctx context.Context, q queryer, declaration mousa.SupersessionDeclaration) error {
	if _, err := getSource(ctx, q, declaration.SourceID); err != nil {
		return err
	}
	for _, pin := range []struct {
		item           string
		representation mousa.RepresentationID
	}{
		{declaration.PredecessorItemID, declaration.PredecessorRepresentationID},
		{declaration.SuccessorItemID, declaration.SuccessorRepresentationID},
	} {
		if err := verifySupersessionRevision(ctx, q, declaration.SourceID, pin.item, pin.representation); err != nil {
			return err
		}
	}
	return nil
}

// verifySupersessionRevision resolves one pinned representation to its canonical ancestry and
// requires the declared source and item to be the ones that ancestry names.
func verifySupersessionRevision(ctx context.Context, q queryer, sourceID mousa.SourceID, itemID string, representationID mousa.RepresentationID) error {
	if _, err := getRepresentation(ctx, q, representationID); err != nil {
		return err
	}
	item, err := localItemPathOfRepresentation(ctx, q, sourceID, representationID)
	if err != nil {
		return err
	}
	if item != itemID {
		return integrity("verify supersession revision", "revision does not belong to the declared item")
	}
	return nil
}

// verifySupersessionDeclarationRecords verifies every stored declaration, its projections and its
// pinned provenance as part of the startup integrity path at the current schema version.
func verifySupersessionDeclarationRecords(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM supersession_declarations ORDER BY id`)
	if err != nil {
		return startupError("scan supersession declarations", err)
	}
	var ids [][]byte
	for rows.Next() {
		var id []byte
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return startupError("scan supersession declarations", err)
		}
		ids = append(ids, append([]byte(nil), id...))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return startupError("scan supersession declarations", err)
	}
	if err := rows.Close(); err != nil {
		return startupError("scan supersession declarations", err)
	}
	for _, raw := range ids {
		if len(raw) != len(mousa.SupersessionDeclarationID{}) {
			return integrity("scan supersession declarations", "invalid ID length")
		}
		var id mousa.SupersessionDeclarationID
		copy(id[:], raw)
		if _, err := readSupersessionDeclaration(ctx, db, id); err != nil {
			return err
		}
	}
	return nil
}

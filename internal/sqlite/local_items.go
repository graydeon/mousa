package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/graydeon/mousa/internal/mousa"
)

// ApplyDocumentBatch commits the successful prefix in one transaction. A
// savepoint prevents a failed item from leaving partial canonical history.
// The scoped store is valid only during apply and must not be retained.
func (store *Store) ApplyDocumentBatch(ctx context.Context, count int, apply func(scoped *Store, index int) error) (int, error) {
	if count == 0 {
		return 0, nil
	}
	if store.writeConn != nil {
		return 0, wrap(CodeConflict, "sync batch", errors.New("nested document batch"))
	}
	var completed int
	var itemErr error
	err := store.writeImmediate(ctx, "sync batch", func(conn *sql.Conn) error {
		scoped := *store
		scoped.writeConn = conn
		for i := range count {
			if _, err := conn.ExecContext(ctx, `SAVEPOINT sync_item`); err != nil {
				return classify("start sync item", err)
			}
			itemErr = apply(&scoped, i)
			if itemErr != nil {
				if _, err := conn.ExecContext(ctx, `ROLLBACK TO sync_item; RELEASE sync_item`); err != nil {
					return errors.Join(itemErr, classify("roll back sync item", err))
				}
				break
			}
			if _, err := conn.ExecContext(ctx, `RELEASE sync_item`); err != nil {
				return classify("finish sync item", err)
			}
			completed++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return completed, itemErr
}

// LocalItem records current activation separately from immutable revision history.
// Inactive items retain their last representation so a restore is not an addition.
type LocalItem struct {
	RepresentationID   mousa.RepresentationID
	Artifact           mousa.Artifact
	Active             bool
	SegmentationPolicy string
}

// GetLocalItem reads the indexed current-item relationship and its verified ancestry.
func (store *Store) GetLocalItem(ctx context.Context, sourceID mousa.SourceID, itemID string) (LocalItem, error) {
	if store.writeConn != nil {
		return getLocalItem(ctx, store.writeConn, sourceID, itemID)
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return LocalItem{}, classify("begin local item read", err)
	}
	defer tx.Rollback()
	item, err := getLocalItem(ctx, tx, sourceID, itemID)
	if err != nil {
		return LocalItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return LocalItem{}, classify("finish local item read", err)
	}
	return item, nil
}

func getLocalItem(ctx context.Context, q queryer, sourceID mousa.SourceID, itemID string) (LocalItem, error) {
	var raw []byte
	var active int
	if err := q.QueryRowContext(ctx, `SELECT representation_id, active FROM local_items WHERE source_id = ? AND item_id = ?`, sourceID[:], itemID).Scan(&raw, &active); err != nil {
		return LocalItem{}, readError("get local item", err)
	}
	if len(raw) != len(mousa.RepresentationID{}) || active < 0 || active > 1 {
		return LocalItem{}, integrity("get local item", "invalid current-item projection")
	}
	item := LocalItem{Active: active == 1}
	copy(item.RepresentationID[:], raw)
	representation, err := getRepresentation(ctx, q, item.RepresentationID)
	if err != nil {
		return LocalItem{}, err
	}
	item.SegmentationPolicy, err = mousa.TextSegmentationPolicy(representation)
	if err != nil {
		return LocalItem{}, wrap(CodeIntegrity, "get local item segmentation", err)
	}
	artifact, err := representationSourceArtifact(ctx, q, item.RepresentationID)
	if err != nil {
		return LocalItem{}, err
	}
	observation, err := getObservation(ctx, q, artifact.ObservationID)
	if err != nil {
		return LocalItem{}, err
	}
	if observation.SourceID != sourceID {
		return LocalItem{}, integrity("get local item", "representation belongs to another source")
	}
	item.Artifact = artifact
	if observation.ExternalObservationID != fmt.Sprintf("item/%s@%x", itemID, artifact.ContentSHA256) {
		return LocalItem{}, integrity("get local item", "item identity disagrees with observation")
	}
	return item, nil
}

// ActivateLocalItem atomically replaces the prior index rows and current pointer.
// The canonical representation and segments must already exist. A failed prepare
// may leave immutable history, but only a committed activation becomes searchable.
func (store *Store) ActivateLocalItem(ctx context.Context, sourceID mousa.SourceID, itemID string, representationID mousa.RepresentationID, content []byte) (string, error) {
	if err := store.requireWritable("activate local item"); err != nil {
		return "", err
	}
	if itemID == "" || !utf8.ValidString(itemID) {
		return "", wrap(CodeInvalidRecord, "activate local item", errors.New("item ID must be nonempty UTF-8"))
	}
	var action string
	err := store.writeImmediate(ctx, "activate local item", func(conn *sql.Conn) error {
		if !store.noteWrite {
			if err := requireDocumentSource(ctx, conn, sourceID); err != nil {
				return err
			}
		}
		representation, err := getRepresentation(ctx, conn, representationID)
		if err != nil {
			return err
		}
		if !store.noteWrite {
			if policy, policyErr := mousa.TextSegmentationPolicy(representation); policyErr == nil && policy == mousa.TextSegmentNoteV1 {
				return wrap(CodeConflict, "activate local item", errors.New("note segments are internal to durable notes"))
			}
		}
		expected, err := mousa.SegmentUTF8Text(representation, content)
		if err != nil {
			return wrap(CodeInvalidRecord, "activate local item", err)
		}
		artifact, err := representationSourceArtifact(ctx, conn, representationID)
		if err != nil {
			return err
		}
		observation, err := getObservation(ctx, conn, artifact.ObservationID)
		if err != nil {
			return err
		}
		if observation.SourceID != sourceID {
			return integrity("activate local item", "representation belongs to another source")
		}
		if observation.ExternalObservationID != fmt.Sprintf("item/%s@%x", itemID, artifact.ContentSHA256) {
			return integrity("activate local item", "item identity disagrees with observation")
		}
		stored, err := lexicalSegments(ctx, conn, representationID)
		if err != nil {
			return err
		}
		if len(stored) != len(expected) {
			return integrity("activate local item", "incomplete representation segments")
		}
		// Both lists use selector order. Compare the verified stored identities
		// without loading and validating every segment a second time.
		for i, segment := range expected {
			if stored[i].ID != segment.ID {
				return integrity("activate local item", "representation segments differ from input")
			}
		}
		previous, err := getLocalItem(ctx, conn, sourceID, itemID)
		switch {
		case IsCode(err, CodeNotFound):
			action = "added"
		case err != nil:
			return err
		case previous.Active && previous.RepresentationID == representationID:
			action = "unchanged"
			return nil
		case previous.Active:
			action = "updated"
		case !previous.Active:
			action = "restored"
		}
		if previous.Active {
			if err := removeRepresentationIndex(ctx, conn, sourceID, previous.RepresentationID); err != nil {
				return err
			}
		}
		if len(expected) > 0 {
			scope, err := ensureLexicalScope(ctx, conn, []mousa.SourceID{sourceID})
			if err != nil {
				return err
			}
			for _, segment := range expected {
				selector, _ := segment.Selector.TextByteRange()
				if err := putLexicalRow(ctx, conn, scope, segment, content[selector.Start:selector.End]); err != nil {
					return err
				}
			}
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO local_items(source_id, item_id, representation_id, active) VALUES(?, ?, ?, 1)
			ON CONFLICT(source_id, item_id) DO UPDATE SET representation_id = excluded.representation_id, active = 1`, sourceID[:], itemID, representationID[:])
		if err != nil {
			return classify("store local item activation", err)
		}
		var prior *mousa.RepresentationID
		if previous.Active {
			prior = &previous.RepresentationID
		}
		return appendItemChange(ctx, conn, sourceID, itemID, prior, &representationID)
	})
	if err != nil {
		return "", err
	}
	return action, nil
}

// DeleteLocalItem removes current search evidence without deleting canonical history.
func (store *Store) DeleteLocalItem(ctx context.Context, sourceID mousa.SourceID, itemID string) (string, error) {
	var action string
	err := store.writeImmediate(ctx, "delete local item", func(conn *sql.Conn) error {
		if !store.noteWrite {
			if err := requireDocumentSource(ctx, conn, sourceID); err != nil {
				return err
			}
		}
		item, err := getLocalItem(ctx, conn, sourceID, itemID)
		if IsCode(err, CodeNotFound) {
			action = "absent"
			return nil
		}
		if err != nil {
			return err
		}
		if !item.Active {
			action = "absent"
			return nil
		}
		if err := removeRepresentationIndex(ctx, conn, sourceID, item.RepresentationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE local_items SET active = 0 WHERE source_id = ? AND item_id = ?`, sourceID[:], itemID); err != nil {
			return classify("deactivate local item", err)
		}
		action = "deleted"
		return appendItemChange(ctx, conn, sourceID, itemID, &item.RepresentationID, nil)
	})
	if err != nil {
		return "", err
	}
	return action, nil
}

func removeRepresentationIndex(ctx context.Context, conn *sql.Conn, sourceID mousa.SourceID, id mousa.RepresentationID) error {
	const rows = `SELECT lr.rowid FROM segment_lexical_rows AS lr JOIN segments AS s ON s.id = lr.segment_id WHERE s.representation_id = ?`
	scope, err := getLexicalScope(ctx, conn, sourceID[:])
	if err == nil {
		if _, err := conn.ExecContext(ctx, `DELETE FROM `+scope.table()+` WHERE rowid IN (`+rows+`)`, id[:]); err != nil {
			return classify("remove scoped lexical rows", err)
		}
	} else if !IsCode(err, CodeNotFound) {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM segment_lexical_fts WHERE rowid IN (`+rows+`)`, id[:]); err != nil {
		return lexicalQueryError(ctx, err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM segment_lexical_rows WHERE rowid IN (`+rows+`)`, id[:]); err != nil {
		return classify("remove representation index", err)
	}
	return nil
}

// LocalItemIDs returns active item identities, not historical observation identities.
func (store *Store) LocalItemIDs(ctx context.Context, sourceID mousa.SourceID) ([]string, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT item_id FROM local_items WHERE source_id = ? AND active = 1 ORDER BY item_id`, sourceID[:])
	if err != nil {
		return nil, classify("list local items", err)
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var item string
		if err := rows.Scan(&item); err != nil {
			return nil, classify("scan local item", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, classify("list local items", err)
	}
	return items, nil
}

func verifyLocalItemRecords(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return startupError("begin local item verification", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT source_id, item_id FROM local_items ORDER BY source_id, item_id`)
	if err != nil {
		return classify("verify local items", err)
	}
	type key struct {
		source mousa.SourceID
		item   string
	}
	var keys []key
	for rows.Next() {
		var raw []byte
		var item string
		if err := rows.Scan(&raw, &item); err != nil {
			rows.Close()
			return classify("scan local items", err)
		}
		if len(raw) != len(mousa.SourceID{}) || item == "" || !utf8.ValidString(item) {
			rows.Close()
			return integrity("verify local items", "invalid item identity")
		}
		k := key{item: item}
		copy(k.source[:], raw)
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return classify("verify local items", err)
	}
	if err := rows.Close(); err != nil {
		return classify("finish local items scan", err)
	}
	for _, k := range keys {
		item, err := getLocalItem(ctx, tx, k.source, k.item)
		if err != nil {
			return err
		}
		if item.SegmentationPolicy == mousa.TextSegmentNoteV1 {
			var hasNote bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM notes WHERE source_id = ? AND item_id = ?)`, k.source[:], k.item).Scan(&hasNote); err != nil {
				return classify("verify note item", err)
			}
			if !hasNote {
				return integrity("verify local items", "note-policy item has no note record")
			}
		}
		var total, indexed int
		err = tx.QueryRowContext(ctx, `SELECT count(*), count(lr.segment_id) FROM segments AS s
			LEFT JOIN segment_lexical_rows AS lr ON lr.segment_id = s.id WHERE s.representation_id = ?`, item.RepresentationID[:]).Scan(&total, &indexed)
		if err != nil {
			return classify("verify local item index", err)
		}
		if item.Active && indexed != total || !item.Active && indexed != 0 {
			return integrity("verify local item index", "current activation disagrees with index")
		}
	}
	var orphan bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM segment_lexical_rows AS lr
		JOIN segments AS s ON s.id = lr.segment_id
		JOIN representation_inputs AS ri ON ri.representation_id = s.representation_id
		JOIN artifacts AS a ON a.id = ri.artifact_id
		JOIN observations AS o ON o.id = a.observation_id
		JOIN sources AS src ON src.id = o.source_id
		WHERE json_extract(CAST(src.record_json AS TEXT), '$.namespace') IN ('mousa-local', 'mousa-jsonl')
		AND NOT EXISTS (SELECT 1 FROM local_items AS li WHERE li.source_id = src.id
			AND li.representation_id = s.representation_id AND li.active = 1)
	)`).Scan(&orphan)
	if err != nil {
		return classify("verify managed lexical rows", err)
	}
	if orphan {
		return integrity("verify managed lexical rows", "indexed local revision is not active")
	}
	if err := tx.Commit(); err != nil {
		return startupError("finish local item verification", err)
	}
	return nil
}

// LocalSourceCounts distinguishes active items from immutable observations.
func (store *Store) LocalSourceCounts(ctx context.Context, sourceID mousa.SourceID) (int, int, error) {
	var active, observations int
	err := store.db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM local_items WHERE source_id = ? AND active = 1),
		(SELECT count(*) FROM observations WHERE source_id = ?)`, sourceID[:], sourceID[:]).Scan(&active, &observations)
	if err != nil {
		return 0, 0, classify("count local source records", err)
	}
	return active, observations, nil
}

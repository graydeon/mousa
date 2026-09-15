package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/graydeon/mousa/internal/mousa"
)

// LocalItem records current activation separately from immutable revision history.
// Inactive items retain their last representation so a restore is not an addition.
type LocalItem struct {
	RepresentationID mousa.RepresentationID
	Artifact         mousa.Artifact
	Active           bool
}

// GetLocalItem reads the indexed current-item relationship and its verified ancestry.
func (store *Store) GetLocalItem(ctx context.Context, sourceID mousa.SourceID, itemID string) (LocalItem, error) {
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
	if _, err := getRepresentation(ctx, q, item.RepresentationID); err != nil {
		return LocalItem{}, err
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
		representation, err := getRepresentation(ctx, conn, representationID)
		if err != nil {
			return err
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
			if err := removeRepresentationIndex(ctx, conn, previous.RepresentationID); err != nil {
				return err
			}
		}
		for _, segment := range expected {
			selector, _ := segment.Selector.TextByteRange()
			if err := putLexicalRow(ctx, conn, segment, content[selector.Start:selector.End]); err != nil {
				return err
			}
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO local_items(source_id, item_id, representation_id, active) VALUES(?, ?, ?, 1)
			ON CONFLICT(source_id, item_id) DO UPDATE SET representation_id = excluded.representation_id, active = 1`, sourceID[:], itemID, representationID[:])
		return classify("store local item activation", err)
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
		if err := removeRepresentationIndex(ctx, conn, item.RepresentationID); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `UPDATE local_items SET active = 0 WHERE source_id = ? AND item_id = ?`, sourceID[:], itemID); err != nil {
			return classify("deactivate local item", err)
		}
		action = "deleted"
		return nil
	})
	if err != nil {
		return "", err
	}
	return action, nil
}

func removeRepresentationIndex(ctx context.Context, conn *sql.Conn, id mousa.RepresentationID) error {
	const rows = `SELECT lr.rowid FROM segment_lexical_rows AS lr JOIN segments AS s ON s.id = lr.segment_id WHERE s.representation_id = ?`
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

// LocalSourceNeedsRecovery reports whether a legacy directory needs a complete sync.
func (store *Store) LocalSourceNeedsRecovery(ctx context.Context, sourceID mousa.SourceID) (bool, error) {
	var required bool
	err := store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM local_recovery_sources WHERE source_id = ?)`, sourceID[:]).Scan(&required)
	return required, classify("read local recovery state", err)
}

// CompleteLocalRecovery makes a legacy directory queryable after a successful full sync.
func (store *Store) CompleteLocalRecovery(ctx context.Context, sourceID mousa.SourceID) error {
	return store.writeImmediate(ctx, "complete local recovery", func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `DELETE FROM local_recovery_sources WHERE source_id = ?`, sourceID[:])
		return classify("complete local recovery", err)
	})
}

func verifyLocalItemRecords(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT source_id, item_id FROM local_items ORDER BY source_id, item_id`)
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
		item, err := getLocalItem(ctx, db, k.source, k.item)
		if err != nil {
			return err
		}
		var total, indexed int
		err = db.QueryRowContext(ctx, `SELECT count(*), count(lr.segment_id) FROM segments AS s
			LEFT JOIN segment_lexical_rows AS lr ON lr.segment_id = s.id WHERE s.representation_id = ?`, item.RepresentationID[:]).Scan(&total, &indexed)
		if err != nil {
			return classify("verify local item index", err)
		}
		if item.Active && indexed != total || !item.Active && indexed != 0 {
			return integrity("verify local item index", "current activation disagrees with index")
		}
	}
	var orphan bool
	err = db.QueryRowContext(ctx, `SELECT EXISTS(
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

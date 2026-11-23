package sqlite

import (
	"bytes"
	"context"
	"database/sql"

	"github.com/graydeon/mousa/internal/mousa"
)

const logIndexSQL = `SELECT coalesce(max(seq), 0) FROM log`

func appendItemChange(ctx context.Context, conn *sql.Conn, source mousa.SourceID, item string, previous, current *mousa.RepresentationID) error {
	var tail uint64
	if err := conn.QueryRowContext(ctx, logIndexSQL).Scan(&tail); err != nil {
		return classify("read item change index", err)
	}
	change, err := mousa.NewItemChange(source, item, tail+1, previous, current)
	if err != nil {
		return wrap(CodeInvalidRecord, "make item change", err)
	}
	data, err := mousa.EncodeItemChange(change)
	if err != nil {
		return wrap(CodeInvalidRecord, "encode item change", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO item_changes(id, source_id, record_json) VALUES(?, ?, ?)`,
		change.ID[:], source[:], data); err != nil {
		return classify("write item change", err)
	}
	if err := appendLog(ctx, conn, mousa.LogKindItemChange, change.ID[:], source[:], data); err != nil {
		return err
	}
	var seq uint64
	var storedSource, recordSource, hash, stored []byte
	err = conn.QueryRowContext(ctx, `
		SELECT l.seq, l.source_id, i.source_id, l.leaf_hash, i.record_json
		FROM log l JOIN item_changes i ON i.id = l.record_id
		WHERE l.kind = 'item_change' AND i.id = ?`, change.ID[:]).
		Scan(&seq, &storedSource, &recordSource, &hash, &stored)
	if err != nil {
		return lexicalStructuralError("read back item change", readError("read back item change", err))
	}
	if seq != change.Seq || !bytes.Equal(storedSource, source[:]) || !bytes.Equal(stored, data) {
		return integrity("read back item change", "item change projection disagrees with write")
	}
	if err := checkLeaf(mousa.LogKindItemChange, storedSource, hash, stored, recordSource); err != nil {
		return wrap(CodeIntegrity, "read back item change", err)
	}
	return nil
}

func readItemChange(seq uint64, recordID, sourceID, record []byte) (mousa.ItemChange, error) {
	change, err := mousa.DecodeItemChange(record)
	if err != nil {
		return mousa.ItemChange{}, wrap(CodeIntegrity, "decode item change", err)
	}
	if change.Seq != seq || !bytes.Equal(change.ID[:], recordID) || !bytes.Equal(change.SourceID[:], sourceID) {
		return mousa.ItemChange{}, integrity("read item change", "record disagrees with log index, ID, or source")
	}
	return change, nil
}

type localItemKey struct {
	source mousa.SourceID
	item   string
}

type localItemHead struct {
	representation mousa.RepresentationID
	active         bool
}

func applyVerifiedItemChange(heads map[localItemKey]localItemHead, change mousa.ItemChange) error {
	key := localItemKey{change.SourceID, change.ItemID}
	head := heads[key]
	if (change.Previous != nil) != head.active ||
		(change.Previous != nil && *change.Previous != head.representation) {
		return integrity("verify item change", "previous representation disagrees with log history")
	}
	head.active = change.Current != nil
	if head.active {
		head.representation = *change.Current
	}
	heads[key] = head
	return nil
}

func verifyItemChangeHeads(ctx context.Context, q queryer, heads map[localItemKey]localItemHead) error {
	rows, err := q.QueryContext(ctx, `SELECT source_id, item_id, representation_id, active FROM local_items`)
	if err != nil {
		return classify("scan item change heads", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source, representation []byte
		var item string
		var active bool
		if err := rows.Scan(&source, &item, &representation, &active); err != nil {
			return classify("scan item change head", err)
		}
		if len(source) != len(mousa.SourceID{}) || len(representation) != len(mousa.RepresentationID{}) {
			return integrity("verify item change heads", "invalid item identity projection")
		}
		key := localItemKey{mousa.SourceID(source), item}
		want := localItemHead{mousa.RepresentationID(representation), active}
		if head, exists := heads[key]; !exists || head != want {
			return integrity("verify item change heads", "local item disagrees with log history")
		}
		delete(heads, key)
	}
	if err := rows.Err(); err != nil {
		return classify("scan item change heads", err)
	}
	if len(heads) != 0 {
		return integrity("verify item change heads", "log history names missing local items")
	}
	return nil
}

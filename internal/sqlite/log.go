package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"

	"github.com/graydeon/mousa/internal/mousa"
)

// appendLog appends one leaf for a record written in the same transaction. The sequence is one
// past the current maximum, so it has no gaps while every write goes through here.
func appendLog(ctx context.Context, conn *sql.Conn, kind string, recordID, sourceID, record []byte) error {
	leaf := mousa.LogLeafHash(record)
	result, err := conn.ExecContext(ctx, `INSERT INTO log(seq, kind, record_id, source_id, leaf_hash) SELECT coalesce(max(seq), 0) + 1, ?, ?, ?, ? FROM log`, kind, recordID, sourceID, leaf[:])
	if err != nil {
		if sqliteConstraint(err) {
			return integrity("append log", "record already has a log leaf")
		}
		return classify("append log", err)
	}
	return oneRow(result)
}

// logRecordSQL selects each leaf with the stored bytes and source of the record it names.
const logRecordSQL = `SELECT l.seq, l.kind, l.record_id, l.source_id, l.leaf_hash,
  coalesce(r.record_json, a.record_json, w.record_json, n.record_json),
  coalesce(r.source_id, a.source_id, w.source_id, n.source_id)
FROM log l
LEFT JOIN ingest_receipts r ON l.kind = 'ingest_receipt' AND r.observation_id = l.record_id
LEFT JOIN access_events a ON l.kind = 'access_event' AND a.id = l.record_id
LEFT JOIN source_withdrawals w ON l.kind = 'source_withdrawal' AND w.id = l.record_id
LEFT JOIN notes n ON l.kind = 'note' AND n.id = l.record_id`

func checkLeaf(kind string, sourceID, leaf, record, recordSource []byte) error {
	if record == nil {
		return fmt.Errorf("%s leaf names a missing record", kind)
	}
	if !bytes.Equal(sourceID, recordSource) {
		return fmt.Errorf("%s leaf source disagrees with its record", kind)
	}
	want := mousa.LogLeafHash(record)
	if !bytes.Equal(leaf, want[:]) {
		return fmt.Errorf("%s leaf hash disagrees with its record", kind)
	}
	return nil
}

// verifyLog checks that the sequence is 1..n with no gaps, that every leaf hash matches the
// record it names, and that every receipt, access event and withdrawal has exactly one leaf.
func verifyLog(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return startupError("begin log verification", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, logRecordSQL+` ORDER BY l.seq`)
	if err != nil {
		return startupError("scan log", err)
	}
	var want int64 = 1
	for rows.Next() {
		var seq int64
		var kind string
		var recordID, sourceID, leaf, record, recordSource []byte
		if err := rows.Scan(&seq, &kind, &recordID, &sourceID, &leaf, &record, &recordSource); err != nil {
			rows.Close()
			return startupError("scan log", err)
		}
		if seq != want {
			rows.Close()
			return integrity("verify log", fmt.Sprintf("sequence has a gap: found %d, want %d", seq, want))
		}
		if err := checkLeaf(kind, sourceID, leaf, record, recordSource); err != nil {
			rows.Close()
			return integrity("verify log", fmt.Sprintf("seq %d: %v", seq, err))
		}
		want++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return startupError("scan log", err)
	}
	if err := rows.Close(); err != nil {
		return startupError("scan log", err)
	}
	var records int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM ingest_receipts) + (SELECT count(*) FROM access_events) + (SELECT count(*) FROM source_withdrawals) + (SELECT count(*) FROM notes)`).Scan(&records); err != nil {
		return startupError("count logged records", err)
	}
	if records != want-1 {
		return integrity("verify log", fmt.Sprintf("%d records but %d log leaves", records, want-1))
	}
	if err := verifyAccessEvents(ctx, tx); err != nil {
		return err
	}
	if err := verifyNoteRows(ctx, tx); err != nil {
		return err
	}
	if _, err := getStoreID(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func newStoreID() ([]byte, error) {
	id := make([]byte, len(mousa.StoreID{}))
	_, err := rand.Read(id)
	return id, err
}

func getStoreID(ctx context.Context, q queryRower) (mousa.StoreID, error) {
	var count int
	var raw []byte
	if err := q.QueryRowContext(ctx, `SELECT count(*), max(id) FROM store_identity`).Scan(&count, &raw); err != nil {
		return mousa.StoreID{}, classify("read store ID", err)
	}
	var id mousa.StoreID
	if count != 1 || len(raw) != len(id) {
		return mousa.StoreID{}, integrity("read store ID", "store must have exactly one 16-byte ID")
	}
	copy(id[:], raw)
	return id, nil
}

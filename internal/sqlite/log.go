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
	result, err := conn.ExecContext(ctx, `INSERT INTO log(seq, kind, record_id, source_id, leaf_hash) SELECT coalesce(max(seq), 0) + 1, ?, ?, ?, ? FROM log`, kind, recordID, nullableBytes(sourceID), leaf[:])
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
  CASE l.kind
    WHEN 'request_admission' THEN (SELECT record_json FROM request_admissions WHERE id = l.record_id)
    WHEN 'request_outcome' THEN (SELECT record_json FROM request_outcomes WHERE id = l.record_id)
    ELSE coalesce(r.record_json, a.record_json, i.record_json, w.record_json, n.record_json, t.record_json, p.record_json, h.record_json)
  END,
  coalesce(r.source_id, a.source_id, i.source_id, w.source_id, n.source_id, p.source_id, h.source_id)
FROM log l
LEFT JOIN ingest_receipts r ON l.kind = 'ingest_receipt' AND r.observation_id = l.record_id
LEFT JOIN access_events a ON l.kind = 'access_event' AND a.id = l.record_id
LEFT JOIN item_changes i ON l.kind = 'item_change' AND i.id = l.record_id
LEFT JOIN source_withdrawals w ON l.kind = 'source_withdrawal' AND w.id = l.record_id
LEFT JOIN notes n ON l.kind = 'note' AND n.id = l.record_id
LEFT JOIN trails t ON l.kind = 'trail' AND t.id = l.record_id
LEFT JOIN path_moves p ON l.kind = 'path_move' AND p.id = l.record_id
LEFT JOIN source_history_events h ON l.kind = 'source_history' AND h.id = l.record_id AND h.seq = l.seq`

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

// verifyLog checks sequence, leaf identity, ordered access history and coverage
// separately for every logged record kind.
func verifyLog(ctx context.Context, db *sql.DB, histories pathHistories) (map[string]int64, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, startupError("begin log verification", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, logRecordSQL+` ORDER BY l.seq`)
	if err != nil {
		return nil, startupError("scan log", err)
	}
	defer rows.Close()
	counts := make(map[string]int64)
	accessTimes := make(map[mousa.SourceID]int64)
	itemHeads := make(map[localItemKey]localItemHead)
	var lastChange mousa.ItemChange
	var want int64 = 1
	for rows.Next() {
		var seq int64
		var kind string
		var recordID, sourceID, leaf, record, recordSource []byte
		if err := rows.Scan(&seq, &kind, &recordID, &sourceID, &leaf, &record, &recordSource); err != nil {
			return nil, startupError("scan log", err)
		}
		if seq != want {
			return nil, integrity("verify log", fmt.Sprintf("sequence has a gap: found %d, want %d", seq, want))
		}
		if err := checkLeaf(kind, sourceID, leaf, record, recordSource); err != nil {
			return nil, integrity("verify log", fmt.Sprintf("seq %d: %v", seq, err))
		}
		if kind == mousa.LogKindAccessEvent {
			if len(recordID) != len(mousa.AccessEventID{}) {
				return nil, integrity("verify log", "invalid access event ID")
			}
			event, _, err := getAccessEvent(ctx, tx, mousa.AccessEventID(recordID))
			if err != nil {
				return nil, err
			}
			if event.OccurredAtUsec <= accessTimes[event.SourceID] {
				return nil, integrity("verify log", fmt.Sprintf("seq %d: access event times do not increase", seq))
			}
			accessTimes[event.SourceID] = event.OccurredAtUsec
		}
		if kind == mousa.LogKindItemChange {
			change, err := readItemChange(uint64(seq), recordID, sourceID, record)
			if err != nil {
				return nil, err
			}
			if err := applyVerifiedItemChange(itemHeads, change); err != nil {
				return nil, err
			}
			if err := histories.applyItemChange(change); err != nil {
				return nil, err
			}
			lastChange = change
		}
		if kind == mousa.LogKindPathMove {
			move, err := decodePathMoveLeaf(uint64(seq), kind, recordID, sourceID, leaf, record, recordSource)
			if err != nil {
				return nil, integrity("verify log", fmt.Sprintf("seq %d: %v", seq, err))
			}
			if err := histories.applyPathMove(move); err != nil {
				return nil, integrity("verify log", fmt.Sprintf("seq %d: %v", seq, err))
			}
		}
		if kind == logKindSourceHistory {
			if _, err := decodeSourceHistoryEvent(uint64(seq), recordID, recordSource, record); err != nil {
				return nil, integrity("verify log", fmt.Sprintf("seq %d: %v", seq, err))
			}
		}
		if kind == mousa.LogKindRequestAdmission {
			admission, err := mousa.DecodeRequestAdmission(record)
			if err != nil {
				return nil, integrity("verify log", fmt.Sprintf("seq %d: %v", seq, err))
			}
			if !bytes.Equal(requestRecordID(admission.RequestID), recordID) {
				return nil, integrity("verify log", fmt.Sprintf("seq %d: admission leaf id disagrees with its request", seq))
			}
		}
		if kind == mousa.LogKindRequestOutcome {
			outcome, err := mousa.DecodeRequestOutcome(record)
			if err != nil {
				return nil, integrity("verify log", fmt.Sprintf("seq %d: %v", seq, err))
			}
			if outcome.Seq != uint64(seq) || !bytes.Equal(requestRecordID(outcome.RequestID), recordID) {
				return nil, integrity("verify log", fmt.Sprintf("seq %d: outcome leaf disagrees with its request or sequence", seq))
			}
			admission, err := checkRequestOutcomeAdmission(ctx, tx, outcome)
			if err != nil {
				return nil, integrity("verify log", fmt.Sprintf("seq %d: %v", seq, err))
			}
			if _, err := requestEvidenceSummary(ctx, tx, admission, outcome, seq); err != nil {
				return nil, err
			}
			if legacyActivationTime(lastChange, admission, outcome) != 0 {
				key := localItemKey{lastChange.SourceID, lastChange.ItemID}
				head := itemHeads[key]
				head.activationSeq = lastChange.Seq
				itemHeads[key] = head
			}
		}
		counts[kind]++
		want++
	}
	if err := rows.Err(); err != nil {
		return nil, startupError("scan log", err)
	}
	if err := rows.Close(); err != nil {
		return nil, startupError("scan log", err)
	}
	expected, err := verificationCounts(ctx, tx, logCountsSQL)
	if err != nil {
		return nil, err
	}
	for kind, total := range expected {
		if total != counts[kind] {
			return nil, integrity("verify log", fmt.Sprintf("%d %s records but %d log leaves", total, kind, counts[kind]))
		}
	}
	if err := verifyNoteRows(ctx, tx); err != nil {
		return nil, err
	}
	if err := verifyItemChangeHeads(ctx, tx, itemHeads); err != nil {
		return nil, err
	}
	if err := verifyPathHeads(ctx, tx, histories); err != nil {
		return nil, err
	}
	if _, err := getStoreID(ctx, tx); err != nil {
		return nil, err
	}
	return expected, classify("finish log verification", tx.Commit())
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

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

const logIndexSQL = `SELECT coalesce(max(seq), 0) FROM log`

func appendItemChange(ctx context.Context, conn *sql.Conn, source mousa.SourceID, item string, previous, current *mousa.RepresentationID, creationPath string) error {
	var tail uint64
	if err := conn.QueryRowContext(ctx, logIndexSQL).Scan(&tail); err != nil {
		return classify("read item change index", err)
	}
	var activatedAtUsec int64
	if current != nil {
		activatedAtUsec = time.Now().UnixMicro()
	}
	change, err := mousa.NewItemChange(source, item, tail+1, previous, current, activatedAtUsec, creationPath)
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
	if current != nil {
		result, err := conn.ExecContext(ctx, `UPDATE local_items SET activation_seq = ? WHERE source_id = ? AND item_id = ?`, change.Seq, source[:], item)
		if err != nil {
			return classify("project item activation", err)
		}
		if err := oneRow(result); err != nil {
			return integrity("project item activation", "item change projection disagrees with local item")
		}
	}
	return nil
}

// legacyActivationTime derives a schema-9 activation clock only from the
// committed document outcome that directly follows and names its item change.
func legacyActivationTime(change mousa.ItemChange, admission mousa.RequestAdmission, outcome mousa.RequestOutcome) int64 {
	if change.Schema == mousa.ItemChangeSchemaV1 && change.Current != nil &&
		outcome.Seq == change.Seq+1 && outcome.State == mousa.RequestStateCommitted &&
		outcome.Evidence.ItemChangeID != nil && *outcome.Evidence.ItemChangeID == change.ID &&
		admission.Operation == mousa.RequestOperationDocument && outcome.FinishedAtUsec > admission.ReceivedAtUsec {
		return outcome.FinishedAtUsec
	}
	return 0
}

// readItemActivationTime verifies the activation leaf named by the projection
// and returns its clock. Schema-9 records carry no clock, so their only valid
// source is the paired served document outcome that proves activation.
func readItemActivationTime(ctx context.Context, q queryer, seq uint64, source mousa.SourceID, item string, repr mousa.RepresentationID) (int64, error) {
	var leafSeq int64
	var kind string
	var recordID, leafSource, leaf, record, recordSource []byte
	err := q.QueryRowContext(ctx, logRecordSQL+` WHERE l.seq = ? AND l.kind = 'item_change'`, seq).
		Scan(&leafSeq, &kind, &recordID, &leafSource, &leaf, &record, &recordSource)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, integrity("read item activation", "item change leaf is missing")
	}
	if err != nil {
		return 0, classify("read item activation", err)
	}
	if err := checkLeaf(kind, leafSource, leaf, record, recordSource); err != nil {
		return 0, integrity("read item activation", err.Error())
	}
	change, err := readItemChange(uint64(leafSeq), recordID, leafSource, record)
	if err != nil {
		return 0, err
	}
	if change.SourceID != source || change.ItemID != item || change.Current == nil || *change.Current != repr {
		return 0, integrity("read item activation", "item change does not activate the released revision")
	}
	if change.Schema != mousa.ItemChangeSchemaV1 {
		return change.ActivatedAtUsec, nil
	}
	var outcomeRecordID, outcomeSource, outcomeLeaf, outcomeRecord []byte
	err = q.QueryRowContext(ctx, `SELECT l.record_id, l.source_id, l.leaf_hash, o.record_json
		FROM log l LEFT JOIN request_outcomes o ON l.record_id = o.id
		WHERE l.seq = ? AND l.kind = 'request_outcome'`, seq+1).
		Scan(&outcomeRecordID, &outcomeSource, &outcomeLeaf, &outcomeRecord)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, integrity("read item activation", "legacy activation outcome leaf is missing")
	}
	if err != nil {
		return 0, classify("read legacy item activation", err)
	}
	entry, err := requestEntry(ctx, q, seq+1, outcomeRecordID, outcomeSource, outcomeLeaf, outcomeRecord)
	if err != nil {
		return 0, err
	}
	activated := legacyActivationTime(change, entry.Admission, entry.Outcome)
	if activated == 0 {
		return 0, integrity("read item activation", "legacy item change lacks activation proof")
	}
	return activated, nil
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
	activationSeq  uint64
	active         bool
}

func applyVerifiedItemChange(heads map[localItemKey]localItemHead, change mousa.ItemChange) error {
	key := localItemKey{change.SourceID, change.ItemID}
	head := heads[key]
	if (change.Previous != nil) != head.active ||
		(change.Previous != nil && *change.Previous != head.representation) {
		return integrity("verify item change", "previous representation disagrees with log history")
	}
	head.activationSeq = 0
	if change.ActivatedAtUsec != 0 {
		head.activationSeq = change.Seq
	}
	head.active = change.Current != nil
	if head.active {
		head.representation = *change.Current
	}
	heads[key] = head
	return nil
}

func verifyItemChangeHeads(ctx context.Context, q queryer, heads map[localItemKey]localItemHead) error {
	rows, err := q.QueryContext(ctx, `SELECT source_id, item_id, representation_id, active, coalesce(activation_seq, 0) FROM local_items`)
	if err != nil {
		return classify("scan item change heads", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source, representation []byte
		var item string
		var active bool
		var activationSeq uint64
		if err := rows.Scan(&source, &item, &representation, &active, &activationSeq); err != nil {
			return classify("scan item change head", err)
		}
		if len(source) != len(mousa.SourceID{}) || len(representation) != len(mousa.RepresentationID{}) {
			return integrity("verify item change heads", "invalid item identity projection")
		}
		key := localItemKey{mousa.SourceID(source), item}
		want := localItemHead{representation: mousa.RepresentationID(representation), activationSeq: activationSeq, active: active}
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

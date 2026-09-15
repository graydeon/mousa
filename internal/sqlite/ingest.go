package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"
	"reflect"

	"github.com/graydeon/mousa/internal/mousa"
)

// ApplyIngest stores one accepted delivery and advances its source projection atomically.
func (store *Store) ApplyIngest(ctx context.Context, batch mousa.IngestBatch) error {
	if err := store.requireWritable("apply ingest"); err != nil {
		return err
	}
	if batch.Checkpoint != nil && (len(batch.Checkpoint.Expected) > 4096 || len(batch.Checkpoint.Next) > 4096) {
		return wrap(CodeResourceLimit, "apply ingest", errors.New("checkpoint exceeds storage limit"))
	}
	receipt, err := batch.Receipt()
	if err != nil {
		return wrap(CodeInvalidRecord, "apply ingest", err)
	}
	receiptData, err := mousa.EncodeIngestReceipt(receipt)
	if err != nil {
		return wrap(CodeInvalidRecord, "apply ingest", err)
	}
	if err := checkRecordSize(receiptData); err != nil {
		return err
	}
	sourceData, err := mousa.EncodeSource(batch.Source)
	if err != nil {
		return wrap(CodeInvalidRecord, "apply ingest", err)
	}
	if err := checkRecordSize(sourceData); err != nil {
		return err
	}
	observationData, err := mousa.EncodeObservation(batch.Observation)
	if err != nil {
		return wrap(CodeInvalidRecord, "apply ingest", err)
	}
	if err := checkRecordSize(observationData); err != nil {
		return err
	}
	artifactData := make([][]byte, len(batch.Artifacts))
	for index, artifact := range batch.Artifacts {
		artifactData[index], err = mousa.EncodeArtifact(artifact)
		if err != nil {
			return wrap(CodeInvalidRecord, "apply ingest", err)
		}
		if err := checkRecordSize(artifactData[index]); err != nil {
			return err
		}
	}
	return store.writeImmediate(ctx, "apply ingest", func(conn *sql.Conn) error {
		var existing []byte
		err := conn.QueryRowContext(ctx, `SELECT record_json FROM ingest_receipts WHERE observation_id = ?`, receipt.ObservationID[:]).Scan(&existing)
		if err == nil {
			if !bytes.Equal(existing, receiptData) {
				return wrap(CodeConflict, "apply ingest", errors.New("observation receipt conflicts with accepted delivery"))
			}
			stored, err := getIngestReceipt(ctx, conn, receipt.ObservationID)
			if err != nil {
				return err
			}
			if difference := receiptDifference(stored, receipt); difference != "" {
				return integrity("apply ingest replay", "receipt read-back disagrees")
			}
			storedSource, err := getSource(ctx, conn, batch.Source.ID)
			if err != nil {
				return err
			}
			storedObservation, err := getObservation(ctx, conn, batch.Observation.ID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(storedSource, batch.Source) || !reflect.DeepEqual(storedObservation, batch.Observation) {
				return wrap(CodeConflict, "apply ingest replay", errors.New("canonical source or observation facts conflict"))
			}
			for _, artifact := range batch.Artifacts {
				storedArtifact, err := getArtifact(ctx, conn, artifact.ID)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(storedArtifact, artifact) {
					return wrap(CodeConflict, "apply ingest replay", errors.New("canonical artifact facts conflict"))
				}
			}
			if _, err := getIngestState(ctx, conn, receipt.SourceID); err != nil {
				return integrity("apply ingest replay", "source state is missing or damaged")
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return classify("apply ingest", err)
		}

		state, stateErr := getIngestState(ctx, conn, receipt.SourceID)
		if stateErr != nil && !IsCode(stateErr, CodeNotFound) {
			return stateErr
		}
		if IsCode(stateErr, CodeNotFound) {
			state = mousa.IngestState{SourceID: receipt.SourceID, CollectionState: mousa.CollectionActive}
		}
		if state.CollectionState == mousa.CollectionWithdrawn {
			if receipt.ResumeWithdrawalID == nil || state.CurrentWithdrawalID == nil || *receipt.ResumeWithdrawalID != *state.CurrentWithdrawalID {
				return wrap(CodeConflict, "apply ingest", errors.New("current withdrawal must be resumed explicitly"))
			}
			state.CollectionState = mousa.CollectionActive
			state.CurrentWithdrawalID = nil
		} else if receipt.ResumeWithdrawalID != nil {
			return wrap(CodeConflict, "apply ingest", errors.New("resume does not match a current withdrawal"))
		}
		if err := advanceDeliveryState(receipt, &state); err != nil {
			return err
		}

		if err := putRoot(ctx, conn, "sources", batch.Source.ID[:], sourceData, func() error {
			stored, err := getSource(ctx, conn, batch.Source.ID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(stored, batch.Source) {
				return integrity("verify ingest source", "record disagrees")
			}
			return nil
		}); err != nil {
			return err
		}
		if err := putChild(ctx, conn, "observations", "source_id", batch.Observation.ID[:], batch.Observation.SourceID[:], observationData, func() error {
			stored, err := getObservation(ctx, conn, batch.Observation.ID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(stored, batch.Observation) {
				return integrity("verify ingest observation", "record disagrees")
			}
			return nil
		}); err != nil {
			return err
		}
		for index, artifact := range batch.Artifacts {
			artifact := artifact
			if err := putChild(ctx, conn, "artifacts", "observation_id", artifact.ID[:], artifact.ObservationID[:], artifactData[index], func() error {
				stored, err := getArtifact(ctx, conn, artifact.ID)
				if err != nil {
					return err
				}
				if !reflect.DeepEqual(stored, artifact) {
					return integrity("verify ingest artifact", "record disagrees")
				}
				return nil
			}); err != nil {
				return err
			}
		}

		var coverageStart, coverageEnd any
		if receipt.Coverage != nil {
			coverageStart, coverageEnd = receipt.Coverage.StartUsec, receipt.Coverage.EndUsec
		}
		result, err := conn.ExecContext(ctx, `INSERT INTO ingest_receipts(observation_id, source_id, initiative, form, captured_at_usec, expected_checkpoint, next_checkpoint, sequence, coverage_start_usec, coverage_end_usec, record_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, receipt.ObservationID[:], receipt.SourceID[:], receipt.Initiative, receipt.Form, receipt.CapturedAtUsec, nullableBytes(receipt.ExpectedCheckpoint), nullableBytes(receipt.NextCheckpoint), nullableUint64(receipt.Sequence), coverageStart, coverageEnd, receiptData)
		if err != nil {
			if sqliteConstraint(err) {
				return wrap(CodeConflict, "apply ingest", err)
			}
			return classify("apply ingest", err)
		}
		if err := oneRow(result); err != nil {
			return integrity("apply ingest", err.Error())
		}
		for ordinal, gap := range receipt.Gaps {
			if _, err := conn.ExecContext(ctx, `INSERT INTO ingest_gaps(observation_id, ordinal, gap_start, gap_end) VALUES(?, ?, ?, ?)`, receipt.ObservationID[:], ordinal, uint64Blob(gap.Start), uint64Blob(gap.End)); err != nil {
				return classify("apply ingest gap", err)
			}
		}
		state.LastObservationID = &receipt.ObservationID
		state.LastCapturedAtUsec = &receipt.CapturedAtUsec
		if err := putIngestState(ctx, conn, state); err != nil {
			return err
		}
		stored, err := getIngestReceipt(ctx, conn, receipt.ObservationID)
		if err != nil {
			return err
		}
		if difference := receiptDifference(stored, receipt); difference != "" {
			return integrity("apply ingest", difference)
		}
		storedState, err := getIngestState(ctx, conn, receipt.SourceID)
		if err != nil || !reflect.DeepEqual(storedState, state) {
			return integrity("apply ingest", "state read-back disagrees")
		}
		return nil
	})
}

func advanceDeliveryState(receipt mousa.IngestReceipt, state *mousa.IngestState) error {
	if receipt.Initiative == mousa.InitiativePull {
		if !bytes.Equal(state.Checkpoint, receipt.ExpectedCheckpoint) || (state.Checkpoint == nil) != (receipt.ExpectedCheckpoint == nil) {
			return wrap(CodeConflict, "apply ingest checkpoint", errors.New("expected checkpoint does not match current state"))
		}
		state.Checkpoint = append([]byte(nil), receipt.NextCheckpoint...)
		return nil
	}
	if receipt.Sequence == nil {
		if len(receipt.Gaps) != 0 {
			return wrap(CodeInvalidRecord, "apply ingest sequence", errors.New("gaps require sequence"))
		}
		return nil
	}
	sequence := *receipt.Sequence
	if state.HighSequence == nil {
		if len(receipt.Gaps) != 0 {
			return wrap(CodeInvalidRecord, "apply ingest sequence", errors.New("first sequence cannot declare a gap"))
		}
		state.HighSequence = &sequence
		return nil
	}
	high := *state.HighSequence
	if sequence <= high {
		if len(receipt.Gaps) != 0 {
			return wrap(CodeInvalidRecord, "apply ingest sequence", errors.New("late sequence cannot declare a gap"))
		}
		return nil
	}
	if high != math.MaxUint64 && sequence == high+1 {
		if len(receipt.Gaps) != 0 {
			return wrap(CodeInvalidRecord, "apply ingest sequence", errors.New("contiguous sequence cannot declare a gap"))
		}
	} else {
		if high == math.MaxUint64 || len(receipt.Gaps) != 1 || receipt.Gaps[0].Start != high+1 || receipt.Gaps[0].End != sequence {
			return wrap(CodeInvalidRecord, "apply ingest sequence", errors.New("forward sequence requires the exact gap"))
		}
	}
	state.HighSequence = &sequence
	return nil
}

// GetIngestState returns the current measured ingestion projection for a source.
func (store *Store) GetIngestState(ctx context.Context, sourceID mousa.SourceID) (mousa.IngestState, error) {
	return getIngestState(ctx, store.db, sourceID)
}

// GetIngestReceipt returns the accepted delivery receipt for one observation.
// An unknown observation is CodeNotFound. A caller that re-delivers a
// content-addressed observation reads this to reproduce the accepted delivery
// evidence instead of contradicting it with a fresh capture time.
func (store *Store) GetIngestReceipt(ctx context.Context, observationID mousa.ObservationID) (mousa.IngestReceipt, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return mousa.IngestReceipt{}, classify("begin ingest receipt read", err)
	}
	defer tx.Rollback()
	receipt, err := getIngestReceipt(ctx, tx, observationID)
	if err != nil {
		return mousa.IngestReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return mousa.IngestReceipt{}, classify("finish ingest receipt read", err)
	}
	return receipt, nil
}

func getIngestState(ctx context.Context, q queryer, sourceID mousa.SourceID) (mousa.IngestState, error) {
	var collection string
	var checkpoint, high, observation, withdrawal []byte
	var captured sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT collection_state, checkpoint, high_sequence, last_observation_id, current_withdrawal_id, last_captured_at_usec FROM source_ingest_state WHERE source_id = ?`, sourceID[:]).Scan(&collection, &checkpoint, &high, &observation, &withdrawal, &captured); err != nil {
		return mousa.IngestState{}, readError("get ingest state", err)
	}
	state := mousa.IngestState{SourceID: sourceID, CollectionState: mousa.CollectionState(collection), Checkpoint: append([]byte(nil), checkpoint...)}
	if len(high) != 0 {
		if len(high) != 8 {
			return mousa.IngestState{}, integrity("get ingest state", "invalid high sequence")
		}
		value := binary.BigEndian.Uint64(high)
		state.HighSequence = &value
	}
	if len(observation) != 0 {
		if len(observation) != 32 {
			return mousa.IngestState{}, integrity("get ingest state", "invalid observation ID")
		}
		var id mousa.ObservationID
		copy(id[:], observation)
		state.LastObservationID = &id
	}
	if len(withdrawal) != 0 {
		if len(withdrawal) != 32 {
			return mousa.IngestState{}, integrity("get ingest state", "invalid withdrawal ID")
		}
		var id mousa.WithdrawalID
		copy(id[:], withdrawal)
		state.CurrentWithdrawalID = &id
		stored, err := getSourceWithdrawal(ctx, q, id)
		if err != nil || stored.SourceID != sourceID {
			return mousa.IngestState{}, integrity("get ingest state", "current withdrawal is missing, damaged, or belongs to another source")
		}
	}
	if captured.Valid {
		value := captured.Int64
		state.LastCapturedAtUsec = &value
	}
	if state.LastObservationID != nil {
		receipt, err := getIngestReceipt(ctx, q, *state.LastObservationID)
		if err != nil {
			if IsCode(err, CodeNotFound) {
				return mousa.IngestState{}, integrity("get ingest state", "last observation has no canonical receipt")
			}
			return mousa.IngestState{}, err
		}
		if receipt.SourceID != sourceID || state.LastCapturedAtUsec == nil || *state.LastCapturedAtUsec != receipt.CapturedAtUsec {
			return mousa.IngestState{}, integrity("get ingest state", "last observation projection disagrees with receipt")
		}
	}
	if (state.CollectionState == mousa.CollectionActive) != (state.CurrentWithdrawalID == nil) {
		return mousa.IngestState{}, integrity("get ingest state", "collection projection disagrees")
	}
	return state, nil
}

func putIngestState(ctx context.Context, conn *sql.Conn, state mousa.IngestState) error {
	var observation, withdrawal any
	if state.LastObservationID != nil {
		observation = state.LastObservationID[:]
	}
	if state.CurrentWithdrawalID != nil {
		withdrawal = state.CurrentWithdrawalID[:]
	}
	var captured any
	if state.LastCapturedAtUsec != nil {
		captured = *state.LastCapturedAtUsec
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO source_ingest_state(source_id, collection_state, checkpoint, high_sequence, last_observation_id, current_withdrawal_id, last_captured_at_usec) VALUES(?, ?, ?, ?, ?, ?, ?) ON CONFLICT(source_id) DO UPDATE SET collection_state=excluded.collection_state, checkpoint=excluded.checkpoint, high_sequence=excluded.high_sequence, last_observation_id=excluded.last_observation_id, current_withdrawal_id=excluded.current_withdrawal_id, last_captured_at_usec=excluded.last_captured_at_usec`, state.SourceID[:], state.CollectionState, nullableBytes(state.Checkpoint), nullableUint64(state.HighSequence), observation, withdrawal, captured)
	if err != nil {
		return classify("put ingest state", err)
	}
	return nil
}

func getIngestReceipt(ctx context.Context, q queryer, observationID mousa.ObservationID) (mousa.IngestReceipt, error) {
	var sourceID, expected, next, sequence, data []byte
	var initiative, form string
	var captured int64
	var coverageStart, coverageEnd sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT source_id, initiative, form, captured_at_usec, expected_checkpoint, next_checkpoint, sequence, coverage_start_usec, coverage_end_usec, record_json FROM ingest_receipts WHERE observation_id = ?`, observationID[:]).Scan(&sourceID, &initiative, &form, &captured, &expected, &next, &sequence, &coverageStart, &coverageEnd, &data); err != nil {
		return mousa.IngestReceipt{}, readError("get ingest receipt", err)
	}
	receipt, err := decodeCanonical(data, mousa.DecodeIngestReceipt, mousa.EncodeIngestReceipt)
	if err != nil {
		return mousa.IngestReceipt{}, wrap(CodeIntegrity, "get ingest receipt", err)
	}
	if receipt.ObservationID != observationID || !bytes.Equal(sourceID, receipt.SourceID[:]) || initiative != string(receipt.Initiative) || form != string(receipt.Form) || captured != receipt.CapturedAtUsec || !equalNullableBytes(expected, receipt.ExpectedCheckpoint) || !equalNullableBytes(next, receipt.NextCheckpoint) || !equalNullableUint64(sequence, receipt.Sequence) {
		return mousa.IngestReceipt{}, integrity("get ingest receipt", "relational projection disagrees")
	}
	if (receipt.Coverage == nil) != (!coverageStart.Valid || !coverageEnd.Valid) || receipt.Coverage != nil && (receipt.Coverage.StartUsec != coverageStart.Int64 || receipt.Coverage.EndUsec != coverageEnd.Int64) {
		return mousa.IngestReceipt{}, integrity("get ingest receipt", "coverage projection disagrees")
	}
	rows, err := q.QueryContext(ctx, `SELECT ordinal, gap_start, gap_end FROM ingest_gaps WHERE observation_id = ? ORDER BY ordinal`, observationID[:])
	if err != nil {
		return mousa.IngestReceipt{}, classify("get ingest gaps", err)
	}
	defer rows.Close()
	for ordinal, gap := range receipt.Gaps {
		if !rows.Next() {
			return mousa.IngestReceipt{}, integrity("get ingest gaps", "missing gap")
		}
		var gotOrdinal int
		var start, end []byte
		if err := rows.Scan(&gotOrdinal, &start, &end); err != nil {
			return mousa.IngestReceipt{}, classify("get ingest gaps", err)
		}
		if gotOrdinal != ordinal || !bytes.Equal(start, uint64Blob(gap.Start)) || !bytes.Equal(end, uint64Blob(gap.End)) {
			return mousa.IngestReceipt{}, integrity("get ingest gaps", "gap projection disagrees")
		}
	}
	if rows.Next() {
		return mousa.IngestReceipt{}, integrity("get ingest gaps", "extra gap")
	}
	return receipt, nil
}

// WithdrawSource appends one source-withdrawal event and marks collection withdrawn.
func (store *Store) WithdrawSource(ctx context.Context, withdrawal mousa.SourceWithdrawal) error {
	if err := store.requireWritable("withdraw source"); err != nil {
		return err
	}
	data, err := mousa.EncodeSourceWithdrawal(withdrawal)
	if err != nil {
		return wrap(CodeInvalidRecord, "withdraw source", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	return store.writeImmediate(ctx, "withdraw source", func(conn *sql.Conn) error {
		if err := requireParent(ctx, conn, "sources", withdrawal.SourceID[:]); err != nil {
			return err
		}
		var existing []byte
		err := conn.QueryRowContext(ctx, `SELECT record_json FROM source_withdrawals WHERE id = ?`, withdrawal.ID[:]).Scan(&existing)
		if err == nil {
			if !bytes.Equal(existing, data) {
				return wrap(CodeConflict, "withdraw source", errors.New("withdrawal ID conflicts"))
			}
			stored, err := getSourceWithdrawal(ctx, conn, withdrawal.ID)
			if err != nil || !reflect.DeepEqual(stored, withdrawal) {
				return integrity("withdraw source", "withdrawal read-back disagrees")
			}
			state, err := getIngestState(ctx, conn, withdrawal.SourceID)
			if err != nil {
				return integrity("withdraw source", "source state is missing or damaged")
			}
			if state.CollectionState == mousa.CollectionWithdrawn && (state.CurrentWithdrawalID == nil || *state.CurrentWithdrawalID != withdrawal.ID) {
				return wrap(CodeConflict, "withdraw source", errors.New("historical withdrawal is not current"))
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return classify("withdraw source", err)
		}
		state, stateErr := getIngestState(ctx, conn, withdrawal.SourceID)
		if stateErr != nil && !IsCode(stateErr, CodeNotFound) {
			return stateErr
		}
		if IsCode(stateErr, CodeNotFound) {
			state = mousa.IngestState{SourceID: withdrawal.SourceID, CollectionState: mousa.CollectionActive}
		}
		if state.CollectionState == mousa.CollectionWithdrawn {
			return wrap(CodeConflict, "withdraw source", errors.New("source is already withdrawn"))
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO source_withdrawals(id, source_id, occurred_at_usec, record_json) VALUES(?, ?, ?, ?)`, withdrawal.ID[:], withdrawal.SourceID[:], withdrawal.OccurredAtUsec, data); err != nil {
			return classify("withdraw source", err)
		}
		state.CollectionState = mousa.CollectionWithdrawn
		state.CurrentWithdrawalID = &withdrawal.ID
		if err := putIngestState(ctx, conn, state); err != nil {
			return err
		}
		stored, err := getSourceWithdrawal(ctx, conn, withdrawal.ID)
		if err != nil || !reflect.DeepEqual(stored, withdrawal) {
			return integrity("withdraw source", "withdrawal read-back disagrees")
		}
		storedState, err := getIngestState(ctx, conn, withdrawal.SourceID)
		if err != nil || !reflect.DeepEqual(storedState, state) {
			return integrity("withdraw source", "state read-back disagrees")
		}
		return nil
	})
}

func getSourceWithdrawal(ctx context.Context, q queryRower, id mousa.WithdrawalID) (mousa.SourceWithdrawal, error) {
	var sourceID []byte
	var occurred int64
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT source_id, occurred_at_usec, record_json FROM source_withdrawals WHERE id = ?`, id[:]).Scan(&sourceID, &occurred, &data); err != nil {
		return mousa.SourceWithdrawal{}, readError("get source withdrawal", err)
	}
	withdrawal, err := decodeCanonical(data, mousa.DecodeSourceWithdrawal, mousa.EncodeSourceWithdrawal)
	if err != nil {
		return mousa.SourceWithdrawal{}, wrap(CodeIntegrity, "get source withdrawal", err)
	}
	if withdrawal.ID != id || !bytes.Equal(sourceID, withdrawal.SourceID[:]) || occurred != withdrawal.OccurredAtUsec {
		return mousa.SourceWithdrawal{}, integrity("get source withdrawal", "relational projection disagrees")
	}
	return withdrawal, nil
}

func verifyIngestRecords(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT observation_id FROM ingest_receipts ORDER BY observation_id`)
	if err != nil {
		return startupError("scan ingest receipts", err)
	}
	var observations [][]byte
	for rows.Next() {
		var id []byte
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return startupError("scan ingest receipts", err)
		}
		observations = append(observations, append([]byte(nil), id...))
	}
	if err := rows.Close(); err != nil {
		return startupError("scan ingest receipts", err)
	}
	for _, raw := range observations {
		if len(raw) != 32 {
			return integrity("scan ingest receipts", "invalid ID")
		}
		var id mousa.ObservationID
		copy(id[:], raw)
		receipt, err := getIngestReceipt(ctx, db, id)
		if err != nil {
			return err
		}
		if _, err := getIngestState(ctx, db, receipt.SourceID); err != nil {
			return integrity("scan ingest receipts", "receipt source state is missing or damaged")
		}
	}
	rows, err = db.QueryContext(ctx, `SELECT id FROM source_withdrawals ORDER BY id`)
	if err != nil {
		return startupError("scan source withdrawals", err)
	}
	var withdrawals [][]byte
	for rows.Next() {
		var id []byte
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return startupError("scan source withdrawals", err)
		}
		withdrawals = append(withdrawals, append([]byte(nil), id...))
	}
	if err := rows.Close(); err != nil {
		return startupError("scan source withdrawals", err)
	}
	for _, raw := range withdrawals {
		if len(raw) != 32 {
			return integrity("scan source withdrawals", "invalid ID")
		}
		var id mousa.WithdrawalID
		copy(id[:], raw)
		withdrawal, err := getSourceWithdrawal(ctx, db, id)
		if err != nil {
			return err
		}
		if _, err := getIngestState(ctx, db, withdrawal.SourceID); err != nil {
			return integrity("scan source withdrawals", "withdrawal source state is missing or damaged")
		}
	}
	rows, err = db.QueryContext(ctx, `SELECT source_id FROM source_ingest_state ORDER BY source_id`)
	if err != nil {
		return startupError("scan ingest state", err)
	}
	var sources [][]byte
	for rows.Next() {
		var id []byte
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return startupError("scan ingest state", err)
		}
		sources = append(sources, append([]byte(nil), id...))
	}
	if err := rows.Close(); err != nil {
		return startupError("scan ingest state", err)
	}
	for _, raw := range sources {
		if len(raw) != 32 {
			return integrity("scan ingest state", "invalid ID")
		}
		var id mousa.SourceID
		copy(id[:], raw)
		if _, err := getIngestState(ctx, db, id); err != nil {
			return err
		}
	}
	return nil
}

func receiptDifference(stored, want mousa.IngestReceipt) string {
	storedJSON, storedErr := mousa.EncodeIngestReceipt(stored)
	wantJSON, wantErr := mousa.EncodeIngestReceipt(want)
	if storedErr != nil || wantErr != nil {
		return "receipt validation disagrees"
	}
	if bytes.Equal(storedJSON, wantJSON) {
		return ""
	}
	return "receipt canonical bytes disagree"
}

func nullableBytes(value []byte) any {
	if value == nil {
		return nil
	}
	return value
}
func nullableUint64(value *uint64) any {
	if value == nil {
		return nil
	}
	return uint64Blob(*value)
}
func equalNullableBytes(stored, want []byte) bool {
	return bytes.Equal(stored, want) && (stored == nil) == (want == nil)
}
func equalNullableUint64(stored []byte, want *uint64) bool {
	if want == nil {
		return stored == nil
	}
	return len(stored) == 8 && binary.BigEndian.Uint64(stored) == *want
}

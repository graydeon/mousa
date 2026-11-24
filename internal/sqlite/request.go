package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

// requestRecordID derives the shared record id of one request's admission and
// outcome records; the log kind disambiguates them.
func requestRecordID(requestID string) []byte {
	sum := sha256.Sum256([]byte(requestID))
	return sum[:]
}

func requestTable(kind string) string {
	if kind == mousa.LogKindRequestAdmission {
		return "request_admissions"
	}
	return "request_outcomes"
}

// readRequestRecord reads one request record's stored bytes with its leaf. A
// record without a leaf or a leaf without a record is integrity damage.
func readRequestRecord(ctx context.Context, q queryer, requestID, kind, op string) ([]byte, int64, error) {
	id := requestRecordID(requestID)
	var record []byte
	recordErr := q.QueryRowContext(ctx, `SELECT record_json FROM `+requestTable(kind)+` WHERE id = ?`, id).Scan(&record)
	var seq int64
	var leafSource, leaf []byte
	leafErr := q.QueryRowContext(ctx, `SELECT seq, source_id, leaf_hash FROM log WHERE kind = ? AND record_id = ?`, kind, id).
		Scan(&seq, &leafSource, &leaf)
	switch {
	case errors.Is(recordErr, sql.ErrNoRows) && errors.Is(leafErr, sql.ErrNoRows):
		return nil, 0, readError(op, sql.ErrNoRows)
	case errors.Is(recordErr, sql.ErrNoRows) || errors.Is(leafErr, sql.ErrNoRows):
		return nil, 0, integrity(op, "request record and leaf disagree about existence")
	case recordErr != nil:
		return nil, 0, readError(op, recordErr)
	case leafErr != nil:
		return nil, 0, classify(op+" leaf", leafErr)
	case seq <= 0:
		return nil, 0, integrity(op, "request leaf has an invalid sequence")
	}
	if err := checkLeaf(kind, leafSource, leaf, record, nil); err != nil {
		return nil, 0, integrity(op, err.Error())
	}
	return record, seq, nil
}

// readRequestAdmissionAt reads the verified stored admission of one request
// with its leaf sequence.
func readRequestAdmissionAt(ctx context.Context, q queryer, requestID string) (mousa.RequestAdmission, int64, error) {
	record, seq, err := readRequestRecord(ctx, q, requestID, mousa.LogKindRequestAdmission, "read request admission")
	if err != nil {
		return mousa.RequestAdmission{}, 0, err
	}
	admission, err := mousa.DecodeRequestAdmission(record)
	if err != nil {
		return mousa.RequestAdmission{}, 0, wrap(CodeIntegrity, "decode request admission", err)
	}
	if admission.RequestID != requestID {
		return mousa.RequestAdmission{}, 0, integrity("read request admission", "record names another request")
	}
	return admission, seq, nil
}

// readRequestAdmission reads the verified stored admission of one request.
func readRequestAdmission(ctx context.Context, q queryer, requestID string) (mousa.RequestAdmission, error) {
	admission, _, err := readRequestAdmissionAt(ctx, q, requestID)
	return admission, err
}

// checkRequestOutcomeAdmission verifies an outcome against its stored
// admission: the admission leaf must precede the outcome and the pair must be
// a lawful request record.
func checkRequestOutcomeAdmission(ctx context.Context, q queryer, outcome mousa.RequestOutcome) (mousa.RequestAdmission, error) {
	admission, seq, err := readRequestAdmissionAt(ctx, q, outcome.RequestID)
	if err != nil {
		return mousa.RequestAdmission{}, err
	}
	if uint64(seq) >= outcome.Seq {
		return mousa.RequestAdmission{}, integrity("request outcome pairing", "admission leaf does not precede the outcome")
	}
	if err := mousa.CheckRequestOutcomePair(admission, outcome); err != nil {
		return mousa.RequestAdmission{}, wrap(CodeIntegrity, "request outcome pairing", err)
	}
	return admission, nil
}

// readRequestOutcome reads the verified stored outcome of one request with its
// leaf sequence.
func readRequestOutcome(ctx context.Context, q queryer, requestID string) (mousa.RequestOutcome, int64, error) {
	record, seq, err := readRequestRecord(ctx, q, requestID, mousa.LogKindRequestOutcome, "read request outcome")
	if err != nil {
		return mousa.RequestOutcome{}, 0, err
	}
	outcome, err := mousa.DecodeRequestOutcome(record)
	if err != nil {
		return mousa.RequestOutcome{}, 0, wrap(CodeIntegrity, "decode request outcome", err)
	}
	if outcome.RequestID != requestID || outcome.Seq != uint64(seq) {
		return mousa.RequestOutcome{}, 0, integrity("read request outcome", "record disagrees with its request or sequence")
	}
	return outcome, seq, nil
}

// AdmitRequest appends one request's admission record and log leaf in one
// transaction before the operation is served.
func (store *Store) AdmitRequest(ctx context.Context, admission mousa.RequestAdmission) error {
	data, err := mousa.EncodeRequestAdmission(admission)
	if err != nil {
		return wrap(CodeInvalidRecord, "admit request", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	recordID := requestRecordID(admission.RequestID)
	return store.writeImmediate(ctx, "admit request", func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, `INSERT INTO request_admissions(id, record_json) VALUES(?, ?)`, recordID, data); err != nil {
			if sqliteConstraint(err) {
				return integrity("admit request", "request id was already admitted")
			}
			return classify("admit request", err)
		}
		if err := appendLog(ctx, conn, mousa.LogKindRequestAdmission, recordID, nil, data); err != nil {
			return err
		}
		stored, err := readRequestAdmission(ctx, conn, admission.RequestID)
		if err != nil {
			return err
		}
		if stored != admission {
			return integrity("admit request", "read-back disagrees with write")
		}
		return nil
	})
}

// finishRequest appends a request's single terminal outcome inside an existing
// write transaction, after verifying the stored admission still matches the
// request it is closing.
func finishRequest(ctx context.Context, conn *sql.Conn, admission mousa.RequestAdmission, state mousa.RequestState, code mousa.RequestErrorCode, evidence mousa.RequestEvidence) error {
	stored, err := readRequestAdmission(ctx, conn, admission.RequestID)
	if err != nil {
		return err
	}
	if stored != admission {
		return integrity("finish request", "stored admission disagrees with the request")
	}
	var tail uint64
	if err := conn.QueryRowContext(ctx, logIndexSQL).Scan(&tail); err != nil {
		return classify("finish request", err)
	}
	outcome := mousa.RequestOutcome{
		RequestID:      admission.RequestID,
		Seq:            tail + 1,
		FinishedAtUsec: max(admission.ReceivedAtUsec, time.Now().UnixMicro()),
		State:          state,
		ErrorCode:      code,
		Evidence:       evidence,
	}
	if err := mousa.CheckRequestOutcomePair(admission, outcome); err != nil {
		return wrap(CodeIntegrity, "finish request", err)
	}
	data, err := mousa.EncodeRequestOutcome(outcome)
	if err != nil {
		return wrap(CodeInvalidRecord, "finish request", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	recordID := requestRecordID(outcome.RequestID)
	if _, err := conn.ExecContext(ctx, `INSERT INTO request_outcomes(id, record_json) VALUES(?, ?)`, recordID, data); err != nil {
		if sqliteConstraint(err) {
			return integrity("finish request", "request already has an outcome")
		}
		return classify("finish request", err)
	}
	if err := appendLog(ctx, conn, mousa.LogKindRequestOutcome, recordID, nil, data); err != nil {
		return err
	}
	storedBytes, _, err := readRequestRecord(ctx, conn, outcome.RequestID, mousa.LogKindRequestOutcome, "finish request")
	if err != nil {
		return err
	}
	if !bytes.Equal(storedBytes, data) {
		return integrity("finish request", "read-back disagrees with write")
	}
	return nil
}

// FinishRequest records an evidence-free terminal outcome in its own
// transaction for requests whose operation never reached a write.
func (store *Store) FinishRequest(ctx context.Context, admission mousa.RequestAdmission, state mousa.RequestState, code mousa.RequestErrorCode) error {
	return store.writeImmediate(ctx, "finish request", func(conn *sql.Conn) error {
		return finishRequest(ctx, conn, admission, state, code, mousa.RequestEvidence{})
	})
}

// RecoverRequests marks admissions that an interrupted process left without an
// outcome as interrupted. A record/leaf parity disagreement is refused, never
// healed. It runs once before the listener announces its address.
func (store *Store) RecoverRequests(ctx context.Context) error {
	const page = 64
	for {
		done := 0
		err := store.writeImmediate(ctx, "recover requests", func(conn *sql.Conn) error {
			// Parity: a leaf without its record or a record without its leaf
			// is damage, not an in-flight request.
			for _, kind := range []string{mousa.LogKindRequestAdmission, mousa.LogKindRequestOutcome} {
				table := requestTable(kind)
				var orphans int
				if err := conn.QueryRowContext(ctx,
					`SELECT count(*) FROM log l LEFT JOIN `+table+` r ON r.id = l.record_id WHERE l.kind = ? AND r.id IS NULL`, kind).
					Scan(&orphans); err != nil {
					return classify("check request parity", err)
				}
				var missing int
				if err := conn.QueryRowContext(ctx,
					`SELECT count(*) FROM `+table+` r LEFT JOIN log l ON l.kind = ? AND l.record_id = r.id WHERE l.record_id IS NULL`, kind).
					Scan(&missing); err != nil {
					return classify("check request parity", err)
				}
				if orphans != 0 || missing != 0 {
					return integrity("recover requests", "request record and leaf disagree about existence")
				}
			}
			rows, err := conn.QueryContext(ctx,
				`SELECT a.id, a.record_json FROM request_admissions a
				 LEFT JOIN request_outcomes o ON o.id = a.id
				 LEFT JOIN log l ON l.kind = 'request_outcome' AND l.record_id = a.id
				 WHERE o.id IS NULL AND l.record_id IS NULL ORDER BY a.id LIMIT ?`, page)
			if err != nil {
				return classify("scan pending requests", err)
			}
			defer rows.Close()
			var records [][]byte
			for rows.Next() {
				var id, record []byte
				if err := rows.Scan(&id, &record); err != nil {
					return classify("scan pending requests", err)
				}
				records = append(records, record)
			}
			if err := rows.Err(); err != nil {
				return classify("scan pending requests", err)
			}
			for _, record := range records {
				admission, err := mousa.DecodeRequestAdmission(record)
				if err != nil {
					return wrap(CodeIntegrity, "decode pending admission", err)
				}
				stored, err := readRequestAdmission(ctx, conn, admission.RequestID)
				if err != nil {
					return err
				}
				if stored != admission {
					return integrity("recover requests", "admission read-back disagrees")
				}
				if err := finishRequest(ctx, conn, admission, mousa.RequestStateInterrupted, mousa.RequestErrorInterrupted, mousa.RequestEvidence{}); err != nil {
					return err
				}
				done++
			}
			return nil
		})
		if err != nil {
			return err
		}
		if done < page {
			return nil
		}
	}
}

// RequestHistoryEntry pairs one request's verified admission with its outcome.
type RequestHistoryEntry struct {
	Admission mousa.RequestAdmission `json:"admission"`
	Outcome   mousa.RequestOutcome   `json:"outcome"`
}

// RequestHistoryPage is one bounded page of request outcomes in log order.
type RequestHistoryPage struct {
	Requests  []RequestHistoryEntry `json:"requests"`
	NextAfter uint64                `json:"next_after"`
	More      bool                  `json:"more"`
}

// RequestEvidenceSummary describes an outcome's canonical evidence without
// carrying request text, note bytes, or query terms.
type RequestEvidenceSummary struct {
	Kind        string   `json:"kind"`
	ID          string   `json:"id"`
	Sources     []string `json:"sources,omitempty"`
	SourceID    string   `json:"source_id,omitempty"`
	Source      string   `json:"source,omitempty"`
	ItemID      string   `json:"item_id,omitempty"`
	Current     string   `json:"current,omitempty"`
	Previous    string   `json:"previous,omitempty"`
	NoteAuthor  string   `json:"note_author,omitempty"`
	CitedTrails []string `json:"cited_trails,omitempty"`
}

// RequestHistoryDetail is one request's verified pair with its evidence
// resolved against canonical rows.
type RequestHistoryDetail struct {
	RequestHistoryEntry
	Evidence *RequestEvidenceSummary `json:"evidence,omitempty"`
}

// requestEntry verifies one outcome leaf with its admission.
func requestEntry(ctx context.Context, q queryer, seq uint64, recordID, leafSource, leaf, record []byte) (RequestHistoryEntry, error) {
	if err := checkLeaf(mousa.LogKindRequestOutcome, leafSource, leaf, record, nil); err != nil {
		return RequestHistoryEntry{}, integrity("read request outcome", err.Error())
	}
	outcome, err := mousa.DecodeRequestOutcome(record)
	if err != nil {
		return RequestHistoryEntry{}, wrap(CodeIntegrity, "decode request outcome", err)
	}
	if outcome.Seq != seq || !bytes.Equal(recordID, requestRecordID(outcome.RequestID)) {
		return RequestHistoryEntry{}, integrity("read request outcome", "record disagrees with its leaf or request")
	}
	admission, err := checkRequestOutcomeAdmission(ctx, q, outcome)
	if err != nil {
		return RequestHistoryEntry{}, err
	}
	return RequestHistoryEntry{Admission: admission, Outcome: outcome}, nil
}

// RequestHistory pages verified request outcomes by durable log sequence.
func (store *Store) RequestHistory(ctx context.Context, after uint64, limit int) (RequestHistoryPage, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RequestHistoryPage{}, classify("begin request history", err)
	}
	defer tx.Rollback()
	// The LEFT JOIN surfaces a leaf that names a missing outcome row as a
	// NULL record; checkLeaf refuses it as integrity instead of skipping
	// the sequence.
	rows, err := tx.QueryContext(ctx, `SELECT l.seq, l.record_id, l.source_id, l.leaf_hash, o.record_json
		FROM log l LEFT JOIN request_outcomes o ON o.id = l.record_id
		WHERE l.source_id IS NULL AND l.kind = 'request_outcome' AND l.seq > ? ORDER BY l.seq LIMIT ?`,
		after, limit+1)
	if err != nil {
		return RequestHistoryPage{}, classify("read request history", err)
	}
	defer rows.Close()
	page := RequestHistoryPage{Requests: []RequestHistoryEntry{}, NextAfter: after}
	for rows.Next() {
		var seq int64
		var recordID, leafSource, leaf, record []byte
		if err := rows.Scan(&seq, &recordID, &leafSource, &leaf, &record); err != nil {
			return RequestHistoryPage{}, classify("read request history", err)
		}
		entry, err := requestEntry(ctx, tx, uint64(seq), recordID, leafSource, leaf, record)
		if err != nil {
			return RequestHistoryPage{}, err
		}
		if len(page.Requests) == limit {
			page.More = true
			break
		}
		page.Requests = append(page.Requests, entry)
		page.NextAfter = uint64(seq)
	}
	if err := rows.Err(); err != nil {
		return RequestHistoryPage{}, classify("read request history", err)
	}
	if err := tx.Commit(); err != nil {
		return RequestHistoryPage{}, classify("finish request history", err)
	}
	return page, nil
}

// RequestDetail verifies one completed request and resolves its evidence.
// Missing and still in-flight requests read as not found.
func (store *Store) RequestDetail(ctx context.Context, requestID string) (RequestHistoryDetail, error) {
	if !mousa.ValidRequestID(requestID) {
		return RequestHistoryDetail{}, readError("get request detail", sql.ErrNoRows)
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RequestHistoryDetail{}, classify("begin request detail", err)
	}
	defer tx.Rollback()
	outcome, seq, err := readRequestOutcome(ctx, tx, requestID)
	if err != nil {
		return RequestHistoryDetail{}, err
	}
	admission, err := checkRequestOutcomeAdmission(ctx, tx, outcome)
	if err != nil {
		return RequestHistoryDetail{}, err
	}
	summary, err := requestEvidenceSummary(ctx, tx, admission, outcome, seq)
	if err != nil {
		return RequestHistoryDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return RequestHistoryDetail{}, classify("finish request detail", err)
	}
	return RequestHistoryDetail{
		RequestHistoryEntry: RequestHistoryEntry{Admission: admission, Outcome: outcome},
		Evidence:            summary,
	}, nil
}

// requestEvidenceSummary verifies an outcome's evidence against the canonical
// records it names and builds the metadata-only summary.
func requestEvidenceSummary(ctx context.Context, q queryer, admission mousa.RequestAdmission, outcome mousa.RequestOutcome, outcomeSeq int64) (*RequestEvidenceSummary, error) {
	evidence := outcome.Evidence
	leafSeq := func(kind string, id []byte) (int64, error) {
		var seq int64
		err := q.QueryRowContext(ctx, `SELECT seq FROM log WHERE kind = ? AND record_id = ?`, kind, id).Scan(&seq)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, integrity("request evidence", "evidence leaf is missing")
		}
		if err != nil {
			return 0, classify("request evidence leaf", err)
		}
		return seq, nil
	}
	switch {
	case evidence.TrailID != nil:
		trail, _, err := readTrailRecord(ctx, q, *evidence.TrailID)
		if err != nil {
			return nil, lexicalStructuralError("request evidence trail", err)
		}
		if err := checkTrailReferences(ctx, q, trail); err != nil {
			return nil, err
		}
		seq, err := leafSeq(queryTrailKind, evidence.TrailID[:])
		if err != nil {
			return nil, err
		}
		if seq >= outcomeSeq {
			return nil, integrity("request evidence", "trail leaf does not precede the outcome")
		}
		if trail.Request.RequestID != outcome.RequestID || trail.Request.Caller != admission.Principal {
			return nil, integrity("request evidence", "trail does not belong to this request")
		}
		if outcome.State == mousa.RequestStateDenied {
			if len(trail.Sources) == 0 {
				return nil, integrity("request evidence", "denied trail has no sources")
			}
			for _, source := range trail.Sources {
				if source.Outcome != "deny" {
					return nil, integrity("request evidence", "denied outcome cites a trail with allowed evidence")
				}
			}
		}
		sources := make([]string, 0, len(trail.Sources))
		for _, source := range trail.Sources {
			sources = append(sources, source.SourceID.String())
		}
		return &RequestEvidenceSummary{Kind: "trail", ID: trail.ID.String(), Sources: sources}, nil
	case evidence.NoteID != nil:
		var rawSource []byte
		var itemID string
		err := q.QueryRowContext(ctx, `SELECT source_id, item_id FROM notes WHERE id = ?`, evidence.NoteID[:]).Scan(&rawSource, &itemID)
		if err != nil {
			return nil, lexicalStructuralError("request evidence note", readError("request evidence note", err))
		}
		if len(rawSource) != len(mousa.SourceID{}) {
			return nil, integrity("request evidence", "note projection is damaged")
		}
		note, _, err := getNote(ctx, q, mousa.SourceID(rawSource), itemID)
		if err != nil {
			return nil, lexicalStructuralError("request evidence note", err)
		}
		if note.ID != *evidence.NoteID {
			return nil, integrity("request evidence", "note id disagrees with its record")
		}
		seq, err := leafSeq(mousa.LogKindNote, evidence.NoteID[:])
		if err != nil {
			return nil, err
		}
		if seq >= outcomeSeq {
			return nil, integrity("request evidence", "note leaf does not precede the outcome")
		}
		if outcome.State == mousa.RequestStateCommitted && note.Author != admission.Principal {
			return nil, integrity("request evidence", "note author disagrees with the request actor")
		}
		source, err := getSource(ctx, q, note.SourceID)
		if err != nil {
			return nil, lexicalStructuralError("request evidence note", err)
		}
		cited := make([]string, 0, len(note.DerivedFrom))
		for _, citation := range note.DerivedFrom {
			cited = append(cited, citation.TrailID.String())
		}
		return &RequestEvidenceSummary{
			Kind: "note", ID: note.ID.String(), SourceID: note.SourceID.String(),
			Source: source.ExternalSourceID, NoteAuthor: note.Author,
			CitedTrails: cited,
		}, nil
	case evidence.ItemChangeID != nil:
		var seq int64
		var kind string
		var recordID, leafSource, leaf, record, recordSource []byte
		err := q.QueryRowContext(ctx, logRecordSQL+` WHERE l.kind = 'item_change' AND l.record_id = ?`, evidence.ItemChangeID[:]).
			Scan(&seq, &kind, &recordID, &leafSource, &leaf, &record, &recordSource)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, integrity("request evidence", "item change leaf is missing")
		}
		if err != nil {
			return nil, classify("request evidence item change", err)
		}
		if err := checkLeaf(kind, leafSource, leaf, record, recordSource); err != nil {
			return nil, wrap(CodeIntegrity, "request evidence item change", err)
		}
		change, err := readItemChange(uint64(seq), recordID, leafSource, record)
		if err != nil {
			return nil, err
		}
		if change.Seq >= uint64(outcomeSeq) {
			return nil, integrity("request evidence", "item change leaf does not precede the outcome")
		}
		// Each named representation must resolve through its source artifact
		// to an observation of this source and item revision. Historical
		// revisions need not remain active.
		for _, ref := range []*mousa.RepresentationID{change.Previous, change.Current} {
			if ref == nil {
				continue
			}
			if err := verifyItemRevision(ctx, q, change, *ref); err != nil {
				return nil, err
			}
		}
		source, err := getSource(ctx, q, change.SourceID)
		if err != nil {
			return nil, lexicalStructuralError("request evidence source", err)
		}
		summary := &RequestEvidenceSummary{
			Kind: "item_change", ID: change.ID.String(), SourceID: change.SourceID.String(),
			Source: source.ExternalSourceID, ItemID: change.ItemID,
		}
		if change.Current != nil {
			summary.Current = change.Current.String()
		}
		if change.Previous != nil {
			summary.Previous = change.Previous.String()
		}
		return summary, nil
	}
	return nil, nil
}

// verifyItemRevision confirms one representation named by an item change
// belongs to the change's source and item: the representation resolves, its
// source artifact resolves, and the owning observation carries this source's
// item revision identity.
func verifyItemRevision(ctx context.Context, q queryer, change mousa.ItemChange, id mousa.RepresentationID) error {
	if _, err := getRepresentation(ctx, q, id); err != nil {
		return lexicalStructuralError("request evidence representation", err)
	}
	artifact, err := representationSourceArtifact(ctx, q, id)
	if err != nil {
		return lexicalStructuralError("request evidence representation", err)
	}
	observation, err := getObservation(ctx, q, artifact.ObservationID)
	if err != nil {
		return lexicalStructuralError("request evidence representation", err)
	}
	if observation.SourceID != change.SourceID {
		return integrity("request evidence", "representation belongs to another source")
	}
	if observation.ExternalObservationID != "item/"+change.ItemID+localRevisionSuffix(artifact.ContentSHA256) {
		return integrity("request evidence", "item revision identity disagrees with observation")
	}
	return nil
}

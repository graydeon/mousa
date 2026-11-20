package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

// EvaluateSourceRetrieval evaluates one source.retrieve action and stores one immutable decision.
// It reads one verified access and lifecycle snapshot inside the writer transaction, returns the
// exact stored decision for an exact retry, and never calls retrieval or enforces the decision.
func (store *Store) EvaluateSourceRetrieval(ctx context.Context, request mousa.PolicyEvaluationRequest) (mousa.PolicyDecision, error) {
	if err := store.requireWritable("evaluate source retrieval"); err != nil {
		return mousa.PolicyDecision{}, err
	}
	if err := request.Validate(); err != nil {
		return mousa.PolicyDecision{}, wrap(CodeInvalidRecord, "evaluate source retrieval", err)
	}
	var stored mousa.PolicyDecision
	err := store.writeImmediate(ctx, "evaluate source retrieval", func(conn *sql.Conn) error {
		var err error
		stored, err = evaluateSourceRetrieval(ctx, conn, request, true)
		return err
	})
	if err != nil {
		return mousa.PolicyDecision{}, err
	}
	return stored, nil
}

func evaluateSourceRetrieval(ctx context.Context, conn *sql.Conn, request mousa.PolicyEvaluationRequest, allowReplay bool) (mousa.PolicyDecision, error) {
	existing, err := getPolicyDecisionByRequest(ctx, conn, request.ID)
	if err == nil {
		if existing.Request != request {
			return mousa.PolicyDecision{}, integrity("evaluate source retrieval", "stored request disagrees with request identity")
		}
		if !allowReplay {
			return mousa.PolicyDecision{}, wrap(CodeConflict, "evaluate source retrieval", errors.New("current evaluation requires a new request identity"))
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return mousa.PolicyDecision{}, err
	}
	if err := requireUnusedExternalRequest(ctx, conn, request); err != nil {
		return mousa.PolicyDecision{}, err
	}
	snapshot, err := readPolicyEvaluationSnapshot(ctx, conn, request)
	if err != nil {
		return mousa.PolicyDecision{}, err
	}
	decision, err := mousa.EvaluateSourceRetrieval(request, snapshot, time.Now().UnixMicro())
	if err != nil {
		return mousa.PolicyDecision{}, wrap(CodeInvalidRecord, "evaluate source retrieval", err)
	}
	if err := insertPolicyDecision(ctx, conn, decision); err != nil {
		return mousa.PolicyDecision{}, err
	}
	written, err := getPolicyDecision(ctx, conn, decision.ID)
	if err != nil {
		return mousa.PolicyDecision{}, err
	}
	if !reflect.DeepEqual(written, decision) {
		return mousa.PolicyDecision{}, integrity("evaluate source retrieval", "exact read-back disagrees with write")
	}
	return written, nil
}

// GetPolicyDecision returns one stored decision with its verified relational projection.
func (store *Store) GetPolicyDecision(ctx context.Context, id mousa.PolicyDecisionID) (mousa.PolicyDecision, error) {
	return getPolicyDecision(ctx, store.db, id)
}

func getPolicyDecisionByRequest(ctx context.Context, q queryer, id mousa.PolicyEvaluationRequestID) (mousa.PolicyDecision, error) {
	var raw []byte
	if err := q.QueryRowContext(ctx, `SELECT id FROM policy_decisions WHERE request_id = ?`, id[:]).Scan(&raw); err != nil {
		return mousa.PolicyDecision{}, readError("find policy decision", err)
	}
	if len(raw) != 32 {
		return mousa.PolicyDecision{}, integrity("find policy decision", "invalid ID length")
	}
	var decisionID mousa.PolicyDecisionID
	copy(decisionID[:], raw)
	return getPolicyDecision(ctx, q, decisionID)
}

func requireUnusedExternalRequest(ctx context.Context, q queryRower, request mousa.PolicyEvaluationRequest) error {
	var raw []byte
	err := q.QueryRowContext(ctx, `SELECT request_id FROM policy_decisions WHERE caller_namespace = ? AND external_caller_id = ? AND external_request_id = ?`, request.CallerNamespace, request.ExternalCallerID, request.ExternalRequestID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return classify("evaluate source retrieval", err)
	}
	if bytes.Equal(raw, request.ID[:]) {
		return nil
	}
	return wrap(CodeConflict, "evaluate source retrieval", errors.New("external request identity is already used by another request"))
}

func readPolicyEvaluationSnapshot(ctx context.Context, q queryer, request mousa.PolicyEvaluationRequest) (mousa.PolicyEvaluationSnapshot, error) {
	var snapshot mousa.PolicyEvaluationSnapshot
	if _, err := getSource(ctx, q, request.SourceID); err != nil {
		if IsCode(err, CodeNotFound) {
			return snapshot, integrity("evaluate source retrieval", "request source does not exist")
		}
		return snapshot, err
	}
	state, err := getIngestState(ctx, q, request.SourceID)
	switch {
	case err == nil:
		collection := state.CollectionState
		snapshot.StatePresent = true
		snapshot.CollectionState = &collection
		snapshot.CurrentWithdrawalID = cloneWithdrawal(state.CurrentWithdrawalID)
	case IsCode(err, CodeNotFound):
	case IsCode(err, CodeIntegrity):
		return snapshot, err
	default:
		return snapshot, err
	}
	snapshot.Access, snapshot.AccessEventID, err = snapshotAccess(ctx, q, request.SourceID)
	return snapshot, err
}

func insertPolicyDecision(ctx context.Context, conn *sql.Conn, decision mousa.PolicyDecision) error {
	data, err := mousa.EncodePolicyDecision(decision)
	if err != nil {
		return wrap(CodeInvalidRecord, "insert policy decision", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	var withdrawal any
	if decision.CurrentWithdrawalID != nil {
		withdrawal = decision.CurrentWithdrawalID[:]
	}
	result, err := conn.ExecContext(ctx, `INSERT INTO policy_decisions(id, request_id, caller_namespace, external_caller_id, external_request_id, source_id, outcome, evaluated_at_usec, current_withdrawal_id, record_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, decision.ID[:], decision.Request.ID[:], decision.Request.CallerNamespace, decision.Request.ExternalCallerID, decision.Request.ExternalRequestID, decision.Request.SourceID[:], string(decision.Outcome), decision.EvaluatedAtUsec, withdrawal, data)
	if err != nil {
		if sqliteForeignKey(err) {
			return integrity("insert policy decision", "typed parent is missing")
		}
		if sqliteConstraint(err) {
			return wrap(CodeConflict, "insert policy decision", err)
		}
		return classify("insert policy decision", err)
	}
	if err := oneRow(result); err != nil {
		return wrap(CodeIntegrity, "insert policy decision", err)
	}
	return nil
}

func getPolicyDecision(ctx context.Context, q queryer, id mousa.PolicyDecisionID) (mousa.PolicyDecision, error) {
	var requestID, sourceID, withdrawal []byte
	var callerNamespace, externalCallerID, externalRequestID, outcome string
	var evaluatedAt int64
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT request_id, caller_namespace, external_caller_id, external_request_id, source_id, outcome, evaluated_at_usec, current_withdrawal_id, record_json FROM policy_decisions WHERE id = ?`, id[:]).Scan(&requestID, &callerNamespace, &externalCallerID, &externalRequestID, &sourceID, &outcome, &evaluatedAt, &withdrawal, &data); err != nil {
		return mousa.PolicyDecision{}, readError("get policy decision", err)
	}
	record, err := decodeCanonical(data, mousa.DecodePolicyDecision, mousa.EncodePolicyDecision)
	if err != nil {
		return mousa.PolicyDecision{}, wrap(CodeIntegrity, "get policy decision", err)
	}
	wantSource := record.Request.SourceID
	if record.ID != id || !bytes.Equal(requestID, record.Request.ID[:]) || record.Request.CallerNamespace != callerNamespace || record.Request.ExternalCallerID != externalCallerID || record.Request.ExternalRequestID != externalRequestID || !bytes.Equal(sourceID, wantSource[:]) || outcome != string(record.Outcome) || evaluatedAt != record.EvaluatedAtUsec || !nullableBytesEqual(withdrawal, withdrawalIDBytes(record.CurrentWithdrawalID)) {
		return mousa.PolicyDecision{}, integrity("get policy decision", "relational projection disagrees with record")
	}
	return record, nil
}

func withdrawalIDBytes(id *mousa.WithdrawalID) []byte {
	if id == nil {
		return nil
	}
	return id[:]
}

func sqliteForeignKey(err error) bool {
	var sqliteErr interface{ Code() int }
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 19 && sqliteErr.Code() != 19
}

// verifyPolicyDecisionRecords verifies every stored decision, its typed source parent and
// withdrawal ownership.
func verifyPolicyDecisionRecords(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM policy_decisions ORDER BY id`)
	if err != nil {
		return startupError("scan policy decisions", err)
	}
	var ids [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return startupError("scan policy decisions", err)
		}
		ids = append(ids, append([]byte(nil), raw...))
	}
	if err := rows.Close(); err != nil {
		return startupError("scan policy decisions", err)
	}
	for _, raw := range ids {
		if len(raw) != 32 {
			return integrity("scan policy decisions", "invalid ID length")
		}
		var id mousa.PolicyDecisionID
		copy(id[:], raw)
		record, err := getPolicyDecision(ctx, db, id)
		if err != nil {
			return err
		}
		if _, err := getSource(ctx, db, record.Request.SourceID); IsCode(err, CodeNotFound) {
			return integrity("verify policy decisions", "typed source parent is missing")
		} else if err != nil {
			return err
		}
		if record.AccessEventID != nil {
			event, _, err := getAccessEvent(ctx, db, *record.AccessEventID)
			if err != nil {
				if IsCode(err, CodeNotFound) {
					return integrity("verify policy decisions", "cited access event is missing")
				}
				return err
			}
			if event.SourceID != record.Request.SourceID || event.Access != record.Access {
				return integrity("verify policy decisions", "cited access event disagrees with the decision")
			}
		}
		if record.CurrentWithdrawalID != nil {
			withdrawal, err := getSourceWithdrawal(ctx, db, *record.CurrentWithdrawalID)
			if err != nil {
				if IsCode(err, CodeNotFound) {
					return integrity("verify policy decisions", "typed withdrawal parent is missing")
				}
				return err
			}
			if withdrawal.SourceID != record.Request.SourceID {
				return integrity("verify policy decisions", "withdrawal belongs to another source")
			}
		}
	}
	return nil
}

func cloneWithdrawal(value *mousa.WithdrawalID) *mousa.WithdrawalID {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func nullableBytesEqual(left, right []byte) bool {
	return (left == nil && right == nil) || bytes.Equal(left, right)
}

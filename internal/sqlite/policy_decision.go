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
// It reads one verified policy and lifecycle snapshot inside the writer transaction, returns the
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

// GetPolicyDecision returns one stored decision with verified relational and ordered input projections.
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
	snapshot := mousa.PolicyEvaluationSnapshot{Inputs: []mousa.PolicyEvaluationInput{}}
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
	for _, layer := range []mousa.PolicyDecisionLayer{mousa.PolicyLayerDeployment, mousa.PolicyLayerSource} {
		inputs, err := readActivePolicyInputs(ctx, q, layer, request.SourceID)
		if err != nil {
			return snapshot, err
		}
		snapshot.Inputs = append(snapshot.Inputs, inputs...)
	}
	return snapshot, nil
}

func readActivePolicyInputs(ctx context.Context, q queryer, layer mousa.PolicyDecisionLayer, sourceID mousa.SourceID) ([]mousa.PolicyEvaluationInput, error) {
	var rows *sql.Rows
	var err error
	if layer == mousa.PolicyLayerDeployment {
		rows, err = q.QueryContext(ctx, `SELECT s.namespace, s.external_binding_id, s.current_activation_id, s.active_binding_id, b.policy_definition_id FROM policy_binding_state AS s JOIN policy_bindings AS b ON b.id = s.active_binding_id WHERE b.scope_kind = 'deployment' AND b.subject_id IS NULL ORDER BY b.namespace, b.external_binding_id, s.current_activation_id`)
	} else {
		rows, err = q.QueryContext(ctx, `SELECT s.namespace, s.external_binding_id, s.current_activation_id, s.active_binding_id, b.policy_definition_id FROM policy_binding_state AS s JOIN policy_bindings AS b ON b.id = s.active_binding_id WHERE b.scope_kind = 'source' AND b.subject_id = ? ORDER BY b.namespace, b.external_binding_id, s.current_activation_id`, sourceID[:])
	}
	if err != nil {
		return nil, classify("read policy inputs", err)
	}
	defer rows.Close()
	inputs := []mousa.PolicyEvaluationInput{}
	for rows.Next() {
		var namespace, externalBindingID string
		var activationRaw, bindingRaw, definitionRaw []byte
		if err := rows.Scan(&namespace, &externalBindingID, &activationRaw, &bindingRaw, &definitionRaw); err != nil {
			rows.Close()
			return nil, classify("read policy inputs", err)
		}
		activation, binding, definition, err := readPolicyInputParents(ctx, q, activationRaw, bindingRaw, definitionRaw)
		if err != nil {
			return nil, err
		}
		if activation.Namespace != namespace || activation.ExternalBindingID != externalBindingID || binding.Namespace != namespace || binding.ExternalBindingID != externalBindingID {
			return nil, integrity("read policy inputs", "activation or binding belongs to another series")
		}
		if activation.ActiveBindingID == nil || *activation.ActiveBindingID != binding.ID {
			return nil, integrity("read policy inputs", "active binding disagrees with activation chain")
		}
		if binding.Scope.Kind() != mousa.PolicyScopeKind(layer) {
			return nil, integrity("read policy inputs", "binding scope disagrees with requested layer")
		}
		if layer == mousa.PolicyLayerSource {
			id, ok := binding.Scope.SourceID()
			if !ok || id != sourceID {
				return nil, integrity("read policy inputs", "binding scope disagrees with requested source")
			}
		}
		state, err := getPolicyBindingState(ctx, q, namespace, externalBindingID)
		if err != nil || state.CurrentActivationID != activation.ID || state.ActiveBindingID == nil || *state.ActiveBindingID != binding.ID {
			return nil, integrity("read policy inputs", "binding state projection disagrees with activation")
		}
		inputs = append(inputs, mousa.PolicyEvaluationInput{
			Layer:        layer,
			ActivationID: activation.ID,
			BindingID:    binding.ID,
			DefinitionID: binding.PolicyDefinitionID,
			Definition:   definition,
		})
	}
	return inputs, nil
}

func readPolicyInputParents(ctx context.Context, q queryer, activationRaw, bindingRaw, definitionRaw []byte) (mousa.PolicyActivation, mousa.PolicyBinding, mousa.PolicyDefinition, error) {
	if len(activationRaw) != 32 || len(bindingRaw) != 32 || len(definitionRaw) != 32 {
		return mousa.PolicyActivation{}, mousa.PolicyBinding{}, mousa.PolicyDefinition{}, integrity("read policy inputs", "invalid typed parent length")
	}
	var activationID mousa.PolicyActivationID
	var bindingID mousa.PolicyBindingID
	var definitionID mousa.PolicyDefinitionID
	copy(activationID[:], activationRaw)
	copy(bindingID[:], bindingRaw)
	copy(definitionID[:], definitionRaw)
	activation, err := getPolicyActivation(ctx, q, activationID)
	if IsCode(err, CodeNotFound) {
		return mousa.PolicyActivation{}, mousa.PolicyBinding{}, mousa.PolicyDefinition{}, integrity("read policy inputs", "typed activation parent is missing")
	}
	if err != nil {
		return mousa.PolicyActivation{}, mousa.PolicyBinding{}, mousa.PolicyDefinition{}, err
	}
	binding, err := getPolicyBinding(ctx, q, bindingID)
	if IsCode(err, CodeNotFound) {
		return mousa.PolicyActivation{}, mousa.PolicyBinding{}, mousa.PolicyDefinition{}, integrity("read policy inputs", "typed binding parent is missing")
	}
	if err != nil {
		return mousa.PolicyActivation{}, mousa.PolicyBinding{}, mousa.PolicyDefinition{}, err
	}
	definition, err := getPolicyDefinition(ctx, q, binding.PolicyDefinitionID)
	if IsCode(err, CodeNotFound) {
		return mousa.PolicyActivation{}, mousa.PolicyBinding{}, mousa.PolicyDefinition{}, integrity("read policy inputs", "typed definition parent is missing")
	}
	if err != nil {
		return mousa.PolicyActivation{}, mousa.PolicyBinding{}, mousa.PolicyDefinition{}, err
	}
	if binding.PolicyDefinitionID != definitionID {
		return mousa.PolicyActivation{}, mousa.PolicyBinding{}, mousa.PolicyDefinition{}, integrity("read policy inputs", "definition projection disagrees with binding")
	}
	return activation, binding, definition, nil
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
	for ordinal, input := range decision.PolicyInputs {
		result, err := conn.ExecContext(ctx, `INSERT INTO policy_decision_inputs(decision_id, ordinal, layer, activation_id, binding_id, definition_id, result) VALUES(?, ?, ?, ?, ?, ?, ?)`, decision.ID[:], ordinal, string(input.Layer), input.PolicyActivationID[:], input.PolicyBindingID[:], input.PolicyDefinitionID[:], string(input.Result))
		if err != nil {
			if sqliteForeignKey(err) {
				return integrity("insert policy decision input", "typed parent is missing")
			}
			return classify("insert policy decision input", err)
		}
		if err := oneRow(result); err != nil {
			return wrap(CodeIntegrity, "insert policy decision input", err)
		}
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
	rows, err := q.QueryContext(ctx, `SELECT ordinal, layer, activation_id, binding_id, definition_id, result FROM policy_decision_inputs WHERE decision_id = ? ORDER BY ordinal`, id[:])
	if err != nil {
		return mousa.PolicyDecision{}, classify("get policy decision inputs", err)
	}
	defer rows.Close()
	for ordinal, want := range record.PolicyInputs {
		if !rows.Next() {
			return mousa.PolicyDecision{}, integrity("get policy decision inputs", "missing ordered input")
		}
		var gotOrdinal int
		var layer, result string
		var activation, binding, definition []byte
		if err := rows.Scan(&gotOrdinal, &layer, &activation, &binding, &definition, &result); err != nil {
			return mousa.PolicyDecision{}, classify("get policy decision inputs", err)
		}
		if gotOrdinal != ordinal || layer != string(want.Layer) || !bytes.Equal(activation, want.PolicyActivationID[:]) || !bytes.Equal(binding, want.PolicyBindingID[:]) || !bytes.Equal(definition, want.PolicyDefinitionID[:]) || result != string(want.Result) {
			return mousa.PolicyDecision{}, integrity("get policy decision inputs", "ordered input projection disagrees with record")
		}
	}
	if rows.Next() {
		return mousa.PolicyDecision{}, integrity("get policy decision inputs", "extra ordered input")
	}
	if err := rows.Err(); err != nil {
		return mousa.PolicyDecision{}, classify("get policy decision inputs", err)
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

// verifyPolicyDecisionRecords verifies every stored decision, its ordered inputs, typed parents,
// withdrawal ownership, and that no input row exists outside the canonical records.
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
	inputCount := 0
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
		for _, input := range record.PolicyInputs {
			if _, _, _, err := readPolicyInputParents(ctx, db, input.PolicyActivationID[:], input.PolicyBindingID[:], input.PolicyDefinitionID[:]); err != nil {
				return err
			}
			if input.Layer == mousa.PolicyLayerSource {
				binding, err := getPolicyBinding(ctx, db, input.PolicyBindingID)
				if err != nil {
					return err
				}
				id, ok := binding.Scope.SourceID()
				if !ok || id != record.Request.SourceID {
					return integrity("verify policy decisions", "source input belongs to another source")
				}
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
		inputCount += len(record.PolicyInputs)
	}
	var total int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM policy_decision_inputs`).Scan(&total); err != nil {
		return startupError("scan policy decision inputs", err)
	}
	if total != inputCount {
		return integrity("verify policy decisions", "input row count disagrees with canonical records")
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

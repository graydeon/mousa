package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/graydeon/mousa/internal/mousa"
)

func (store *Store) PutPolicyBinding(ctx context.Context, record mousa.PolicyBinding) error {
	if err := store.requireWritable("put policy binding"); err != nil {
		return err
	}
	data, err := mousa.EncodePolicyBinding(record)
	if err != nil {
		return wrap(CodeInvalidRecord, "put policy binding", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	kind, subjectID := policyScopeProjection(record.Scope)
	return store.writeImmediate(ctx, "put policy binding", func(conn *sql.Conn) error {
		if err := verifyPolicyBindingParents(ctx, conn, record.PolicyDefinitionID, kind, subjectID); err != nil {
			return err
		}
		result, err := conn.ExecContext(ctx, `INSERT INTO policy_bindings(id, namespace, external_binding_id, external_binding_version, scope_kind, subject_id, policy_definition_id, record_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, record.ID[:], record.Namespace, record.ExternalBindingID, record.ExternalBindingVersion, kind, subjectID, record.PolicyDefinitionID[:], data)
		if err != nil {
			if !sqliteConstraint(err) {
				return classify("put policy binding", err)
			}
			var rawID, existingSubject, rawDefinition, existing []byte
			var namespace, externalID, version, existingKind string
			queryErr := conn.QueryRowContext(ctx, `SELECT id, namespace, external_binding_id, external_binding_version, scope_kind, subject_id, policy_definition_id, record_json FROM policy_bindings WHERE id = ? OR (namespace = ? AND external_binding_id = ? AND external_binding_version = ?)`, record.ID[:], record.Namespace, record.ExternalBindingID, record.ExternalBindingVersion).Scan(&rawID, &namespace, &externalID, &version, &existingKind, &existingSubject, &rawDefinition, &existing)
			if queryErr != nil || !bytes.Equal(rawID, record.ID[:]) || namespace != record.Namespace || externalID != record.ExternalBindingID || version != record.ExternalBindingVersion || existingKind != kind || !nullableBytesEqual(existingSubject, subjectID) || !bytes.Equal(rawDefinition, record.PolicyDefinitionID[:]) || !bytes.Equal(existing, data) {
				return wrap(CodeConflict, "put policy binding", err)
			}
			if verifyErr := verifyPolicyBinding(ctx, conn, record, data); verifyErr != nil {
				return wrap(CodeConflict, "put policy binding", verifyErr)
			}
			return nil
		}
		if err := oneRow(result); err != nil {
			return wrap(CodeIntegrity, "put policy binding", err)
		}
		return verifyPolicyBinding(ctx, conn, record, data)
	})
}

func (store *Store) GetPolicyBinding(ctx context.Context, id mousa.PolicyBindingID) (mousa.PolicyBinding, error) {
	return getPolicyBinding(ctx, store.db, id)
}

func getPolicyBinding(ctx context.Context, q queryRower, id mousa.PolicyBindingID) (mousa.PolicyBinding, error) {
	var namespace, externalID, version, kind string
	var subjectID, definitionID, data []byte
	if err := q.QueryRowContext(ctx, `SELECT namespace, external_binding_id, external_binding_version, scope_kind, subject_id, policy_definition_id, record_json FROM policy_bindings WHERE id = ?`, id[:]).Scan(&namespace, &externalID, &version, &kind, &subjectID, &definitionID, &data); err != nil {
		return mousa.PolicyBinding{}, readError("get policy binding", err)
	}
	record, err := decodeCanonical(data, mousa.DecodePolicyBinding, mousa.EncodePolicyBinding)
	if err != nil {
		return mousa.PolicyBinding{}, wrap(CodeIntegrity, "get policy binding", err)
	}
	wantKind, wantSubject := policyScopeProjection(record.Scope)
	if record.ID != id || record.Namespace != namespace || record.ExternalBindingID != externalID || record.ExternalBindingVersion != version || wantKind != kind || !nullableBytesEqual(subjectID, wantSubject) || !bytes.Equal(definitionID, record.PolicyDefinitionID[:]) {
		return mousa.PolicyBinding{}, integrity("get policy binding", "relational projection disagrees with record")
	}
	return record, nil
}

func verifyPolicyBinding(ctx context.Context, q queryRower, want mousa.PolicyBinding, wantData []byte) error {
	got, err := getPolicyBinding(ctx, q, want.ID)
	if err != nil {
		return err
	}
	encoded, err := mousa.EncodePolicyBinding(got)
	if err != nil || !bytes.Equal(encoded, wantData) || !reflect.DeepEqual(got, want) {
		return integrity("verify policy binding", "exact read-back disagrees with write")
	}
	return nil
}

func (store *Store) ApplyPolicyActivation(ctx context.Context, record mousa.PolicyActivation) error {
	if err := store.requireWritable("apply policy activation"); err != nil {
		return err
	}
	data, err := mousa.EncodePolicyActivation(record)
	if err != nil {
		return wrap(CodeInvalidRecord, "apply policy activation", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	return store.writeImmediate(ctx, "apply policy activation", func(conn *sql.Conn) error {
		existing, existingData, err := findPolicyActivation(ctx, conn, record)
		if err == nil {
			if !reflect.DeepEqual(existing, record) || !bytes.Equal(existingData, data) {
				return wrap(CodeConflict, "apply policy activation", errors.New("activation identity already has different bytes"))
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if record.ActiveBindingID != nil {
			binding, err := getPolicyBinding(ctx, conn, *record.ActiveBindingID)
			if err != nil {
				return wrap(CodeConflict, "apply policy activation", err)
			}
			if binding.Namespace != record.Namespace || binding.ExternalBindingID != record.ExternalBindingID {
				return wrap(CodeConflict, "apply policy activation", errors.New("active binding belongs to another series"))
			}
		}
		var currentRaw, activeRaw []byte
		stateErr := conn.QueryRowContext(ctx, `SELECT current_activation_id, active_binding_id FROM policy_binding_state WHERE namespace = ? AND external_binding_id = ?`, record.Namespace, record.ExternalBindingID).Scan(&currentRaw, &activeRaw)
		if stateErr != nil && !errors.Is(stateErr, sql.ErrNoRows) {
			return classify("apply policy activation", stateErr)
		}
		if errors.Is(stateErr, sql.ErrNoRows) {
			if record.ExpectedPreviousActivationID != nil || record.ActiveBindingID == nil {
				return wrap(CodeConflict, "apply policy activation", errors.New("initial transition is stale or a no-op"))
			}
		} else {
			if record.ExpectedPreviousActivationID == nil || !bytes.Equal(currentRaw, record.ExpectedPreviousActivationID[:]) {
				return wrap(CodeConflict, "apply policy activation", errors.New("expected previous activation is stale"))
			}
			if nullableBytesEqual(activeRaw, bindingIDBytes(record.ActiveBindingID)) {
				return wrap(CodeConflict, "apply policy activation", errors.New("new transition does not change active binding"))
			}
		}
		result, err := conn.ExecContext(ctx, `INSERT INTO policy_activations(id, namespace, external_binding_id, external_activation_id, expected_previous_activation_id, active_binding_id, record_json) VALUES(?, ?, ?, ?, ?, ?, ?)`, record.ID[:], record.Namespace, record.ExternalBindingID, record.ExternalActivationID, activationIDBytes(record.ExpectedPreviousActivationID), bindingIDBytes(record.ActiveBindingID), data)
		if err != nil {
			if sqliteConstraint(err) {
				return wrap(CodeConflict, "apply policy activation", err)
			}
			return classify("apply policy activation", err)
		}
		if err := oneRow(result); err != nil {
			return wrap(CodeIntegrity, "apply policy activation", err)
		}
		result, err = conn.ExecContext(ctx, `INSERT INTO policy_binding_state(namespace, external_binding_id, current_activation_id, active_binding_id) VALUES(?, ?, ?, ?) ON CONFLICT(namespace, external_binding_id) DO UPDATE SET current_activation_id = excluded.current_activation_id, active_binding_id = excluded.active_binding_id`, record.Namespace, record.ExternalBindingID, record.ID[:], bindingIDBytes(record.ActiveBindingID))
		if err != nil {
			return classify("apply policy activation", err)
		}
		if err := oneRow(result); err != nil {
			return wrap(CodeIntegrity, "apply policy activation", err)
		}
		return verifyPolicyActivation(ctx, conn, record, data)
	})
}

func (store *Store) GetPolicyActivation(ctx context.Context, id mousa.PolicyActivationID) (mousa.PolicyActivation, error) {
	return getPolicyActivation(ctx, store.db, id)
}

func getPolicyActivation(ctx context.Context, q queryRower, id mousa.PolicyActivationID) (mousa.PolicyActivation, error) {
	var namespace, externalID, externalActivationID string
	var previous, active, data []byte
	if err := q.QueryRowContext(ctx, `SELECT namespace, external_binding_id, external_activation_id, expected_previous_activation_id, active_binding_id, record_json FROM policy_activations WHERE id = ?`, id[:]).Scan(&namespace, &externalID, &externalActivationID, &previous, &active, &data); err != nil {
		return mousa.PolicyActivation{}, readError("get policy activation", err)
	}
	record, err := decodeCanonical(data, mousa.DecodePolicyActivation, mousa.EncodePolicyActivation)
	if err != nil {
		return mousa.PolicyActivation{}, wrap(CodeIntegrity, "get policy activation", err)
	}
	if record.ID != id || record.Namespace != namespace || record.ExternalBindingID != externalID || record.ExternalActivationID != externalActivationID || !nullableBytesEqual(previous, activationIDBytes(record.ExpectedPreviousActivationID)) || !nullableBytesEqual(active, bindingIDBytes(record.ActiveBindingID)) {
		return mousa.PolicyActivation{}, integrity("get policy activation", "relational projection disagrees with record")
	}
	return record, nil
}

func findPolicyActivation(ctx context.Context, q queryRower, want mousa.PolicyActivation) (mousa.PolicyActivation, []byte, error) {
	var rawID, data []byte
	err := q.QueryRowContext(ctx, `SELECT id, record_json FROM policy_activations WHERE id = ? OR (namespace = ? AND external_binding_id = ? AND external_activation_id = ?)`, want.ID[:], want.Namespace, want.ExternalBindingID, want.ExternalActivationID).Scan(&rawID, &data)
	if err != nil {
		return mousa.PolicyActivation{}, nil, err
	}
	if len(rawID) != 32 {
		return mousa.PolicyActivation{}, nil, integrity("find policy activation", "invalid ID length")
	}
	var id mousa.PolicyActivationID
	copy(id[:], rawID)
	record, err := getPolicyActivation(ctx, q, id)
	return record, data, err
}

func verifyPolicyActivation(ctx context.Context, q queryRower, want mousa.PolicyActivation, wantData []byte) error {
	got, err := getPolicyActivation(ctx, q, want.ID)
	if err != nil {
		return err
	}
	encoded, err := mousa.EncodePolicyActivation(got)
	if err != nil || !bytes.Equal(encoded, wantData) || !reflect.DeepEqual(got, want) {
		return integrity("verify policy activation", "exact read-back disagrees with write")
	}
	state, err := getPolicyBindingState(ctx, q, want.Namespace, want.ExternalBindingID)
	if err != nil || state.CurrentActivationID != want.ID || !bindingPointersEqual(state.ActiveBindingID, want.ActiveBindingID) {
		return integrity("verify policy activation", "current projection disagrees with transition")
	}
	return nil
}

func (store *Store) GetPolicyBindingState(ctx context.Context, namespace, externalBindingID string) (mousa.PolicyBindingState, error) {
	return getPolicyBindingState(ctx, store.db, namespace, externalBindingID)
}

func getPolicyBindingState(ctx context.Context, q queryRower, namespace, externalBindingID string) (mousa.PolicyBindingState, error) {
	var currentRaw, activeRaw []byte
	if err := q.QueryRowContext(ctx, `SELECT current_activation_id, active_binding_id FROM policy_binding_state WHERE namespace = ? AND external_binding_id = ?`, namespace, externalBindingID).Scan(&currentRaw, &activeRaw); err != nil {
		return mousa.PolicyBindingState{}, readError("get policy binding state", err)
	}
	if len(currentRaw) != 32 || (activeRaw != nil && len(activeRaw) != 32) {
		return mousa.PolicyBindingState{}, integrity("get policy binding state", "invalid ID length")
	}
	var current mousa.PolicyActivationID
	copy(current[:], currentRaw)
	state := mousa.PolicyBindingState{Namespace: namespace, ExternalBindingID: externalBindingID, CurrentActivationID: current}
	if activeRaw != nil {
		var active mousa.PolicyBindingID
		copy(active[:], activeRaw)
		state.ActiveBindingID = &active
	}
	return state, nil
}

func verifyPolicyBindingRecords(ctx context.Context, db *sql.DB) error {
	bindingRows, err := db.QueryContext(ctx, `SELECT id FROM policy_bindings ORDER BY id`)
	if err != nil {
		return startupError("scan policy bindings", err)
	}
	var bindingIDs []mousa.PolicyBindingID
	for bindingRows.Next() {
		var raw []byte
		if err := bindingRows.Scan(&raw); err != nil || len(raw) != 32 {
			bindingRows.Close()
			return integrity("scan policy bindings", "invalid ID")
		}
		var id mousa.PolicyBindingID
		copy(id[:], raw)
		bindingIDs = append(bindingIDs, id)
	}
	if err := bindingRows.Close(); err != nil {
		return startupError("scan policy bindings", err)
	}
	for _, id := range bindingIDs {
		record, err := getPolicyBinding(ctx, db, id)
		if err != nil {
			if IsCode(err, CodeConflict) || IsCode(err, CodeNotFound) {
				return integrity("verify policy bindings", "typed parent is missing")
			}
			return err
		}
		kind, subject := policyScopeProjection(record.Scope)
		if err := verifyPolicyScopeTarget(ctx, db, kind, subject); err != nil {
			return wrap(CodeIntegrity, "verify policy binding", err)
		}
	}

	activationRows, err := db.QueryContext(ctx, `SELECT id FROM policy_activations ORDER BY id`)
	if err != nil {
		return startupError("scan policy activations", err)
	}
	var activationIDs []mousa.PolicyActivationID
	for activationRows.Next() {
		var raw []byte
		if err := activationRows.Scan(&raw); err != nil || len(raw) != 32 {
			activationRows.Close()
			return integrity("scan policy activations", "invalid ID")
		}
		var id mousa.PolicyActivationID
		copy(id[:], raw)
		activationIDs = append(activationIDs, id)
	}
	if err := activationRows.Close(); err != nil {
		return startupError("scan policy activations", err)
	}
	activations := make(map[mousa.PolicyActivationID]mousa.PolicyActivation, len(activationIDs))
	for _, id := range activationIDs {
		record, err := getPolicyActivation(ctx, db, id)
		if err != nil {
			return err
		}
		activations[id] = record
	}
	stateRows, err := db.QueryContext(ctx, `SELECT namespace, external_binding_id FROM policy_binding_state ORDER BY namespace, external_binding_id`)
	if err != nil {
		return startupError("scan policy binding state", err)
	}
	type series struct{ namespace, externalID string }
	var seriesRows []series
	for stateRows.Next() {
		var item series
		if err := stateRows.Scan(&item.namespace, &item.externalID); err != nil {
			stateRows.Close()
			return startupError("scan policy binding state", err)
		}
		seriesRows = append(seriesRows, item)
	}
	if err := stateRows.Close(); err != nil {
		return startupError("scan policy binding state", err)
	}
	visited := make(map[mousa.PolicyActivationID]bool)
	for _, item := range seriesRows {
		state, err := getPolicyBindingState(ctx, db, item.namespace, item.externalID)
		if err != nil {
			return err
		}
		tip, ok := activations[state.CurrentActivationID]
		if !ok || tip.Namespace != item.namespace || tip.ExternalBindingID != item.externalID || !bindingPointersEqual(state.ActiveBindingID, tip.ActiveBindingID) {
			return integrity("verify policy binding state", "tip projection disagrees with activation history")
		}
		current := tip
		for {
			if visited[current.ID] {
				return integrity("verify policy activation history", "cycle, fork, or duplicate chain entry")
			}
			visited[current.ID] = true
			if current.ActiveBindingID != nil {
				binding, err := getPolicyBinding(ctx, db, *current.ActiveBindingID)
				if err != nil || binding.Namespace != item.namespace || binding.ExternalBindingID != item.externalID {
					return integrity("verify policy activation history", "active binding belongs to another series")
				}
			}
			if current.ExpectedPreviousActivationID == nil {
				if current.ActiveBindingID == nil {
					return integrity("verify policy activation history", "root activation must select a binding")
				}
				break
			}
			previous, ok := activations[*current.ExpectedPreviousActivationID]
			if !ok || previous.Namespace != item.namespace || previous.ExternalBindingID != item.externalID {
				return integrity("verify policy activation history", "previous activation is missing or cross-series")
			}
			current = previous
		}
	}
	if len(visited) != len(activations) {
		return integrity("verify policy activation history", fmt.Sprintf("%d activations are outside current chains", len(activations)-len(visited)))
	}
	return nil
}

func policyScopeProjection(scope mousa.PolicyScope) (string, []byte) {
	switch scope.Kind() {
	case mousa.PolicyScopeSource:
		id, _ := scope.SourceID()
		return string(scope.Kind()), id[:]
	case mousa.PolicyScopeObservation:
		id, _ := scope.ObservationID()
		return string(scope.Kind()), id[:]
	case mousa.PolicyScopeArtifact:
		id, _ := scope.ArtifactID()
		return string(scope.Kind()), id[:]
	case mousa.PolicyScopeRepresentation:
		id, _ := scope.RepresentationID()
		return string(scope.Kind()), id[:]
	case mousa.PolicyScopeSegment:
		id, _ := scope.SegmentID()
		return string(scope.Kind()), id[:]
	default:
		return string(scope.Kind()), nil
	}
}

func verifyPolicyScopeTarget(ctx context.Context, q queryRower, kind string, subjectID []byte) error {
	if kind == string(mousa.PolicyScopeDeployment) {
		return nil
	}
	tables := map[string]string{
		string(mousa.PolicyScopeSource): "sources", string(mousa.PolicyScopeObservation): "observations", string(mousa.PolicyScopeArtifact): "artifacts",
		string(mousa.PolicyScopeRepresentation): "representations", string(mousa.PolicyScopeSegment): "segments",
	}
	table, ok := tables[kind]
	if !ok {
		return wrap(CodeInvalidRecord, "verify policy scope", errors.New("unknown policy scope"))
	}
	var exists int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE id = ?`, subjectID).Scan(&exists); err != nil {
		return classify("verify policy scope", err)
	}
	if exists != 1 {
		return wrap(CodeConflict, "verify policy scope", errors.New("policy scope target does not exist"))
	}
	return nil
}

func verifyPolicyBindingParents(ctx context.Context, q queryer, definitionID mousa.PolicyDefinitionID, kind string, subjectID []byte) error {
	if _, err := getPolicyDefinition(ctx, q, definitionID); err != nil {
		if IsCode(err, CodeNotFound) {
			return wrap(CodeConflict, "verify policy binding parent", errors.New("policy definition does not exist"))
		}
		return wrap(CodeIntegrity, "verify policy binding parent", err)
	}
	if kind == string(mousa.PolicyScopeDeployment) {
		return nil
	}
	var err error
	switch kind {
	case string(mousa.PolicyScopeSource):
		var id mousa.SourceID
		copy(id[:], subjectID)
		_, err = getSource(ctx, q, id)
	case string(mousa.PolicyScopeObservation):
		var id mousa.ObservationID
		copy(id[:], subjectID)
		_, err = getObservation(ctx, q, id)
	case string(mousa.PolicyScopeArtifact):
		var id mousa.ArtifactID
		copy(id[:], subjectID)
		_, err = getArtifact(ctx, q, id)
	case string(mousa.PolicyScopeRepresentation):
		var id mousa.RepresentationID
		copy(id[:], subjectID)
		_, err = getRepresentation(ctx, q, id)
	case string(mousa.PolicyScopeSegment):
		var id mousa.SegmentID
		copy(id[:], subjectID)
		_, err = getSegment(ctx, q, id)
	default:
		return wrap(CodeInvalidRecord, "verify policy binding parent", errors.New("unknown policy scope"))
	}
	if err == nil {
		return nil
	}
	if IsCode(err, CodeNotFound) {
		return wrap(CodeConflict, "verify policy binding parent", errors.New("policy scope target does not exist"))
	}
	return wrap(CodeIntegrity, "verify policy binding parent", err)
}

func activationIDBytes(id *mousa.PolicyActivationID) []byte {
	if id == nil {
		return nil
	}
	return id[:]
}
func bindingIDBytes(id *mousa.PolicyBindingID) []byte {
	if id == nil {
		return nil
	}
	return id[:]
}
func nullableBytesEqual(left, right []byte) bool {
	return (left == nil && right == nil) || bytes.Equal(left, right)
}
func bindingPointersEqual(left, right *mousa.PolicyBindingID) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

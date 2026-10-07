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

// maxSupersessionActivationBytes bounds one stored canonical activation and mirrors the
// record_json check in migration 0013.
const maxSupersessionActivationBytes = 64 << 10

// ApplySupersessionActivation appends one recorded activation transition and updates the
// source-keyed current projection in the store's single writer transaction.
//
// One transaction verifies the source, the selected declaration and its canonical provenance, the
// stored current chain, and the caller's expectation before it appends anything:
//
//   - A first activation must expect no predecessor and must select a declaration.
//   - A replacement or deactivation must name the exact event that is current for that source.
//   - A reactivation is just another replacement whose expected predecessor is the deactivation
//     event that is current.
//   - A transition that would not change the selected declaration is redundant and is refused.
//
// An exact retry (identical identity and canonical bytes) succeeds without appending a row only
// while that event is still the current state. A stale expectation, a competing branch and a retry
// of an older event after the state advanced all return CodeConflict without changing any row, and
// no historical event is ever overwritten, repaired or deleted.
//
// The transition records administration state only. It selects no evidence, grants no access,
// moves no item pointer, re-ingests no successor text and mutates no declaration.
func (store *Store) ApplySupersessionActivation(ctx context.Context, activation mousa.SupersessionActivation) error {
	if err := store.requireWritable("apply supersession activation"); err != nil {
		return err
	}
	data, err := mousa.EncodeSupersessionActivation(activation)
	if err != nil {
		return wrap(CodeInvalidRecord, "apply supersession activation", err)
	}
	if len(data) > maxSupersessionActivationBytes {
		return wrap(CodeResourceLimit, "apply supersession activation", fmt.Errorf("canonical activation size %d exceeds %d", len(data), maxSupersessionActivationBytes))
	}
	return store.writeImmediate(ctx, "apply supersession activation", func(conn *sql.Conn) error {
		return applySupersessionActivation(ctx, conn, activation, data)
	})
}

func applySupersessionActivation(ctx context.Context, conn *sql.Conn, activation mousa.SupersessionActivation, data []byte) error {
	if _, err := getSource(ctx, conn, activation.SourceID); err != nil {
		return err
	}
	if activation.DeclarationID != nil {
		declaration, err := readSupersessionDeclaration(ctx, conn, *activation.DeclarationID)
		if err != nil {
			if IsCode(err, CodeNotFound) {
				return wrap(CodeConflict, "apply supersession activation", errors.New("selected declaration does not exist"))
			}
			return err
		}
		if declaration.SourceID != activation.SourceID {
			return wrap(CodeConflict, "apply supersession activation", errors.New("selected declaration belongs to another source"))
		}
	}

	existing, existingData, err := findSupersessionActivation(ctx, conn, activation.ID)
	if err == nil {
		if !bytes.Equal(existingData, data) || !reflect.DeepEqual(existing, activation) {
			return wrap(CodeConflict, "apply supersession activation", errors.New("activation identity already has different bytes"))
		}
		state, stateErr := readSupersessionActivationStateRow(ctx, conn, activation.SourceID)
		if stateErr != nil {
			if IsCode(stateErr, CodeNotFound) {
				return integrity("apply supersession activation", "stored activation has no current state")
			}
			return stateErr
		}
		if state.CurrentActivationID != activation.ID || !supersessionDeclarationPointersEqual(state.ActiveDeclarationID, activation.DeclarationID) {
			return wrap(CodeConflict, "apply supersession activation", errors.New("identical activation is no longer current"))
		}
		return verifySupersessionActivation(ctx, conn, activation, data)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return classify("apply supersession activation", err)
	}

	state, stateErr := readSupersessionActivationStateRow(ctx, conn, activation.SourceID)
	if stateErr != nil && !IsCode(stateErr, CodeNotFound) {
		return stateErr
	}
	if IsCode(stateErr, CodeNotFound) {
		var exists int
		if checkErr := conn.QueryRowContext(ctx, `SELECT 1 FROM supersession_activations WHERE source_id = ? LIMIT 1`, activation.SourceID[:]).Scan(&exists); checkErr == nil {
			return integrity("apply supersession activation", "activation history has no current projection")
		} else if !errors.Is(checkErr, sql.ErrNoRows) {
			return classify("get supersession activation history", checkErr)
		}
		if activation.ExpectedPreviousActivationID != nil {
			return wrap(CodeConflict, "apply supersession activation", errors.New("expected previous activation does not exist"))
		}
	} else {
		if activation.ExpectedPreviousActivationID == nil {
			return wrap(CodeConflict, "apply supersession activation", errors.New("initial transition follows existing activation history"))
		}
		if *activation.ExpectedPreviousActivationID != state.CurrentActivationID {
			return wrap(CodeConflict, "apply supersession activation", errors.New("expected previous activation is stale"))
		}
		tip, err := readSupersessionActivationRow(ctx, conn, state.CurrentActivationID)
		if err != nil {
			return err
		}
		if tip.SourceID != activation.SourceID {
			return integrity("apply supersession activation", "current activation belongs to another source")
		}
		if err := verifySupersessionStateAgainstTip(ctx, conn, activation.SourceID, state, tip, map[mousa.SupersessionActivationID]bool{}); err != nil {
			return err
		}
		if supersessionDeclarationPointersEqual(state.ActiveDeclarationID, activation.DeclarationID) {
			return wrap(CodeConflict, "apply supersession activation", errors.New("new transition does not change the active declaration"))
		}
	}

	result, err := conn.ExecContext(ctx, `INSERT INTO supersession_activations(id, source_id, expected_previous_activation_id, declaration_id, record_json) VALUES(?, ?, ?, ?, ?)`,
		activation.ID[:], activation.SourceID[:], supersessionActivationIDBytes(activation.ExpectedPreviousActivationID), supersessionDeclarationIDBytes(activation.DeclarationID), data)
	if err != nil {
		if sqliteConstraint(err) {
			return wrap(CodeConflict, "apply supersession activation", err)
		}
		return classify("apply supersession activation", err)
	}
	if err := oneRow(result); err != nil {
		return wrap(CodeIntegrity, "apply supersession activation", err)
	}
	result, err = conn.ExecContext(ctx, `INSERT INTO supersession_activation_state(source_id, current_activation_id, active_declaration_id) VALUES(?, ?, ?) ON CONFLICT(source_id) DO UPDATE SET current_activation_id = excluded.current_activation_id, active_declaration_id = excluded.active_declaration_id`,
		activation.SourceID[:], activation.ID[:], supersessionDeclarationIDBytes(activation.DeclarationID))
	if err != nil {
		return classify("apply supersession activation", err)
	}
	if err := oneRow(result); err != nil {
		return wrap(CodeIntegrity, "apply supersession activation", err)
	}
	return verifySupersessionActivation(ctx, conn, activation, data)
}

// GetSupersessionActivationState returns the verified current activation state for one source, or
// CodeNotFound when that source has no activation history.
//
// Verification covers the state projection, the latest event's canonical bytes, identity and
// projections, its same-source declaration and predecessor references, and the whole predecessor
// chain back to one root within a single read snapshot. The state is never reconstructed or
// repaired, and the read exposes no document text.
//
// The method only supplies the read transaction and the commit: verifySupersessionActivationState
// performs the verification, so a caller that already holds a transaction reaches the same checks
// through readSupersessionConsultation instead of opening a second one.
func (store *Store) GetSupersessionActivationState(ctx context.Context, sourceID mousa.SourceID) (mousa.SupersessionActivationState, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return mousa.SupersessionActivationState{}, classify("begin supersession activation state read", err)
	}
	defer tx.Rollback()
	state, hasHistory, err := verifySupersessionActivationState(ctx, tx, sourceID)
	if err != nil {
		return mousa.SupersessionActivationState{}, err
	}
	if !hasHistory {
		// A verified absence reports the same not-found the missing state row produced, so the
		// command's code and message are unchanged by the extraction.
		return mousa.SupersessionActivationState{}, readError("get supersession activation state", sql.ErrNoRows)
	}
	if err := tx.Commit(); err != nil {
		return mousa.SupersessionActivationState{}, classify("finish supersession activation state read", err)
	}
	return state, nil
}

// verifySupersessionActivationState verifies one source's current activation state through the
// supplied queryer and reports whether that source has any activation history.
//
// Every read goes through the supplied handle, so a caller that already holds a transaction consults
// exactly the snapshot of that transaction: the state projection, the event it names, the event's
// canonical bytes and projections, its declaration reference and the whole predecessor chain all come
// from one snapshot, and no second transaction or public store read is opened.
//
// A source with no activation history reports hasHistory false with no error, so a caller that
// records a verified absence reads no error code as one. Activation history whose current projection
// is missing is an integrity failure instead, and no state is reconstructed or repaired.
func verifySupersessionActivationState(ctx context.Context, q queryer, sourceID mousa.SourceID) (mousa.SupersessionActivationState, bool, error) {
	state, err := readSupersessionActivationStateRow(ctx, q, sourceID)
	if err != nil {
		if !IsCode(err, CodeNotFound) {
			return mousa.SupersessionActivationState{}, false, err
		}
		var exists int
		if checkErr := q.QueryRowContext(ctx, `SELECT 1 FROM supersession_activations WHERE source_id = ? LIMIT 1`, sourceID[:]).Scan(&exists); checkErr == nil {
			return mousa.SupersessionActivationState{}, false, integrity("get supersession activation state", "activation history has no current projection")
		} else if !errors.Is(checkErr, sql.ErrNoRows) {
			return mousa.SupersessionActivationState{}, false, classify("get supersession activation history", checkErr)
		}
		return mousa.SupersessionActivationState{}, false, nil
	}
	tip, err := readSupersessionActivationRow(ctx, q, state.CurrentActivationID)
	if err != nil {
		return mousa.SupersessionActivationState{}, true, err
	}
	if err := verifySupersessionStateAgainstTip(ctx, q, sourceID, state, tip, map[mousa.SupersessionActivationID]bool{}); err != nil {
		return mousa.SupersessionActivationState{}, true, err
	}
	return state, true, nil
}

// supersessionConsultation is one transaction-local consultation of a source's supersession records,
// in the shape mousa.BuildSupersessionSelection accepts: the verified current activation state when
// the source has activation history, and the declaration that state selects when it selects one. A nil
// state is a verified absence of history; a state naming no declaration is a deactivation, and a state
// naming one always carries that declaration's verified canonical record.
type supersessionConsultation struct {
	state       *mousa.SupersessionActivationState
	declaration *mousa.SupersessionDeclaration
}

// readSupersessionConsultation returns the verified supersession records one writer-transaction
// decision must consult, reading every provenance fact through the supplied queryer. It therefore uses
// the caller's existing snapshot for the activation state, its predecessor chain and the selected
// declaration, and a concurrent transition on another connection cannot mix into that snapshot.
//
// A source with no activation history reports a nil state and no declaration. A deactivation reports
// the consulted event with no declaration. A selected declaration must belong to the same source and
// pass the same canonical provenance verification a direct declaration read performs. Missing or
// inconsistent recorded state stays an integrity failure, so this reader never manufactures a
// no-history result, never repairs a record and writes nothing.
func readSupersessionConsultation(ctx context.Context, q queryer, sourceID mousa.SourceID) (supersessionConsultation, error) {
	state, hasHistory, err := verifySupersessionActivationState(ctx, q, sourceID)
	if err != nil {
		return supersessionConsultation{}, err
	}
	if !hasHistory {
		return supersessionConsultation{}, nil
	}
	consultation := supersessionConsultation{state: &state}
	if state.ActiveDeclarationID == nil {
		return consultation, nil
	}
	// The event row already required this declaration to exist in the same snapshot, so this read
	// supplies its value and not its existence proof. The failure mapping still matters: a caller of
	// this reader reads a nil state as the verified absence of history, so a not-found must never
	// reach it as a successful absence.
	declaration, err := readSupersessionDeclaration(ctx, q, *state.ActiveDeclarationID)
	if err != nil {
		if IsCode(err, CodeNotFound) {
			return supersessionConsultation{}, integrity("consult supersession activation", "selected declaration is missing")
		}
		return supersessionConsultation{}, err
	}
	if declaration.SourceID != state.SourceID {
		return supersessionConsultation{}, integrity("consult supersession activation", "selected declaration belongs to another source")
	}
	consultation.declaration = &declaration
	return consultation, nil
}

// GetSupersessionActivation returns one stored activation event after verifying its canonical
// encoding, identity, every projected column, its same-source references and the chain that leads
// to it. A historical event stays readable after the state advances, because history is never
// rewritten.
func (store *Store) GetSupersessionActivation(ctx context.Context, activationID mousa.SupersessionActivationID) (mousa.SupersessionActivation, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return mousa.SupersessionActivation{}, classify("begin supersession activation read", err)
	}
	defer tx.Rollback()
	activation, err := readSupersessionActivationRow(ctx, tx, activationID)
	if err != nil {
		return mousa.SupersessionActivation{}, err
	}
	if err := walkSupersessionActivationChain(ctx, tx, map[mousa.SupersessionActivationID]bool{}, activation); err != nil {
		return mousa.SupersessionActivation{}, err
	}
	if err := tx.Commit(); err != nil {
		return mousa.SupersessionActivation{}, classify("finish supersession activation read", err)
	}
	return activation, nil
}

// readSupersessionActivationRow reads one event row and verifies it against its canonical record:
// the record must decode as canonical bytes, carry the requested identity, agree with every
// projected column, reference a declaration of the same source, and name a predecessor event of
// the same source. Predecessor canonical bytes are verified by the chain walk, which reads every
// event on the path.
func readSupersessionActivationRow(ctx context.Context, q queryer, activationID mousa.SupersessionActivationID) (mousa.SupersessionActivation, error) {
	var rawSource, rawPrevious, rawDeclaration, data []byte
	if err := q.QueryRowContext(ctx, `SELECT source_id, expected_previous_activation_id, declaration_id, record_json FROM supersession_activations WHERE id = ?`, activationID[:]).
		Scan(&rawSource, &rawPrevious, &rawDeclaration, &data); err != nil {
		return mousa.SupersessionActivation{}, readError("get supersession activation", err)
	}
	record, err := decodeCanonical(data, mousa.DecodeSupersessionActivation, mousa.EncodeSupersessionActivation)
	if err != nil {
		return mousa.SupersessionActivation{}, wrap(CodeIntegrity, "get supersession activation", err)
	}
	if len(rawSource) != len(mousa.SourceID{}) ||
		(rawPrevious != nil && len(rawPrevious) != len(mousa.SupersessionActivationID{})) ||
		(rawDeclaration != nil && len(rawDeclaration) != len(mousa.SupersessionDeclarationID{})) {
		return mousa.SupersessionActivation{}, integrity("get supersession activation", "invalid identity projection")
	}
	var sourceID mousa.SourceID
	copy(sourceID[:], rawSource)
	if record.ID != activationID || record.SourceID != sourceID ||
		!supersessionActivationProjectionMatches(rawPrevious, record.ExpectedPreviousActivationID) ||
		!supersessionDeclarationProjectionMatches(rawDeclaration, record.DeclarationID) {
		return mousa.SupersessionActivation{}, integrity("get supersession activation", "relational projection disagrees with record")
	}
	if record.DeclarationID != nil {
		declaration, err := readSupersessionDeclaration(ctx, q, *record.DeclarationID)
		if err != nil {
			if IsCode(err, CodeNotFound) {
				return mousa.SupersessionActivation{}, integrity("get supersession activation", "selected declaration is missing")
			}
			return mousa.SupersessionActivation{}, err
		}
		if declaration.SourceID != record.SourceID {
			return mousa.SupersessionActivation{}, integrity("get supersession activation", "selected declaration belongs to another source")
		}
	}
	if record.ExpectedPreviousActivationID != nil {
		var rawPreviousSource []byte
		if err := q.QueryRowContext(ctx, `SELECT source_id FROM supersession_activations WHERE id = ?`, record.ExpectedPreviousActivationID[:]).Scan(&rawPreviousSource); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return mousa.SupersessionActivation{}, integrity("get supersession activation", "predecessor activation is missing")
			}
			return mousa.SupersessionActivation{}, classify("get supersession activation", err)
		}
		if !bytes.Equal(rawPreviousSource, sourceID[:]) {
			return mousa.SupersessionActivation{}, integrity("get supersession activation", "predecessor activation belongs to another source")
		}
	}
	return record, nil
}

// walkSupersessionActivationChain verifies the chain that ends at the caller-verified tip event
// through its single root. Every event must verify as canonical, and the chain must stay within one
// source and terminate without repeating an event. The shared visited set lets a caller that walks
// several chains detect an event reachable from more than one of them.
func walkSupersessionActivationChain(ctx context.Context, q queryer, visited map[mousa.SupersessionActivationID]bool, tip mousa.SupersessionActivation) error {
	current := tip
	for {
		if visited[current.ID] {
			return integrity("verify supersession activation chain", "cycle, fork, or duplicate chain entry")
		}
		visited[current.ID] = true
		if current.SourceID != tip.SourceID {
			return integrity("verify supersession activation chain", "activation history mixes sources")
		}
		if current.ExpectedPreviousActivationID == nil {
			return nil
		}
		if *current.ExpectedPreviousActivationID == (mousa.SupersessionActivationID{}) {
			return integrity("verify supersession activation chain", "predecessor identity is zero")
		}
		previous, err := readSupersessionActivationRow(ctx, q, *current.ExpectedPreviousActivationID)
		if err != nil {
			return err
		}
		current = previous
	}
}

// readSupersessionActivationStateRow reads the source-keyed current projection without following
// the chain that it points at.
func readSupersessionActivationStateRow(ctx context.Context, q queryRower, sourceID mousa.SourceID) (mousa.SupersessionActivationState, error) {
	var currentRaw, activeRaw []byte
	if err := q.QueryRowContext(ctx, `SELECT current_activation_id, active_declaration_id FROM supersession_activation_state WHERE source_id = ?`, sourceID[:]).Scan(&currentRaw, &activeRaw); err != nil {
		return mousa.SupersessionActivationState{}, readError("get supersession activation state", err)
	}
	if len(currentRaw) != len(mousa.SupersessionActivationID{}) || (activeRaw != nil && len(activeRaw) != len(mousa.SupersessionDeclarationID{})) {
		return mousa.SupersessionActivationState{}, integrity("get supersession activation state", "invalid identity projection")
	}
	var current mousa.SupersessionActivationID
	copy(current[:], currentRaw)
	state := mousa.SupersessionActivationState{SourceID: sourceID, CurrentActivationID: current}
	if activeRaw != nil {
		var active mousa.SupersessionDeclarationID
		copy(active[:], activeRaw)
		state.ActiveDeclarationID = &active
	}
	if state.CurrentActivationID == (mousa.SupersessionActivationID{}) {
		return mousa.SupersessionActivationState{}, integrity("get supersession activation state", "current activation identity is zero")
	}
	return state, nil
}

// verifySupersessionStateAgainstTip requires one current projection to agree with the latest
// activation event it names and with the whole chain behind that event.
func verifySupersessionStateAgainstTip(ctx context.Context, q queryer, sourceID mousa.SourceID, state mousa.SupersessionActivationState, tip mousa.SupersessionActivation, visited map[mousa.SupersessionActivationID]bool) error {
	if tip.ID != state.CurrentActivationID || tip.SourceID != sourceID {
		return integrity("verify supersession current state", "current activation belongs to another source or event")
	}
	if !supersessionDeclarationPointersEqual(state.ActiveDeclarationID, tip.DeclarationID) {
		return integrity("verify supersession current state", "current projection disagrees with the latest activation")
	}
	// A current projection must name the end of a chain: an event that a later transition expects
	// as its predecessor is not the current event, whatever the projection claims.
	var successors int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM supersession_activations WHERE expected_previous_activation_id = ?`, tip.ID[:]).Scan(&successors); err != nil {
		return classify("verify supersession current state", err)
	}
	if successors != 0 {
		return integrity("verify supersession current state", "a later activation succeeds the current one")
	}
	return walkSupersessionActivationChain(ctx, q, visited, tip)
}

// verifySupersessionActivation requires the stored event and the current projection to match an
// exact write, including canonical bytes and the chain that leads to the event.
func verifySupersessionActivation(ctx context.Context, q queryer, want mousa.SupersessionActivation, wantData []byte) error {
	got, err := readSupersessionActivationRow(ctx, q, want.ID)
	if err != nil {
		return err
	}
	encoded, err := mousa.EncodeSupersessionActivation(got)
	if err != nil || !bytes.Equal(encoded, wantData) || !reflect.DeepEqual(got, want) {
		return integrity("verify supersession activation", "exact read-back disagrees with write")
	}
	state, err := readSupersessionActivationStateRow(ctx, q, want.SourceID)
	if err != nil {
		return integrity("verify supersession activation", "current projection is unreadable")
	}
	if state.CurrentActivationID != want.ID || !supersessionDeclarationPointersEqual(state.ActiveDeclarationID, want.DeclarationID) {
		return integrity("verify supersession activation", "current projection disagrees with transition")
	}
	return verifySupersessionStateAgainstTip(ctx, q, want.SourceID, state, got, map[mousa.SupersessionActivationID]bool{})
}

// findSupersessionActivation reads one stored event and its raw payload by identity, so a retry
// cannot silently accept a different record under the same identity.
func findSupersessionActivation(ctx context.Context, q queryer, activationID mousa.SupersessionActivationID) (mousa.SupersessionActivation, []byte, error) {
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT record_json FROM supersession_activations WHERE id = ?`, activationID[:]).Scan(&data); err != nil {
		return mousa.SupersessionActivation{}, nil, err
	}
	record, err := readSupersessionActivationRow(ctx, q, activationID)
	return record, data, err
}

// verifySupersessionActivationRecords verifies every stored activation event, every current
// projection and every chain as part of the startup integrity path at the current schema version.
// An event that is missing from its chain, a chain that forks or cycles, a projection that
// disagrees with its tip and an event outside every current chain all fail closed; nothing is
// reconstructed or repaired.
func verifySupersessionActivationRecords(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM supersession_activations ORDER BY id`)
	if err != nil {
		return startupError("scan supersession activations", err)
	}
	var activationIDs []mousa.SupersessionActivationID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return startupError("scan supersession activations", err)
		}
		if len(raw) != len(mousa.SupersessionActivationID{}) {
			rows.Close()
			return integrity("scan supersession activations", "invalid ID length")
		}
		var id mousa.SupersessionActivationID
		copy(id[:], raw)
		activationIDs = append(activationIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return startupError("scan supersession activations", err)
	}
	if err := rows.Close(); err != nil {
		return startupError("scan supersession activations", err)
	}
	for _, id := range activationIDs {
		if _, err := readSupersessionActivationRow(ctx, db, id); err != nil {
			return err
		}
	}

	stateRows, err := db.QueryContext(ctx, `SELECT source_id FROM supersession_activation_state ORDER BY source_id`)
	if err != nil {
		return startupError("scan supersession activation state", err)
	}
	var sourceIDs []mousa.SourceID
	for stateRows.Next() {
		var raw []byte
		if err := stateRows.Scan(&raw); err != nil {
			stateRows.Close()
			return startupError("scan supersession activation state", err)
		}
		if len(raw) != len(mousa.SourceID{}) {
			stateRows.Close()
			return integrity("scan supersession activation state", "invalid ID length")
		}
		var sourceID mousa.SourceID
		copy(sourceID[:], raw)
		sourceIDs = append(sourceIDs, sourceID)
	}
	if err := stateRows.Err(); err != nil {
		stateRows.Close()
		return startupError("scan supersession activation state", err)
	}
	if err := stateRows.Close(); err != nil {
		return startupError("scan supersession activation state", err)
	}

	visited := make(map[mousa.SupersessionActivationID]bool, len(activationIDs))
	for _, sourceID := range sourceIDs {
		state, err := readSupersessionActivationStateRow(ctx, db, sourceID)
		if err != nil {
			return err
		}
		tip, err := readSupersessionActivationRow(ctx, db, state.CurrentActivationID)
		if err != nil {
			return err
		}
		if err := verifySupersessionStateAgainstTip(ctx, db, sourceID, state, tip, visited); err != nil {
			return err
		}
	}
	if len(visited) != len(activationIDs) {
		return integrity("verify supersession activation history", fmt.Sprintf("%d activations are outside current chains", len(activationIDs)-len(visited)))
	}
	return nil
}

func supersessionActivationIDBytes(id *mousa.SupersessionActivationID) []byte {
	if id == nil {
		return nil
	}
	return id[:]
}

func supersessionDeclarationIDBytes(id *mousa.SupersessionDeclarationID) []byte {
	if id == nil {
		return nil
	}
	return id[:]
}

func supersessionActivationProjectionMatches(raw []byte, id *mousa.SupersessionActivationID) bool {
	return (raw == nil && id == nil) || (raw != nil && id != nil && bytes.Equal(raw, id[:]))
}

func supersessionDeclarationProjectionMatches(raw []byte, id *mousa.SupersessionDeclarationID) bool {
	return (raw == nil && id == nil) || (raw != nil && id != nil && bytes.Equal(raw, id[:]))
}

func supersessionDeclarationPointersEqual(left, right *mousa.SupersessionDeclarationID) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

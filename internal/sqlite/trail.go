package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"reflect"

	"github.com/graydeon/mousa/internal/mousa"
)

const maxTrailCandidates = 100

// TracedLexicalResult is one decision-gated retrieval that has been traced: the immutable trail,
// the verified candidates of the decision Source, and the packed selection.
type TracedLexicalResult struct {
	Decision   mousa.PolicyDecision
	Trail      mousa.SourceTrail
	Candidates []mousa.VerifiedLexicalCandidate
}

// TraceEnforcedLexical verifies the exact stored decision for the request, reads the verified
// Source-scoped candidates when the decision allows, packs them within budgetBytes, records one
// immutable Source Trail with its ordered candidate rows, and reads it back canonically, all inside
// one writer transaction. A stored deny decision is data, not an error: it produces a trail with no
// candidates and an empty selection. Tracing writes, so a read-only store cannot trace.
func (store *Store) TraceEnforcedLexical(ctx context.Context, request mousa.PolicyEvaluationRequest, expression string, limit int, budgetBytes uint64, packingPolicy string) (TracedLexicalResult, error) {
	return store.traceLexical(ctx, request, expression, limit, budgetBytes, false, packingPolicy)
}

// EvaluateAndTraceLexical evaluates current policy, retrieves and packs verified
// candidates, and stores the decision and Source Trail in one writer transaction.
// It requires a new request identity; it never reuses a historical decision.
func (store *Store) EvaluateAndTraceLexical(ctx context.Context, request mousa.PolicyEvaluationRequest, expression string, limit int, budgetBytes uint64, packingPolicy string) (TracedLexicalResult, error) {
	return store.traceLexical(ctx, request, expression, limit, budgetBytes, true, packingPolicy)
}

func (store *Store) traceLexical(ctx context.Context, request mousa.PolicyEvaluationRequest, expression string, limit int, budgetBytes uint64, evaluateCurrent bool, packingPolicy string) (TracedLexicalResult, error) {
	if packingPolicy != mousa.PackingOriginal && packingPolicy != mousa.PackingExactV1 {
		return TracedLexicalResult{}, wrap(CodeInvalidQuery, "trace enforced lexical", errors.New("packing policy must be original or exact-v1"))
	}
	if err := store.requireWritable("trace enforced lexical"); err != nil {
		return TracedLexicalResult{}, err
	}
	if err := request.Validate(); err != nil {
		return TracedLexicalResult{}, wrap(CodeInvalidRecord, "trace enforced lexical", err)
	}
	if err := validateLexicalQuery(expression, limit); err != nil {
		return TracedLexicalResult{}, err
	}
	if budgetBytes == 0 {
		return TracedLexicalResult{}, wrap(CodeInvalidQuery, "trace enforced lexical", errors.New("pack budget must be positive"))
	}
	var result TracedLexicalResult
	err := store.writeImmediate(ctx, "trace enforced lexical", func(conn *sql.Conn) error {
		if evaluateCurrent {
			if _, err := evaluateSourceRetrieval(ctx, conn, request, false); err != nil {
				return err
			}
		}
		enforced, err := searchEnforcedLexical(ctx, conn, request, expression, limit)
		if err != nil {
			return err
		}
		trail, err := mousa.NewSourceTrail(request, enforced.Decision, expression, enforced.Candidates, budgetBytes, packingPolicy)
		if err != nil {
			return wrap(CodeInvalidRecord, "trace enforced lexical", err)
		}
		if err := insertSourceTrail(ctx, conn, trail); err != nil {
			return err
		}
		written, err := getSourceTrail(ctx, conn, trail.ID)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(written, trail) {
			return integrity("trace enforced lexical", "exact read-back disagrees with write")
		}
		result = TracedLexicalResult{Decision: enforced.Decision, Trail: written, Candidates: enforced.Candidates}
		return nil
	})
	if err != nil {
		return TracedLexicalResult{}, err
	}
	return result, nil
}

// GetSourceTrail returns one stored trail with verified relational projections, ordered candidate
// rows, and parent decision agreement in one read snapshot.
func (store *Store) GetSourceTrail(ctx context.Context, id mousa.SourceTrailID) (mousa.SourceTrail, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return mousa.SourceTrail{}, ctxErr
		}
		return mousa.SourceTrail{}, classify("begin source trail read", err)
	}
	defer tx.Rollback()
	trail, err := getSourceTrail(ctx, tx, id)
	if err != nil {
		return mousa.SourceTrail{}, err
	}
	if err := tx.Commit(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return mousa.SourceTrail{}, ctxErr
		}
		return mousa.SourceTrail{}, classify("finish source trail read", err)
	}
	return trail, nil
}

func insertSourceTrail(ctx context.Context, conn *sql.Conn, trail mousa.SourceTrail) error {
	data, err := mousa.EncodeSourceTrail(trail)
	if err != nil {
		return wrap(CodeInvalidRecord, "insert source trail", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	decisionID, err := mousa.ParsePolicyDecisionID(trail.DecisionID)
	if err != nil {
		return wrap(CodeInvalidRecord, "insert source trail", err)
	}
	packetID, err := mousa.ParseContextPacketID(trail.PacketID)
	if err != nil {
		return wrap(CodeInvalidRecord, "insert source trail", err)
	}
	result, err := conn.ExecContext(ctx, `INSERT INTO source_trails(id, decision_id, outcome, packet_id, record_json) VALUES(?, ?, ?, ?, ?)`, trail.ID[:], decisionID[:], trail.Outcome, packetID[:], data)
	if err != nil {
		if sqliteConstraint(err) {
			var existing []byte
			if queryErr := conn.QueryRowContext(ctx, `SELECT record_json FROM source_trails WHERE id = ?`, trail.ID[:]).Scan(&existing); queryErr == nil {
				if bytes.Equal(existing, data) {
					return verifySourceTrailCandidates(ctx, conn, trail)
				}
				return wrap(CodeConflict, "insert source trail", err)
			}
			if sqliteForeignKey(err) {
				return integrity("insert source trail", "typed parent is missing")
			}
		}
		return classify("insert source trail", err)
	}
	if err := oneRow(result); err != nil {
		return wrap(CodeIntegrity, "insert source trail", err)
	}
	return insertSourceTrailCandidates(ctx, conn, trail)
}

func insertSourceTrailCandidates(ctx context.Context, conn *sql.Conn, trail mousa.SourceTrail) error {
	for ordinal, candidate := range trail.Candidates {
		selected := 0
		if candidate.Selected {
			selected = 1
		}
		result, err := conn.ExecContext(ctx, `INSERT INTO source_trail_candidates(trail_id, ordinal, segment_id, final_rank, disposition, selected) VALUES(?, ?, ?, ?, ?, ?)`, trail.ID[:], ordinal, candidate.SegmentID[:], candidate.FinalRank, string(candidate.Disposition), selected)
		if err != nil {
			if sqliteForeignKey(err) {
				return integrity("insert source trail candidate", "typed segment parent is missing")
			}
			return classify("insert source trail candidate", err)
		}
		if err := oneRow(result); err != nil {
			return wrap(CodeIntegrity, "insert source trail candidate", err)
		}
	}
	return nil
}

func verifySourceTrailCandidates(ctx context.Context, conn *sql.Conn, trail mousa.SourceTrail) error {
	rows, err := conn.QueryContext(ctx, `SELECT ordinal, segment_id, final_rank, disposition, selected FROM source_trail_candidates WHERE trail_id = ? ORDER BY ordinal`, trail.ID[:])
	if err != nil {
		return classify("verify source trail candidates", err)
	}
	defer rows.Close()
	for ordinal, want := range trail.Candidates {
		if !rows.Next() {
			return integrity("verify source trail candidates", "missing ordered candidate row")
		}
		var gotOrdinal, finalRank, selected int
		var segmentID []byte
		var disposition string
		if err := rows.Scan(&gotOrdinal, &segmentID, &finalRank, &disposition, &selected); err != nil {
			return classify("verify source trail candidates", err)
		}
		wantSelected := 0
		if want.Selected {
			wantSelected = 1
		}
		if gotOrdinal != ordinal || !bytes.Equal(segmentID, want.SegmentID[:]) || finalRank != want.FinalRank || disposition != string(want.Disposition) || selected != wantSelected {
			return integrity("verify source trail candidates", "ordered candidate projection disagrees with record")
		}
	}
	if rows.Next() {
		return integrity("verify source trail candidates", "extra ordered candidate row")
	}
	if err := rows.Err(); err != nil {
		return classify("verify source trail candidates", err)
	}
	return nil
}

// getSourceTrail requires a transaction snapshot, including for ancestry reuse.
func getSourceTrail(ctx context.Context, q queryer, id mousa.SourceTrailID) (mousa.SourceTrail, error) {
	var decisionID, packetID []byte
	var outcome string
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT decision_id, outcome, packet_id, record_json FROM source_trails WHERE id = ?`, id[:]).Scan(&decisionID, &outcome, &packetID, &data); err != nil {
		return mousa.SourceTrail{}, readError("get source trail", err)
	}
	trail, err := decodeCanonical(data, mousa.DecodeSourceTrail, mousa.EncodeSourceTrail)
	if err != nil {
		return mousa.SourceTrail{}, wrap(CodeIntegrity, "get source trail", err)
	}
	wantDecision, err := mousa.ParsePolicyDecisionID(trail.DecisionID)
	if err != nil {
		return mousa.SourceTrail{}, wrap(CodeIntegrity, "get source trail", err)
	}
	wantPacket, err := mousa.ParseContextPacketID(trail.PacketID)
	if err != nil {
		return mousa.SourceTrail{}, wrap(CodeIntegrity, "get source trail", err)
	}
	if trail.ID != id || !bytes.Equal(decisionID, wantDecision[:]) || outcome != trail.Outcome || !bytes.Equal(packetID, wantPacket[:]) {
		return mousa.SourceTrail{}, integrity("get source trail", "relational projection disagrees with record")
	}
	decision, err := getPolicyDecision(ctx, q, wantDecision)
	if err != nil {
		if IsCode(err, CodeNotFound) {
			return mousa.SourceTrail{}, integrity("get source trail", "typed decision parent is missing")
		}
		return mousa.SourceTrail{}, err
	}
	if decision.ID.String() != trail.DecisionID || decision.Request.ID.String() != trail.RequestID || string(decision.Outcome) != trail.Outcome {
		return mousa.SourceTrail{}, integrity("get source trail", "parent decision disagrees with record")
	}
	rows, err := q.QueryContext(ctx, `SELECT ordinal, segment_id, final_rank, disposition, selected FROM source_trail_candidates WHERE trail_id = ? ORDER BY ordinal`, id[:])
	if err != nil {
		return mousa.SourceTrail{}, classify("get source trail candidates", err)
	}
	defer rows.Close()
	for ordinal, want := range trail.Candidates {
		if !rows.Next() {
			return mousa.SourceTrail{}, integrity("get source trail candidates", "missing ordered candidate row")
		}
		var gotOrdinal, finalRank, selected int
		var segmentID []byte
		var disposition string
		if err := rows.Scan(&gotOrdinal, &segmentID, &finalRank, &disposition, &selected); err != nil {
			return mousa.SourceTrail{}, classify("get source trail candidates", err)
		}
		wantSelected := 0
		if want.Selected {
			wantSelected = 1
		}
		if gotOrdinal != ordinal || !bytes.Equal(segmentID, want.SegmentID[:]) || finalRank != want.FinalRank || disposition != string(want.Disposition) || selected != wantSelected {
			return mousa.SourceTrail{}, integrity("get source trail candidates", "ordered candidate projection disagrees with record")
		}
	}
	if rows.Next() {
		return mousa.SourceTrail{}, integrity("get source trail candidates", "extra ordered candidate row")
	}
	if err := rows.Err(); err != nil {
		return mousa.SourceTrail{}, classify("get source trail candidates", err)
	}
	if err := rows.Close(); err != nil {
		return mousa.SourceTrail{}, classify("get source trail candidates", err)
	}
	if trail.Schema == mousa.SourceTrailSchemaV2 {
		if err := verifyExactTrailContent(ctx, q, trail, decision.Request.SourceID); err != nil {
			return mousa.SourceTrail{}, err
		}
	}
	return trail, nil
}

// verifySourceTrailRecords verifies every stored trail, its ordered candidate rows, typed parents,
// and that no candidate row exists outside the canonical records.
func verifySourceTrailRecords(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return startupError("begin source trail verification", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM source_trails ORDER BY id`)
	if err != nil {
		return startupError("scan source trails", err)
	}
	var ids [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return startupError("scan source trails", err)
		}
		ids = append(ids, append([]byte(nil), raw...))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return startupError("scan source trails", err)
	}
	if err := rows.Close(); err != nil {
		return startupError("scan source trails", err)
	}
	candidateCount := 0
	for _, raw := range ids {
		if len(raw) != 32 {
			return integrity("scan source trails", "invalid ID length")
		}
		var id mousa.SourceTrailID
		copy(id[:], raw)
		trail, err := getSourceTrail(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, candidate := range trail.Candidates {
			if _, err := getSegment(ctx, tx, candidate.SegmentID); err != nil {
				if IsCode(err, CodeNotFound) {
					return integrity("verify source trails", "typed segment parent is missing")
				}
				return err
			}
		}
		candidateCount += len(trail.Candidates)
	}
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source_trail_candidates`).Scan(&total); err != nil {
		return startupError("scan source trail candidates", err)
	}
	if total != candidateCount {
		return integrity("verify source trails", "candidate row count disagrees with canonical records")
	}
	if err := tx.Commit(); err != nil {
		return startupError("finish source trail verification", err)
	}
	return nil
}

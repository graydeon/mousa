package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/graydeon/mousa/internal/mousa"
)

// TrailInspection releases a historical projection only after a fresh source
// authorization. It contains no evidence text or rejected candidate identities.
type TrailInspection struct {
	AuthorizationDecisionID mousa.PolicyDecisionID       `json:"authorization_decision_id"`
	AuthorizationOutcome    mousa.PolicyDecisionOutcome  `json:"authorization_outcome"`
	AuthorizationReasons    []mousa.PolicyDecisionReason `json:"authorization_reasons"`
	Historical              *SourceTrailView             `json:"historical,omitempty"`
}

// SourceTrailView is a filtered explanation, not a canonical SourceTrail record.
// Accepted metadata describes the historical packet, not a new text release.
type SourceTrailView struct {
	ID                mousa.SourceTrailID          `json:"id"`
	RequestID         string                       `json:"request_id"`
	DecisionID        string                       `json:"decision_id"`
	DecisionOutcome   string                       `json:"decision_outcome"`
	DecisionReasons   []mousa.PolicyDecisionReason `json:"decision_reasons"`
	Expression        string                       `json:"expression"`
	BudgetBytes       uint64                       `json:"budget_bytes"`
	UsedBytes         uint64                       `json:"used_bytes"`
	PacketID          string                       `json:"packet_id"`
	LifecycleExcluded int                          `json:"lifecycle_excluded"`
	Candidates        []SourceTrailCandidateView   `json:"candidates"`
	Schema            string                       `json:"schema,omitempty"`
	PackingPolicy     string                       `json:"packing_policy,omitempty"`
}

// SourceTrailCandidateView describes a historically accepted candidate. An
// indexed row now is an activation fact, not evidence of authorization or truth.
type SourceTrailCandidateView struct {
	SegmentID     mousa.SegmentID `json:"segment_id"`
	ContentSHA256 mousa.SHA256    `json:"content_sha256"`
	Rank          int             `json:"rank"`
	TextBytes     uint64          `json:"text_bytes"`
	Selected      bool            `json:"selected"`
	IndexedNow    bool            `json:"indexed_now"`
	Omission      string          `json:"omission,omitempty"`
	DuplicateOf   string          `json:"duplicate_of,omitempty"`
}

// InspectSourceTrail authorizes a new request before looking up a trail of the
// requested source. Denial releases no history, including whether the trail
// exists. Authorization, verified history, and index membership share one writer
// snapshot. Historical rejected candidates are counted but never projected.
func (store *Store) InspectSourceTrail(ctx context.Context, request mousa.PolicyEvaluationRequest, id mousa.SourceTrailID) (TrailInspection, error) {
	if err := store.requireWritable("inspect source trail"); err != nil {
		return TrailInspection{}, err
	}
	if err := request.Validate(); err != nil {
		return TrailInspection{}, wrap(CodeInvalidRecord, "inspect source trail", err)
	}
	if id == (mousa.SourceTrailID{}) {
		return TrailInspection{}, wrap(CodeInvalidRecord, "inspect source trail", errors.New("trail ID must not be zero"))
	}
	var result TrailInspection
	err := store.writeImmediate(ctx, "inspect source trail", func(conn *sql.Conn) error {
		decision, err := evaluateSourceRetrieval(ctx, conn, request, false)
		if err != nil {
			return err
		}
		result = TrailInspection{
			AuthorizationDecisionID: decision.ID,
			AuthorizationOutcome:    decision.Outcome,
			AuthorizationReasons:    decision.ReasonCodes,
		}
		if decision.Outcome != mousa.PolicyOutcomeAllow {
			return nil
		}
		var found bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM source_trails AS t
			JOIN policy_decisions AS d ON d.id = t.decision_id
			WHERE t.id = ? AND d.source_id = ?)`, id[:], request.SourceID[:]).Scan(&found); err != nil {
			return classify("scope source trail", err)
		}
		if !found {
			return readError("get source trail", sql.ErrNoRows)
		}
		trail, err := getSourceTrail(ctx, conn, id)
		if err != nil {
			return err
		}
		parentID, err := mousa.ParsePolicyDecisionID(trail.DecisionID)
		if err != nil {
			return wrap(CodeIntegrity, "inspect source trail", err)
		}
		parent, err := getPolicyDecision(ctx, conn, parentID)
		if err != nil {
			return err
		}
		view := &SourceTrailView{
			ID: trail.ID, RequestID: trail.RequestID, DecisionID: trail.DecisionID,
			DecisionOutcome: trail.Outcome, DecisionReasons: parent.ReasonCodes,
			Expression: trail.Expression, BudgetBytes: trail.BudgetBytes,
			UsedBytes: trail.UsedBytes, PacketID: trail.PacketID,
			Candidates: []SourceTrailCandidateView{},
		}
		if trail.Schema == mousa.SourceTrailSchemaV2 {
			view.Schema = trail.Schema
			view.PackingPolicy = trail.PackingPolicy
		}
		for _, candidate := range trail.Candidates {
			if candidate.Disposition != mousa.CandidateAccepted {
				view.LifecycleExcluded++
				continue
			}
			segment, err := getSegment(ctx, conn, candidate.SegmentID)
			if err != nil {
				return err
			}
			if segment.ContentSHA256 != candidate.ContentSHA256 {
				return integrity("inspect source trail", "candidate digest disagrees with canonical segment")
			}
			paths, err := lexicalEvidencePaths(ctx, conn, segment)
			if err != nil {
				return err
			}
			inSource := false
			for _, path := range paths {
				if path.SourceID == request.SourceID {
					inSource = true
					break
				}
			}
			if !inSource {
				return integrity("inspect source trail", "candidate does not derive from authorized source")
			}
			var indexed bool
			if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM segment_lexical_rows WHERE segment_id = ?)`, candidate.SegmentID[:]).Scan(&indexed); err != nil {
				return classify("inspect segment activation", err)
			}
			view.Candidates = append(view.Candidates, SourceTrailCandidateView{
				SegmentID: candidate.SegmentID, ContentSHA256: candidate.ContentSHA256,
				Rank: candidate.FinalRank, TextBytes: candidate.TextBytes,
				Selected: candidate.Selected, IndexedNow: indexed,
				Omission: candidate.Omission, DuplicateOf: candidate.DuplicateOf,
			})
		}
		result.Historical = view
		return nil
	})
	if err != nil {
		return TrailInspection{}, err
	}
	return result, nil
}

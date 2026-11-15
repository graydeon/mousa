package main

import (
	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// These projections release verified ranking facts, not canonical records or
// embedding values. History uses the producing view, never today's search.
type mcpVectorDetails struct {
	Mode          string                  `json:"mode"`
	SpaceID       mousa.SHA256            `json:"space_id"`
	Space         mousa.VectorSpace       `json:"space"`
	QueryVectorID mousa.SHA256            `json:"query_vector_id"`
	Eligible      int                     `json:"eligible"`
	Indexed       int                     `json:"indexed"`
	Candidates    []mousa.VectorCandidate `json:"candidates"`
}

type mcpHybridDetails struct {
	Query         string                  `json:"query"`
	TermPolicy    string                  `json:"term_policy"`
	Terms         []string                `json:"terms"`
	FusionPolicy  string                  `json:"fusion_policy"`
	SpaceID       mousa.SHA256            `json:"space_id"`
	Space         mousa.VectorSpace       `json:"space"`
	QueryVectorID mousa.SHA256            `json:"query_vector_id"`
	Eligible      int                     `json:"eligible"`
	Indexed       int                     `json:"indexed"`
	Candidates    []mousa.HybridCandidate `json:"candidates"`
}

func mcpVectorFacts(v *mousa.VectorSelection) *mcpVectorDetails {
	if v == nil {
		return nil
	}
	return &mcpVectorDetails{v.Mode, v.SpaceID, v.Space, v.QueryVectorID, v.Eligible, v.Indexed, v.Candidates}
}

func mcpHybridFacts(h *mousa.HybridSelection) *mcpHybridDetails {
	if h == nil {
		return nil
	}
	return &mcpHybridDetails{h.Query, h.TermPolicy, h.Terms, h.FusionPolicy, h.SpaceID, h.Space, h.QueryVectorID, h.Eligible, h.Indexed, h.Candidates}
}

type mcpRetrievalEvidence struct {
	sqlite.VectorPassage
	Rank        int                              `json:"rank"`
	Cosine      *float64                         `json:"cosine,omitempty"`
	EmbeddingID *mousa.SHA256                    `json:"embedding_id,omitempty"`
	RRF         *float64                         `json:"rrf,omitempty"`
	Lexical     *mousa.HybridLexicalContribution `json:"lexical,omitempty"`
	Vector      *mousa.HybridVectorContribution  `json:"vector,omitempty"`
}

type mcpRetrievalResult struct {
	Mode                 string                       `json:"mode"`
	Source               string                       `json:"source"`
	SpaceID              mousa.SHA256                 `json:"space_id"`
	QueryVectorID        mousa.SHA256                 `json:"query_vector_id"`
	DecisionID           mousa.PolicyDecisionID       `json:"decision_id"`
	DecisionOutcome      mousa.PolicyDecisionOutcome  `json:"decision_outcome"`
	DecisionReasons      []mousa.PolicyDecisionReason `json:"decision_reasons"`
	TrailID              mousa.SourceTrailID          `json:"trail_id"`
	PacketID             string                       `json:"packet_id"`
	Eligible             int                          `json:"eligible"`
	Indexed              int                          `json:"indexed"`
	Missing              int                          `json:"missing"`
	Outcome              string                       `json:"outcome"`
	PackingPolicy        string                       `json:"packing_policy"`
	BudgetBytes          uint64                       `json:"budget_bytes"`
	UsedBytes            uint64                       `json:"used_bytes"`
	BudgetOmitted        int                          `json:"budget_omitted"`
	DuplicateOmitted     int                          `json:"duplicate_omitted"`
	Evidence             []mcpRetrievalEvidence       `json:"evidence"`
	Vector               *mcpVectorDetails            `json:"vector,omitempty"`
	Hybrid               *mcpHybridDetails            `json:"hybrid,omitempty"`
	Context              *string                      `json:"context,omitempty"`
	Token                *mousa.TokenSelectionView    `json:"token,omitempty"`
	SupersessionExcluded *int                         `json:"supersession_excluded,omitempty"`
}

type mcpLexicalResult struct {
	*evidenceResult
	Context *string                   `json:"context,omitempty"`
	Token   *mousa.TokenSelectionView `json:"token,omitempty"`
}

func mcpRetrievalProjection(source string, traced sqlite.TracedVectorResult) mcpRetrievalResult {
	trail := traced.Trail
	result := mcpRetrievalResult{
		Source: source, DecisionID: traced.Decision.ID, DecisionOutcome: traced.Decision.Outcome,
		DecisionReasons: traced.Decision.ReasonCodes, TrailID: trail.ID, PacketID: trail.PacketID,
		PackingPolicy: trail.PackingPolicy, BudgetBytes: trail.BudgetBytes, UsedBytes: trail.UsedBytes,
		Evidence: []mcpRetrievalEvidence{}, Vector: mcpVectorFacts(trail.Vector), Hybrid: mcpHybridFacts(trail.Hybrid),
	}
	if trail.Token != nil {
		result.Context, result.Token = &traced.Context, mousa.ReleasedTokenSelection(trail)
	}
	if v := result.Vector; v != nil {
		result.Mode, result.SpaceID, result.QueryVectorID, result.Eligible, result.Indexed = "vector", v.SpaceID, v.QueryVectorID, v.Eligible, v.Indexed
	} else if h := result.Hybrid; h != nil {
		result.Mode, result.SpaceID, result.QueryVectorID, result.Eligible, result.Indexed = "hybrid", h.SpaceID, h.QueryVectorID, h.Eligible, h.Indexed
	}
	result.Missing = result.Eligible - result.Indexed
	supersessionIndex := 0
	if trail.Supersession != nil {
		result.SupersessionExcluded = new(int)
		*result.SupersessionExcluded = len(trail.Supersession.Dispositions)
	}
	for i, row := range trail.Candidates {
		if row.Disposition != mousa.CandidateAccepted {
			continue
		}
		if trail.Supersession != nil && supersessionIndex < len(trail.Supersession.Dispositions) &&
			trail.Supersession.Dispositions[supersessionIndex].SegmentID == row.SegmentID {
			supersessionIndex++
			continue
		}
		if !row.Selected && row.Omission != "duplicate" {
			result.BudgetOmitted++
		}
		if row.Omission == "duplicate" {
			result.DuplicateOmitted++
		}
		if !row.Selected {
			continue
		}
		evidence := mcpRetrievalEvidence{VectorPassage: traced.Passages[i], Rank: row.FinalRank}
		if result.Vector != nil {
			c := &trail.Vector.Candidates[i]
			evidence.Cosine, evidence.EmbeddingID = &c.Cosine, &c.EmbeddingID
		} else {
			c := &trail.Hybrid.Candidates[i]
			evidence.RRF, evidence.Lexical, evidence.Vector = &c.RRF, c.Lexical, c.Vector
		}
		result.Evidence = append(result.Evidence, evidence)
	}
	switch {
	case traced.Decision.Outcome != mousa.PolicyOutcomeAllow:
		result.Outcome = "policy_excluded"
	case len(result.Evidence) > 0:
		result.Outcome = "evidence"
	case result.Eligible == 0:
		result.Outcome = "no_passages"
	case result.Vector != nil && result.Indexed == 0:
		result.Outcome = "no_indexed_passages"
	case len(trail.Candidates) == 0:
		result.Outcome = "no_candidates"
	case result.SupersessionExcluded != nil && *result.SupersessionExcluded == len(trail.Candidates):
		result.Outcome = "supersession_excluded"
	default:
		result.Outcome = "budget_omitted"
	}
	return result
}

func mcpTrailProjection(source, id string, inspection sqlite.TrailInspection, details, tokenDetails bool) mcpTrailResult {
	result := mcpTrailResult{Source: source, TrailID: id,
		AuthorizationDecisionID: inspection.AuthorizationDecisionID,
		AuthorizationOutcome:    inspection.AuthorizationOutcome, AuthorizationReasons: inspection.AuthorizationReasons}
	if inspection.Historical != nil {
		result.Historical = &mcpHistoricalTrail{SourceTrailView: inspection.Historical}
		if details {
			// The verified view already enforces accepted-only v7 metadata.
			result.Historical.Vector = mcpVectorFacts(inspection.Historical.Vector)
			result.Historical.Hybrid = mcpHybridFacts(inspection.Historical.Hybrid)
		}
		if tokenDetails {
			result.Historical.Token = inspection.Historical.Token
		}
	}
	return result
}

package mousa

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
)

const SourceTrailSchemaV6 = "mousa.source_trail.v6"
const HybridFusionPolicy = "rrf-equal-k60-v1"

// HybridSelection records producing branch facts, not an embedding-generation claim.
// Eligible and Indexed describe the authorized snapshot; history cannot prove past absence.
type HybridSelection struct {
	Query         string            `json:"query"`
	TermPolicy    string            `json:"term_policy"`
	Terms         []string          `json:"terms"`
	FusionPolicy  string            `json:"fusion_policy"`
	SpaceID       SHA256            `json:"space_id"`
	Space         VectorSpace       `json:"space"`
	QueryVectorID SHA256            `json:"query_vector_id"`
	QueryValues   []float64         `json:"query_values"`
	Eligible      int               `json:"eligible"`
	Indexed       int               `json:"indexed"`
	Candidates    []HybridCandidate `json:"candidates"`
}

type HybridLexicalContribution struct {
	Rank int     `json:"rank"`
	BM25 float64 `json:"bm25"`
}

type HybridVectorContribution struct {
	Rank        int     `json:"rank"`
	Cosine      float64 `json:"cosine"`
	EmbeddingID SHA256  `json:"embedding_id"`
}

type HybridCandidate struct {
	EmbeddingInput
	Lexical *HybridLexicalContribution `json:"lexical,omitempty"`
	Vector  *HybridVectorContribution  `json:"vector,omitempty"`
	RRF     float64                    `json:"rrf"`
}

// HybridRRFScore implements equal-weight reciprocal ranks starting at one.
// Zero denotes absence from that branch, never a similarity threshold.
func HybridRRFScore(lexicalRank, vectorRank int) float64 {
	var score float64
	if lexicalRank > 0 {
		score += 1 / (60 + float64(lexicalRank))
	}
	if vectorRank > 0 {
		score += 1 / (60 + float64(vectorRank))
	}
	return score
}

// NewHybridSourceTrail packs only the supplied fused order. Canonical store
// membership and current-pointer verification remain the caller's responsibility.
func NewHybridSourceTrail(request PolicyEvaluationRequest, decision PolicyDecision, expression string, selection HybridSelection, candidates []VerifiedLexicalCandidate, budget uint64, packing string) (SourceTrail, error) {
	if err := request.Validate(); err != nil {
		return SourceTrail{}, err
	}
	if err := decision.Validate(); err != nil {
		return SourceTrail{}, err
	}
	if decision.Request != request {
		return SourceTrail{}, errors.New("hybrid decision belongs to another request")
	}
	rows := trailCandidatesFromVerified(candidates)
	plan, err := packTraceCandidates(rows, candidates, budget, packing, nil)
	if err != nil {
		return SourceTrail{}, err
	}
	for i := range rows {
		rows[i].Selected = plan.Selected[i]
	}
	trail := SourceTrail{Schema: SourceTrailSchemaV6, RequestID: request.ID.String(), DecisionID: decision.ID.String(), Outcome: string(decision.Outcome), Expression: expression, BudgetBytes: budget, UsedBytes: plan.UsedBytes, PacketID: plan.PacketID.String(), Candidates: rows, PackingPolicy: packing, Hybrid: &selection}
	trail.ID, err = vectorTrailIdentity(trail)
	if err != nil {
		return SourceTrail{}, err
	}
	return trail, trail.Validate()
}

func (trail SourceTrail) validateHybridTrail() error {
	invalid := func() error { return errors.New("invalid v6 hybrid bindings, coverage or order") }
	h := trail.Hybrid
	if h == nil || trail.Vector != nil || trail.Supersession != nil || len(trail.Associated) != 0 || len(trail.AssociationOmissions) != 0 {
		return invalid()
	}
	if _, err := ParsePolicyEvaluationRequestID(trail.RequestID); err != nil {
		return err
	}
	if _, err := ParsePolicyDecisionID(trail.DecisionID); err != nil {
		return err
	}
	if h.TermPolicy != "original" && h.TermPolicy != "dedup" {
		return invalid()
	}
	terms, err := PrepareLexicalTerms(h.Query, h.TermPolicy == "dedup")
	if err != nil {
		return err
	}
	if len(h.Terms) != len(terms) || trail.Expression != strings.Join(terms, " OR ") || len(trail.Expression) > MaxLexicalExpressionBytes {
		return invalid()
	}
	for i := range terms {
		if terms[i] != h.Terms[i] {
			return invalid()
		}
	}
	sid, err := h.Space.Identity()
	if err != nil {
		return err
	}
	qid, err := QueryVectorIdentity(h.SpaceID, h.QueryValues)
	if err != nil {
		return err
	}
	if err := ValidateVector(h.QueryValues, h.Space.Dimensions); err != nil {
		return err
	}
	if sid != h.SpaceID || qid != h.QueryVectorID || h.FusionPolicy != HybridFusionPolicy || h.Eligible < 0 || h.Eligible > MaxVectorPassages || h.Indexed < 0 || h.Indexed > h.Eligible || len(h.Candidates) > h.Eligible || len(h.Candidates) != len(trail.Candidates) || h.Candidates == nil || trail.Candidates == nil {
		return invalid()
	}
	if trail.Outcome != string(PolicyOutcomeAllow) && trail.Outcome != string(PolicyOutcomeDeny) {
		return invalid()
	}
	if trail.Outcome == string(PolicyOutcomeDeny) && (h.Eligible != 0 || h.Indexed != 0 || len(h.Candidates) != 0) {
		return invalid()
	}
	if trail.BudgetBytes == 0 || trail.BudgetBytes > MaxVectorTextBytes || trail.UsedBytes > trail.BudgetBytes {
		return invalid()
	}
	if err := trail.validateCandidateInvariants(); err != nil {
		return err
	}
	if err := trail.validatePackedCandidates(); err != nil {
		return err
	}
	lexical := make([]HybridCandidate, 0, len(h.Candidates))
	vectors := make([]HybridCandidate, 0, h.Indexed)
	var source SourceID
	var used, considered uint64
	for i, c := range h.Candidates {
		row := trail.Candidates[i]
		if c.SourceID == (SourceID{}) || c.SegmentID != row.SegmentID || c.ContentSHA256 != row.ContentSHA256 || c.RepresentationID == (RepresentationID{}) || c.RepresentationSHA256 == (SHA256{}) || c.ContentSHA256 == (SHA256{}) || row.Disposition != CandidateAccepted || row.TextBytes == 0 || row.TextBytes > MaxUTF8TextSegmentBytes || (i > 0 && c.SourceID != source) {
			return invalid()
		}
		source = c.SourceID
		lr, vr := 0, 0
		if c.Lexical != nil {
			lr = c.Lexical.Rank
			if lr < 1 || lr > h.Eligible || math.IsNaN(c.Lexical.BM25) || math.IsInf(c.Lexical.BM25, 0) {
				return invalid()
			}
			lexical = append(lexical, c)
		}
		if c.Vector != nil {
			vr = c.Vector.Rank
			if vr < 1 || vr > h.Indexed || c.Vector.EmbeddingID == (SHA256{}) || math.IsNaN(c.Vector.Cosine) || math.IsInf(c.Vector.Cosine, 0) || c.Vector.Cosine < -1 || c.Vector.Cosine > 1 {
				return invalid()
			}
			vectors = append(vectors, c)
		}
		if lr == 0 && vr == 0 || c.RRF != HybridRRFScore(lr, vr) {
			return invalid()
		}
		if i > 0 {
			p := h.Candidates[i-1]
			if p.RRF < c.RRF || p.RRF == c.RRF && bytes.Compare(p.SegmentID[:], c.SegmentID[:]) >= 0 {
				return invalid()
			}
		}
		considered += row.TextBytes
		if row.Selected {
			used += row.TextBytes
		}
	}
	if len(vectors) != h.Indexed || considered > MaxVectorTextBytes || used != trail.UsedBytes {
		return invalid()
	}
	sort.Slice(lexical, func(i, j int) bool { return lexical[i].Lexical.Rank < lexical[j].Lexical.Rank })
	sort.Slice(vectors, func(i, j int) bool { return vectors[i].Vector.Rank < vectors[j].Vector.Rank })
	for i, c := range lexical {
		if c.Lexical.Rank != i+1 {
			return invalid()
		}
		if i > 0 {
			p := lexical[i-1]
			if p.Lexical.BM25 > c.Lexical.BM25 || p.Lexical.BM25 == c.Lexical.BM25 && bytes.Compare(p.SegmentID[:], c.SegmentID[:]) >= 0 {
				return invalid()
			}
		}
	}
	for i, c := range vectors {
		if c.Vector.Rank != i+1 {
			return invalid()
		}
		if i > 0 {
			p := vectors[i-1]
			if p.Vector.Cosine < c.Vector.Cosine || p.Vector.Cosine == c.Vector.Cosine && bytes.Compare(p.SegmentID[:], c.SegmentID[:]) >= 0 {
				return invalid()
			}
		}
	}
	packet, err := trail.packetIdentity()
	if err != nil || packet.String() != trail.PacketID {
		return invalid()
	}
	id, err := vectorTrailIdentity(trail)
	if err != nil || id != trail.ID {
		return invalid()
	}
	return nil
}

func hybridTrailSchema(data []byte) bool {
	var header struct {
		Schema string `json:"schema"`
	}
	return json.Unmarshal(data, &header) == nil && header.Schema == SourceTrailSchemaV6
}

func decodeHybridSourceTrail(data []byte) (SourceTrail, error) {
	// An explicit closed wire keeps v1-v5 and unsupported stage fields out of v6.
	var wire struct {
		Schema        string           `json:"schema"`
		ID            SourceTrailID    `json:"id"`
		RequestID     string           `json:"request_id"`
		DecisionID    string           `json:"decision_id"`
		Outcome       string           `json:"outcome"`
		Expression    string           `json:"expression"`
		BudgetBytes   uint64           `json:"budget_bytes"`
		UsedBytes     uint64           `json:"used_bytes"`
		PacketID      string           `json:"packet_id"`
		Candidates    []TrailCandidate `json:"candidates"`
		PackingPolicy string           `json:"packing_policy"`
		Hybrid        *HybridSelection `json:"hybrid"`
	}
	if err := decodeStrictVectorJSON(data, &wire); err != nil {
		return SourceTrail{}, err
	}
	t := SourceTrail{Schema: wire.Schema, ID: wire.ID, RequestID: wire.RequestID, DecisionID: wire.DecisionID, Outcome: wire.Outcome, Expression: wire.Expression, BudgetBytes: wire.BudgetBytes, UsedBytes: wire.UsedBytes, PacketID: wire.PacketID, Candidates: wire.Candidates, PackingPolicy: wire.PackingPolicy, Hybrid: wire.Hybrid}
	return t, t.Validate()
}

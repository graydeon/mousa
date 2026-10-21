package mousa

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
)

const SourceTrailSchemaV5 = "mousa.source_trail.v5"

// VectorSelection records actual arithmetic inputs and immutable producing records.
// Coverage describes the authorized current source snapshot, not a quality estimate.
type VectorSelection struct {
	Mode          string            `json:"mode"`
	SpaceID       SHA256            `json:"space_id"`
	Space         VectorSpace       `json:"space"`
	QueryVectorID SHA256            `json:"query_vector_id"`
	QueryValues   []float64         `json:"query_values"`
	Eligible      int               `json:"eligible"`
	Indexed       int               `json:"indexed"`
	Candidates    []VectorCandidate `json:"candidates"`
}

type VectorCandidate struct {
	EmbeddingID SHA256 `json:"embedding_id"`
	EmbeddingInput
	Cosine float64 `json:"cosine"`
}

// NewVectorSourceTrail packs the already verified vector order without giving
// cosine a lexical name or generating an FTS expression. Store checks remain required.
func NewVectorSourceTrail(request PolicyEvaluationRequest, decision PolicyDecision, selection VectorSelection, candidates []VerifiedLexicalCandidate, budget uint64, packing string) (SourceTrail, error) {
	if err := request.Validate(); err != nil {
		return SourceTrail{}, err
	}
	if err := decision.Validate(); err != nil {
		return SourceTrail{}, err
	}
	if decision.Request != request {
		return SourceTrail{}, errors.New("vector trail decision belongs to another request")
	}
	if packing != PackingOriginal && packing != PackingExactV1 {
		return SourceTrail{}, errors.New("invalid vector packing policy")
	}
	rows := trailCandidatesFromVerified(candidates)
	plan, err := packTraceCandidates(rows, candidates, budget, packing, nil)
	if err != nil {
		return SourceTrail{}, err
	}
	for i := range rows {
		rows[i].Selected = plan.Selected[i]
	}
	trail := SourceTrail{Schema: SourceTrailSchemaV5, RequestID: request.ID.String(), DecisionID: decision.ID.String(), Outcome: string(decision.Outcome), BudgetBytes: budget, UsedBytes: plan.UsedBytes, PacketID: plan.PacketID.String(), Candidates: rows, PackingPolicy: packing, Vector: &selection}
	trail.ID, err = vectorTrailIdentity(trail)
	if err != nil {
		return SourceTrail{}, err
	}
	return trail, trail.validateVectorTrail()
}

func vectorTrailIdentity(trail SourceTrail) (SourceTrailID, error) {
	trail.ID = SourceTrailID{}
	data, err := encodeJSONContract(trail, "vector trail")
	return SourceTrailID(sha256.Sum256(data)), err
}

func (trail SourceTrail) validateVectorTrail() error {
	invalid := func() error { return errors.New("invalid v5 vector trail bindings, coverage or order") }
	if trail.Schema != SourceTrailSchemaV5 || trail.Expression != "" || trail.Supersession != nil || len(trail.Associated) != 0 || len(trail.AssociationOmissions) != 0 || trail.Vector == nil {
		return invalid()
	}
	if _, err := ParsePolicyEvaluationRequestID(trail.RequestID); err != nil {
		return err
	}
	if _, err := ParsePolicyDecisionID(trail.DecisionID); err != nil {
		return err
	}
	v := trail.Vector
	id, err := v.Space.Identity()
	if err != nil {
		return err
	}
	qid, err := QueryVectorIdentity(v.SpaceID, v.QueryValues)
	if err != nil {
		return err
	}
	if err := ValidateVector(v.QueryValues, v.Space.Dimensions); err != nil {
		return err
	}
	if v.Mode != "vector" || id != v.SpaceID || qid != v.QueryVectorID || v.Eligible < 0 || v.Eligible > MaxVectorPassages || v.Indexed < 0 || v.Indexed > v.Eligible || v.Indexed != len(v.Candidates) || len(v.Candidates) != len(trail.Candidates) || trail.Candidates == nil || v.Candidates == nil {
		return invalid()
	}
	if trail.Outcome != string(PolicyOutcomeAllow) && trail.Outcome != string(PolicyOutcomeDeny) {
		return invalid()
	}
	if trail.Outcome == string(PolicyOutcomeDeny) && (v.Eligible != 0 || v.Indexed != 0) {
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
	var used, considered uint64
	var source SourceID
	for i, c := range v.Candidates {
		row := trail.Candidates[i]
		considered += row.TextBytes
		if considered > MaxVectorTextBytes {
			return invalid()
		}
		if c.EmbeddingID == (SHA256{}) || c.SourceID == (SourceID{}) || c.SegmentID == (SegmentID{}) || c.ContentSHA256 == (SHA256{}) || c.RepresentationID == (RepresentationID{}) || c.RepresentationSHA256 == (SHA256{}) || c.SegmentID != row.SegmentID || c.ContentSHA256 != row.ContentSHA256 || row.Disposition != CandidateAccepted || row.TextBytes == 0 || row.TextBytes > MaxUTF8TextSegmentBytes || math.IsNaN(c.Cosine) || math.IsInf(c.Cosine, 0) || c.Cosine < -1 || c.Cosine > 1 {
			return invalid()
		}
		if i > 0 && (source != c.SourceID || v.Candidates[i-1].Cosine < c.Cosine || (v.Candidates[i-1].Cosine == c.Cosine && bytes.Compare(v.Candidates[i-1].SegmentID[:], c.SegmentID[:]) >= 0)) {
			return invalid()
		}
		source = c.SourceID
		if row.Selected {
			used += row.TextBytes
		}
	}
	if used != trail.UsedBytes {
		return invalid()
	}
	packet, err := trail.packetIdentity()
	if err != nil || packet.String() != trail.PacketID {
		return invalid()
	}
	expected, err := vectorTrailIdentity(trail)
	if err != nil {
		return err
	}
	if expected != trail.ID {
		return invalid()
	}
	return nil
}

func decodeVectorSourceTrail(data []byte) (SourceTrail, error) {
	// This closed version has no lexical expression, associations or supersession.
	var wire struct {
		Schema        string           `json:"schema"`
		ID            SourceTrailID    `json:"id"`
		RequestID     string           `json:"request_id"`
		DecisionID    string           `json:"decision_id"`
		Outcome       string           `json:"outcome"`
		BudgetBytes   uint64           `json:"budget_bytes"`
		UsedBytes     uint64           `json:"used_bytes"`
		PacketID      string           `json:"packet_id"`
		Candidates    []TrailCandidate `json:"candidates"`
		PackingPolicy string           `json:"packing_policy"`
		Vector        *VectorSelection `json:"vector"`
	}
	if err := decodeStrictVectorJSON(data, &wire); err != nil {
		return SourceTrail{}, err
	}
	trail := SourceTrail{Schema: wire.Schema, ID: wire.ID, RequestID: wire.RequestID, DecisionID: wire.DecisionID, Outcome: wire.Outcome, BudgetBytes: wire.BudgetBytes, UsedBytes: wire.UsedBytes, PacketID: wire.PacketID, Candidates: wire.Candidates, PackingPolicy: wire.PackingPolicy, Vector: wire.Vector}
	return trail, trail.Validate()
}

func vectorTrailSchema(data []byte) bool {
	var header struct {
		Schema string `json:"schema"`
	}
	return json.Unmarshal(data, &header) == nil && header.Schema == SourceTrailSchemaV5
}

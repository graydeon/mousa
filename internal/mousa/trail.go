package mousa

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"unicode/utf8"
)

const (
	ContextPacketSchema = "mousa.context_packet.v1"
	SourceTrailSchema   = "mousa.source_trail.v1"
)

// PacketPlan is the deterministic outcome of the Pack stage for one verified candidate set: the
// ordered selection flags, the released byte total, and the packet identity binding the selection.
type PacketPlan struct {
	Selected   []bool
	UsedBytes  uint64
	PacketID   ContextPacketID
	BudgetUsed bool
}

// ContextPacketID binds one packed selection.
type ContextPacketID [sha256.Size]byte

func (id ContextPacketID) String() string { return hex.EncodeToString(id[:]) }

func (id ContextPacketID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *ContextPacketID) UnmarshalJSON(data []byte) error {
	decoded, err := decodeLowerHexJSON("id", data)
	if err != nil {
		return err
	}
	*id = ContextPacketID(decoded)
	return nil
}

// ParseContextPacketID parses one lowercase hexadecimal packet identity.
func ParseContextPacketID(value string) (ContextPacketID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return ContextPacketID{}, err
	}
	return ContextPacketID(decoded), nil
}

// PackVerifiedLexicalCandidates is the Pack stage: it walks candidates in verified order and selects
// every accepted candidate whose released text fits the remaining byte budget, skipping candidates
// that do not fit and continuing. Rejected candidates are never selected. The budget is an explicit
// released-text byte budget; token-aware budgeting, truncation, deduplication, and redundancy removal
// remain deferred.
func PackVerifiedLexicalCandidates(candidates []TrailCandidate, budgetBytes uint64) (PacketPlan, error) {
	if budgetBytes == 0 {
		return PacketPlan{}, retrievalValidationError("budget_bytes", ValidationCodeInvalidRange, "must be positive")
	}
	plan := PacketPlan{Selected: make([]bool, len(candidates))}
	used := uint64(0)
	for index, candidate := range candidates {
		if candidate.Disposition != CandidateAccepted {
			continue
		}
		if candidate.TextBytes > budgetBytes-used {
			continue
		}
		plan.Selected[index] = true
		used += candidate.TextBytes
	}
	plan.UsedBytes = used
	id, err := NewContextPacketID(candidates, budgetBytes, plan.Selected)
	if err != nil {
		return PacketPlan{}, err
	}
	plan.PacketID = id
	return plan, nil
}

// NewContextPacketID derives the packet identity from the packet schema, request identity, decision
// identity, budget, used bytes, and every selected candidate in order.
func NewContextPacketID(candidates []TrailCandidate, budgetBytes uint64, selected []bool) (ContextPacketID, error) {
	if len(selected) != len(candidates) {
		return ContextPacketID{}, errors.New("selection length disagrees with candidates")
	}
	var used uint64
	digest := sha256.New()
	fields := [][]byte{[]byte(ContextPacketSchema)}
	var budget [8]byte
	binary.BigEndian.PutUint64(budget[:], budgetBytes)
	fields = append(fields, budget[:])
	for index, candidate := range candidates {
		if !selected[index] {
			continue
		}
		var rank, bytes [8]byte
		binary.BigEndian.PutUint64(rank[:], uint64(candidate.FinalRank))
		binary.BigEndian.PutUint64(bytes[:], candidate.TextBytes)
		fields = append(fields,
			candidate.SegmentID[:],
			candidate.ContentSHA256[:],
			rank[:],
			bytes[:],
		)
		used += candidate.TextBytes
	}
	var usedField [8]byte
	binary.BigEndian.PutUint64(usedField[:], used)
	fields = append(fields, usedField[:])
	writeTuple(digest, fields...)
	return ContextPacketID(digest.Sum(nil)), nil
}

// TrailCandidate is one considered candidate in a Source Trail, shared between the Pack and Trace
// stages. TextBytes is the released text size for an accepted candidate and zero for a rejected one.
type TrailCandidate struct {
	SegmentID     SegmentID            `json:"segment_id"`
	ContentSHA256 SHA256               `json:"content_sha256"`
	FinalRank     int                  `json:"final_rank"`
	TextBytes     uint64               `json:"text_bytes"`
	Disposition   CandidateDisposition `json:"disposition"`
	Reasons       []LifecycleReason    `json:"reasons"`
	Selected      bool                 `json:"selected"`
}

// SourceTrail is one immutable record of how a context packet was produced: the enforced decision,
// the search expression, the pack budget accounting, and every considered candidate with its
// disposition and selection. Released text is never copied into the record.
type SourceTrail struct {
	Schema      string           `json:"schema"`
	ID          SourceTrailID    `json:"id"`
	RequestID   string           `json:"request_id"`
	DecisionID  string           `json:"decision_id"`
	Outcome     string           `json:"outcome"`
	Expression  string           `json:"expression"`
	BudgetBytes uint64           `json:"budget_bytes"`
	UsedBytes   uint64           `json:"used_bytes"`
	PacketID    string           `json:"packet_id"`
	Candidates  []TrailCandidate `json:"candidates"`
}

// SourceTrailID binds the complete trail explanation and parent snapshot.
type SourceTrailID [sha256.Size]byte

func (id SourceTrailID) String() string { return hex.EncodeToString(id[:]) }

func (id SourceTrailID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *SourceTrailID) UnmarshalJSON(data []byte) error {
	decoded, err := decodeLowerHexJSON("id", data)
	if err != nil {
		return err
	}
	*id = SourceTrailID(decoded)
	return nil
}

// ParseSourceTrailID parses one lowercase hexadecimal trail identity.
func ParseSourceTrailID(value string) (SourceTrailID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return SourceTrailID{}, err
	}
	return SourceTrailID(decoded), nil
}

// NewSourceTrail is the Trace stage: it packs the verified candidates within the explicit budget and
// builds one immutable trail record whose identity binds the complete explanation.
func NewSourceTrail(request PolicyEvaluationRequest, decision PolicyDecision, expression string, candidates []VerifiedLexicalCandidate, budgetBytes uint64) (SourceTrail, error) {
	if err := request.Validate(); err != nil {
		return SourceTrail{}, prefixValidationError(err, "request")
	}
	if err := decision.Validate(); err != nil {
		return SourceTrail{}, prefixValidationError(err, "decision")
	}
	if decision.Request != request {
		return SourceTrail{}, newValidationError("decision", ValidationCodeInvalidValue, "decision belongs to another request", nil)
	}
	if decision.Outcome == PolicyOutcomeAllow && len(candidates) == 0 {
		candidates = []VerifiedLexicalCandidate{}
	}
	if candidates == nil {
		candidates = []VerifiedLexicalCandidate{}
	}
	trailCandidates := make([]TrailCandidate, len(candidates))
	for index, candidate := range candidates {
		textBytes := uint64(len(candidate.Text))
		if candidate.Disposition == CandidateRejected {
			textBytes = 0
		}
		trailCandidates[index] = TrailCandidate{
			SegmentID:     candidate.Segment.ID,
			ContentSHA256: candidate.Segment.ContentSHA256,
			FinalRank:     candidate.FinalRank,
			TextBytes:     textBytes,
			Disposition:   candidate.Disposition,
			Reasons:       append([]LifecycleReason(nil), candidate.Reasons...),
		}
	}
	plan, err := PackVerifiedLexicalCandidates(trailCandidates, budgetBytes)
	if err != nil {
		return SourceTrail{}, err
	}
	for index := range trailCandidates {
		trailCandidates[index].Selected = plan.Selected[index]
	}
	if !utf8.ValidString(expression) || len(expression) < 1 || len(expression) > 4096 {
		return SourceTrail{}, retrievalValidationError("expression", ValidationCodeInvalidRange, "must be non-empty valid UTF-8 within bounds")
	}
	trail := SourceTrail{
		Schema:      SourceTrailSchema,
		RequestID:   request.ID.String(),
		DecisionID:  decision.ID.String(),
		Outcome:     string(decision.Outcome),
		Expression:  expression,
		BudgetBytes: budgetBytes,
		UsedBytes:   plan.UsedBytes,
		PacketID:    plan.PacketID.String(),
		Candidates:  trailCandidates,
	}
	id, err := NewSourceTrailID(trail)
	if err != nil {
		return SourceTrail{}, err
	}
	trail.ID = id
	if err := trail.Validate(); err != nil {
		return SourceTrail{}, err
	}
	return trail, nil
}

// Validate recomputes every derived relation: the pack plan from the ordered candidates, the used
// bytes, the packet identity, and the trail identity.
func (trail SourceTrail) Validate() error {
	if trail.Schema != SourceTrailSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.source_trail.v1", nil)
	}
	if _, err := ParsePolicyEvaluationRequestID(trail.RequestID); err != nil {
		return prefixValidationError(err, "request_id")
	}
	if _, err := ParsePolicyDecisionID(trail.DecisionID); err != nil {
		return prefixValidationError(err, "decision_id")
	}
	if trail.Outcome != string(PolicyOutcomeAllow) && trail.Outcome != string(PolicyOutcomeDeny) {
		return newValidationError("outcome", ValidationCodeInvalidValue, "must be allow or deny", nil)
	}
	if !utf8.ValidString(trail.Expression) || len(trail.Expression) < 1 || len(trail.Expression) > 4096 {
		return retrievalValidationError("expression", ValidationCodeInvalidRange, "must be non-empty valid UTF-8 within bounds")
	}
	if trail.BudgetBytes == 0 {
		return retrievalValidationError("budget_bytes", ValidationCodeInvalidRange, "must be positive")
	}
	if trail.UsedBytes > trail.BudgetBytes {
		return retrievalValidationError("used_bytes", ValidationCodeInvalidRange, "must not exceed the budget")
	}
	used := uint64(0)
	for index, candidate := range trail.Candidates {
		field := "candidates[" + itoa(index) + "]"
		if candidate.SegmentID == (SegmentID{}) {
			return newValidationError(field+".segment_id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		if candidate.ContentSHA256 == (SHA256{}) {
			return newValidationError(field+".content_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
		}
		if candidate.Disposition != CandidateAccepted && candidate.Disposition != CandidateRejected {
			return newValidationError(field+".disposition", ValidationCodeInvalidValue, "must be accepted or rejected", nil)
		}
		if candidate.Disposition == CandidateRejected && (candidate.FinalRank != 0 || candidate.TextBytes != 0 || candidate.Selected) {
			return newValidationError(field, ValidationCodeInvalidRange, "rejected candidate must carry no rank, bytes, or selection", nil)
		}
		if candidate.Disposition == CandidateAccepted && candidate.FinalRank == 0 {
			return newValidationError(field+".final_rank", ValidationCodeInvalidRange, "accepted candidate must carry a final rank", nil)
		}
		if candidate.Disposition == CandidateAccepted && candidate.TextBytes == 0 {
			return newValidationError(field+".text_bytes", ValidationCodeInvalidRange, "accepted candidate must carry released bytes", nil)
		}
		if candidate.Selected && candidate.Disposition != CandidateAccepted {
			return newValidationError(field+".selected", ValidationCodeInvalidValue, "only accepted candidates can be selected", nil)
		}
		if candidate.Selected {
			used += candidate.TextBytes
		}
	}
	if used != trail.UsedBytes {
		return retrievalValidationError("used_bytes", ValidationCodeInvalidRange, "disagrees with the selected candidates")
	}
	selected := make([]bool, len(trail.Candidates))
	for index, candidate := range trail.Candidates {
		selected[index] = candidate.Selected
	}
	packetID, err := NewContextPacketID(trail.Candidates, trail.BudgetBytes, selected)
	if err != nil {
		return err
	}
	if packetID.String() != trail.PacketID {
		return newValidationError("packet_id", ValidationCodeInvalidID, "does not match the packed selection", nil)
	}
	expected, err := NewSourceTrailID(trail)
	if err != nil {
		return err
	}
	if expected != trail.ID {
		return newValidationError("id", ValidationCodeInvalidID, "does not match the trail content", nil)
	}
	return nil
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

// NewSourceTrailID derives the trail identity from every bound field and every ordered candidate.
func NewSourceTrailID(trail SourceTrail) (SourceTrailID, error) {
	if trail.Schema != SourceTrailSchema {
		return SourceTrailID{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.source_trail.v1", nil)
	}
	requestID, err := ParsePolicyEvaluationRequestID(trail.RequestID)
	if err != nil {
		return SourceTrailID{}, prefixValidationError(err, "request_id")
	}
	decisionID, err := ParsePolicyDecisionID(trail.DecisionID)
	if err != nil {
		return SourceTrailID{}, prefixValidationError(err, "decision_id")
	}
	packetID, err := ParseContextPacketID(trail.PacketID)
	if err != nil {
		return SourceTrailID{}, prefixValidationError(err, "packet_id")
	}
	if trail.Outcome != string(PolicyOutcomeAllow) && trail.Outcome != string(PolicyOutcomeDeny) {
		return SourceTrailID{}, newValidationError("outcome", ValidationCodeInvalidValue, "must be allow or deny", nil)
	}
	digest := sha256.New()
	var budget, used [8]byte
	binary.BigEndian.PutUint64(budget[:], trail.BudgetBytes)
	binary.BigEndian.PutUint64(used[:], trail.UsedBytes)
	fields := [][]byte{
		[]byte(trail.Schema),
		requestID[:],
		decisionID[:],
		[]byte(trail.Outcome),
		[]byte(trail.Expression),
		budget[:],
		used[:],
		packetID[:],
	}
	for index, candidate := range trail.Candidates {
		var rank, bytes, selected [8]byte
		binary.BigEndian.PutUint64(rank[:], uint64(candidate.FinalRank))
		binary.BigEndian.PutUint64(bytes[:], candidate.TextBytes)
		if candidate.Selected {
			selected[0] = 1
		}
		fields = append(fields,
			[]byte("candidate"),
			candidate.SegmentID[:],
			candidate.ContentSHA256[:],
			rank[:],
			bytes[:],
			[]byte(candidate.Disposition),
			selected[:],
		)
		for _, reason := range candidate.Reasons {
			fields = append(fields, []byte(reason))
		}
		fields = append(fields, []byte("end"))
		_ = index
	}
	writeTuple(digest, fields...)
	return SourceTrailID(digest.Sum(nil)), nil
}

// EncodeSourceTrail returns the exact canonical trail bytes.
func EncodeSourceTrail(trail SourceTrail) ([]byte, error) {
	if err := trail.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(trail, "source trail")
}

// DecodeSourceTrail decodes one canonical stored trail and validates its complete explanation.
func DecodeSourceTrail(data []byte) (SourceTrail, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return SourceTrail{}, err
	}
	var wire struct {
		Schema      string `json:"schema"`
		ID          string `json:"id"`
		RequestID   string `json:"request_id"`
		DecisionID  string `json:"decision_id"`
		Outcome     string `json:"outcome"`
		Expression  string `json:"expression"`
		BudgetBytes uint64 `json:"budget_bytes"`
		UsedBytes   uint64 `json:"used_bytes"`
		PacketID    string `json:"packet_id"`
		Candidates  []struct {
			SegmentID     string   `json:"segment_id"`
			ContentSHA256 string   `json:"content_sha256"`
			FinalRank     int      `json:"final_rank"`
			TextBytes     uint64   `json:"text_bytes"`
			Disposition   string   `json:"disposition"`
			Reasons       []string `json:"reasons"`
			Selected      bool     `json:"selected"`
		} `json:"candidates"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return SourceTrail{}, err
	}
	if wire.Schema != SourceTrailSchema {
		return SourceTrail{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.source_trail.v1", nil)
	}
	id, err := ParseSourceTrailID(wire.ID)
	if err != nil {
		return SourceTrail{}, validationErrorForField(err, "id")
	}
	trail := SourceTrail{
		Schema:      wire.Schema,
		ID:          id,
		RequestID:   wire.RequestID,
		DecisionID:  wire.DecisionID,
		Outcome:     wire.Outcome,
		Expression:  wire.Expression,
		BudgetBytes: wire.BudgetBytes,
		UsedBytes:   wire.UsedBytes,
		PacketID:    wire.PacketID,
	}
	trail.Candidates = make([]TrailCandidate, len(wire.Candidates))
	for index, candidate := range wire.Candidates {
		field := "candidates"
		segmentID, err := ParseSegmentID(candidate.SegmentID)
		if err != nil {
			return SourceTrail{}, validationErrorForField(prefixValidationError(err, field), field)
		}
		content, err := ParseSHA256(candidate.ContentSHA256)
		if err != nil {
			return SourceTrail{}, validationErrorForField(prefixValidationError(err, field), field)
		}
		entry := TrailCandidate{
			SegmentID:     segmentID,
			ContentSHA256: content,
			FinalRank:     candidate.FinalRank,
			TextBytes:     candidate.TextBytes,
			Disposition:   CandidateDisposition(candidate.Disposition),
		}
		if len(candidate.Reasons) > 0 {
			entry.Reasons = make([]LifecycleReason, len(candidate.Reasons))
			for reasonIndex, reason := range candidate.Reasons {
				entry.Reasons[reasonIndex] = LifecycleReason(reason)
			}
		}
		entry.Selected = candidate.Selected
		trail.Candidates[index] = entry
	}
	if err := trail.Validate(); err != nil {
		return SourceTrail{}, err
	}
	return trail, nil
}

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
	ContextPacketSchema   = "mousa.context_packet.v1"
	ContextPacketSchemaV2 = "mousa.context_packet.v2"
	SourceTrailSchema     = "mousa.source_trail.v1"
	SourceTrailSchemaV2   = "mousa.source_trail.v2"
	SourceTrailSchemaV3   = "mousa.source_trail.v3"
	SourceTrailSchemaV4   = "mousa.source_trail.v4"
	PackingOriginal       = "original"
	PackingExactV1        = "exact-v1"
)

// isSourceTrailSchema reports whether one schema literal is a supported Source Trail version.
func isSourceTrailSchema(schema string) bool {
	switch schema {
	case SourceTrailSchema, SourceTrailSchemaV2, SourceTrailSchemaV3, SourceTrailSchemaV4:
		return true
	}
	return false
}

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
// that do not fit and continuing. Rejected candidates are never selected. This is the original
// released-text byte packing policy; exact-content packing requires verified text in NewSourceTrail.
func PackVerifiedLexicalCandidates(candidates []TrailCandidate, budgetBytes uint64) (PacketPlan, error) {
	return packAcceptedCandidates(candidates, budgetBytes, nil)
}

// NewContextPacketID binds the budget and ordered selected segment identities, digests, ranks
// and byte lengths. Request, decision and packing policy are bound by the trail instead.
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
	Omission      string               `json:"omission,omitempty"`
	DuplicateOf   string               `json:"duplicate_of,omitempty"`
}

// SourceTrail is one immutable record of how a context packet was produced: the enforced decision,
// the search expression, the pack budget accounting, and every considered candidate with its
// disposition and selection. Released text is never copied into the record. A v3 trail additionally
// records the association stage: considered associated passages and the declared relationships that
// released nothing. A v4 trail records the consulted supersession evidence instead: an opt-in trail
// always carries one closed Supersession member, and it carries no associated passages.
type SourceTrail struct {
	Schema               string                 `json:"schema"`
	ID                   SourceTrailID          `json:"id"`
	RequestID            string                 `json:"request_id"`
	DecisionID           string                 `json:"decision_id"`
	Outcome              string                 `json:"outcome"`
	Expression           string                 `json:"expression"`
	BudgetBytes          uint64                 `json:"budget_bytes"`
	UsedBytes            uint64                 `json:"used_bytes"`
	PacketID             string                 `json:"packet_id"`
	Candidates           []TrailCandidate       `json:"candidates"`
	PackingPolicy        string                 `json:"packing_policy,omitempty"`
	Associated           []TrailAssociated      `json:"associated,omitempty"`
	AssociationOmissions []AssociationOmission  `json:"association_omissions,omitempty"`
	Supersession         *SupersessionSelection `json:"supersession,omitempty"`
}

// trailCandidatesFromVerified maps one verified candidate list onto the text-free trail rows the
// default and opted-in Trace stages share. A rejected candidate carries no released text, so its
// recorded size is zero; every accepted candidate records its canonical released size. The result is
// never nil, so an empty trail encodes an explicit empty candidate array.
func trailCandidatesFromVerified(candidates []VerifiedLexicalCandidate) []TrailCandidate {
	rows := make([]TrailCandidate, len(candidates))
	for index, candidate := range candidates {
		textBytes := uint64(len(candidate.Text))
		if candidate.Disposition == CandidateRejected {
			textBytes = 0
		}
		rows[index] = TrailCandidate{
			SegmentID:     candidate.Segment.ID,
			ContentSHA256: candidate.Segment.ContentSHA256,
			FinalRank:     candidate.FinalRank,
			TextBytes:     textBytes,
			Disposition:   candidate.Disposition,
			Reasons:       append([]LifecycleReason(nil), candidate.Reasons...),
		}
	}
	return rows
}

// packTraceCandidates is the Trace stage's Pack step: it applies the recorded packing policy to the
// trail's candidate rows and returns the selection, the released total and the packet identity.
// suppressed marks candidates removed from the surviving set before packing, as the opt-in Trace
// stage does for withheld candidates; the default Trace stage suppresses none.
func packTraceCandidates(trail []TrailCandidate, candidates []VerifiedLexicalCandidate, budgetBytes uint64, packingPolicy string, suppressed []bool) (PacketPlan, error) {
	if packingPolicy == PackingExactV1 {
		if err := packExact(trail, candidates, budgetBytes, suppressed); err != nil {
			return PacketPlan{}, err
		}
		plan := PacketPlan{Selected: make([]bool, len(trail))}
		for index, candidate := range trail {
			plan.Selected[index] = candidate.Selected
			if candidate.Selected {
				plan.UsedBytes += candidate.TextBytes
			}
		}
		id, err := NewContextPacketID(trail, budgetBytes, plan.Selected)
		if err != nil {
			return PacketPlan{}, err
		}
		plan.PacketID = id
		return plan, nil
	}
	return packAcceptedCandidates(trail, budgetBytes, suppressed)
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
func NewSourceTrail(request PolicyEvaluationRequest, decision PolicyDecision, expression string, candidates []VerifiedLexicalCandidate, budgetBytes uint64, packingPolicy string) (SourceTrail, error) {
	if packingPolicy != PackingOriginal && packingPolicy != PackingExactV1 {
		return SourceTrail{}, retrievalValidationError("packing_policy", ValidationCodeInvalidValue, "must be original or exact-v1")
	}
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
	trailCandidates := trailCandidatesFromVerified(candidates)
	if budgetBytes == 0 {
		return SourceTrail{}, retrievalValidationError("budget_bytes", ValidationCodeInvalidRange, "must be positive")
	}
	plan, err := packTraceCandidates(trailCandidates, candidates, budgetBytes, packingPolicy, nil)
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
	if packingPolicy == PackingExactV1 {
		trail.Schema = SourceTrailSchemaV2
		trail.PackingPolicy = packingPolicy
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

// NewSourceTrailWithSupersession is the opt-in Trace stage: it records the consulted supersession
// evidence, packs only the surviving candidates under the requested policy, and builds one immutable
// v4 trail whose identity binds the complete explanation, including the consultation and every
// suppression row.
//
// The consultation member is derived by BuildSupersessionSelection from the request's source, the
// decision's outcome, the already verified candidates and the explicitly supplied verified activation
// state and declaration, so the exact-pin match exists once instead of being reimplemented here. A
// non-allow outcome is recorded unconsulted and rejects supplied administrative records or candidates;
// an allow outcome distinguishes verified no-history (nil state), deactivation (a state naming no
// declaration) and an active declaration. The opt-in is not authorization: the outcome, the request,
// the decision and the packet identity keep their existing construction, and a request that releases
// the same selection keeps the packet identity a default query would produce.
//
// This constructor takes no association input, so an opted-in trail always records no associated
// passages and no association omissions; rejecting that combination belongs to the retrieval caller
// that would accept association declarations, which does not exist yet.
//
// Like NewSourceTrail it checks structure, accounting and identity only: it reads no store, so it
// proves no stored existence, canonical ancestry, authorization or transaction-local current state,
// and validation of a decoded v4 record stays structural for the same reason.
func NewSourceTrailWithSupersession(request PolicyEvaluationRequest, decision PolicyDecision, expression string, candidates []VerifiedLexicalCandidate, budgetBytes uint64, packingPolicy string, activation *SupersessionActivationState, declaration *SupersessionDeclaration) (SourceTrail, error) {
	if packingPolicy != PackingOriginal && packingPolicy != PackingExactV1 {
		return SourceTrail{}, retrievalValidationError("packing_policy", ValidationCodeInvalidValue, "must be original or exact-v1")
	}
	if err := request.Validate(); err != nil {
		return SourceTrail{}, prefixValidationError(err, "request")
	}
	if err := decision.Validate(); err != nil {
		return SourceTrail{}, prefixValidationError(err, "decision")
	}
	if decision.Request != request {
		return SourceTrail{}, newValidationError("decision", ValidationCodeInvalidValue, "decision belongs to another request", nil)
	}
	if budgetBytes == 0 {
		return SourceTrail{}, retrievalValidationError("budget_bytes", ValidationCodeInvalidRange, "must be positive")
	}
	if !utf8.ValidString(expression) || len(expression) < 1 || len(expression) > 4096 {
		return SourceTrail{}, retrievalValidationError("expression", ValidationCodeInvalidRange, "must be non-empty valid UTF-8 within bounds")
	}
	selection, err := BuildSupersessionSelection(request.SourceID, decision.Outcome, candidates, activation, declaration)
	if err != nil {
		return SourceTrail{}, prefixValidationError(err, "supersession")
	}
	if candidates == nil {
		candidates = []VerifiedLexicalCandidate{}
	}
	trailCandidates := trailCandidatesFromVerified(candidates)
	suppressed, err := suppressSupersededCandidates(trailCandidates, &selection)
	if err != nil {
		return SourceTrail{}, err
	}
	plan, err := packTraceCandidates(trailCandidates, candidates, budgetBytes, packingPolicy, suppressed)
	if err != nil {
		return SourceTrail{}, err
	}
	for index := range trailCandidates {
		trailCandidates[index].Selected = plan.Selected[index]
	}
	trail := SourceTrail{
		Schema:        SourceTrailSchemaV4,
		RequestID:     request.ID.String(),
		DecisionID:    decision.ID.String(),
		Outcome:       string(decision.Outcome),
		Expression:    expression,
		BudgetBytes:   budgetBytes,
		UsedBytes:     plan.UsedBytes,
		PacketID:      plan.PacketID.String(),
		Candidates:    trailCandidates,
		PackingPolicy: packingPolicy,
		Supersession:  &selection,
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

// Validate checks text-free structure, accounting and identities. Exact equality must be
// checked against verified content at creation; an identity is not proof of that assertion.
func (trail SourceTrail) Validate() error {
	if !isSourceTrailSchema(trail.Schema) {
		return newValidationError("schema", ValidationCodeInvalidSchema, "unsupported source trail version", nil)
	}
	if trail.Schema == SourceTrailSchemaV4 {
		if trail.Supersession == nil {
			return newValidationError("supersession", ValidationCodeInvalidValue, "is required for source trail v4", nil)
		}
		if err := trail.Supersession.Validate(); err != nil {
			return prefixValidationError(err, "supersession")
		}
	} else if trail.Supersession != nil {
		return newValidationError("supersession", ValidationCodeInvalidSchema, "field requires source trail v4", nil)
	}
	if err := trail.validatePacking(); err != nil {
		return err
	}
	if trail.Schema == SourceTrailSchemaV3 {
		if err := trail.validateAssociated(); err != nil {
			return err
		}
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
	if trail.Schema == SourceTrailSchemaV4 && trail.Supersession.Consulted != (trail.Outcome == string(PolicyOutcomeAllow)) {
		// The flag is recorded, never inferred: an allow consulted supersession and a deny did not.
		return newValidationError("supersession.consulted", ValidationCodeInvalidValue, "must be true exactly when the decision allowed the source", nil)
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
	for _, row := range trail.Associated {
		if row.Selected {
			used += row.TextBytes
		}
	}
	if used != trail.UsedBytes {
		return retrievalValidationError("used_bytes", ValidationCodeInvalidRange, "disagrees with the selected passages")
	}
	selected := make([]bool, len(trail.Candidates))
	for index, candidate := range trail.Candidates {
		selected[index] = candidate.Selected
	}
	packetID, err := trail.packetIdentity()
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

// packetIdentity re-derives the packet identity of a validated trail: the v1 domain for
// v1/v2 records, and the v2 domain that also binds associated rows for v3.
func (trail SourceTrail) packetIdentity() (ContextPacketID, error) {
	if trail.Schema == SourceTrailSchemaV3 {
		return contextPacketIDV3(trail)
	}
	selected := make([]bool, len(trail.Candidates))
	for index, candidate := range trail.Candidates {
		selected[index] = candidate.Selected
	}
	return NewContextPacketID(trail.Candidates, trail.BudgetBytes, selected)
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

// NewSourceTrailID derives the trail identity from every bound field and every ordered candidate.
func NewSourceTrailID(trail SourceTrail) (SourceTrailID, error) {
	if !isSourceTrailSchema(trail.Schema) {
		return SourceTrailID{}, newValidationError("schema", ValidationCodeInvalidSchema, "unsupported source trail version", nil)
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
	if trail.Schema != SourceTrailSchema {
		fields = append(fields, []byte(trail.PackingPolicy))
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
		if trail.Schema == SourceTrailSchemaV2 || trail.Schema == SourceTrailSchemaV4 {
			fields = append(fields, []byte(candidate.Omission), []byte(candidate.DuplicateOf))
		}
		for _, reason := range candidate.Reasons {
			fields = append(fields, []byte(reason))
		}
		fields = append(fields, []byte("end"))
		_ = index
	}
	if trail.Schema == SourceTrailSchemaV3 {
		for _, row := range trail.Associated {
			var bytes, selected [8]byte
			binary.BigEndian.PutUint64(bytes[:], row.TextBytes)
			if row.Selected {
				selected[0] = 1
			}
			fields = append(fields,
				[]byte("associated"),
				row.SegmentID[:],
				row.ContentSHA256[:],
				bytes[:],
				selected[:],
				[]byte(row.Omission),
				[]byte(row.DuplicateOf),
				[]byte(row.FromItem),
				[]byte(row.ToItem),
				[]byte(row.Basis),
				[]byte(row.Author),
				[]byte("end"),
			)
		}
		for _, omission := range trail.AssociationOmissions {
			fields = append(fields,
				[]byte("omission"),
				[]byte(omission.FromItem),
				[]byte(omission.ToItem),
				[]byte(omission.Reason),
			)
		}
	}
	if trail.Schema == SourceTrailSchemaV4 {
		if trail.Supersession == nil {
			return SourceTrailID{}, newValidationError("supersession", ValidationCodeInvalidValue, "is required for source trail v4", nil)
		}
		fields = append(fields, supersessionIdentityFields(trail.Supersession)...)
	}
	writeTuple(digest, fields...)
	return SourceTrailID(digest.Sum(nil)), nil
}

// supersessionIdentityFields returns the v4 identity fields for one consultation member: the
// consultation flag, both nullable identities and every disposition row in recorded order. A nil
// identity is written as an empty field and a present identity is always 32 nonzero bytes, so
// absence and presence cannot collide; the shared writeTuple length-delimits every field.
func supersessionIdentityFields(selection *SupersessionSelection) [][]byte {
	consulted := []byte{0}
	if selection.Consulted {
		consulted[0] = 1
	}
	fields := [][]byte{
		[]byte("supersession"),
		consulted,
		supersessionActivationIdentityBytes(selection.ActivationID),
		supersessionDeclarationIdentityBytes(selection.DeclarationID),
	}
	for _, disposition := range selection.Dispositions {
		fields = append(fields,
			[]byte("disposition"),
			[]byte(disposition.Selection),
			disposition.SegmentID[:],
			disposition.ContentSHA256[:],
			disposition.DeclarationID[:],
			[]byte(disposition.PredecessorItemID),
			disposition.PredecessorRepresentationID[:],
			[]byte(disposition.SuccessorItemID),
			disposition.SuccessorRepresentationID[:],
			[]byte("end"),
		)
	}
	return fields
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
		Schema        string          `json:"schema"`
		ID            string          `json:"id"`
		RequestID     string          `json:"request_id"`
		DecisionID    string          `json:"decision_id"`
		Outcome       string          `json:"outcome"`
		Expression    string          `json:"expression"`
		BudgetBytes   uint64          `json:"budget_bytes"`
		UsedBytes     uint64          `json:"used_bytes"`
		PacketID      string          `json:"packet_id"`
		PackingPolicy json.RawMessage `json:"packing_policy"`
		Associated    []struct {
			SegmentID     string          `json:"segment_id"`
			ContentSHA256 string          `json:"content_sha256"`
			TextBytes     uint64          `json:"text_bytes"`
			Selected      bool            `json:"selected"`
			Omission      json.RawMessage `json:"omission"`
			DuplicateOf   json.RawMessage `json:"duplicate_of"`
			FromItem      string          `json:"from_item"`
			ToItem        string          `json:"to_item"`
			Basis         string          `json:"basis"`
			Author        string          `json:"author"`
		} `json:"associated"`
		AssociationOmissions []struct {
			FromItem string `json:"from_item"`
			ToItem   string `json:"to_item"`
			Reason   string `json:"reason"`
		} `json:"association_omissions"`
		Supersession json.RawMessage `json:"supersession"`
		Candidates   []struct {
			SegmentID     string          `json:"segment_id"`
			ContentSHA256 string          `json:"content_sha256"`
			FinalRank     int             `json:"final_rank"`
			TextBytes     uint64          `json:"text_bytes"`
			Disposition   string          `json:"disposition"`
			Reasons       []string        `json:"reasons"`
			Selected      bool            `json:"selected"`
			Omission      json.RawMessage `json:"omission"`
			DuplicateOf   json.RawMessage `json:"duplicate_of"`
		} `json:"candidates"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return SourceTrail{}, err
	}
	if !isSourceTrailSchema(wire.Schema) {
		return SourceTrail{}, newValidationError("schema", ValidationCodeInvalidSchema, "unsupported source trail version", nil)
	}
	decodePackingField := func(field string, raw json.RawMessage) (string, error) {
		if len(raw) == 0 {
			return "", nil
		}
		if wire.Schema == SourceTrailSchema {
			return "", newValidationError(field, ValidationCodeInvalidSchema, "field requires source trail v2", nil)
		}
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil || value == nil {
			return "", newValidationError(field, ValidationCodeInvalidValue, "must be a string", err)
		}
		return *value, nil
	}
	decodeAssociationField := func(field string, present bool) error {
		if !present {
			return nil
		}
		if wire.Schema != SourceTrailSchemaV3 {
			return newValidationError(field, ValidationCodeInvalidSchema, "field requires source trail v3", nil)
		}
		return nil
	}
	packingPolicy, err := decodePackingField("packing_policy", wire.PackingPolicy)
	if err != nil {
		return SourceTrail{}, err
	}
	id, err := ParseSourceTrailID(wire.ID)
	if err != nil {
		return SourceTrail{}, validationErrorForField(err, "id")
	}
	trail := SourceTrail{
		Schema:        wire.Schema,
		ID:            id,
		RequestID:     wire.RequestID,
		DecisionID:    wire.DecisionID,
		Outcome:       wire.Outcome,
		Expression:    wire.Expression,
		BudgetBytes:   wire.BudgetBytes,
		UsedBytes:     wire.UsedBytes,
		PacketID:      wire.PacketID,
		PackingPolicy: packingPolicy,
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
		omission, err := decodePackingField(field+".omission", candidate.Omission)
		if err != nil {
			return SourceTrail{}, err
		}
		duplicateOf, err := decodePackingField(field+".duplicate_of", candidate.DuplicateOf)
		if err != nil {
			return SourceTrail{}, err
		}
		entry := TrailCandidate{
			SegmentID:     segmentID,
			ContentSHA256: content,
			FinalRank:     candidate.FinalRank,
			TextBytes:     candidate.TextBytes,
			Disposition:   CandidateDisposition(candidate.Disposition),
			Omission:      omission,
			DuplicateOf:   duplicateOf,
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
	if err := decodeAssociationField("associated", len(wire.Associated) > 0); err != nil {
		return SourceTrail{}, err
	}
	if len(wire.Associated) > 0 {
		trail.Associated = make([]TrailAssociated, len(wire.Associated))
	}
	for index, row := range wire.Associated {
		field := "associated"
		segmentID, err := ParseSegmentID(row.SegmentID)
		if err != nil {
			return SourceTrail{}, validationErrorForField(prefixValidationError(err, field), field)
		}
		content, err := ParseSHA256(row.ContentSHA256)
		if err != nil {
			return SourceTrail{}, validationErrorForField(prefixValidationError(err, field), field)
		}
		omission, err := decodePackingField(field+".omission", row.Omission)
		if err != nil {
			return SourceTrail{}, err
		}
		duplicateOf, err := decodePackingField(field+".duplicate_of", row.DuplicateOf)
		if err != nil {
			return SourceTrail{}, err
		}
		trail.Associated[index] = TrailAssociated{
			SegmentID:     segmentID,
			ContentSHA256: content,
			TextBytes:     row.TextBytes,
			Selected:      row.Selected,
			Omission:      omission,
			DuplicateOf:   duplicateOf,
			FromItem:      row.FromItem,
			ToItem:        row.ToItem,
			Basis:         row.Basis,
			Author:        row.Author,
		}
	}
	if err := decodeAssociationField("association_omissions", len(wire.AssociationOmissions) > 0); err != nil {
		return SourceTrail{}, err
	}
	if len(wire.AssociationOmissions) > 0 {
		trail.AssociationOmissions = make([]AssociationOmission, len(wire.AssociationOmissions))
	}
	for index, omission := range wire.AssociationOmissions {
		trail.AssociationOmissions[index] = AssociationOmission{
			FromItem: omission.FromItem, ToItem: omission.ToItem, Reason: omission.Reason,
		}
	}
	if wire.Supersession != nil {
		if wire.Schema != SourceTrailSchemaV4 {
			return SourceTrail{}, newValidationError("supersession", ValidationCodeInvalidSchema, "field requires source trail v4", nil)
		}
		selection, err := decodeSupersessionSelection(wire.Supersession)
		if err != nil {
			return SourceTrail{}, err
		}
		trail.Supersession = selection
	} else if wire.Schema == SourceTrailSchemaV4 {
		return SourceTrail{}, newValidationError("supersession", ValidationCodeInvalidValue, "is required for source trail v4", nil)
	}
	if err := trail.Validate(); err != nil {
		return SourceTrail{}, err
	}
	return trail, nil
}

// decodeSupersessionSelection decodes the v4 consultation member into its canonical output. Every
// member is required: the consultation flag, both identities as an explicit null or a lowercase
// identity, and the disposition array, which null cannot stand in for. Unknown or duplicate keys,
// mistyped values, zero identities and a non-array disposition field are rejected. This is a
// structural codec: it cannot show that a named activation event, declaration or pinned revision
// exists, belongs to the trail's source, or was authorized.
func decodeSupersessionSelection(data []byte) (*SupersessionSelection, error) {
	if string(data) == "null" {
		return nil, newValidationError("supersession", ValidationCodeInvalidValue, "must be an object, not null", nil)
	}
	var wire struct {
		Consulted     *bool           `json:"consulted"`
		ActivationID  json.RawMessage `json:"activation_id"`
		DeclarationID json.RawMessage `json:"declaration_id"`
		Dispositions  json.RawMessage `json:"dispositions"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return nil, prefixValidationError(err, "supersession")
	}
	if wire.Consulted == nil || wire.ActivationID == nil || wire.DeclarationID == nil || wire.Dispositions == nil {
		return nil, newValidationError("supersession", ValidationCodeInvalidValue, "every member must be present, including explicit nulls", nil)
	}
	if string(wire.Dispositions) == "null" {
		return nil, newValidationError("supersession.dispositions", ValidationCodeInvalidValue, "must be an array, not null", nil)
	}
	activationID, err := decodeNullableSupersessionActivationID(wire.ActivationID)
	if err != nil {
		return nil, validationErrorForField(err, "supersession.activation_id")
	}
	declarationID, err := decodeNullableSupersessionDeclarationID(wire.DeclarationID)
	if err != nil {
		return nil, validationErrorForField(err, "supersession.declaration_id")
	}
	var dispositions []SupersessionDisposition
	if err := decodeJSONContract(wire.Dispositions, &dispositions); err != nil {
		return nil, prefixValidationError(err, "supersession.dispositions")
	}
	selection := &SupersessionSelection{
		Consulted:     *wire.Consulted,
		ActivationID:  activationID,
		DeclarationID: declarationID,
		Dispositions:  dispositions,
	}
	if err := selection.Validate(); err != nil {
		return nil, prefixValidationError(err, "supersession")
	}
	return selection, nil
}

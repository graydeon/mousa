package mousa

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"slices"
)

const (
	// PolicyEvaluationRequestSchema names one validated transient policy evaluation request.
	PolicyEvaluationRequestSchema = "mousa.policy_evaluation_request.v1"
	// PolicyDecisionSchema names one immutable stored policy decision.
	PolicyDecisionSchema = "mousa.policy_decision.v3"
	// SourceRetrievalAction is the only action the source retrieval evaluator accepts.
	SourceRetrievalAction = "source.retrieve"
	// PolicyEvaluatorID and PolicyEvaluatorVersion identify the source-retrieval evaluator.
	PolicyEvaluatorID      = "mousa.policy.source_retrieval"
	PolicyEvaluatorVersion = "3"
)

// PolicyDecisionOutcome names the closed set of decision outcomes.
type PolicyDecisionOutcome string

const (
	PolicyOutcomeAllow PolicyDecisionOutcome = "allow"
	PolicyOutcomeDeny  PolicyDecisionOutcome = "deny"
)

// PolicyDecisionReason names the closed set of decision reason codes.
type PolicyDecisionReason string

const (
	PolicyReasonAllow              PolicyDecisionReason = "allow"
	PolicyReasonMissingSourceState PolicyDecisionReason = "missing_source_state"
	PolicyReasonSourceWithdrawn    PolicyDecisionReason = "source_withdrawn"
	PolicyReasonAccessDenied       PolicyDecisionReason = "access_denied"
	PolicyReasonAccessDefault      PolicyDecisionReason = "access_default"
)

// SourceAccess is the state an access event sets for a source. A source with no access event
// is denied.
type SourceAccess string

const (
	SourceAccessAllow SourceAccess = "allow"
	SourceAccessDeny  SourceAccess = "deny"
)

// Validate rejects any value outside the closed access vocabulary.
func (access SourceAccess) Validate() error {
	if access != SourceAccessAllow && access != SourceAccessDeny {
		return newValidationError("access", ValidationCodeInvalidEnum, "must be allow or deny", nil)
	}
	return nil
}

type PolicyEvaluationRequestID [sha256.Size]byte

// PolicyEvaluationRequest is one validated transient evaluation input. It is never stored on its own.
type PolicyEvaluationRequest struct {
	Schema            string                    `json:"schema"`
	ID                PolicyEvaluationRequestID `json:"id"`
	Action            string                    `json:"action"`
	CallerNamespace   string                    `json:"caller_namespace"`
	ExternalCallerID  string                    `json:"external_caller_id"`
	ExternalRequestID string                    `json:"external_request_id"`
	PurposeNamespace  string                    `json:"purpose_namespace"`
	ExternalPurposeID string                    `json:"external_purpose_id"`
	SourceID          SourceID                  `json:"source_id"`
	RequestedAtUsec   int64                     `json:"requested_at_usec"`
}

// NewPolicyEvaluationRequestID derives the request identity from every request field except the ID.
func NewPolicyEvaluationRequestID(request PolicyEvaluationRequest) (PolicyEvaluationRequestID, error) {
	if request.Schema != PolicyEvaluationRequestSchema {
		return PolicyEvaluationRequestID{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_evaluation_request.v1", nil)
	}
	for _, value := range []struct{ field, value string }{
		{"action", request.Action},
		{"caller_namespace", request.CallerNamespace},
		{"external_caller_id", request.ExternalCallerID},
		{"external_request_id", request.ExternalRequestID},
		{"purpose_namespace", request.PurposeNamespace},
		{"external_purpose_id", request.ExternalPurposeID},
	} {
		if err := validateIdentityInput(value.field, value.value); err != nil {
			return PolicyEvaluationRequestID{}, err
		}
	}
	if request.Action != SourceRetrievalAction {
		return PolicyEvaluationRequestID{}, newValidationError("action", ValidationCodeInvalidEnum, "must be source.retrieve", nil)
	}
	if request.SourceID == (SourceID{}) {
		return PolicyEvaluationRequestID{}, newValidationError("source_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if request.RequestedAtUsec <= 0 {
		return PolicyEvaluationRequestID{}, newValidationError("requested_at_usec", ValidationCodeInvalidRange, "must be positive", nil)
	}
	var time [8]byte
	binary.BigEndian.PutUint64(time[:], uint64(request.RequestedAtUsec))
	digest := sha256.New()
	writeTuple(digest,
		[]byte(request.Schema),
		[]byte(request.Action),
		[]byte(request.CallerNamespace),
		[]byte(request.ExternalCallerID),
		[]byte(request.ExternalRequestID),
		[]byte(request.PurposeNamespace),
		[]byte(request.ExternalPurposeID),
		request.SourceID[:],
		time[:],
	)
	return PolicyEvaluationRequestID(digest.Sum(nil)), nil
}

// Validate recomputes the request identity and enforces the closed request vocabulary.
func (request PolicyEvaluationRequest) Validate() error {
	if request.Schema != PolicyEvaluationRequestSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_evaluation_request.v1", nil)
	}
	if request.ID == (PolicyEvaluationRequestID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	expected, err := NewPolicyEvaluationRequestID(request)
	if err != nil {
		return err
	}
	if request.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match policy evaluation request identity", nil)
	}
	return nil
}

func (id PolicyEvaluationRequestID) String() string { return hex.EncodeToString(id[:]) }

func ParsePolicyEvaluationRequestID(value string) (PolicyEvaluationRequestID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return PolicyEvaluationRequestID{}, err
	}
	return PolicyEvaluationRequestID(decoded), nil
}

func (id PolicyEvaluationRequestID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *PolicyEvaluationRequestID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParsePolicyEvaluationRequestID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// DecodePolicyEvaluationRequest decodes one canonical request payload.
func DecodePolicyEvaluationRequest(data []byte) (PolicyEvaluationRequest, error) {
	var wire struct {
		Schema            string `json:"schema"`
		ID                string `json:"id"`
		Action            string `json:"action"`
		CallerNamespace   string `json:"caller_namespace"`
		ExternalCallerID  string `json:"external_caller_id"`
		ExternalRequestID string `json:"external_request_id"`
		PurposeNamespace  string `json:"purpose_namespace"`
		ExternalPurposeID string `json:"external_purpose_id"`
		SourceID          string `json:"source_id"`
		RequestedAtUsec   int64  `json:"requested_at_usec"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return PolicyEvaluationRequest{}, err
	}
	if wire.Schema != PolicyEvaluationRequestSchema {
		return PolicyEvaluationRequest{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_evaluation_request.v1", nil)
	}
	id, err := ParsePolicyEvaluationRequestID(wire.ID)
	if err != nil {
		return PolicyEvaluationRequest{}, validationErrorForField(err, "id")
	}
	sourceID, err := ParseSourceID(wire.SourceID)
	if err != nil {
		return PolicyEvaluationRequest{}, validationErrorForField(err, "source_id")
	}
	request := PolicyEvaluationRequest{
		Schema:            wire.Schema,
		ID:                id,
		Action:            wire.Action,
		CallerNamespace:   wire.CallerNamespace,
		ExternalCallerID:  wire.ExternalCallerID,
		ExternalRequestID: wire.ExternalRequestID,
		PurposeNamespace:  wire.PurposeNamespace,
		ExternalPurposeID: wire.ExternalPurposeID,
		SourceID:          sourceID,
		RequestedAtUsec:   wire.RequestedAtUsec,
	}
	if err := request.Validate(); err != nil {
		return PolicyEvaluationRequest{}, err
	}
	return request, nil
}

// PolicyEvaluationSnapshot is the verified read-only access and lifecycle input for one evaluation.
type PolicyEvaluationSnapshot struct {
	StatePresent        bool
	CollectionState     *CollectionState
	CurrentWithdrawalID *WithdrawalID
	Access              SourceAccess
	AccessEventID       *AccessEventID
}

type PolicyDecisionID [sha256.Size]byte

// PolicyDecision is one immutable evaluation record whose identity binds the complete explanation.
type PolicyDecision struct {
	Schema              string                  `json:"schema"`
	ID                  PolicyDecisionID        `json:"id"`
	Request             PolicyEvaluationRequest `json:"request"`
	EvaluatorID         string                  `json:"evaluator_id"`
	EvaluatorVersion    string                  `json:"evaluator_version"`
	EvaluatedAtUsec     int64                   `json:"evaluated_at_usec"`
	StatePresent        bool                    `json:"state_present"`
	CollectionState     *CollectionState        `json:"collection_state"`
	CurrentWithdrawalID *WithdrawalID           `json:"current_withdrawal_id"`
	Access              SourceAccess            `json:"access"`
	AccessEventID       *AccessEventID          `json:"access_event_id"`
	Outcome             PolicyDecisionOutcome   `json:"outcome"`
	ReasonCodes         []PolicyDecisionReason  `json:"reason_codes"`
}

// NewPolicyDecisionID derives the decision identity from the decision schema, request identity,
// evaluator identity, evaluation time, lifecycle evidence, source access, the cited access event
// (empty for the default), outcome and reasons.
func NewPolicyDecisionID(decision PolicyDecision) (PolicyDecisionID, error) {
	if decision.Schema != PolicyDecisionSchema {
		return PolicyDecisionID{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_decision.v3", nil)
	}
	if decision.Request.ID == (PolicyEvaluationRequestID{}) {
		return PolicyDecisionID{}, newValidationError("request.id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if decision.EvaluatedAtUsec <= 0 {
		return PolicyDecisionID{}, newValidationError("evaluated_at_usec", ValidationCodeInvalidRange, "must be positive", nil)
	}
	digest := sha256.New()
	fields := [][]byte{
		[]byte(decision.Schema),
		decision.Request.ID[:],
		[]byte(decision.EvaluatorID),
		[]byte(decision.EvaluatorVersion),
	}
	var time [8]byte
	binary.BigEndian.PutUint64(time[:], uint64(decision.EvaluatedAtUsec))
	fields = append(fields, time[:])
	fields = append(fields, lifecycleFields(decision)...)
	var event []byte
	if decision.AccessEventID != nil {
		event = decision.AccessEventID[:]
	}
	fields = append(fields, []byte(decision.Access), event, []byte(decision.Outcome))
	for _, reason := range decision.ReasonCodes {
		fields = append(fields, []byte(reason))
	}
	writeTuple(digest, fields...)
	return PolicyDecisionID(digest.Sum(nil)), nil
}

func lifecycleFields(decision PolicyDecision) [][]byte {
	fields := [][]byte{{0}}
	if decision.StatePresent {
		fields[0] = []byte{1}
	}
	if decision.CollectionState != nil {
		fields = append(fields, []byte(*decision.CollectionState))
	} else {
		fields = append(fields, nil)
	}
	if decision.CurrentWithdrawalID != nil {
		fields = append(fields, decision.CurrentWithdrawalID[:])
	} else {
		fields = append(fields, nil)
	}
	return fields
}

// Validate recomputes the decision identity and enforces the closed decision vocabulary and the
// derivable relation between lifecycle evidence, source access, outcome, and reason order.
func (decision PolicyDecision) Validate() error {
	if decision.Schema != PolicyDecisionSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_decision.v3", nil)
	}
	if decision.ID == (PolicyDecisionID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if err := decision.Request.Validate(); err != nil {
		return err
	}
	if decision.EvaluatorID != PolicyEvaluatorID {
		return newValidationError("evaluator_id", ValidationCodeInvalidValue, "must be mousa.policy.source_retrieval", nil)
	}
	if decision.EvaluatorVersion != PolicyEvaluatorVersion {
		return newValidationError("evaluator_version", ValidationCodeInvalidValue, "must be 3", nil)
	}
	if decision.EvaluatedAtUsec <= 0 {
		return newValidationError("evaluated_at_usec", ValidationCodeInvalidRange, "must be positive", nil)
	}
	if decision.StatePresent != (decision.CollectionState != nil) {
		return newValidationError("collection_state", ValidationCodeInvalidValue, "must be present exactly when the source state is present", nil)
	}
	if decision.CollectionState == nil {
		if decision.CurrentWithdrawalID != nil {
			return newValidationError("current_withdrawal_id", ValidationCodeInvalidValue, "must be absent without source state", nil)
		}
	} else {
		switch *decision.CollectionState {
		case CollectionActive:
			if decision.CurrentWithdrawalID != nil {
				return newValidationError("current_withdrawal_id", ValidationCodeInvalidValue, "must be absent while collection is active", nil)
			}
		case CollectionWithdrawn:
			if decision.CurrentWithdrawalID == nil {
				return newValidationError("current_withdrawal_id", ValidationCodeInvalidID, "must not be zero while collection is withdrawn", nil)
			}
		default:
			return newValidationError("collection_state", ValidationCodeInvalidEnum, "must be active or withdrawn", nil)
		}
	}
	if err := decision.Access.Validate(); err != nil {
		return err
	}
	if decision.AccessEventID == nil && decision.Access != SourceAccessDeny {
		return newValidationError("access", ValidationCodeInvalidValue, "must be deny without an access event", nil)
	}
	switch decision.Outcome {
	case PolicyOutcomeAllow, PolicyOutcomeDeny:
	default:
		return newValidationError("outcome", ValidationCodeInvalidEnum, "must be allow or deny", nil)
	}
	wantOutcome, wantReasons := deriveDecisionOutcome(decision.StatePresent, decision.CurrentWithdrawalID, decision.Access, decision.AccessEventID)
	if decision.Outcome != wantOutcome {
		return newValidationError("outcome", ValidationCodeInvalidValue, "does not follow lifecycle evidence and source access", nil)
	}
	if !slices.Equal(decision.ReasonCodes, wantReasons) {
		return newValidationError("reason_codes", ValidationCodeInvalidValue, "do not follow lifecycle evidence and source access", nil)
	}
	expected, err := NewPolicyDecisionID(decision)
	if err != nil {
		return err
	}
	if decision.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match policy decision identity", nil)
	}
	return nil
}

// deriveDecisionOutcome computes the outcome and ordered reason codes: lifecycle checks first,
// then source access. A source with no access event is denied by default. Any reason other than
// allow denies.
func deriveDecisionOutcome(statePresent bool, withdrawal *WithdrawalID, access SourceAccess, event *AccessEventID) (PolicyDecisionOutcome, []PolicyDecisionReason) {
	var reasons []PolicyDecisionReason
	if !statePresent {
		reasons = append(reasons, PolicyReasonMissingSourceState)
	}
	if withdrawal != nil {
		reasons = append(reasons, PolicyReasonSourceWithdrawn)
	}
	switch {
	case event == nil:
		reasons = append(reasons, PolicyReasonAccessDefault)
	case access != SourceAccessAllow:
		reasons = append(reasons, PolicyReasonAccessDenied)
	}
	if len(reasons) == 0 {
		return PolicyOutcomeAllow, []PolicyDecisionReason{PolicyReasonAllow}
	}
	return PolicyOutcomeDeny, reasons
}

// EvaluateSourceRetrieval evaluates one validated request against one verified snapshot. It never
// performs retrieval, releases evidence, or enforces the returned decision.
func EvaluateSourceRetrieval(request PolicyEvaluationRequest, snapshot PolicyEvaluationSnapshot, evaluatedAtUsec int64) (PolicyDecision, error) {
	if err := request.Validate(); err != nil {
		return PolicyDecision{}, err
	}
	if evaluatedAtUsec <= 0 {
		return PolicyDecision{}, newValidationError("evaluated_at_usec", ValidationCodeInvalidRange, "must be positive", nil)
	}
	if err := snapshot.Access.Validate(); err != nil {
		return PolicyDecision{}, err
	}
	outcome, reasons := deriveDecisionOutcome(snapshot.StatePresent, snapshot.CurrentWithdrawalID, snapshot.Access, snapshot.AccessEventID)
	decision := PolicyDecision{
		Schema:              PolicyDecisionSchema,
		Request:             request,
		EvaluatorID:         PolicyEvaluatorID,
		EvaluatorVersion:    PolicyEvaluatorVersion,
		EvaluatedAtUsec:     evaluatedAtUsec,
		StatePresent:        snapshot.StatePresent,
		CollectionState:     cloneCollectionState(snapshot.CollectionState),
		CurrentWithdrawalID: cloneWithdrawalID(snapshot.CurrentWithdrawalID),
		Access:              snapshot.Access,
		AccessEventID:       cloneAccessEventID(snapshot.AccessEventID),
		Outcome:             outcome,
		ReasonCodes:         reasons,
	}
	id, err := NewPolicyDecisionID(decision)
	if err != nil {
		return PolicyDecision{}, err
	}
	decision.ID = id
	if err := decision.Validate(); err != nil {
		return PolicyDecision{}, err
	}
	return decision, nil
}

func cloneAccessEventID(value *AccessEventID) *AccessEventID {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func cloneCollectionState(value *CollectionState) *CollectionState {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// EncodePolicyDecision returns the exact canonical decision bytes.
func EncodePolicyDecision(decision PolicyDecision) ([]byte, error) {
	if err := decision.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(decision, "policy decision")
}

// DecodePolicyDecision decodes one canonical stored decision and validates its complete explanation.
func DecodePolicyDecision(data []byte) (PolicyDecision, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return PolicyDecision{}, err
	}
	var wire struct {
		Schema              string          `json:"schema"`
		ID                  string          `json:"id"`
		Request             json.RawMessage `json:"request"`
		EvaluatorID         string          `json:"evaluator_id"`
		EvaluatorVersion    string          `json:"evaluator_version"`
		EvaluatedAtUsec     int64           `json:"evaluated_at_usec"`
		StatePresent        bool            `json:"state_present"`
		CollectionState     *string         `json:"collection_state"`
		CurrentWithdrawalID *string         `json:"current_withdrawal_id"`
		Access              string          `json:"access"`
		AccessEventID       *AccessEventID  `json:"access_event_id"`
		Outcome             string          `json:"outcome"`
		ReasonCodes         []string        `json:"reason_codes"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return PolicyDecision{}, err
	}
	if wire.Schema != PolicyDecisionSchema {
		return PolicyDecision{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_decision.v3", nil)
	}
	id, err := ParsePolicyDecisionID(wire.ID)
	if err != nil {
		return PolicyDecision{}, validationErrorForField(err, "id")
	}
	request, err := DecodePolicyEvaluationRequest(wire.Request)
	if err != nil {
		return PolicyDecision{}, prefixValidationError(err, "request")
	}
	var collectionState *CollectionState
	if wire.CollectionState != nil {
		state := CollectionState(*wire.CollectionState)
		collectionState = &state
	}
	var withdrawal *WithdrawalID
	if wire.CurrentWithdrawalID != nil {
		parsed, err := ParseWithdrawalID(*wire.CurrentWithdrawalID)
		if err != nil {
			return PolicyDecision{}, validationErrorForField(err, "current_withdrawal_id")
		}
		withdrawal = &parsed
	}
	reasons := make([]PolicyDecisionReason, 0, len(wire.ReasonCodes))
	for _, code := range wire.ReasonCodes {
		reasons = append(reasons, PolicyDecisionReason(code))
	}
	decision := PolicyDecision{
		Schema:              wire.Schema,
		ID:                  id,
		Request:             request,
		EvaluatorID:         wire.EvaluatorID,
		EvaluatorVersion:    wire.EvaluatorVersion,
		EvaluatedAtUsec:     wire.EvaluatedAtUsec,
		StatePresent:        wire.StatePresent,
		CollectionState:     collectionState,
		CurrentWithdrawalID: withdrawal,
		Access:              SourceAccess(wire.Access),
		AccessEventID:       wire.AccessEventID,
		Outcome:             PolicyDecisionOutcome(wire.Outcome),
		ReasonCodes:         reasons,
	}
	if err := decision.Validate(); err != nil {
		return PolicyDecision{}, err
	}
	return decision, nil
}

func (id PolicyDecisionID) String() string { return hex.EncodeToString(id[:]) }

func ParsePolicyDecisionID(value string) (PolicyDecisionID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return PolicyDecisionID{}, err
	}
	return PolicyDecisionID(decoded), nil
}

func (id PolicyDecisionID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *PolicyDecisionID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParsePolicyDecisionID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

package mousa

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

const (
	// PolicyEvaluationRequestSchema names one validated transient policy evaluation request.
	PolicyEvaluationRequestSchema = "mousa.policy_evaluation_request.v1"
	// PolicyDecisionSchema names one immutable stored policy decision.
	PolicyDecisionSchema = "mousa.policy_decision.v1"
	// SourceRetrievalAction is the only action the Phase 11B4 evaluator accepts.
	SourceRetrievalAction = "source.retrieve"
	// SourceRetrievalPolicyMediaType is the only supported policy definition media type.
	SourceRetrievalPolicyMediaType = "application/vnd.mousa.source-retrieval-policy+json"
	// SourceRetrievalPolicySchema is the only supported policy definition inner schema.
	SourceRetrievalPolicySchema = "mousa.policy.source_retrieval.v1"
	// PolicyEvaluatorID and PolicyEvaluatorVersion identify the source-retrieval evaluator.
	PolicyEvaluatorID      = "mousa.policy.source_retrieval"
	PolicyEvaluatorVersion = "1"
)

// PolicyDecisionLayer names the closed set of applicable binding layers.
type PolicyDecisionLayer string

const (
	PolicyLayerDeployment PolicyDecisionLayer = "deployment"
	PolicyLayerSource     PolicyDecisionLayer = "source"
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
	PolicyReasonAllow                       PolicyDecisionReason = "allow"
	PolicyReasonMissingSourceState          PolicyDecisionReason = "missing_source_state"
	PolicyReasonSourceWithdrawn             PolicyDecisionReason = "source_withdrawn"
	PolicyReasonMissingDeploymentPolicy     PolicyDecisionReason = "missing_deployment_policy"
	PolicyReasonPolicyDeny                  PolicyDecisionReason = "policy_deny"
	PolicyReasonPolicyRequestMismatch       PolicyDecisionReason = "policy_request_mismatch"
	PolicyReasonUnsupportedPolicyDefinition PolicyDecisionReason = "unsupported_policy_definition"
	PolicyReasonMalformedPolicyDefinition   PolicyDecisionReason = "malformed_policy_definition"
)

// PolicyInputResult names the closed set of per-input result codes.
type PolicyInputResult string

const (
	PolicyInputAllow                 PolicyInputResult = "allow"
	PolicyInputDeny                  PolicyInputResult = "deny"
	PolicyInputRequestMismatch       PolicyInputResult = "request_mismatch"
	PolicyInputUnsupportedDefinition PolicyInputResult = "unsupported_definition"
	PolicyInputMalformedDefinition   PolicyInputResult = "malformed_definition"
)

// SourceRetrievalEffect names the closed set of inner policy effects.
type SourceRetrievalEffect string

const (
	SourceRetrievalEffectAllow SourceRetrievalEffect = "allow"
	SourceRetrievalEffectDeny  SourceRetrievalEffect = "deny"
)

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

// SourceRetrievalPolicy is the one supported inner policy definition format.
type SourceRetrievalPolicy struct {
	Schema            string                `json:"schema"`
	Action            string                `json:"action"`
	CallerNamespace   string                `json:"caller_namespace"`
	ExternalCallerID  string                `json:"external_caller_id"`
	PurposeNamespace  string                `json:"purpose_namespace"`
	ExternalPurposeID string                `json:"external_purpose_id"`
	Effect            SourceRetrievalEffect `json:"effect"`
}

// Validate enforces the closed inner vocabulary and identity field rules.
func (policy SourceRetrievalPolicy) Validate() error {
	if policy.Schema != SourceRetrievalPolicySchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy.source_retrieval.v1", nil)
	}
	if policy.Action != SourceRetrievalAction {
		return newValidationError("action", ValidationCodeInvalidEnum, "must be source.retrieve", nil)
	}
	for _, value := range []struct{ field, value string }{
		{"caller_namespace", policy.CallerNamespace},
		{"external_caller_id", policy.ExternalCallerID},
		{"purpose_namespace", policy.PurposeNamespace},
		{"external_purpose_id", policy.ExternalPurposeID},
	} {
		if err := validateIdentityInput(value.field, value.value); err != nil {
			return err
		}
	}
	switch policy.Effect {
	case SourceRetrievalEffectAllow, SourceRetrievalEffectDeny:
	default:
		return newValidationError("effect", ValidationCodeInvalidEnum, "must be allow or deny", nil)
	}
	return nil
}

// EncodeSourceRetrievalPolicy returns the exact canonical inner definition bytes.
func EncodeSourceRetrievalPolicy(policy SourceRetrievalPolicy) ([]byte, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(policy, "source retrieval policy")
}

// DecodeSourceRetrievalPolicy rejects any inner bytes whose decode/re-encode round trip is not exact.
func DecodeSourceRetrievalPolicy(data []byte) (SourceRetrievalPolicy, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return SourceRetrievalPolicy{}, err
	}
	var wire struct {
		Schema            string `json:"schema"`
		Action            string `json:"action"`
		CallerNamespace   string `json:"caller_namespace"`
		ExternalCallerID  string `json:"external_caller_id"`
		PurposeNamespace  string `json:"purpose_namespace"`
		ExternalPurposeID string `json:"external_purpose_id"`
		Effect            string `json:"effect"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return SourceRetrievalPolicy{}, err
	}
	policy := SourceRetrievalPolicy{
		Schema:            wire.Schema,
		Action:            wire.Action,
		CallerNamespace:   wire.CallerNamespace,
		ExternalCallerID:  wire.ExternalCallerID,
		PurposeNamespace:  wire.PurposeNamespace,
		ExternalPurposeID: wire.ExternalPurposeID,
		Effect:            SourceRetrievalEffect(wire.Effect),
	}
	if err := policy.Validate(); err != nil {
		return SourceRetrievalPolicy{}, err
	}
	canonical, err := EncodeSourceRetrievalPolicy(policy)
	if err != nil {
		return SourceRetrievalPolicy{}, err
	}
	if !bytes.Equal(canonical, data) {
		return SourceRetrievalPolicy{}, newValidationError("", ValidationCodeInvalidJSON, "inner definition bytes are not canonical", nil)
	}
	return policy, nil
}

// PolicyEvaluationInput is one verified active policy input read by the store.
type PolicyEvaluationInput struct {
	Layer        PolicyDecisionLayer
	ActivationID PolicyActivationID
	BindingID    PolicyBindingID
	DefinitionID PolicyDefinitionID
	Definition   PolicyDefinition
}

// PolicyEvaluationSnapshot is the verified read-only policy and lifecycle input for one evaluation.
type PolicyEvaluationSnapshot struct {
	StatePresent        bool
	CollectionState     *CollectionState
	CurrentWithdrawalID *WithdrawalID
	Inputs              []PolicyEvaluationInput
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
	Outcome             PolicyDecisionOutcome   `json:"outcome"`
	ReasonCodes         []PolicyDecisionReason  `json:"reason_codes"`
	PolicyInputs        []PolicyDecisionInput   `json:"policy_inputs"`
}

// PolicyDecisionInput records one evaluated active policy input.
type PolicyDecisionInput struct {
	Layer              PolicyDecisionLayer `json:"layer"`
	PolicyActivationID PolicyActivationID  `json:"policy_activation_id"`
	PolicyBindingID    PolicyBindingID     `json:"policy_binding_id"`
	PolicyDefinitionID PolicyDefinitionID  `json:"policy_definition_id"`
	Result             PolicyInputResult   `json:"result"`
}

// NewPolicyDecisionID derives the decision identity from the decision schema, request identity,
// evaluator identity, evaluation time, lifecycle evidence, outcome, and every ordered input.
func NewPolicyDecisionID(decision PolicyDecision) (PolicyDecisionID, error) {
	if decision.Schema != PolicyDecisionSchema {
		return PolicyDecisionID{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_decision.v1", nil)
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
	fields = append(fields, []byte(decision.Outcome))
	for _, reason := range decision.ReasonCodes {
		fields = append(fields, []byte(reason))
	}
	for _, input := range decision.PolicyInputs {
		fields = append(fields,
			[]byte(input.Layer),
			input.PolicyActivationID[:],
			input.PolicyBindingID[:],
			input.PolicyDefinitionID[:],
			[]byte(input.Result),
		)
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
// derivable relation between lifecycle evidence, per-input results, outcome, and reason order.
func (decision PolicyDecision) Validate() error {
	if decision.Schema != PolicyDecisionSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_decision.v1", nil)
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
		return newValidationError("evaluator_version", ValidationCodeInvalidValue, "must be 1", nil)
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
	switch decision.Outcome {
	case PolicyOutcomeAllow, PolicyOutcomeDeny:
	default:
		return newValidationError("outcome", ValidationCodeInvalidEnum, "must be allow or deny", nil)
	}
	if len(decision.ReasonCodes) == 0 {
		return newValidationError("reason_codes", ValidationCodeInvalidRange, "must not be empty", nil)
	}
	wantOutcome, wantReasons := deriveDecisionOutcome(decision.StatePresent, decision.CurrentWithdrawalID, decision.PolicyInputs)
	if decision.Outcome != wantOutcome {
		return newValidationError("outcome", ValidationCodeInvalidValue, "does not follow lifecycle evidence and policy inputs", nil)
	}
	if len(decision.ReasonCodes) != len(wantReasons) {
		return newValidationError("reason_codes", ValidationCodeInvalidValue, "do not follow lifecycle evidence and policy inputs", nil)
	}
	for index, reason := range decision.ReasonCodes {
		if reason != wantReasons[index] {
			return newValidationError("reason_codes", ValidationCodeInvalidValue, "do not follow lifecycle evidence and policy inputs", nil)
		}
	}
	for index, input := range decision.PolicyInputs {
		field := "policy_inputs"
		if input.Layer != PolicyLayerDeployment && input.Layer != PolicyLayerSource {
			return newValidationError(field+".layer", ValidationCodeInvalidEnum, "must be deployment or source", nil)
		}
		if index > 0 && decision.PolicyInputs[index-1].Layer == PolicyLayerSource && input.Layer == PolicyLayerDeployment {
			return newValidationError(field+".layer", ValidationCodeInvalidRange, "deployment inputs must precede source inputs", nil)
		}
		if input.PolicyActivationID == (PolicyActivationID{}) {
			return newValidationError(field+".policy_activation_id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		if input.PolicyBindingID == (PolicyBindingID{}) {
			return newValidationError(field+".policy_binding_id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		if input.PolicyDefinitionID == (PolicyDefinitionID{}) {
			return newValidationError(field+".policy_definition_id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		switch input.Result {
		case PolicyInputAllow, PolicyInputDeny, PolicyInputRequestMismatch, PolicyInputUnsupportedDefinition, PolicyInputMalformedDefinition:
		default:
			return newValidationError(field+".result", ValidationCodeInvalidEnum, "must be a closed policy input result", nil)
		}
		for _, previous := range decision.PolicyInputs[:index] {
			if previous.Layer == input.Layer && previous.PolicyActivationID == input.PolicyActivationID && previous.PolicyBindingID == input.PolicyBindingID && previous.PolicyDefinitionID == input.PolicyDefinitionID {
				return newValidationError(field, ValidationCodeInvalidValue, "must not repeat one active policy input", nil)
			}
		}
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

// deriveDecisionOutcome computes the outcome and ordered reason codes from lifecycle evidence and
// per-input results. Reason order is lifecycle checks, missing deployment authorization, then first
// occurrence in deterministic policy-input order.
func deriveDecisionOutcome(statePresent bool, withdrawal *WithdrawalID, inputs []PolicyDecisionInput) (PolicyDecisionOutcome, []PolicyDecisionReason) {
	var reasons []PolicyDecisionReason
	if !statePresent {
		reasons = append(reasons, PolicyReasonMissingSourceState)
	}
	if withdrawal != nil {
		reasons = append(reasons, PolicyReasonSourceWithdrawn)
	}
	deployment := false
	for _, input := range inputs {
		if input.Layer == PolicyLayerDeployment {
			deployment = true
			break
		}
	}
	if !deployment {
		reasons = append(reasons, PolicyReasonMissingDeploymentPolicy)
	}
	seen := make(map[PolicyDecisionReason]struct{}, len(reasons))
	for _, reason := range reasons {
		seen[reason] = struct{}{}
	}
	for _, input := range inputs {
		reason, ok := inputResultReason[input.Result]
		if !ok || reason == PolicyReasonAllow {
			continue
		}
		if _, duplicate := seen[reason]; duplicate {
			continue
		}
		seen[reason] = struct{}{}
		reasons = append(reasons, reason)
	}
	if len(reasons) == 0 {
		return PolicyOutcomeAllow, []PolicyDecisionReason{PolicyReasonAllow}
	}
	return PolicyOutcomeDeny, reasons
}

var inputResultReason = map[PolicyInputResult]PolicyDecisionReason{
	PolicyInputAllow:                 PolicyReasonAllow,
	PolicyInputDeny:                  PolicyReasonPolicyDeny,
	PolicyInputRequestMismatch:       PolicyReasonPolicyRequestMismatch,
	PolicyInputUnsupportedDefinition: PolicyReasonUnsupportedPolicyDefinition,
	PolicyInputMalformedDefinition:   PolicyReasonMalformedPolicyDefinition,
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
	inputs := make([]PolicyDecisionInput, 0, len(snapshot.Inputs))
	for _, candidate := range snapshot.Inputs {
		result, err := evaluatePolicyInput(request, candidate)
		if err != nil {
			return PolicyDecision{}, err
		}
		inputs = append(inputs, PolicyDecisionInput{
			Layer:              candidate.Layer,
			PolicyActivationID: candidate.ActivationID,
			PolicyBindingID:    candidate.BindingID,
			PolicyDefinitionID: candidate.DefinitionID,
			Result:             result,
		})
	}
	outcome, reasons := deriveDecisionOutcome(snapshot.StatePresent, snapshot.CurrentWithdrawalID, inputs)
	decision := PolicyDecision{
		Schema:              PolicyDecisionSchema,
		Request:             request,
		EvaluatorID:         PolicyEvaluatorID,
		EvaluatorVersion:    PolicyEvaluatorVersion,
		EvaluatedAtUsec:     evaluatedAtUsec,
		StatePresent:        snapshot.StatePresent,
		CollectionState:     cloneCollectionState(snapshot.CollectionState),
		CurrentWithdrawalID: cloneWithdrawalID(snapshot.CurrentWithdrawalID),
		Outcome:             outcome,
		ReasonCodes:         reasons,
		PolicyInputs:        inputs,
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

func evaluatePolicyInput(request PolicyEvaluationRequest, input PolicyEvaluationInput) (PolicyInputResult, error) {
	if input.Layer != PolicyLayerDeployment && input.Layer != PolicyLayerSource {
		return "", newValidationError("layer", ValidationCodeInvalidEnum, "must be deployment or source", nil)
	}
	if input.BindingID == (PolicyBindingID{}) || input.DefinitionID == (PolicyDefinitionID{}) || input.Definition.ID != input.DefinitionID {
		return "", newValidationError("definition_id", ValidationCodeInvalidID, "must match the evaluated definition", nil)
	}
	if input.Definition.DefinitionMediaType != SourceRetrievalPolicyMediaType || input.Definition.DefinitionSchema != SourceRetrievalPolicySchema {
		return PolicyInputUnsupportedDefinition, nil
	}
	policy, err := DecodeSourceRetrievalPolicy([]byte(input.Definition.Definition))
	if err != nil {
		return PolicyInputMalformedDefinition, nil
	}
	if policy.CallerNamespace != request.CallerNamespace || policy.ExternalCallerID != request.ExternalCallerID || policy.PurposeNamespace != request.PurposeNamespace || policy.ExternalPurposeID != request.ExternalPurposeID {
		return PolicyInputRequestMismatch, nil
	}
	if policy.Effect == SourceRetrievalEffectAllow {
		return PolicyInputAllow, nil
	}
	return PolicyInputDeny, nil
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
		Schema              string                    `json:"schema"`
		ID                  string                    `json:"id"`
		Request             json.RawMessage           `json:"request"`
		EvaluatorID         string                    `json:"evaluator_id"`
		EvaluatorVersion    string                    `json:"evaluator_version"`
		EvaluatedAtUsec     int64                     `json:"evaluated_at_usec"`
		StatePresent        bool                      `json:"state_present"`
		CollectionState     *string                   `json:"collection_state"`
		CurrentWithdrawalID *string                   `json:"current_withdrawal_id"`
		Outcome             string                    `json:"outcome"`
		ReasonCodes         []string                  `json:"reason_codes"`
		PolicyInputs        []policyDecisionInputWire `json:"policy_inputs"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return PolicyDecision{}, err
	}
	if wire.Schema != PolicyDecisionSchema {
		return PolicyDecision{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_decision.v1", nil)
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
	inputs := make([]PolicyDecisionInput, 0, len(wire.PolicyInputs))
	for _, raw := range wire.PolicyInputs {
		input, err := raw.input()
		if err != nil {
			return PolicyDecision{}, err
		}
		inputs = append(inputs, input)
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
		Outcome:             PolicyDecisionOutcome(wire.Outcome),
		ReasonCodes:         reasons,
		PolicyInputs:        inputs,
	}
	if err := decision.Validate(); err != nil {
		return PolicyDecision{}, err
	}
	return decision, nil
}

type policyDecisionInputWire struct {
	Layer              string `json:"layer"`
	PolicyActivationID string `json:"policy_activation_id"`
	PolicyBindingID    string `json:"policy_binding_id"`
	PolicyDefinitionID string `json:"policy_definition_id"`
	Result             string `json:"result"`
}

func (wire policyDecisionInputWire) input() (PolicyDecisionInput, error) {
	activationID, err := ParsePolicyActivationID(wire.PolicyActivationID)
	if err != nil {
		return PolicyDecisionInput{}, validationErrorForField(err, "policy_activation_id")
	}
	bindingID, err := ParsePolicyBindingID(wire.PolicyBindingID)
	if err != nil {
		return PolicyDecisionInput{}, validationErrorForField(err, "policy_binding_id")
	}
	definitionID, err := ParsePolicyDefinitionID(wire.PolicyDefinitionID)
	if err != nil {
		return PolicyDecisionInput{}, validationErrorForField(err, "policy_definition_id")
	}
	return PolicyDecisionInput{
		Layer:              PolicyDecisionLayer(wire.Layer),
		PolicyActivationID: activationID,
		PolicyBindingID:    bindingID,
		PolicyDefinitionID: definitionID,
		Result:             PolicyInputResult(wire.Result),
	}, nil
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

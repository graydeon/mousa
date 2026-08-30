package mousa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"unicode/utf8"
)

const PolicyBindingSchema = "mousa.policy_binding.v1"

// PolicyBindingID is the stable identity of one immutable policy binding.
type PolicyBindingID [sha256.Size]byte

// PolicyScopeKind names the closed set of binding scopes.
type PolicyScopeKind string

const (
	PolicyScopeDeployment     PolicyScopeKind = "deployment"
	PolicyScopeSource         PolicyScopeKind = "source"
	PolicyScopeObservation    PolicyScopeKind = "observation"
	PolicyScopeArtifact       PolicyScopeKind = "artifact"
	PolicyScopeRepresentation PolicyScopeKind = "representation"
	PolicyScopeSegment        PolicyScopeKind = "segment"
)

// PolicyScope is a closed typed reference to one policy layer.
type PolicyScope struct {
	kind             PolicyScopeKind
	sourceID         SourceID
	observationID    ObservationID
	artifactID       ArtifactID
	representationID RepresentationID
	segmentID        SegmentID
}

func NewDeploymentPolicyScope() PolicyScope { return PolicyScope{kind: PolicyScopeDeployment} }
func NewSourcePolicyScope(id SourceID) PolicyScope {
	return PolicyScope{kind: PolicyScopeSource, sourceID: id}
}
func NewObservationPolicyScope(id ObservationID) PolicyScope {
	return PolicyScope{kind: PolicyScopeObservation, observationID: id}
}
func NewArtifactPolicyScope(id ArtifactID) PolicyScope {
	return PolicyScope{kind: PolicyScopeArtifact, artifactID: id}
}
func NewRepresentationPolicyScope(id RepresentationID) PolicyScope {
	return PolicyScope{kind: PolicyScopeRepresentation, representationID: id}
}
func NewSegmentPolicyScope(id SegmentID) PolicyScope {
	return PolicyScope{kind: PolicyScopeSegment, segmentID: id}
}

func (scope PolicyScope) Kind() PolicyScopeKind { return scope.kind }
func (scope PolicyScope) SourceID() (SourceID, bool) {
	return scope.sourceID, scope.kind == PolicyScopeSource
}
func (scope PolicyScope) ObservationID() (ObservationID, bool) {
	return scope.observationID, scope.kind == PolicyScopeObservation
}
func (scope PolicyScope) ArtifactID() (ArtifactID, bool) {
	return scope.artifactID, scope.kind == PolicyScopeArtifact
}
func (scope PolicyScope) RepresentationID() (RepresentationID, bool) {
	return scope.representationID, scope.kind == PolicyScopeRepresentation
}
func (scope PolicyScope) SegmentID() (SegmentID, bool) {
	return scope.segmentID, scope.kind == PolicyScopeSegment
}

func (scope PolicyScope) identityFields(field string) ([]byte, []byte, error) {
	switch scope.kind {
	case PolicyScopeDeployment:
		return []byte(scope.kind), nil, nil
	case PolicyScopeSource:
		if scope.sourceID == (SourceID{}) {
			return nil, nil, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(scope.kind), scope.sourceID[:], nil
	case PolicyScopeObservation:
		if scope.observationID == (ObservationID{}) {
			return nil, nil, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(scope.kind), scope.observationID[:], nil
	case PolicyScopeArtifact:
		if scope.artifactID == (ArtifactID{}) {
			return nil, nil, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(scope.kind), scope.artifactID[:], nil
	case PolicyScopeRepresentation:
		if scope.representationID == (RepresentationID{}) {
			return nil, nil, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(scope.kind), scope.representationID[:], nil
	case PolicyScopeSegment:
		if scope.segmentID == (SegmentID{}) {
			return nil, nil, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(scope.kind), scope.segmentID[:], nil
	default:
		return nil, nil, newValidationError(field+".kind", ValidationCodeInvalidEnum, "must be deployment, source, observation, artifact, representation, or segment", nil)
	}
}

func (scope PolicyScope) MarshalJSON() ([]byte, error) {
	kind, id, err := scope.identityFields("scope")
	if err != nil {
		return nil, err
	}
	if scope.kind == PolicyScopeDeployment {
		return json.Marshal(struct {
			Kind string `json:"kind"`
		}{Kind: string(kind)})
	}
	return json.Marshal(struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{Kind: string(kind), ID: hex.EncodeToString(id)})
}

func (scope *PolicyScope) UnmarshalJSON(data []byte) error {
	decoded, err := decodePolicyScope(data, "scope")
	if err != nil {
		return err
	}
	*scope = decoded
	return nil
}

func decodePolicyScope(data []byte, field string) (PolicyScope, error) {
	var kindOnly struct {
		Kind string `json:"kind"`
	}
	if err := decodeJSONContract(data, &kindOnly); err == nil && PolicyScopeKind(kindOnly.Kind) == PolicyScopeDeployment {
		return NewDeploymentPolicyScope(), nil
	}
	var wire struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return PolicyScope{}, prefixValidationError(err, field)
	}
	switch PolicyScopeKind(wire.Kind) {
	case PolicyScopeSource:
		id, err := ParseSourceID(wire.ID)
		if err != nil || id == (SourceID{}) {
			return PolicyScope{}, validationErrorForField(errOrZeroID(err), field+".id")
		}
		return NewSourcePolicyScope(id), nil
	case PolicyScopeObservation:
		id, err := ParseObservationID(wire.ID)
		if err != nil || id == (ObservationID{}) {
			return PolicyScope{}, validationErrorForField(errOrZeroID(err), field+".id")
		}
		return NewObservationPolicyScope(id), nil
	case PolicyScopeArtifact:
		id, err := ParseArtifactID(wire.ID)
		if err != nil || id == (ArtifactID{}) {
			return PolicyScope{}, validationErrorForField(errOrZeroID(err), field+".id")
		}
		return NewArtifactPolicyScope(id), nil
	case PolicyScopeRepresentation:
		id, err := ParseRepresentationID(wire.ID)
		if err != nil || id == (RepresentationID{}) {
			return PolicyScope{}, validationErrorForField(errOrZeroID(err), field+".id")
		}
		return NewRepresentationPolicyScope(id), nil
	case PolicyScopeSegment:
		id, err := ParseSegmentID(wire.ID)
		if err != nil || id == (SegmentID{}) {
			return PolicyScope{}, validationErrorForField(errOrZeroID(err), field+".id")
		}
		return NewSegmentPolicyScope(id), nil
	default:
		return PolicyScope{}, newValidationError(field+".kind", ValidationCodeInvalidEnum, "must be deployment, source, observation, artifact, representation, or segment", nil)
	}
}

func errOrZeroID(err error) error {
	if err != nil {
		return err
	}
	return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
}

// PolicyBinding attaches one exact definition to one exact immutable scope.
type PolicyBinding struct {
	Schema                 string             `json:"schema"`
	ID                     PolicyBindingID    `json:"id"`
	Namespace              string             `json:"namespace"`
	ExternalBindingID      string             `json:"external_binding_id"`
	ExternalBindingVersion string             `json:"external_binding_version"`
	Scope                  PolicyScope        `json:"scope"`
	PolicyDefinitionID     PolicyDefinitionID `json:"policy_definition_id"`
}

func NewPolicyBindingID(namespace, externalBindingID, externalBindingVersion string, scope PolicyScope, definitionID PolicyDefinitionID) (PolicyBindingID, error) {
	for _, value := range []struct{ field, value string }{{"namespace", namespace}, {"external_binding_id", externalBindingID}, {"external_binding_version", externalBindingVersion}} {
		if err := validateIdentityInput(value.field, value.value); err != nil {
			return PolicyBindingID{}, err
		}
	}
	kind, subjectID, err := scope.identityFields("scope")
	if err != nil {
		return PolicyBindingID{}, err
	}
	if definitionID == (PolicyDefinitionID{}) {
		return PolicyBindingID{}, newValidationError("policy_definition_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	digest := sha256.New()
	writeTuple(digest, []byte(PolicyBindingSchema), []byte(namespace), []byte(externalBindingID), []byte(externalBindingVersion), kind, subjectID, definitionID[:])
	return PolicyBindingID(digest.Sum(nil)), nil
}

func (record PolicyBinding) Validate() error {
	if record.Schema != PolicyBindingSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_binding.v1", nil)
	}
	if record.ID == (PolicyBindingID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	expected, err := NewPolicyBindingID(record.Namespace, record.ExternalBindingID, record.ExternalBindingVersion, record.Scope, record.PolicyDefinitionID)
	if err != nil {
		return err
	}
	if record.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match policy binding identity", nil)
	}
	return nil
}

func EncodePolicyBinding(record PolicyBinding) ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(record, "policy binding")
}

func DecodePolicyBinding(data []byte) (PolicyBinding, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return PolicyBinding{}, err
	}
	var wire struct {
		Schema                 string          `json:"schema"`
		ID                     string          `json:"id"`
		Namespace              string          `json:"namespace"`
		ExternalBindingID      string          `json:"external_binding_id"`
		ExternalBindingVersion string          `json:"external_binding_version"`
		Scope                  json.RawMessage `json:"scope"`
		PolicyDefinitionID     string          `json:"policy_definition_id"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return PolicyBinding{}, err
	}
	if wire.Schema != PolicyBindingSchema {
		return PolicyBinding{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_binding.v1", nil)
	}
	id, err := ParsePolicyBindingID(wire.ID)
	if err != nil {
		return PolicyBinding{}, validationErrorForField(err, "id")
	}
	scope, err := decodePolicyScope(wire.Scope, "scope")
	if err != nil {
		return PolicyBinding{}, err
	}
	definitionID, err := ParsePolicyDefinitionID(wire.PolicyDefinitionID)
	if err != nil {
		return PolicyBinding{}, validationErrorForField(err, "policy_definition_id")
	}
	record := PolicyBinding{Schema: wire.Schema, ID: id, Namespace: wire.Namespace, ExternalBindingID: wire.ExternalBindingID, ExternalBindingVersion: wire.ExternalBindingVersion, Scope: scope, PolicyDefinitionID: definitionID}
	if err := record.Validate(); err != nil {
		return PolicyBinding{}, err
	}
	return record, nil
}

func (id PolicyBindingID) String() string { return hex.EncodeToString(id[:]) }
func ParsePolicyBindingID(value string) (PolicyBindingID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return PolicyBindingID{}, err
	}
	return PolicyBindingID(decoded), nil
}
func (id PolicyBindingID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }
func (id *PolicyBindingID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParsePolicyBindingID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

const PolicyActivationSchema = "mousa.policy_activation.v1"

// PolicyActivationID is the retry-stable identity of one administrative event.
type PolicyActivationID [sha256.Size]byte

// PolicyActivation is one immutable compare-and-swap transition.
type PolicyActivation struct {
	Schema                       string              `json:"schema"`
	ID                           PolicyActivationID  `json:"id"`
	Namespace                    string              `json:"namespace"`
	ExternalBindingID            string              `json:"external_binding_id"`
	ExternalActivationID         string              `json:"external_activation_id"`
	ExpectedPreviousActivationID *PolicyActivationID `json:"expected_previous_activation_id"`
	ActiveBindingID              *PolicyBindingID    `json:"active_binding_id"`
	ActorID                      string              `json:"actor_id"`
	ActorVersion                 string              `json:"actor_version"`
	OccurredAtUsec               int64               `json:"occurred_at_usec"`
	Reason                       *string             `json:"reason"`
}

// PolicyBindingState is the verified current projection for one logical series.
type PolicyBindingState struct {
	Namespace           string
	ExternalBindingID   string
	CurrentActivationID PolicyActivationID
	ActiveBindingID     *PolicyBindingID
}

func NewPolicyActivationID(namespace, externalBindingID, externalActivationID string) (PolicyActivationID, error) {
	for _, value := range []struct{ field, value string }{{"namespace", namespace}, {"external_binding_id", externalBindingID}, {"external_activation_id", externalActivationID}} {
		if err := validateIdentityInput(value.field, value.value); err != nil {
			return PolicyActivationID{}, err
		}
	}
	digest := sha256.New()
	writeTuple(digest, []byte(PolicyActivationSchema), []byte(namespace), []byte(externalBindingID), []byte(externalActivationID))
	return PolicyActivationID(digest.Sum(nil)), nil
}

func (record PolicyActivation) Validate() error {
	if record.Schema != PolicyActivationSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_activation.v1", nil)
	}
	if record.ID == (PolicyActivationID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	expected, err := NewPolicyActivationID(record.Namespace, record.ExternalBindingID, record.ExternalActivationID)
	if err != nil {
		return err
	}
	if record.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match policy activation identity", nil)
	}
	if record.ExpectedPreviousActivationID != nil && *record.ExpectedPreviousActivationID == (PolicyActivationID{}) {
		return newValidationError("expected_previous_activation_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if record.ActiveBindingID != nil && *record.ActiveBindingID == (PolicyBindingID{}) {
		return newValidationError("active_binding_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if err := validateIdentityInput("actor_id", record.ActorID); err != nil {
		return err
	}
	if err := validateIdentityInput("actor_version", record.ActorVersion); err != nil {
		return err
	}
	if record.OccurredAtUsec <= 0 {
		return newValidationError("occurred_at_usec", ValidationCodeInvalidValue, "must be positive", nil)
	}
	if record.Reason != nil && (!utf8.ValidString(*record.Reason) || len(*record.Reason) > 4096) {
		return newValidationError("reason", ValidationCodeInvalidValue, "must be valid UTF-8 no longer than 4096 bytes", nil)
	}
	return nil
}

func EncodePolicyActivation(record PolicyActivation) ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(record, "policy activation")
}

func DecodePolicyActivation(data []byte) (PolicyActivation, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return PolicyActivation{}, err
	}
	var wire struct {
		Schema                       string          `json:"schema"`
		ID                           string          `json:"id"`
		Namespace                    string          `json:"namespace"`
		ExternalBindingID            string          `json:"external_binding_id"`
		ExternalActivationID         string          `json:"external_activation_id"`
		ExpectedPreviousActivationID json.RawMessage `json:"expected_previous_activation_id"`
		ActiveBindingID              json.RawMessage `json:"active_binding_id"`
		ActorID                      string          `json:"actor_id"`
		ActorVersion                 string          `json:"actor_version"`
		OccurredAtUsec               int64           `json:"occurred_at_usec"`
		Reason                       json.RawMessage `json:"reason"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return PolicyActivation{}, err
	}
	if wire.Schema != PolicyActivationSchema {
		return PolicyActivation{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_activation.v1", nil)
	}
	if wire.ExpectedPreviousActivationID == nil || wire.ActiveBindingID == nil || wire.Reason == nil {
		return PolicyActivation{}, newValidationError("", ValidationCodeInvalidValue, "nullable fields must be present", nil)
	}
	id, err := ParsePolicyActivationID(wire.ID)
	if err != nil {
		return PolicyActivation{}, validationErrorForField(err, "id")
	}
	previous, err := decodeNullablePolicyActivationID(wire.ExpectedPreviousActivationID)
	if err != nil {
		return PolicyActivation{}, validationErrorForField(err, "expected_previous_activation_id")
	}
	active, err := decodeNullablePolicyBindingID(wire.ActiveBindingID)
	if err != nil {
		return PolicyActivation{}, validationErrorForField(err, "active_binding_id")
	}
	var reason *string
	if string(wire.Reason) != "null" {
		var value string
		if err := json.Unmarshal(wire.Reason, &value); err != nil {
			return PolicyActivation{}, newValidationError("reason", ValidationCodeInvalidValue, "must be null or a string", err)
		}
		reason = &value
	}
	record := PolicyActivation{Schema: wire.Schema, ID: id, Namespace: wire.Namespace, ExternalBindingID: wire.ExternalBindingID, ExternalActivationID: wire.ExternalActivationID, ExpectedPreviousActivationID: previous, ActiveBindingID: active, ActorID: wire.ActorID, ActorVersion: wire.ActorVersion, OccurredAtUsec: wire.OccurredAtUsec, Reason: reason}
	if err := record.Validate(); err != nil {
		return PolicyActivation{}, err
	}
	return record, nil
}

func decodeNullablePolicyActivationID(data []byte) (*PolicyActivationID, error) {
	if string(data) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, newValidationError("id", ValidationCodeInvalidID, "must be null or a lowercase SHA-256 value", err)
	}
	id, err := ParsePolicyActivationID(value)
	if err != nil {
		return nil, err
	}
	if id == (PolicyActivationID{}) {
		return nil, newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	return &id, nil
}

func decodeNullablePolicyBindingID(data []byte) (*PolicyBindingID, error) {
	if string(data) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, newValidationError("id", ValidationCodeInvalidID, "must be null or a lowercase SHA-256 value", err)
	}
	id, err := ParsePolicyBindingID(value)
	if err != nil {
		return nil, err
	}
	if id == (PolicyBindingID{}) {
		return nil, newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	return &id, nil
}

func (id PolicyActivationID) String() string { return hex.EncodeToString(id[:]) }
func ParsePolicyActivationID(value string) (PolicyActivationID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return PolicyActivationID{}, err
	}
	return PolicyActivationID(decoded), nil
}
func (id PolicyActivationID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }
func (id *PolicyActivationID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParsePolicyActivationID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

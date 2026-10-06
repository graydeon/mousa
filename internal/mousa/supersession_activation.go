package mousa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// SupersessionActivationSchema is the closed schema of one supersession activation transition.
const SupersessionActivationSchema = "mousa.supersession_activation.v1"

// Activation field limits. The actor and reason limits are the smallest that still cover an
// operator identity and a sentence of justification; the encoded limit mirrors the record_json
// check in migration 0013 and covers worst-case JSON escaping of those labels.
const (
	MaxSupersessionActorBytes      = 512
	MaxSupersessionReasonBytes     = 4096
	MaxSupersessionActivationBytes = 64 << 10
)

// SupersessionActivationID is the deterministic identity of one immutable activation transition.
type SupersessionActivationID [sha256.Size]byte

// SupersessionActivation is one recorded administration transition that selects which
// supersession declaration is active for one source. It is a compare-and-swap statement: the
// transition names the activation event it expects to be current and the declaration it selects,
// or an explicit null declaration to deactivate. The event it appends is immutable history.
//
// Version 1 of this record keeps at most one active declaration per source. It is recorded
// administration state, not an authorization decision: it grants no access, denies no source and
// changes no current evidence selection, ranking, packing, freshness judgment or conflict
// handling. It never moves an item pointer, re-ingests successor text or mutates a declaration,
// and a declaration pin it selects stays exact and historical whether or not that revision is
// currently active.
//
// The identity is derived from the source, the expected predecessor event and the selected
// declaration only. Actor, time and reason are recorded evidence of the transition, not part of
// its identity, so an identical transition replayed later keeps its identity while a differing
// replay of that identity is a conflict.
//
// Validate checks structure only. Whether the source, the selected declaration and the expected
// predecessor event exist, belong to the source together, and are the current state that the
// expectation claims is a later transaction-bound store check this codec cannot perform.
type SupersessionActivation struct {
	Schema                       string                     `json:"schema"`
	ID                           SupersessionActivationID   `json:"id"`
	SourceID                     SourceID                   `json:"source_id"`
	ExpectedPreviousActivationID *SupersessionActivationID  `json:"expected_previous_activation_id"`
	DeclarationID                *SupersessionDeclarationID `json:"declaration_id"`
	ActorID                      string                     `json:"actor_id"`
	ActorVersion                 string                     `json:"actor_version"`
	OccurredAtUsec               int64                      `json:"occurred_at_usec"`
	Reason                       *string                    `json:"reason"`
}

// SupersessionActivationState is the verified current projection for one source: the latest
// activation event and the declaration that event selects, if any.
type SupersessionActivationState struct {
	SourceID            SourceID
	CurrentActivationID SupersessionActivationID
	ActiveDeclarationID *SupersessionDeclarationID
}

// NewSupersessionActivationID derives the transition identity: the schema-domain-separated hash of
// the length-delimited ordered tuple source, expected predecessor event and selected declaration.
// A null predecessor and a null declaration are empty tuple fields, while a present identity is
// always 32 nonzero bytes, so absence and presence cannot collide. The generated identity is not
// an input to its own hash.
func NewSupersessionActivationID(sourceID SourceID, expectedPreviousActivationID *SupersessionActivationID, declarationID *SupersessionDeclarationID) (SupersessionActivationID, error) {
	if sourceID == (SourceID{}) {
		return SupersessionActivationID{}, newValidationError("source_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if expectedPreviousActivationID != nil && *expectedPreviousActivationID == (SupersessionActivationID{}) {
		return SupersessionActivationID{}, newValidationError("expected_previous_activation_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if declarationID != nil && *declarationID == (SupersessionDeclarationID{}) {
		return SupersessionActivationID{}, newValidationError("declaration_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	digest := sha256.New()
	writeTuple(digest, []byte(SupersessionActivationSchema), sourceID[:], supersessionActivationIdentityBytes(expectedPreviousActivationID), supersessionDeclarationIdentityBytes(declarationID))
	return SupersessionActivationID(digest.Sum(nil)), nil
}

func supersessionActivationIdentityBytes(id *SupersessionActivationID) []byte {
	if id == nil {
		return nil
	}
	return id[:]
}

func supersessionDeclarationIdentityBytes(id *SupersessionDeclarationID) []byte {
	if id == nil {
		return nil
	}
	return id[:]
}

// Validate recomputes the transition identity and enforces the closed record vocabulary, including
// the structural rule that an initial transition (no expected predecessor) must select a
// declaration. It establishes structure only, so a validated transition is not evidence that the
// source, the selected declaration or the expected predecessor event exist, nor that the
// expectation matches the stored current state.
func (activation SupersessionActivation) Validate() error {
	if activation.Schema != SupersessionActivationSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.supersession_activation.v1", nil)
	}
	if activation.ID == (SupersessionActivationID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if activation.ExpectedPreviousActivationID == nil && activation.DeclarationID == nil {
		return newValidationError("declaration_id", ValidationCodeInvalidValue, "must be set when expected_previous_activation_id is null", nil)
	}
	expected, err := NewSupersessionActivationID(activation.SourceID, activation.ExpectedPreviousActivationID, activation.DeclarationID)
	if err != nil {
		return err
	}
	if activation.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match activation content", nil)
	}
	if err := validateSupersessionLabel("actor_id", activation.ActorID, MaxSupersessionActorBytes); err != nil {
		return err
	}
	if err := validateSupersessionLabel("actor_version", activation.ActorVersion, MaxSupersessionActorBytes); err != nil {
		return err
	}
	if activation.OccurredAtUsec <= 0 {
		return newValidationError("occurred_at_usec", ValidationCodeInvalidValue, "must be positive", nil)
	}
	if activation.Reason != nil {
		if err := validateSupersessionLabel("reason", *activation.Reason, MaxSupersessionReasonBytes); err != nil {
			return err
		}
	}
	return nil
}

// EncodeSupersessionActivation returns the exact canonical transition bytes.
func EncodeSupersessionActivation(activation SupersessionActivation) ([]byte, error) {
	if err := activation.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(activation, "supersession activation")
}

// DecodeSupersessionActivation decodes one canonical transition payload into canonical output,
// rejecting unknown or duplicate keys, missing, null or mistyped required fields, malformed or
// zero identities, trailing values, oversized input and an identity that disagrees with the
// transition content. Every nullable field must be present as either null or a value, so an absent
// key is never mistaken for an explicit null.
func DecodeSupersessionActivation(data []byte) (SupersessionActivation, error) {
	if len(data) > MaxSupersessionActivationBytes {
		return SupersessionActivation{}, newValidationError("", ValidationCodeInvalidRange, "must not exceed the encoded activation byte limit", nil)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return SupersessionActivation{}, err
	}
	var wire struct {
		Schema                       string          `json:"schema"`
		ID                           string          `json:"id"`
		SourceID                     string          `json:"source_id"`
		ExpectedPreviousActivationID json.RawMessage `json:"expected_previous_activation_id"`
		DeclarationID                json.RawMessage `json:"declaration_id"`
		ActorID                      string          `json:"actor_id"`
		ActorVersion                 string          `json:"actor_version"`
		OccurredAtUsec               int64           `json:"occurred_at_usec"`
		Reason                       json.RawMessage `json:"reason"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return SupersessionActivation{}, err
	}
	if wire.Schema != SupersessionActivationSchema {
		return SupersessionActivation{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.supersession_activation.v1", nil)
	}
	if wire.ExpectedPreviousActivationID == nil || wire.DeclarationID == nil || wire.Reason == nil {
		return SupersessionActivation{}, newValidationError("", ValidationCodeInvalidValue, "nullable fields must be present", nil)
	}
	id, err := ParseSupersessionActivationID(wire.ID)
	if err != nil {
		return SupersessionActivation{}, validationErrorForField(err, "id")
	}
	sourceID, err := ParseSourceID(wire.SourceID)
	if err != nil {
		return SupersessionActivation{}, validationErrorForField(err, "source_id")
	}
	previous, err := decodeNullableSupersessionActivationID(wire.ExpectedPreviousActivationID)
	if err != nil {
		return SupersessionActivation{}, validationErrorForField(err, "expected_previous_activation_id")
	}
	declarationID, err := decodeNullableSupersessionDeclarationID(wire.DeclarationID)
	if err != nil {
		return SupersessionActivation{}, validationErrorForField(err, "declaration_id")
	}
	var reason *string
	if string(wire.Reason) != "null" {
		var value string
		if err := json.Unmarshal(wire.Reason, &value); err != nil {
			return SupersessionActivation{}, newValidationError("reason", ValidationCodeInvalidValue, "must be null or a string", err)
		}
		reason = &value
	}
	activation := SupersessionActivation{
		Schema:                       wire.Schema,
		ID:                           id,
		SourceID:                     sourceID,
		ExpectedPreviousActivationID: previous,
		DeclarationID:                declarationID,
		ActorID:                      wire.ActorID,
		ActorVersion:                 wire.ActorVersion,
		OccurredAtUsec:               wire.OccurredAtUsec,
		Reason:                       reason,
	}
	if err := activation.Validate(); err != nil {
		return SupersessionActivation{}, err
	}
	return activation, nil
}

func decodeNullableSupersessionActivationID(data []byte) (*SupersessionActivationID, error) {
	if string(data) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, newValidationError("id", ValidationCodeInvalidID, "must be null or a lowercase SHA-256 value", err)
	}
	id, err := ParseSupersessionActivationID(value)
	if err != nil {
		return nil, err
	}
	if id == (SupersessionActivationID{}) {
		return nil, newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	return &id, nil
}

func decodeNullableSupersessionDeclarationID(data []byte) (*SupersessionDeclarationID, error) {
	if string(data) == "null" {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, newValidationError("id", ValidationCodeInvalidID, "must be null or a lowercase SHA-256 value", err)
	}
	id, err := ParseSupersessionDeclarationID(value)
	if err != nil {
		return nil, err
	}
	if id == (SupersessionDeclarationID{}) {
		return nil, newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	return &id, nil
}

func (id SupersessionActivationID) String() string { return hex.EncodeToString(id[:]) }

// ParseSupersessionActivationID parses one lowercase hexadecimal activation identity.
func ParseSupersessionActivationID(value string) (SupersessionActivationID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return SupersessionActivationID{}, err
	}
	return SupersessionActivationID(decoded), nil
}

func (id SupersessionActivationID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *SupersessionActivationID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseSupersessionActivationID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

package mousa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const PolicyDefinitionSchema = "mousa.policy_definition.v1"

// PolicyDefinitionID is the stable identity of one immutable policy definition.
type PolicyDefinitionID [sha256.Size]byte

// PolicyDefinition preserves an administrative policy document without giving it effect.
type PolicyDefinition struct {
	Schema                string             `json:"schema"`
	ID                    PolicyDefinitionID `json:"id"`
	Namespace             string             `json:"namespace"`
	ExternalPolicyID      string             `json:"external_policy_id"`
	ExternalPolicyVersion string             `json:"external_policy_version"`
	DefinitionMediaType   string             `json:"definition_media_type"`
	DefinitionSchema      string             `json:"definition_schema"`
	DefinitionSHA256      SHA256             `json:"definition_sha256"`
	Definition            string             `json:"definition"`
}

func NewPolicyDefinitionID(namespace, externalPolicyID, externalPolicyVersion, definitionMediaType, definitionSchema string, definitionSHA256 SHA256) (PolicyDefinitionID, error) {
	for _, value := range []struct{ field, value string }{
		{"namespace", namespace},
		{"external_policy_id", externalPolicyID},
		{"external_policy_version", externalPolicyVersion},
		{"definition_media_type", definitionMediaType},
		{"definition_schema", definitionSchema},
	} {
		if err := validateIdentityInput(value.field, value.value); err != nil {
			return PolicyDefinitionID{}, err
		}
	}
	if definitionSHA256 == (SHA256{}) {
		return PolicyDefinitionID{}, newValidationError("definition_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	digest := sha256.New()
	writeTuple(digest,
		[]byte(PolicyDefinitionSchema),
		[]byte(namespace),
		[]byte(externalPolicyID),
		[]byte(externalPolicyVersion),
		[]byte(definitionMediaType),
		[]byte(definitionSchema),
		definitionSHA256[:],
	)
	return PolicyDefinitionID(digest.Sum(nil)), nil
}

func (record PolicyDefinition) Validate() error {
	if record.Schema != PolicyDefinitionSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_definition.v1", nil)
	}
	if record.ID == (PolicyDefinitionID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	for _, value := range []struct{ field, value string }{
		{"namespace", record.Namespace},
		{"external_policy_id", record.ExternalPolicyID},
		{"external_policy_version", record.ExternalPolicyVersion},
		{"definition_media_type", record.DefinitionMediaType},
		{"definition_schema", record.DefinitionSchema},
		{"definition", record.Definition},
	} {
		if err := validateIdentityInput(value.field, value.value); err != nil {
			return err
		}
	}
	if record.DefinitionSHA256 == (SHA256{}) {
		return newValidationError("definition_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	if sha256.Sum256([]byte(record.Definition)) != [sha256.Size]byte(record.DefinitionSHA256) {
		return newValidationError("definition_sha256", ValidationCodeInvalidDigest, "does not match definition bytes", nil)
	}
	expected, err := NewPolicyDefinitionID(record.Namespace, record.ExternalPolicyID, record.ExternalPolicyVersion, record.DefinitionMediaType, record.DefinitionSchema, record.DefinitionSHA256)
	if err != nil {
		return err
	}
	if record.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match policy definition identity", nil)
	}
	return nil
}

func EncodePolicyDefinition(record PolicyDefinition) ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(record, "policy definition")
}

func DecodePolicyDefinition(data []byte) (PolicyDefinition, error) {
	var wire struct {
		Schema                string `json:"schema"`
		ID                    string `json:"id"`
		Namespace             string `json:"namespace"`
		ExternalPolicyID      string `json:"external_policy_id"`
		ExternalPolicyVersion string `json:"external_policy_version"`
		DefinitionMediaType   string `json:"definition_media_type"`
		DefinitionSchema      string `json:"definition_schema"`
		DefinitionSHA256      string `json:"definition_sha256"`
		Definition            string `json:"definition"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return PolicyDefinition{}, err
	}
	if wire.Schema != PolicyDefinitionSchema {
		return PolicyDefinition{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.policy_definition.v1", nil)
	}
	id, err := ParsePolicyDefinitionID(wire.ID)
	if err != nil {
		return PolicyDefinition{}, validationErrorForField(err, "id")
	}
	definitionSHA256, err := ParseSHA256(wire.DefinitionSHA256)
	if err != nil {
		return PolicyDefinition{}, validationErrorForField(err, "definition_sha256")
	}
	record := PolicyDefinition{
		Schema:                wire.Schema,
		ID:                    id,
		Namespace:             wire.Namespace,
		ExternalPolicyID:      wire.ExternalPolicyID,
		ExternalPolicyVersion: wire.ExternalPolicyVersion,
		DefinitionMediaType:   wire.DefinitionMediaType,
		DefinitionSchema:      wire.DefinitionSchema,
		DefinitionSHA256:      definitionSHA256,
		Definition:            wire.Definition,
	}
	if err := record.Validate(); err != nil {
		return PolicyDefinition{}, err
	}
	return record, nil
}

func (id PolicyDefinitionID) String() string { return hex.EncodeToString(id[:]) }

func ParsePolicyDefinitionID(value string) (PolicyDefinitionID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return PolicyDefinitionID{}, err
	}
	return PolicyDefinitionID(decoded), nil
}

func (id PolicyDefinitionID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *PolicyDefinitionID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParsePolicyDefinitionID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

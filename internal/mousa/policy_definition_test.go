package mousa

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const (
	canonicalPolicyDefinitionID = "d55989574364003f9c967bc63306b2b1542ff704984487b32d066137b517a39f"
	validPolicyDefinitionJSON   = "{\"schema\":\"mousa.policy_definition.v1\",\"id\":\"d55989574364003f9c967bc63306b2b1542ff704984487b32d066137b517a39f\",\"namespace\":\"example.operator\",\"external_policy_id\":\"outbound-default\",\"external_policy_version\":\"2026-08-25\",\"definition_media_type\":\"application/example-policy+json\",\"definition_schema\":\"example.policy.v1\",\"definition_sha256\":\"393d82ee635666f0bd7b8bf2fc7159c05506b908936f14a1da32c8e8b71d21a5\",\"definition\":\"{\\\"default\\\":\\\"deny\\\"}\\n\"}\n"
)

func TestPolicyDefinitionExactRoundTripAndIdentity(t *testing.T) {
	digest, err := ParseSHA256("393d82ee635666f0bd7b8bf2fc7159c05506b908936f14a1da32c8e8b71d21a5")
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewPolicyDefinitionID("example.operator", "outbound-default", "2026-08-25", "application/example-policy+json", "example.policy.v1", digest)
	if err != nil {
		t.Fatalf("NewPolicyDefinitionID(): %v", err)
	}
	if id.String() != canonicalPolicyDefinitionID {
		t.Fatalf("NewPolicyDefinitionID() = %q, want %q", id.String(), canonicalPolicyDefinitionID)
	}
	record := PolicyDefinition{
		Schema:                PolicyDefinitionSchema,
		ID:                    id,
		Namespace:             "example.operator",
		ExternalPolicyID:      "outbound-default",
		ExternalPolicyVersion: "2026-08-25",
		DefinitionMediaType:   "application/example-policy+json",
		DefinitionSchema:      "example.policy.v1",
		DefinitionSHA256:      digest,
		Definition:            "{\"default\":\"deny\"}\n",
	}
	first, err := EncodePolicyDefinition(record)
	if err != nil {
		t.Fatalf("EncodePolicyDefinition(): %v", err)
	}
	second, err := EncodePolicyDefinition(record)
	if err != nil {
		t.Fatalf("EncodePolicyDefinition() again: %v", err)
	}
	if string(first) != validPolicyDefinitionJSON || !bytes.Equal(first, second) {
		t.Fatalf("EncodePolicyDefinition() = %q, second = %q", first, second)
	}
	decoded, err := DecodePolicyDefinition(first)
	if err != nil || !reflect.DeepEqual(decoded, record) {
		t.Fatalf("DecodePolicyDefinition() = %#v, %v", decoded, err)
	}
}

func TestPolicyDefinitionRejectsInvalidAndNonCanonical(t *testing.T) {
	record := validPolicyDefinition(t)
	testStrictID(t, canonicalPolicyDefinitionID, func(value string) (string, error) {
		id, err := ParsePolicyDefinitionID(value)
		return id.String(), err
	}, func(data []byte) (string, error) {
		var id PolicyDefinitionID
		err := json.Unmarshal(data, &id)
		return id.String(), err
	})

	for _, test := range []struct {
		name, input, field string
		code               ValidationCode
	}{
		{"unknown field", strings.TrimSuffix(validPolicyDefinitionJSON, "}\n") + ",\"extra\":true}\n", "extra", ValidationCodeUnknownField},
		{"uppercase digest", strings.Replace(validPolicyDefinitionJSON, "393d82ee", "393D82EE", 1), "definition_sha256", ValidationCodeInvalidDigest},
		{"zero digest", strings.Replace(validPolicyDefinitionJSON, "393d82ee635666f0bd7b8bf2fc7159c05506b908936f14a1da32c8e8b71d21a5", strings.Repeat("0", 64), 1), "definition_sha256", ValidationCodeInvalidDigest},
		{"digest mismatch", strings.Replace(validPolicyDefinitionJSON, `deny`, `allow`, 1), "definition_sha256", ValidationCodeInvalidDigest},
		{"trailing value", validPolicyDefinitionJSON + `{}`, "", ValidationCodeTrailingData},
		{"malformed trailing", validPolicyDefinitionJSON + `{`, "", ValidationCodeInvalidJSON},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodePolicyDefinition([]byte(test.input))
			requireValidationError(t, err, test.field, test.code)
		})
	}
	_, err := DecodePolicyDefinition([]byte{'{', '"', 0xff, '"', ':', '1', '}'})
	requireValidationError(t, err, "", ValidationCodeInvalidJSON)

	for _, test := range []struct {
		name, field string
		code        ValidationCode
		mutate      func(*PolicyDefinition)
	}{
		{"schema", "schema", ValidationCodeInvalidSchema, func(value *PolicyDefinition) { value.Schema = "wrong" }},
		{"zero ID", "id", ValidationCodeInvalidID, func(value *PolicyDefinition) { value.ID = PolicyDefinitionID{} }},
		{"empty namespace", "namespace", ValidationCodeInvalidValue, func(value *PolicyDefinition) { value.Namespace = "" }},
		{"invalid namespace UTF-8", "namespace", ValidationCodeInvalidValue, func(value *PolicyDefinition) { value.Namespace = string([]byte{0xff}) }},
		{"empty policy ID", "external_policy_id", ValidationCodeInvalidValue, func(value *PolicyDefinition) { value.ExternalPolicyID = "" }},
		{"empty policy version", "external_policy_version", ValidationCodeInvalidValue, func(value *PolicyDefinition) { value.ExternalPolicyVersion = "" }},
		{"empty media type", "definition_media_type", ValidationCodeInvalidValue, func(value *PolicyDefinition) { value.DefinitionMediaType = "" }},
		{"empty definition schema", "definition_schema", ValidationCodeInvalidValue, func(value *PolicyDefinition) { value.DefinitionSchema = "" }},
		{"zero digest", "definition_sha256", ValidationCodeInvalidDigest, func(value *PolicyDefinition) { value.DefinitionSHA256 = SHA256{} }},
		{"empty definition", "definition", ValidationCodeInvalidValue, func(value *PolicyDefinition) { value.Definition = "" }},
		{"invalid definition UTF-8", "definition", ValidationCodeInvalidValue, func(value *PolicyDefinition) { value.Definition = string([]byte{0xff}) }},
		{"digest mismatch", "definition_sha256", ValidationCodeInvalidDigest, func(value *PolicyDefinition) { value.Definition += " " }},
		{"identity", "id", ValidationCodeInvalidID, func(value *PolicyDefinition) { value.ID[0] ^= 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := record
			test.mutate(&value)
			requireValidationError(t, value.Validate(), test.field, test.code)
		})
	}

	base := record.ID
	mutations := []func(*PolicyDefinition){
		func(value *PolicyDefinition) { value.Namespace = " other " },
		func(value *PolicyDefinition) { value.ExternalPolicyID = "other" },
		func(value *PolicyDefinition) { value.ExternalPolicyVersion = "other" },
		func(value *PolicyDefinition) { value.DefinitionMediaType = "not parsed" },
		func(value *PolicyDefinition) { value.DefinitionSchema = ":not-a-uri" },
		func(value *PolicyDefinition) { value.Definition = strings.TrimSuffix(value.Definition, "\n") },
		func(value *PolicyDefinition) { value.Definition = " " + value.Definition },
	}
	for index, mutate := range mutations {
		value := record
		mutate(&value)
		if value.Definition != record.Definition {
			value.DefinitionSHA256 = SHA256(sha256.Sum256([]byte(value.Definition)))
		}
		id, err := NewPolicyDefinitionID(value.Namespace, value.ExternalPolicyID, value.ExternalPolicyVersion, value.DefinitionMediaType, value.DefinitionSchema, value.DefinitionSHA256)
		if err != nil || id == base {
			t.Fatalf("identity discriminator %d = %s, %v", index, id, err)
		}
		value.ID = id
		if _, err := EncodePolicyDefinition(value); err != nil {
			t.Fatalf("preserve discriminator %d: %v", index, err)
		}
	}
}

func validPolicyDefinition(t testing.TB) PolicyDefinition {
	t.Helper()
	digest, err := ParseSHA256("393d82ee635666f0bd7b8bf2fc7159c05506b908936f14a1da32c8e8b71d21a5")
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewPolicyDefinitionID("example.operator", "outbound-default", "2026-08-25", "application/example-policy+json", "example.policy.v1", digest)
	if err != nil {
		t.Fatal(err)
	}
	return PolicyDefinition{Schema: PolicyDefinitionSchema, ID: id, Namespace: "example.operator", ExternalPolicyID: "outbound-default", ExternalPolicyVersion: "2026-08-25", DefinitionMediaType: "application/example-policy+json", DefinitionSchema: "example.policy.v1", DefinitionSHA256: digest, Definition: "{\"default\":\"deny\"}\n"}
}

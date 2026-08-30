package mousa

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const (
	canonicalPolicyBindingID = "c435722c483fd2a673313568bb62ee3222a69f7a7512a642e70e88a1c6139abb"
	validPolicyBindingJSON   = "{\"schema\":\"mousa.policy_binding.v1\",\"id\":\"c435722c483fd2a673313568bb62ee3222a69f7a7512a642e70e88a1c6139abb\",\"namespace\":\"example.operator\",\"external_binding_id\":\"outbound-default\",\"external_binding_version\":\"1\",\"scope\":{\"kind\":\"deployment\"},\"policy_definition_id\":\"d55989574364003f9c967bc63306b2b1542ff704984487b32d066137b517a39f\"}\n"
)

func TestPolicyBindingExactRoundTripAndIdentity(t *testing.T) {
	definitionID, err := ParsePolicyDefinitionID(canonicalPolicyDefinitionID)
	if err != nil {
		t.Fatal(err)
	}
	scope := NewDeploymentPolicyScope()
	id, err := NewPolicyBindingID("example.operator", "outbound-default", "1", scope, definitionID)
	if err != nil {
		t.Fatalf("NewPolicyBindingID: %v", err)
	}
	if id.String() != canonicalPolicyBindingID {
		t.Fatalf("NewPolicyBindingID = %q, want %q", id.String(), canonicalPolicyBindingID)
	}
	record := PolicyBinding{Schema: PolicyBindingSchema, ID: id, Namespace: "example.operator", ExternalBindingID: "outbound-default", ExternalBindingVersion: "1", Scope: scope, PolicyDefinitionID: definitionID}
	first, err := EncodePolicyBinding(record)
	if err != nil {
		t.Fatalf("EncodePolicyBinding: %v", err)
	}
	second, err := EncodePolicyBinding(record)
	if err != nil {
		t.Fatalf("EncodePolicyBinding again: %v", err)
	}
	if string(first) != validPolicyBindingJSON || !bytes.Equal(first, second) {
		t.Fatalf("EncodePolicyBinding = %q, second = %q", first, second)
	}
	decoded, err := DecodePolicyBinding(first)
	if err != nil || !reflect.DeepEqual(decoded, record) {
		t.Fatalf("DecodePolicyBinding = %#v, %v", decoded, err)
	}
}

func TestPolicyActivationExactRoundTripAndIdentity(t *testing.T) {
	bindingID, err := ParsePolicyBindingID(canonicalPolicyBindingID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewPolicyActivationID("example.operator", "outbound-default", "activate-1")
	if err != nil {
		t.Fatal(err)
	}
	if id.String() != "935bf71d0700fe680df9d3741792aa2a18541cfb1cd762920ca91508ec60de6a" {
		t.Fatalf("NewPolicyActivationID = %s", id)
	}
	reason := "initial selection"
	record := PolicyActivation{Schema: PolicyActivationSchema, ID: id, Namespace: "example.operator", ExternalBindingID: "outbound-default", ExternalActivationID: "activate-1", ActiveBindingID: &bindingID, ActorID: "admin.example", ActorVersion: "1", OccurredAtUsec: 1, Reason: &reason}
	want := "{\"schema\":\"mousa.policy_activation.v1\",\"id\":\"935bf71d0700fe680df9d3741792aa2a18541cfb1cd762920ca91508ec60de6a\",\"namespace\":\"example.operator\",\"external_binding_id\":\"outbound-default\",\"external_activation_id\":\"activate-1\",\"expected_previous_activation_id\":null,\"active_binding_id\":\"c435722c483fd2a673313568bb62ee3222a69f7a7512a642e70e88a1c6139abb\",\"actor_id\":\"admin.example\",\"actor_version\":\"1\",\"occurred_at_usec\":1,\"reason\":\"initial selection\"}\n"
	encoded, err := EncodePolicyActivation(record)
	if err != nil || string(encoded) != want {
		t.Fatalf("EncodePolicyActivation = %q, %v", encoded, err)
	}
	decoded, err := DecodePolicyActivation(encoded)
	if err != nil || !reflect.DeepEqual(decoded, record) {
		t.Fatalf("DecodePolicyActivation = %#v, %v", decoded, err)
	}
}

func TestPolicyBindingAndActivationRejectInvalidContracts(t *testing.T) {
	definitionID, _ := ParsePolicyDefinitionID(canonicalPolicyDefinitionID)
	var raw [32]byte
	raw[0] = 1
	scopes := []PolicyScope{
		NewDeploymentPolicyScope(), NewSourcePolicyScope(SourceID(raw)), NewObservationPolicyScope(ObservationID(raw)),
		NewArtifactPolicyScope(ArtifactID(raw)), NewRepresentationPolicyScope(RepresentationID(raw)), NewSegmentPolicyScope(SegmentID(raw)),
	}
	for _, scope := range scopes {
		id, err := NewPolicyBindingID("n", "b", string(scope.Kind()), scope, definitionID)
		if err != nil {
			t.Fatalf("scope %s: %v", scope.Kind(), err)
		}
		record := PolicyBinding{Schema: PolicyBindingSchema, ID: id, Namespace: "n", ExternalBindingID: "b", ExternalBindingVersion: string(scope.Kind()), Scope: scope, PolicyDefinitionID: definitionID}
		encoded, err := EncodePolicyBinding(record)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodePolicyBinding(encoded)
		if err != nil || !reflect.DeepEqual(decoded, record) {
			t.Fatalf("scope %s round trip = %#v, %v", scope.Kind(), decoded, err)
		}
	}

	for _, input := range []string{
		strings.Replace(validPolicyBindingJSON, `{"kind":"deployment"}`, `{"kind":"deployment","id":"`+strings.Repeat("0", 64)+`"}`, 1),
		strings.Replace(validPolicyBindingJSON, `{"kind":"deployment"}`, `{"kind":"unknown"}`, 1),
		strings.TrimSuffix(validPolicyBindingJSON, "}\n") + ",\"extra\":true}\n",
		validPolicyBindingJSON + `{}`,
	} {
		if _, err := DecodePolicyBinding([]byte(input)); err == nil {
			t.Fatalf("DecodePolicyBinding accepted %q", input)
		}
	}
	if _, err := DecodePolicyBinding([]byte{'{', '"', 0xff, '"', ':', '1', '}'}); err == nil {
		t.Fatal("DecodePolicyBinding accepted invalid UTF-8")
	}

	bindingID, _ := ParsePolicyBindingID(canonicalPolicyBindingID)
	activationID, _ := NewPolicyActivationID("n", "b", "a")
	reason := strings.Repeat("x", 4097)
	activation := PolicyActivation{Schema: PolicyActivationSchema, ID: activationID, Namespace: "n", ExternalBindingID: "b", ExternalActivationID: "a", ActiveBindingID: &bindingID, ActorID: "actor", ActorVersion: "1", OccurredAtUsec: 1, Reason: &reason}
	if err := activation.Validate(); err == nil {
		t.Fatal("PolicyActivation accepted oversized reason")
	}
	reason = string([]byte{0xff})
	activation.Reason = &reason
	if err := activation.Validate(); err == nil {
		t.Fatal("PolicyActivation accepted invalid UTF-8 reason")
	}
	activation.Reason = nil
	activation.OccurredAtUsec = 0
	if err := activation.Validate(); err == nil {
		t.Fatal("PolicyActivation accepted nonpositive timestamp")
	}
	valid := strings.Replace(wantPolicyActivationJSON(t, bindingID), `"expected_previous_activation_id":null`, `"expected_previous_activation_id" : null`, 1)
	if _, err := DecodePolicyActivation([]byte(valid)); err != nil {
		t.Fatalf("JSON whitespace is valid input: %v", err)
	}
	missingNull := strings.Replace(wantPolicyActivationJSON(t, bindingID), `"expected_previous_activation_id":null,`, "", 1)
	if _, err := DecodePolicyActivation([]byte(missingNull)); err == nil {
		t.Fatal("DecodePolicyActivation accepted omitted nullable field")
	}
	var parsed PolicyActivationID
	if err := json.Unmarshal([]byte(`"`+strings.ToUpper(activationID.String())+`"`), &parsed); err == nil {
		t.Fatal("PolicyActivationID accepted uppercase")
	}
}

func wantPolicyActivationJSON(t testing.TB, bindingID PolicyBindingID) string {
	t.Helper()
	id, err := NewPolicyActivationID("n", "b", "a")
	if err != nil {
		t.Fatal(err)
	}
	return "{\"schema\":\"mousa.policy_activation.v1\",\"id\":\"" + id.String() + "\",\"namespace\":\"n\",\"external_binding_id\":\"b\",\"external_activation_id\":\"a\",\"expected_previous_activation_id\":null,\"active_binding_id\":\"" + bindingID.String() + "\",\"actor_id\":\"actor\",\"actor_version\":\"1\",\"occurred_at_usec\":1,\"reason\":null}\n"
}

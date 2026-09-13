package mousa

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func testPolicyEvaluationRequest(t testing.TB) PolicyEvaluationRequest {
	t.Helper()
	sourceID, err := NewSourceID("example.source", "source-1")
	if err != nil {
		t.Fatalf("NewSourceID: %v", err)
	}
	request := PolicyEvaluationRequest{
		Schema:            PolicyEvaluationRequestSchema,
		Action:            SourceRetrievalAction,
		CallerNamespace:   "example.caller",
		ExternalCallerID:  "caller-1",
		ExternalRequestID: "request-1",
		PurposeNamespace:  "example.purpose",
		ExternalPurposeID: "purpose-1",
		SourceID:          sourceID,
		RequestedAtUsec:   100,
	}
	id, err := NewPolicyEvaluationRequestID(request)
	if err != nil {
		t.Fatalf("NewPolicyEvaluationRequestID: %v", err)
	}
	request.ID = id
	return request
}

func testAllowSourceRetrievalDefinition(t testing.TB, namespace, externalPolicyID string) PolicyDefinition {
	t.Helper()
	return testRetrievalDefinition(t, namespace, externalPolicyID, "example.caller", "caller-1", "example.purpose", "purpose-1", SourceRetrievalEffectAllow)
}

func testRetrievalDefinition(t testing.TB, namespace, externalPolicyID, callerNamespace, externalCallerID, purposeNamespace, externalPurposeID string, effect SourceRetrievalEffect) PolicyDefinition {
	t.Helper()
	inner := SourceRetrievalPolicy{
		Schema:            SourceRetrievalPolicySchema,
		Action:            SourceRetrievalAction,
		CallerNamespace:   callerNamespace,
		ExternalCallerID:  externalCallerID,
		PurposeNamespace:  purposeNamespace,
		ExternalPurposeID: externalPurposeID,
		Effect:            effect,
	}
	data, err := EncodeSourceRetrievalPolicy(inner)
	if err != nil {
		t.Fatalf("EncodeSourceRetrievalPolicy: %v", err)
	}
	digest := SHA256(sha256.Sum256(data))
	id, err := NewPolicyDefinitionID(namespace, externalPolicyID, "1", SourceRetrievalPolicyMediaType, SourceRetrievalPolicySchema, digest)
	if err != nil {
		t.Fatalf("NewPolicyDefinitionID: %v", err)
	}
	return PolicyDefinition{
		Schema:                PolicyDefinitionSchema,
		ID:                    id,
		Namespace:             namespace,
		ExternalPolicyID:      externalPolicyID,
		ExternalPolicyVersion: "1",
		DefinitionMediaType:   SourceRetrievalPolicyMediaType,
		DefinitionSchema:      SourceRetrievalPolicySchema,
		DefinitionSHA256:      digest,
		Definition:            string(data),
	}
}

func testOpaqueDefinition(t testing.TB, namespace, externalPolicyID string) PolicyDefinition {
	t.Helper()
	return testRetrievalDefinitionWithFormat(t, namespace, externalPolicyID, "opaque", "opaque", "definition")
}

func testRetrievalDefinitionWithFormat(t testing.TB, namespace, externalPolicyID, mediaType, schema, definition string) PolicyDefinition {
	t.Helper()
	digest := SHA256(sha256.Sum256([]byte(definition)))
	id, err := NewPolicyDefinitionID(namespace, externalPolicyID, "1", mediaType, schema, digest)
	if err != nil {
		t.Fatalf("NewPolicyDefinitionID: %v", err)
	}
	return PolicyDefinition{
		Schema:                PolicyDefinitionSchema,
		ID:                    id,
		Namespace:             namespace,
		ExternalPolicyID:      externalPolicyID,
		ExternalPolicyVersion: "1",
		DefinitionMediaType:   mediaType,
		DefinitionSchema:      schema,
		DefinitionSHA256:      digest,
		Definition:            definition,
	}
}

func testEvaluationSnapshot(inputs ...PolicyEvaluationInput) PolicyEvaluationSnapshot {
	state := CollectionActive
	return PolicyEvaluationSnapshot{
		StatePresent:    true,
		CollectionState: &state,
		Inputs:          inputs,
	}
}

func TestSourceRetrievalPolicyCanonicalRoundTrip(t *testing.T) {
	policy := SourceRetrievalPolicy{
		Schema:            SourceRetrievalPolicySchema,
		Action:            SourceRetrievalAction,
		CallerNamespace:   "example.caller",
		ExternalCallerID:  "caller-1",
		PurposeNamespace:  "example.purpose",
		ExternalPurposeID: "purpose-1",
		Effect:            SourceRetrievalEffectAllow,
	}
	want := `{"schema":"mousa.policy.source_retrieval.v1","action":"source.retrieve","caller_namespace":"example.caller","external_caller_id":"caller-1","purpose_namespace":"example.purpose","external_purpose_id":"purpose-1","effect":"allow"}` + "\n"
	encoded, err := EncodeSourceRetrievalPolicy(policy)
	if err != nil || string(encoded) != want {
		t.Fatalf("EncodeSourceRetrievalPolicy = %q, %v", encoded, err)
	}
	decoded, err := DecodeSourceRetrievalPolicy(encoded)
	if err != nil || !reflect.DeepEqual(decoded, policy) {
		t.Fatalf("DecodeSourceRetrievalPolicy = %#v, %v", decoded, err)
	}
	for name, data := range map[string]string{
		"unknown field":     `{"schema":"mousa.policy.source_retrieval.v1","action":"source.retrieve","caller_namespace":"a","external_caller_id":"b","purpose_namespace":"c","external_purpose_id":"d","effect":"allow","extra":1}` + "\n",
		"reordered fields":  `{"action":"source.retrieve","schema":"mousa.policy.source_retrieval.v1","caller_namespace":"example.caller","external_caller_id":"caller-1","purpose_namespace":"example.purpose","external_purpose_id":"purpose-1","effect":"allow"}` + "\n",
		"whitespace":        "{\n\"schema\":\"mousa.policy.source_retrieval.v1\",\"action\":\"source.retrieve\",\"caller_namespace\":\"example.caller\",\"external_caller_id\":\"caller-1\",\"purpose_namespace\":\"example.purpose\",\"external_purpose_id\":\"purpose-1\",\"effect\":\"allow\"\n}\n",
		"unknown effect":    `{"schema":"mousa.policy.source_retrieval.v1","action":"source.retrieve","caller_namespace":"a","external_caller_id":"b","purpose_namespace":"c","external_purpose_id":"d","effect":"maybe"}` + "\n",
		"unknown action":    `{"schema":"mousa.policy.source_retrieval.v1","action":"evidence.export","caller_namespace":"a","external_caller_id":"b","purpose_namespace":"c","external_purpose_id":"d","effect":"allow"}` + "\n",
		"empty caller":      `{"schema":"mousa.policy.source_retrieval.v1","action":"source.retrieve","caller_namespace":"","external_caller_id":"b","purpose_namespace":"c","external_purpose_id":"d","effect":"allow"}` + "\n",
		"trailing data":     want + want,
		"duplicate keys":    `{"schema":"mousa.policy.source_retrieval.v1","schema":"mousa.policy.source_retrieval.v1","action":"source.retrieve","caller_namespace":"a","external_caller_id":"b","purpose_namespace":"c","external_purpose_id":"d","effect":"allow"}` + "\n",
		"non-object":        `"allow"` + "\n",
		"invalid utf8":      "\xff",
		"empty effect code": `{"schema":"mousa.policy.source_retrieval.v1","action":"source.retrieve","caller_namespace":"a","external_caller_id":"b","purpose_namespace":"c","external_purpose_id":"d","effect":""}` + "\n",
	} {
		if _, err := DecodeSourceRetrievalPolicy([]byte(data)); err == nil {
			t.Fatalf("%s decoded successfully, want rejection", name)
		}
	}
}

func TestPolicyEvaluationRequestIdentityAndValidation(t *testing.T) {
	request := testPolicyEvaluationRequest(t)
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePolicyEvaluationRequest(encoded)
	if err != nil || !reflect.DeepEqual(decoded, request) {
		t.Fatalf("DecodePolicyEvaluationRequest = %#v, %v", decoded, err)
	}

	var zeroID PolicyEvaluationRequestID
	if _, err := NewPolicyEvaluationRequestID(PolicyEvaluationRequest{Schema: PolicyEvaluationRequestSchema, Action: SourceRetrievalAction}); err == nil {
		t.Fatal("empty request derived an identity")
	}
	if _, err := NewPolicyEvaluationRequestID(PolicyEvaluationRequest{Schema: "other", Action: SourceRetrievalAction}); err == nil {
		t.Fatal("foreign schema derived an identity")
	}
	tampered := request
	tampered.Action = "evidence.export"
	if err := tampered.Validate(); err == nil {
		t.Fatal("unknown action accepted")
	}
	tampered = request
	tampered.ID = zeroID
	if err := tampered.Validate(); err == nil {
		t.Fatal("zero request ID accepted")
	}
	tampered = request
	tampered.ExternalRequestID = "request-2"
	if err := tampered.Validate(); err == nil {
		t.Fatal("identity mismatch accepted")
	}
	tampered = request
	tampered.RequestedAtUsec = 0
	if err := tampered.Validate(); err == nil {
		t.Fatal("non-positive requested_at_usec accepted")
	}
	tampered = request
	tampered.CallerNamespace = ""
	if err := tampered.Validate(); err == nil {
		t.Fatal("empty caller namespace accepted")
	}
	other := request
	other.ExternalRequestID = "request-2"
	otherID, err := NewPolicyEvaluationRequestID(other)
	if err != nil {
		t.Fatal(err)
	}
	if otherID == request.ID {
		t.Fatal("distinct requests share one identity")
	}
}

func TestEvaluateSourceRetrievalReasonOrderAndIdentity(t *testing.T) {
	request := testPolicyEvaluationRequest(t)
	allow := testAllowSourceRetrievalDefinition(t, "example", "allow")
	allowInput := PolicyEvaluationInput{
		Layer:        PolicyLayerDeployment,
		ActivationID: PolicyActivationID{1},
		BindingID:    PolicyBindingID{2},
		DefinitionID: allow.ID,
		Definition:   allow,
	}

	decision, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(allowInput), 500)
	if err != nil {
		t.Fatalf("EvaluateSourceRetrieval: %v", err)
	}
	if decision.Outcome != PolicyOutcomeAllow || len(decision.ReasonCodes) != 1 || decision.ReasonCodes[0] != PolicyReasonAllow {
		t.Fatalf("allow decision = %#v", decision.ReasonCodes)
	}
	if len(decision.PolicyInputs) != 1 || decision.PolicyInputs[0].Result != PolicyInputAllow {
		t.Fatalf("allow inputs = %#v", decision.PolicyInputs)
	}
	repeated, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(allowInput), 500)
	if err != nil || repeated.ID != decision.ID {
		t.Fatalf("repeated evaluation = %v, %v", repeated.ID, err)
	}
	differentTime, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(allowInput), 501)
	if err != nil || differentTime.ID == decision.ID {
		t.Fatal("evaluation time does not change the decision identity")
	}

	deny := testRetrievalDefinition(t, "example", "deny", "example.caller", "caller-1", "example.purpose", "purpose-1", SourceRetrievalEffectDeny)
	mismatch := testRetrievalDefinition(t, "example", "mismatch", "example.caller", "caller-1", "example.purpose", "purpose-2", SourceRetrievalEffectAllow)
	unsupported := testOpaqueDefinition(t, "example", "unsupported")
	malformedData := []byte(`{"schema":"mousa.policy.source_retrieval.v1","action":"source.retrieve","purpose_namespace":"example.purpose","caller_namespace":"example.caller","external_caller_id":"caller-1","external_purpose_id":"purpose-1","effect":"allow"}` + "\n")
	malformed := PolicyDefinition{
		Schema:                PolicyDefinitionSchema,
		Namespace:             "example",
		ExternalPolicyID:      "malformed",
		ExternalPolicyVersion: "1",
		DefinitionMediaType:   SourceRetrievalPolicyMediaType,
		DefinitionSchema:      SourceRetrievalPolicySchema,
		DefinitionSHA256:      SHA256(sha256.Sum256(malformedData)),
		Definition:            string(malformedData),
	}
	malformed.ID, err = NewPolicyDefinitionID(malformed.Namespace, malformed.ExternalPolicyID, malformed.ExternalPolicyVersion, malformed.DefinitionMediaType, malformed.DefinitionSchema, malformed.DefinitionSHA256)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []PolicyEvaluationInput{
		{Layer: PolicyLayerDeployment, ActivationID: PolicyActivationID{1}, BindingID: PolicyBindingID{2}, DefinitionID: deny.ID, Definition: deny},
		{Layer: PolicyLayerDeployment, ActivationID: PolicyActivationID{3}, BindingID: PolicyBindingID{4}, DefinitionID: mismatch.ID, Definition: mismatch},
		{Layer: PolicyLayerSource, ActivationID: PolicyActivationID{5}, BindingID: PolicyBindingID{6}, DefinitionID: unsupported.ID, Definition: unsupported},
		{Layer: PolicyLayerSource, ActivationID: PolicyActivationID{7}, BindingID: PolicyBindingID{8}, DefinitionID: malformed.ID, Definition: malformed},
		{Layer: PolicyLayerSource, ActivationID: PolicyActivationID{9}, BindingID: PolicyBindingID{10}, DefinitionID: allow.ID, Definition: allow},
	}
	denied, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(inputs...), 500)
	if err != nil {
		t.Fatalf("denied evaluation: %v", err)
	}
	if denied.Outcome != PolicyOutcomeDeny {
		t.Fatalf("outcome = %q", denied.Outcome)
	}
	wantReasons := []PolicyDecisionReason{PolicyReasonPolicyDeny, PolicyReasonPolicyRequestMismatch, PolicyReasonUnsupportedPolicyDefinition, PolicyReasonMalformedPolicyDefinition}
	if len(denied.ReasonCodes) != len(wantReasons) {
		t.Fatalf("reasons = %v", denied.ReasonCodes)
	}
	for index, reason := range wantReasons {
		if denied.ReasonCodes[index] != reason {
			t.Fatalf("reason[%d] = %q, want %q", index, denied.ReasonCodes[index], reason)
		}
	}
	if len(denied.PolicyInputs) != len(inputs) || denied.PolicyInputs[0].Result != PolicyInputDeny || denied.PolicyInputs[2].Result != PolicyInputUnsupportedDefinition || denied.PolicyInputs[3].Result != PolicyInputMalformedDefinition || denied.PolicyInputs[4].Result != PolicyInputAllow {
		t.Fatalf("input results = %#v", denied.PolicyInputs)
	}
	if err := denied.Validate(); err != nil {
		t.Fatalf("denied decision validation: %v", err)
	}

	missingState := testEvaluationSnapshot(allowInput)
	missingState.StatePresent = false
	missingState.CollectionState = nil
	missing, err := EvaluateSourceRetrieval(request, missingState, 500)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Outcome != PolicyOutcomeDeny || len(missing.ReasonCodes) != 1 || missing.ReasonCodes[0] != PolicyReasonMissingSourceState {
		t.Fatalf("missing state reasons = %v", missing.ReasonCodes)
	}
	if missing.CollectionState != nil || missing.CurrentWithdrawalID != nil {
		t.Fatalf("missing state evidence = %#v", missing)
	}

	withdrawnID, err := NewWithdrawalID(request.SourceID, "withdrawal-1")
	if err != nil {
		t.Fatal(err)
	}
	withdrawn := testEvaluationSnapshot(allowInput)
	withdrawn.CollectionState = func() *CollectionState { state := CollectionWithdrawn; return &state }()
	withdrawn.CurrentWithdrawalID = &withdrawnID
	sealed, err := EvaluateSourceRetrieval(request, withdrawn, 500)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Outcome != PolicyOutcomeDeny || sealed.ReasonCodes[0] != PolicyReasonSourceWithdrawn {
		t.Fatalf("withdrawn reasons = %v", sealed.ReasonCodes)
	}

	noDeployment := testEvaluationSnapshot()
	noDeployment.Inputs = []PolicyEvaluationInput{allowInput}
	noDeployment.Inputs[0].Layer = PolicyLayerSource
	unauthorized, err := EvaluateSourceRetrieval(request, noDeployment, 500)
	if err != nil {
		t.Fatal(err)
	}
	if unauthorized.Outcome != PolicyOutcomeDeny || len(unauthorized.ReasonCodes) != 1 || unauthorized.ReasonCodes[0] != PolicyReasonMissingDeploymentPolicy {
		t.Fatalf("missing deployment reasons = %v", unauthorized.ReasonCodes)
	}

	empty, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(), 500)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Outcome != PolicyOutcomeDeny || len(empty.ReasonCodes) != 1 || empty.ReasonCodes[0] != PolicyReasonMissingDeploymentPolicy {
		t.Fatalf("no input reasons = %v", empty.ReasonCodes)
	}
	if len(empty.PolicyInputs) != 0 {
		t.Fatalf("no-input decision carries inputs: %#v", empty.PolicyInputs)
	}

	if _, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(allowInput), 0); err == nil {
		t.Fatal("non-positive evaluation time accepted")
	}
}

func TestPolicyDecisionCanonicalEncoding(t *testing.T) {
	request := testPolicyEvaluationRequest(t)
	allow := testAllowSourceRetrievalDefinition(t, "example", "allow")
	input := PolicyEvaluationInput{Layer: PolicyLayerDeployment, ActivationID: PolicyActivationID{1}, BindingID: PolicyBindingID{2}, DefinitionID: allow.ID, Definition: allow}
	decision, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(input), 500)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodePolicyDecision(decision)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(encoded), "\n") {
		t.Fatal("canonical decision lacks trailing newline")
	}
	decoded, err := DecodePolicyDecision(encoded)
	if err != nil || !reflect.DeepEqual(decoded, decision) {
		t.Fatalf("DecodePolicyDecision = %#v, %v", decoded, err)
	}
	again, err := EncodePolicyDecision(decoded)
	if err != nil || string(again) != string(encoded) {
		t.Fatalf("re-encode = %q, %v", again, err)
	}

	for name, mutate := range map[string]func(*PolicyDecision){
		"unknown outcome": func(d *PolicyDecision) { d.Outcome = "maybe" },
		"empty reasons":   func(d *PolicyDecision) { d.ReasonCodes = nil },
		// No state and no inputs derives [missing_source_state, missing_deployment_policy]; the
		// swapped order must not encode, because reason order is part of the canonical contract.
		"reordered reasons": func(d *PolicyDecision) {
			d.StatePresent = false
			d.CollectionState = nil
			d.PolicyInputs = nil
			d.Outcome = PolicyOutcomeDeny
			d.ReasonCodes = []PolicyDecisionReason{PolicyReasonMissingDeploymentPolicy, PolicyReasonMissingSourceState}
		},
		"wrong id":        func(d *PolicyDecision) { d.ID = PolicyDecisionID{} },
		"wrong evaluator": func(d *PolicyDecision) { d.EvaluatorID = "other" },
		"wrong version":   func(d *PolicyDecision) { d.EvaluatorVersion = "2" },
		"zero time":       func(d *PolicyDecision) { d.EvaluatedAtUsec = 0 },
		"unknown layer":   func(d *PolicyDecision) { d.PolicyInputs[0].Layer = "observation" },
		"unknown result":  func(d *PolicyDecision) { d.PolicyInputs[0].Result = "maybe" },
		"zero activation": func(d *PolicyDecision) { d.PolicyInputs[0].PolicyActivationID = PolicyActivationID{} },
		"zero binding":    func(d *PolicyDecision) { d.PolicyInputs[0].PolicyBindingID = PolicyBindingID{} },
		"zero definition": func(d *PolicyDecision) { d.PolicyInputs[0].PolicyDefinitionID = PolicyDefinitionID{} },
	} {
		tampered := decision
		mutate(&tampered)
		if _, err := EncodePolicyDecision(tampered); err == nil {
			t.Fatalf("%s encoded successfully, want rejection", name)
		}
	}
	data := append([]byte(nil), encoded...)
	data[len(data)-2] = ' '
	if _, err := DecodePolicyDecision(data); err == nil {
		t.Fatal("non-canonical stored bytes accepted")
	}
}

func TestPolicyDecisionDecodeRejections(t *testing.T) {
	request := testPolicyEvaluationRequest(t)
	allow := testAllowSourceRetrievalDefinition(t, "example", "allow")
	input := PolicyEvaluationInput{Layer: PolicyLayerDeployment, ActivationID: PolicyActivationID{1}, BindingID: PolicyBindingID{2}, DefinitionID: allow.ID, Definition: allow}
	decision, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(input), 500)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodePolicyDecision(decision)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]func(map[string]any){
		"unknown field":       func(m map[string]any) { m["extra"] = 1 },
		"duplicate keys":      func(m map[string]any) {},
		"foreign outcome":     func(m map[string]any) { m["outcome"] = "maybe" },
		"foreign schema":      func(m map[string]any) { m["schema"] = "other" },
		"null inputs":         func(m map[string]any) { m["policy_inputs"] = nil },
		"missing input field": func(m map[string]any) { delete(any(m["policy_inputs"]).([]any)[0].(map[string]any), "layer") },
	} {
		payload := append([]byte(nil), encoded...)
		if name == "duplicate keys" {
			payload = []byte(`{"schema":"mousa.policy_decision.v1","schema":"mousa.policy_decision.v1"}`)
		} else {
			var generic map[string]any
			if err := json.Unmarshal(encoded, &generic); err != nil {
				t.Fatal(err)
			}
			mutation(generic)
			payload, err = json.Marshal(generic)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := DecodePolicyDecision(payload); err == nil {
			t.Fatalf("%s decoded successfully, want rejection", name)
		}
	}
	empty, err := EncodePolicyDecision(func() PolicyDecision {
		withoutInputs, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(), 500)
		if err != nil {
			t.Fatal(err)
		}
		return withoutInputs
	}())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(empty), `"policy_inputs":[]`) {
		t.Fatalf("no-input decision encodes %q", empty)
	}
}

// testTupleSHA256 recomputes the identity-contract tuple digest (8-byte big-endian length prefix per
// field, then field bytes) directly from the documented field list, without production identity code.
func testTupleSHA256(fields ...[]byte) [sha256.Size]byte {
	digest := sha256.New()
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		digest.Write(length[:])
		digest.Write(field)
	}
	var sum [sha256.Size]byte
	copy(sum[:], digest.Sum(nil))
	return sum
}

// TestPolicyDecisionGoldenVectors pins the exact canonical bytes and identities for one fixed request,
// policy, and decision. Any changed field, field order, encoding rule, or evaluation time breaks them.
func TestPolicyDecisionGoldenVectors(t *testing.T) {
	request := testPolicyEvaluationRequest(t)
	if got := hex.EncodeToString(request.ID[:]); got != "de97652c1f223588781f6f4ed33fe319839837ec51e444098fd7cd0d9dd42265" {
		t.Fatalf("request identity = %s", got)
	}
	sourceID, err := NewSourceID("example.source", "source-1")
	if err != nil {
		t.Fatal(err)
	}
	var time [8]byte
	binary.BigEndian.PutUint64(time[:], 100)
	want := testTupleSHA256(
		[]byte(PolicyEvaluationRequestSchema),
		[]byte(SourceRetrievalAction),
		[]byte("example.caller"),
		[]byte("caller-1"),
		[]byte("request-1"),
		[]byte("example.purpose"),
		[]byte("purpose-1"),
		sourceID[:],
		time[:],
	)
	if request.ID != PolicyEvaluationRequestID(want) {
		t.Fatalf("request identity does not match the contract field tuple: %s", request.ID)
	}

	inner, err := EncodeSourceRetrievalPolicy(SourceRetrievalPolicy{
		Schema:            SourceRetrievalPolicySchema,
		Action:            SourceRetrievalAction,
		CallerNamespace:   "example.caller",
		ExternalCallerID:  "caller-1",
		PurposeNamespace:  "example.purpose",
		ExternalPurposeID: "purpose-1",
		Effect:            SourceRetrievalEffectAllow,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantInner := `{"schema":"mousa.policy.source_retrieval.v1","action":"source.retrieve","caller_namespace":"example.caller","external_caller_id":"caller-1","purpose_namespace":"example.purpose","external_purpose_id":"purpose-1","effect":"allow"}` + "\n"
	if string(inner) != wantInner {
		t.Fatalf("inner policy bytes = %q, want %q", inner, wantInner)
	}

	definition := testAllowSourceRetrievalDefinition(t, "example", "allow")
	state := CollectionActive
	decision, err := EvaluateSourceRetrieval(request, PolicyEvaluationSnapshot{
		StatePresent:    true,
		CollectionState: &state,
		Inputs: []PolicyEvaluationInput{{
			Layer:        PolicyLayerDeployment,
			ActivationID: PolicyActivationID{1},
			BindingID:    PolicyBindingID{2},
			DefinitionID: definition.ID,
			Definition:   definition,
		}},
	}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(decision.ID[:]); got != "bebd0644e7a75353d01ff9d8203efaa4e97222cf710a115defe58005682de0c0" {
		t.Fatalf("decision identity = %s", got)
	}
	encoded, err := EncodePolicyDecision(decision)
	if err != nil {
		t.Fatal(err)
	}
	wantDecision := `{"schema":"mousa.policy_decision.v1","id":"bebd0644e7a75353d01ff9d8203efaa4e97222cf710a115defe58005682de0c0","request":{"schema":"mousa.policy_evaluation_request.v1","id":"de97652c1f223588781f6f4ed33fe319839837ec51e444098fd7cd0d9dd42265","action":"source.retrieve","caller_namespace":"example.caller","external_caller_id":"caller-1","external_request_id":"request-1","purpose_namespace":"example.purpose","external_purpose_id":"purpose-1","source_id":"3d2fe6ed8b99d0cadef9f4ff85fb524a7ef5fddd8b249600acb40c7c8c615521","requested_at_usec":100},"evaluator_id":"mousa.policy.source_retrieval","evaluator_version":"1","evaluated_at_usec":500,"state_present":true,"collection_state":"active","current_withdrawal_id":null,"outcome":"allow","reason_codes":["allow"],"policy_inputs":[{"layer":"deployment","policy_activation_id":"0100000000000000000000000000000000000000000000000000000000000000","policy_binding_id":"0200000000000000000000000000000000000000000000000000000000000000","policy_definition_id":"d079e91a19edb3f0e35dcbd87710768fbcf687dc59d2b28c107d9a1eea6bf1d1","result":"allow"}]}` + "\n"
	if string(encoded) != wantDecision {
		t.Fatalf("canonical decision bytes = %q, want %q", encoded, wantDecision)
	}
	// The identity binds the evaluation time: one changed input must change the pinned identity.
	later, err := EvaluateSourceRetrieval(request, PolicyEvaluationSnapshot{
		StatePresent:    true,
		CollectionState: &state,
		Inputs: []PolicyEvaluationInput{{
			Layer:        PolicyLayerDeployment,
			ActivationID: PolicyActivationID{1},
			BindingID:    PolicyBindingID{2},
			DefinitionID: definition.ID,
			Definition:   definition,
		}},
	}, 501)
	if err != nil {
		t.Fatal(err)
	}
	if later.ID == decision.ID {
		t.Fatal("decision identity ignored the evaluation time")
	}
}

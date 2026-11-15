package mousa

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"reflect"
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

func testEvaluationSnapshot(access SourceAccess) PolicyEvaluationSnapshot {
	state := CollectionActive
	return PolicyEvaluationSnapshot{StatePresent: true, CollectionState: &state, Access: access}
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
	decision, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(SourceAccessAllow), 500)
	if err != nil {
		t.Fatalf("EvaluateSourceRetrieval: %v", err)
	}
	if decision.Outcome != PolicyOutcomeAllow || !reflect.DeepEqual(decision.ReasonCodes, []PolicyDecisionReason{PolicyReasonAllow}) || decision.Access != SourceAccessAllow {
		t.Fatalf("allow decision = %#v", decision)
	}
	repeated, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(SourceAccessAllow), 500)
	if err != nil || repeated.ID != decision.ID {
		t.Fatalf("repeated evaluation = %v, %v", repeated.ID, err)
	}
	differentTime, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(SourceAccessAllow), 501)
	if err != nil || differentTime.ID == decision.ID {
		t.Fatal("evaluation time does not change the decision identity")
	}

	denied, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(SourceAccessDeny), 500)
	if err != nil {
		t.Fatal(err)
	}
	if denied.Outcome != PolicyOutcomeDeny || !reflect.DeepEqual(denied.ReasonCodes, []PolicyDecisionReason{PolicyReasonAccessDenied}) || denied.ID == decision.ID {
		t.Fatalf("denied decision = %#v", denied)
	}

	withdrawnID, err := NewWithdrawalID(request.SourceID, "withdrawal-1")
	if err != nil {
		t.Fatal(err)
	}
	// Lifecycle reasons come before access, and an allow never overrides withdrawal.
	for _, access := range []SourceAccess{SourceAccessAllow, SourceAccessDeny} {
		withdrawn := testEvaluationSnapshot(access)
		withdrawn.CollectionState = func() *CollectionState { state := CollectionWithdrawn; return &state }()
		withdrawn.CurrentWithdrawalID = &withdrawnID
		sealed, err := EvaluateSourceRetrieval(request, withdrawn, 500)
		if err != nil {
			t.Fatal(err)
		}
		want := []PolicyDecisionReason{PolicyReasonSourceWithdrawn}
		if access == SourceAccessDeny {
			want = append(want, PolicyReasonAccessDenied)
		}
		if sealed.Outcome != PolicyOutcomeDeny || !reflect.DeepEqual(sealed.ReasonCodes, want) {
			t.Fatalf("withdrawn %s reasons = %v", access, sealed.ReasonCodes)
		}
	}

	missingState := PolicyEvaluationSnapshot{Access: SourceAccessAllow}
	missing, err := EvaluateSourceRetrieval(request, missingState, 500)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Outcome != PolicyOutcomeDeny || !reflect.DeepEqual(missing.ReasonCodes, []PolicyDecisionReason{PolicyReasonMissingSourceState}) {
		t.Fatalf("missing state reasons = %v", missing.ReasonCodes)
	}

	if _, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(""), 500); err == nil {
		t.Fatal("unset access accepted")
	}
	if _, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(SourceAccessAllow), 0); err == nil {
		t.Fatal("non-positive evaluation time accepted")
	}
}

func TestPolicyDecisionCanonicalEncoding(t *testing.T) {
	request := testPolicyEvaluationRequest(t)
	decision, err := EvaluateSourceRetrieval(request, testEvaluationSnapshot(SourceAccessAllow), 500)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodePolicyDecision(decision)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodePolicyDecision(encoded)
	if err != nil || !reflect.DeepEqual(decoded, decision) {
		t.Fatalf("DecodePolicyDecision = %#v, %v", decoded, err)
	}
	for name, mutate := range map[string]func(*PolicyDecision){
		"unknown outcome": func(d *PolicyDecision) { d.Outcome = "maybe" },
		"empty reasons":   func(d *PolicyDecision) { d.ReasonCodes = nil },
		// Withdrawn plus denied derives [source_withdrawn, access_denied]; reason order is part of the contract.
		"reordered reasons": func(d *PolicyDecision) {
			d.Access = SourceAccessDeny
			d.Outcome = PolicyOutcomeDeny
			d.StatePresent = false
			d.CollectionState = nil
			d.ReasonCodes = []PolicyDecisionReason{PolicyReasonAccessDenied, PolicyReasonMissingSourceState}
		},
		"denied but allowed": func(d *PolicyDecision) { d.Access = SourceAccessDeny },
		"unknown access":     func(d *PolicyDecision) { d.Access = "maybe" },
		"wrong id":           func(d *PolicyDecision) { d.ID = PolicyDecisionID{} },
		"wrong evaluator":    func(d *PolicyDecision) { d.EvaluatorID = "other" },
		"wrong version":      func(d *PolicyDecision) { d.EvaluatorVersion = "1" },
		"zero time":          func(d *PolicyDecision) { d.EvaluatedAtUsec = 0 },
	} {
		tampered := decision
		mutate(&tampered)
		if _, err := EncodePolicyDecision(tampered); err == nil {
			t.Fatalf("%s encoded successfully, want rejection", name)
		}
	}
	for name, mutation := range map[string]func(map[string]any){
		"unknown field":   func(m map[string]any) { m["extra"] = 1 },
		"foreign schema":  func(m map[string]any) { m["schema"] = "mousa.policy_decision.v1" },
		"missing access":  func(m map[string]any) { delete(m, "access") },
		"flipped access":  func(m map[string]any) { m["access"] = "deny" },
		"foreign outcome": func(m map[string]any) { m["outcome"] = "maybe" },
	} {
		var generic map[string]any
		if err := json.Unmarshal(encoded, &generic); err != nil {
			t.Fatal(err)
		}
		mutation(generic)
		payload, err := json.Marshal(generic)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodePolicyDecision(payload); err == nil {
			t.Fatalf("%s decoded successfully, want rejection", name)
		}
	}
	data := append([]byte(nil), encoded...)
	data[len(data)-2] = ' '
	if _, err := DecodePolicyDecision(data); err == nil {
		t.Fatal("non-canonical stored bytes accepted")
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

// TestPolicyDecisionGoldenVectors pins the exact canonical bytes and identities for one fixed request
// and decision. Any changed field, field order, encoding rule, or evaluation time breaks them.
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

	state := CollectionActive
	decision, err := EvaluateSourceRetrieval(request, PolicyEvaluationSnapshot{StatePresent: true, CollectionState: &state, Access: SourceAccessAllow}, 500)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(decision.ID[:]); got != "53e2a2966b7f084087dc0e2e37eb001cfe0d2c73ba968f10ac6ea58d360c5fbd" {
		t.Fatalf("decision identity = %s", got)
	}
	var evaluated [8]byte
	binary.BigEndian.PutUint64(evaluated[:], 500)
	wantID := testTupleSHA256(
		[]byte(PolicyDecisionSchema), request.ID[:], []byte(PolicyEvaluatorID), []byte(PolicyEvaluatorVersion), evaluated[:],
		[]byte{1}, []byte(CollectionActive), nil, []byte(SourceAccessAllow), []byte(PolicyOutcomeAllow), []byte(PolicyReasonAllow),
	)
	if decision.ID != PolicyDecisionID(wantID) {
		t.Fatal("decision identity does not match the contract field tuple")
	}
	encoded, err := EncodePolicyDecision(decision)
	if err != nil {
		t.Fatal(err)
	}
	wantDecision := `{"schema":"mousa.policy_decision.v2","id":"53e2a2966b7f084087dc0e2e37eb001cfe0d2c73ba968f10ac6ea58d360c5fbd","request":{"schema":"mousa.policy_evaluation_request.v1","id":"de97652c1f223588781f6f4ed33fe319839837ec51e444098fd7cd0d9dd42265","action":"source.retrieve","caller_namespace":"example.caller","external_caller_id":"caller-1","external_request_id":"request-1","purpose_namespace":"example.purpose","external_purpose_id":"purpose-1","source_id":"3d2fe6ed8b99d0cadef9f4ff85fb524a7ef5fddd8b249600acb40c7c8c615521","requested_at_usec":100},"evaluator_id":"mousa.policy.source_retrieval","evaluator_version":"2","evaluated_at_usec":500,"state_present":true,"collection_state":"active","current_withdrawal_id":null,"access":"allow","outcome":"allow","reason_codes":["allow"]}` + "\n"
	if string(encoded) != wantDecision {
		t.Fatalf("canonical decision bytes = %q, want %q", encoded, wantDecision)
	}
	// The identity binds the evaluation time: one changed input must change the pinned identity.
	later, err := EvaluateSourceRetrieval(request, PolicyEvaluationSnapshot{StatePresent: true, CollectionState: &state, Access: SourceAccessAllow}, 501)
	if err != nil {
		t.Fatal(err)
	}
	if later.ID == decision.ID {
		t.Fatal("decision identity ignored the evaluation time")
	}
}

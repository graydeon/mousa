package mousa

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func testTrailRequest(t *testing.T) PolicyEvaluationRequest {
	t.Helper()
	request := PolicyEvaluationRequest{
		Schema:            PolicyEvaluationRequestSchema,
		Action:            "source.retrieve",
		CallerNamespace:   "example.harness",
		ExternalCallerID:  "agent:alpha",
		ExternalRequestID: "req:1",
		PurposeNamespace:  "example.harness",
		ExternalPurposeID: "task:answer",
		SourceID:          mustSourceIDForTrail(t),
		RequestedAtUsec:   1000,
	}
	id, err := NewPolicyEvaluationRequestID(request)
	if err != nil {
		t.Fatal(err)
	}
	request.ID = id
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	return request
}

func trailDecision(t *testing.T, request PolicyEvaluationRequest, outcome PolicyDecisionOutcome) PolicyDecision {
	t.Helper()
	inputs := []PolicyDecisionInput{{
		Layer:              PolicyLayerDeployment,
		PolicyActivationID: mustActivationIDForTrail(t),
		PolicyBindingID:    mustBindingIDForTrail(t),
		PolicyDefinitionID: mustDefinitionIDForTrail(t),
		Result:             PolicyInputAllow,
	}}
	reasons := []PolicyDecisionReason{PolicyReasonAllow}
	if outcome == PolicyOutcomeDeny {
		inputs[0].Result = PolicyInputDeny
		reasons = []PolicyDecisionReason{PolicyReasonPolicyDeny}
	}
	decision := PolicyDecision{
		Schema:           PolicyDecisionSchema,
		Request:          request,
		EvaluatorID:      "mousa.policy.source_retrieval",
		EvaluatorVersion: "1",
		EvaluatedAtUsec:  2000,
		StatePresent:     true,
		CollectionState:  collectionStatePtr(CollectionActive),
		Outcome:          outcome,
		ReasonCodes:      reasons,
		PolicyInputs:     inputs,
	}
	id, err := NewPolicyDecisionID(decision)
	if err != nil {
		t.Fatalf("NewPolicyDecisionID: %v", err)
	}
	decision.ID = id
	if err := decision.Validate(); err != nil {
		t.Fatalf("decision validate: %v", err)
	}
	return decision
}

func trailCandidates(t *testing.T) []VerifiedLexicalCandidate {
	t.Helper()
	candidates := []VerifiedLexicalCandidate{
		{Segment: trailSegment(t, 1), Text: "four bytes", BM25: 1.0, Disposition: CandidateAccepted, FinalRank: 1},
		{Segment: trailSegment(t, 2), Text: "sixteen bytes here", BM25: 2.0, Disposition: CandidateAccepted, FinalRank: 2},
		{Segment: trailSegment(t, 3), Text: "", BM25: 3.0, Disposition: CandidateRejected, Reasons: []LifecycleReason{ReasonSourceWithdrawn}},
		{Segment: trailSegment(t, 4), Text: "tail", BM25: 4.0, Disposition: CandidateAccepted, FinalRank: 3},
	}
	return candidates
}

func trailInput(candidates []VerifiedLexicalCandidate) []TrailCandidate {
	input := make([]TrailCandidate, len(candidates))
	for index, candidate := range candidates {
		textBytes := uint64(len(candidate.Text))
		if candidate.Disposition == CandidateRejected {
			textBytes = 0
		}
		input[index] = TrailCandidate{
			SegmentID:     candidate.Segment.ID,
			ContentSHA256: candidate.Segment.ContentSHA256,
			FinalRank:     candidate.FinalRank,
			TextBytes:     textBytes,
			Disposition:   candidate.Disposition,
			Reasons:       candidate.Reasons,
		}
	}
	return input
}

func TestPackVerifiedLexicalCandidatesSelectsInVerifiedOrder(t *testing.T) {
	candidates := trailCandidates(t)
	plan, err := PackVerifiedLexicalCandidates(trailInput(candidates), 100)
	if err != nil {
		t.Fatalf("PackVerifiedLexicalCandidates: %v", err)
	}
	want := []bool{true, true, false, true}
	for index, selected := range want {
		if plan.Selected[index] != selected {
			t.Fatalf("selection[%d] = %v, want %v", index, plan.Selected[index], selected)
		}
	}
	if plan.UsedBytes != uint64(len("four bytes")+len("sixteen bytes here")+len("tail")) {
		t.Fatalf("used bytes = %d", plan.UsedBytes)
	}
	if plan.PacketID == (ContextPacketID{}) {
		t.Fatal("packet ID is zero")
	}
}

func TestPackVerifiedLexicalCandidatesSkipsAndContinues(t *testing.T) {
	candidates := trailCandidates(t)
	// Budget fits ranks 1 and 3 but not 2: rank 2 is skipped, rank 3 still fits.
	plan, err := PackVerifiedLexicalCandidates(trailInput(candidates), uint64(len("four bytes")+len("tail")))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Selected[0] || plan.Selected[1] || !plan.Selected[3] {
		t.Fatalf("selection = %v, want rank1 and rank3 selected with rank2 skipped", plan.Selected)
	}
	if plan.UsedBytes != uint64(len("four bytes")+len("tail")) {
		t.Fatalf("used bytes = %d", plan.UsedBytes)
	}
}

func TestPackVerifiedLexicalCandidatesRejectsZeroBudget(t *testing.T) {
	if _, err := PackVerifiedLexicalCandidates(trailInput(trailCandidates(t)), 0); err == nil {
		t.Fatal("zero budget must be rejected")
	}
}

func TestNewSourceTrailBindsExplanation(t *testing.T) {
	request := testTrailRequest(t)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	trail, err := NewSourceTrail(request, decision, "alpha", trailCandidates(t), 100)
	if err != nil {
		t.Fatalf("NewSourceTrail: %v", err)
	}
	if trail.RequestID != request.ID.String() || trail.DecisionID != decision.ID.String() {
		t.Fatal("trail does not bind its parents")
	}
	if trail.UsedBytes == 0 || trail.PacketID == "" {
		t.Fatal("trail lacks pack accounting")
	}
	if err := trail.Validate(); err != nil {
		t.Fatalf("trail validate: %v", err)
	}
	changed := trail
	changed.Expression = "beta"
	changedID, err := NewSourceTrailID(changed)
	if err != nil {
		t.Fatal(err)
	}
	if changedID == trail.ID {
		t.Fatal("trail ID does not bind the expression")
	}
}

func TestNewSourceTrailDenyProducesEmptySelection(t *testing.T) {
	request := testTrailRequest(t)
	decision := trailDecision(t, request, PolicyOutcomeDeny)
	trail, err := NewSourceTrail(request, decision, "alpha", nil, 64)
	if err != nil {
		t.Fatalf("NewSourceTrail: %v", err)
	}
	if len(trail.Candidates) != 0 || trail.UsedBytes != 0 {
		t.Fatalf("deny trail candidates = %d used = %d, want empty", len(trail.Candidates), trail.UsedBytes)
	}
	if err := trail.Validate(); err != nil {
		t.Fatalf("deny trail validate: %v", err)
	}
}

func TestTrailValidateRejectsTamperedDerivedValues(t *testing.T) {
	request := testTrailRequest(t)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	trail, err := NewSourceTrail(request, decision, "alpha", trailCandidates(t), 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := trail.Validate(); err != nil {
		t.Fatal(err)
	}

	used := trail.UsedBytes
	trail.UsedBytes = used + 1
	if err := trail.Validate(); err == nil {
		t.Fatal("used_bytes tamper must be rejected")
	}
	trail.UsedBytes = used

	packet := trail.PacketID
	trail.PacketID = strings.Repeat("0", 64)
	if err := trail.Validate(); err == nil {
		t.Fatal("packet_id tamper must be rejected")
	}
	trail.PacketID = packet

	selected := trail.Candidates[0].Selected
	trail.Candidates[0].Selected = !selected
	if err := trail.Validate(); err == nil {
		t.Fatal("selection tamper must change the packet ID and be rejected")
	}
}

func TestNewSourceTrailRejectsMismatchedParents(t *testing.T) {
	request := testTrailRequest(t)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	other := testTrailRequest(t)
	other.ExternalRequestID = "req:2"
	otherID, err := NewPolicyEvaluationRequestID(other)
	if err != nil {
		t.Fatal(err)
	}
	other.ID = otherID
	if _, err := NewSourceTrail(other, decision, "alpha", nil, 64); err == nil {
		t.Fatal("decision from another request must be rejected")
	}
	if _, err := NewSourceTrail(request, decision, "", nil, 64); err == nil {
		t.Fatal("empty expression must be rejected")
	}
	if _, err := NewSourceTrail(request, decision, "alpha", nil, 0); err == nil {
		t.Fatal("zero budget must be rejected")
	}
}

func mustSourceIDForTrail(t *testing.T) SourceID {
	t.Helper()
	id, err := NewSourceID("example.corpus", "doc:trail")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustActivationIDForTrail(t *testing.T) PolicyActivationID {
	t.Helper()
	var id PolicyActivationID
	for index := range id {
		id[index] = byte(index + 1)
	}
	return id
}

func mustBindingIDForTrail(t *testing.T) PolicyBindingID {
	t.Helper()
	var id PolicyBindingID
	for index := range id {
		id[index] = byte(index + 2)
	}
	return id
}

func mustDefinitionIDForTrail(t *testing.T) PolicyDefinitionID {
	t.Helper()
	var id PolicyDefinitionID
	for index := range id {
		id[index] = byte(index + 3)
	}
	return id
}

func trailSegment(t *testing.T, seed byte) Segment {
	t.Helper()
	content := []byte{seed}
	digest := sha256.Sum256(content)
	segmentID, err := NewSegmentID(RepresentationID(digest), NewTextByteRangeSelector(0, 1), SHA256(digest))
	if err != nil {
		t.Fatal(err)
	}
	return Segment{
		Schema:           SegmentSchema,
		ID:               segmentID,
		RepresentationID: RepresentationID(digest),
		Selector:         NewTextByteRangeSelector(0, 1),
		ContentSHA256:    SHA256(digest),
	}
}

func collectionStatePtr(state CollectionState) *CollectionState {
	copied := state
	return &copied
}

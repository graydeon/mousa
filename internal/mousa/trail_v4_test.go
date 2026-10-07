package mousa

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The golden v4 trail bytes in testdata/trail-v4.json were derived outside this package from the
// documented construction: SHA-256 over each tuple field prefixed by its 8-byte big-endian length,
// in the field order described in the derivation script, with a nil identity written as an empty
// field and 32 nonzero bytes for a present one. The identity below is a fixed expectation rather
// than an output of the implementation under test.
const goldenV4TrailID = "fd473ce702a9be73763eaf5a942a9c22f38bdeb5a28da4c7e4b1b8cc7138f6ad"

// v4Request builds one valid policy evaluation request for the supplied source.
func v4Request(t testing.TB, sourceID SourceID) PolicyEvaluationRequest {
	t.Helper()
	request := PolicyEvaluationRequest{
		Schema:            PolicyEvaluationRequestSchema,
		Action:            "source.retrieve",
		CallerNamespace:   "example.harness",
		ExternalCallerID:  "agent:alpha",
		ExternalRequestID: "req:v4",
		PurposeNamespace:  "example.harness",
		ExternalPurposeID: "task:answer",
		SourceID:          sourceID,
		RequestedAtUsec:   1000,
	}
	id, err := NewPolicyEvaluationRequestID(request)
	if err != nil {
		t.Fatalf("NewPolicyEvaluationRequestID(): %v", err)
	}
	request.ID = id
	if err := request.Validate(); err != nil {
		t.Fatalf("request.Validate(): %v", err)
	}
	return request
}

// v4Declaration returns the canonical golden declaration as the active declaration of its own source.
func v4Declaration(t testing.TB) SupersessionDeclaration {
	t.Helper()
	declaration := supersessionSelectionDeclaration(t)
	return declaration
}

// v4DeactivationState returns one verified activation state that consulted the source and selected
// no declaration: the recorded deactivation event with a null declaration.
func v4DeactivationState(t testing.TB, sourceID SourceID) *SupersessionActivationState {
	t.Helper()
	state := supersessionSelectionState(t, v4Declaration(t))
	state.SourceID = sourceID
	state.ActiveDeclarationID = nil
	return &state
}

// v4Candidates returns fixture A: sizes [4, 8, 2] at ranks 1, 2 and 3, where rank 1 is the exact
// pinned predecessor representation and ranks 2 and 3 are unrelated representations of the source.
func v4Candidates(t testing.TB, declaration SupersessionDeclaration) []VerifiedLexicalCandidate {
	t.Helper()
	return []VerifiedLexicalCandidate{
		supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "abcd", 1, CandidateAccepted),
		supersessionCandidate(t, supersessionRepresentation("v4-other-1"), declaration.SourceID, "abcdefgh", 2, CandidateAccepted),
		supersessionCandidate(t, supersessionRepresentation("v4-other-2"), declaration.SourceID, "xy", 3, CandidateAccepted),
	}
}

// v4TwoRowCandidates returns fixture B: two segments of the pinned predecessor representation at
// ranks 1 and 2 plus one survivor at rank 3.
func v4TwoRowCandidates(t testing.TB, declaration SupersessionDeclaration) []VerifiedLexicalCandidate {
	t.Helper()
	return []VerifiedLexicalCandidate{
		supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "old-a", 1, CandidateAccepted),
		supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "old-bb", 2, CandidateAccepted),
		supersessionCandidate(t, supersessionRepresentation("v4-other-3"), declaration.SourceID, "new", 3, CandidateAccepted),
	}
}

// v4DuplicateCandidates returns fixture C: a withheld copy of the shared text, one surviving copy and
// one later byte-equal survivor that exact-v1 has to displace.
func v4DuplicateCandidates(t testing.TB, declaration SupersessionDeclaration) []VerifiedLexicalCandidate {
	t.Helper()
	return []VerifiedLexicalCandidate{
		supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "same text", 1, CandidateAccepted),
		supersessionCandidate(t, supersessionRepresentation("v4-other-4"), declaration.SourceID, "same text", 2, CandidateAccepted),
		supersessionCandidate(t, supersessionRepresentation("v4-other-5"), declaration.SourceID, "same text", 3, CandidateAccepted),
	}
}

// cloneV4Trail copies a trail, its candidate rows and its consultation member so a mutation cannot
// alias the fixture it was copied from.
func cloneV4Trail(trail SourceTrail) SourceTrail {
	cloned := trail
	cloned.Candidates = make([]TrailCandidate, len(trail.Candidates))
	for index, candidate := range trail.Candidates {
		cloned.Candidates[index] = candidate
		cloned.Candidates[index].Reasons = append([]LifecycleReason(nil), candidate.Reasons...)
	}
	if trail.Supersession != nil {
		member := *trail.Supersession
		if trail.Supersession.ActivationID != nil {
			id := *trail.Supersession.ActivationID
			member.ActivationID = &id
		}
		if trail.Supersession.DeclarationID != nil {
			id := *trail.Supersession.DeclarationID
			member.DeclarationID = &id
		}
		member.Dispositions = append([]SupersessionDisposition(nil), trail.Supersession.Dispositions...)
		cloned.Supersession = &member
	}
	return cloned
}

// requireV4IdentityBinds mutates one identity-bound field, asserts the mutated trail no longer
// validates under its stale identity, and asserts the derived identity changed. When the mutation
// leaves the record otherwise valid, the derived identity is also asserted to validate, so the test
// separates an identity boundary from an unrelated rejection.
func requireV4IdentityBinds(t *testing.T, base SourceTrail, mutate func(*SourceTrail), stillValid bool) {
	t.Helper()
	changed := cloneV4Trail(base)
	mutate(&changed)
	if changed.Validate() == nil {
		t.Fatal("a mutated v4 trail validated under its stale identity")
	}
	derived, err := NewSourceTrailID(changed)
	if err != nil {
		t.Fatalf("NewSourceTrailID(mutated): %v", err)
	}
	if derived == base.ID {
		t.Fatal("the mutated field is not bound by the trail identity")
	}
	if stillValid {
		changed.ID = derived
		if err := changed.Validate(); err != nil {
			t.Fatalf("the mutation should only change the identity: %v", err)
		}
	}
}

func TestSourceTrailV4GoldenBytesAndIdentity(t *testing.T) {
	golden, err := os.ReadFile("testdata/trail-v4.json")
	if err != nil {
		t.Fatal(err)
	}
	trail, err := DecodeSourceTrail(golden)
	if err != nil {
		t.Fatalf("DecodeSourceTrail(golden v4): %v", err)
	}
	if trail.ID.String() != goldenV4TrailID {
		t.Fatalf("decoded id = %s, want %s", trail.ID, goldenV4TrailID)
	}
	if trail.Schema != SourceTrailSchemaV4 || trail.PackingPolicy != PackingOriginal {
		t.Fatalf("decoded schema = %s policy = %s", trail.Schema, trail.PackingPolicy)
	}
	if trail.Supersession == nil || !trail.Supersession.Consulted {
		t.Fatal("decoded member is absent or unconsulted")
	}
	if trail.Supersession.ActivationID.String() != strings.Repeat("33", 32) || trail.Supersession.DeclarationID.String() != strings.Repeat("44", 32) {
		t.Fatalf("decoded identities = %v / %v", trail.Supersession.ActivationID, trail.Supersession.DeclarationID)
	}
	if len(trail.Supersession.Dispositions) != 1 {
		t.Fatalf("dispositions = %d, want 1", len(trail.Supersession.Dispositions))
	}
	row := trail.Supersession.Dispositions[0]
	if row.Selection != SupersessionSuperseded || row.SegmentID.String() != strings.Repeat("a1", 32) ||
		row.PredecessorItemID != "docs/mooring" || row.PredecessorRepresentationID.String() != strings.Repeat("55", 32) ||
		row.SuccessorItemID != "docs/mooring-corrected" || row.SuccessorRepresentationID.String() != strings.Repeat("66", 32) {
		t.Fatalf("decoded row = %+v", row)
	}
	wantSelected := []bool{false, true, false}
	for index, candidate := range trail.Candidates {
		if candidate.Selected != wantSelected[index] || candidate.TextBytes != []uint64{5, 4, 8}[index] {
			t.Fatalf("candidate %d = %+v", index, candidate)
		}
	}
	if trail.UsedBytes != 4 {
		t.Fatalf("used_bytes = %d, want 4", trail.UsedBytes)
	}
	encoded, err := EncodeSourceTrail(trail)
	if err != nil {
		t.Fatalf("EncodeSourceTrail(golden): %v", err)
	}
	if !bytes.Equal(encoded, golden) {
		t.Fatalf("re-encoded golden bytes changed:\n got %s\nwant %s", encoded, golden)
	}
	derived, err := NewSourceTrailID(trail)
	if err != nil {
		t.Fatal(err)
	}
	if derived.String() != goldenV4TrailID {
		t.Fatalf("derived id = %s, want %s", derived, goldenV4TrailID)
	}
}

func TestSourceTrailV4ActiveDeclarationWithholdsPinnedPredecessor(t *testing.T) {
	declaration := v4Declaration(t)
	state := supersessionSelectionState(t, declaration)
	request := v4Request(t, declaration.SourceID)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	candidates := v4Candidates(t, declaration)
	trail, err := NewSourceTrailWithSupersession(request, decision, "mooring", candidates, 5, PackingOriginal, &state, &declaration)
	if err != nil {
		t.Fatalf("NewSourceTrailWithSupersession: %v", err)
	}
	if trail.Schema != SourceTrailSchemaV4 || trail.PackingPolicy != PackingOriginal {
		t.Fatalf("schema = %s policy = %s", trail.Schema, trail.PackingPolicy)
	}
	if err := trail.Validate(); err != nil {
		t.Fatalf("trail.Validate(): %v", err)
	}
	// Rank 1 is withheld: present, accepted, unselected, canonical size kept, no packing omission.
	withheld := trail.Candidates[0]
	if withheld.Disposition != CandidateAccepted || withheld.Selected || withheld.TextBytes != 4 || withheld.Omission != "" || withheld.DuplicateOf != "" || withheld.FinalRank != 1 {
		t.Fatalf("withheld candidate = %+v", withheld)
	}
	// Rank 2 is an ordinary budget skip of a survivor: it has no omission field and no row.
	if trail.Candidates[1].Selected || trail.Candidates[1].Omission != "" || trail.Candidates[1].TextBytes != 8 {
		t.Fatalf("survivor budget skip = %+v", trail.Candidates[1])
	}
	if !trail.Candidates[2].Selected || trail.Candidates[2].TextBytes != 2 {
		t.Fatalf("survivor rank 3 = %+v", trail.Candidates[2])
	}
	if trail.UsedBytes != 2 {
		t.Fatalf("used_bytes = %d, want 2", trail.UsedBytes)
	}
	member := trail.Supersession
	if member == nil || !member.Consulted || member.ActivationID == nil || *member.ActivationID != state.CurrentActivationID {
		t.Fatalf("member = %+v", member)
	}
	if member.DeclarationID == nil || *member.DeclarationID != declaration.ID || len(member.Dispositions) != 1 {
		t.Fatalf("member declaration = %+v", member)
	}
	expectedRow := supersessionExpectedRow(declaration, candidates[0])
	if member.Dispositions[0] != expectedRow {
		t.Fatalf("row = %+v, want %+v", member.Dispositions[0], expectedRow)
	}
	// The released selection is fixed by the packet identity the same way a default trail's is.
	plan, err := NewContextPacketID(trail.Candidates, 5, []bool{false, false, true})
	if err != nil {
		t.Fatal(err)
	}
	if trail.PacketID != plan.String() {
		t.Fatalf("packet id = %s, want the surviving selection's identity %s", trail.PacketID, plan)
	}
	encoded, err := EncodeSourceTrail(trail)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSourceTrail(encoded)
	if err != nil {
		t.Fatalf("DecodeSourceTrail(encoded v4): %v", err)
	}
	if decoded.ID != trail.ID || !reflect.DeepEqual(decoded.Supersession, trail.Supersession) {
		t.Fatalf("round trip lost the member: %+v", decoded.Supersession)
	}
	reencoded, err := EncodeSourceTrail(decoded)
	if err != nil || !bytes.Equal(reencoded, encoded) {
		t.Fatalf("round trip changed the bytes: %v", err)
	}
}

func TestSourceTrailV4NoHistoryKeepsDefaultSelectionAndPacketIdentity(t *testing.T) {
	declaration := v4Declaration(t)
	request := v4Request(t, declaration.SourceID)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	candidates := v4Candidates(t, declaration)
	defaultTrail, err := NewSourceTrail(request, decision, "mooring", candidates, 5, PackingOriginal)
	if err != nil {
		t.Fatal(err)
	}
	trail, err := NewSourceTrailWithSupersession(request, decision, "mooring", candidates, 5, PackingOriginal, nil, nil)
	if err != nil {
		t.Fatalf("NewSourceTrailWithSupersession(no history): %v", err)
	}
	member := trail.Supersession
	if member == nil || !member.Consulted || member.ActivationID != nil || member.DeclarationID != nil || len(member.Dispositions) != 0 {
		t.Fatalf("no-history member = %+v", member)
	}
	if member.Dispositions == nil {
		t.Fatal("no-history member must carry an explicit empty disposition array")
	}
	if defaultTrail.PacketID != trail.PacketID || defaultTrail.UsedBytes != trail.UsedBytes {
		t.Fatalf("released selection changed: packet %s/%s used %d/%d", defaultTrail.PacketID, trail.PacketID, defaultTrail.UsedBytes, trail.UsedBytes)
	}
	if defaultTrail.ID == trail.ID || defaultTrail.Schema == trail.Schema {
		t.Fatal("an opted-in trail must not share the default trail's version or identity")
	}
	for index := range candidates {
		if defaultTrail.Candidates[index].Selected != trail.Candidates[index].Selected {
			t.Fatalf("candidate %d selection changed", index)
		}
	}
	encoded, err := EncodeSourceTrail(trail)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"supersession":{"consulted":true,"activation_id":null,"declaration_id":null,"dispositions":[]}`) {
		t.Fatalf("canonical v4 member = %s", encoded)
	}
}

func TestSourceTrailV4DeactivationRecordsConsultedEventWithoutSuppression(t *testing.T) {
	declaration := v4Declaration(t)
	state := v4DeactivationState(t, declaration.SourceID)
	request := v4Request(t, declaration.SourceID)
	candidates := v4Candidates(t, declaration)
	trail, err := NewSourceTrailWithSupersession(request, trailDecision(t, request, PolicyOutcomeAllow), "mooring", candidates, 5, PackingOriginal, state, nil)
	if err != nil {
		t.Fatalf("NewSourceTrailWithSupersession(deactivation): %v", err)
	}
	member := trail.Supersession
	if member == nil || !member.Consulted || member.ActivationID == nil || *member.ActivationID != state.CurrentActivationID {
		t.Fatalf("deactivation member = %+v", member)
	}
	if member.DeclarationID != nil || len(member.Dispositions) != 0 {
		t.Fatalf("deactivation must select no declaration and withhold nothing: %+v", member)
	}
	if !trail.Candidates[0].Selected || trail.UsedBytes != 4 {
		t.Fatal("a deactivated declaration must not suppress the pinned predecessor")
	}
}

func TestSourceTrailV4DenialIsUnconsulted(t *testing.T) {
	declaration := v4Declaration(t)
	request := v4Request(t, declaration.SourceID)
	decision := trailDecision(t, request, PolicyOutcomeDeny)
	for _, policy := range []string{PackingOriginal, PackingExactV1} {
		trail, err := NewSourceTrailWithSupersession(request, decision, "mooring", nil, 64, policy, nil, nil)
		if err != nil {
			t.Fatalf("NewSourceTrailWithSupersession(deny, %s): %v", policy, err)
		}
		member := trail.Supersession
		if member == nil || member.Consulted || member.ActivationID != nil || member.DeclarationID != nil || len(member.Dispositions) != 0 {
			t.Fatalf("denial member = %+v", member)
		}
		if len(trail.Candidates) != 0 || trail.UsedBytes != 0 || trail.PackingPolicy != policy {
			t.Fatalf("denial trail = %+v", trail)
		}
	}
	state := supersessionSelectionState(t, declaration)
	candidates := v4Candidates(t, declaration)
	for _, test := range []struct {
		name        string
		candidates  []VerifiedLexicalCandidate
		activation  *SupersessionActivationState
		declaration *SupersessionDeclaration
	}{
		{name: "candidates", candidates: candidates},
		{name: "activation", activation: &state},
		{name: "declaration", declaration: &declaration},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewSourceTrailWithSupersession(request, decision, "mooring", test.candidates, 64, PackingOriginal, test.activation, test.declaration); err == nil {
				t.Fatal("a denial accepted administrative input it must not consult")
			}
		})
	}
}

func TestSourceTrailV4DeclarationRequiresItsEventAndSource(t *testing.T) {
	declaration := v4Declaration(t)
	request := v4Request(t, declaration.SourceID)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	candidates := v4Candidates(t, declaration)
	if _, err := NewSourceTrailWithSupersession(request, decision, "mooring", candidates, 5, PackingOriginal, nil, &declaration); err == nil {
		t.Fatal("a selected declaration without its activation event must be rejected")
	}
	foreign := declaration
	foreign.SourceID = supersessionForeignSource(t)
	state := supersessionSelectionState(t, declaration)
	if _, err := NewSourceTrailWithSupersession(request, decision, "mooring", candidates, 5, PackingOriginal, &state, &foreign); err == nil {
		t.Fatal("a cross-source declaration must be rejected")
	}
	other := declaration
	other.Basis = "another basis"
	other.ID = newSupersessionDeclarationID(t, other)
	if _, err := NewSourceTrailWithSupersession(request, decision, "mooring", candidates, 5, PackingOriginal, &state, &other); err == nil {
		t.Fatal("a declaration the activation state does not name must be rejected")
	}
}

func TestSourceTrailV4MultipleRowsFollowOriginalRankOrder(t *testing.T) {
	declaration := v4Declaration(t)
	state := supersessionSelectionState(t, declaration)
	request := v4Request(t, declaration.SourceID)
	candidates := v4TwoRowCandidates(t, declaration)
	trail, err := NewSourceTrailWithSupersession(request, trailDecision(t, request, PolicyOutcomeAllow), "mooring", candidates, 6, PackingOriginal, &state, &declaration)
	if err != nil {
		t.Fatalf("NewSourceTrailWithSupersession(two rows): %v", err)
	}
	rows := trail.Supersession.Dispositions
	if len(rows) != 2 {
		t.Fatalf("dispositions = %d, want 2", len(rows))
	}
	for index, row := range rows {
		if row.DeclarationID != declaration.ID || row.PredecessorRepresentationID != declaration.PredecessorRepresentationID {
			t.Fatalf("row %d = %+v", index, row)
		}
		if row.SegmentID != candidates[index].Segment.ID {
			t.Fatalf("row %d names %s, want %s", index, row.SegmentID, candidates[index].Segment.ID)
		}
		if trail.Candidates[index].Selected || trail.Candidates[index].TextBytes != uint64(len(candidates[index].Text)) {
			t.Fatalf("withheld candidate %d = %+v", index, trail.Candidates[index])
		}
	}
	if !trail.Candidates[2].Selected || trail.UsedBytes != 3 {
		t.Fatalf("surviving selection = %+v used %d", trail.Candidates, trail.UsedBytes)
	}
}

func TestSourceTrailV4ExactV1WithheldCopyIsNotTheRetainedDuplicate(t *testing.T) {
	declaration := v4Declaration(t)
	state := supersessionSelectionState(t, declaration)
	request := v4Request(t, declaration.SourceID)
	candidates := v4DuplicateCandidates(t, declaration)
	trail, err := NewSourceTrailWithSupersession(request, trailDecision(t, request, PolicyOutcomeAllow), "mooring", candidates, 64, PackingExactV1, &state, &declaration)
	if err != nil {
		t.Fatalf("NewSourceTrailWithSupersession(exact-v1): %v", err)
	}
	if trail.PackingPolicy != PackingExactV1 || len(trail.Supersession.Dispositions) != 1 {
		t.Fatalf("trail = %+v", trail)
	}
	withheld, retained, displaced := trail.Candidates[0], trail.Candidates[1], trail.Candidates[2]
	if withheld.Selected || withheld.Omission != "" || withheld.DuplicateOf != "" || withheld.TextBytes != uint64(len("same text")) {
		t.Fatalf("withheld candidate = %+v", withheld)
	}
	if !retained.Selected || retained.Omission != "" {
		t.Fatalf("retained survivor = %+v", retained)
	}
	if displaced.Selected || displaced.Omission != "duplicate" || displaced.DuplicateOf != retained.SegmentID.String() {
		t.Fatalf("displaced duplicate = %+v", displaced)
	}
	if trail.UsedBytes != uint64(len("same text")) {
		t.Fatalf("used_bytes = %d", trail.UsedBytes)
	}
	encoded, err := EncodeSourceTrail(trail)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSourceTrail(encoded)
	if err != nil || decoded.ID != trail.ID {
		t.Fatalf("exact-v1 round trip: %v", err)
	}
}

func TestSourceTrailV4RejectsTheMemberOnEarlierVersions(t *testing.T) {
	golden, err := os.ReadFile("testdata/trail-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`null`, `{"consulted":false,"activation_id":null,"declaration_id":null,"dispositions":[]}`} {
		t.Run(value, func(t *testing.T) {
			var record map[string]any
			if err := json.Unmarshal(golden, &record); err != nil {
				t.Fatal(err)
			}
			record["supersession"] = json.RawMessage(value)
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSourceTrail(data); err == nil {
				t.Fatal("a v1 record accepted the v4 member")
			}
		})
	}
	// In-memory records reject the member for every earlier version too.
	request := v4Request(t, v4Declaration(t).SourceID)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	base, err := NewSourceTrail(request, decision, "mooring", v4Candidates(t, v4Declaration(t)), 5, PackingOriginal)
	if err != nil {
		t.Fatal(err)
	}
	member := &SupersessionSelection{Consulted: false, Dispositions: []SupersessionDisposition{}}
	for _, schema := range []string{SourceTrailSchema, SourceTrailSchemaV2, SourceTrailSchemaV3} {
		changed := cloneV4Trail(base)
		changed.Schema = schema
		if schema == SourceTrailSchemaV2 {
			changed.PackingPolicy = PackingExactV1
		}
		changed.Supersession = member
		if err := changed.Validate(); err == nil {
			t.Fatalf("%s accepted the v4 member", schema)
		}
	}
	// The v4 record requires the member as a closed object, not an absent or null field.
	encoded, err := EncodeSourceTrail(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, replacement := range []string{"", `,"supersession":null`} {
		var record map[string]any
		if err := json.Unmarshal(encoded, &record); err != nil {
			t.Fatal(err)
		}
		record["schema"] = SourceTrailSchemaV4
		record["packing_policy"] = PackingOriginal
		delete(record, "supersession")
		data, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if replacement != "" {
			data = append(data[:len(data)-1], []byte(replacement+"}")...)
		}
		if _, err := DecodeSourceTrail(data); err == nil {
			t.Fatalf("a v4 record accepted %q for its required member", replacement)
		}
	}
}

// mutateTrailRecord decodes one canonical trail, applies one mutation and re-encodes it, so a codec
// expectation is checked against the recorded bytes rather than an in-memory record.
func mutateTrailRecord(t testing.TB, data []byte, mutate func(record map[string]any)) []byte {
	t.Helper()
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	mutate(record)
	changed, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return changed
}

func trailRecordSupersession(t testing.TB, record map[string]any) map[string]any {
	t.Helper()
	member, ok := record["supersession"].(map[string]any)
	if !ok {
		t.Fatal("record has no supersession object")
	}
	return member
}

func trailRecordRows(t testing.TB, record map[string]any) []any {
	t.Helper()
	rows, ok := trailRecordSupersession(t, record)["dispositions"].([]any)
	if !ok {
		t.Fatal("member has no disposition array")
	}
	return rows
}

func TestSourceTrailV4RejectsInvalidMembers(t *testing.T) {
	declaration := v4Declaration(t)
	state := supersessionSelectionState(t, declaration)
	request := v4Request(t, declaration.SourceID)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	canonical, err := EncodeSourceTrail(mustV4Trail(t, request, decision, v4Candidates(t, declaration), 5, PackingOriginal, &state, &declaration))
	if err != nil {
		t.Fatal(err)
	}
	twoRow, err := EncodeSourceTrail(mustV4Trail(t, request, decision, v4TwoRowCandidates(t, declaration), 6, PackingOriginal, &state, &declaration))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		source []byte
		mutate func(record map[string]any)
		field  string
	}{
		{name: "missing consulted", source: canonical, field: "supersession", mutate: func(r map[string]any) { delete(trailRecordSupersession(t, r), "consulted") }},
		{name: "null consulted", source: canonical, field: "supersession", mutate: func(r map[string]any) { trailRecordSupersession(t, r)["consulted"] = nil }},
		{name: "missing activation_id", source: canonical, field: "supersession", mutate: func(r map[string]any) { delete(trailRecordSupersession(t, r), "activation_id") }},
		{name: "missing declaration_id", source: canonical, field: "supersession", mutate: func(r map[string]any) { delete(trailRecordSupersession(t, r), "declaration_id") }},
		{name: "missing dispositions", source: canonical, field: "supersession", mutate: func(r map[string]any) { delete(trailRecordSupersession(t, r), "dispositions") }},
		{name: "null dispositions", source: canonical, field: "supersession.dispositions", mutate: func(r map[string]any) { trailRecordSupersession(t, r)["dispositions"] = nil }},
		{name: "unknown member field", source: canonical, field: "enforced", mutate: func(r map[string]any) { trailRecordSupersession(t, r)["enforced"] = true }},
		{name: "unknown row field", source: canonical, field: "reason", mutate: func(r map[string]any) { trailRecordRows(t, r)[0].(map[string]any)["reason"] = "editorial" }},
		{name: "rows without a declaration", source: canonical, field: "dispositions", mutate: func(r map[string]any) { trailRecordSupersession(t, r)["declaration_id"] = nil }},
		{name: "declaration without its event", source: canonical, field: "activation_id", mutate: func(r map[string]any) { trailRecordSupersession(t, r)["activation_id"] = nil }},
		{name: "zero activation identity", source: canonical, field: "activation_id", mutate: func(r map[string]any) { trailRecordSupersession(t, r)["activation_id"] = strings.Repeat("0", 64) }},
		{name: "row names another declaration", source: canonical, field: "dispositions[0].declaration_id", mutate: func(r map[string]any) {
			trailRecordRows(t, r)[0].(map[string]any)["declaration_id"] = strings.Repeat("99", 32)
		}},
		{name: "unknown selection value", source: canonical, field: "dispositions[0].selection", mutate: func(r map[string]any) {
			trailRecordRows(t, r)[0].(map[string]any)["selection"] = "superseded-by-rank"
		}},
		{name: "empty predecessor item", source: canonical, field: "predecessor_item_id", mutate: func(r map[string]any) {
			trailRecordRows(t, r)[0].(map[string]any)["predecessor_item_id"] = ""
		}},
		{name: "equal successor item", source: canonical, field: "successor_item_id", mutate: func(r map[string]any) {
			trailRecordRows(t, r)[0].(map[string]any)["successor_item_id"] = "docs/mooring"
		}},
		{name: "unknown row segment", source: canonical, field: "dispositions[0].segment_id", mutate: func(r map[string]any) {
			trailRecordRows(t, r)[0].(map[string]any)["segment_id"] = strings.Repeat("77", 32)
		}},
		{name: "row digest disagrees with candidate", source: canonical, field: "dispositions[0].content_sha256", mutate: func(r map[string]any) {
			trailRecordRows(t, r)[0].(map[string]any)["content_sha256"] = strings.Repeat("88", 32)
		}},
		{name: "selection claims withholding but records no row", source: canonical, field: "packing", mutate: func(r map[string]any) {
			trailRecordSupersession(t, r)["dispositions"] = []any{}
		}},
		{name: "swapped row order", source: twoRow, field: "ordered", mutate: func(r map[string]any) {
			rows := trailRecordRows(t, r)
			rows[0], rows[1] = rows[1], rows[0]
		}},
		{name: "duplicate row", source: canonical, field: "dispositions[1].segment_id", mutate: func(r map[string]any) {
			rows := trailRecordRows(t, r)
			trailRecordSupersession(t, r)["dispositions"] = append(rows, rows[0])
		}},
		{name: "row names a rejected candidate", source: canonical, field: "dispositions[0].segment_id", mutate: func(r map[string]any) {
			candidate := r["candidates"].([]any)[0].(map[string]any)
			candidate["disposition"] = string(CandidateRejected)
			candidate["final_rank"] = float64(0)
			candidate["text_bytes"] = float64(0)
			candidate["reasons"] = []any{string(ReasonSourceWithdrawn)}
		}},
		{name: "row names a selected candidate", source: canonical, field: "packing", mutate: func(r map[string]any) {
			r["candidates"].([]any)[0].(map[string]any)["selected"] = true
			r["used_bytes"] = float64(4)
		}},
		{name: "survivor selected beyond the budget", source: canonical, field: "packing", mutate: func(r map[string]any) {
			r["candidates"].([]any)[1].(map[string]any)["selected"] = true
			r["used_bytes"] = float64(8)
		}},
		{name: "used_bytes disagrees", source: canonical, field: "used_bytes", mutate: func(r map[string]any) { r["used_bytes"] = float64(3) }},
		{name: "missing packing policy", source: canonical, field: "packing", mutate: func(r map[string]any) { delete(r, "packing_policy") }},
		{name: "unknown packing policy", source: canonical, field: "packing", mutate: func(r map[string]any) { r["packing_policy"] = "semantic" }},
		{name: "v4 cannot carry the member of another version", source: canonical, field: "supersession", mutate: func(r map[string]any) { r["schema"] = SourceTrailSchemaV2 }},
		{name: "associated passages", source: canonical, field: "associated", mutate: func(r map[string]any) {
			r["associated"] = []any{map[string]any{"segment_id": strings.Repeat("aa", 32)}}
		}},
		{name: "association omissions", source: canonical, field: "association_omissions", mutate: func(r map[string]any) {
			r["association_omissions"] = []any{map[string]any{"from_item": "docs/a", "to_item": "docs/b", "reason": "budget"}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := mutateTrailRecord(t, test.source, test.mutate)
			_, err := DecodeSourceTrail(data)
			if err == nil {
				t.Fatal("invalid v4 record decoded")
			}
			if !strings.Contains(err.Error(), test.field) {
				t.Fatalf("rejected for the wrong reason: %v", err)
			}
		})
	}
	// Duplicate keys inside the member are rejected like every other contract.
	t.Run("duplicate member key", func(t *testing.T) {
		duplicated := strings.Replace(string(canonical), `"consulted":true,`, `"consulted":true,"consulted":true,`, 1)
		if duplicated == string(canonical) {
			t.Fatal("fixture member changed")
		}
		if _, err := DecodeSourceTrail([]byte(duplicated)); err == nil {
			t.Fatal("duplicate member key decoded")
		}
	})
	// A structurally valid v4 record still re-validates after a clean round trip.
	if decoded, err := DecodeSourceTrail(canonical); err != nil {
		t.Fatalf("the unmutated fixture must decode: %v", err)
	} else if err := decoded.Validate(); err != nil {
		t.Fatalf("the unmutated fixture must validate: %v", err)
	}
}

func TestSourceTrailV4IdentityBindsEveryMember(t *testing.T) {
	declaration := v4Declaration(t)
	state := supersessionSelectionState(t, declaration)
	request := v4Request(t, declaration.SourceID)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	base := mustV4Trail(t, request, decision, v4Candidates(t, declaration), 5, PackingOriginal, &state, &declaration)
	otherActivation := SupersessionActivationID{}
	otherActivation[0] = 9
	otherDeclaration := declaration.ID
	otherDeclaration[0] ^= 0xff
	requireV4IdentityBinds(t, base, func(changed *SourceTrail) {
		changed.Supersession.ActivationID = &otherActivation
	}, true)
	requireV4IdentityBinds(t, base, func(changed *SourceTrail) {
		changed.Supersession.DeclarationID = &otherDeclaration
		for index := range changed.Supersession.Dispositions {
			changed.Supersession.Dispositions[index].DeclarationID = otherDeclaration
		}
	}, true)
	requireV4IdentityBinds(t, base, func(changed *SourceTrail) {
		changed.Supersession.Dispositions[0].PredecessorRepresentationID = supersessionRepresentation("other-pin")
	}, true)
	requireV4IdentityBinds(t, base, func(changed *SourceTrail) {
		changed.Supersession.Dispositions[0].SuccessorItemID = "docs/other"
	}, true)
	requireV4IdentityBinds(t, base, func(changed *SourceTrail) {
		changed.Supersession.Consulted = false
		changed.Supersession.ActivationID = nil
		changed.Supersession.DeclarationID = nil
		changed.Supersession.Dispositions = []SupersessionDisposition{}
	}, false)
	requireV4IdentityBinds(t, base, func(changed *SourceTrail) {
		changed.Candidates[0].TextBytes = 6
	}, false)
	requireV4IdentityBinds(t, base, func(changed *SourceTrail) {
		changed.Expression = "mooring-v2"
	}, true)
	// The consultation flag is also rejected structurally when it disagrees with the outcome.
	unconsulted := cloneV4Trail(base)
	unconsulted.Supersession.Consulted = false
	unconsulted.Supersession.ActivationID = nil
	unconsulted.Supersession.DeclarationID = nil
	unconsulted.Supersession.Dispositions = []SupersessionDisposition{}
	unconsulted.ID, _ = NewSourceTrailID(unconsulted)
	if err := unconsulted.Validate(); err == nil {
		t.Fatal("an allowed v4 record must record consultation")
	}
	denied := cloneV4Trail(base)
	denied.Outcome = string(PolicyOutcomeDeny)
	denied.Candidates = []TrailCandidate{}
	denied.UsedBytes = 0
	denied.ID, _ = NewSourceTrailID(denied)
	if err := denied.Validate(); err == nil {
		t.Fatal("a denied v4 record must not claim consultation")
	}
}

func TestSourceTrailV4DoesNotMutateItsInputs(t *testing.T) {
	declaration := v4Declaration(t)
	state := supersessionSelectionState(t, declaration)
	request := v4Request(t, declaration.SourceID)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	candidates := v4Candidates(t, declaration)
	candidatesBefore := cloneVerifiedLexicalCandidates(candidates)
	stateBefore := state
	declarationBefore := declaration
	expected, err := BuildSupersessionSelection(request.SourceID, decision.Outcome, candidates, &state, &declaration)
	if err != nil {
		t.Fatalf("BuildSupersessionSelection(): %v", err)
	}
	trail := mustV4Trail(t, request, decision, candidates, 5, PackingOriginal, &state, &declaration)
	if !reflect.DeepEqual(candidates, candidatesBefore) {
		t.Fatalf("verified candidates were mutated: %+v", candidates)
	}
	if state != stateBefore || declaration != declarationBefore {
		t.Fatal("the supplied administrative records were mutated")
	}
	if !reflect.DeepEqual(*trail.Supersession, expected) {
		t.Fatalf("member = %s", renderSupersessionSelection(t, *trail.Supersession))
	}
	// The member owns its identities and rows: changing them later cannot move the caller's records.
	member := trail.Supersession
	original := *member.ActivationID
	moved := original
	moved[0] ^= 0xff
	*member.ActivationID = moved
	member.Dispositions[0].PredecessorItemID = "docs/moved"
	if state.CurrentActivationID != original || declaration.PredecessorItemID != declarationBefore.PredecessorItemID {
		t.Fatal("the member aliases the caller's administrative records")
	}
	if candidates[0].Segment.ID != candidatesBefore[0].Segment.ID {
		t.Fatal("the member aliases a supplied candidate")
	}
}

func mustV4Trail(t testing.TB, request PolicyEvaluationRequest, decision PolicyDecision, candidates []VerifiedLexicalCandidate, budget uint64, policy string, activation *SupersessionActivationState, declaration *SupersessionDeclaration) SourceTrail {
	t.Helper()
	trail, err := NewSourceTrailWithSupersession(request, decision, "mooring", candidates, budget, policy, activation, declaration)
	if err != nil {
		t.Fatalf("NewSourceTrailWithSupersession(): %v", err)
	}
	return trail
}

package mousa

import (
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"testing"
)

// supersessionSelectionDeclaration returns the canonical golden declaration, validated, so the
// selection fixtures pin real identities rather than values this test invents.
func supersessionSelectionDeclaration(t testing.TB) SupersessionDeclaration {
	t.Helper()
	declaration := goldenSupersessionDeclaration(t)
	if err := declaration.Validate(); err != nil {
		t.Fatalf("declaration.Validate(): %v", err)
	}
	return declaration
}

// supersessionSelectionState returns one verified activation state selecting the supplied declaration.
// The state owns its declaration pointer, so a test can mutate it and observe aliasing.
func supersessionSelectionState(t testing.TB, declaration SupersessionDeclaration) SupersessionActivationState {
	t.Helper()
	selected := declaration.ID
	return SupersessionActivationState{
		SourceID:            declaration.SourceID,
		CurrentActivationID: mustParseActivationID(t, goldenSupersessionActivationID),
		ActiveDeclarationID: &selected,
	}
}

// supersessionRepresentation derives one nonzero representation identity from a label, so a fixture can
// name a distinct revision without a second constructor.
func supersessionRepresentation(label string) RepresentationID {
	return RepresentationID(sha256.Sum256([]byte("representation:" + label)))
}

// supersessionCandidate builds one already verified lexical candidate: a canonical segment derived from
// the supplied representation identity and text, one verified ancestry path naming the supplied source,
// and the supplied rank and disposition. The segment identity comes from the canonical segment
// constructor rather than a hand-derived value; a rejected candidate is expected to carry no released
// text and no accepted rank, exactly as the ranking stage records it.
func supersessionCandidate(t testing.TB, representationID RepresentationID, sourceID SourceID, text string, finalRank int, disposition CandidateDisposition) VerifiedLexicalCandidate {
	t.Helper()
	contentDigest := sha256.Sum256([]byte(text))
	selector := NewTextByteRangeSelector(0, uint64(len(text)))
	segmentID, err := NewSegmentID(representationID, selector, SHA256(contentDigest))
	if err != nil {
		t.Fatalf("NewSegmentID(): %v", err)
	}
	candidate := VerifiedLexicalCandidate{
		Segment: Segment{
			Schema:           SegmentSchema,
			ID:               segmentID,
			RepresentationID: representationID,
			Selector:         selector,
			ContentSHA256:    SHA256(contentDigest),
		},
		Text:      text,
		FinalRank: finalRank,
		Paths: []EvidencePath{{
			RepresentationIDs: []RepresentationID{representationID},
			ArtifactID:        ArtifactID(sha256.Sum256([]byte("artifact:" + text))),
			ObservationID:     ObservationID(sha256.Sum256([]byte("observation:" + text))),
			SourceID:          sourceID,
		}},
		Disposition: disposition,
	}
	if disposition == CandidateRejected {
		// The ranking stage releases no text for a rejected candidate, so the fixture keeps only the
		// canonical segment identity it was built from.
		candidate.Text = ""
	}
	return candidate
}

// supersessionExpectedRow writes the expected disposition membership directly from the caller's
// declaration pins and candidate identities, so the expectation does not pass through the
// implementation under test.
func supersessionExpectedRow(declaration SupersessionDeclaration, candidate VerifiedLexicalCandidate) SupersessionDisposition {
	return SupersessionDisposition{
		Selection:                   SupersessionSuperseded,
		SegmentID:                   candidate.Segment.ID,
		ContentSHA256:               candidate.Segment.ContentSHA256,
		DeclarationID:               declaration.ID,
		PredecessorItemID:           declaration.PredecessorItemID,
		PredecessorRepresentationID: declaration.PredecessorRepresentationID,
		SuccessorItemID:             declaration.SuccessorItemID,
		SuccessorRepresentationID:   declaration.SuccessorRepresentationID,
	}
}

func supersessionForeignSource(t testing.TB) SourceID {
	t.Helper()
	sourceID, err := NewSourceID("example.elsewhere", "notes")
	if err != nil {
		t.Fatalf("NewSourceID(): %v", err)
	}
	return sourceID
}

func renderSupersessionSelection(t testing.TB, selection SupersessionSelection) string {
	t.Helper()
	encoded, err := json.Marshal(selection)
	if err != nil {
		t.Fatalf("json.Marshal(selection): %v", err)
	}
	return string(encoded)
}

func requireSupersessionSelection(t testing.TB, got, want SupersessionSelection) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selection = %s, want %s", renderSupersessionSelection(t, got), renderSupersessionSelection(t, want))
	}
}

func cloneSupersessionSelection(selection SupersessionSelection) SupersessionSelection {
	cloned := SupersessionSelection{Consulted: selection.Consulted}
	if selection.ActivationID != nil {
		id := *selection.ActivationID
		cloned.ActivationID = &id
	}
	if selection.DeclarationID != nil {
		id := *selection.DeclarationID
		cloned.DeclarationID = &id
	}
	cloned.Dispositions = append([]SupersessionDisposition(nil), selection.Dispositions...)
	return cloned
}

func cloneVerifiedLexicalCandidates(candidates []VerifiedLexicalCandidate) []VerifiedLexicalCandidate {
	cloned := make([]VerifiedLexicalCandidate, len(candidates))
	for index, candidate := range candidates {
		cloned[index] = candidate
		cloned[index].Paths = make([]EvidencePath, len(candidate.Paths))
		for pathIndex, path := range candidate.Paths {
			cloned[index].Paths[pathIndex] = path
			cloned[index].Paths[pathIndex].RepresentationIDs = append([]RepresentationID(nil), path.RepresentationIDs...)
		}
		cloned[index].Sources = append([]SourceLifecycleEvidence(nil), candidate.Sources...)
		cloned[index].Reasons = append([]LifecycleReason(nil), candidate.Reasons...)
	}
	return cloned
}

func TestSupersessionSelectionUnconsultedDenial(t *testing.T) {
	sourceID := supersessionSelectionDeclaration(t).SourceID
	for _, candidates := range [][]VerifiedLexicalCandidate{nil, {}} {
		selection, err := BuildSupersessionSelection(sourceID, PolicyOutcomeDeny, candidates, nil, nil)
		if err != nil {
			t.Fatalf("BuildSupersessionSelection(deny): %v", err)
		}
		requireSupersessionSelection(t, selection, SupersessionSelection{Dispositions: []SupersessionDisposition{}})
		if selection.Dispositions == nil {
			t.Fatal("unconsulted denial must carry an explicit empty disposition array, not nil")
		}
		if rendered, want := renderSupersessionSelection(t, selection), `{"consulted":false,"activation_id":null,"declaration_id":null,"dispositions":[]}`; rendered != want {
			t.Fatalf("denial member = %s, want %s", rendered, want)
		}
	}
}

func TestSupersessionSelectionRejectsAdministrativeInputWithoutAllow(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	state := supersessionSelectionState(t, declaration)
	matching := supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "mooring guidance", 1, CandidateAccepted)
	for _, test := range []struct {
		name        string
		candidates  []VerifiedLexicalCandidate
		activation  *SupersessionActivationState
		declaration *SupersessionDeclaration
	}{
		{name: "candidates", candidates: []VerifiedLexicalCandidate{matching}},
		{name: "activation state", activation: &state},
		{name: "declaration", declaration: &declaration},
		{name: "every input", candidates: []VerifiedLexicalCandidate{matching}, activation: &state, declaration: &declaration},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildSupersessionSelection(declaration.SourceID, PolicyOutcomeDeny, test.candidates, test.activation, test.declaration); err == nil {
				t.Fatal("denial accepted input that contradicts unconsulted state")
			}
		})
	}
}

func TestSupersessionSelectionRejectsInvalidSourceAndOutcome(t *testing.T) {
	sourceID := supersessionSelectionDeclaration(t).SourceID
	for _, test := range []struct {
		name    string
		source  SourceID
		outcome PolicyDecisionOutcome
	}{
		{name: "zero source", outcome: PolicyOutcomeAllow},
		{name: "unknown outcome", source: sourceID, outcome: PolicyDecisionOutcome("maybe")},
		{name: "absent outcome", source: sourceID},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildSupersessionSelection(test.source, test.outcome, nil, nil, nil); err == nil {
				t.Fatal("selection accepted an invalid source or outcome")
			}
		})
	}
}

func TestSupersessionSelectionConsultedHistoryShapes(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	deactivated := SupersessionActivationState{
		SourceID:            declaration.SourceID,
		CurrentActivationID: mustParseActivationID(t, goldenSupersessionActivationID),
	}
	for _, test := range []struct {
		name       string
		activation *SupersessionActivationState
	}{
		{name: "no activation history"},
		{name: "deactivation", activation: &deactivated},
	} {
		t.Run(test.name, func(t *testing.T) {
			selection, err := BuildSupersessionSelection(declaration.SourceID, PolicyOutcomeAllow, nil, test.activation, nil)
			if err != nil {
				t.Fatalf("BuildSupersessionSelection(allow): %v", err)
			}
			want := SupersessionSelection{Consulted: true, Dispositions: []SupersessionDisposition{}}
			if test.activation != nil {
				activationID := test.activation.CurrentActivationID
				want.ActivationID = &activationID
			}
			requireSupersessionSelection(t, selection, want)
		})
	}
}

func TestSupersessionSelectionSuppressesExactPredecessorPin(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	state := supersessionSelectionState(t, declaration)
	matching := supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "mooring guidance", 1, CandidateAccepted)
	successor := supersessionCandidate(t, declaration.SuccessorRepresentationID, declaration.SourceID, "corrected mooring guidance", 2, CandidateAccepted)
	selection, err := BuildSupersessionSelection(declaration.SourceID, PolicyOutcomeAllow, []VerifiedLexicalCandidate{matching, successor}, &state, &declaration)
	if err != nil {
		t.Fatalf("BuildSupersessionSelection(allow): %v", err)
	}
	activationID := state.CurrentActivationID
	declarationID := declaration.ID
	requireSupersessionSelection(t, selection, SupersessionSelection{
		Consulted:     true,
		ActivationID:  &activationID,
		DeclarationID: &declarationID,
		Dispositions:  []SupersessionDisposition{supersessionExpectedRow(declaration, matching)},
	})
	if selection.Dispositions[0].Selection != SupersessionSuperseded {
		t.Fatalf("selection value = %q, want superseded", selection.Dispositions[0].Selection)
	}
}

func TestSupersessionSelectionOrdersMultiplePredecessorSegments(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	state := supersessionSelectionState(t, declaration)
	first := supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "first pinned passage", 1, CandidateAccepted)
	unrelated := supersessionCandidate(t, supersessionRepresentation("unrelated"), declaration.SourceID, "unrelated passage", 2, CandidateAccepted)
	second := supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "second pinned passage", 3, CandidateAccepted)
	third := supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "third pinned passage", 4, CandidateAccepted)
	selection, err := BuildSupersessionSelection(declaration.SourceID, PolicyOutcomeAllow, []VerifiedLexicalCandidate{first, unrelated, second, third}, &state, &declaration)
	if err != nil {
		t.Fatalf("BuildSupersessionSelection(allow): %v", err)
	}
	if want := []SupersessionDisposition{
		supersessionExpectedRow(declaration, first),
		supersessionExpectedRow(declaration, second),
		supersessionExpectedRow(declaration, third),
	}; !reflect.DeepEqual(selection.Dispositions, want) {
		t.Fatalf("dispositions = %s, want three rows in original final_rank order", renderSupersessionSelection(t, selection))
	}
}

func TestSupersessionSelectionIgnoresLifecycleRejectedCandidate(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	state := supersessionSelectionState(t, declaration)
	rejected := supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "withdrawn pinned passage", 0, CandidateRejected)
	rejected.Reasons = []LifecycleReason{ReasonSourceWithdrawn}
	current := supersessionCandidate(t, supersessionRepresentation("current"), declaration.SourceID, "current evidence", 1, CandidateAccepted)
	selection, err := BuildSupersessionSelection(declaration.SourceID, PolicyOutcomeAllow, []VerifiedLexicalCandidate{rejected, current}, &state, &declaration)
	if err != nil {
		t.Fatalf("BuildSupersessionSelection(allow): %v", err)
	}
	activationID := state.CurrentActivationID
	declarationID := declaration.ID
	requireSupersessionSelection(t, selection, SupersessionSelection{
		Consulted:     true,
		ActivationID:  &activationID,
		DeclarationID: &declarationID,
		Dispositions:  []SupersessionDisposition{},
	})
}

func TestSupersessionSelectionIgnoresLaterRevisionAndByteEqualOtherItem(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	state := supersessionSelectionState(t, declaration)
	const pinnedText = "mooring guidance"
	candidates := []VerifiedLexicalCandidate{
		supersessionCandidate(t, supersessionRepresentation("later-revision"), declaration.SourceID, pinnedText, 1, CandidateAccepted),
		supersessionCandidate(t, supersessionRepresentation("other-item"), declaration.SourceID, pinnedText, 2, CandidateAccepted),
		supersessionCandidate(t, declaration.SuccessorRepresentationID, declaration.SourceID, pinnedText, 3, CandidateAccepted),
		supersessionCandidate(t, supersessionRepresentation("unrelated"), declaration.SourceID, pinnedText, 4, CandidateAccepted),
	}
	selection, err := BuildSupersessionSelection(declaration.SourceID, PolicyOutcomeAllow, candidates, &state, &declaration)
	if err != nil {
		t.Fatalf("BuildSupersessionSelection(allow): %v", err)
	}
	if len(selection.Dispositions) != 0 {
		t.Fatalf("dispositions = %s, want none: only the exact predecessor representation matches", renderSupersessionSelection(t, selection))
	}
}

func TestSupersessionSelectionIgnoresAncestorRepresentationPin(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	state := supersessionSelectionState(t, declaration)
	own := supersessionRepresentation("derived-view")
	derived := supersessionCandidate(t, own, declaration.SourceID, "derived view passage", 1, CandidateAccepted)
	derived.Paths[0].RepresentationIDs = []RepresentationID{declaration.PredecessorRepresentationID, own}
	selection, err := BuildSupersessionSelection(declaration.SourceID, PolicyOutcomeAllow, []VerifiedLexicalCandidate{derived}, &state, &declaration)
	if err != nil {
		t.Fatalf("BuildSupersessionSelection(allow): %v", err)
	}
	if len(selection.Dispositions) != 0 {
		t.Fatalf("dispositions = %s, want none: an ancestor pin is not the candidate's own representation", renderSupersessionSelection(t, selection))
	}
}

func TestSupersessionSelectionScopesToAuthorizedSource(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	state := supersessionSelectionState(t, declaration)
	foreign := supersessionForeignSource(t)
	matching := supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "authorized passage", 1, CandidateAccepted)
	foreignOnly := supersessionCandidate(t, declaration.PredecessorRepresentationID, foreign, "foreign passage", 2, CandidateAccepted)
	shared := supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "shared ancestry passage", 3, CandidateAccepted)
	shared.Paths = append(shared.Paths, EvidencePath{
		RepresentationIDs: []RepresentationID{declaration.PredecessorRepresentationID},
		ArtifactID:        ArtifactID(sha256.Sum256([]byte("shared artifact"))),
		ObservationID:     ObservationID(sha256.Sum256([]byte("shared observation"))),
		SourceID:          foreign,
	})
	selection, err := BuildSupersessionSelection(declaration.SourceID, PolicyOutcomeAllow, []VerifiedLexicalCandidate{matching, foreignOnly, shared}, &state, &declaration)
	if err != nil {
		t.Fatalf("BuildSupersessionSelection(allow): %v", err)
	}
	if want := []SupersessionDisposition{
		supersessionExpectedRow(declaration, matching),
		supersessionExpectedRow(declaration, shared),
	}; !reflect.DeepEqual(selection.Dispositions, want) {
		t.Fatalf("dispositions = %s, want only candidates in the decision source's verified ancestry", renderSupersessionSelection(t, selection))
	}
}

func TestSupersessionSelectionRejectsContradictoryAdministrativeInputs(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	state := supersessionSelectionState(t, declaration)
	matching := supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "mooring guidance", 1, CandidateAccepted)
	otherDeclaration := declaration
	otherDeclaration.PredecessorItemID = "docs/other"
	otherDeclaration.ID = newSupersessionDeclarationID(t, otherDeclaration)
	foreign := supersessionForeignSource(t)
	noDeclaration := SupersessionActivationState{
		SourceID:            declaration.SourceID,
		CurrentActivationID: state.CurrentActivationID,
	}
	foreignState := state
	foreignState.SourceID = foreign
	foreignSource := declaration
	foreignSource.SourceID = foreign
	foreignSource.ID = newSupersessionDeclarationID(t, foreignSource)
	zeroActivation := state
	zeroActivation.CurrentActivationID = SupersessionActivationID{}
	invalidDeclaration := declaration
	invalidDeclaration.Basis = ""
	for _, test := range []struct {
		name        string
		source      SourceID
		candidates  []VerifiedLexicalCandidate
		activation  *SupersessionActivationState
		declaration *SupersessionDeclaration
	}{
		{name: "activation source", source: declaration.SourceID, activation: &foreignState, declaration: &declaration},
		{name: "declaration source", source: declaration.SourceID, activation: &state, declaration: &foreignSource},
		{name: "zero activation identity", source: declaration.SourceID, activation: &zeroActivation, declaration: &declaration},
		{name: "invalid declaration record", source: declaration.SourceID, activation: &state, declaration: &invalidDeclaration},
		{name: "declaration without activation state", source: declaration.SourceID, declaration: &declaration},
		{name: "state without the declaration record", source: declaration.SourceID, activation: &state},
		{name: "state selecting another declaration", source: declaration.SourceID, activation: &state, declaration: &otherDeclaration},
		{name: "state not selecting a declaration", source: declaration.SourceID, activation: &noDeclaration, declaration: &declaration},
		{name: "decision source differs from both records", source: foreign, activation: &state, declaration: &declaration},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildSupersessionSelection(test.source, PolicyOutcomeAllow, []VerifiedLexicalCandidate{matching}, test.activation, test.declaration); err == nil {
				t.Fatal("selection accepted contradictory administrative input")
			}
		})
	}
}

func TestSupersessionSelectionRejectsInvalidCandidateRecords(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	state := supersessionSelectionState(t, declaration)
	source := declaration.SourceID
	unrelated := func(text string, rank int) VerifiedLexicalCandidate {
		return supersessionCandidate(t, supersessionRepresentation("unrelated-"+text), source, text, rank, CandidateAccepted)
	}
	rejected := supersessionCandidate(t, supersessionRepresentation("rejected"), source, "rejected passage", 0, CandidateRejected)
	unknownDisposition := supersessionCandidate(t, supersessionRepresentation("unknown"), source, "text", 1, CandidateDisposition("deferred"))
	rejectedWithRank := rejected
	rejectedWithRank.FinalRank = 2
	for _, test := range []struct {
		name       string
		candidates []VerifiedLexicalCandidate
	}{
		{name: "duplicate final ranks", candidates: []VerifiedLexicalCandidate{unrelated("first", 1), unrelated("second", 1)}},
		{name: "descending final ranks", candidates: []VerifiedLexicalCandidate{unrelated("first", 2), unrelated("second", 1)}},
		{name: "zero accepted rank", candidates: []VerifiedLexicalCandidate{unrelated("first", 0)}},
		{name: "rejected candidate with a rank", candidates: []VerifiedLexicalCandidate{rejectedWithRank}},
		{name: "unknown disposition", candidates: []VerifiedLexicalCandidate{unknownDisposition}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildSupersessionSelection(source, PolicyOutcomeAllow, test.candidates, &state, &declaration); err == nil {
				t.Fatal("selection accepted an invalid candidate record")
			}
		})
	}
}

func TestSupersessionSelectionValidateRejectsContradictions(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	matching := supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "mooring guidance", 1, CandidateAccepted)
	row := supersessionExpectedRow(declaration, matching)
	activationID := mustParseActivationID(t, goldenSupersessionActivationID)
	declarationID := declaration.ID
	otherDeclaration := declaration
	otherDeclaration.Basis = "another recorded basis"
	otherDeclaration.ID = newSupersessionDeclarationID(t, otherDeclaration)
	empty := []SupersessionDisposition{}
	mutate := func(change func(*SupersessionDisposition)) SupersessionDisposition {
		mutated := row
		change(&mutated)
		return mutated
	}
	for _, test := range []struct {
		name      string
		selection SupersessionSelection
	}{
		{name: "unconsulted with activation", selection: SupersessionSelection{ActivationID: &activationID, Dispositions: empty}},
		{name: "unconsulted with declaration", selection: SupersessionSelection{DeclarationID: &declarationID, Dispositions: empty}},
		{name: "null disposition list", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID}},
		{name: "unconsulted with rows", selection: SupersessionSelection{Dispositions: []SupersessionDisposition{row}}},
		{name: "zero activation identity", selection: SupersessionSelection{Consulted: true, ActivationID: &SupersessionActivationID{}, Dispositions: empty}},
		{name: "zero declaration identity", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &SupersessionDeclarationID{}, Dispositions: empty}},
		{name: "declaration without activation", selection: SupersessionSelection{Consulted: true, DeclarationID: &declarationID, Dispositions: empty}},
		{name: "rows without a selected declaration", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, Dispositions: []SupersessionDisposition{row}}},
		{name: "duplicate rows", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID, Dispositions: []SupersessionDisposition{row, row}}},
		{name: "row with another declaration", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID, Dispositions: []SupersessionDisposition{mutate(func(row *SupersessionDisposition) { row.DeclarationID = otherDeclaration.ID })}}},
		{name: "unknown selection value", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID, Dispositions: []SupersessionDisposition{mutate(func(row *SupersessionDisposition) { row.Selection = SupersessionSelectionValue("withheld") })}}},
		{name: "zero segment identity", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID, Dispositions: []SupersessionDisposition{mutate(func(row *SupersessionDisposition) { row.SegmentID = SegmentID{} })}}},
		{name: "zero content digest", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID, Dispositions: []SupersessionDisposition{mutate(func(row *SupersessionDisposition) { row.ContentSHA256 = SHA256{} })}}},
		{name: "zero declaration identity in a row", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID, Dispositions: []SupersessionDisposition{mutate(func(row *SupersessionDisposition) { row.DeclarationID = SupersessionDeclarationID{} })}}},
		{name: "empty item label", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID, Dispositions: []SupersessionDisposition{mutate(func(row *SupersessionDisposition) { row.PredecessorItemID = "" })}}},
		{name: "zero predecessor revision", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID, Dispositions: []SupersessionDisposition{mutate(func(row *SupersessionDisposition) { row.PredecessorRepresentationID = RepresentationID{} })}}},
		{name: "equal revision pins", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID, Dispositions: []SupersessionDisposition{mutate(func(row *SupersessionDisposition) { row.SuccessorRepresentationID = row.PredecessorRepresentationID })}}},
		{name: "equal item pins", selection: SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID, Dispositions: []SupersessionDisposition{mutate(func(row *SupersessionDisposition) { row.SuccessorItemID = row.PredecessorItemID })}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.selection.Validate(); err == nil {
				t.Fatal("Validate accepted a contradictory member")
			}
		})
	}
	canonical := SupersessionSelection{Consulted: true, ActivationID: &activationID, DeclarationID: &declarationID, Dispositions: []SupersessionDisposition{row}}
	if err := canonical.Validate(); err != nil {
		t.Fatalf("Validate(canonical member): %v", err)
	}
}

func TestSupersessionSelectionValidateAgainstRejectsMismatches(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	state := supersessionSelectionState(t, declaration)
	source := declaration.SourceID
	first := supersessionCandidate(t, declaration.PredecessorRepresentationID, source, "first pinned passage", 1, CandidateAccepted)
	unrelated := supersessionCandidate(t, supersessionRepresentation("unrelated"), source, "unrelated passage", 2, CandidateAccepted)
	second := supersessionCandidate(t, declaration.PredecessorRepresentationID, source, "second pinned passage", 3, CandidateAccepted)
	rejected := supersessionCandidate(t, declaration.PredecessorRepresentationID, source, "rejected pinned passage", 0, CandidateRejected)
	candidates := []VerifiedLexicalCandidate{first, unrelated, second, rejected}
	built, err := BuildSupersessionSelection(source, PolicyOutcomeAllow, candidates, &state, &declaration)
	if err != nil {
		t.Fatalf("BuildSupersessionSelection(allow): %v", err)
	}
	if err := built.ValidateAgainst(source, PolicyOutcomeAllow, candidates, &state, &declaration); err != nil {
		t.Fatalf("ValidateAgainst(built member): %v", err)
	}
	otherDeclaration := declaration
	otherDeclaration.Basis = "another recorded basis"
	otherDeclaration.ID = newSupersessionDeclarationID(t, otherDeclaration)
	denied, err := BuildSupersessionSelection(source, PolicyOutcomeDeny, nil, nil, nil)
	if err != nil {
		t.Fatalf("BuildSupersessionSelection(deny): %v", err)
	}
	if err := denied.ValidateAgainst(source, PolicyOutcomeDeny, nil, nil, nil); err != nil {
		t.Fatalf("ValidateAgainst(unconsulted denial): %v", err)
	}
	unconsulted, err := BuildSupersessionSelection(source, PolicyOutcomeAllow, candidates, nil, nil)
	if err != nil {
		t.Fatalf("BuildSupersessionSelection(no history): %v", err)
	}
	if err := unconsulted.ValidateAgainst(source, PolicyOutcomeAllow, candidates, nil, nil); err != nil {
		t.Fatalf("ValidateAgainst(no activation history): %v", err)
	}
	foreign := supersessionForeignSource(t)
	foreignState := state
	foreignState.SourceID = foreign
	noDeclarationState := SupersessionActivationState{SourceID: source, CurrentActivationID: state.CurrentActivationID}
	otherActivationState := state
	otherActivationState.CurrentActivationID = mustParseActivationID(t, goldenSupersessionDeactivationID)
	for _, test := range []struct {
		name        string
		source      SourceID
		outcome     PolicyDecisionOutcome
		selection   SupersessionSelection
		activation  *SupersessionActivationState
		declaration *SupersessionDeclaration
	}{
		{name: "consulted flag contradicts the denial", selection: built, outcome: PolicyOutcomeDeny},
		{name: "unconsulted member for an allow decision", selection: SupersessionSelection{Dispositions: []SupersessionDisposition{}}, activation: &state, declaration: &declaration},
		{name: "missing row", selection: cloneWithDispositions(built, built.Dispositions[:1]), activation: &state, declaration: &declaration},
		{name: "row naming a rejected candidate", selection: cloneWithDispositions(built, append(append([]SupersessionDisposition{}, built.Dispositions...), supersessionExpectedRow(declaration, rejected))), activation: &state, declaration: &declaration},
		{name: "row naming a non-matching candidate", selection: cloneWithDispositions(built, append(append([]SupersessionDisposition{}, built.Dispositions...), supersessionExpectedRow(declaration, unrelated))), activation: &state, declaration: &declaration},
		{name: "out-of-order rows", selection: cloneWithDispositions(built, []SupersessionDisposition{built.Dispositions[1], built.Dispositions[0]}), activation: &state, declaration: &declaration},
		{name: "wrong successor pin", selection: cloneWithDispositions(built, []SupersessionDisposition{mutatedRow(built.Dispositions[0], func(row *SupersessionDisposition) {
			row.SuccessorRepresentationID = supersessionRepresentation("elsewhere")
		}), built.Dispositions[1]}), activation: &state, declaration: &declaration},
		{name: "wrong declaration identity", selection: cloneWithDispositions(built, []SupersessionDisposition{mutatedRow(built.Dispositions[0], func(row *SupersessionDisposition) { row.DeclarationID = otherDeclaration.ID }), built.Dispositions[1]}), activation: &state, declaration: &declaration},
		{name: "wrong content digest", selection: cloneWithDispositions(built, []SupersessionDisposition{mutatedRow(built.Dispositions[0], func(row *SupersessionDisposition) { row.ContentSHA256 = SHA256(sha256.Sum256([]byte("elsewhere"))) }), built.Dispositions[1]}), activation: &state, declaration: &declaration},
		{name: "another activation event", selection: built, activation: &otherActivationState, declaration: &declaration},
		{name: "another source", selection: built, source: foreign, activation: &state, declaration: &declaration},
		{name: "cross-source activation state", selection: built, activation: &foreignState, declaration: &declaration},
		{name: "state without the selected declaration", selection: unconsulted, activation: &noDeclarationState},
		{name: "no history but rows recorded", selection: built},
	} {
		t.Run(test.name, func(t *testing.T) {
			selection := test.selection
			source := test.source
			if source == (SourceID{}) {
				source = declaration.SourceID
			}
			outcome := test.outcome
			if outcome == "" {
				outcome = PolicyOutcomeAllow
			}
			if err := selection.ValidateAgainst(source, outcome, candidates, test.activation, test.declaration); err == nil {
				t.Fatal("ValidateAgainst accepted a mismatched member")
			}
		})
	}
}

func cloneWithDispositions(selection SupersessionSelection, dispositions []SupersessionDisposition) SupersessionSelection {
	cloned := selection
	cloned.Dispositions = dispositions
	return cloned
}

func mutatedRow(row SupersessionDisposition, change func(*SupersessionDisposition)) SupersessionDisposition {
	change(&row)
	return row
}

func TestSupersessionSelectionPreservesInputsAndCopiesEvidence(t *testing.T) {
	declaration := supersessionSelectionDeclaration(t)
	state := supersessionSelectionState(t, declaration)
	matching := supersessionCandidate(t, declaration.PredecessorRepresentationID, declaration.SourceID, "mooring guidance", 1, CandidateAccepted)
	later := supersessionCandidate(t, supersessionRepresentation("later-revision"), declaration.SourceID, "later revision text", 2, CandidateAccepted)
	candidates := []VerifiedLexicalCandidate{matching, later}
	beforeCandidates := cloneVerifiedLexicalCandidates(candidates)
	beforeDeclaration := declaration
	beforeState := state

	selection, err := BuildSupersessionSelection(declaration.SourceID, PolicyOutcomeAllow, candidates, &state, &declaration)
	if err != nil {
		t.Fatalf("BuildSupersessionSelection(allow): %v", err)
	}
	snapshot := cloneSupersessionSelection(selection)
	if !reflect.DeepEqual(candidates, beforeCandidates) {
		t.Fatal("selection mutated the supplied candidates")
	}
	if declaration != beforeDeclaration || state != beforeState {
		t.Fatal("selection mutated a supplied administrative record")
	}
	if candidates[0].Text != "mooring guidance" || candidates[1].Text != "later revision text" {
		t.Fatal("selection changed candidate text or filled in successor text")
	}
	if len(selection.Dispositions) != 1 || selection.Dispositions[0].SegmentID != matching.Segment.ID {
		t.Fatalf("dispositions = %s, want the pinned predecessor segment only", renderSupersessionSelection(t, selection))
	}
	if selection.ActivationID == &state.CurrentActivationID {
		t.Fatal("activation identity aliases the caller's activation state")
	}
	if selection.DeclarationID == state.ActiveDeclarationID {
		t.Fatal("declaration identity aliases the caller's activation state")
	}

	state.CurrentActivationID = mustParseActivationID(t, goldenSupersessionDeactivationID)
	*state.ActiveDeclarationID = SupersessionDeclarationID{}
	mutated := declaration
	mutated.Basis = "another recorded basis"
	declaration.ID = newSupersessionDeclarationID(t, mutated)
	declaration.PredecessorItemID = "docs/mutated"
	candidates[0].Segment.RepresentationID = supersessionRepresentation("mutated")
	candidates[0].Text = "mutated"
	candidates[0].Paths[0].SourceID = supersessionForeignSource(t)
	candidates[0].Disposition = CandidateRejected
	if !reflect.DeepEqual(selection, snapshot) {
		t.Fatalf("returned evidence changed with caller-owned inputs: %s", renderSupersessionSelection(t, selection))
	}
}

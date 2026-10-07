package mousa

import "fmt"

// SupersessionSelectionValue is the closed selection outcome one supersession disposition records.
type SupersessionSelectionValue string

// SupersessionSuperseded is the recorded selection outcome: the disposition's candidate is withheld
// because the consulted declaration pins its exact representation as replaced. Withholding is not an
// authorization rejection and not a byte-budget or duplicate omission: the candidate keeps its accepted
// disposition, its rank and its canonical size, and this value only explains why it is not selected.
const SupersessionSuperseded SupersessionSelectionValue = "superseded"

// SupersessionDisposition is one self-contained withholding record: which considered candidate the
// consulted declaration withheld, which declaration withheld it, and the exact item and representation
// pins that declaration named. The row repeats the declaration identity and both pins so a reader can
// check it without the declaration record, and it carries no text and no size, because the withheld
// candidate's canonical size stays on the candidate row it was read from.
type SupersessionDisposition struct {
	Selection                   SupersessionSelectionValue `json:"selection"`
	SegmentID                   SegmentID                  `json:"segment_id"`
	ContentSHA256               SHA256                     `json:"content_sha256"`
	DeclarationID               SupersessionDeclarationID  `json:"declaration_id"`
	PredecessorItemID           string                     `json:"predecessor_item_id"`
	PredecessorRepresentationID RepresentationID           `json:"predecessor_representation_id"`
	SuccessorItemID             string                     `json:"successor_item_id"`
	SuccessorRepresentationID   RepresentationID           `json:"successor_representation_id"`
}

// SupersessionSelection is the consultation and selection evidence for one retrieval decision: whether
// supersession was consulted, which verified activation event and declaration were consulted, and which
// considered candidates that declaration withheld.
//
// The member is evidence, not authority. It reads no store, so it cannot establish that activation
// history or a declaration is stored, that a named record belongs to the decision's source, or that its
// identity hashes and ancestry are intact: a nil ActivationID or DeclarationID records which verified
// records the caller supplied, and does not assert that history is absent unless the caller verified
// that separately. A consulted member with both identities nil records a source with no activation
// history or a deactivated declaration; Dispositions is always non-nil, so an empty selection is an
// explicit empty array rather than an absent field.
//
// The member withholds nothing by itself: recording it in a trail, blanking withheld text, releasing
// fewer bytes or freeing budget is later integration work. The candidates it names stay in the
// caller's verified list with their accepted disposition, rank, text and canonical size unchanged.
type SupersessionSelection struct {
	Consulted     bool                       `json:"consulted"`
	ActivationID  *SupersessionActivationID  `json:"activation_id"`
	DeclarationID *SupersessionDeclarationID `json:"declaration_id"`
	Dispositions  []SupersessionDisposition  `json:"dispositions"`
}

// Validate checks one disposition row on its own: the closed selection value, nonzero segment, digest,
// declaration and revision identities, bounded item labels and distinct predecessor/successor pins. It
// proves structure only. It does not recompute the declaration identity and cannot show that the named
// segment, declaration, items or revisions exist, belong to the decision's source, or were ever
// activated.
func (disposition SupersessionDisposition) Validate() error {
	if disposition.Selection != SupersessionSuperseded {
		return newValidationError("selection", ValidationCodeInvalidEnum, "must be superseded", nil)
	}
	if disposition.SegmentID == (SegmentID{}) {
		return newValidationError("segment_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if disposition.ContentSHA256 == (SHA256{}) {
		return newValidationError("content_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	if disposition.DeclarationID == (SupersessionDeclarationID{}) {
		return newValidationError("declaration_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"predecessor_item_id", disposition.PredecessorItemID},
		{"successor_item_id", disposition.SuccessorItemID},
	} {
		if err := validateSupersessionLabel(field.name, field.value, MaxSupersessionItemBytes); err != nil {
			return err
		}
	}
	if disposition.PredecessorItemID == disposition.SuccessorItemID {
		return newValidationError("successor_item_id", ValidationCodeInvalidValue, "must differ from predecessor_item_id", nil)
	}
	for _, field := range []struct {
		name string
		id   RepresentationID
	}{
		{"predecessor_representation_id", disposition.PredecessorRepresentationID},
		{"successor_representation_id", disposition.SuccessorRepresentationID},
	} {
		if field.id == (RepresentationID{}) {
			return newValidationError(field.name, ValidationCodeInvalidID, "must not be zero", nil)
		}
	}
	if disposition.PredecessorRepresentationID == disposition.SuccessorRepresentationID {
		return newValidationError("successor_representation_id", ValidationCodeInvalidValue, "must differ from predecessor_representation_id", nil)
	}
	return nil
}

// Validate checks the member's own structure: the disposition list is an explicit array rather than
// null, an unconsulted member carries no identity and no row, a
// consulted member carries nonzero identities when they are present, a selected declaration implies a
// consulted activation event, rows exist only with a selected declaration, rows are unique by segment
// and every row names the member's own declaration. It does not compare the member against the
// candidates the decision considered; ValidateAgainst does that.
//
// This proves internal consistency only, and never integrity, authorization or stored existence: a
// recomputed hash would not establish that a named record exists or that a source authorized it.
func (selection SupersessionSelection) Validate() error {
	if selection.Dispositions == nil {
		return newValidationError("dispositions", ValidationCodeInvalidValue, "must be an array, not null", nil)
	}
	if !selection.Consulted {
		if selection.ActivationID != nil {
			return newValidationError("activation_id", ValidationCodeInvalidValue, "must be null when the decision did not consult supersession", nil)
		}
		if selection.DeclarationID != nil {
			return newValidationError("declaration_id", ValidationCodeInvalidValue, "must be null when the decision did not consult supersession", nil)
		}
		if len(selection.Dispositions) != 0 {
			return newValidationError("dispositions", ValidationCodeInvalidValue, "must be empty when the decision did not consult supersession", nil)
		}
		return nil
	}
	if selection.ActivationID != nil && *selection.ActivationID == (SupersessionActivationID{}) {
		return newValidationError("activation_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if selection.DeclarationID != nil {
		if *selection.DeclarationID == (SupersessionDeclarationID{}) {
			return newValidationError("declaration_id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		if selection.ActivationID == nil {
			return newValidationError("activation_id", ValidationCodeInvalidValue, "must be set when declaration_id is set", nil)
		}
	}
	if selection.DeclarationID == nil && len(selection.Dispositions) != 0 {
		return newValidationError("dispositions", ValidationCodeInvalidValue, "must be empty without a selected declaration", nil)
	}
	seen := make(map[SegmentID]struct{}, len(selection.Dispositions))
	for index, disposition := range selection.Dispositions {
		if err := disposition.Validate(); err != nil {
			return prefixValidationError(err, fmt.Sprintf("dispositions[%d]", index))
		}
		if _, duplicate := seen[disposition.SegmentID]; duplicate {
			return newValidationError(fmt.Sprintf("dispositions[%d].segment_id", index), ValidationCodeInvalidID, "must be unique", nil)
		}
		seen[disposition.SegmentID] = struct{}{}
		if disposition.DeclarationID != *selection.DeclarationID {
			return newValidationError(fmt.Sprintf("dispositions[%d].declaration_id", index), ValidationCodeInvalidID, "must match declaration_id", nil)
		}
	}
	return nil
}

// BuildSupersessionSelection derives the consultation member for one retrieval decision from the
// decision's source and outcome, the already verified lexical candidates the decision considered, and
// the explicitly supplied verified activation state and declaration.
//
// A non-allow outcome is unconsulted: it carries no activation event, no declaration and no row, and
// supplied candidates or administrative records that contradict that shape are rejected rather than
// ignored. An allow outcome is consulted: a nil activation state means the caller verified that the
// source has no activation history, a state naming no declaration is a deactivation, and a selected
// declaration requires a valid declaration record whose identity the state names and whose source is
// the decision's source.
//
// An accepted candidate is withheld exactly when its own segment representation equals the consulted
// declaration's predecessor representation and its verified ancestry names the decision's source. Item
// labels, candidate text, segment identity, later revisions of the same item, other items with equal
// bytes, an ancestor representation pin and another source's candidates never match. Rejected
// candidates stay rejected and unsuppressed. Rows follow the original accepted final_rank order and
// name the candidate's segment identity, content digest and the declaration's exact pins.
//
// The function is pure and reads no store: it derives no identity, consults no successor state, packs
// nothing, blanks no text and mutates no supplied record, candidate, rank, disposition or size.
// Returned identities and rows are copies rather than aliases of caller-owned values, and the successor
// revision's currency, matching or availability is not an input.
func BuildSupersessionSelection(sourceID SourceID, outcome PolicyDecisionOutcome, candidates []VerifiedLexicalCandidate, activation *SupersessionActivationState, declaration *SupersessionDeclaration) (SupersessionSelection, error) {
	return expectedSupersessionSelection(sourceID, outcome, candidates, activation, declaration)
}

// ValidateAgainst re-derives the member the supplied inputs require and checks that this member is
// exactly that evidence: the same consultation, the same activation and declaration identities, and one
// row per withheld candidate, each naming an accepted considered candidate and the consulted
// declaration's exact pins, in original final_rank order.
//
// It rejects contradictory consultation, mismatched or cross-source administrative records, invalid
// records, duplicate or out-of-order rows, wrong pins, a missing row and rows naming a rejected or
// otherwise non-matching candidate. Like Validate it proves consistency with the supplied verified
// inputs only: stored existence, pinned ancestry, authorization, successor currency and history reads
// remain transaction-bound store responsibilities.
func (selection SupersessionSelection) ValidateAgainst(sourceID SourceID, outcome PolicyDecisionOutcome, candidates []VerifiedLexicalCandidate, activation *SupersessionActivationState, declaration *SupersessionDeclaration) error {
	if err := selection.Validate(); err != nil {
		return err
	}
	expected, err := expectedSupersessionSelection(sourceID, outcome, candidates, activation, declaration)
	if err != nil {
		return err
	}
	if selection.Consulted != expected.Consulted {
		return newValidationError("consulted", ValidationCodeInvalidValue, "must match the decision outcome and the supplied administrative state", nil)
	}
	if !supersessionActivationIDsEqual(selection.ActivationID, expected.ActivationID) {
		return newValidationError("activation_id", ValidationCodeInvalidID, "must be the consulted activation event", nil)
	}
	if !supersessionDeclarationIDsEqual(selection.DeclarationID, expected.DeclarationID) {
		return newValidationError("declaration_id", ValidationCodeInvalidID, "must be the consulted declaration", nil)
	}
	if len(selection.Dispositions) != len(expected.Dispositions) {
		return newValidationError("dispositions", ValidationCodeInvalidRange, fmt.Sprintf("must record %d withheld candidate(s)", len(expected.Dispositions)), nil)
	}
	required := make(map[SegmentID]SupersessionDisposition, len(expected.Dispositions))
	for _, disposition := range expected.Dispositions {
		required[disposition.SegmentID] = disposition
	}
	ranks := make(map[SegmentID]int, len(candidates))
	for _, candidate := range candidates {
		if candidate.Disposition == CandidateAccepted {
			ranks[candidate.Segment.ID] = candidate.FinalRank
		}
	}
	previousRank := 0
	for index, disposition := range selection.Dispositions {
		field := fmt.Sprintf("dispositions[%d]", index)
		requiredRow, ok := required[disposition.SegmentID]
		if !ok {
			return newValidationError(field+".segment_id", ValidationCodeInvalidID, "must name an accepted candidate withheld by the consulted declaration", nil)
		}
		if differing := supersessionDispositionMismatch(disposition, requiredRow); differing != "" {
			return newValidationError(field+"."+differing, ValidationCodeInvalidValue, "does not match the consulted declaration", nil)
		}
		if ranks[disposition.SegmentID] <= previousRank {
			return newValidationError(field, ValidationCodeInvalidValue, "must be ordered by the withheld candidate's original final_rank", nil)
		}
		previousRank = ranks[disposition.SegmentID]
	}
	return nil
}

// expectedSupersessionSelection applies every shape rule and the exact-pin match once, so the builder
// and the re-check share one definition of the evidence the supplied inputs require.
func expectedSupersessionSelection(sourceID SourceID, outcome PolicyDecisionOutcome, candidates []VerifiedLexicalCandidate, activation *SupersessionActivationState, declaration *SupersessionDeclaration) (SupersessionSelection, error) {
	if sourceID == (SourceID{}) {
		return SupersessionSelection{}, newValidationError("source_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	switch outcome {
	case PolicyOutcomeDeny:
		if activation != nil {
			return SupersessionSelection{}, newValidationError("activation", ValidationCodeInvalidValue, "must not be supplied for a decision that did not allow the source", nil)
		}
		if declaration != nil {
			return SupersessionSelection{}, newValidationError("declaration", ValidationCodeInvalidValue, "must not be supplied for a decision that did not allow the source", nil)
		}
		if len(candidates) != 0 {
			return SupersessionSelection{}, newValidationError("candidates", ValidationCodeInvalidRange, "must be empty for a decision that did not allow the source", nil)
		}
		// An unconsulted denial records no identity and no row, and reads no history: it asserts
		// nothing about whether activation history exists.
		return SupersessionSelection{Dispositions: []SupersessionDisposition{}}, nil
	case PolicyOutcomeAllow:
	default:
		return SupersessionSelection{}, newValidationError("outcome", ValidationCodeInvalidEnum, "must be allow or deny", nil)
	}
	if err := validateSupersessionCandidateRanks(candidates); err != nil {
		return SupersessionSelection{}, err
	}
	selection := SupersessionSelection{Consulted: true, Dispositions: []SupersessionDisposition{}}
	if activation != nil {
		if activation.SourceID != sourceID {
			return SupersessionSelection{}, newValidationError("activation.source_id", ValidationCodeInvalidID, "must match the decision source", nil)
		}
		if activation.CurrentActivationID == (SupersessionActivationID{}) {
			return SupersessionSelection{}, newValidationError("activation.current_activation_id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		current := activation.CurrentActivationID
		selection.ActivationID = &current
	}
	if declaration == nil {
		if activation != nil && activation.ActiveDeclarationID != nil {
			return SupersessionSelection{}, newValidationError("declaration", ValidationCodeInvalidValue, "must be supplied when the activation state selects one", nil)
		}
		return selection, nil
	}
	if err := declaration.Validate(); err != nil {
		return SupersessionSelection{}, prefixValidationError(err, "declaration")
	}
	if activation == nil {
		return SupersessionSelection{}, newValidationError("activation", ValidationCodeInvalidValue, "must be supplied with a selected declaration", nil)
	}
	if declaration.SourceID != sourceID {
		return SupersessionSelection{}, newValidationError("declaration.source_id", ValidationCodeInvalidID, "must match the decision source", nil)
	}
	if activation.ActiveDeclarationID == nil {
		return SupersessionSelection{}, newValidationError("activation.active_declaration_id", ValidationCodeInvalidID, "must name the supplied declaration", nil)
	}
	if *activation.ActiveDeclarationID != declaration.ID {
		return SupersessionSelection{}, newValidationError("activation.active_declaration_id", ValidationCodeInvalidID, "must match declaration.id", nil)
	}
	selected := declaration.ID
	selection.DeclarationID = &selected
	for _, candidate := range candidates {
		if candidate.Disposition != CandidateAccepted {
			continue
		}
		if candidate.Segment.RepresentationID != declaration.PredecessorRepresentationID {
			continue
		}
		if !candidateNamesSource(candidate, sourceID) {
			continue
		}
		selection.Dispositions = append(selection.Dispositions, supersessionDispositionFor(candidate, declaration))
	}
	if err := selection.Validate(); err != nil {
		return SupersessionSelection{}, err
	}
	return selection, nil
}

// validateSupersessionCandidateRanks enforces the ranking contract the selection stage relies on: the
// verified list carries accepted candidates in ascending unique final ranks and rejected candidates
// without one, so rows can be ordered by the withheld candidate's original rank and a duplicate or
// regressed rank is an invalid input rather than a reordering decision this stage would invent.
func validateSupersessionCandidateRanks(candidates []VerifiedLexicalCandidate) error {
	previous := 0
	for index, candidate := range candidates {
		field := fmt.Sprintf("candidates[%d]", index)
		switch candidate.Disposition {
		case CandidateAccepted:
			if candidate.FinalRank <= previous {
				return newValidationError(field+".final_rank", ValidationCodeInvalidRange, "must be a unique positive rank in ascending order", nil)
			}
			previous = candidate.FinalRank
		case CandidateRejected:
			if candidate.FinalRank != 0 {
				return newValidationError(field+".final_rank", ValidationCodeInvalidValue, "must be zero for a rejected candidate", nil)
			}
		default:
			return newValidationError(field+".disposition", ValidationCodeInvalidEnum, "must be accepted or rejected", nil)
		}
	}
	return nil
}

// candidateNamesSource reports whether one verified ancestry path names the decision source. Source
// scope is verified by the retrieval stage that produced the candidates, exactly as its source-scoped
// search requires some derivation path to reach an artifact of that source; this stage re-checks that
// supplied fact instead of inferring a source from a representation identity it cannot recompute, so a
// candidate whose verified ancestry names only other sources is never withheld and never contributes
// another source's identities to the evidence.
func candidateNamesSource(candidate VerifiedLexicalCandidate, sourceID SourceID) bool {
	for _, path := range candidate.Paths {
		if path.SourceID == sourceID {
			return true
		}
	}
	return false
}

// supersessionDispositionFor copies the withheld candidate's identities and the declaration's pins into
// one row. Every value is copied, so the row keeps its evidence when the caller later changes the
// candidate, the declaration or their pointers.
func supersessionDispositionFor(candidate VerifiedLexicalCandidate, declaration *SupersessionDeclaration) SupersessionDisposition {
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

func supersessionActivationIDsEqual(left, right *SupersessionActivationID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func supersessionDeclarationIDsEqual(left, right *SupersessionDeclarationID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// supersessionDispositionMismatch names the first member of one recorded row that disagrees with the row
// the supplied inputs require, so a re-check reports the wrong identity or pin rather than only that two
// rows differ.
func supersessionDispositionMismatch(recorded, required SupersessionDisposition) string {
	switch {
	case recorded.Selection != required.Selection:
		return "selection"
	case recorded.SegmentID != required.SegmentID:
		return "segment_id"
	case recorded.ContentSHA256 != required.ContentSHA256:
		return "content_sha256"
	case recorded.DeclarationID != required.DeclarationID:
		return "declaration_id"
	case recorded.PredecessorItemID != required.PredecessorItemID:
		return "predecessor_item_id"
	case recorded.PredecessorRepresentationID != required.PredecessorRepresentationID:
		return "predecessor_representation_id"
	case recorded.SuccessorItemID != required.SuccessorItemID:
		return "successor_item_id"
	case recorded.SuccessorRepresentationID != required.SuccessorRepresentationID:
		return "successor_representation_id"
	}
	return ""
}

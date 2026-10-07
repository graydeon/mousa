package mousa

import "crypto/sha256"

// packAcceptedCandidates is the original released-text byte packing policy: it walks candidates in
// verified order and selects every accepted candidate whose released text fits the remaining byte
// budget, skipping a candidate that does not fit and continuing. suppressed marks candidates removed
// from the surviving set before packing, which the opt-in Trace stage uses for withheld candidates; a
// nil mask suppresses none. A suppressed candidate is never selected, never contributes bytes and
// keeps its recorded row and canonical size. The packet identity binds the released selection, so a
// suppressed candidate changes nothing in it: it is simply never selected.
func packAcceptedCandidates(candidates []TrailCandidate, budgetBytes uint64, suppressed []bool) (PacketPlan, error) {
	if budgetBytes == 0 {
		return PacketPlan{}, retrievalValidationError("budget_bytes", ValidationCodeInvalidRange, "must be positive")
	}
	plan := PacketPlan{Selected: make([]bool, len(candidates))}
	used := uint64(0)
	for index, candidate := range candidates {
		if suppressed != nil && suppressed[index] {
			continue
		}
		if candidate.Disposition != CandidateAccepted {
			continue
		}
		if candidate.TextBytes > budgetBytes-used {
			continue
		}
		plan.Selected[index] = true
		used += candidate.TextBytes
	}
	plan.UsedBytes = used
	id, err := NewContextPacketID(candidates, budgetBytes, plan.Selected)
	if err != nil {
		return PacketPlan{}, err
	}
	plan.PacketID = id
	return plan, nil
}

// packExact uses digests only to narrow comparisons; only selected byte-equal text suppresses a candidate.
// suppressed marks candidates removed from the surviving set before packing: a suppressed candidate is
// verified against its own text like any other candidate, then excluded, so it is never selected and
// never enters the retained set, and a suppressed copy cannot be the retained duplicate of a survivor.
func packExact(trail []TrailCandidate, candidates []VerifiedLexicalCandidate, budget uint64, suppressed []bool) error {
	retained := make(map[SHA256][]int)
	var used uint64
	for index := range trail {
		candidate := &trail[index]
		candidate.Selected = false
		if candidate.Disposition != CandidateAccepted {
			continue
		}
		text := candidates[index].Text
		if SHA256(sha256.Sum256([]byte(text))) != candidate.ContentSHA256 {
			return retrievalValidationError("candidates", ValidationCodeInvalidDigest, "text disagrees with segment digest")
		}
		if suppressed != nil && suppressed[index] {
			continue
		}
		for _, previous := range retained[candidate.ContentSHA256] {
			if text == candidates[previous].Text {
				candidate.Omission = "duplicate"
				candidate.DuplicateOf = trail[previous].SegmentID.String()
				break
			}
		}
		if candidate.DuplicateOf != "" {
			continue
		}
		if candidate.TextBytes > budget-used {
			candidate.Omission = "budget"
			continue
		}
		candidate.Selected = true
		used += candidate.TextBytes
		retained[candidate.ContentSHA256] = append(retained[candidate.ContentSHA256], index)
	}
	return nil
}

// suppressSupersededCandidates maps a v4 consultation member's rows onto the trail's candidate list and
// returns the suppressed-candidate mask. Each row must name exactly one accepted candidate of this trail
// with a positive canonical size and no lifecycle reasons, and the rows must be unique and ordered by
// that candidate's original final_rank. Withholding is explicit membership: an accepted, unselected
// candidate with no row is never treated as suppressed, and a row is never inferred from the shape of
// the selection.
//
// The mask is derived before packing from the member's rows; the packed-selection checks (a suppressed
// candidate stays unselected and carries no packing omission) belong to validatePackedCandidates, which
// runs after packing. This proves membership, ordering and the recorded sizes only: it reads no store, so
// it cannot show that a named representation is the candidate's ancestry or that the named declaration
// exists.
func suppressSupersededCandidates(candidates []TrailCandidate, selection *SupersessionSelection) ([]bool, error) {
	mask := make([]bool, len(candidates))
	if selection == nil {
		return mask, nil
	}
	bySegment := make(map[SegmentID]int, len(candidates))
	for index, candidate := range candidates {
		if _, seen := bySegment[candidate.SegmentID]; seen {
			// A repeated segment makes a row's membership ambiguous, so mark it unresolvable.
			bySegment[candidate.SegmentID] = -1
			continue
		}
		bySegment[candidate.SegmentID] = index
	}
	previousRank := 0
	for index, disposition := range selection.Dispositions {
		field := "supersession.dispositions[" + itoa(index) + "]"
		position, known := bySegment[disposition.SegmentID]
		if !known || position < 0 {
			return nil, newValidationError(field+".segment_id", ValidationCodeInvalidID, "must name exactly one candidate of this trail", nil)
		}
		candidate := candidates[position]
		if candidate.Disposition != CandidateAccepted {
			return nil, newValidationError(field+".segment_id", ValidationCodeInvalidValue, "must name an accepted candidate", nil)
		}
		if disposition.ContentSHA256 != candidate.ContentSHA256 {
			return nil, newValidationError(field+".content_sha256", ValidationCodeInvalidDigest, "must match the withheld candidate", nil)
		}
		if candidate.TextBytes == 0 {
			return nil, newValidationError(field+".segment_id", ValidationCodeInvalidRange, "must name a candidate with a positive canonical size", nil)
		}
		if len(candidate.Reasons) != 0 {
			return nil, newValidationError(field+".segment_id", ValidationCodeInvalidValue, "must name a candidate without lifecycle reasons", nil)
		}
		if candidate.FinalRank <= previousRank {
			return nil, newValidationError(field, ValidationCodeInvalidValue, "must be ordered by the withheld candidate's original final_rank", nil)
		}
		previousRank = candidate.FinalRank
		mask[position] = true
	}
	return mask, nil
}

func (trail SourceTrail) validatePacking() error {
	invalid := func(message string) error {
		return retrievalValidationError("packing", ValidationCodeInvalidValue, message)
	}
	if trail.Schema == SourceTrailSchemaV3 {
		// v3 records its packing policy, so its candidates are checked under that policy; the
		// association stage continues in validateAssociated, which also rechecks packet identity.
		return trail.validatePackedCandidates()
	}
	if trail.Schema == SourceTrailSchemaV4 {
		// v4 requires a packing policy, carries no association stage, and checks its recorded
		// selection over the candidates that survive the recorded suppression.
		if trail.PackingPolicy != PackingOriginal && trail.PackingPolicy != PackingExactV1 {
			return invalid("v4 requires a packing policy of original or exact-v1")
		}
		if len(trail.Associated) != 0 || len(trail.AssociationOmissions) != 0 {
			return invalid("v4 records no associated passages or association omissions")
		}
		return trail.validatePackedCandidates()
	}
	if trail.Schema == SourceTrailSchema {
		if trail.PackingPolicy != "" {
			return invalid("v1 cannot specify a packing policy")
		}
		for _, candidate := range trail.Candidates {
			if candidate.Omission != "" || candidate.DuplicateOf != "" {
				return invalid("v1 cannot specify packing omissions")
			}
		}
		return nil
	}
	if trail.PackingPolicy != PackingExactV1 {
		return invalid("v2 requires exact-v1 packing")
	}
	return trail.validatePackedCandidates()
}

// validatePackedCandidates checks the recorded primary selection under the trail's own packing
// policy. Exact-v1 names the byte-equal duplicate it omitted; the original policy keeps every
// accepted passage whose bytes fit and records no packing omission, so its selection must still be
// the greedy first-fit result of that budget. A v4 trail first maps its recorded suppression rows
// onto the candidate list, so both policies are reproduced over the surviving candidates only: a
// withheld candidate stays accepted and unselected with no packing omission, and an ordinary budget
// skip of a surviving candidate keeps its existing meaning.
func (trail SourceTrail) validatePackedCandidates() error {
	invalid := func(message string) error {
		return retrievalValidationError("packing", ValidationCodeInvalidValue, message)
	}
	if trail.Outcome == string(PolicyOutcomeDeny) && len(trail.Candidates) != 0 {
		return invalid("deny cannot carry candidates")
	}
	var suppressed []bool
	if trail.Schema == SourceTrailSchemaV4 {
		mask, err := suppressSupersededCandidates(trail.Candidates, trail.Supersession)
		if err != nil {
			return err
		}
		suppressed = mask
	}
	if trail.PackingPolicy == PackingOriginal {
		if trail.BudgetBytes == 0 {
			return invalid("budget must be positive")
		}
		plan, err := packAcceptedCandidates(trail.Candidates, trail.BudgetBytes, suppressed)
		if err != nil {
			return err
		}
		for index, candidate := range trail.Candidates {
			if candidate.Omission != "" || candidate.DuplicateOf != "" {
				return invalid("original packing records no packing omission")
			}
			if candidate.Selected != plan.Selected[index] {
				return invalid("original packing disagrees with the recorded budget")
			}
		}
		return nil
	}
	if trail.PackingPolicy != PackingExactV1 {
		return invalid("packing policy must be original or exact-v1")
	}
	seen := make(map[string]TrailCandidate, len(trail.Candidates))
	var used uint64
	rank := 0
	for index, candidate := range trail.Candidates {
		id := candidate.SegmentID.String()
		if _, exists := seen[id]; exists {
			return invalid("segment occurs more than once")
		}
		if candidate.Disposition == CandidateRejected {
			if candidate.Omission != "" || candidate.DuplicateOf != "" || len(candidate.Reasons) == 0 {
				return invalid("rejected candidate must carry lifecycle reasons, not packing omissions")
			}
		} else {
			rank++
			if candidate.FinalRank != rank || len(candidate.Reasons) != 0 {
				return invalid("accepted candidates require stable consecutive ranks and no lifecycle reasons")
			}
			if suppressed != nil && suppressed[index] {
				if candidate.Selected || candidate.Omission != "" || candidate.DuplicateOf != "" {
					return invalid("a withheld candidate stays accepted and unselected with no packing omission")
				}
			} else {
				switch {
				case candidate.Selected:
					if candidate.Omission != "" || candidate.DuplicateOf != "" || candidate.TextBytes > trail.BudgetBytes-used {
						return invalid("selected candidate contradicts omission or budget")
					}
					used += candidate.TextBytes
				case candidate.Omission == "duplicate":
					retained, exists := seen[candidate.DuplicateOf]
					if !exists || !retained.Selected || retained.ContentSHA256 != candidate.ContentSHA256 || retained.TextBytes != candidate.TextBytes {
						return invalid("duplicate must reference a prior selected candidate with equal digest and size")
					}
				case candidate.Omission == "budget":
					if candidate.DuplicateOf != "" || candidate.TextBytes <= trail.BudgetBytes-used {
						return invalid("budget omission must not fit or carry a duplicate relationship")
					}
				default:
					return invalid("accepted unselected candidate requires a packing omission")
				}
			}
		}
		seen[id] = candidate
	}
	return nil
}

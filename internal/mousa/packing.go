package mousa

import "crypto/sha256"

// packAcceptedCandidates is the original released-text byte packing policy: it walks candidates in
// verified order and selects every accepted candidate whose released text fits the remaining byte
// budget, skipping a candidate that does not fit and continuing.
func packAcceptedCandidates(candidates []TrailCandidate, budgetBytes uint64) (PacketPlan, error) {
	if budgetBytes == 0 {
		return PacketPlan{}, retrievalValidationError("budget_bytes", ValidationCodeInvalidRange, "must be positive")
	}
	plan := PacketPlan{Selected: make([]bool, len(candidates))}
	used := uint64(0)
	for index, candidate := range candidates {
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
func packExact(trail []TrailCandidate, candidates []VerifiedLexicalCandidate, budget uint64) error {
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

// validateCandidateInvariants checks the candidate properties that hold under either packing policy:
// a segment is considered once, an accepted candidate carries a consecutive rank from one and no
// lifecycle reason, and a rejected candidate carries lifecycle reasons instead of a packing omission.
func (trail SourceTrail) validateCandidateInvariants() error {
	invalid := func(message string) error {
		return retrievalValidationError("packing", ValidationCodeInvalidValue, message)
	}
	seen := make(map[SegmentID]struct{}, len(trail.Candidates))
	rank := 0
	for _, candidate := range trail.Candidates {
		if _, exists := seen[candidate.SegmentID]; exists {
			return invalid("segment occurs more than once")
		}
		seen[candidate.SegmentID] = struct{}{}
		switch candidate.Disposition {
		case CandidateRejected:
			if candidate.Omission != "" || candidate.DuplicateOf != "" || len(candidate.Reasons) == 0 {
				return invalid("rejected candidate must carry lifecycle reasons, not packing omissions")
			}
		case CandidateAccepted:
			rank++
			if candidate.FinalRank != rank || len(candidate.Reasons) != 0 {
				return invalid("accepted candidates require stable consecutive ranks and no lifecycle reasons")
			}
		}
	}
	return nil
}

func (trail SourceTrail) validatePacking() error {
	invalid := func(message string) error {
		return retrievalValidationError("packing", ValidationCodeInvalidValue, message)
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
// the greedy first-fit result of that budget.
func (trail SourceTrail) validatePackedCandidates() error {
	invalid := func(message string) error {
		return retrievalValidationError("packing", ValidationCodeInvalidValue, message)
	}
	if trail.Outcome == string(PolicyOutcomeDeny) && len(trail.Candidates) != 0 {
		return invalid("deny cannot carry candidates")
	}
	if trail.PackingPolicy == PackingOriginal {
		if trail.BudgetBytes == 0 {
			return invalid("budget must be positive")
		}
		plan, err := packAcceptedCandidates(trail.Candidates, trail.BudgetBytes)
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
	for _, candidate := range trail.Candidates {
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
		seen[id] = candidate
	}
	return nil
}

package mousa

// TokenSelectionView releases accepted candidate facts, not a canonical selection.
// CandidateIDs aligns both arrays; lexical ranks refer only to this accepted order.
// Every slice and proposed-count pointer is owned by the view.
type TokenSelectionView struct {
	Mode           string             `json:"mode"`
	Backend        string             `json:"backend"`
	Rendering      string             `json:"rendering"`
	SourceID       SourceID           `json:"source_id"`
	Limit          uint64             `json:"limit"`
	Budget         TokenBudget        `json:"budget"`
	MeasuredTokens uint64             `json:"measured_tokens"`
	RenderedBytes  uint64             `json:"rendered_bytes"`
	RenderedSHA256 SHA256             `json:"rendered_sha256"`
	CandidateIDs   []SegmentID        `json:"candidate_ids"`
	ProposedTokens []*uint64          `json:"proposed_tokens"`
	Lexical        []TokenLexicalFact `json:"lexical,omitempty"`
}

// ReleasedTokenSelection projects a validated producing trail without changing it.
// Budget/duplicate omissions remain accepted; lifecycle rejections have no slot.
func ReleasedTokenSelection(trail SourceTrail) *TokenSelectionView {
	t := trail.Token
	if t == nil {
		return nil
	}
	accepted := 0
	for _, row := range trail.Candidates {
		if row.Disposition == CandidateAccepted {
			accepted++
		}
	}
	v := &TokenSelectionView{
		Mode: t.Mode, Backend: t.Backend, Rendering: t.Rendering, SourceID: t.SourceID,
		Limit: t.Limit, Budget: t.Budget, MeasuredTokens: t.MeasuredTokens,
		RenderedBytes: t.RenderedBytes, RenderedSHA256: t.RenderedSHA256,
		CandidateIDs: make([]SegmentID, 0, accepted), ProposedTokens: make([]*uint64, 0, accepted),
	}
	if len(t.Lexical) > 0 {
		v.Lexical = make([]TokenLexicalFact, 0, accepted)
	}
	for i, row := range trail.Candidates {
		if row.Disposition != CandidateAccepted {
			continue
		}
		v.CandidateIDs = append(v.CandidateIDs, row.SegmentID)
		var count *uint64
		if t.ProposedTokens[i] != nil {
			n := *t.ProposedTokens[i]
			count = &n
		}
		v.ProposedTokens = append(v.ProposedTokens, count)
		if len(t.Lexical) > 0 {
			v.Lexical = append(v.Lexical, TokenLexicalFact{Rank: len(v.CandidateIDs), BM25: t.Lexical[i].BM25})
		}
	}
	return v
}

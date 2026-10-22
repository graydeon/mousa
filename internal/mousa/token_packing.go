package mousa

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/graydeon/mousa/internal/tokenizer"
)

const SourceTrailSchemaV7 = "mousa.source_trail.v7"
const ContextRenderingV1 = "mousa.context_text.v1"
const TokenBackend = "tiktoken/0.12.0/o200k_base/ordinary"

// TokenSelection records measured producer evidence, not proof of tokenizer execution.
// The source text and rendered context are deliberately not archived.
type TokenSelection struct {
	Mode           string             `json:"mode"`
	Backend        string             `json:"backend"`
	Rendering      string             `json:"rendering"`
	SourceID       SourceID           `json:"source_id"`
	Limit          uint64             `json:"limit"`
	Budget         TokenBudget        `json:"budget"`
	MeasuredTokens uint64             `json:"measured_tokens"`
	RenderedBytes  uint64             `json:"rendered_bytes"`
	RenderedSHA256 SHA256             `json:"rendered_sha256"`
	ProposedTokens []*uint64          `json:"proposed_tokens"`
	Lexical        []TokenLexicalFact `json:"lexical,omitempty"`
}

type TokenLexicalFact struct {
	Rank int     `json:"rank"`
	BM25 float64 `json:"bm25"`
}

// ContextPassage renders one provenance header and the unchanged whole passage.
// Multiple passages are joined by exactly two LF bytes; the empty selection is empty.
func ContextPassage(source SourceID, segment Segment, text string) string {
	return fmt.Sprintf("[source=%s segment=%s sha256=%s]\n%s", source, segment.ID, segment.ContentSHA256, text)
}

func contextHeaderBytes() uint64 { return uint64(len("[source= segment= sha256=]\n") + 3*64) }

// PackTokenContext replaces only packing, preserving the actual producing ranking facts.
// It finishes and reaps the counter before returning a record eligible for persistence.
func PackTokenContext(base SourceTrail, source SourceID, candidates []VerifiedLexicalCandidate, budget TokenBudget, counter *tokenizer.Counter) (SourceTrail, string, error) {
	if err := base.Validate(); err != nil {
		return SourceTrail{}, "", err
	}
	limit, err := budget.Available()
	if err != nil {
		return SourceTrail{}, "", err
	}
	if source == (SourceID{}) || counter == nil || base.BudgetBytes > MaxVectorTextBytes || len(candidates) > MaxVectorPassages || len(candidates) != len(base.Candidates) || base.Supersession != nil || len(base.Associated) > 0 {
		return SourceTrail{}, "", errors.New("invalid token packing inputs or unsupported stages")
	}
	if base.Outcome == string(PolicyOutcomeDeny) && len(candidates) != 0 {
		return SourceTrail{}, "", errors.New("denied token selection cannot carry candidates")
	}
	mode := "lexical"
	switch base.Schema {
	case SourceTrailSchema, SourceTrailSchemaV2:
	case SourceTrailSchemaV5:
		mode = "vector"
	case SourceTrailSchemaV6:
		mode = "hybrid"
	default:
		return SourceTrail{}, "", errors.New("unsupported token retrieval record")
	}
	t := base
	t.Schema = SourceTrailSchemaV7
	t.Candidates = trailCandidatesFromVerified(candidates)
	t.PackingPolicy = base.PackingPolicy
	if t.PackingPolicy == "" {
		t.PackingPolicy = PackingOriginal
	}
	t.UsedBytes = 0
	t.Token = &TokenSelection{Mode: mode, Backend: TokenBackend, Rendering: ContextRenderingV1, SourceID: source, Limit: limit, Budget: budget, ProposedTokens: make([]*uint64, len(candidates))}
	retained := make(map[SHA256][]int)
	rendered := ""
	var total uint64
	for i, c := range candidates {
		row := &t.Candidates[i]
		if mode == "lexical" {
			t.Token.Lexical = append(t.Token.Lexical, TokenLexicalFact{Rank: c.LexicalRank, BM25: c.BM25})
		}
		if c.Disposition != CandidateAccepted {
			continue
		}
		total += row.TextBytes
		if total > MaxVectorTextBytes || row.SegmentID != base.Candidates[i].SegmentID || SHA256(sha256.Sum256([]byte(c.Text))) != row.ContentSHA256 {
			return SourceTrail{}, "", errors.New("token candidate bounds or content mismatch")
		}
		if t.PackingPolicy == PackingExactV1 {
			for _, prev := range retained[row.ContentSHA256] {
				if c.Text == candidates[prev].Text {
					row.Omission = "duplicate"
					row.DuplicateOf = t.Candidates[prev].SegmentID.String()
					break
				}
			}
			if row.DuplicateOf != "" {
				continue
			}
		}
		if row.TextBytes > t.BudgetBytes-t.UsedBytes {
			row.Omission = "bytes"
			continue
		}
		proposed := rendered
		if proposed != "" {
			proposed += "\n\n"
		}
		proposed += ContextPassage(source, c.Segment, c.Text)
		if len(proposed) > tokenizer.MaxBytes {
			row.Omission = "rendered_bytes"
			continue
		}
		count, err := counter.Count(proposed)
		if err != nil {
			return SourceTrail{}, "", err
		}
		t.Token.ProposedTokens[i] = &count
		if count > limit {
			row.Omission = "tokens"
			continue
		}
		row.Selected = true
		t.UsedBytes += row.TextBytes
		rendered = proposed
		t.Token.MeasuredTokens = count
		retained[row.ContentSHA256] = append(retained[row.ContentSHA256], i)
	}
	count, err := counter.Count(rendered)
	if err != nil {
		return SourceTrail{}, "", err
	}
	if count != t.Token.MeasuredTokens {
		return SourceTrail{}, "", errors.New("tokenizer final recount differs")
	}
	if err := counter.Finish(); err != nil {
		return SourceTrail{}, "", err
	}
	t.Token.RenderedBytes = uint64(len(rendered))
	t.Token.RenderedSHA256 = SHA256(sha256.Sum256([]byte(rendered)))
	t.PacketID, err = tokenPacketIdentity(t)
	if err != nil {
		return SourceTrail{}, "", err
	}
	t.ID, err = vectorTrailIdentity(t)
	if err != nil {
		return SourceTrail{}, "", err
	}
	return t, rendered, t.Validate()
}

func tokenPacketIdentity(t SourceTrail) (string, error) {
	t.ID = SourceTrailID{}
	t.RequestID = ""
	t.DecisionID = ""
	t.PacketID = ""
	data, err := encodeJSONContract(struct {
		Schema    string      `json:"schema"`
		Selection SourceTrail `json:"selection"`
	}{"mousa.context_packet.v3", t}, "token packet")
	return ContextPacketID(sha256.Sum256(data)).String(), err
}

// tokenRankingProjection reuses legacy ranking validation, not legacy packing decisions.
// All accepted rows are selected under a bounded original byte budget for this check.
func (t SourceTrail) tokenRankingProjection() (SourceTrail, error) {
	p := t
	p.Token = nil
	p.Candidates = slices.Clone(t.Candidates)
	p.PackingPolicy = PackingOriginal
	p.BudgetBytes = MaxVectorTextBytes
	p.UsedBytes = 0
	for i := range p.Candidates {
		r := &p.Candidates[i]
		r.Omission = ""
		r.DuplicateOf = ""
		r.Selected = r.Disposition == CandidateAccepted
		if r.Selected {
			p.UsedBytes += r.TextBytes
		}
	}
	switch t.Token.Mode {
	case "lexical":
		p.Schema = SourceTrailSchema
		p.PackingPolicy = ""
		if p.Vector != nil || p.Hybrid != nil {
			return SourceTrail{}, errors.New("lexical token record carries vector facts")
		}
	case "vector":
		p.Schema = SourceTrailSchemaV5
	case "hybrid":
		p.Schema = SourceTrailSchemaV6
	default:
		return SourceTrail{}, errors.New("unknown token retrieval mode")
	}
	if p.UsedBytes > MaxVectorTextBytes {
		return SourceTrail{}, errors.New("token candidate bytes exceed bound")
	}
	id, err := p.packetIdentity()
	if err != nil {
		return SourceTrail{}, err
	}
	p.PacketID = id.String()
	p.ID, err = NewSourceTrailID(p)
	if err != nil {
		return SourceTrail{}, err
	}
	return p, p.Validate()
}

func (t SourceTrail) validateTokenTrail() error {
	invalid := func() error { return errors.New("invalid v7 token selection bindings or accounting") }
	s := t.Token
	if s == nil || s.SourceID == (SourceID{}) || s.Backend != TokenBackend || s.Rendering != ContextRenderingV1 || s.Limit > tokenizer.MaxBytes || s.MeasuredTokens > s.Limit || s.RenderedBytes > tokenizer.MaxBytes || s.MeasuredTokens > s.RenderedBytes || s.RenderedSHA256 == (SHA256{}) || t.Supersession != nil || len(t.Associated) > 0 || len(t.AssociationOmissions) > 0 || t.Candidates == nil || len(t.Candidates) > MaxVectorPassages || len(s.ProposedTokens) != len(t.Candidates) || t.BudgetBytes == 0 || t.BudgetBytes > MaxVectorTextBytes || (t.PackingPolicy != PackingOriginal && t.PackingPolicy != PackingExactV1) {
		return invalid()
	}
	if t.Outcome == string(PolicyOutcomeDeny) && len(t.Candidates) != 0 {
		return invalid()
	}
	if available, err := s.Budget.Available(); err != nil || available != s.Limit {
		return invalid()
	}
	if err := t.validateCandidateInvariants(); err != nil {
		return err
	}
	if _, err := t.tokenRankingProjection(); err != nil {
		return err
	}
	if s.Mode == "lexical" {
		if len(s.Lexical) != len(t.Candidates) {
			return invalid()
		}
		for i, f := range s.Lexical {
			if f.Rank != i+1 || math.IsNaN(f.BM25) || math.IsInf(f.BM25, 0) {
				return invalid()
			}
			if i > 0 {
				previous := s.Lexical[i-1]
				if previous.BM25 > f.BM25 || previous.BM25 == f.BM25 && bytes.Compare(t.Candidates[i-1].SegmentID[:], t.Candidates[i].SegmentID[:]) >= 0 {
					return invalid()
				}
			}
		}
	} else if len(s.Lexical) > 0 {
		return invalid()
	}
	var used, rendered, measured uint64
	selected := map[string]TrailCandidate{}
	for i, r := range t.Candidates {
		n := s.ProposedTokens[i]
		if r.Disposition == CandidateRejected {
			if n != nil || r.Selected || r.TextBytes != 0 || r.FinalRank != 0 {
				return invalid()
			}
			continue
		}
		if r.TextBytes == 0 || r.TextBytes > MaxUTF8TextSegmentBytes || r.SegmentID == (SegmentID{}) || r.ContentSHA256 == (SHA256{}) {
			return invalid()
		}
		proposed := rendered + contextHeaderBytes() + r.TextBytes
		if rendered > 0 {
			proposed += 2
		}
		if r.Omission == "duplicate" {
			p, ok := selected[r.DuplicateOf]
			if !ok || t.PackingPolicy != PackingExactV1 || p.ContentSHA256 != r.ContentSHA256 || p.TextBytes != r.TextBytes || n != nil || r.Selected {
				return invalid()
			}
			continue
		}
		if r.DuplicateOf != "" {
			return invalid()
		}
		if r.TextBytes > t.BudgetBytes-used {
			if r.Omission != "bytes" || r.Selected || n != nil {
				return invalid()
			}
			continue
		}
		if proposed > tokenizer.MaxBytes {
			if r.Omission != "rendered_bytes" || r.Selected || n != nil {
				return invalid()
			}
			continue
		}
		if n == nil || *n == 0 || *n > proposed {
			return invalid()
		}
		if *n > s.Limit {
			if r.Omission != "tokens" || r.Selected {
				return invalid()
			}
			continue
		}
		if !r.Selected || r.Omission != "" {
			return invalid()
		}
		used += r.TextBytes
		rendered = proposed
		measured = *n
		selected[r.SegmentID.String()] = r
	}
	if used != t.UsedBytes || rendered != s.RenderedBytes || measured != s.MeasuredTokens || rendered == 0 && s.RenderedSHA256 != SHA256(sha256.Sum256(nil)) {
		return invalid()
	}
	packet, err := tokenPacketIdentity(t)
	if err != nil || packet != t.PacketID {
		return invalid()
	}
	id, err := vectorTrailIdentity(t)
	if err != nil || id != t.ID {
		return invalid()
	}
	return nil
}

func decodeTokenSourceTrail(data []byte) (SourceTrail, error) {
	var t SourceTrail
	if err := decodeStrictVectorJSON(data, &t); err != nil {
		return t, err
	}
	return t, t.Validate()
}

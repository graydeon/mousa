package mousa

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/graydeon/mousa/internal/tokenizer"
)

// TokenAssociationSelection aligns measured whole-context proposals with associated
// rows, separately from lexical consideration order. Null means no count was made.
type TokenAssociationSelection struct {
	AssociatedIDs  []SegmentID `json:"associated_ids"`
	ProposedTokens []*uint64   `json:"proposed_tokens"`
}

// PackAssociatedTokenContext resolves declared targets only after measured primary
// selection. The resolver and declaration must use the caller's authorization snapshot.
// A recorded supersession declaration is required even if no associations fire.
// Recorded association facts with supersession use v13; unfired declarations retain v8.
// One counter serves both stages and is completed before this function returns.
func PackAssociatedTokenContext(base SourceTrail, source SourceID, candidates []VerifiedLexicalCandidate, budget TokenBudget, counter *tokenizer.Counter, declaration *SupersessionDeclaration, resolve func(SourceTrail) ([]AssociatedPassage, []AssociationOmission, error)) (SourceTrail, string, error) {
	if resolve == nil {
		return SourceTrail{}, "", errors.New("token associations require a resolver")
	}
	if declaration != nil {
		if err := declaration.Validate(); err != nil {
			return SourceTrail{}, "", err
		}
		if base.Supersession == nil || base.Supersession.DeclarationID == nil || *base.Supersession.DeclarationID != declaration.ID {
			return SourceTrail{}, "", errors.New("declaration disagrees with recorded consultation")
		}
	} else if base.Supersession != nil && base.Supersession.DeclarationID != nil {
		return SourceTrail{}, "", errors.New("recorded declaration is required")
	}
	return packTokenContext(base, source, candidates, budget, counter, declaration, resolve)
}

func appendTokenAssociations(t SourceTrail, candidates []VerifiedLexicalCandidate, associated []AssociatedPassage, omissions []AssociationOmission, rendered string, counter *tokenizer.Counter, declaration *SupersessionDeclaration) (SourceTrail, string, error) {
	if len(associated) == 0 && len(omissions) == 0 {
		return t, rendered, nil
	}
	if len(candidates)+len(associated) > MaxVectorPassages {
		return SourceTrail{}, "", errors.New("combined token association passage rows exceed bound")
	}
	var total uint64
	var suppressed []bool
	if t.Supersession != nil {
		var err error
		suppressed, err = suppressSupersededCandidates(t.Candidates, t.Supersession)
		if err != nil {
			return SourceTrail{}, "", err
		}
	}
	retained := make([]retainedPassage, 0, len(candidates)+len(associated))
	for i, r := range t.Candidates {
		if t.Vector == nil && t.Hybrid == nil && r.Disposition == CandidateAccepted && !(len(suppressed) > 0 && suppressed[i]) {
			if r.TextBytes > MaxVectorTextBytes-total {
				return SourceTrail{}, "", errors.New("combined token association passage bytes exceed bound")
			}
			total += r.TextBytes
		}
		if r.Selected {
			retained = append(retained, retainedPassage{r.SegmentID, r.ContentSHA256, candidates[i].Text})
		}
	}
	for _, p := range associated {
		if err := p.Validate(); err != nil {
			return SourceTrail{}, "", err
		}
		if declaration != nil && p.Segment.RepresentationID == declaration.PredecessorRepresentationID {
			continue
		}
		if t.Vector == nil && t.Hybrid == nil && uint64(len(p.Text)) > MaxVectorTextBytes-total {
			return SourceTrail{}, "", errors.New("combined token association passage bytes exceed bound")
		}
		total += uint64(len(p.Text))
	}
	t.Schema = SourceTrailSchemaV11
	if t.Supersession != nil {
		t.Schema = SourceTrailSchemaV13
	}
	if t.Vector != nil || t.Hybrid != nil {
		t.Schema = SourceTrailSchemaV15
	}
	t.AssociationOmissions = slices.Clone(omissions)
	t.Associated = make([]TrailAssociated, len(associated))
	counts := &TokenAssociationSelection{AssociatedIDs: make([]SegmentID, len(associated)), ProposedTokens: make([]*uint64, len(associated))}
	t.Token.Associations = counts
	t.Token.PrimaryIDs = make([]SegmentID, len(t.Candidates))
	for i, row := range t.Candidates {
		t.Token.PrimaryIDs[i] = row.SegmentID
	}
	for i, p := range associated {
		r := &t.Associated[i]
		*r = TrailAssociated{SegmentID: p.Segment.ID, ContentSHA256: p.Segment.ContentSHA256, TextBytes: uint64(len(p.Text)), FromItem: p.Declaration.FromItem, ToItem: p.Declaration.ToItem, Basis: p.Declaration.Basis, Author: p.Declaration.Author}
		counts.AssociatedIDs[i] = r.SegmentID
		if declaration != nil && p.Segment.RepresentationID == declaration.PredecessorRepresentationID {
			r.Omission = string(SupersessionSuperseded)
			continue
		}
		for _, previous := range retained {
			if previous.segmentID == r.SegmentID || t.PackingPolicy == PackingExactV1 && previous.digest == r.ContentSHA256 && previous.text == p.Text {
				r.Omission = "duplicate"
				r.DuplicateOf = previous.segmentID.String()
				break
			}
		}
		if r.Omission != "" {
			continue
		}
		if r.TextBytes > t.BudgetBytes-t.UsedBytes {
			r.Omission = "bytes"
			continue
		}
		proposed := rendered
		if proposed != "" {
			proposed += "\n\n"
		}
		proposed += ContextPassage(t.Token.SourceID, p.Segment, p.Text)
		if len(proposed) > tokenizer.MaxBytes {
			r.Omission = "rendered_bytes"
			continue
		}
		n, err := counter.Count(proposed)
		if err != nil {
			return SourceTrail{}, "", err
		}
		counts.ProposedTokens[i] = &n
		if n > t.Token.Limit {
			r.Omission = "tokens"
			continue
		}
		r.Selected = true
		t.UsedBytes += r.TextBytes
		t.Token.MeasuredTokens = n
		rendered = proposed
		retained = append(retained, retainedPassage{r.SegmentID, r.ContentSHA256, p.Text})
	}
	return t, rendered, nil
}

func (t SourceTrail) validateTokenAssociationProfile() error {
	invalid := func() error { return errors.New("invalid v11 token association profile or alignment") }
	s := t.Token.Associations
	retrieval := t.Schema == SourceTrailSchemaV15
	if retrieval {
		if err := t.validateRetrievalAssociations(); err != nil {
			return err
		}
	} else if t.Token.Mode != "lexical" || t.Vector != nil || t.Hybrid != nil || (t.Supersession != nil) != (t.Schema == SourceTrailSchemaV13) {
		return invalid()
	}
	if t.Outcome != string(PolicyOutcomeAllow) || s == nil || s.AssociatedIDs == nil || s.ProposedTokens == nil || len(s.AssociatedIDs) != len(t.Associated) || len(s.ProposedTokens) != len(t.Associated) || len(t.Token.PrimaryIDs) != len(t.Candidates) || len(t.Candidates) == 0 || len(t.Associated)+len(t.AssociationOmissions) == 0 || len(t.Candidates)+len(t.Associated) > MaxVectorPassages || len(t.AssociationOmissions) > MaxAssociationDeclarations {
		return invalid()
	}
	var total uint64
	var suppressed []bool
	if t.Supersession != nil {
		var err error
		suppressed, err = suppressSupersededCandidates(t.Candidates, t.Supersession)
		if err != nil {
			return err
		}
	}
	hasPrimary := false
	for i, r := range t.Candidates {
		if t.Token.PrimaryIDs[i] != r.SegmentID {
			return invalid()
		}
		hasPrimary = hasPrimary || r.Selected
		if !retrieval && r.Disposition == CandidateAccepted && !(len(suppressed) > 0 && suppressed[i]) {
			if r.TextBytes > MaxVectorTextBytes-total {
				return invalid()
			}
			total += r.TextBytes
		}
	}
	if !hasPrimary {
		return invalid()
	}
	seen := map[SegmentID]bool{}
	declarations := map[string]AssociationDeclaration{}
	targets := map[string]AssociationDeclaration{}
	lastTarget := ""
	for i, r := range t.Associated {
		if t.Schema == SourceTrailSchemaV13 && (r.TextBytes == 0 || r.TextBytes > MaxUTF8TextSegmentBytes) {
			return invalid()
		}
		if s.AssociatedIDs[i] != r.SegmentID || seen[r.SegmentID] {
			return invalid()
		}
		seen[r.SegmentID] = true
		if !retrieval && r.Omission != string(SupersessionSuperseded) {
			if r.TextBytes > MaxVectorTextBytes-total {
				return invalid()
			}
			total += r.TextBytes
		}
		key := r.FromItem + "\x00" + r.ToItem
		d := AssociationDeclaration{r.FromItem, r.ToItem, r.Basis, r.Author}
		if old, ok := targets[r.ToItem]; ok && (old != d || lastTarget != r.ToItem) {
			return invalid()
		}
		declarations[key] = d
		targets[r.ToItem] = d
		lastTarget = r.ToItem
	}
	for _, omission := range t.AssociationOmissions {
		key := omission.FromItem + "\x00" + omission.ToItem
		if _, duplicate := declarations[key]; duplicate || omission.Reason == AssociationTargetTruncated {
			return invalid()
		}
		declarations[key] = AssociationDeclaration{}
	}
	if len(targets) > MaxAssociationTargets || len(declarations) > MaxAssociationDeclarations {
		return invalid()
	}
	return t.validateAssociated()
}

func (t SourceTrail) validateAssociatedTokenAccounting(used, rendered, measured *uint64, selected map[string]TrailCandidate) error {
	invalid := func() error { return errors.New("invalid v11 associated token accounting") }
	for i, r := range t.Associated {
		n := t.Token.Associations.ProposedTokens[i]
		if r.Omission == string(SupersessionSuperseded) {
			if (t.Schema != SourceTrailSchemaV13 && t.Schema != SourceTrailSchemaV15) || t.Supersession == nil || r.Selected || r.DuplicateOf != "" || n != nil {
				return invalid()
			}
			continue
		}
		proposed := *rendered + contextHeaderBytes() + r.TextBytes
		if *rendered > 0 {
			proposed += 2
		}
		if r.Omission == "duplicate" {
			p, ok := selected[r.DuplicateOf]
			if !ok || (t.PackingPolicy != PackingExactV1 && p.SegmentID != r.SegmentID) || p.ContentSHA256 != r.ContentSHA256 || p.TextBytes != r.TextBytes || r.Selected || n != nil {
				return invalid()
			}
			continue
		}
		if _, exists := selected[r.SegmentID.String()]; exists || r.DuplicateOf != "" {
			return invalid()
		}
		if r.TextBytes > t.BudgetBytes-*used {
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
		if *n > t.Token.Limit {
			if r.Omission != "tokens" || r.Selected {
				return invalid()
			}
			continue
		}
		if !r.Selected || r.Omission != "" {
			return invalid()
		}
		*used += r.TextBytes
		*rendered = proposed
		*measured = *n
		selected[r.SegmentID.String()] = TrailCandidate{SegmentID: r.SegmentID, ContentSHA256: r.ContentSHA256, TextBytes: r.TextBytes}
	}
	return nil
}

func decodeTokenAssociationMembers(data []byte, t SourceTrail) error {
	var fields map[string]json.RawMessage
	if err := decodeStrictVectorJSON(data, &fields); err != nil {
		return err
	}
	keys := []string{"schema", "id", "request_id", "decision_id", "outcome", "expression", "budget_bytes", "used_bytes", "packet_id", "candidates", "packing_policy", "token"}
	if len(t.Associated) > 0 {
		keys = append(keys, "associated")
	}
	if len(t.AssociationOmissions) > 0 {
		keys = append(keys, "association_omissions")
	}
	if _, err := vectorObject(data, keys...); err != nil {
		return err
	}
	token, err := vectorObject(fields["token"], "mode", "backend", "rendering", "source_id", "limit", "budget", "measured_tokens", "rendered_bytes", "rendered_sha256", "proposed_tokens", "lexical", "associations", "primary_ids")
	if err != nil {
		return err
	}
	if _, err := vectorObject(token["budget"], "cap", "window", "prompt_reserve", "output_reserve"); err != nil {
		return err
	}
	if _, err := vectorObject(token["associations"], "associated_ids", "proposed_tokens"); err != nil {
		return err
	}
	var lexical []json.RawMessage
	if err := json.Unmarshal(token["lexical"], &lexical); err != nil {
		return err
	}
	for _, fact := range lexical {
		if _, err := vectorObject(fact, "rank", "bm25"); err != nil {
			return err
		}
	}
	var rows []json.RawMessage
	if len(t.Associated) > 0 {
		if err := json.Unmarshal(fields["associated"], &rows); err != nil {
			return err
		}
	}
	for i, row := range rows {
		keys := []string{"segment_id", "content_sha256", "text_bytes", "selected", "from_item", "to_item", "basis", "author"}
		if t.Associated[i].Omission != "" {
			keys = append(keys, "omission")
		}
		if t.Associated[i].DuplicateOf != "" {
			keys = append(keys, "duplicate_of")
		}
		if _, err := vectorObject(row, keys...); err != nil {
			return err
		}
	}
	if len(t.AssociationOmissions) > 0 {
		var omissions []json.RawMessage
		if err := json.Unmarshal(fields["association_omissions"], &omissions); err != nil {
			return err
		}
		for _, omission := range omissions {
			if _, err := vectorObject(omission, "from_item", "to_item", "reason"); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectLegacyTokenAssociationMembers(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for key, raw := range fields {
		if !strings.EqualFold(key, "token") {
			continue
		}
		var token map[string]json.RawMessage
		if err := json.Unmarshal(raw, &token); err != nil {
			return err
		}
		for member := range token {
			if strings.EqualFold(member, "associations") || strings.EqualFold(member, "primary_ids") {
				return errors.New("token association members require v11")
			}
		}
	}
	return nil
}

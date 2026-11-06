package mousa

import "errors"

const SourceTrailSchemaV14 = "mousa.source_trail.v14"
const SourceTrailSchemaV15 = "mousa.source_trail.v15"

func (t SourceTrail) validateRetrievalAssociations() error {
	invalid := func() error { return errors.New("invalid retrieval association profile") }
	if (t.Vector == nil) == (t.Hybrid == nil) || t.Outcome != string(PolicyOutcomeAllow) || len(t.Associated)+len(t.AssociationOmissions) == 0 || len(t.Candidates)+len(t.Associated) > MaxVectorPassages {
		return invalid()
	}
	// Reuse the closed association profile without changing the producing record.
	profile := t
	profile.Vector, profile.Hybrid, profile.Token = nil, nil, nil
	if profile.Supersession == nil {
		profile.Supersession = &SupersessionSelection{}
	}
	if err := profile.validateAssociatedSupersessionProfile(); err != nil {
		return err
	}
	seen := make(map[SegmentID]bool, len(t.Candidates)+len(t.Associated))
	var total uint64
	for _, r := range t.Candidates {
		if !seen[r.SegmentID] {
			if r.TextBytes > MaxVectorTextBytes-total {
				return invalid()
			}
			total += r.TextBytes
			seen[r.SegmentID] = true
		}
	}
	declarations := make(map[string]bool, len(t.Associated)+len(t.AssociationOmissions))
	for _, r := range t.Associated {
		if r.TextBytes == 0 || r.TextBytes > MaxUTF8TextSegmentBytes {
			return invalid()
		}
		declarations[r.FromItem+"\x00"+r.ToItem] = true
		if t.PackingPolicy == PackingOriginal && r.Omission == "duplicate" && r.DuplicateOf != r.SegmentID.String() {
			return invalid()
		}
		if !seen[r.SegmentID] {
			if r.TextBytes > MaxVectorTextBytes-total {
				return invalid()
			}
			total += r.TextBytes
			seen[r.SegmentID] = true
		}
	}
	for _, r := range t.AssociationOmissions {
		key := r.FromItem + "\x00" + r.ToItem
		if declarations[key] || r.Reason == AssociationTargetTruncated {
			return invalid()
		}
		declarations[key] = true
	}
	if len(declarations) > MaxAssociationDeclarations {
		return invalid()
	}
	return nil
}

func (t SourceTrail) validateRetrievalAssociationTrail() error {
	if t.Token != nil {
		return errors.New("byte retrieval association carries tokens")
	}
	if err := t.validateRetrievalAssociations(); err != nil {
		return err
	}
	p := t
	p.Associated, p.AssociationOmissions = nil, nil
	p.UsedBytes = 0
	for _, r := range p.Candidates {
		if r.Selected {
			p.UsedBytes += r.TextBytes
		}
	}
	p.Schema = SourceTrailSchemaV5
	if p.Hybrid != nil {
		p.Schema = SourceTrailSchemaV6
	}
	if p.Supersession != nil {
		p.Schema = SourceTrailSchemaV9
	}
	packet, err := p.packetIdentity()
	if err != nil {
		return err
	}
	p.PacketID = packet.String()
	p.ID, err = NewSourceTrailID(p)
	if err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if err := t.validateAssociated(); err != nil {
		return err
	}
	used := p.UsedBytes
	for _, r := range t.Associated {
		if r.Selected {
			used += r.TextBytes
		}
	}
	packet, err = contextPacketIDV3(t)
	if err != nil {
		return err
	}
	id, err := NewSourceTrailID(t)
	if err != nil {
		return err
	}
	if used != t.UsedBytes || packet.String() != t.PacketID || id != t.ID {
		return errors.New("retrieval association accounting or identity mismatch")
	}
	return nil
}

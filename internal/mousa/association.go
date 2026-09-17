package mousa

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"unicode/utf8"
)

// Association declarations are explicit inputs. Mousa never infers a relationship between
// items, and a declaration is not an access grant: resolution stays inside one allowed
// source decision, and the author and basis stay attached to every passage released.
const (
	AssociationDeclarationSchema = "mousa.association_declarations.v1"
	// MaxAssociationDeclarations bounds one declaration file, so one request cannot turn an
	// unbounded number of declared relationships into resolution work.
	MaxAssociationDeclarations = 32
	// MaxAssociationTargets bounds how many distinct target items one query reads, so
	// declared fan-out stays bounded additional work.
	MaxAssociationTargets     = 4
	MaxAssociationItemBytes   = 1024
	MaxAssociationBasisBytes  = 512
	MaxAssociationAuthorBytes = 128
)

// Relationship omissions. A declared relationship that releases no passage is recorded with
// one of these reasons instead of being dropped silently.
const (
	AssociationTargetUnknown   = "target_unknown"
	AssociationTargetInactive  = "target_inactive"
	AssociationFanOut          = "fan_out"
	AssociationDuplicateTarget = "duplicate_target"
)

// AssociationDeclaration is one author-declared relationship between two items of one
// source: evidence of FromItem is incomplete for its declared use without the qualification
// documented in ToItem. The declaration stays visible so its author and limits can be judged.
type AssociationDeclaration struct {
	FromItem string `json:"from_item"`
	ToItem   string `json:"to_item"`
	Basis    string `json:"basis"`
	Author   string `json:"author"`
}

// Validate checks that one declaration is complete, bounded and meaningful.
func (declaration AssociationDeclaration) Validate() error {
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"from_item", declaration.FromItem, MaxAssociationItemBytes},
		{"to_item", declaration.ToItem, MaxAssociationItemBytes},
		{"basis", declaration.Basis, MaxAssociationBasisBytes},
		{"author", declaration.Author, MaxAssociationAuthorBytes},
	} {
		if field.value == "" {
			return newValidationError(field.name, ValidationCodeInvalidValue, "must not be empty", nil)
		}
		if len(field.value) > field.limit {
			return newValidationError(field.name, ValidationCodeInvalidRange, "exceeds the byte limit", nil)
		}
		if !utf8.ValidString(field.value) || strings.ContainsRune(field.value, 0) {
			return newValidationError(field.name, ValidationCodeInvalidValue, "must be valid UTF-8 without NUL", nil)
		}
	}
	if declaration.FromItem == declaration.ToItem {
		return newValidationError("to_item", ValidationCodeInvalidValue, "must differ from from_item", nil)
	}
	return nil
}

// validateAssociationItem checks one item identity: nonempty, bounded, UTF-8 without NUL.
func validateAssociationItem(field, value string) error {
	if value == "" {
		return newValidationError(field, ValidationCodeInvalidValue, "must not be empty", nil)
	}
	if len(value) > MaxAssociationItemBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return newValidationError(field, ValidationCodeInvalidRange, "must be valid UTF-8 within the item limit", nil)
	}
	return nil
}

// DecodeAssociationDeclarations decodes one strict declaration file. Duplicate
// (from_item, to_item) pairs are an author error, so they are rejected instead of silently
// applied once.
func DecodeAssociationDeclarations(data []byte) ([]AssociationDeclaration, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var wire struct {
		Schema       string                   `json:"schema"`
		Associations []AssociationDeclaration `json:"associations"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return nil, err
	}
	if wire.Schema != AssociationDeclarationSchema {
		return nil, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.association_declarations.v1", nil)
	}
	if len(wire.Associations) == 0 {
		return nil, newValidationError("associations", ValidationCodeInvalidValue, "must declare at least one association", nil)
	}
	if len(wire.Associations) > MaxAssociationDeclarations {
		return nil, newValidationError("associations", ValidationCodeInvalidRange, "exceeds the declaration limit", nil)
	}
	seen := make(map[string]struct{}, len(wire.Associations))
	for index, declaration := range wire.Associations {
		if err := declaration.Validate(); err != nil {
			return nil, prefixValidationError(err, "associations["+itoa(index)+"]")
		}
		key := declaration.FromItem + "\x00" + declaration.ToItem
		if _, duplicate := seen[key]; duplicate {
			return nil, newValidationError("associations["+itoa(index)+"]", ValidationCodeInvalidValue, "repeats an earlier from_item and to_item", nil)
		}
		seen[key] = struct{}{}
	}
	return wire.Associations, nil
}

// AssociatedPassage is one passage considered through a declared association rather than a
// lexical match: the target item's verified current text with its own segment identity and
// coordinates. The declaration is recorded, not implied.
type AssociatedPassage struct {
	Segment     Segment
	Item        string
	Text        string
	Declaration AssociationDeclaration
}

// Validate checks the passage's own identity agreement before it can enter a trail.
func (passage AssociatedPassage) Validate() error {
	if err := passage.Segment.Validate(); err != nil {
		return prefixValidationError(err, "segment")
	}
	if err := validateAssociationItem("item", passage.Item); err != nil {
		return err
	}
	if err := passage.Declaration.Validate(); err != nil {
		return prefixValidationError(err, "declaration")
	}
	if passage.Declaration.ToItem != passage.Item {
		return newValidationError("item", ValidationCodeInvalidValue, "disagrees with the declaration target", nil)
	}
	if SHA256(sha256.Sum256([]byte(passage.Text))) != passage.Segment.ContentSHA256 {
		return newValidationError("text", ValidationCodeInvalidDigest, "text disagrees with segment digest", nil)
	}
	return nil
}

// TrailAssociated is one considered associated passage in a Source Trail. It carries the
// declaration that included it, so the trail explains the release without storing text.
type TrailAssociated struct {
	SegmentID     SegmentID `json:"segment_id"`
	ContentSHA256 SHA256    `json:"content_sha256"`
	TextBytes     uint64    `json:"text_bytes"`
	Selected      bool      `json:"selected"`
	Omission      string    `json:"omission,omitempty"`
	DuplicateOf   string    `json:"duplicate_of,omitempty"`
	FromItem      string    `json:"from_item"`
	ToItem        string    `json:"to_item"`
	Basis         string    `json:"basis"`
	Author        string    `json:"author"`
}

// AssociationOmission records one declared relationship that released no passage, and why.
type AssociationOmission struct {
	FromItem string `json:"from_item"`
	ToItem   string `json:"to_item"`
	Reason   string `json:"reason"`
}

// Validate checks that one omission names a relationship and a known reason.
func (omission AssociationOmission) Validate() error {
	if err := validateAssociationItem("from_item", omission.FromItem); err != nil {
		return err
	}
	if err := validateAssociationItem("to_item", omission.ToItem); err != nil {
		return err
	}
	if omission.FromItem == omission.ToItem {
		return newValidationError("to_item", ValidationCodeInvalidValue, "must differ from from_item", nil)
	}
	switch omission.Reason {
	case AssociationTargetUnknown, AssociationTargetInactive, AssociationFanOut, AssociationDuplicateTarget:
	default:
		return newValidationError("reason", ValidationCodeInvalidValue, "must be a known association omission reason", nil)
	}
	return nil
}

// retainedPassage is one passage the packet already releases: identity, digest and text.
// The digest narrows comparisons; only byte-equal text suppresses an exact-v1 passage.
type retainedPassage struct {
	segmentID SegmentID
	digest    SHA256
	text      string
}

// validateAssociated checks the association stage of one trail: every row keeps its own
// identity and accounting, and every omission stays explainable.
func (trail SourceTrail) validateAssociated() error {
	if trail.Outcome == string(PolicyOutcomeDeny) {
		if len(trail.Associated) != 0 || len(trail.AssociationOmissions) != 0 {
			return retrievalValidationError("associated", ValidationCodeInvalidValue, "deny cannot carry associated passages or omissions")
		}
	}
	if trail.PackingPolicy != PackingOriginal && trail.PackingPolicy != PackingExactV1 {
		return retrievalValidationError("packing_policy", ValidationCodeInvalidValue, "v3 requires original or exact-v1 packing")
	}
	primaryUsed := uint64(0)
	for _, candidate := range trail.Candidates {
		if candidate.Selected {
			primaryUsed += candidate.TextBytes
		}
	}
	// Associated rows account against the budget the primary evidence left over.
	remaining := trail.BudgetBytes - primaryUsed
	released := make(map[string]retainedPassage, len(trail.Candidates)+len(trail.Associated))
	for _, candidate := range trail.Candidates {
		if !candidate.Selected {
			continue
		}
		released[candidate.SegmentID.String()] = retainedPassage{
			segmentID: candidate.SegmentID, digest: candidate.ContentSHA256,
		}
	}
	for index, row := range trail.Associated {
		field := "associated[" + itoa(index) + "]"
		if row.SegmentID == (SegmentID{}) {
			return newValidationError(field+".segment_id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		if row.ContentSHA256 == (SHA256{}) {
			return newValidationError(field+".content_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
		}
		if row.TextBytes == 0 {
			return newValidationError(field+".text_bytes", ValidationCodeInvalidRange, "associated passage must carry released bytes", nil)
		}
		if err := validateAssociationItem(field+".from_item", row.FromItem); err != nil {
			return err
		}
		if err := validateAssociationItem(field+".to_item", row.ToItem); err != nil {
			return err
		}
		if row.FromItem == row.ToItem {
			return newValidationError(field+".to_item", ValidationCodeInvalidValue, "must differ from from_item", nil)
		}
		for _, item := range []struct {
			name  string
			value string
			limit int
		}{{"basis", row.Basis, MaxAssociationBasisBytes}, {"author", row.Author, MaxAssociationAuthorBytes}} {
			if item.value == "" || len(item.value) > item.limit || !utf8.ValidString(item.value) || strings.ContainsRune(item.value, 0) {
				return newValidationError(field+"."+item.name, ValidationCodeInvalidValue, "must be non-empty valid UTF-8 within bounds", nil)
			}
		}
		_, repeated := released[row.SegmentID.String()]
		if row.Selected {
			if row.Omission != "" || row.DuplicateOf != "" {
				return newValidationError(field, ValidationCodeInvalidValue, "selected associated passage contradicts omission", nil)
			}
			if repeated {
				return newValidationError(field+".segment_id", ValidationCodeInvalidValue, "releases a segment already in the packet", nil)
			}
			if row.TextBytes > remaining {
				return newValidationError(field+".text_bytes", ValidationCodeInvalidRange, "exceeds the remaining budget", nil)
			}
			remaining -= row.TextBytes
			released[row.SegmentID.String()] = retainedPassage{segmentID: row.SegmentID, digest: row.ContentSHA256}
			continue
		}
		switch row.Omission {
		case "duplicate":
			previous, exists := released[row.DuplicateOf]
			if !exists || previous.digest != row.ContentSHA256 {
				return newValidationError(field+".duplicate_of", ValidationCodeInvalidValue, "must reference a prior released passage with equal digest", nil)
			}
			if row.DuplicateOf != row.SegmentID.String() && repeated {
				return newValidationError(field+".segment_id", ValidationCodeInvalidValue, "repeats a released segment without naming it", nil)
			}
		case "budget":
			if row.DuplicateOf != "" {
				return newValidationError(field+".duplicate_of", ValidationCodeInvalidValue, "budget omission must not name a duplicate", nil)
			}
			if repeated {
				return newValidationError(field+".segment_id", ValidationCodeInvalidValue, "repeats a released segment without naming it", nil)
			}
			if row.TextBytes <= remaining {
				return newValidationError(field+".text_bytes", ValidationCodeInvalidRange, "budget omission must not fit the remaining budget", nil)
			}
		default:
			return newValidationError(field+".omission", ValidationCodeInvalidValue, "unselected associated passage requires a packing omission", nil)
		}
	}
	seen := make(map[string]struct{}, len(trail.AssociationOmissions))
	for index, omission := range trail.AssociationOmissions {
		if err := omission.Validate(); err != nil {
			return prefixValidationError(err, "association_omissions["+itoa(index)+"]")
		}
		key := omission.FromItem + "\x00" + omission.ToItem + "\x00" + omission.Reason
		if _, duplicate := seen[key]; duplicate {
			return newValidationError("association_omissions["+itoa(index)+"]", ValidationCodeInvalidValue, "repeats an earlier omission", nil)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// AppendAssociatedPassages is the association stage: given a packed primary trail, it packs
// the considered associated passages into the remaining budget, records the declared
// relationships that released nothing, and re-derives the trail and packet identities under
// the v3 schema. Primary evidence keeps its existing selection and priority; an associated
// passage is never selected ahead of primary evidence. With nothing considered and nothing
// omitted, the trail is returned unchanged.
func AppendAssociatedPassages(trail SourceTrail, candidates []VerifiedLexicalCandidate, associated []AssociatedPassage, omissions []AssociationOmission) (SourceTrail, error) {
	if err := trail.Validate(); err != nil {
		return SourceTrail{}, err
	}
	if len(associated) == 0 && len(omissions) == 0 {
		return trail, nil
	}
	if trail.Schema != SourceTrailSchema && trail.Schema != SourceTrailSchemaV2 {
		return SourceTrail{}, newValidationError("schema", ValidationCodeInvalidSchema, "association stage requires an original or exact-v1 trail", nil)
	}
	if trail.Outcome != string(PolicyOutcomeAllow) {
		return SourceTrail{}, newValidationError("outcome", ValidationCodeInvalidValue, "only an allowed retrieval can record associated passages", nil)
	}
	if len(trail.Candidates) != len(candidates) {
		return SourceTrail{}, newValidationError("candidates", ValidationCodeInvalidRange, "verified candidates disagree with the trail", nil)
	}
	if len(omissions) > 0 {
		trail.AssociationOmissions = make([]AssociationOmission, len(omissions))
		copy(trail.AssociationOmissions, omissions)
	}
	// The released set starts from the primary selection, so budget sharing and duplicate
	// accounting compare associated passages against what primary evidence already released.
	retained := make([]retainedPassage, 0, len(trail.Candidates)+len(associated))
	bySegment := make(map[string]int, cap(retained))
	for index, candidate := range trail.Candidates {
		if !candidate.Selected {
			continue
		}
		bySegment[candidates[index].Segment.ID.String()] = len(retained)
		retained = append(retained, retainedPassage{segmentID: candidates[index].Segment.ID, digest: candidate.ContentSHA256, text: candidates[index].Text})
	}
	used := trail.UsedBytes
	rows := make([]TrailAssociated, len(associated))
	for index, passage := range associated {
		if err := passage.Validate(); err != nil {
			return SourceTrail{}, prefixValidationError(err, "associated["+itoa(index)+"]")
		}
		row := &rows[index]
		row.SegmentID = passage.Segment.ID
		row.ContentSHA256 = passage.Segment.ContentSHA256
		row.TextBytes = uint64(len(passage.Text))
		row.FromItem = passage.Declaration.FromItem
		row.ToItem = passage.Declaration.ToItem
		row.Basis = passage.Declaration.Basis
		row.Author = passage.Declaration.Author
		// A packet never releases one segment twice, whatever the packing policy: the second
		// occurrence is an omission that names the released passage.
		if previous, seen := bySegment[row.SegmentID.String()]; seen {
			row.Omission = "duplicate"
			row.DuplicateOf = retained[previous].segmentID.String()
			continue
		}
		// exact-v1 never releases byte-equal text twice. The original policy releases every
		// considered passage whose bytes fit, which preserves its documented behavior.
		if trail.PackingPolicy == PackingExactV1 {
			if previous, duplicate := retainedIndexByDigest(retained, row.ContentSHA256); duplicate && retained[previous].text == passage.Text {
				row.Omission = "duplicate"
				row.DuplicateOf = retained[previous].segmentID.String()
				continue
			}
		}
		if row.TextBytes > trail.BudgetBytes-used {
			row.Omission = "budget"
			continue
		}
		row.Selected = true
		used += row.TextBytes
		bySegment[row.SegmentID.String()] = len(retained)
		retained = append(retained, retainedPassage{segmentID: row.SegmentID, digest: row.ContentSHA256, text: passage.Text})
	}
	if len(rows) > 0 {
		trail.Associated = rows
	}
	if trail.PackingPolicy == "" {
		trail.PackingPolicy = PackingOriginal
	}
	trail.Schema = SourceTrailSchemaV3
	trail.UsedBytes = used
	packetID, err := contextPacketIDV3(trail)
	if err != nil {
		return SourceTrail{}, err
	}
	trail.PacketID = packetID.String()
	id, err := NewSourceTrailID(trail)
	if err != nil {
		return SourceTrail{}, err
	}
	trail.ID = id
	if err := trail.Validate(); err != nil {
		return SourceTrail{}, err
	}
	return trail, nil
}

func retainedIndexByDigest(retained []retainedPassage, digest SHA256) (int, bool) {
	for index, passage := range retained {
		if passage.digest == digest {
			return index, true
		}
	}
	return 0, false
}

// contextPacketIDV3 binds one v3 packed selection: the v1 domain plus the associated rows,
// so a packet identity covers every released byte.
func contextPacketIDV3(trail SourceTrail) (ContextPacketID, error) {
	digest := sha256.New()
	var budget [8]byte
	binary.BigEndian.PutUint64(budget[:], trail.BudgetBytes)
	fields := [][]byte{[]byte(ContextPacketSchemaV2), budget[:]}
	var used uint64
	for _, candidate := range trail.Candidates {
		if !candidate.Selected {
			continue
		}
		var rank, bytes [8]byte
		binary.BigEndian.PutUint64(rank[:], uint64(candidate.FinalRank))
		binary.BigEndian.PutUint64(bytes[:], candidate.TextBytes)
		fields = append(fields, candidate.SegmentID[:], candidate.ContentSHA256[:], rank[:], bytes[:])
		used += candidate.TextBytes
	}
	for _, row := range trail.Associated {
		if !row.Selected {
			continue
		}
		var bytes [8]byte
		binary.BigEndian.PutUint64(bytes[:], row.TextBytes)
		fields = append(fields, row.SegmentID[:], row.ContentSHA256[:], bytes[:])
		used += row.TextBytes
	}
	var usedField [8]byte
	binary.BigEndian.PutUint64(usedField[:], used)
	fields = append(fields, usedField[:])
	writeTuple(digest, fields...)
	return ContextPacketID(digest.Sum(nil)), nil
}

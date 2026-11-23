package mousa

import (
	"bytes"
	"crypto/sha256"
)

// NoteStale describes a citation that cannot support a fresh note.
type NoteStale struct {
	TrailID   SourceTrailID `json:"trail_id"`
	SegmentID *SegmentID    `json:"segment_id,omitempty"`
	Reason    string        `json:"reason"`
}

type NoteEvidence struct {
	ID            SHA256         `json:"id"`
	Author        string         `json:"author"`
	CreatedAtUsec int64          `json:"created_at_usec"`
	DerivedFrom   []NoteCitation `json:"derived_from"`
	Origin        string         `json:"origin"`
	Stale         []NoteStale    `json:"stale"`
}

// NoteSchema is the canonical schema/version of a durable note record.
const NoteSchema = "mousa.note.v1"

// LogKindNote is the log kind of the immutable leaf written for every note.
const LogKindNote = "note"

// NoteCitation binds a note to the evidence it was derived from: one source
// trail plus the selected segments the note relies on. An omitted or empty
// segment list on input means every segment the trail selected; a stored
// citation always lists its segments explicitly.
type NoteCitation struct {
	SegmentIDs []SegmentID   `json:"segment_ids"`
	TrailID    SourceTrailID `json:"trail_id"`
}

// Note is the immutable canonical record of one durable note. The identity is
// the SHA-256 digest of the canonical record with the id field omitted, so
// every field that produced the note participates in its identity.
type Note struct {
	Author        string         `json:"author"`
	ContentSHA256 SHA256         `json:"content_sha256"`
	CreatedAtUsec int64          `json:"created_at_usec"`
	DerivedFrom   []NoteCitation `json:"derived_from"`
	ID            SHA256         `json:"id,omitzero"`
	ItemID        string         `json:"item_id"`
	Schema        string         `json:"schema"`
	SourceID      SourceID       `json:"source_id"`
}

// NoteItemID is the local item identity of a note: the hex form of its content
// digest, so two writes of identical text address the same item.
func NoteItemID(digest SHA256) string { return digest.String() }

// NewNote builds the canonical note for one write. The digest is computed over
// the canonical record with id omitted and then stored back on the note.
func NewNote(sourceID SourceID, author string, contentSHA256 SHA256, createdAtUsec int64, derivedFrom []NoteCitation) (Note, error) {
	if derivedFrom == nil {
		derivedFrom = []NoteCitation{}
	}
	note := Note{
		Schema: NoteSchema, SourceID: sourceID, Author: author,
		ContentSHA256: contentSHA256, CreatedAtUsec: createdAtUsec,
		DerivedFrom: derivedFrom, ItemID: NoteItemID(contentSHA256),
	}
	digest, err := noteDigest(note)
	if err != nil {
		return Note{}, err
	}
	note.ID = digest
	if err := note.Validate(); err != nil {
		return Note{}, err
	}
	return note, nil
}

// noteDigest hashes the canonical record encoding with the id field omitted.
func noteDigest(note Note) (SHA256, error) {
	note.ID = SHA256{}
	data, err := encodeJSONContract(note, "note")
	if err != nil {
		return SHA256{}, err
	}
	return SHA256(sha256.Sum256(bytes.TrimSuffix(data, []byte("\n")))), nil
}

// Validate checks the stored form of a note: every citation must name its
// segments explicitly, and the id must be the digest of the rest of the record.
func (note Note) Validate() error {
	if note.Schema != NoteSchema {
		return newValidationError("schema", ValidationCodeInvalidValue, "must be "+NoteSchema, nil)
	}
	if err := validateIdentityInput("author", note.Author); err != nil {
		return err
	}
	if note.ContentSHA256 == (SHA256{}) {
		return newValidationError("content_sha256", ValidationCodeInvalidValue, "must not be zero", nil)
	}
	if note.CreatedAtUsec <= 0 || note.CreatedAtUsec > maxJSONSafeInteger {
		return newValidationError("created_at_usec", ValidationCodeInvalidValue, "must be a positive JSON-safe integer", nil)
	}
	if note.DerivedFrom == nil {
		return newValidationError("derived_from", ValidationCodeInvalidValue, "must not be null", nil)
	}
	if err := validateNoteCitations(note.DerivedFrom, true); err != nil {
		return err
	}
	if note.ItemID != NoteItemID(note.ContentSHA256) {
		return newValidationError("item_id", ValidationCodeInvalidValue, "must be the content digest", nil)
	}
	if note.SourceID == (SourceID{}) {
		return newValidationError("source_id", ValidationCodeInvalidValue, "must not be zero", nil)
	}
	if note.ID == (SHA256{}) {
		return newValidationError("id", ValidationCodeInvalidValue, "must not be zero", nil)
	}
	digest, err := noteDigest(note)
	if err != nil {
		return err
	}
	if note.ID != digest {
		return newValidationError("id", ValidationCodeInvalidValue, "must equal the record digest", nil)
	}
	return nil
}

// validateNoteCitations rejects malformed, duplicated, or ambiguous lineage.
// Stored citations must name at least one segment; input citations may leave
// the list empty to mean every selected segment of the trail.
func validateNoteCitations(citations []NoteCitation, stored bool) error {
	trails := make(map[SourceTrailID]bool, len(citations))
	for _, citation := range citations {
		field := "derived_from"
		// Citations are (trail, segment) pairs: a segment may repeat across
		// distinct trails, but never inside one citation.
		segments := make(map[SegmentID]bool)
		if citation.TrailID == (SourceTrailID{}) {
			return newValidationError(field, ValidationCodeInvalidValue, "citation trail_id must not be zero", nil)
		}
		if trails[citation.TrailID] {
			return newValidationError(field, ValidationCodeInvalidValue, "citation repeats a trail", nil)
		}
		trails[citation.TrailID] = true
		if stored && len(citation.SegmentIDs) == 0 {
			return newValidationError(field, ValidationCodeInvalidValue, "a stored citation must list its segments", nil)
		}
		for _, segment := range citation.SegmentIDs {
			if segment == (SegmentID{}) {
				return newValidationError(field, ValidationCodeInvalidValue, "citation segment id must not be zero", nil)
			}
			if segments[segment] {
				return newValidationError(field, ValidationCodeInvalidValue, "citation repeats a segment", nil)
			}
			segments[segment] = true
		}
	}
	return nil
}

// EncodeNote emits the canonical record bytes, without a trailing newline.
func EncodeNote(note Note) ([]byte, error) {
	if err := note.Validate(); err != nil {
		return nil, err
	}
	data, err := encodeJSONContract(note, "note")
	if err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(data, []byte("\n")), nil
}

// DecodeNote parses a stored canonical note and verifies every identity rule.
func DecodeNote(data []byte) (Note, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Note{}, err
	}
	var note Note
	if err := decodeJSONContract(data, &note); err != nil {
		return Note{}, err
	}
	if err := note.Validate(); err != nil {
		return Note{}, err
	}
	return note, nil
}

// DecodeNoteCitations parses a derived_from array from an untrusted caller.
// Each object may carry an empty segment_ids list to mean the whole trail.
func DecodeNoteCitations(data []byte) ([]NoteCitation, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var citations []NoteCitation
	if err := decodeJSONContract(data, &citations); err != nil {
		return nil, err
	}
	if citations == nil {
		citations = []NoteCitation{}
	}
	if err := validateNoteCitations(citations, false); err != nil {
		return nil, err
	}
	return citations, nil
}

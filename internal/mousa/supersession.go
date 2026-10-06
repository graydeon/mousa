package mousa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// SupersessionDeclarationSchema is the closed schema of one supersession declaration.
const SupersessionDeclarationSchema = "mousa.supersession_declaration.v1"

// Declaration field limits. Item identities share the JSONL item limit, so a declaration can pin
// any item the JSONL ingress accepts. The encoded limit covers worst-case JSON escaping at six
// bytes per input byte, which keeps a maximal declaration near 53 KiB.
const (
	MaxSupersessionItemBytes        = 4096
	MaxSupersessionAuthorBytes      = 128
	MaxSupersessionBasisBytes       = 512
	MaxSupersessionDeclarationBytes = 64 << 10
)

// SupersessionDeclarationID is the stable identity of one immutable supersession declaration.
type SupersessionDeclarationID [sha256.Size]byte

// SupersessionDeclaration records that one successor revision declares itself the replacement of
// one predecessor revision: the named source's successor item and its exact representation replace
// that source's predecessor item and its exact representation. Item labels alone do not pin
// revisions, so both item and representation identities are required.
//
// A declaration is immutable history, not an activation pointer and not an access grant. It does
// not deactivate an item, change what a source authorizes, or alter ranking, packing or retrieval,
// and it carries no relation set, conflict detection, transitive closure, cycle handling or
// freshness rule. Author and basis are untrusted labels, not signatures or factual truth.
//
// Validate checks structure only. Whether both pinned revisions exist in that source, belong to
// it, and are currently active is a later transaction-bound store check that this codec cannot
// perform.
type SupersessionDeclaration struct {
	Schema                      string                    `json:"schema"`
	ID                          SupersessionDeclarationID `json:"id"`
	SourceID                    SourceID                  `json:"source_id"`
	PredecessorItemID           string                    `json:"predecessor_item_id"`
	PredecessorRepresentationID RepresentationID          `json:"predecessor_representation_id"`
	SuccessorItemID             string                    `json:"successor_item_id"`
	SuccessorRepresentationID   RepresentationID          `json:"successor_representation_id"`
	Author                      string                    `json:"author"`
	Basis                       string                    `json:"basis"`
}

// NewSupersessionDeclarationID derives the declaration identity: the schema-domain-separated hash
// of the length-delimited ordered tuple source, predecessor item, predecessor revision, successor
// item, successor revision, author and basis. The generated identity is not an input to its own
// hash.
func NewSupersessionDeclarationID(sourceID SourceID, predecessorItemID string, predecessorRepresentationID RepresentationID, successorItemID string, successorRepresentationID RepresentationID, author, basis string) (SupersessionDeclarationID, error) {
	fields, err := supersessionDeclarationIdentityFields(sourceID, predecessorItemID, predecessorRepresentationID, successorItemID, successorRepresentationID, author, basis)
	if err != nil {
		return SupersessionDeclarationID{}, err
	}
	digest := sha256.New()
	writeTuple(digest, fields...)
	return SupersessionDeclarationID(digest.Sum(nil)), nil
}

// supersessionDeclarationIdentityFields validates every tuple field and returns them in order, so
// one validation serves both identity derivation and record validation.
func supersessionDeclarationIdentityFields(sourceID SourceID, predecessorItemID string, predecessorRepresentationID RepresentationID, successorItemID string, successorRepresentationID RepresentationID, author, basis string) ([][]byte, error) {
	if sourceID == (SourceID{}) {
		return nil, newValidationError("source_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"predecessor_item_id", predecessorItemID, MaxSupersessionItemBytes},
		{"successor_item_id", successorItemID, MaxSupersessionItemBytes},
		{"author", author, MaxSupersessionAuthorBytes},
		{"basis", basis, MaxSupersessionBasisBytes},
	} {
		if err := validateSupersessionLabel(field.name, field.value, field.limit); err != nil {
			return nil, err
		}
	}
	for _, field := range []struct {
		name string
		id   RepresentationID
	}{
		{"predecessor_representation_id", predecessorRepresentationID},
		{"successor_representation_id", successorRepresentationID},
	} {
		if field.id == (RepresentationID{}) {
			return nil, newValidationError(field.name, ValidationCodeInvalidID, "must not be zero", nil)
		}
	}
	if predecessorItemID == successorItemID {
		return nil, newValidationError("successor_item_id", ValidationCodeInvalidValue, "must differ from predecessor_item_id", nil)
	}
	if predecessorRepresentationID == successorRepresentationID {
		return nil, newValidationError("successor_representation_id", ValidationCodeInvalidValue, "must differ from predecessor_representation_id", nil)
	}
	return [][]byte{
		[]byte(SupersessionDeclarationSchema),
		sourceID[:],
		[]byte(predecessorItemID),
		predecessorRepresentationID[:],
		[]byte(successorItemID),
		successorRepresentationID[:],
		[]byte(author),
		[]byte(basis),
	}, nil
}

// validateSupersessionLabel checks one untrusted label: nonempty, bounded, valid UTF-8 without NUL.
// The bytes are hashed and encoded exactly as supplied: no trimming and no Unicode normalization.
func validateSupersessionLabel(field, value string, limit int) error {
	if value == "" {
		return newValidationError(field, ValidationCodeInvalidValue, "must not be empty", nil)
	}
	if len(value) > limit {
		return newValidationError(field, ValidationCodeInvalidRange, "exceeds the byte limit", nil)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return newValidationError(field, ValidationCodeInvalidValue, "must be valid UTF-8 without NUL", nil)
	}
	return nil
}

// Validate recomputes the declaration identity and enforces the closed record vocabulary. It
// establishes structure only, so a validated declaration is not evidence that either pinned
// revision exists, belongs to the source, or is currently active or authorized.
func (declaration SupersessionDeclaration) Validate() error {
	if declaration.Schema != SupersessionDeclarationSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.supersession_declaration.v1", nil)
	}
	if declaration.ID == (SupersessionDeclarationID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	expected, err := NewSupersessionDeclarationID(
		declaration.SourceID,
		declaration.PredecessorItemID,
		declaration.PredecessorRepresentationID,
		declaration.SuccessorItemID,
		declaration.SuccessorRepresentationID,
		declaration.Author,
		declaration.Basis,
	)
	if err != nil {
		return err
	}
	if declaration.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match declaration content", nil)
	}
	return nil
}

// EncodeSupersessionDeclaration returns the exact canonical declaration bytes.
func EncodeSupersessionDeclaration(declaration SupersessionDeclaration) ([]byte, error) {
	if err := declaration.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(declaration, "supersession declaration")
}

// DecodeSupersessionDeclaration decodes one canonical declaration payload into canonical output,
// rejecting unknown or duplicate keys, missing, null or mistyped required fields, malformed or
// zero identities, trailing values, oversized input and an identity that disagrees with the
// declaration content.
func DecodeSupersessionDeclaration(data []byte) (SupersessionDeclaration, error) {
	if len(data) > MaxSupersessionDeclarationBytes {
		return SupersessionDeclaration{}, newValidationError("", ValidationCodeInvalidRange, "must not exceed the encoded declaration byte limit", nil)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return SupersessionDeclaration{}, err
	}
	var wire struct {
		Schema                      string `json:"schema"`
		ID                          string `json:"id"`
		SourceID                    string `json:"source_id"`
		PredecessorItemID           string `json:"predecessor_item_id"`
		PredecessorRepresentationID string `json:"predecessor_representation_id"`
		SuccessorItemID             string `json:"successor_item_id"`
		SuccessorRepresentationID   string `json:"successor_representation_id"`
		Author                      string `json:"author"`
		Basis                       string `json:"basis"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return SupersessionDeclaration{}, err
	}
	if wire.Schema != SupersessionDeclarationSchema {
		return SupersessionDeclaration{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.supersession_declaration.v1", nil)
	}
	id, err := ParseSupersessionDeclarationID(wire.ID)
	if err != nil {
		return SupersessionDeclaration{}, validationErrorForField(err, "id")
	}
	sourceID, err := ParseSourceID(wire.SourceID)
	if err != nil {
		return SupersessionDeclaration{}, validationErrorForField(err, "source_id")
	}
	predecessorRepresentationID, err := ParseRepresentationID(wire.PredecessorRepresentationID)
	if err != nil {
		return SupersessionDeclaration{}, validationErrorForField(err, "predecessor_representation_id")
	}
	successorRepresentationID, err := ParseRepresentationID(wire.SuccessorRepresentationID)
	if err != nil {
		return SupersessionDeclaration{}, validationErrorForField(err, "successor_representation_id")
	}
	declaration := SupersessionDeclaration{
		Schema:                      wire.Schema,
		ID:                          id,
		SourceID:                    sourceID,
		PredecessorItemID:           wire.PredecessorItemID,
		PredecessorRepresentationID: predecessorRepresentationID,
		SuccessorItemID:             wire.SuccessorItemID,
		SuccessorRepresentationID:   successorRepresentationID,
		Author:                      wire.Author,
		Basis:                       wire.Basis,
	}
	if err := declaration.Validate(); err != nil {
		return SupersessionDeclaration{}, err
	}
	return declaration, nil
}

func (id SupersessionDeclarationID) String() string { return hex.EncodeToString(id[:]) }

// ParseSupersessionDeclarationID parses one lowercase hexadecimal declaration identity.
func ParseSupersessionDeclarationID(value string) (SupersessionDeclarationID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return SupersessionDeclarationID{}, err
	}
	return SupersessionDeclarationID(decoded), nil
}

func (id SupersessionDeclarationID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *SupersessionDeclarationID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseSupersessionDeclarationID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

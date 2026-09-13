package mousa

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"unicode/utf8"
)

type SourceID [sha256.Size]byte

type ObservationID [sha256.Size]byte

type ArtifactID [sha256.Size]byte

type RepresentationID [sha256.Size]byte

type SegmentID [sha256.Size]byte

type CallerID [sha256.Size]byte

type PurposeID [sha256.Size]byte

type SHA256 [sha256.Size]byte

type derivationInputKind string

const (
	derivationInputKindArtifact       derivationInputKind = "artifact"
	derivationInputKindRepresentation derivationInputKind = "representation"
)

type DerivationInput struct {
	kind             derivationInputKind
	artifactID       ArtifactID
	representationID RepresentationID
}

type TextByteRangeSelector struct {
	Start uint64
	End   uint64
}

type segmentSelectorKind uint8

const segmentSelectorKindTextByteRange segmentSelectorKind = 1

type SegmentSelector struct {
	kind          segmentSelectorKind
	textByteRange TextByteRangeSelector
}

func NewArtifactDerivationInput(id ArtifactID) DerivationInput {
	return DerivationInput{kind: derivationInputKindArtifact, artifactID: id}
}

func NewRepresentationDerivationInput(id RepresentationID) DerivationInput {
	return DerivationInput{kind: derivationInputKindRepresentation, representationID: id}
}

func NewTextByteRangeSelector(start, end uint64) SegmentSelector {
	return SegmentSelector{
		kind:          segmentSelectorKindTextByteRange,
		textByteRange: TextByteRangeSelector{Start: start, End: end},
	}
}

func (selector SegmentSelector) TextByteRange() (TextByteRangeSelector, bool) {
	return selector.textByteRange, selector.kind == segmentSelectorKindTextByteRange
}

func NewSourceID(namespace, externalSourceID string) (SourceID, error) {
	if err := validateIdentityInput("namespace", namespace); err != nil {
		return SourceID{}, err
	}
	if err := validateIdentityInput("external_source_id", externalSourceID); err != nil {
		return SourceID{}, err
	}
	digest := sha256.New()
	writeTuple(digest, []byte("mousa.source.v1"), []byte(namespace), []byte(externalSourceID))
	return SourceID(digest.Sum(nil)), nil
}

func NewObservationID(sourceID SourceID, externalObservationID string) (ObservationID, error) {
	if err := validateIdentityInput("external_observation_id", externalObservationID); err != nil {
		return ObservationID{}, err
	}
	digest := sha256.New()
	writeTuple(digest, []byte("mousa.observation.v1"), sourceID[:], []byte(externalObservationID))
	return ObservationID(digest.Sum(nil)), nil
}

// NewCallerID derives the caller identity from the caller schema, namespace, and external caller ID.

func NewCallerID(namespace, externalCallerID string) (CallerID, error) {
	if err := validateIdentityInput("namespace", namespace); err != nil {
		return CallerID{}, err
	}
	if err := validateIdentityInput("external_caller_id", externalCallerID); err != nil {
		return CallerID{}, err
	}
	digest := sha256.New()
	writeTuple(digest, []byte(CallerSchema), []byte(namespace), []byte(externalCallerID))
	return CallerID(digest.Sum(nil)), nil
}

// NewPurposeID derives the purpose identity from the purpose schema, namespace, and external purpose ID.
func NewPurposeID(namespace, externalPurposeID string) (PurposeID, error) {
	if err := validateIdentityInput("namespace", namespace); err != nil {
		return PurposeID{}, err
	}
	if err := validateIdentityInput("external_purpose_id", externalPurposeID); err != nil {
		return PurposeID{}, err
	}
	digest := sha256.New()
	writeTuple(digest, []byte(PurposeSchema), []byte(namespace), []byte(externalPurposeID))
	return PurposeID(digest.Sum(nil)), nil
}

func (id CallerID) String() string { return hex.EncodeToString(id[:]) }

func (id PurposeID) String() string { return hex.EncodeToString(id[:]) }

func ParseCallerID(value string) (CallerID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return CallerID{}, err
	}
	return CallerID(decoded), nil
}

func ParsePurposeID(value string) (PurposeID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return PurposeID{}, err
	}
	return PurposeID(decoded), nil
}

func (id CallerID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *CallerID) UnmarshalJSON(data []byte) error {
	decoded, err := decodeLowerHexJSON("id", data)
	if err != nil {
		return err
	}
	*id = CallerID(decoded)
	return nil
}

func (id PurposeID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *PurposeID) UnmarshalJSON(data []byte) error {
	decoded, err := decodeLowerHexJSON("id", data)
	if err != nil {
		return err
	}
	*id = PurposeID(decoded)
	return nil
}

func decodeLowerHexJSON(field string, data []byte) ([sha256.Size]byte, error) {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return [sha256.Size]byte{}, newValidationError(field, ValidationCodeInvalidID, "must be a hexadecimal string", err)
	}
	return decodeLowerHex(field, ValidationCodeInvalidID, text)
}

func NewArtifactID(observationID ObservationID, artifactKey string) (ArtifactID, error) {
	if err := validateIdentityInput("artifact_key", artifactKey); err != nil {
		return ArtifactID{}, err
	}
	digest := sha256.New()
	writeTuple(digest, []byte("mousa.artifact.v1"), observationID[:], []byte(artifactKey))
	return ArtifactID(digest.Sum(nil)), nil
}

func NewRepresentationID(inputs []DerivationInput, processorID, processorVersion string, parametersSHA256 SHA256, mediaType string, contentSHA256 SHA256) (RepresentationID, error) {
	if len(inputs) == 0 {
		return RepresentationID{}, newValidationError("inputs", ValidationCodeInvalidValue, "must not be empty", nil)
	}
	var inputCount [8]byte
	binary.BigEndian.PutUint64(inputCount[:], uint64(len(inputs)))
	fields := make([][]byte, 0, 2*len(inputs)+6)
	fields = append(fields, []byte("mousa.representation.v1"), inputCount[:])
	for index, input := range inputs {
		kind, rawID, err := input.identityFields(index)
		if err != nil {
			return RepresentationID{}, err
		}
		fields = append(fields, kind, rawID)
	}
	if err := validateIdentityInput("processor_id", processorID); err != nil {
		return RepresentationID{}, err
	}
	if err := validateIdentityInput("processor_version", processorVersion); err != nil {
		return RepresentationID{}, err
	}
	if parametersSHA256 == (SHA256{}) {
		return RepresentationID{}, newValidationError("parameters_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	if err := validateIdentityInput("media_type", mediaType); err != nil {
		return RepresentationID{}, err
	}
	if contentSHA256 == (SHA256{}) {
		return RepresentationID{}, newValidationError("content_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	fields = append(fields, []byte(processorID), []byte(processorVersion), parametersSHA256[:], []byte(mediaType), contentSHA256[:])
	digest := sha256.New()
	writeTuple(digest, fields...)
	return RepresentationID(digest.Sum(nil)), nil
}

func (input DerivationInput) identityFields(index int) ([]byte, []byte, error) {
	switch input.kind {
	case derivationInputKindArtifact:
		if input.artifactID == (ArtifactID{}) {
			return nil, nil, newValidationError(fmt.Sprintf("inputs[%d].id", index), ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(input.kind), input.artifactID[:], nil
	case derivationInputKindRepresentation:
		if input.representationID == (RepresentationID{}) {
			return nil, nil, newValidationError(fmt.Sprintf("inputs[%d].id", index), ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(input.kind), input.representationID[:], nil
	default:
		return nil, nil, newValidationError(fmt.Sprintf("inputs[%d].kind", index), ValidationCodeInvalidEnum, "must be artifact or representation", nil)
	}
}

func NewSegmentID(representationID RepresentationID, selector SegmentSelector, contentSHA256 SHA256) (SegmentID, error) {
	if representationID == (RepresentationID{}) {
		return SegmentID{}, newValidationError("representation_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	selectorSchema, coordinates, err := selector.identityFields()
	if err != nil {
		return SegmentID{}, err
	}
	if contentSHA256 == (SHA256{}) {
		return SegmentID{}, newValidationError("content_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	fields := [][]byte{[]byte("mousa.segment.v1"), representationID[:], selectorSchema}
	fields = append(fields, coordinates...)
	fields = append(fields, contentSHA256[:])
	digest := sha256.New()
	writeTuple(digest, fields...)
	return SegmentID(digest.Sum(nil)), nil
}

func (selector SegmentSelector) identityFields() ([]byte, [][]byte, error) {
	if selector.kind != segmentSelectorKindTextByteRange {
		return nil, nil, newValidationError("selector.schema", ValidationCodeUnsupportedSelector, "unsupported selector schema", nil)
	}
	if selector.textByteRange.Start >= selector.textByteRange.End {
		return nil, nil, newValidationError("selector.end", ValidationCodeInvalidRange, "must be greater than start", nil)
	}
	var start, end [8]byte
	binary.BigEndian.PutUint64(start[:], selector.textByteRange.Start)
	binary.BigEndian.PutUint64(end[:], selector.textByteRange.End)
	return []byte("mousa.selector.text_byte_range.v1"), [][]byte{start[:], end[:]}, nil
}

func (id SourceID) String() string {
	return hex.EncodeToString(id[:])
}

func (id ObservationID) String() string {
	return hex.EncodeToString(id[:])
}

func (id ArtifactID) String() string {
	return hex.EncodeToString(id[:])
}

func (id RepresentationID) String() string {
	return hex.EncodeToString(id[:])
}

func (id SegmentID) String() string {
	return hex.EncodeToString(id[:])
}

func (digest SHA256) String() string {
	return hex.EncodeToString(digest[:])
}

func ParseSourceID(value string) (SourceID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return SourceID{}, err
	}
	return SourceID(decoded), nil
}

func ParseObservationID(value string) (ObservationID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return ObservationID{}, err
	}
	return ObservationID(decoded), nil
}

func ParseArtifactID(value string) (ArtifactID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return ArtifactID{}, err
	}
	return ArtifactID(decoded), nil
}

func ParseRepresentationID(value string) (RepresentationID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return RepresentationID{}, err
	}
	return RepresentationID(decoded), nil
}

func ParseSegmentID(value string) (SegmentID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return SegmentID{}, err
	}
	return SegmentID(decoded), nil
}

func (id SourceID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *SourceID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseSourceID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id ObservationID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *ObservationID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseObservationID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id ArtifactID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *ArtifactID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseArtifactID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id RepresentationID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *RepresentationID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseRepresentationID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id SegmentID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *SegmentID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseSegmentID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func ParseSHA256(value string) (SHA256, error) {
	decoded, err := decodeLowerHex("sha256", ValidationCodeInvalidDigest, value)
	if err != nil {
		return SHA256{}, err
	}
	return SHA256(decoded), nil
}

func (digest SHA256) MarshalJSON() ([]byte, error) {
	return json.Marshal(digest.String())
}

func (digest *SHA256) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("sha256", ValidationCodeInvalidDigest, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseSHA256(value)
	if err != nil {
		return err
	}
	*digest = parsed
	return nil
}

func decodeLowerHex(field string, code ValidationCode, value string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	if len(value) != hex.EncodedLen(len(result)) {
		return result, newValidationError(field, code, "must contain 64 lowercase hexadecimal characters", nil)
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return result, newValidationError(field, code, "must contain 64 lowercase hexadecimal characters", nil)
		}
	}
	if _, err := hex.Decode(result[:], []byte(value)); err != nil {
		return result, newValidationError(field, code, "must contain 64 lowercase hexadecimal characters", fmt.Errorf("decode hexadecimal value: %w", err))
	}
	return result, nil
}

func validateIdentityInput(field, value string) error {
	if value == "" {
		return newValidationError(field, ValidationCodeInvalidValue, "must not be empty", nil)
	}
	if !utf8.ValidString(value) {
		return newValidationError(field, ValidationCodeInvalidValue, "must be valid UTF-8", nil)
	}
	return nil
}

func writeTuple(digest hash.Hash, fields ...[]byte) {
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write(field)
	}
}

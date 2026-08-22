package mousa

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

type ValidationCode string

const (
	ValidationCodeInvalidJSON   ValidationCode = "invalid_json"
	ValidationCodeUnknownField  ValidationCode = "unknown_field"
	ValidationCodeInvalidSchema ValidationCode = "invalid_schema"
	ValidationCodeInvalidDigest ValidationCode = "invalid_digest"
	ValidationCodeInvalidID     ValidationCode = "invalid_id"
	ValidationCodeInvalidEnum   ValidationCode = "invalid_enum"
	ValidationCodeInvalidRange  ValidationCode = "invalid_range"
	ValidationCodeInvalidValue  ValidationCode = "invalid_value"
	ValidationCodeTrailingData  ValidationCode = "trailing_data"
)

const ValidationCodeUnsupportedSelector ValidationCode = "unsupported_selector"

const (
	SourceSchema      = "mousa.source.v1"
	ObservationSchema = "mousa.observation.v1"
)

const (
	ArtifactSchema              = "mousa.artifact.v1"
	RepresentationSchema        = "mousa.representation.v1"
	SegmentSchema               = "mousa.segment.v1"
	TextByteRangeSelectorSchema = "mousa.selector.text_byte_range.v1"
	UTF8TextMediaType           = "text/plain; charset=utf-8"
)

type Source struct {
	Schema           string   `json:"schema"`
	ID               SourceID `json:"id"`
	Namespace        string   `json:"namespace"`
	ExternalSourceID string   `json:"external_source_id"`
}

type Observation struct {
	Schema                string        `json:"schema"`
	ID                    ObservationID `json:"id"`
	SourceID              SourceID      `json:"source_id"`
	ExternalObservationID string        `json:"external_observation_id"`
}

type Artifact struct {
	Schema        string        `json:"schema"`
	ID            ArtifactID    `json:"id"`
	ObservationID ObservationID `json:"observation_id"`
	ArtifactKey   string        `json:"artifact_key"`
	MediaType     string        `json:"media_type"`
	ContentSHA256 SHA256        `json:"content_sha256"`
	ByteLength    uint64        `json:"byte_length"`
}

type Representation struct {
	Schema           string            `json:"schema"`
	ID               RepresentationID  `json:"id"`
	Inputs           []DerivationInput `json:"inputs"`
	ProcessorID      string            `json:"processor_id"`
	ProcessorVersion string            `json:"processor_version"`
	ParametersSHA256 SHA256            `json:"parameters_sha256"`
	MediaType        string            `json:"media_type"`
	ContentSHA256    SHA256            `json:"content_sha256"`
	ByteLength       uint64            `json:"byte_length"`
}

type Segment struct {
	Schema           string           `json:"schema"`
	ID               SegmentID        `json:"id"`
	RepresentationID RepresentationID `json:"representation_id"`
	Selector         SegmentSelector  `json:"selector"`
	ContentSHA256    SHA256           `json:"content_sha256"`
}

func (input DerivationInput) MarshalJSON() ([]byte, error) {
	var id string
	switch input.kind {
	case derivationInputKindArtifact:
		id = input.artifactID.String()
	case derivationInputKindRepresentation:
		id = input.representationID.String()
	default:
		return nil, newValidationError("kind", ValidationCodeInvalidEnum, "must be artifact or representation", nil)
	}
	return json.Marshal(struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{Kind: string(input.kind), ID: id})
}

func (selector SegmentSelector) MarshalJSON() ([]byte, error) {
	if selector.kind != segmentSelectorKindTextByteRange {
		return nil, newValidationError("schema", ValidationCodeUnsupportedSelector, "unsupported selector schema", nil)
	}
	return json.Marshal(struct {
		Schema string `json:"schema"`
		Start  uint64 `json:"start"`
		End    uint64 `json:"end"`
	}{
		Schema: TextByteRangeSelectorSchema,
		Start:  selector.textByteRange.Start,
		End:    selector.textByteRange.End,
	})
}

type ValidationError struct {
	Field   string
	Code    ValidationCode
	Message string
	cause   error
}

func (err *ValidationError) Error() string {
	if err.Field == "" {
		return err.Message
	}
	return fmt.Sprintf("%s: %s", err.Field, err.Message)
}

func (err *ValidationError) Unwrap() error {
	return err.cause
}

func newValidationError(field string, code ValidationCode, message string, cause error) *ValidationError {
	return &ValidationError{Field: field, Code: code, Message: message, cause: cause}
}

func (source Source) Validate() error {
	if source.Schema != SourceSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.source.v1", nil)
	}
	if source.ID == (SourceID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if err := validateIdentityInput("namespace", source.Namespace); err != nil {
		return err
	}
	if err := validateIdentityInput("external_source_id", source.ExternalSourceID); err != nil {
		return err
	}
	expected, err := NewSourceID(source.Namespace, source.ExternalSourceID)
	if err != nil {
		return err
	}
	if source.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match namespace and external_source_id", nil)
	}
	return nil
}

func (observation Observation) Validate() error {
	if observation.Schema != ObservationSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.observation.v1", nil)
	}
	if observation.ID == (ObservationID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if observation.SourceID == (SourceID{}) {
		return newValidationError("source_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if err := validateIdentityInput("external_observation_id", observation.ExternalObservationID); err != nil {
		return err
	}
	expected, err := NewObservationID(observation.SourceID, observation.ExternalObservationID)
	if err != nil {
		return err
	}
	if observation.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match source_id and external_observation_id", nil)
	}
	return nil
}

func (artifact Artifact) Validate() error {
	if artifact.Schema != ArtifactSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.artifact.v1", nil)
	}
	if artifact.ID == (ArtifactID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if artifact.ObservationID == (ObservationID{}) {
		return newValidationError("observation_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if err := validateIdentityInput("artifact_key", artifact.ArtifactKey); err != nil {
		return err
	}
	if err := validateIdentityInput("media_type", artifact.MediaType); err != nil {
		return err
	}
	if artifact.ContentSHA256 == (SHA256{}) {
		return newValidationError("content_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	expected, err := NewArtifactID(artifact.ObservationID, artifact.ArtifactKey)
	if err != nil {
		return err
	}
	if artifact.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match observation_id and artifact_key", nil)
	}
	return nil
}

func (representation Representation) Validate() error {
	if representation.Schema != RepresentationSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.representation.v1", nil)
	}
	if representation.ID == (RepresentationID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if len(representation.Inputs) == 0 {
		return newValidationError("inputs", ValidationCodeInvalidValue, "must not be empty", nil)
	}
	for index, input := range representation.Inputs {
		if _, _, err := input.identityFields(index); err != nil {
			return err
		}
	}
	if err := validateIdentityInput("processor_id", representation.ProcessorID); err != nil {
		return err
	}
	if err := validateIdentityInput("processor_version", representation.ProcessorVersion); err != nil {
		return err
	}
	if representation.ParametersSHA256 == (SHA256{}) {
		return newValidationError("parameters_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	if err := validateIdentityInput("media_type", representation.MediaType); err != nil {
		return err
	}
	if representation.ContentSHA256 == (SHA256{}) {
		return newValidationError("content_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	expected, err := NewRepresentationID(
		representation.Inputs,
		representation.ProcessorID,
		representation.ProcessorVersion,
		representation.ParametersSHA256,
		representation.MediaType,
		representation.ContentSHA256,
	)
	if err != nil {
		return err
	}
	if representation.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match derivation evidence", nil)
	}
	return nil
}

func (segment Segment) Validate() error {
	if segment.Schema != SegmentSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.segment.v1", nil)
	}
	if segment.ID == (SegmentID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if segment.RepresentationID == (RepresentationID{}) {
		return newValidationError("representation_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if _, _, err := segment.Selector.identityFields(); err != nil {
		return err
	}
	if segment.ContentSHA256 == (SHA256{}) {
		return newValidationError("content_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	expected, err := NewSegmentID(segment.RepresentationID, segment.Selector, segment.ContentSHA256)
	if err != nil {
		return err
	}
	if segment.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match representation, selector, and digest", nil)
	}
	return nil
}

func (segment Segment) ValidateAgainst(representation Representation) error {
	if err := segment.Validate(); err != nil {
		return err
	}
	if err := representation.Validate(); err != nil {
		return err
	}
	if segment.RepresentationID != representation.ID {
		return newValidationError("representation_id", ValidationCodeInvalidID, "does not match parent representation", nil)
	}
	if representation.MediaType != UTF8TextMediaType {
		return newValidationError("selector.schema", ValidationCodeUnsupportedSelector, "selector is not supported for parent media type", nil)
	}
	textByteRange, ok := segment.Selector.TextByteRange()
	if !ok {
		return newValidationError("selector.schema", ValidationCodeUnsupportedSelector, "unsupported selector schema", nil)
	}
	if textByteRange.End > representation.ByteLength {
		return newValidationError("selector.end", ValidationCodeInvalidRange, "must not exceed parent byte length", nil)
	}
	return nil
}

func EncodeSource(source Source) ([]byte, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(source, "source")
}

func DecodeSource(data []byte) (Source, error) {
	var wire struct {
		Schema           string `json:"schema"`
		ID               string `json:"id"`
		Namespace        string `json:"namespace"`
		ExternalSourceID string `json:"external_source_id"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return Source{}, err
	}
	id, err := ParseSourceID(wire.ID)
	if err != nil {
		return Source{}, validationErrorForField(err, "id")
	}
	source := Source{Schema: wire.Schema, ID: id, Namespace: wire.Namespace, ExternalSourceID: wire.ExternalSourceID}
	if err := source.Validate(); err != nil {
		return Source{}, err
	}
	return source, nil
}

func EncodeObservation(observation Observation) ([]byte, error) {
	if err := observation.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(observation, "observation")
}

func DecodeObservation(data []byte) (Observation, error) {
	var wire struct {
		Schema                string `json:"schema"`
		ID                    string `json:"id"`
		SourceID              string `json:"source_id"`
		ExternalObservationID string `json:"external_observation_id"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return Observation{}, err
	}
	id, err := ParseObservationID(wire.ID)
	if err != nil {
		return Observation{}, validationErrorForField(err, "id")
	}
	sourceID, err := ParseSourceID(wire.SourceID)
	if err != nil {
		return Observation{}, validationErrorForField(err, "source_id")
	}
	observation := Observation{Schema: wire.Schema, ID: id, SourceID: sourceID, ExternalObservationID: wire.ExternalObservationID}
	if err := observation.Validate(); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func EncodeArtifact(artifact Artifact) ([]byte, error) {
	if err := artifact.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(artifact, "artifact")
}

func DecodeArtifact(data []byte) (Artifact, error) {
	var wire struct {
		Schema        string `json:"schema"`
		ID            string `json:"id"`
		ObservationID string `json:"observation_id"`
		ArtifactKey   string `json:"artifact_key"`
		MediaType     string `json:"media_type"`
		ContentSHA256 string `json:"content_sha256"`
		ByteLength    uint64 `json:"byte_length"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return Artifact{}, err
	}
	if wire.Schema != ArtifactSchema {
		return Artifact{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.artifact.v1", nil)
	}
	id, err := ParseArtifactID(wire.ID)
	if err != nil {
		return Artifact{}, validationErrorForField(err, "id")
	}
	if id == (ArtifactID{}) {
		return Artifact{}, newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	observationID, err := ParseObservationID(wire.ObservationID)
	if err != nil {
		return Artifact{}, validationErrorForField(err, "observation_id")
	}
	if observationID == (ObservationID{}) {
		return Artifact{}, newValidationError("observation_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if err := validateIdentityInput("artifact_key", wire.ArtifactKey); err != nil {
		return Artifact{}, err
	}
	if err := validateIdentityInput("media_type", wire.MediaType); err != nil {
		return Artifact{}, err
	}
	contentSHA256, err := ParseSHA256(wire.ContentSHA256)
	if err != nil {
		return Artifact{}, validationErrorForField(err, "content_sha256")
	}
	if contentSHA256 == (SHA256{}) {
		return Artifact{}, newValidationError("content_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	artifact := Artifact{
		Schema:        wire.Schema,
		ID:            id,
		ObservationID: observationID,
		ArtifactKey:   wire.ArtifactKey,
		MediaType:     wire.MediaType,
		ContentSHA256: contentSHA256,
		ByteLength:    wire.ByteLength,
	}
	if err := artifact.Validate(); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func EncodeRepresentation(representation Representation) ([]byte, error) {
	if err := representation.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(representation, "representation")
}

func DecodeRepresentation(data []byte) (Representation, error) {
	var wire struct {
		Schema           string            `json:"schema"`
		ID               string            `json:"id"`
		Inputs           []json.RawMessage `json:"inputs"`
		ProcessorID      string            `json:"processor_id"`
		ProcessorVersion string            `json:"processor_version"`
		ParametersSHA256 string            `json:"parameters_sha256"`
		MediaType        string            `json:"media_type"`
		ContentSHA256    string            `json:"content_sha256"`
		ByteLength       uint64            `json:"byte_length"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return Representation{}, err
	}
	if wire.Schema != RepresentationSchema {
		return Representation{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.representation.v1", nil)
	}
	id, err := ParseRepresentationID(wire.ID)
	if err != nil {
		return Representation{}, validationErrorForField(err, "id")
	}
	if id == (RepresentationID{}) {
		return Representation{}, newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if len(wire.Inputs) == 0 {
		return Representation{}, newValidationError("inputs", ValidationCodeInvalidValue, "must not be empty", nil)
	}
	inputs := make([]DerivationInput, len(wire.Inputs))
	for index, raw := range wire.Inputs {
		input, err := decodeDerivationInput(raw, index)
		if err != nil {
			return Representation{}, err
		}
		inputs[index] = input
	}
	if err := validateIdentityInput("processor_id", wire.ProcessorID); err != nil {
		return Representation{}, err
	}
	if err := validateIdentityInput("processor_version", wire.ProcessorVersion); err != nil {
		return Representation{}, err
	}
	parametersSHA256, err := ParseSHA256(wire.ParametersSHA256)
	if err != nil {
		return Representation{}, validationErrorForField(err, "parameters_sha256")
	}
	if parametersSHA256 == (SHA256{}) {
		return Representation{}, newValidationError("parameters_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	if err := validateIdentityInput("media_type", wire.MediaType); err != nil {
		return Representation{}, err
	}
	contentSHA256, err := ParseSHA256(wire.ContentSHA256)
	if err != nil {
		return Representation{}, validationErrorForField(err, "content_sha256")
	}
	if contentSHA256 == (SHA256{}) {
		return Representation{}, newValidationError("content_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	representation := Representation{
		Schema:           wire.Schema,
		ID:               id,
		Inputs:           inputs,
		ProcessorID:      wire.ProcessorID,
		ProcessorVersion: wire.ProcessorVersion,
		ParametersSHA256: parametersSHA256,
		MediaType:        wire.MediaType,
		ContentSHA256:    contentSHA256,
		ByteLength:       wire.ByteLength,
	}
	if err := representation.Validate(); err != nil {
		return Representation{}, err
	}
	return representation, nil
}

func decodeDerivationInput(data []byte, index int) (DerivationInput, error) {
	var wire struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return DerivationInput{}, prefixValidationError(err, fmt.Sprintf("inputs[%d]", index))
	}
	switch wire.Kind {
	case string(derivationInputKindArtifact):
		id, err := ParseArtifactID(wire.ID)
		if err != nil {
			return DerivationInput{}, validationErrorForField(err, fmt.Sprintf("inputs[%d].id", index))
		}
		if id == (ArtifactID{}) {
			return DerivationInput{}, newValidationError(fmt.Sprintf("inputs[%d].id", index), ValidationCodeInvalidID, "must not be zero", nil)
		}
		return NewArtifactDerivationInput(id), nil
	case string(derivationInputKindRepresentation):
		id, err := ParseRepresentationID(wire.ID)
		if err != nil {
			return DerivationInput{}, validationErrorForField(err, fmt.Sprintf("inputs[%d].id", index))
		}
		if id == (RepresentationID{}) {
			return DerivationInput{}, newValidationError(fmt.Sprintf("inputs[%d].id", index), ValidationCodeInvalidID, "must not be zero", nil)
		}
		return NewRepresentationDerivationInput(id), nil
	default:
		return DerivationInput{}, newValidationError(fmt.Sprintf("inputs[%d].kind", index), ValidationCodeInvalidEnum, "must be artifact or representation", nil)
	}
}

func prefixValidationError(err error, prefix string) error {
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		return err
	}
	field := prefix
	if validationErr.Field != "" {
		field += "." + validationErr.Field
	}
	return newValidationError(field, validationErr.Code, validationErr.Message, validationErr.cause)
}

func EncodeSegment(segment Segment) ([]byte, error) {
	if err := segment.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(segment, "segment")
}

func DecodeSegment(data []byte) (Segment, error) {
	var wire struct {
		Schema           string          `json:"schema"`
		ID               string          `json:"id"`
		RepresentationID string          `json:"representation_id"`
		Selector         json.RawMessage `json:"selector"`
		ContentSHA256    string          `json:"content_sha256"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return Segment{}, err
	}
	if wire.Schema != SegmentSchema {
		return Segment{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.segment.v1", nil)
	}
	id, err := ParseSegmentID(wire.ID)
	if err != nil {
		return Segment{}, validationErrorForField(err, "id")
	}
	if id == (SegmentID{}) {
		return Segment{}, newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	representationID, err := ParseRepresentationID(wire.RepresentationID)
	if err != nil {
		return Segment{}, validationErrorForField(err, "representation_id")
	}
	if representationID == (RepresentationID{}) {
		return Segment{}, newValidationError("representation_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	selector, err := decodeSegmentSelector(wire.Selector)
	if err != nil {
		return Segment{}, err
	}
	contentSHA256, err := ParseSHA256(wire.ContentSHA256)
	if err != nil {
		return Segment{}, validationErrorForField(err, "content_sha256")
	}
	if contentSHA256 == (SHA256{}) {
		return Segment{}, newValidationError("content_sha256", ValidationCodeInvalidDigest, "must not be zero", nil)
	}
	segment := Segment{
		Schema:           wire.Schema,
		ID:               id,
		RepresentationID: representationID,
		Selector:         selector,
		ContentSHA256:    contentSHA256,
	}
	if err := segment.Validate(); err != nil {
		return Segment{}, err
	}
	return segment, nil
}

func decodeSegmentSelector(data []byte) (SegmentSelector, error) {
	var wire struct {
		Schema string `json:"schema"`
		Start  uint64 `json:"start"`
		End    uint64 `json:"end"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return SegmentSelector{}, prefixValidationError(err, "selector")
	}
	if wire.Schema != TextByteRangeSelectorSchema {
		return SegmentSelector{}, newValidationError("selector.schema", ValidationCodeUnsupportedSelector, "unsupported selector schema", nil)
	}
	selector := NewTextByteRangeSelector(wire.Start, wire.End)
	if _, _, err := selector.identityFields(); err != nil {
		return SegmentSelector{}, err
	}
	return selector, nil
}

func encodeJSONContract(value any, name string) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, newValidationError("", ValidationCodeInvalidJSON, "could not encode "+name, err)
	}
	return buffer.Bytes(), nil
}

func decodeJSONContract(data []byte, destination any) error {
	if !utf8.Valid(data) {
		return newValidationError("", ValidationCodeInvalidJSON, "must be valid UTF-8 JSON", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var validationErr *ValidationError
		if errors.As(err, &validationErr) {
			return validationErr
		}
		const prefix = "json: unknown field \""
		if strings.HasPrefix(err.Error(), prefix) && strings.HasSuffix(err.Error(), "\"") {
			field := strings.TrimSuffix(strings.TrimPrefix(err.Error(), prefix), "\"")
			return newValidationError(field, ValidationCodeUnknownField, "field is not allowed", err)
		}
		field := ""
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			field = typeErr.Field
		}
		return newValidationError(field, ValidationCodeInvalidJSON, "invalid JSON value", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return newValidationError("", ValidationCodeTrailingData, "must contain exactly one JSON value", nil)
		}
		return newValidationError("", ValidationCodeInvalidJSON, "invalid trailing JSON data", err)
	}
	return nil
}

func validationErrorForField(err error, field string) error {
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		return err
	}
	return newValidationError(field, validationErr.Code, validationErr.Message, validationErr.cause)
}

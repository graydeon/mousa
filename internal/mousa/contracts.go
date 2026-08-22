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

const (
	SourceSchema      = "mousa.source.v1"
	ObservationSchema = "mousa.observation.v1"
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

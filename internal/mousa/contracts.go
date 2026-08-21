package mousa

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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

type DocumentStatus string

const (
	DocumentStatusActive     DocumentStatus = "active"
	DocumentStatusStale      DocumentStatus = "stale"
	DocumentStatusSuperseded DocumentStatus = "superseded"
	DocumentStatusDeleted    DocumentStatus = "deleted"
)

type Authority string

const (
	AuthorityCanonical Authority = "canonical"
	AuthorityDerived   Authority = "derived"
	AuthorityAdvisory  Authority = "advisory"
	AuthorityUnknown   Authority = "unknown"
)

type Sensitivity string

const (
	SensitivityPublic       Sensitivity = "public"
	SensitivityInternal     Sensitivity = "internal"
	SensitivityPrivate      Sensitivity = "private"
	SensitivityRestricted   Sensitivity = "restricted"
	SensitivityNonIndexable Sensitivity = "non_indexable"
)

const (
	SourceDocumentSchema = "mousa.source_document.v1"
	ChunkSchema          = "mousa.chunk.v1"
)

type SourceDocument struct {
	Schema                  string         `json:"schema"`
	ID                      DocumentID     `json:"id"`
	SourceID                string         `json:"source_id"`
	URI                     string         `json:"uri,omitempty"`
	Title                   string         `json:"title,omitempty"`
	ContentType             string         `json:"content_type"`
	NormalizedContentSHA256 SHA256         `json:"normalized_content_sha256"`
	Status                  DocumentStatus `json:"status"`
	Authority               Authority      `json:"authority"`
	Sensitivity             Sensitivity    `json:"sensitivity"`
	Supersedes              *DocumentID    `json:"supersedes,omitempty"`
}

type Chunk struct {
	Schema      string         `json:"schema"`
	ID          ChunkID        `json:"id"`
	DocumentID  DocumentID     `json:"document_id"`
	Ordinal     uint64         `json:"ordinal"`
	StartByte   uint64         `json:"start_byte"`
	EndByte     uint64         `json:"end_byte"`
	TextSHA256  SHA256         `json:"text_sha256"`
	Text        string         `json:"text"`
	Status      DocumentStatus `json:"status"`
	Authority   Authority      `json:"authority"`
	Sensitivity Sensitivity    `json:"sensitivity"`
	Indexable   bool           `json:"indexable"`
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

func (document SourceDocument) Validate() error {
	if document.Schema != SourceDocumentSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.source_document.v1", nil)
	}
	if document.SourceID == "" {
		return newValidationError("source_id", ValidationCodeInvalidValue, "must not be empty", nil)
	}
	if document.ContentType == "" {
		return newValidationError("content_type", ValidationCodeInvalidValue, "must not be empty", nil)
	}
	if err := document.Status.validate(); err != nil {
		return err
	}
	if err := document.Authority.validate(); err != nil {
		return err
	}
	if err := document.Sensitivity.validate(); err != nil {
		return err
	}
	if expected := NewDocumentID(document.SourceID, document.NormalizedContentSHA256); document.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match source_id and normalized_content_sha256", nil)
	}
	return nil
}

func EncodeSourceDocument(document SourceDocument) ([]byte, error) {
	if err := document.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(document, "source document")
}

func DecodeSourceDocument(data []byte) (SourceDocument, error) {
	var document SourceDocument
	if err := decodeJSONContract(data, &document); err != nil {
		return SourceDocument{}, err
	}
	if err := document.Validate(); err != nil {
		return SourceDocument{}, err
	}
	return document, nil
}

func (chunk Chunk) Validate() error {
	if chunk.Schema != ChunkSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.chunk.v1", nil)
	}
	if err := chunk.Status.validate(); err != nil {
		return err
	}
	if err := chunk.Authority.validate(); err != nil {
		return err
	}
	if err := chunk.Sensitivity.validate(); err != nil {
		return err
	}
	expected, err := NewChunkID(chunk.DocumentID, chunk.Ordinal, chunk.StartByte, chunk.EndByte, chunk.TextSHA256)
	if err != nil {
		return err
	}
	if chunk.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match document_id, location, and text_sha256", nil)
	}
	if chunk.Sensitivity == SensitivityNonIndexable && chunk.Indexable {
		return newValidationError("indexable", ValidationCodeInvalidValue, "must be false for non_indexable sensitivity", nil)
	}
	return nil
}

func EncodeChunk(chunk Chunk) ([]byte, error) {
	if err := chunk.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(chunk, "chunk")
}

func DecodeChunk(data []byte) (Chunk, error) {
	var chunk Chunk
	if err := decodeJSONContract(data, &chunk); err != nil {
		return Chunk{}, err
	}
	if err := chunk.Validate(); err != nil {
		return Chunk{}, err
	}
	return chunk, nil
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

func (value DocumentStatus) validate() error {
	switch value {
	case DocumentStatusActive, DocumentStatusStale, DocumentStatusSuperseded, DocumentStatusDeleted:
		return nil
	default:
		return newValidationError("status", ValidationCodeInvalidEnum, "must be a known document status", nil)
	}
}

func (value *DocumentStatus) UnmarshalJSON(data []byte) error {
	var decoded string
	if err := json.Unmarshal(data, &decoded); err != nil {
		return newValidationError("status", ValidationCodeInvalidEnum, "must be a known document status", err)
	}
	candidate := DocumentStatus(decoded)
	if err := candidate.validate(); err != nil {
		return err
	}
	*value = candidate
	return nil
}

func (value Authority) validate() error {
	switch value {
	case AuthorityCanonical, AuthorityDerived, AuthorityAdvisory, AuthorityUnknown:
		return nil
	default:
		return newValidationError("authority", ValidationCodeInvalidEnum, "must be a known authority", nil)
	}
}

func (value *Authority) UnmarshalJSON(data []byte) error {
	var decoded string
	if err := json.Unmarshal(data, &decoded); err != nil {
		return newValidationError("authority", ValidationCodeInvalidEnum, "must be a known authority", err)
	}
	candidate := Authority(decoded)
	if err := candidate.validate(); err != nil {
		return err
	}
	*value = candidate
	return nil
}

func (value Sensitivity) validate() error {
	switch value {
	case SensitivityPublic, SensitivityInternal, SensitivityPrivate, SensitivityRestricted, SensitivityNonIndexable:
		return nil
	default:
		return newValidationError("sensitivity", ValidationCodeInvalidEnum, "must be a known sensitivity", nil)
	}
}

func (value *Sensitivity) UnmarshalJSON(data []byte) error {
	var decoded string
	if err := json.Unmarshal(data, &decoded); err != nil {
		return newValidationError("sensitivity", ValidationCodeInvalidEnum, "must be a known sensitivity", err)
	}
	candidate := Sensitivity(decoded)
	if err := candidate.validate(); err != nil {
		return err
	}
	*value = candidate
	return nil
}

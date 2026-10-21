package mousa

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"
)

const (
	VectorSpaceSchema   = "mousa.vector_space.v1"
	VectorImportSchema  = "mousa.vector_import.v1"
	VectorQuerySchema   = "mousa.vector_query.v1"
	MaxVectorDimensions = 2048
	MaxVectorPassages   = 100
	MaxVectorTextBytes  = 256 << 10
	MaxVectorInputBytes = 8 << 20
)

// VectorSpace is a caller declaration, not attestation of an embedding producer.
// Every descriptor field participates in its identity; dimension alone is not a space.
type VectorSpace struct {
	Schema     string `json:"schema"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Version    string `json:"version"`
	Dimensions int    `json:"dimensions"`
	Metric     string `json:"metric"`
}

func (space VectorSpace) Validate() error {
	if space.Schema != VectorSpaceSchema || space.Metric != "cosine" {
		return errors.New("vector space requires mousa.vector_space.v1 and metric cosine")
	}
	for _, s := range []string{space.Provider, space.Model, space.Version} {
		if !utf8.ValidString(s) || len(s) == 0 || len(s) > 256 {
			return errors.New("vector provider, model and version require 1..256 UTF-8 bytes")
		}
	}
	if space.Dimensions < 1 || space.Dimensions > MaxVectorDimensions {
		return fmt.Errorf("vector dimensions must be 1..%d", MaxVectorDimensions)
	}
	return nil
}

func (space VectorSpace) Identity() (SHA256, error) {
	if err := space.Validate(); err != nil {
		return SHA256{}, err
	}
	data, err := encodeJSONContract(space, "vector space")
	return SHA256(sha256.Sum256(data)), err
}

// EmbeddingInput pins exactly the normalized passage consumed by the external producer.
type EmbeddingInput struct {
	SourceID             SourceID         `json:"source_id"`
	SegmentID            SegmentID        `json:"segment_id"`
	RepresentationID     RepresentationID `json:"representation_id"`
	RepresentationSHA256 SHA256           `json:"representation_sha256"`
	ContentSHA256        SHA256           `json:"content_sha256"`
}

type SuppliedEmbedding struct {
	EmbeddingInput
	Values []float64 `json:"values"`
}

// Embedding is immutable for a source/segment/space. Different values require a new space.
type Embedding struct {
	ID      SHA256 `json:"id"`
	SpaceID SHA256 `json:"space_id"`
	SuppliedEmbedding
}

func NewEmbedding(space VectorSpace, supplied SuppliedEmbedding) (Embedding, error) {
	id, err := space.Identity()
	if err != nil {
		return Embedding{}, err
	}
	if supplied.SourceID == (SourceID{}) || supplied.SegmentID == (SegmentID{}) || supplied.RepresentationID == (RepresentationID{}) || supplied.RepresentationSHA256 == (SHA256{}) || supplied.ContentSHA256 == (SHA256{}) {
		return Embedding{}, errors.New("embedding requires nonzero source, segment, representation and content pins")
	}
	if err := ValidateVector(supplied.Values, space.Dimensions); err != nil {
		return Embedding{}, err
	}
	data, err := encodeJSONContract(supplied, "embedding")
	if err != nil {
		return Embedding{}, err
	}
	digest := sha256.New()
	writeTuple(digest, []byte("mousa.embedding.v1"), id[:], data)
	return Embedding{ID: SHA256(digest.Sum(nil)), SpaceID: id, SuppliedEmbedding: supplied}, nil
}

type VectorImport struct {
	Schema     string              `json:"schema"`
	Space      VectorSpace         `json:"space"`
	Embeddings []SuppliedEmbedding `json:"embeddings"`
}

func DecodeVectorImport(data []byte) (VectorImport, error) {
	var value VectorImport
	if len(data) > MaxVectorInputBytes {
		return value, errors.New("vector import exceeds 8 MiB")
	}
	object, err := vectorObject(data, "schema", "space", "embeddings")
	if err != nil {
		return value, err
	}
	if _, err := vectorObject(object["space"], "schema", "provider", "model", "version", "dimensions", "metric"); err != nil {
		return value, err
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(object["embeddings"], &entries); err != nil {
		return value, err
	}
	if len(entries) < 1 || len(entries) > MaxVectorPassages {
		return value, errors.New("import requires 1..100 embeddings")
	}
	for _, entry := range entries {
		embedding, err := vectorObject(entry, "source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256", "values")
		if err != nil {
			return value, err
		}
		if err := vectorJSONNumbers(embedding["values"]); err != nil {
			return value, err
		}
	}
	if err := decodeStrictVectorJSON(data, &value); err != nil {
		return value, err
	}
	return value, value.Validate()
}

func (value VectorImport) Validate() error {
	if value.Schema != VectorImportSchema {
		return errors.New("unsupported vector import schema")
	}
	if err := value.Space.Validate(); err != nil {
		return err
	}
	if len(value.Embeddings) == 0 || len(value.Embeddings) > MaxVectorPassages {
		return errors.New("import requires 1..100 embeddings")
	}
	seen := make(map[SegmentID]bool, len(value.Embeddings))
	for _, supplied := range value.Embeddings {
		if seen[supplied.SegmentID] {
			return errors.New("duplicate embedding segment")
		}
		seen[supplied.SegmentID] = true
		if _, err := NewEmbedding(value.Space, supplied); err != nil {
			return err
		}
	}
	return nil
}

type VectorQuery struct {
	Schema string    `json:"schema"`
	Values []float64 `json:"values"`
}

func DecodeVectorQuery(data []byte) (VectorQuery, error) {
	var value VectorQuery
	if len(data) > 128<<10 {
		return value, errors.New("query vector exceeds 128 KiB")
	}
	object, err := vectorObject(data, "schema", "values")
	if err != nil {
		return value, err
	}
	if err := vectorJSONNumbers(object["values"]); err != nil {
		return value, err
	}
	if err := decodeStrictVectorJSON(data, &value); err != nil {
		return value, err
	}
	if value.Schema != VectorQuerySchema {
		return value, errors.New("unsupported query vector schema")
	}
	return value, ValidateVector(value.Values, len(value.Values))
}

// vectorObject requires exact member spelling, presence and non-null values.
// encoding/json alone accepts case-insensitive field names and null scalars.
func vectorObject(data []byte, keys ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := decodeStrictVectorJSON(data, &object); err != nil {
		return nil, err
	}
	if len(object) != len(keys) {
		return nil, errors.New("vector object has missing or unknown members")
	}
	for _, key := range keys {
		raw, exists := object[key]
		if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, fmt.Errorf("vector member %s is required and cannot be null", key)
		}
	}
	return object, nil
}

func decodeStrictVectorJSON(data []byte, value any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	return decodeJSONContract(data, value)
}

func vectorJSONNumbers(data []byte) error {
	var values []json.RawMessage
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	if len(values) < 1 || len(values) > MaxVectorDimensions {
		return errors.New("vector requires 1..2048 numbers")
	}
	for _, value := range values {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("vector values must be numbers, not null")
		}
	}
	return nil
}

// ValidateVector rejects nonfinite and zero vectors before norm arithmetic.
func ValidateVector(values []float64, dimensions int) error {
	if dimensions < 1 || dimensions > MaxVectorDimensions || len(values) != dimensions {
		return errors.New("vector dimension mismatch or outside 1..2048")
	}
	nonzero := false
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("vector values must be finite")
		}
		nonzero = nonzero || v != 0
	}
	if !nonzero {
		return errors.New("vector norm must be nonzero")
	}
	return nil
}

// CosineSimilarity scales each operand before summing, avoiding norm overflow
// and underflow even for finite subnormal or maximum-magnitude input values.
func CosineSimilarity(a, b []float64) (float64, error) {
	if err := ValidateVector(a, len(a)); err != nil {
		return 0, err
	}
	if err := ValidateVector(b, len(a)); err != nil {
		return 0, err
	}
	var am, bm float64
	for i, v := range a {
		am = math.Max(am, math.Abs(v))
		bm = math.Max(bm, math.Abs(b[i]))
	}
	var dot, aa, bb float64
	for i, v := range a {
		x, y := v/am, b[i]/bm
		dot += x * y
		aa += x * x
		bb += y * y
	}
	return math.Max(-1, math.Min(1, dot/(math.Sqrt(aa)*math.Sqrt(bb)))), nil
}

// QueryVectorIdentity binds the declared space and exact float64 query values.
func QueryVectorIdentity(spaceID SHA256, values []float64) (SHA256, error) {
	if spaceID == (SHA256{}) {
		return SHA256{}, errors.New("query requires a vector space")
	}
	if err := ValidateVector(values, len(values)); err != nil {
		return SHA256{}, err
	}
	data, err := json.Marshal(values)
	if err != nil {
		return SHA256{}, err
	}
	digest := sha256.New()
	writeTuple(digest, []byte(VectorQuerySchema), spaceID[:], data)
	return SHA256(digest.Sum(nil)), nil
}

package mousa

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const ClassificationInputSchema = "mousa.classification_input.v1"

// MaxClassificationInputBytes bounds native draft transport, including whitespace.
// One MiB accommodates all 4096 typed basis entries; store record limits still apply.
const MaxClassificationInputBytes = 1 << 20

// DecodeClassificationInput derives an immutable canonical assertion from explicit
// caller facts. It does not verify parent existence or authenticate the asserter.
func DecodeClassificationInput(data []byte) (Classification, error) {
	if len(data) > MaxClassificationInputBytes {
		return Classification{}, newValidationError("", ValidationCodeInvalidRange, "classification input exceeds 1 MiB", nil)
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Classification{}, err
	}
	object, err := classificationInputObject(data, "schema", "subject", "taxonomy", "taxonomy_version", "label", "asserter_id", "asserter_version", "basis", "asserted_at_usec", "confidence_ppm")
	if err != nil {
		return Classification{}, err
	}
	if _, err := classificationInputObject(object["subject"], "kind", "id"); err != nil {
		return Classification{}, prefixValidationError(err, "subject")
	}
	var bases []json.RawMessage
	if err := decodeJSONContract(object["basis"], &bases); err != nil {
		return Classification{}, prefixValidationError(err, "basis")
	}
	// Bound work before decoding each typed reference. Validate owns the real limit.
	if len(bases) == 0 || len(bases) > maxClassificationBasis {
		return Classification{}, newValidationError("basis", ValidationCodeInvalidValue, "must contain between 1 and 4096 entries", nil)
	}
	for i, raw := range bases {
		if _, err := classificationInputObject(raw, "kind", "id"); err != nil {
			return Classification{}, prefixValidationError(err, fmt.Sprintf("basis[%d]", i))
		}
	}
	var wire struct {
		Schema          string                  `json:"schema"`
		Subject         ClassificationSubject   `json:"subject"`
		Taxonomy        string                  `json:"taxonomy"`
		TaxonomyVersion string                  `json:"taxonomy_version"`
		Label           string                  `json:"label"`
		AsserterID      string                  `json:"asserter_id"`
		AsserterVersion string                  `json:"asserter_version"`
		Basis           []ClassificationSubject `json:"basis"`
		AssertedAtUsec  int64                   `json:"asserted_at_usec"`
		ConfidencePPM   uint64                  `json:"confidence_ppm"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return Classification{}, err
	}
	if wire.Schema != ClassificationInputSchema {
		return Classification{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.classification_input.v1", nil)
	}
	record := Classification{Schema: ClassificationSchema, Subject: wire.Subject, Taxonomy: wire.Taxonomy, TaxonomyVersion: wire.TaxonomyVersion, Label: wire.Label, AsserterID: wire.AsserterID, AsserterVersion: wire.AsserterVersion, Basis: wire.Basis, AssertedAtUsec: wire.AssertedAtUsec, ConfidencePPM: wire.ConfidencePPM}
	record.ID, err = NewClassificationID(record.Subject, record.Taxonomy, record.TaxonomyVersion, record.Label, record.AsserterID, record.AsserterVersion, record.Basis, record.AssertedAtUsec, record.ConfidencePPM)
	if err != nil {
		return Classification{}, err
	}
	return record, record.Validate()
}

// Require exact names and explicit non-null values before encoding/json can
// accept case aliases or silently turn null scalars into their zero values.
func classificationInputObject(data []byte, keys ...string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := decodeJSONContract(data, &object); err != nil {
		return nil, err
	}
	if len(object) != len(keys) {
		return nil, newValidationError("", ValidationCodeInvalidJSON, "missing or unknown classification input members", nil)
	}
	for _, key := range keys {
		raw, ok := object[key]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, newValidationError(key, ValidationCodeInvalidJSON, "required and cannot be null", nil)
		}
	}
	return object, nil
}

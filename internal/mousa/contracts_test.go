package mousa

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestClosedSourcePolicyValues(t *testing.T) {
	tests := []struct {
		name   string
		valid  []string
		decode func([]byte) error
	}{
		{"document status", []string{"active", "stale", "superseded", "deleted"}, func(data []byte) error { var value DocumentStatus; return json.Unmarshal(data, &value) }},
		{"authority", []string{"canonical", "derived", "advisory", "unknown"}, func(data []byte) error { var value Authority; return json.Unmarshal(data, &value) }},
		{"sensitivity", []string{"public", "internal", "private", "restricted", "non_indexable"}, func(data []byte) error { var value Sensitivity; return json.Unmarshal(data, &value) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, value := range test.valid {
				if err := test.decode([]byte(`"` + value + `"`)); err != nil {
					t.Fatalf("decode(%q): %v", value, err)
				}
			}
			for _, data := range [][]byte{[]byte(`""`), []byte(`"other"`), []byte(`1`)} {
				err := test.decode(data)
				var validationErr *ValidationError
				if !errors.As(err, &validationErr) || validationErr.Code != ValidationCodeInvalidEnum {
					t.Fatalf("decode(%s) error = %#v, want code %q", data, validationErr, ValidationCodeInvalidEnum)
				}
			}
		})
	}
}

func TestSourceDocumentRoundTrip(t *testing.T) {
	digest, err := ParseSHA256("e9024f1a07d29d52ad3aa5e1a18e94db1f3a9fd32b89e39d47c472cd99071e13")
	if err != nil {
		t.Fatal(err)
	}
	document := SourceDocument{
		Schema:                  SourceDocumentSchema,
		ID:                      NewDocumentID("source:a", digest),
		SourceID:                "source:a",
		URI:                     "https://example.invalid/a",
		Title:                   "A <B>",
		ContentType:             "text/plain",
		NormalizedContentSHA256: digest,
		Status:                  DocumentStatusActive,
		Authority:               AuthorityCanonical,
		Sensitivity:             SensitivityInternal,
	}

	encoded, err := EncodeSourceDocument(document)
	if err != nil {
		t.Fatalf("EncodeSourceDocument(): %v", err)
	}
	const want = "{\"schema\":\"mousa.source_document.v1\",\"id\":\"70ec22ca22cbfee77d6be8ce3f542c805efa64a210781ff3fd225ae8026400f3\",\"source_id\":\"source:a\",\"uri\":\"https://example.invalid/a\",\"title\":\"A <B>\",\"content_type\":\"text/plain\",\"normalized_content_sha256\":\"e9024f1a07d29d52ad3aa5e1a18e94db1f3a9fd32b89e39d47c472cd99071e13\",\"status\":\"active\",\"authority\":\"canonical\",\"sensitivity\":\"internal\"}\n"
	if !bytes.Equal(encoded, []byte(want)) {
		t.Fatalf("EncodeSourceDocument() = %q, want %q", encoded, want)
	}

	decoded, err := DecodeSourceDocument(encoded)
	if err != nil {
		t.Fatalf("DecodeSourceDocument(): %v", err)
	}
	if decoded != document {
		t.Fatalf("DecodeSourceDocument() = %#v, want %#v", decoded, document)
	}
	reencoded, err := EncodeSourceDocument(decoded)
	if err != nil || !bytes.Equal(reencoded, encoded) {
		t.Fatalf("second EncodeSourceDocument() = %q, %v", reencoded, err)
	}
}

func TestChunkRoundTrip(t *testing.T) {
	documentID, err := ParseDocumentID("70ec22ca22cbfee77d6be8ce3f542c805efa64a210781ff3fd225ae8026400f3")
	if err != nil {
		t.Fatal(err)
	}
	textDigest, err := ParseSHA256("1a989ea86150171c687b0727f218eedbb94c4665a7da9b0add1bf5de607f2bf1")
	if err != nil {
		t.Fatal(err)
	}
	chunkID, err := NewChunkID(documentID, 3, 12, 22, textDigest)
	if err != nil {
		t.Fatal(err)
	}
	chunk := Chunk{
		Schema:      ChunkSchema,
		ID:          chunkID,
		DocumentID:  documentID,
		Ordinal:     3,
		StartByte:   12,
		EndByte:     22,
		TextSHA256:  textDigest,
		Text:        "alpha beta",
		Status:      DocumentStatusActive,
		Authority:   AuthorityDerived,
		Sensitivity: SensitivityPublic,
		Indexable:   true,
	}

	encoded, err := EncodeChunk(chunk)
	if err != nil {
		t.Fatalf("EncodeChunk(): %v", err)
	}
	const want = "{\"schema\":\"mousa.chunk.v1\",\"id\":\"14d47ddbacb490a34c4223517eb4b841546d8c6980b4116198fdf0c1ff4529cf\",\"document_id\":\"70ec22ca22cbfee77d6be8ce3f542c805efa64a210781ff3fd225ae8026400f3\",\"ordinal\":3,\"start_byte\":12,\"end_byte\":22,\"text_sha256\":\"1a989ea86150171c687b0727f218eedbb94c4665a7da9b0add1bf5de607f2bf1\",\"text\":\"alpha beta\",\"status\":\"active\",\"authority\":\"derived\",\"sensitivity\":\"public\",\"indexable\":true}\n"
	if !bytes.Equal(encoded, []byte(want)) {
		t.Fatalf("EncodeChunk() = %q, want %q", encoded, want)
	}

	decoded, err := DecodeChunk(encoded)
	if err != nil {
		t.Fatalf("DecodeChunk(): %v", err)
	}
	if decoded != chunk {
		t.Fatalf("DecodeChunk() = %#v, want %#v", decoded, chunk)
	}
	reencoded, err := EncodeChunk(decoded)
	if err != nil || !bytes.Equal(reencoded, encoded) {
		t.Fatalf("second EncodeChunk() = %q, %v", reencoded, err)
	}
}

func TestSourceDocumentValidationFailures(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		code   ValidationCode
		mutate func(*SourceDocument)
	}{
		{"schema", "schema", ValidationCodeInvalidSchema, func(value *SourceDocument) { value.Schema = "other" }},
		{"source ID", "source_id", ValidationCodeInvalidValue, func(value *SourceDocument) { value.SourceID = "" }},
		{"content type", "content_type", ValidationCodeInvalidValue, func(value *SourceDocument) { value.ContentType = "" }},
		{"identity", "id", ValidationCodeInvalidID, func(value *SourceDocument) { value.ID = DocumentID{} }},
		{"status", "status", ValidationCodeInvalidEnum, func(value *SourceDocument) { value.Status = "other" }},
		{"authority", "authority", ValidationCodeInvalidEnum, func(value *SourceDocument) { value.Authority = "other" }},
		{"sensitivity", "sensitivity", ValidationCodeInvalidEnum, func(value *SourceDocument) { value.Sensitivity = "other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validSourceDocument(t)
			test.mutate(&value)
			requireValidationError(t, value.Validate(), test.field, test.code)
		})
	}
}

func TestSourceDocumentDecodeRejectsInvalidContracts(t *testing.T) {
	encoded, err := EncodeSourceDocument(validSourceDocument(t))
	if err != nil {
		t.Fatal(err)
	}
	valid := string(encoded)
	tests := []struct {
		name  string
		data  string
		field string
		code  ValidationCode
	}{
		{"unknown field", strings.TrimSuffix(valid, "}\n") + ",\"extra\":true}\n", "extra", ValidationCodeUnknownField},
		{"trailing value", valid + "{}", "", ValidationCodeTrailingData},
		{"wrong schema", strings.Replace(valid, SourceDocumentSchema, "other", 1), "schema", ValidationCodeInvalidSchema},
		{"wrong field type", strings.Replace(valid, `"source_id":"source:a"`, `"source_id":1`, 1), "source_id", ValidationCodeInvalidJSON},
		{"uppercase ID", strings.Replace(valid, validSourceDocument(t).ID.String(), strings.ToUpper(validSourceDocument(t).ID.String()), 1), "id", ValidationCodeInvalidID},
		{"malformed JSON", "{", "", ValidationCodeInvalidJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeSourceDocument([]byte(test.data))
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestChunkValidationFailures(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		code   ValidationCode
		mutate func(*Chunk)
	}{
		{"schema", "schema", ValidationCodeInvalidSchema, func(value *Chunk) { value.Schema = "other" }},
		{"identity", "id", ValidationCodeInvalidID, func(value *Chunk) { value.ID = ChunkID{} }},
		{"range", "end_byte", ValidationCodeInvalidRange, func(value *Chunk) { value.EndByte = value.StartByte - 1 }},
		{"status", "status", ValidationCodeInvalidEnum, func(value *Chunk) { value.Status = "other" }},
		{"authority", "authority", ValidationCodeInvalidEnum, func(value *Chunk) { value.Authority = "other" }},
		{"sensitivity", "sensitivity", ValidationCodeInvalidEnum, func(value *Chunk) { value.Sensitivity = "other" }},
		{"indexability", "indexable", ValidationCodeInvalidValue, func(value *Chunk) { value.Sensitivity = SensitivityNonIndexable; value.Indexable = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validChunk(t)
			test.mutate(&value)
			requireValidationError(t, value.Validate(), test.field, test.code)
		})
	}
}

func TestChunkDecodeRejectsInvalidContracts(t *testing.T) {
	encoded, err := EncodeChunk(validChunk(t))
	if err != nil {
		t.Fatal(err)
	}
	valid := string(encoded)
	tests := []struct {
		name  string
		data  string
		field string
		code  ValidationCode
	}{
		{"unknown field", strings.TrimSuffix(valid, "}\n") + ",\"extra\":true}\n", "extra", ValidationCodeUnknownField},
		{"trailing value", valid + "{}", "", ValidationCodeTrailingData},
		{"wrong schema", strings.Replace(valid, ChunkSchema, "other", 1), "schema", ValidationCodeInvalidSchema},
		{"wrong field type", strings.Replace(valid, `"ordinal":3`, `"ordinal":"3"`, 1), "ordinal", ValidationCodeInvalidJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeChunk([]byte(test.data))
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestValidationErrorPreservesFieldCodeAndCause(t *testing.T) {
	_, err := DecodeSourceDocument([]byte(`{"schema":1}`))
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if validationErr.Field != "schema" || validationErr.Code != ValidationCodeInvalidJSON || validationErr.Unwrap() == nil {
		t.Fatalf("validation error = %#v, cause %v", validationErr, validationErr.Unwrap())
	}
}

func validSourceDocument(t testing.TB) SourceDocument {
	t.Helper()
	digest, err := ParseSHA256("e9024f1a07d29d52ad3aa5e1a18e94db1f3a9fd32b89e39d47c472cd99071e13")
	if err != nil {
		t.Fatal(err)
	}
	return SourceDocument{
		Schema: SourceDocumentSchema, ID: NewDocumentID("source:a", digest), SourceID: "source:a",
		ContentType: "text/plain", NormalizedContentSHA256: digest, Status: DocumentStatusActive,
		Authority: AuthorityCanonical, Sensitivity: SensitivityInternal,
	}
}

func validChunk(t testing.TB) Chunk {
	t.Helper()
	documentID := validSourceDocument(t).ID
	digest, err := ParseSHA256("1a989ea86150171c687b0727f218eedbb94c4665a7da9b0add1bf5de607f2bf1")
	if err != nil {
		t.Fatal(err)
	}
	id, err := NewChunkID(documentID, 3, 12, 22, digest)
	if err != nil {
		t.Fatal(err)
	}
	return Chunk{Schema: ChunkSchema, ID: id, DocumentID: documentID, Ordinal: 3, StartByte: 12, EndByte: 22,
		TextSHA256: digest, Text: "alpha beta", Status: DocumentStatusActive, Authority: AuthorityDerived,
		Sensitivity: SensitivityPublic, Indexable: true}
}

func requireValidationError(t testing.TB, err error, field string, code ValidationCode) {
	t.Helper()
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	if validationErr.Field != field || validationErr.Code != code {
		t.Fatalf("validation evidence = %q/%q, want %q/%q", validationErr.Field, validationErr.Code, field, code)
	}
}

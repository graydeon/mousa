package mousa

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// The golden identity and canonical bytes below were derived outside this package from the
// documented construction: SHA-256 over each tuple field prefixed by its 8-byte big-endian
// length, in the order schema, source, predecessor item, predecessor revision, successor item,
// successor revision, author, basis. They are fixed expectations rather than outputs of the
// implementation under test.
const (
	goldenSupersessionDeclarationID         = "d4b3d618cc6a6d8cd5fb9da52a321602af1c89c23935ad7f5b7554db90c296af"
	goldenSupersessionSourceID              = "94205f99d18b70dd476c6527f64346696288df5d59bca83a02052383eca79ebe"
	goldenSupersessionPredecessorRevisionID = "de4b827bbb5084b2d463bfc36213749bbfa0683217d188eca2d3d6a27ade52ca"
	goldenSupersessionSuccessorRevisionID   = "8c8e968727f07da9f1abd7f0320cd4fd4c14856a2a5e7d0efc2134991e587886"
	goldenSupersessionPredecessorItemID     = "docs/mooring"
	goldenSupersessionSuccessorItemID       = "docs/mooring-corrected"
	goldenSupersessionAuthor                = "example.operations"
	goldenSupersessionBasis                 = "successor replaces the predecessor's mooring guidance"
	goldenSupersessionDeclarationBytes      = `{"schema":"mousa.supersession_declaration.v1","id":"d4b3d618cc6a6d8cd5fb9da52a321602af1c89c23935ad7f5b7554db90c296af","source_id":"94205f99d18b70dd476c6527f64346696288df5d59bca83a02052383eca79ebe","predecessor_item_id":"docs/mooring","predecessor_representation_id":"de4b827bbb5084b2d463bfc36213749bbfa0683217d188eca2d3d6a27ade52ca","successor_item_id":"docs/mooring-corrected","successor_representation_id":"8c8e968727f07da9f1abd7f0320cd4fd4c14856a2a5e7d0efc2134991e587886","author":"example.operations","basis":"successor replaces the predecessor's mooring guidance"}` + "\n"
)

// omittedJSONValue marks a required field a wire payload leaves out entirely.
const omittedJSONValue = "<omitted>"

func goldenSupersessionDeclaration(t testing.TB) SupersessionDeclaration {
	t.Helper()
	id, err := ParseSupersessionDeclarationID(goldenSupersessionDeclarationID)
	if err != nil {
		t.Fatalf("ParseSupersessionDeclarationID(): %v", err)
	}
	sourceID, err := ParseSourceID(goldenSupersessionSourceID)
	if err != nil {
		t.Fatalf("ParseSourceID(): %v", err)
	}
	predecessorRevisionID, err := ParseRepresentationID(goldenSupersessionPredecessorRevisionID)
	if err != nil {
		t.Fatalf("ParseRepresentationID(predecessor): %v", err)
	}
	successorRevisionID, err := ParseRepresentationID(goldenSupersessionSuccessorRevisionID)
	if err != nil {
		t.Fatalf("ParseRepresentationID(successor): %v", err)
	}
	return SupersessionDeclaration{
		Schema:                      SupersessionDeclarationSchema,
		ID:                          id,
		SourceID:                    sourceID,
		PredecessorItemID:           goldenSupersessionPredecessorItemID,
		PredecessorRepresentationID: predecessorRevisionID,
		SuccessorItemID:             goldenSupersessionSuccessorItemID,
		SuccessorRepresentationID:   successorRevisionID,
		Author:                      goldenSupersessionAuthor,
		Basis:                       goldenSupersessionBasis,
	}
}

func newSupersessionDeclarationID(t testing.TB, declaration SupersessionDeclaration) SupersessionDeclarationID {
	t.Helper()
	id, err := NewSupersessionDeclarationID(
		declaration.SourceID,
		declaration.PredecessorItemID,
		declaration.PredecessorRepresentationID,
		declaration.SuccessorItemID,
		declaration.SuccessorRepresentationID,
		declaration.Author,
		declaration.Basis,
	)
	if err != nil {
		t.Fatalf("NewSupersessionDeclarationID(): %v", err)
	}
	return id
}

// supersessionDeclarationJSON renders the golden declaration with quoted overrides, so a test can
// replace, retype or omit one field without hand-writing the whole payload.
func supersessionDeclarationJSON(overrides map[string]string) string {
	fields := []struct{ name, value string }{
		{"schema", `"` + SupersessionDeclarationSchema + `"`},
		{"id", `"` + goldenSupersessionDeclarationID + `"`},
		{"source_id", `"` + goldenSupersessionSourceID + `"`},
		{"predecessor_item_id", `"` + goldenSupersessionPredecessorItemID + `"`},
		{"predecessor_representation_id", `"` + goldenSupersessionPredecessorRevisionID + `"`},
		{"successor_item_id", `"` + goldenSupersessionSuccessorItemID + `"`},
		{"successor_representation_id", `"` + goldenSupersessionSuccessorRevisionID + `"`},
		{"author", `"` + goldenSupersessionAuthor + `"`},
		{"basis", `"` + goldenSupersessionBasis + `"`},
	}
	var rendered strings.Builder
	rendered.WriteString("{")
	written := 0
	for _, field := range fields {
		value, overridden := overrides[field.name]
		if overridden {
			if value == omittedJSONValue {
				continue
			}
			field.value = value
		}
		if written > 0 {
			rendered.WriteString(",")
		}
		rendered.WriteString(`"` + field.name + `":` + field.value)
		written++
	}
	rendered.WriteString("}")
	return rendered.String()
}

func mutatedHex(value string) string {
	last := value[len(value)-1]
	if last == 'a' {
		return value[:len(value)-1] + "b"
	}
	return value[:len(value)-1] + "a"
}

func TestSupersessionDeclarationGoldenIdentityAndCanonicalBytes(t *testing.T) {
	declaration := goldenSupersessionDeclaration(t)
	if err := declaration.Validate(); err != nil {
		t.Fatalf("Validate(): %v", err)
	}
	if declaration.ID.String() != goldenSupersessionDeclarationID {
		t.Fatalf("declaration ID = %q, want the independently derived %q", declaration.ID.String(), goldenSupersessionDeclarationID)
	}
	encoded, err := EncodeSupersessionDeclaration(declaration)
	if err != nil {
		t.Fatalf("EncodeSupersessionDeclaration(): %v", err)
	}
	if string(encoded) != goldenSupersessionDeclarationBytes {
		t.Fatalf("encoded declaration = %q, want the independently derived %q", encoded, goldenSupersessionDeclarationBytes)
	}
	decoded, err := DecodeSupersessionDeclaration(encoded)
	if err != nil {
		t.Fatalf("DecodeSupersessionDeclaration(): %v", err)
	}
	if decoded != declaration {
		t.Fatalf("decoded declaration = %+v, want %+v", decoded, declaration)
	}
	reencoded, err := EncodeSupersessionDeclaration(decoded)
	if err != nil {
		t.Fatalf("EncodeSupersessionDeclaration(decoded): %v", err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Fatal("re-encoding a decoded declaration is not byte-exact")
	}
	if derived := newSupersessionDeclarationID(t, declaration); derived != declaration.ID {
		t.Fatalf("derived ID = %q, want %q", derived.String(), declaration.ID.String())
	}
}

func TestSupersessionDeclarationIdentitySeparatesEveryField(t *testing.T) {
	declaration := goldenSupersessionDeclaration(t)
	baseline := declaration.ID.String()
	sourceID, err := ParseSourceID(mutatedHex(goldenSupersessionSourceID))
	if err != nil {
		t.Fatal(err)
	}
	predecessorRevisionID, err := ParseRepresentationID(mutatedHex(goldenSupersessionPredecessorRevisionID))
	if err != nil {
		t.Fatal(err)
	}
	successorRevisionID, err := ParseRepresentationID(mutatedHex(goldenSupersessionSuccessorRevisionID))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(value *SupersessionDeclaration)
	}{
		{"direction", func(value *SupersessionDeclaration) {
			value.PredecessorItemID, value.SuccessorItemID = value.SuccessorItemID, value.PredecessorItemID
			value.PredecessorRepresentationID, value.SuccessorRepresentationID = value.SuccessorRepresentationID, value.PredecessorRepresentationID
		}},
		{"source", func(value *SupersessionDeclaration) { value.SourceID = sourceID }},
		{"predecessor item", func(value *SupersessionDeclaration) { value.PredecessorItemID = "docs/mooring-old" }},
		{"predecessor revision", func(value *SupersessionDeclaration) { value.PredecessorRepresentationID = predecessorRevisionID }},
		{"successor item", func(value *SupersessionDeclaration) { value.SuccessorItemID = "docs/mooring-latest" }},
		{"successor revision", func(value *SupersessionDeclaration) { value.SuccessorRepresentationID = successorRevisionID }},
		{"author", func(value *SupersessionDeclaration) { value.Author = "example.reviewer" }},
		{"basis", func(value *SupersessionDeclaration) {
			value.Basis = "successor restates the predecessor's mooring guidance"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := declaration
			test.mutate(&value)
			derived := newSupersessionDeclarationID(t, value)
			if derived == (SupersessionDeclarationID{}) {
				t.Fatal("mutated declaration ID is zero")
			}
			if derived.String() == baseline {
				t.Fatalf("changing %s did not change the declaration identity", test.name)
			}
		})
	}
}

func TestSupersessionDeclarationDecodeIsKeyOrderIndependent(t *testing.T) {
	declaration := goldenSupersessionDeclaration(t)
	reordered, err := json.Marshal(struct {
		Basis                       string `json:"basis"`
		SuccessorRepresentationID   string `json:"successor_representation_id"`
		ID                          string `json:"id"`
		Author                      string `json:"author"`
		SuccessorItemID             string `json:"successor_item_id"`
		SourceID                    string `json:"source_id"`
		PredecessorRepresentationID string `json:"predecessor_representation_id"`
		Schema                      string `json:"schema"`
		PredecessorItemID           string `json:"predecessor_item_id"`
	}{
		Basis:                       goldenSupersessionBasis,
		SuccessorRepresentationID:   goldenSupersessionSuccessorRevisionID,
		ID:                          goldenSupersessionDeclarationID,
		Author:                      goldenSupersessionAuthor,
		SuccessorItemID:             goldenSupersessionSuccessorItemID,
		SourceID:                    goldenSupersessionSourceID,
		PredecessorRepresentationID: goldenSupersessionPredecessorRevisionID,
		Schema:                      SupersessionDeclarationSchema,
		PredecessorItemID:           goldenSupersessionPredecessorItemID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(reordered) == goldenSupersessionDeclarationBytes {
		t.Fatal("premise: reordered payload must differ from the canonical bytes")
	}
	decoded, err := DecodeSupersessionDeclaration(reordered)
	if err != nil {
		t.Fatalf("DecodeSupersessionDeclaration(reordered): %v", err)
	}
	if decoded != declaration {
		t.Fatalf("decoded declaration = %+v, want %+v", decoded, declaration)
	}
	reencoded, err := EncodeSupersessionDeclaration(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(reencoded) != goldenSupersessionDeclarationBytes {
		t.Fatalf("re-encoded declaration = %q, want %q", reencoded, goldenSupersessionDeclarationBytes)
	}
}

func TestSupersessionDeclarationRejectsIdentityMismatch(t *testing.T) {
	declaration := goldenSupersessionDeclaration(t)
	t.Run("record identity", func(t *testing.T) {
		zeroID := declaration
		zeroID.ID = SupersessionDeclarationID{}
		requireValidationError(t, zeroID.Validate(), "id", ValidationCodeInvalidID)
		edited := declaration
		edited.Basis = goldenSupersessionBasis + " "
		requireValidationError(t, edited.Validate(), "id", ValidationCodeInvalidID)
		_, err := EncodeSupersessionDeclaration(edited)
		requireValidationError(t, err, "id", ValidationCodeInvalidID)
	})
	t.Run("decoded mismatch", func(t *testing.T) {
		_, err := DecodeSupersessionDeclaration([]byte(supersessionDeclarationJSON(map[string]string{
			"id": `"` + mutatedHex(goldenSupersessionDeclarationID) + `"`,
		})))
		requireValidationError(t, err, "id", ValidationCodeInvalidID)
	})
}

func TestSupersessionDeclarationValidationFailures(t *testing.T) {
	modified := func(mutate func(value *SupersessionDeclaration)) SupersessionDeclaration {
		value := goldenSupersessionDeclaration(t)
		mutate(&value)
		return value
	}
	zeroRevision := RepresentationID{}
	tests := []struct {
		name  string
		value SupersessionDeclaration
		field string
		code  ValidationCode
	}{
		{"wrong schema", modified(func(value *SupersessionDeclaration) { value.Schema = "mousa.caller.v1" }), "schema", ValidationCodeInvalidSchema},
		{"zero ID", modified(func(value *SupersessionDeclaration) { value.ID = SupersessionDeclarationID{} }), "id", ValidationCodeInvalidID},
		{"mismatched ID", modified(func(value *SupersessionDeclaration) { value.ID[0] ^= 1 }), "id", ValidationCodeInvalidID},
		{"zero source", modified(func(value *SupersessionDeclaration) { value.SourceID = SourceID{} }), "source_id", ValidationCodeInvalidID},
		{"zero predecessor revision", modified(func(value *SupersessionDeclaration) { value.PredecessorRepresentationID = zeroRevision }), "predecessor_representation_id", ValidationCodeInvalidID},
		{"zero successor revision", modified(func(value *SupersessionDeclaration) { value.SuccessorRepresentationID = zeroRevision }), "successor_representation_id", ValidationCodeInvalidID},
		{"empty predecessor item", modified(func(value *SupersessionDeclaration) { value.PredecessorItemID = "" }), "predecessor_item_id", ValidationCodeInvalidValue},
		{"empty successor item", modified(func(value *SupersessionDeclaration) { value.SuccessorItemID = "" }), "successor_item_id", ValidationCodeInvalidValue},
		{"oversized predecessor item", modified(func(value *SupersessionDeclaration) {
			value.PredecessorItemID = strings.Repeat("a", MaxSupersessionItemBytes+1)
		}), "predecessor_item_id", ValidationCodeInvalidRange},
		{"NUL in successor item", modified(func(value *SupersessionDeclaration) { value.SuccessorItemID = "docs/moor\x00ing" }), "successor_item_id", ValidationCodeInvalidValue},
		{"invalid UTF-8 predecessor item", modified(func(value *SupersessionDeclaration) { value.PredecessorItemID = string([]byte{0xff}) }), "predecessor_item_id", ValidationCodeInvalidValue},
		{"equal items", modified(func(value *SupersessionDeclaration) { value.SuccessorItemID = value.PredecessorItemID }), "successor_item_id", ValidationCodeInvalidValue},
		{"equal revisions", modified(func(value *SupersessionDeclaration) {
			value.SuccessorRepresentationID = value.PredecessorRepresentationID
		}), "successor_representation_id", ValidationCodeInvalidValue},
		{"empty author", modified(func(value *SupersessionDeclaration) { value.Author = "" }), "author", ValidationCodeInvalidValue},
		{"oversized author", modified(func(value *SupersessionDeclaration) { value.Author = strings.Repeat("a", MaxSupersessionAuthorBytes+1) }), "author", ValidationCodeInvalidRange},
		{"NUL in author", modified(func(value *SupersessionDeclaration) { value.Author = "example\x00operations" }), "author", ValidationCodeInvalidValue},
		{"invalid UTF-8 author", modified(func(value *SupersessionDeclaration) { value.Author = string([]byte{0xfe}) }), "author", ValidationCodeInvalidValue},
		{"empty basis", modified(func(value *SupersessionDeclaration) { value.Basis = "" }), "basis", ValidationCodeInvalidValue},
		{"oversized basis", modified(func(value *SupersessionDeclaration) { value.Basis = strings.Repeat("a", MaxSupersessionBasisBytes+1) }), "basis", ValidationCodeInvalidRange},
		{"invalid UTF-8 basis", modified(func(value *SupersessionDeclaration) { value.Basis = string([]byte{0xc3}) }), "basis", ValidationCodeInvalidValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireValidationError(t, test.value.Validate(), test.field, test.code)
			_, err := EncodeSupersessionDeclaration(test.value)
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestSupersessionDeclarationDecodeRejectsInvalidPayloads(t *testing.T) {
	tests := []struct {
		name  string
		input string
		field string
		code  ValidationCode
	}{
		{"empty input", "", "", ValidationCodeInvalidJSON},
		{"malformed JSON", `{"schema":`, "", ValidationCodeInvalidJSON},
		{"duplicate key", supersessionDeclarationJSON(map[string]string{
			"schema": `"` + SupersessionDeclarationSchema + `","schema":"` + SupersessionDeclarationSchema + `"`,
		}), "", ValidationCodeInvalidJSON},
		{"unknown field", strings.Replace(goldenSupersessionDeclarationBytes, "{", `{"extra":1,`, 1), "extra", ValidationCodeUnknownField},
		{"wrong schema", supersessionDeclarationJSON(map[string]string{"schema": `"mousa.supersession_declaration.v2"`}), "schema", ValidationCodeInvalidSchema},
		{"missing schema", supersessionDeclarationJSON(map[string]string{"schema": omittedJSONValue}), "schema", ValidationCodeInvalidSchema},
		{"missing id", supersessionDeclarationJSON(map[string]string{"id": omittedJSONValue}), "id", ValidationCodeInvalidID},
		{"missing source", supersessionDeclarationJSON(map[string]string{"source_id": omittedJSONValue}), "source_id", ValidationCodeInvalidID},
		{"missing predecessor item", supersessionDeclarationJSON(map[string]string{"predecessor_item_id": omittedJSONValue}), "predecessor_item_id", ValidationCodeInvalidValue},
		{"missing predecessor revision", supersessionDeclarationJSON(map[string]string{"predecessor_representation_id": omittedJSONValue}), "predecessor_representation_id", ValidationCodeInvalidID},
		{"missing successor item", supersessionDeclarationJSON(map[string]string{"successor_item_id": omittedJSONValue}), "successor_item_id", ValidationCodeInvalidValue},
		{"missing successor revision", supersessionDeclarationJSON(map[string]string{"successor_representation_id": omittedJSONValue}), "successor_representation_id", ValidationCodeInvalidID},
		{"missing author", supersessionDeclarationJSON(map[string]string{"author": omittedJSONValue}), "author", ValidationCodeInvalidValue},
		{"missing basis", supersessionDeclarationJSON(map[string]string{"basis": omittedJSONValue}), "basis", ValidationCodeInvalidValue},
		{"null id", supersessionDeclarationJSON(map[string]string{"id": "null"}), "id", ValidationCodeInvalidID},
		{"null source", supersessionDeclarationJSON(map[string]string{"source_id": "null"}), "source_id", ValidationCodeInvalidID},
		{"null basis", supersessionDeclarationJSON(map[string]string{"basis": "null"}), "basis", ValidationCodeInvalidValue},
		{"wrong type id", supersessionDeclarationJSON(map[string]string{"id": "1"}), "id", ValidationCodeInvalidJSON},
		{"wrong type source", supersessionDeclarationJSON(map[string]string{"source_id": "1"}), "source_id", ValidationCodeInvalidJSON},
		{"wrong type basis", supersessionDeclarationJSON(map[string]string{"basis": "[]"}), "basis", ValidationCodeInvalidJSON},
		{"malformed id", supersessionDeclarationJSON(map[string]string{"id": `"bad"`}), "id", ValidationCodeInvalidID},
		{"uppercase id", supersessionDeclarationJSON(map[string]string{"id": `"` + strings.ToUpper(goldenSupersessionDeclarationID) + `"`}), "id", ValidationCodeInvalidID},
		{"zero id", supersessionDeclarationJSON(map[string]string{"id": `"` + strings.Repeat("0", 64) + `"`}), "id", ValidationCodeInvalidID},
		{"zero source", supersessionDeclarationJSON(map[string]string{"source_id": `"` + strings.Repeat("0", 64) + `"`}), "source_id", ValidationCodeInvalidID},
		{"zero predecessor revision", supersessionDeclarationJSON(map[string]string{"predecessor_representation_id": `"` + strings.Repeat("0", 64) + `"`}), "predecessor_representation_id", ValidationCodeInvalidID},
		{"zero successor revision", supersessionDeclarationJSON(map[string]string{"successor_representation_id": `"` + strings.Repeat("0", 64) + `"`}), "successor_representation_id", ValidationCodeInvalidID},
		{"self predecessor and successor item", supersessionDeclarationJSON(map[string]string{"successor_item_id": `"` + goldenSupersessionPredecessorItemID + `"`}), "successor_item_id", ValidationCodeInvalidValue},
		{"self predecessor and successor revision", supersessionDeclarationJSON(map[string]string{"successor_representation_id": `"` + goldenSupersessionPredecessorRevisionID + `"`}), "successor_representation_id", ValidationCodeInvalidValue},
		{"oversized predecessor item", supersessionDeclarationJSON(map[string]string{"predecessor_item_id": `"` + strings.Repeat("a", MaxSupersessionItemBytes+1) + `"`}), "predecessor_item_id", ValidationCodeInvalidRange},
		{"oversized author", supersessionDeclarationJSON(map[string]string{"author": `"` + strings.Repeat("a", MaxSupersessionAuthorBytes+1) + `"`}), "author", ValidationCodeInvalidRange},
		{"oversized basis", supersessionDeclarationJSON(map[string]string{"basis": `"` + strings.Repeat("a", MaxSupersessionBasisBytes+1) + `"`}), "basis", ValidationCodeInvalidRange},
		{"NUL in basis", supersessionDeclarationJSON(map[string]string{"basis": `"successor\u0000replaces"`}), "basis", ValidationCodeInvalidValue},
		{"trailing value", goldenSupersessionDeclarationBytes + `{}`, "", ValidationCodeTrailingData},
		{"trailing object start", goldenSupersessionDeclarationBytes + `{`, "", ValidationCodeTrailingData},
		{"invalid UTF-8 input", string([]byte{0xff}), "", ValidationCodeInvalidJSON},
		{"oversized input", goldenSupersessionDeclarationBytes + strings.Repeat(" ", MaxSupersessionDeclarationBytes), "", ValidationCodeInvalidRange},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeSupersessionDeclaration([]byte(test.input))
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestSupersessionDeclarationFieldByteLimitBoundaries(t *testing.T) {
	atLimit := func(size int) string { return strings.Repeat("a", size) }
	t.Run("item ids at the limit round trip", func(t *testing.T) {
		declaration := goldenSupersessionDeclaration(t)
		declaration.PredecessorItemID = atLimit(MaxSupersessionItemBytes)
		declaration.SuccessorItemID = atLimit(MaxSupersessionItemBytes-1) + "b"
		declaration.ID = newSupersessionDeclarationID(t, declaration)
		encoded, err := EncodeSupersessionDeclaration(declaration)
		if err != nil {
			t.Fatalf("EncodeSupersessionDeclaration(): %v", err)
		}
		decoded, err := DecodeSupersessionDeclaration(encoded)
		if err != nil {
			t.Fatalf("DecodeSupersessionDeclaration(): %v", err)
		}
		if decoded != declaration {
			t.Fatal("item ids at the byte limit did not round trip")
		}
	})
	t.Run("author and basis at the limit round trip", func(t *testing.T) {
		declaration := goldenSupersessionDeclaration(t)
		declaration.Author = atLimit(MaxSupersessionAuthorBytes)
		declaration.Basis = atLimit(MaxSupersessionBasisBytes)
		declaration.ID = newSupersessionDeclarationID(t, declaration)
		encoded, err := EncodeSupersessionDeclaration(declaration)
		if err != nil {
			t.Fatalf("EncodeSupersessionDeclaration(): %v", err)
		}
		decoded, err := DecodeSupersessionDeclaration(encoded)
		if err != nil {
			t.Fatalf("DecodeSupersessionDeclaration(): %v", err)
		}
		if decoded != declaration {
			t.Fatal("author and basis at the byte limit did not round trip")
		}
	})
	t.Run("one byte over each limit", func(t *testing.T) {
		tests := []struct {
			name  string
			value func(*SupersessionDeclaration)
			field string
		}{
			{"predecessor item", func(value *SupersessionDeclaration) { value.PredecessorItemID = atLimit(MaxSupersessionItemBytes + 1) }, "predecessor_item_id"},
			{"successor item", func(value *SupersessionDeclaration) { value.SuccessorItemID = atLimit(MaxSupersessionItemBytes + 1) }, "successor_item_id"},
			{"author", func(value *SupersessionDeclaration) { value.Author = atLimit(MaxSupersessionAuthorBytes + 1) }, "author"},
			{"basis", func(value *SupersessionDeclaration) { value.Basis = atLimit(MaxSupersessionBasisBytes + 1) }, "basis"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				declaration := goldenSupersessionDeclaration(t)
				test.value(&declaration)
				requireValidationError(t, declaration.Validate(), test.field, ValidationCodeInvalidRange)
			})
		}
	})
	t.Run("encoded input at the limit is not rejected as oversized", func(t *testing.T) {
		padded := goldenSupersessionDeclarationBytes + strings.Repeat(" ", MaxSupersessionDeclarationBytes-len(goldenSupersessionDeclarationBytes))
		if len(padded) != MaxSupersessionDeclarationBytes {
			t.Fatalf("padded payload = %d bytes, want %d", len(padded), MaxSupersessionDeclarationBytes)
		}
		decoded, err := DecodeSupersessionDeclaration([]byte(padded))
		if err != nil {
			t.Fatalf("DecodeSupersessionDeclaration() at the encoded limit: %v", err)
		}
		if decoded != goldenSupersessionDeclaration(t) {
			t.Fatal("payload at the encoded limit did not decode to the golden declaration")
		}
		over := append(append([]byte(nil), padded...), ' ')
		_, err = DecodeSupersessionDeclaration(over)
		requireValidationError(t, err, "", ValidationCodeInvalidRange)
	})
	t.Run("worst-case escaping stays inside the encoded limit", func(t *testing.T) {
		declaration := goldenSupersessionDeclaration(t)
		declaration.PredecessorItemID = strings.Repeat("\x01", MaxSupersessionItemBytes)
		declaration.SuccessorItemID = strings.Repeat("\x02", MaxSupersessionItemBytes)
		declaration.Author = strings.Repeat("\x03", MaxSupersessionAuthorBytes)
		declaration.Basis = strings.Repeat("\x04", MaxSupersessionBasisBytes)
		declaration.ID = newSupersessionDeclarationID(t, declaration)
		encoded, err := EncodeSupersessionDeclaration(declaration)
		if err != nil {
			t.Fatalf("EncodeSupersessionDeclaration(): %v", err)
		}
		if len(encoded) > MaxSupersessionDeclarationBytes {
			t.Fatalf("encoded declaration = %d bytes, exceeds the %d-byte input limit", len(encoded), MaxSupersessionDeclarationBytes)
		}
		decoded, err := DecodeSupersessionDeclaration(encoded)
		if err != nil {
			t.Fatalf("DecodeSupersessionDeclaration(): %v", err)
		}
		if decoded != declaration {
			t.Fatal("control-character fields did not round trip")
		}
	})
}

package mousa

import (
	"reflect"
	"strings"
	"testing"
)

// The golden activation identities and canonical bytes below were derived outside this package from
// the documented construction: SHA-256 over each tuple field prefixed by its 8-byte big-endian
// length, in the order schema, source, expected predecessor event, selected declaration, where an
// absent identity is the empty field. They are fixed expectations rather than outputs of the
// implementation under test.
const (
	goldenSupersessionActivationID      = "047bfd80e0f688e33fa9affff719226fb6c8fc775f2ee3369b3848f651422b3d"
	goldenSupersessionDeactivationID    = "4b7c1ecabc60f37a1d6d133dec1e253a4bf2f7d650415ed04c4264486ae896a5"
	goldenSupersessionActorID           = "example.operator"
	goldenSupersessionActorVersion      = "console/1.0"
	goldenSupersessionActivationAt      = int64(1759622400000000)
	goldenSupersessionActivationText    = "activate the declared successor"
	goldenSupersessionActivationBytes   = `{"schema":"mousa.supersession_activation.v1","id":"047bfd80e0f688e33fa9affff719226fb6c8fc775f2ee3369b3848f651422b3d","source_id":"94205f99d18b70dd476c6527f64346696288df5d59bca83a02052383eca79ebe","expected_previous_activation_id":null,"declaration_id":"d4b3d618cc6a6d8cd5fb9da52a321602af1c89c23935ad7f5b7554db90c296af","actor_id":"example.operator","actor_version":"console/1.0","occurred_at_usec":1759622400000000,"reason":"activate the declared successor"}` + "\n"
	goldenSupersessionDeactivationBytes = `{"schema":"mousa.supersession_activation.v1","id":"4b7c1ecabc60f37a1d6d133dec1e253a4bf2f7d650415ed04c4264486ae896a5","source_id":"94205f99d18b70dd476c6527f64346696288df5d59bca83a02052383eca79ebe","expected_previous_activation_id":"047bfd80e0f688e33fa9affff719226fb6c8fc775f2ee3369b3848f651422b3d","declaration_id":null,"actor_id":"example.operator","actor_version":"console/1.0","occurred_at_usec":1759622401000000,"reason":null}` + "\n"
)

func goldenSupersessionActivation(t testing.TB) SupersessionActivation {
	t.Helper()
	declaration := goldenSupersessionDeclaration(t)
	reason := goldenSupersessionActivationText
	return SupersessionActivation{
		Schema:         SupersessionActivationSchema,
		ID:             mustParseActivationID(t, goldenSupersessionActivationID),
		SourceID:       declaration.SourceID,
		DeclarationID:  &declaration.ID,
		ActorID:        goldenSupersessionActorID,
		ActorVersion:   goldenSupersessionActorVersion,
		OccurredAtUsec: goldenSupersessionActivationAt,
		Reason:         &reason,
	}
}

func goldenSupersessionDeactivation(t testing.TB) SupersessionActivation {
	t.Helper()
	previous := mustParseActivationID(t, goldenSupersessionActivationID)
	return SupersessionActivation{
		Schema:                       SupersessionActivationSchema,
		ID:                           mustParseActivationID(t, goldenSupersessionDeactivationID),
		SourceID:                     goldenSupersessionDeclaration(t).SourceID,
		ExpectedPreviousActivationID: &previous,
		ActorID:                      goldenSupersessionActorID,
		ActorVersion:                 goldenSupersessionActorVersion,
		OccurredAtUsec:               goldenSupersessionActivationAt + 1000000,
	}
}

func mustParseActivationID(t testing.TB, value string) SupersessionActivationID {
	t.Helper()
	id, err := ParseSupersessionActivationID(value)
	if err != nil {
		t.Fatalf("ParseSupersessionActivationID(%q): %v", value, err)
	}
	return id
}

func newSupersessionActivationID(t testing.TB, activation SupersessionActivation) SupersessionActivationID {
	t.Helper()
	id, err := NewSupersessionActivationID(activation.SourceID, activation.ExpectedPreviousActivationID, activation.DeclarationID)
	if err != nil {
		t.Fatalf("NewSupersessionActivationID(): %v", err)
	}
	return id
}

// supersessionActivationJSON renders one golden activation with quoted overrides, so a test can
// replace, retype or omit one field without hand-writing the whole payload.
func supersessionActivationJSON(overrides map[string]string) string {
	fields := []struct{ name, value string }{
		{"schema", `"` + SupersessionActivationSchema + `"`},
		{"id", `"` + goldenSupersessionActivationID + `"`},
		{"source_id", `"` + goldenSupersessionSourceID + `"`},
		{"expected_previous_activation_id", "null"},
		{"declaration_id", `"` + goldenSupersessionDeclarationID + `"`},
		{"actor_id", `"` + goldenSupersessionActorID + `"`},
		{"actor_version", `"` + goldenSupersessionActorVersion + `"`},
		{"occurred_at_usec", "1759622400000000"},
		{"reason", `"` + goldenSupersessionActivationText + `"`},
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

func TestSupersessionActivationGoldenIdentityAndCanonicalBytes(t *testing.T) {
	for _, test := range []struct {
		name  string
		value SupersessionActivation
		id    string
		bytes string
	}{
		{"initial activation", goldenSupersessionActivation(t), goldenSupersessionActivationID, goldenSupersessionActivationBytes},
		{"deactivation", goldenSupersessionDeactivation(t), goldenSupersessionDeactivationID, goldenSupersessionDeactivationBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.value.Validate(); err != nil {
				t.Fatalf("Validate(): %v", err)
			}
			if test.value.ID.String() != test.id {
				t.Fatalf("activation ID = %q, want the independently derived %q", test.value.ID.String(), test.id)
			}
			encoded, err := EncodeSupersessionActivation(test.value)
			if err != nil {
				t.Fatalf("EncodeSupersessionActivation(): %v", err)
			}
			if string(encoded) != test.bytes {
				t.Fatalf("encoded activation = %q, want the independently derived %q", encoded, test.bytes)
			}
			decoded, err := DecodeSupersessionActivation([]byte(test.bytes))
			if err != nil {
				t.Fatalf("DecodeSupersessionActivation(): %v", err)
			}
			if !reflect.DeepEqual(decoded, test.value) {
				t.Fatalf("decoded activation = %#v, want %#v", decoded, test.value)
			}
		})
	}
}

func TestSupersessionActivationIdentitySeparatesEveryField(t *testing.T) {
	base := goldenSupersessionDeactivation(t)
	otherActivation := mustParseActivationID(t, mutatedHex(goldenSupersessionActivationID))
	otherDeclaration := mutatedDeclarationID(t)
	otherSource := base.SourceID
	otherSource[0] ^= 1
	identities := map[string]SupersessionActivationID{}
	for _, test := range []struct {
		name  string
		value SupersessionActivation
	}{
		{"base", base},
		{"other source", func() SupersessionActivation { value := base; value.SourceID = otherSource; return value }()},
		{"null predecessor", func() SupersessionActivation {
			value := base
			value.ExpectedPreviousActivationID = nil
			value.DeclarationID = &otherDeclaration
			return value
		}()},
		{"other predecessor", func() SupersessionActivation {
			value := base
			value.ExpectedPreviousActivationID = &otherActivation
			return value
		}()},
		{"other declaration", func() SupersessionActivation {
			value := base
			value.DeclarationID = &otherDeclaration
			return value
		}()},
	} {
		identities[test.name] = newSupersessionActivationID(t, test.value)
	}
	for name, id := range identities {
		for otherName, otherID := range identities {
			if name == otherName {
				continue
			}
			if id == otherID {
				t.Fatalf("identities for %q and %q collide", name, otherName)
			}
		}
	}
}

// The recorded actor, time and reason are evidence about the transition, not part of its identity,
// so a differing replay of one identity is a conflict rather than a second transition.
func TestSupersessionActivationIdentityExcludesRecordedMetadata(t *testing.T) {
	base := goldenSupersessionActivation(t)
	baseID := newSupersessionActivationID(t, base)
	reason := "another reason"
	for _, test := range []struct {
		name   string
		mutate func(*SupersessionActivation)
	}{
		{"actor", func(value *SupersessionActivation) { value.ActorID = "example.other" }},
		{"actor version", func(value *SupersessionActivation) { value.ActorVersion = "console/2.0" }},
		{"occurred at", func(value *SupersessionActivation) { value.OccurredAtUsec++ }},
		{"reason", func(value *SupersessionActivation) { value.Reason = &reason }},
		{"absent reason", func(value *SupersessionActivation) { value.Reason = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			edited := base
			test.mutate(&edited)
			edited.ID = newSupersessionActivationID(t, edited)
			if edited.ID != baseID {
				t.Fatalf("identity changed to %q, want the unchanged %q", edited.ID, baseID)
			}
			if err := edited.Validate(); err != nil {
				t.Fatalf("Validate(): %v", err)
			}
		})
	}
}

func mutatedDeclarationID(t testing.TB) SupersessionDeclarationID {
	t.Helper()
	declaration := goldenSupersessionDeclaration(t)
	declaration.Basis += " corrected"
	return newSupersessionDeclarationID(t, declaration)
}

func TestSupersessionActivationDecodeIsKeyOrderIndependent(t *testing.T) {
	reordered := `{"reason":"` + goldenSupersessionActivationText + `","occurred_at_usec":1759622400000000,"actor_version":"` + goldenSupersessionActorVersion + `","actor_id":"` + goldenSupersessionActorID + `","declaration_id":"` + goldenSupersessionDeclarationID + `","expected_previous_activation_id":null,"source_id":"` + goldenSupersessionSourceID + `","id":"` + goldenSupersessionActivationID + `","schema":"` + SupersessionActivationSchema + `"}`
	decoded, err := DecodeSupersessionActivation([]byte(reordered))
	if err != nil {
		t.Fatalf("DecodeSupersessionActivation(): %v", err)
	}
	if !reflect.DeepEqual(decoded, goldenSupersessionActivation(t)) {
		t.Fatalf("reordered decode = %#v, want the golden activation", decoded)
	}
	encoded, err := EncodeSupersessionActivation(decoded)
	if err != nil {
		t.Fatalf("EncodeSupersessionActivation(): %v", err)
	}
	if string(encoded) != goldenSupersessionActivationBytes {
		t.Fatal("reordered input did not re-encode to the canonical bytes")
	}
}

func TestSupersessionActivationRejectsIdentityMismatch(t *testing.T) {
	activation := goldenSupersessionActivation(t)
	t.Run("record identity", func(t *testing.T) {
		zero := activation
		zero.ID = SupersessionActivationID{}
		requireValidationError(t, zero.Validate(), "id", ValidationCodeInvalidID)
		mismatched := activation
		mismatched.SourceID[0] ^= 1
		requireValidationError(t, mismatched.Validate(), "id", ValidationCodeInvalidID)
		_, err := EncodeSupersessionActivation(mismatched)
		requireValidationError(t, err, "id", ValidationCodeInvalidID)
	})
	t.Run("decoded mismatch", func(t *testing.T) {
		_, err := DecodeSupersessionActivation([]byte(supersessionActivationJSON(map[string]string{
			"id": `"` + mutatedHex(goldenSupersessionActivationID) + `"`,
		})))
		requireValidationError(t, err, "id", ValidationCodeInvalidID)
	})
}

func TestSupersessionActivationValidationFailures(t *testing.T) {
	modified := func(mutate func(value *SupersessionActivation)) SupersessionActivation {
		value := goldenSupersessionActivation(t)
		mutate(&value)
		return value
	}
	zeroActivation := SupersessionActivationID{}
	zeroDeclaration := SupersessionDeclarationID{}
	blank := ""
	tests := []struct {
		name  string
		value SupersessionActivation
		field string
		code  ValidationCode
	}{
		{"wrong schema", modified(func(value *SupersessionActivation) { value.Schema = "mousa.caller.v1" }), "schema", ValidationCodeInvalidSchema},
		{"zero ID", modified(func(value *SupersessionActivation) { value.ID = zeroActivation }), "id", ValidationCodeInvalidID},
		{"mismatched ID", modified(func(value *SupersessionActivation) { value.ID[0] ^= 1 }), "id", ValidationCodeInvalidID},
		{"zero source", modified(func(value *SupersessionActivation) { value.SourceID = SourceID{} }), "source_id", ValidationCodeInvalidID},
		{"zero expected predecessor", modified(func(value *SupersessionActivation) {
			value.ExpectedPreviousActivationID = &zeroActivation
		}), "expected_previous_activation_id", ValidationCodeInvalidID},
		{"zero declaration", modified(func(value *SupersessionActivation) { value.DeclarationID = &zeroDeclaration }), "declaration_id", ValidationCodeInvalidID},
		{"root without declaration", modified(func(value *SupersessionActivation) { value.DeclarationID = nil }), "declaration_id", ValidationCodeInvalidValue},
		{"empty actor", modified(func(value *SupersessionActivation) { value.ActorID = "" }), "actor_id", ValidationCodeInvalidValue},
		{"oversized actor", modified(func(value *SupersessionActivation) { value.ActorID = strings.Repeat("a", MaxSupersessionActorBytes+1) }), "actor_id", ValidationCodeInvalidRange},
		{"NUL in actor", modified(func(value *SupersessionActivation) { value.ActorID = "example\x00operator" }), "actor_id", ValidationCodeInvalidValue},
		{"invalid UTF-8 actor", modified(func(value *SupersessionActivation) { value.ActorID = string([]byte{0xff}) }), "actor_id", ValidationCodeInvalidValue},
		{"empty actor version", modified(func(value *SupersessionActivation) { value.ActorVersion = "" }), "actor_version", ValidationCodeInvalidValue},
		{"oversized actor version", modified(func(value *SupersessionActivation) {
			value.ActorVersion = strings.Repeat("a", MaxSupersessionActorBytes+1)
		}), "actor_version", ValidationCodeInvalidRange},
		{"zero occurred_at", modified(func(value *SupersessionActivation) { value.OccurredAtUsec = 0 }), "occurred_at_usec", ValidationCodeInvalidValue},
		{"negative occurred_at", modified(func(value *SupersessionActivation) { value.OccurredAtUsec = -1 }), "occurred_at_usec", ValidationCodeInvalidValue},
		{"empty reason", modified(func(value *SupersessionActivation) { value.Reason = &blank }), "reason", ValidationCodeInvalidValue},
		{"oversized reason", modified(func(value *SupersessionActivation) {
			reason := strings.Repeat("a", MaxSupersessionReasonBytes+1)
			value.Reason = &reason
		}), "reason", ValidationCodeInvalidRange},
		{"NUL in reason", modified(func(value *SupersessionActivation) {
			reason := "replace\x00predecessor"
			value.Reason = &reason
		}), "reason", ValidationCodeInvalidValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireValidationError(t, test.value.Validate(), test.field, test.code)
			_, err := EncodeSupersessionActivation(test.value)
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestSupersessionActivationDecodeRejectsInvalidPayloads(t *testing.T) {
	zero := strings.Repeat("0", 64)
	tests := []struct {
		name  string
		input string
		field string
		code  ValidationCode
	}{
		{"empty input", "", "", ValidationCodeInvalidJSON},
		{"malformed JSON", `{"schema":`, "", ValidationCodeInvalidJSON},
		{"duplicate key", supersessionActivationJSON(map[string]string{
			"schema": `"` + SupersessionActivationSchema + `","schema":"` + SupersessionActivationSchema + `"`,
		}), "", ValidationCodeInvalidJSON},
		{"unknown field", strings.Replace(goldenSupersessionActivationBytes, "{", `{"extra":1,`, 1), "extra", ValidationCodeUnknownField},
		{"wrong schema", supersessionActivationJSON(map[string]string{"schema": `"mousa.supersession_activation.v2"`}), "schema", ValidationCodeInvalidSchema},
		{"missing schema", supersessionActivationJSON(map[string]string{"schema": omittedJSONValue}), "schema", ValidationCodeInvalidSchema},
		{"missing id", supersessionActivationJSON(map[string]string{"id": omittedJSONValue}), "id", ValidationCodeInvalidID},
		{"missing source", supersessionActivationJSON(map[string]string{"source_id": omittedJSONValue}), "source_id", ValidationCodeInvalidID},
		{"missing expected predecessor", supersessionActivationJSON(map[string]string{"expected_previous_activation_id": omittedJSONValue}), "", ValidationCodeInvalidValue},
		{"missing declaration", supersessionActivationJSON(map[string]string{"declaration_id": omittedJSONValue}), "", ValidationCodeInvalidValue},
		{"missing reason", supersessionActivationJSON(map[string]string{"reason": omittedJSONValue}), "", ValidationCodeInvalidValue},
		{"missing actor", supersessionActivationJSON(map[string]string{"actor_id": omittedJSONValue}), "actor_id", ValidationCodeInvalidValue},
		{"missing actor version", supersessionActivationJSON(map[string]string{"actor_version": omittedJSONValue}), "actor_version", ValidationCodeInvalidValue},
		{"null id", supersessionActivationJSON(map[string]string{"id": "null"}), "id", ValidationCodeInvalidID},
		{"null source", supersessionActivationJSON(map[string]string{"source_id": "null"}), "source_id", ValidationCodeInvalidID},
		{"null actor", supersessionActivationJSON(map[string]string{"actor_id": "null"}), "actor_id", ValidationCodeInvalidValue},
		{"null occurred_at", supersessionActivationJSON(map[string]string{"occurred_at_usec": "null"}), "occurred_at_usec", ValidationCodeInvalidValue},
		{"wrong type id", supersessionActivationJSON(map[string]string{"id": "1"}), "id", ValidationCodeInvalidJSON},
		{"wrong type source", supersessionActivationJSON(map[string]string{"source_id": "1"}), "source_id", ValidationCodeInvalidJSON},
		{"wrong type expected predecessor", supersessionActivationJSON(map[string]string{"expected_previous_activation_id": "[]"}), "expected_previous_activation_id", ValidationCodeInvalidID},
		{"wrong type declaration", supersessionActivationJSON(map[string]string{"declaration_id": "{}"}), "declaration_id", ValidationCodeInvalidID},
		{"wrong type reason", supersessionActivationJSON(map[string]string{"reason": "[]"}), "reason", ValidationCodeInvalidValue},
		{"malformed id", supersessionActivationJSON(map[string]string{"id": `"bad"`}), "id", ValidationCodeInvalidID},
		{"uppercase id", supersessionActivationJSON(map[string]string{"id": `"` + strings.ToUpper(goldenSupersessionActivationID) + `"`}), "id", ValidationCodeInvalidID},
		{"zero id", supersessionActivationJSON(map[string]string{"id": `"` + zero + `"`}), "id", ValidationCodeInvalidID},
		{"zero source", supersessionActivationJSON(map[string]string{"source_id": `"` + zero + `"`}), "source_id", ValidationCodeInvalidID},
		{"zero expected predecessor", supersessionActivationJSON(map[string]string{"expected_previous_activation_id": `"` + zero + `"`}), "expected_previous_activation_id", ValidationCodeInvalidID},
		{"zero declaration", supersessionActivationJSON(map[string]string{"declaration_id": `"` + zero + `"`}), "declaration_id", ValidationCodeInvalidID},
		{"root without declaration", supersessionActivationJSON(map[string]string{"declaration_id": "null"}), "declaration_id", ValidationCodeInvalidValue},
		{"oversized actor", supersessionActivationJSON(map[string]string{"actor_id": `"` + strings.Repeat("a", MaxSupersessionActorBytes+1) + `"`}), "actor_id", ValidationCodeInvalidRange},
		{"oversized reason", supersessionActivationJSON(map[string]string{"reason": `"` + strings.Repeat("a", MaxSupersessionReasonBytes+1) + `"`}), "reason", ValidationCodeInvalidRange},
		{"trailing value", goldenSupersessionActivationBytes + `{}`, "", ValidationCodeTrailingData},
		{"invalid UTF-8 input", string([]byte{0xff}), "", ValidationCodeInvalidJSON},
		{"oversized input", goldenSupersessionActivationBytes + strings.Repeat(" ", MaxSupersessionActivationBytes), "", ValidationCodeInvalidRange},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeSupersessionActivation([]byte(test.input))
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestSupersessionActivationFieldByteLimitBoundaries(t *testing.T) {
	atLimit := func(size int) string { return strings.Repeat("a", size) }
	t.Run("actor and reason at the limit round trip", func(t *testing.T) {
		activation := goldenSupersessionActivation(t)
		activation.ActorID = atLimit(MaxSupersessionActorBytes)
		activation.ActorVersion = atLimit(MaxSupersessionActorBytes)
		reason := atLimit(MaxSupersessionReasonBytes)
		activation.Reason = &reason
		activation.ID = newSupersessionActivationID(t, activation)
		encoded, err := EncodeSupersessionActivation(activation)
		if err != nil {
			t.Fatalf("EncodeSupersessionActivation(): %v", err)
		}
		decoded, err := DecodeSupersessionActivation(encoded)
		if err != nil {
			t.Fatalf("DecodeSupersessionActivation(): %v", err)
		}
		if !reflect.DeepEqual(decoded, activation) {
			t.Fatal("actor and reason at the byte limit did not round trip")
		}
	})
	t.Run("one byte over each limit", func(t *testing.T) {
		tests := []struct {
			name  string
			value func(*SupersessionActivation)
			field string
		}{
			{"actor", func(value *SupersessionActivation) { value.ActorID = atLimit(MaxSupersessionActorBytes + 1) }, "actor_id"},
			{"actor version", func(value *SupersessionActivation) { value.ActorVersion = atLimit(MaxSupersessionActorBytes + 1) }, "actor_version"},
			{"reason", func(value *SupersessionActivation) {
				reason := atLimit(MaxSupersessionReasonBytes + 1)
				value.Reason = &reason
			}, "reason"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				activation := goldenSupersessionActivation(t)
				test.value(&activation)
				requireValidationError(t, activation.Validate(), test.field, ValidationCodeInvalidRange)
			})
		}
	})
	t.Run("encoded input at the limit is not rejected as oversized", func(t *testing.T) {
		padded := goldenSupersessionActivationBytes + strings.Repeat(" ", MaxSupersessionActivationBytes-len(goldenSupersessionActivationBytes))
		if len(padded) != MaxSupersessionActivationBytes {
			t.Fatalf("padded payload = %d bytes, want %d", len(padded), MaxSupersessionActivationBytes)
		}
		decoded, err := DecodeSupersessionActivation([]byte(padded))
		if err != nil {
			t.Fatalf("DecodeSupersessionActivation() at the encoded limit: %v", err)
		}
		if !reflect.DeepEqual(decoded, goldenSupersessionActivation(t)) {
			t.Fatal("payload at the encoded limit did not decode to the golden activation")
		}
		over := append(append([]byte(nil), padded...), ' ')
		_, err = DecodeSupersessionActivation(over)
		requireValidationError(t, err, "", ValidationCodeInvalidRange)
	})
	t.Run("worst-case escaping stays inside the encoded limit", func(t *testing.T) {
		activation := goldenSupersessionActivation(t)
		activation.ActorID = strings.Repeat("\x01", MaxSupersessionActorBytes)
		activation.ActorVersion = strings.Repeat("\x02", MaxSupersessionActorBytes)
		reason := strings.Repeat("\x03", MaxSupersessionReasonBytes)
		activation.Reason = &reason
		activation.ID = newSupersessionActivationID(t, activation)
		encoded, err := EncodeSupersessionActivation(activation)
		if err != nil {
			t.Fatalf("EncodeSupersessionActivation(): %v", err)
		}
		if len(encoded) > MaxSupersessionActivationBytes {
			t.Fatalf("encoded activation = %d bytes, exceeds the %d-byte input limit", len(encoded), MaxSupersessionActivationBytes)
		}
		decoded, err := DecodeSupersessionActivation(encoded)
		if err != nil {
			t.Fatalf("DecodeSupersessionActivation(): %v", err)
		}
		if !reflect.DeepEqual(decoded, activation) {
			t.Fatal("control-character fields did not round trip")
		}
	})
}

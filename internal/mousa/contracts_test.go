package mousa

import (
	"bytes"
	"errors"
	"testing"
)

const (
	validSourceJSON      = "{\"schema\":\"mousa.source.v1\",\"id\":\"f111237310ae4db59c528c5e0e53581384059910adab736913c6e29f0ef91d41\",\"namespace\":\"example.mailbox\",\"external_source_id\":\"account:alpha\"}\n"
	validObservationJSON = "{\"schema\":\"mousa.observation.v1\",\"id\":\"8ad5e465541e71ad6246c21fdd9da960736394e1ac4688e034471910784ea12a\",\"source_id\":\"f111237310ae4db59c528c5e0e53581384059910adab736913c6e29f0ef91d41\",\"external_observation_id\":\"message:42\"}\n"
)

func TestSourceExactRoundTripAndDeterministicEncoding(t *testing.T) {
	source := validSource(t)
	first, err := EncodeSource(source)
	if err != nil {
		t.Fatalf("EncodeSource(): %v", err)
	}
	second, err := EncodeSource(source)
	if err != nil {
		t.Fatalf("EncodeSource() again: %v", err)
	}
	if string(first) != validSourceJSON || !bytes.Equal(first, second) {
		t.Fatalf("EncodeSource() = %q, second = %q", first, second)
	}
	decoded, err := DecodeSource(first)
	if err != nil || decoded != source {
		t.Fatalf("DecodeSource() = %#v, %v", decoded, err)
	}
}

func TestSourceValidationFailures(t *testing.T) {
	tests := []struct {
		name, field string
		code        ValidationCode
		mutate      func(*Source)
	}{
		{"wrong schema", "schema", ValidationCodeInvalidSchema, func(value *Source) { value.Schema = "mousa.source.v2" }},
		{"zero ID", "id", ValidationCodeInvalidID, func(value *Source) { value.ID = SourceID{} }},
		{"empty namespace", "namespace", ValidationCodeInvalidValue, func(value *Source) { value.Namespace = "" }},
		{"invalid namespace UTF-8", "namespace", ValidationCodeInvalidValue, func(value *Source) { value.Namespace = string([]byte{0xff}) }},
		{"empty external source ID", "external_source_id", ValidationCodeInvalidValue, func(value *Source) { value.ExternalSourceID = "" }},
		{"invalid external source ID UTF-8", "external_source_id", ValidationCodeInvalidValue, func(value *Source) { value.ExternalSourceID = string([]byte{0xff}) }},
		{"mismatched ID", "id", ValidationCodeInvalidID, func(value *Source) { value.ID[0] ^= 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validSource(t)
			test.mutate(&value)
			requireValidationError(t, value.Validate(), test.field, test.code)
		})
	}
}

func TestSourceDecodeFailures(t *testing.T) {
	tests := []struct {
		name, input, field string
		code               ValidationCode
	}{
		{"missing ID", `{"schema":"mousa.source.v1","namespace":"example.mailbox","external_source_id":"account:alpha"}`, "id", ValidationCodeInvalidID},
		{"unknown field", `{"schema":"mousa.source.v1","id":"` + canonicalSourceID + `","namespace":"example.mailbox","external_source_id":"account:alpha","extra":true}`, "extra", ValidationCodeUnknownField},
		{"wrong field type", `{"schema":"mousa.source.v1","id":1,"namespace":"example.mailbox","external_source_id":"account:alpha"}`, "id", ValidationCodeInvalidJSON},
		{"malformed ID", `{"schema":"mousa.source.v1","id":"bad","namespace":"example.mailbox","external_source_id":"account:alpha"}`, "id", ValidationCodeInvalidID},
		{"second value", validSourceJSON + `{}`, "", ValidationCodeTrailingData},
		{"malformed trailing bytes", validSourceJSON + `{`, "", ValidationCodeInvalidJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeSource([]byte(test.input))
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestObservationExactRoundTripAndDeterministicEncoding(t *testing.T) {
	observation := validObservation(t)
	first, err := EncodeObservation(observation)
	if err != nil {
		t.Fatalf("EncodeObservation(): %v", err)
	}
	second, err := EncodeObservation(observation)
	if err != nil {
		t.Fatalf("EncodeObservation() again: %v", err)
	}
	if string(first) != validObservationJSON || !bytes.Equal(first, second) {
		t.Fatalf("EncodeObservation() = %q, second = %q", first, second)
	}
	decoded, err := DecodeObservation(first)
	if err != nil || decoded != observation {
		t.Fatalf("DecodeObservation() = %#v, %v", decoded, err)
	}
}

func TestObservationValidationFailures(t *testing.T) {
	tests := []struct {
		name, field string
		code        ValidationCode
		mutate      func(*Observation)
	}{
		{"wrong schema", "schema", ValidationCodeInvalidSchema, func(value *Observation) { value.Schema = "mousa.observation.v2" }},
		{"zero ID", "id", ValidationCodeInvalidID, func(value *Observation) { value.ID = ObservationID{} }},
		{"zero source ID", "source_id", ValidationCodeInvalidID, func(value *Observation) { value.SourceID = SourceID{} }},
		{"empty external observation ID", "external_observation_id", ValidationCodeInvalidValue, func(value *Observation) { value.ExternalObservationID = "" }},
		{"invalid external observation ID UTF-8", "external_observation_id", ValidationCodeInvalidValue, func(value *Observation) { value.ExternalObservationID = string([]byte{0xff}) }},
		{"mismatched ID", "id", ValidationCodeInvalidID, func(value *Observation) { value.ID[0] ^= 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validObservation(t)
			test.mutate(&value)
			requireValidationError(t, value.Validate(), test.field, test.code)
		})
	}
}

func TestObservationDecodeFailures(t *testing.T) {
	tests := []struct {
		name, input, field string
		code               ValidationCode
	}{
		{"missing ID", `{"schema":"mousa.observation.v1","source_id":"` + canonicalSourceID + `","external_observation_id":"message:42"}`, "id", ValidationCodeInvalidID},
		{"missing source ID", `{"schema":"mousa.observation.v1","id":"` + canonicalObservationID + `","external_observation_id":"message:42"}`, "source_id", ValidationCodeInvalidID},
		{"unknown field", `{"schema":"mousa.observation.v1","id":"` + canonicalObservationID + `","source_id":"` + canonicalSourceID + `","external_observation_id":"message:42","extra":true}`, "extra", ValidationCodeUnknownField},
		{"wrong source ID type", `{"schema":"mousa.observation.v1","id":"` + canonicalObservationID + `","source_id":1,"external_observation_id":"message:42"}`, "source_id", ValidationCodeInvalidJSON},
		{"malformed source ID", `{"schema":"mousa.observation.v1","id":"` + canonicalObservationID + `","source_id":"bad","external_observation_id":"message:42"}`, "source_id", ValidationCodeInvalidID},
		{"second value", validObservationJSON + `[]`, "", ValidationCodeTrailingData},
		{"malformed trailing bytes", validObservationJSON + `[`, "", ValidationCodeInvalidJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeObservation([]byte(test.input))
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestSourceAndObservationRejectInvalidRawUTF8JSON(t *testing.T) {
	for _, decode := range []func([]byte) error{
		func(data []byte) error { _, err := DecodeSource(data); return err },
		func(data []byte) error { _, err := DecodeObservation(data); return err },
	} {
		requireValidationError(t, decode([]byte{'{', '"', 0xff, '"', ':', '1', '}'}), "", ValidationCodeInvalidJSON)
	}
}

func TestValidationErrorPreservesFieldCodeAndCause(t *testing.T) {
	cause := errors.New("cause")
	err := newValidationError("field", ValidationCodeInvalidValue, "message", cause)
	if err.Field != "field" || err.Code != ValidationCodeInvalidValue || !errors.Is(err, cause) {
		t.Fatalf("ValidationError = %#v", err)
	}
}

func validSource(t testing.TB) Source {
	t.Helper()
	id, err := NewSourceID("example.mailbox", "account:alpha")
	if err != nil {
		t.Fatal(err)
	}
	return Source{Schema: SourceSchema, ID: id, Namespace: "example.mailbox", ExternalSourceID: "account:alpha"}
}

func validObservation(t testing.TB) Observation {
	t.Helper()
	source := validSource(t)
	id, err := NewObservationID(source.ID, "message:42")
	if err != nil {
		t.Fatal(err)
	}
	return Observation{Schema: ObservationSchema, ID: id, SourceID: source.ID, ExternalObservationID: "message:42"}
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

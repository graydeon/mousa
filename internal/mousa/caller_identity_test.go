package mousa

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCallerIDMatchesTupleDerivation(t *testing.T) {
	caller := Caller{Schema: CallerSchema, Namespace: "example.harness", ExternalCallerID: "agent:alpha"}
	id, err := NewCallerID(caller.Namespace, caller.ExternalCallerID)
	if err != nil {
		t.Fatalf("NewCallerID(): %v", err)
	}
	caller.ID = id
	derived, err := NewCallerID(caller.Namespace, caller.ExternalCallerID)
	if err != nil {
		t.Fatal(err)
	}
	if caller.ID != derived {
		t.Fatalf("caller ID is not a pure function of its tuple")
	}
	if caller.ID == (CallerID{}) {
		t.Fatal("caller ID is zero")
	}
}

func TestPurposeIDMatchesTupleDerivation(t *testing.T) {
	purpose := Purpose{Schema: PurposeSchema, Namespace: "example.harness", ExternalPurposeID: "task:answer"}
	id, err := NewPurposeID(purpose.Namespace, purpose.ExternalPurposeID)
	if err != nil {
		t.Fatalf("NewPurposeID(): %v", err)
	}
	purpose.ID = id
	if purpose.ID == (PurposeID{}) {
		t.Fatal("purpose ID is zero")
	}
}

func TestCallerAndPurposeIDsDifferAcrossSchemas(t *testing.T) {
	callerID, err := NewCallerID("example.harness", "shared")
	if err != nil {
		t.Fatal(err)
	}
	purposeID, err := NewPurposeID("example.harness", "shared")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(callerID[:], purposeID[:]) {
		t.Fatal("caller and purpose identities collide for the same tuple")
	}
}

func TestIdentityIDRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name, namespace, externalID, callerField, purposeField string
	}{
		{"empty namespace", "", "agent:alpha", "namespace", "namespace"},
		{"invalid namespace UTF-8", string([]byte{0xff}), "agent:alpha", "namespace", "namespace"},
		{"empty external ID", "example.harness", "", "external_caller_id", "external_purpose_id"},
		{"invalid external ID UTF-8", "example.harness", string([]byte{0xff}), "external_caller_id", "external_purpose_id"},
	}
	for _, test := range tests {
		t.Run("caller "+test.name, func(t *testing.T) {
			_, err := NewCallerID(test.namespace, test.externalID)
			requireValidationError(t, err, test.callerField, ValidationCodeInvalidValue)
		})
		t.Run("purpose "+test.name, func(t *testing.T) {
			_, err := NewPurposeID(test.namespace, test.externalID)
			requireValidationError(t, err, test.purposeField, ValidationCodeInvalidValue)
		})
	}
}

func TestCallerRoundTripIsExact(t *testing.T) {
	id, err := NewCallerID("example.harness", "agent:alpha")
	if err != nil {
		t.Fatal(err)
	}
	caller := Caller{Schema: CallerSchema, ID: id, Namespace: "example.harness", ExternalCallerID: "agent:alpha"}
	data, err := EncodeCaller(caller)
	if err != nil {
		t.Fatalf("EncodeCaller(): %v", err)
	}
	decoded, err := DecodeCaller(data)
	if err != nil {
		t.Fatalf("DecodeCaller(): %v", err)
	}
	if decoded != caller {
		t.Fatalf("decoded caller = %+v, want %+v", decoded, caller)
	}
	reencoded, err := EncodeCaller(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reencoded, data) {
		t.Fatal("caller re-encoding is not exact")
	}
	if !strings.Contains(string(data), `"external_caller_id":"agent:alpha"`) {
		t.Fatalf("canonical caller bytes lack the tuple field: %s", data)
	}
}

func TestPurposeRoundTripIsExact(t *testing.T) {
	id, err := NewPurposeID("example.harness", "task:answer")
	if err != nil {
		t.Fatal(err)
	}
	purpose := Purpose{Schema: PurposeSchema, ID: id, Namespace: "example.harness", ExternalPurposeID: "task:answer"}
	data, err := EncodePurpose(purpose)
	if err != nil {
		t.Fatalf("EncodePurpose(): %v", err)
	}
	decoded, err := DecodePurpose(data)
	if err != nil {
		t.Fatalf("DecodePurpose(): %v", err)
	}
	if decoded != purpose {
		t.Fatalf("decoded purpose = %+v, want %+v", decoded, purpose)
	}
}

func TestCallerRejectsInvalidRecords(t *testing.T) {
	id, err := NewCallerID("example.harness", "agent:alpha")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		caller Caller
		field  string
		code   ValidationCode
	}{
		{"wrong schema", Caller{Schema: "mousa.source.v1", ID: id, Namespace: "example.harness", ExternalCallerID: "agent:alpha"}, "schema", ValidationCodeInvalidSchema},
		{"zero ID", Caller{Schema: CallerSchema, Namespace: "example.harness", ExternalCallerID: "agent:alpha"}, "id", ValidationCodeInvalidID},
		{"mismatched ID", Caller{Schema: CallerSchema, ID: mustCallerID(t, "other:beta"), Namespace: "example.harness", ExternalCallerID: "agent:alpha"}, "id", ValidationCodeInvalidID},
		{"empty namespace", Caller{Schema: CallerSchema, ID: id, Namespace: "", ExternalCallerID: "agent:alpha"}, "namespace", ValidationCodeInvalidValue},
		{"empty external caller ID", Caller{Schema: CallerSchema, ID: id, Namespace: "example.harness", ExternalCallerID: ""}, "external_caller_id", ValidationCodeInvalidValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireValidationError(t, test.caller.Validate(), test.field, test.code)
			_, err := EncodeCaller(test.caller)
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestPurposeRejectsInvalidRecords(t *testing.T) {
	id, err := NewPurposeID("example.harness", "task:answer")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		purpose Purpose
		field   string
		code    ValidationCode
	}{
		{"wrong schema", Purpose{Schema: "mousa.caller.v1", ID: id, Namespace: "example.harness", ExternalPurposeID: "task:answer"}, "schema", ValidationCodeInvalidSchema},
		{"zero ID", Purpose{Schema: PurposeSchema, Namespace: "example.harness", ExternalPurposeID: "task:answer"}, "id", ValidationCodeInvalidID},
		{"mismatched ID", Purpose{Schema: PurposeSchema, ID: mustPurposeID(t, "other:review"), Namespace: "example.harness", ExternalPurposeID: "task:answer"}, "id", ValidationCodeInvalidID},
		{"empty external purpose ID", Purpose{Schema: PurposeSchema, ID: id, Namespace: "example.harness", ExternalPurposeID: ""}, "external_purpose_id", ValidationCodeInvalidValue},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireValidationError(t, test.purpose.Validate(), test.field, test.code)
			_, err := EncodePurpose(test.purpose)
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestCallerDecodeRejectsNonCanonicalPayloads(t *testing.T) {
	id, err := NewCallerID("example.harness", "agent:alpha")
	if err != nil {
		t.Fatal(err)
	}
	caller := Caller{Schema: CallerSchema, ID: id, Namespace: "example.harness", ExternalCallerID: "agent:alpha"}
	data, err := EncodeCaller(caller)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("unknown field", func(t *testing.T) {
		poisoned := bytes.Replace(data, []byte(`"schema"`), []byte(`"extra":1,"schema"`), 1)
		_, err := DecodeCaller(poisoned)
		requireValidationError(t, err, "extra", ValidationCodeUnknownField)
	})
	t.Run("trailing data", func(t *testing.T) {
		_, err := DecodeCaller(append(append([]byte(nil), data...), []byte("{}")...))
		requireValidationError(t, err, "", ValidationCodeTrailingData)
	})
	t.Run("duplicate field", func(t *testing.T) {
		duplicated := bytes.Replace(data, []byte(`"schema"`), []byte(`"schema":"mousa.caller.v1","schema"`), 1)
		_, err := DecodeCaller(duplicated)
		requireValidationError(t, err, "", ValidationCodeInvalidJSON)
	})
	t.Run("decode of reordered fields still re-encodes canonically", func(t *testing.T) {
		reordered, err := json.Marshal(struct {
			ID               string `json:"id"`
			Schema           string `json:"schema"`
			Namespace        string `json:"namespace"`
			ExternalCallerID string `json:"external_caller_id"`
		}{ID: id.String(), Schema: CallerSchema, Namespace: "example.harness", ExternalCallerID: "agent:alpha"})
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(reordered, data) {
			t.Fatal("premise: reordered bytes must differ from canonical bytes")
		}
		decoded, err := DecodeCaller(reordered)
		if err != nil {
			t.Fatalf("DecodeCaller(): %v", err)
		}
		reencoded, err := EncodeCaller(decoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(reencoded, data) {
			t.Fatal("re-encoding reordered input did not restore canonical bytes")
		}
	})
	t.Run("uppercase ID", func(t *testing.T) {
		upper := bytes.Replace(data, []byte(id.String()), []byte(strings.ToUpper(id.String())), 1)
		_, err := DecodeCaller(upper)
		requireValidationError(t, err, "id", ValidationCodeInvalidID)
	})
}

func TestPurposeDecodeRejectsNonCanonicalPayloads(t *testing.T) {
	id, err := NewPurposeID("example.harness", "task:answer")
	if err != nil {
		t.Fatal(err)
	}
	purpose := Purpose{Schema: PurposeSchema, ID: id, Namespace: "example.harness", ExternalPurposeID: "task:answer"}
	data, err := EncodePurpose(purpose)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("unknown field", func(t *testing.T) {
		poisoned := bytes.Replace(data, []byte(`"schema"`), []byte(`"extra":1,"schema"`), 1)
		_, err := DecodePurpose(poisoned)
		requireValidationError(t, err, "extra", ValidationCodeUnknownField)
	})
	t.Run("trailing data", func(t *testing.T) {
		_, err := DecodePurpose(append(append([]byte(nil), data...), []byte("{}")...))
		requireValidationError(t, err, "", ValidationCodeTrailingData)
	})
	t.Run("uppercase ID", func(t *testing.T) {
		upper := bytes.Replace(data, []byte(id.String()), []byte(strings.ToUpper(id.String())), 1)
		_, err := DecodePurpose(upper)
		requireValidationError(t, err, "id", ValidationCodeInvalidID)
	})
}

func mustCallerID(t *testing.T, externalCallerID string) CallerID {
	t.Helper()
	id, err := NewCallerID("example.harness", externalCallerID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustPurposeID(t *testing.T, externalPurposeID string) PurposeID {
	t.Helper()
	id, err := NewPurposeID("example.harness", externalPurposeID)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

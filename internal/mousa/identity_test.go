package mousa

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"
)

const (
	canonicalSourceID      = "f111237310ae4db59c528c5e0e53581384059910adab736913c6e29f0ef91d41"
	canonicalObservationID = "8ad5e465541e71ad6246c21fdd9da960736394e1ac4688e034471910784ea12a"
)

func TestSourceIDMatchesCanonicalVector(t *testing.T) {
	got, err := NewSourceID("example.mailbox", "account:alpha")
	if err != nil {
		t.Fatalf("NewSourceID(): %v", err)
	}
	if got.String() != canonicalSourceID {
		t.Fatalf("NewSourceID() = %q, want %q", got.String(), canonicalSourceID)
	}
}

func TestSourceIDRejectsInvalidInputs(t *testing.T) {
	tests := []struct {
		name, namespace, externalSourceID, field string
	}{
		{"empty namespace", "", "account:alpha", "namespace"},
		{"invalid namespace UTF-8", string([]byte{0xff}), "account:alpha", "namespace"},
		{"empty external source ID", "example.mailbox", "", "external_source_id"},
		{"invalid external source ID UTF-8", "example.mailbox", string([]byte{0xff}), "external_source_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSourceID(test.namespace, test.externalSourceID)
			requireValidationError(t, err, test.field, ValidationCodeInvalidValue)
		})
	}
}

func TestSourceIDPreservesExactUTF8Bytes(t *testing.T) {
	composedText := string([]byte{'c', 'a', 'f', 0xc3, 0xa9})
	decomposedText := string([]byte{'c', 'a', 'f', 'e', 0xcc, 0x81})
	composed, err := NewSourceID("example.mailbox", composedText)
	if err != nil {
		t.Fatal(err)
	}
	decomposed, err := NewSourceID("example.mailbox", decomposedText)
	if err != nil {
		t.Fatal(err)
	}
	spaced, err := NewSourceID("example.mailbox", " "+composedText+" ")
	if err != nil {
		t.Fatal(err)
	}
	if composed == decomposed || composed == spaced {
		t.Fatal("NewSourceID normalized distinct valid UTF-8 inputs")
	}
}

func TestSourceIDStrictParsingAndJSON(t *testing.T) {
	testStrictID(t, canonicalSourceID, func(value string) (string, error) {
		id, err := ParseSourceID(value)
		return id.String(), err
	}, func(data []byte) (string, error) {
		var id SourceID
		err := json.Unmarshal(data, &id)
		return id.String(), err
	})
}

func TestObservationIDMatchesCanonicalVector(t *testing.T) {
	sourceID := mustParseSourceID(t, canonicalSourceID)
	got, err := NewObservationID(sourceID, "message:42")
	if err != nil {
		t.Fatalf("NewObservationID(): %v", err)
	}
	if got.String() != canonicalObservationID {
		t.Fatalf("NewObservationID() = %q, want %q", got.String(), canonicalObservationID)
	}
}

func TestObservationIDRejectsInvalidInputs(t *testing.T) {
	sourceID := mustParseSourceID(t, canonicalSourceID)
	for _, value := range []string{"", string([]byte{0xff})} {
		_, err := NewObservationID(sourceID, value)
		requireValidationError(t, err, "external_observation_id", ValidationCodeInvalidValue)
	}
}

func TestObservationIDStrictParsingAndJSON(t *testing.T) {
	testStrictID(t, canonicalObservationID, func(value string) (string, error) {
		id, err := ParseObservationID(value)
		return id.String(), err
	}, func(data []byte) (string, error) {
		var id ObservationID
		err := json.Unmarshal(data, &id)
		return id.String(), err
	})
}

func TestSourceIDAndObservationIDAreDeterministic(t *testing.T) {
	firstSource, err := NewSourceID("example.mailbox", "account:alpha")
	if err != nil {
		t.Fatal(err)
	}
	secondSource, err := NewSourceID("example.mailbox", "account:alpha")
	if err != nil {
		t.Fatal(err)
	}
	if firstSource != secondSource {
		t.Fatal("repeated source identity generation differed")
	}
	firstObservation, err := NewObservationID(firstSource, "message:42")
	if err != nil {
		t.Fatal(err)
	}
	secondObservation, err := NewObservationID(firstSource, "message:42")
	if err != nil {
		t.Fatal(err)
	}
	if firstObservation != secondObservation {
		t.Fatal("repeated observation identity generation differed")
	}
}

func TestSHA256StrictParsingAndJSON(t *testing.T) {
	const valid = "1a989ea86150171c687b0727f218eedbb94c4665a7da9b0add1bf5de607f2bf1"
	digest, err := ParseSHA256(valid)
	if err != nil {
		t.Fatalf("ParseSHA256(valid): %v", err)
	}
	encoded, err := json.Marshal(digest)
	if err != nil || string(encoded) != `"`+valid+`"` {
		t.Fatalf("json.Marshal() = %s, %v", encoded, err)
	}
	for _, input := range invalidHexValues(valid) {
		_, err := ParseSHA256(input)
		requireValidationError(t, err, "sha256", ValidationCodeInvalidDigest)
	}
	var decoded SHA256
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded != digest {
		t.Fatalf("json.Unmarshal(valid) = %s, %v", decoded, err)
	}
	if err := json.Unmarshal([]byte(`123`), &decoded); err == nil {
		t.Fatal("json.Unmarshal(number) succeeded")
	}
}

func TestTupleEncodingDistinguishesFieldBoundaries(t *testing.T) {
	left := sha256.New()
	writeTuple(left, []byte("ab"), []byte("c"))
	right := sha256.New()
	writeTuple(right, []byte("a"), []byte("bc"))
	if string(left.Sum(nil)) == string(right.Sum(nil)) {
		t.Fatal("length-prefixed tuple encoding collided across field boundaries")
	}
}

func testStrictID(t testing.TB, valid string, parse func(string) (string, error), unmarshal func([]byte) (string, error)) {
	t.Helper()
	got, err := parse(valid)
	if err != nil || got != valid {
		t.Fatalf("parse(valid) = %q, %v", got, err)
	}
	for _, input := range invalidHexValues(valid) {
		_, err := parse(input)
		requireValidationError(t, err, "id", ValidationCodeInvalidID)
	}
	got, err = unmarshal([]byte(`"` + valid + `"`))
	if err != nil || got != valid {
		t.Fatalf("unmarshal(valid) = %q, %v", got, err)
	}
	for _, data := range [][]byte{[]byte(`null`), []byte(`1`), []byte(`{}`), []byte(`[]`), []byte(`"` + strings.ToUpper(valid) + `"`)} {
		_, err := unmarshal(data)
		requireValidationError(t, err, "id", ValidationCodeInvalidID)
	}
}

func invalidHexValues(valid string) []string {
	return []string{strings.ToUpper(valid), valid[:63], valid + "0", strings.Repeat("g", 64)}
}

func mustParseSourceID(t testing.TB, value string) SourceID {
	t.Helper()
	id, err := ParseSourceID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

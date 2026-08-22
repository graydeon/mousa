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

func TestArtifactIDMatchesCanonicalVector(t *testing.T) {
	observationID, err := ParseObservationID(canonicalObservationID)
	if err != nil {
		t.Fatal(err)
	}
	var got ArtifactID
	got, err = NewArtifactID(observationID, "raw-message")
	if err != nil {
		t.Fatalf("NewArtifactID(): %v", err)
	}
	const want = "4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839"
	if got.String() != want {
		t.Fatalf("NewArtifactID() = %q, want %q", got.String(), want)
	}
}

func TestArtifactIDRejectsInvalidArtifactKey(t *testing.T) {
	observationID := mustParseObservationID(t, canonicalObservationID)
	for _, value := range []string{"", string([]byte{0xff})} {
		_, err := NewArtifactID(observationID, value)
		requireValidationError(t, err, "artifact_key", ValidationCodeInvalidValue)
	}
}

func TestArtifactIDPreservesExactUTF8Bytes(t *testing.T) {
	observationID := mustParseObservationID(t, canonicalObservationID)
	composed, err := NewArtifactID(observationID, "caf\u00e9")
	if err != nil {
		t.Fatal(err)
	}
	decomposed, err := NewArtifactID(observationID, "cafe\u0301")
	if err != nil {
		t.Fatal(err)
	}
	spaced, err := NewArtifactID(observationID, " caf\u00e9 ")
	if err != nil {
		t.Fatal(err)
	}
	if composed == decomposed || composed == spaced {
		t.Fatal("NewArtifactID normalized distinct valid UTF-8 inputs")
	}
}

func TestArtifactIDChangesWithObservation(t *testing.T) {
	first := mustParseObservationID(t, canonicalObservationID)
	second := first
	second[0] ^= 1
	firstID, err := NewArtifactID(first, "raw-message")
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := NewArtifactID(second, "raw-message")
	if err != nil {
		t.Fatal(err)
	}
	if firstID == secondID {
		t.Fatal("different Observation IDs produced the same Artifact ID")
	}
}

func TestArtifactIDStrictParsingAndJSON(t *testing.T) {
	const valid = "4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839"
	testStrictID(t, valid, func(value string) (string, error) {
		id, err := ParseArtifactID(value)
		return id.String(), err
	}, func(data []byte) (string, error) {
		var id ArtifactID
		err := json.Unmarshal(data, &id)
		return id.String(), err
	})
}

func TestRepresentationIDMatchesCanonicalVector(t *testing.T) {
	artifactID, err := ParseArtifactID("4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839")
	if err != nil {
		t.Fatal(err)
	}
	parametersSHA256, err := ParseSHA256("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if err != nil {
		t.Fatal(err)
	}
	contentSHA256, err := ParseSHA256("f0df22d2bcadf69b874b74b12a790a236cf590850b779e427dcdebd4d464ae3d")
	if err != nil {
		t.Fatal(err)
	}
	var got RepresentationID
	got, err = NewRepresentationID(
		[]DerivationInput{NewArtifactDerivationInput(artifactID)},
		"example.text-extractor",
		"1.0.0",
		parametersSHA256,
		"text/plain; charset=utf-8",
		contentSHA256,
	)
	if err != nil {
		t.Fatalf("NewRepresentationID(): %v", err)
	}
	const want = "c30b39a8e1595804b964dfb86b4ce16f8fbcffe0af4c65d0c23742c1cc6774ae"
	if got.String() != want {
		t.Fatalf("NewRepresentationID() = %q, want %q", got.String(), want)
	}
}

func TestSegmentIDMatchesCanonicalVector(t *testing.T) {
	representationID, err := ParseRepresentationID("c30b39a8e1595804b964dfb86b4ce16f8fbcffe0af4c65d0c23742c1cc6774ae")
	if err != nil {
		t.Fatal(err)
	}
	contentSHA256, err := ParseSHA256("185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969")
	if err != nil {
		t.Fatal(err)
	}
	var got SegmentID
	got, err = NewSegmentID(representationID, NewTextByteRangeSelector(0, 5), contentSHA256)
	if err != nil {
		t.Fatalf("NewSegmentID(): %v", err)
	}
	const want = "a454fa6d452787a2ec079bb3f453a9842a9a7d36da099123676093247e423492"
	if got.String() != want {
		t.Fatalf("NewSegmentID() = %q, want %q", got.String(), want)
	}
}

func TestRepresentationIDStrictParsingAndJSON(t *testing.T) {
	const valid = "c30b39a8e1595804b964dfb86b4ce16f8fbcffe0af4c65d0c23742c1cc6774ae"
	testStrictID(t, valid, func(value string) (string, error) {
		id, err := ParseRepresentationID(value)
		return id.String(), err
	}, func(data []byte) (string, error) {
		var id RepresentationID
		err := json.Unmarshal(data, &id)
		return id.String(), err
	})
}

func TestSegmentIDStrictParsingAndJSON(t *testing.T) {
	const valid = "a454fa6d452787a2ec079bb3f453a9842a9a7d36da099123676093247e423492"
	testStrictID(t, valid, func(value string) (string, error) {
		id, err := ParseSegmentID(value)
		return id.String(), err
	}, func(data []byte) (string, error) {
		var id SegmentID
		err := json.Unmarshal(data, &id)
		return id.String(), err
	})
}

func TestRepresentationIDIncludesEveryDerivationField(t *testing.T) {
	artifactID, err := ParseArtifactID("4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839")
	if err != nil {
		t.Fatal(err)
	}
	secondArtifactID := artifactID
	secondArtifactID[0] ^= 1
	parametersSHA256, err := ParseSHA256("e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	if err != nil {
		t.Fatal(err)
	}
	contentSHA256, err := ParseSHA256("f0df22d2bcadf69b874b74b12a790a236cf590850b779e427dcdebd4d464ae3d")
	if err != nil {
		t.Fatal(err)
	}
	baseInputs := []DerivationInput{
		NewArtifactDerivationInput(artifactID),
		NewArtifactDerivationInput(secondArtifactID),
	}
	base, err := NewRepresentationID(baseInputs, "example.text-extractor", "1.0.0", parametersSHA256, UTF8TextMediaType, contentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	changedParameters := parametersSHA256
	changedParameters[0] ^= 1
	changedContent := contentSHA256
	changedContent[0] ^= 1
	tests := []struct {
		name             string
		inputs           []DerivationInput
		processorID      string
		processorVersion string
		parameters       SHA256
		mediaType        string
		content          SHA256
	}{
		{"input kind", []DerivationInput{NewRepresentationDerivationInput(RepresentationID(artifactID)), NewArtifactDerivationInput(secondArtifactID)}, "example.text-extractor", "1.0.0", parametersSHA256, UTF8TextMediaType, contentSHA256},
		{"input ID", []DerivationInput{NewArtifactDerivationInput(secondArtifactID), NewArtifactDerivationInput(secondArtifactID)}, "example.text-extractor", "1.0.0", parametersSHA256, UTF8TextMediaType, contentSHA256},
		{"input order", []DerivationInput{NewArtifactDerivationInput(secondArtifactID), NewArtifactDerivationInput(artifactID)}, "example.text-extractor", "1.0.0", parametersSHA256, UTF8TextMediaType, contentSHA256},
		{"processor ID", baseInputs, "example.other-extractor", "1.0.0", parametersSHA256, UTF8TextMediaType, contentSHA256},
		{"processor version", baseInputs, "example.text-extractor", "1.0.1", parametersSHA256, UTF8TextMediaType, contentSHA256},
		{"parameter digest", baseInputs, "example.text-extractor", "1.0.0", changedParameters, UTF8TextMediaType, contentSHA256},
		{"media type", baseInputs, "example.text-extractor", "1.0.0", parametersSHA256, "text/markdown", contentSHA256},
		{"output digest", baseInputs, "example.text-extractor", "1.0.0", parametersSHA256, UTF8TextMediaType, changedContent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NewRepresentationID(test.inputs, test.processorID, test.processorVersion, test.parameters, test.mediaType, test.content)
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatalf("changing %s did not change Representation ID", test.name)
			}
		})
	}
}

func TestRepresentationIDPreservesExactUTF8Bytes(t *testing.T) {
	artifactID := mustParseArtifactID(t, "4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839")
	parametersSHA256 := mustParseSHA256(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	contentSHA256 := mustParseSHA256(t, "f0df22d2bcadf69b874b74b12a790a236cf590850b779e427dcdebd4d464ae3d")
	inputs := []DerivationInput{NewArtifactDerivationInput(artifactID)}
	base, err := NewRepresentationID(inputs, "caf\u00e9", "versi\u00f3n", parametersSHA256, "text/pl\u00e1in", contentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		values [3]string
	}{
		{"processor ID", [3]string{"cafe\u0301", "versi\u00f3n", "text/pl\u00e1in"}},
		{"processor version", [3]string{"caf\u00e9", "versio\u0301n", "text/pl\u00e1in"}},
		{"media type", [3]string{"caf\u00e9", "versi\u00f3n", "text/pla\u0301in"}},
	} {
		name, values := test.name, test.values
		got, err := NewRepresentationID(inputs, values[0], values[1], parametersSHA256, values[2], contentSHA256)
		if err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Fatalf("%s was Unicode-normalized", name)
		}
	}
}

func TestIdentityConstructorsRejectInvalidEvidence(t *testing.T) {
	artifactID := mustParseArtifactID(t, "4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839")
	digest := mustParseSHA256(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	validInputs := []DerivationInput{NewArtifactDerivationInput(artifactID)}
	for _, test := range []struct {
		name, field string
		code        ValidationCode
		inputs      []DerivationInput
		processorID string
		version     string
		parameters  SHA256
		mediaType   string
		content     SHA256
	}{
		{"empty inputs", "inputs", ValidationCodeInvalidValue, nil, "processor", "1", digest, "text/plain", digest},
		{"unknown input kind", "inputs[0].kind", ValidationCodeInvalidEnum, []DerivationInput{{}}, "processor", "1", digest, "text/plain", digest},
		{"zero artifact input", "inputs[0].id", ValidationCodeInvalidID, []DerivationInput{NewArtifactDerivationInput(ArtifactID{})}, "processor", "1", digest, "text/plain", digest},
		{"empty processor", "processor_id", ValidationCodeInvalidValue, validInputs, "", "1", digest, "text/plain", digest},
		{"invalid version UTF-8", "processor_version", ValidationCodeInvalidValue, validInputs, "processor", string([]byte{0xff}), digest, "text/plain", digest},
		{"zero parameters", "parameters_sha256", ValidationCodeInvalidDigest, validInputs, "processor", "1", SHA256{}, "text/plain", digest},
		{"empty media type", "media_type", ValidationCodeInvalidValue, validInputs, "processor", "1", digest, "", digest},
		{"zero content", "content_sha256", ValidationCodeInvalidDigest, validInputs, "processor", "1", digest, "text/plain", SHA256{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRepresentationID(test.inputs, test.processorID, test.version, test.parameters, test.mediaType, test.content)
			requireValidationError(t, err, test.field, test.code)
		})
	}
	representationID := mustParseRepresentationID(t, "c30b39a8e1595804b964dfb86b4ce16f8fbcffe0af4c65d0c23742c1cc6774ae")
	_, err := NewSegmentID(representationID, NewTextByteRangeSelector(5, 5), digest)
	requireValidationError(t, err, "selector.end", ValidationCodeInvalidRange)
	_, err = NewSegmentID(representationID, NewTextByteRangeSelector(6, 5), digest)
	requireValidationError(t, err, "selector.end", ValidationCodeInvalidRange)
	_, err = NewSegmentID(representationID, NewTextByteRangeSelector(0, 1), SHA256{})
	requireValidationError(t, err, "content_sha256", ValidationCodeInvalidDigest)
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

func mustParseObservationID(t testing.TB, value string) ObservationID {
	t.Helper()
	id, err := ParseObservationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustParseArtifactID(t testing.TB, value string) ArtifactID {
	t.Helper()
	id, err := ParseArtifactID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustParseRepresentationID(t testing.TB, value string) RepresentationID {
	t.Helper()
	id, err := ParseRepresentationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustParseSHA256(t testing.TB, value string) SHA256 {
	t.Helper()
	digest, err := ParseSHA256(value)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

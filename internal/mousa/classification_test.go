package mousa

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const (
	canonicalClassificationID = "e2219c6503abb262adf8526af12cc96bd9cd62e99e2ba6be89628695bf3c7b66"
	validClassificationJSON   = "{\"schema\":\"mousa.classification.v1\",\"id\":\"e2219c6503abb262adf8526af12cc96bd9cd62e99e2ba6be89628695bf3c7b66\",\"subject\":{\"kind\":\"segment\",\"id\":\"a454fa6d452787a2ec079bb3f453a9842a9a7d36da099123676093247e423492\"},\"taxonomy\":\"example.sensitivity\",\"taxonomy_version\":\"2026-08-24\",\"label\":\"restricted\",\"asserter_id\":\"example.classifier\",\"asserter_version\":\"1.0.0\",\"basis\":[{\"kind\":\"source\",\"id\":\"f111237310ae4db59c528c5e0e53581384059910adab736913c6e29f0ef91d41\"},{\"kind\":\"observation\",\"id\":\"8ad5e465541e71ad6246c21fdd9da960736394e1ac4688e034471910784ea12a\"}],\"asserted_at_usec\":1724544000123456,\"confidence_ppm\":875000}\n"
)

func TestClassificationExactRoundTripAndIdentity(t *testing.T) {
	subject := NewSegmentClassificationSubject(mustParseSegmentID(t, "a454fa6d452787a2ec079bb3f453a9842a9a7d36da099123676093247e423492"))
	basis := []ClassificationSubject{
		NewSourceClassificationSubject(mustParseSourceID(t, canonicalSourceID)),
		NewObservationClassificationSubject(mustParseObservationID(t, canonicalObservationID)),
	}
	id, err := NewClassificationID(subject, "example.sensitivity", "2026-08-24", "restricted", "example.classifier", "1.0.0", basis, 1724544000123456, 875000)
	if err != nil {
		t.Fatalf("NewClassificationID(): %v", err)
	}
	if id.String() != canonicalClassificationID {
		t.Fatalf("NewClassificationID() = %q, want %q", id.String(), canonicalClassificationID)
	}
	record := Classification{
		Schema:          ClassificationSchema,
		ID:              id,
		Subject:         subject,
		Taxonomy:        "example.sensitivity",
		TaxonomyVersion: "2026-08-24",
		Label:           "restricted",
		AsserterID:      "example.classifier",
		AsserterVersion: "1.0.0",
		Basis:           basis,
		AssertedAtUsec:  1724544000123456,
		ConfidencePPM:   875000,
	}
	first, err := EncodeClassification(record)
	if err != nil {
		t.Fatalf("EncodeClassification(): %v", err)
	}
	second, err := EncodeClassification(record)
	if err != nil {
		t.Fatalf("EncodeClassification() again: %v", err)
	}
	if string(first) != validClassificationJSON || !bytes.Equal(first, second) {
		t.Fatalf("EncodeClassification() = %q, second = %q", first, second)
	}
	decoded, err := DecodeClassification(first)
	if err != nil || !reflect.DeepEqual(decoded, record) {
		t.Fatalf("DecodeClassification() = %#v, %v", decoded, err)
	}
}

func TestClassificationRejectsInvalidAndNonCanonical(t *testing.T) {
	record := validClassification(t)
	testStrictID(t, canonicalClassificationID, func(value string) (string, error) {
		id, err := ParseClassificationID(value)
		return id.String(), err
	}, func(data []byte) (string, error) {
		var id ClassificationID
		err := json.Unmarshal(data, &id)
		return id.String(), err
	})

	subjects := []struct {
		kind  ClassificationSubjectKind
		value ClassificationSubject
	}{
		{ClassificationSubjectSource, NewSourceClassificationSubject(mustParseSourceID(t, canonicalSourceID))},
		{ClassificationSubjectObservation, NewObservationClassificationSubject(mustParseObservationID(t, canonicalObservationID))},
		{ClassificationSubjectArtifact, NewArtifactClassificationSubject(mustParseArtifactID(t, "4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839"))},
		{ClassificationSubjectRepresentation, NewRepresentationClassificationSubject(mustParseRepresentationID(t, "c30b39a8e1595804b964dfb86b4ce16f8fbcffe0af4c65d0c23742c1cc6774ae"))},
		{ClassificationSubjectSegment, NewSegmentClassificationSubject(mustParseSegmentID(t, "a454fa6d452787a2ec079bb3f453a9842a9a7d36da099123676093247e423492"))},
	}
	for _, test := range subjects {
		t.Run(string(test.kind), func(t *testing.T) {
			data, err := json.Marshal(test.value)
			if err != nil {
				t.Fatal(err)
			}
			var decoded ClassificationSubject
			if err := json.Unmarshal(data, &decoded); err != nil || decoded != test.value || decoded.Kind() != test.kind {
				t.Fatalf("subject round trip = %#v, %v", decoded, err)
			}
			_, source := decoded.SourceID()
			_, observation := decoded.ObservationID()
			_, artifact := decoded.ArtifactID()
			_, representation := decoded.RepresentationID()
			_, segment := decoded.SegmentID()
			got := []bool{source, observation, artifact, representation, segment}
			wantIndex := map[ClassificationSubjectKind]int{ClassificationSubjectSource: 0, ClassificationSubjectObservation: 1, ClassificationSubjectArtifact: 2, ClassificationSubjectRepresentation: 3, ClassificationSubjectSegment: 4}[test.kind]
			for index, ok := range got {
				if ok != (index == wantIndex) {
					t.Fatalf("accessors = %v", got)
				}
			}
		})
	}

	for _, test := range []struct {
		name, input, field string
		code               ValidationCode
	}{
		{"unknown field", strings.TrimSuffix(validClassificationJSON, "}\n") + ",\"extra\":true}\n", "extra", ValidationCodeUnknownField},
		{"unknown subject field", strings.Replace(validClassificationJSON, `"kind":"segment","id":`, `"kind":"segment","extra":true,"id":`, 1), "subject.extra", ValidationCodeUnknownField},
		{"unknown subject kind", strings.Replace(validClassificationJSON, `"kind":"segment"`, `"kind":"claim"`, 1), "subject.kind", ValidationCodeInvalidEnum},
		{"malformed subject ID", strings.Replace(validClassificationJSON, "a454fa6d452787a2ec079bb3f453a9842a9a7d36da099123676093247e423492", "bad", 1), "subject.id", ValidationCodeInvalidID},
		{"zero subject ID", strings.Replace(validClassificationJSON, "a454fa6d452787a2ec079bb3f453a9842a9a7d36da099123676093247e423492", strings.Repeat("0", 64), 1), "subject.id", ValidationCodeInvalidID},
		{"wrong basis kind type", strings.Replace(validClassificationJSON, `"kind":"source"`, `"kind":1`, 1), "basis[0].kind", ValidationCodeInvalidJSON},
		{"trailing value", validClassificationJSON + `{}`, "", ValidationCodeTrailingData},
		{"malformed trailing", validClassificationJSON + `{`, "", ValidationCodeInvalidJSON},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeClassification([]byte(test.input))
			requireValidationError(t, err, test.field, test.code)
		})
	}
	_, err := DecodeClassification([]byte{'{', '"', 0xff, '"', ':', '1', '}'})
	requireValidationError(t, err, "", ValidationCodeInvalidJSON)

	for _, test := range []struct {
		name, field string
		code        ValidationCode
		mutate      func(*Classification)
	}{
		{"schema", "schema", ValidationCodeInvalidSchema, func(value *Classification) { value.Schema = "wrong" }},
		{"zero ID", "id", ValidationCodeInvalidID, func(value *Classification) { value.ID = ClassificationID{} }},
		{"unknown subject", "subject.kind", ValidationCodeInvalidEnum, func(value *Classification) { value.Subject = ClassificationSubject{} }},
		{"zero subject", "subject.id", ValidationCodeInvalidID, func(value *Classification) { value.Subject = NewSourceClassificationSubject(SourceID{}) }},
		{"empty taxonomy", "taxonomy", ValidationCodeInvalidValue, func(value *Classification) { value.Taxonomy = "" }},
		{"invalid UTF-8", "taxonomy", ValidationCodeInvalidValue, func(value *Classification) { value.Taxonomy = string([]byte{0xff}) }},
		{"empty taxonomy version", "taxonomy_version", ValidationCodeInvalidValue, func(value *Classification) { value.TaxonomyVersion = "" }},
		{"empty label", "label", ValidationCodeInvalidValue, func(value *Classification) { value.Label = "" }},
		{"empty asserter", "asserter_id", ValidationCodeInvalidValue, func(value *Classification) { value.AsserterID = "" }},
		{"empty asserter version", "asserter_version", ValidationCodeInvalidValue, func(value *Classification) { value.AsserterVersion = "" }},
		{"empty basis", "basis", ValidationCodeInvalidValue, func(value *Classification) { value.Basis = nil }},
		{"oversized basis", "basis", ValidationCodeInvalidValue, func(value *Classification) { value.Basis = make([]ClassificationSubject, 4097) }},
		{"duplicate basis", "basis[1]", ValidationCodeInvalidValue, func(value *Classification) { value.Basis[1] = value.Basis[0] }},
		{"zero basis", "basis[0].id", ValidationCodeInvalidID, func(value *Classification) { value.Basis[0] = NewArtifactClassificationSubject(ArtifactID{}) }},
		{"zero time", "asserted_at_usec", ValidationCodeInvalidValue, func(value *Classification) { value.AssertedAtUsec = 0 }},
		{"negative time", "asserted_at_usec", ValidationCodeInvalidValue, func(value *Classification) { value.AssertedAtUsec = -1 }},
		{"confidence", "confidence_ppm", ValidationCodeInvalidRange, func(value *Classification) { value.ConfidencePPM = 1_000_001 }},
		{"identity", "id", ValidationCodeInvalidID, func(value *Classification) { value.ID[0] ^= 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := record
			value.Basis = append([]ClassificationSubject(nil), record.Basis...)
			test.mutate(&value)
			requireValidationError(t, value.Validate(), test.field, test.code)
		})
	}

	base := record.ID
	mutations := []func(*Classification){
		func(value *Classification) { value.Subject = subjects[0].value },
		func(value *Classification) { value.Taxonomy = "other" },
		func(value *Classification) { value.TaxonomyVersion = "other" },
		func(value *Classification) { value.Label = "other" },
		func(value *Classification) { value.AsserterID = "other" },
		func(value *Classification) { value.AsserterVersion = "other" },
		func(value *Classification) { value.Basis[0], value.Basis[1] = value.Basis[1], value.Basis[0] },
		func(value *Classification) { value.Basis = value.Basis[:1] },
		func(value *Classification) { value.AssertedAtUsec++ },
		func(value *Classification) { value.ConfidencePPM++ },
	}
	for index, mutate := range mutations {
		value := record
		value.Basis = append([]ClassificationSubject(nil), record.Basis...)
		mutate(&value)
		id, err := NewClassificationID(value.Subject, value.Taxonomy, value.TaxonomyVersion, value.Label, value.AsserterID, value.AsserterVersion, value.Basis, value.AssertedAtUsec, value.ConfidencePPM)
		if err != nil || id == base {
			t.Fatalf("identity discriminator %d = %s, %v", index, id, err)
		}
	}
	spaced := record
	spaced.Taxonomy = " " + record.Taxonomy + " "
	spaced.ID, err = NewClassificationID(spaced.Subject, spaced.Taxonomy, spaced.TaxonomyVersion, spaced.Label, spaced.AsserterID, spaced.AsserterVersion, spaced.Basis, spaced.AssertedAtUsec, spaced.ConfidencePPM)
	if err != nil || spaced.ID == record.ID {
		t.Fatalf("spaced identity = %s, %v", spaced.ID, err)
	}
}

func validClassification(t testing.TB) Classification {
	t.Helper()
	subject := NewSegmentClassificationSubject(mustParseSegmentID(t, "a454fa6d452787a2ec079bb3f453a9842a9a7d36da099123676093247e423492"))
	basis := []ClassificationSubject{NewSourceClassificationSubject(mustParseSourceID(t, canonicalSourceID)), NewObservationClassificationSubject(mustParseObservationID(t, canonicalObservationID))}
	id, err := NewClassificationID(subject, "example.sensitivity", "2026-08-24", "restricted", "example.classifier", "1.0.0", basis, 1724544000123456, 875000)
	if err != nil {
		t.Fatal(err)
	}
	return Classification{Schema: ClassificationSchema, ID: id, Subject: subject, Taxonomy: "example.sensitivity", TaxonomyVersion: "2026-08-24", Label: "restricted", AsserterID: "example.classifier", AsserterVersion: "1.0.0", Basis: basis, AssertedAtUsec: 1724544000123456, ConfidencePPM: 875000}
}

func mustParseSegmentID(t testing.TB, value string) SegmentID {
	t.Helper()
	id, err := ParseSegmentID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

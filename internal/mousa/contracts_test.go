package mousa

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	validSourceJSON      = "{\"schema\":\"mousa.source.v1\",\"id\":\"f111237310ae4db59c528c5e0e53581384059910adab736913c6e29f0ef91d41\",\"namespace\":\"example.mailbox\",\"external_source_id\":\"account:alpha\"}\n"
	validObservationJSON = "{\"schema\":\"mousa.observation.v1\",\"id\":\"8ad5e465541e71ad6246c21fdd9da960736394e1ac4688e034471910784ea12a\",\"source_id\":\"f111237310ae4db59c528c5e0e53581384059910adab736913c6e29f0ef91d41\",\"external_observation_id\":\"message:42\"}\n"
)

const (
	validArtifactJSON       = "{\"schema\":\"mousa.artifact.v1\",\"id\":\"4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839\",\"observation_id\":\"8ad5e465541e71ad6246c21fdd9da960736394e1ac4688e034471910784ea12a\",\"artifact_key\":\"raw-message\",\"media_type\":\"message/rfc822\",\"content_sha256\":\"fc02a0cd21f2db893e609e488ef366fa232b9b0788a7d12ae2a42b25460a2421\",\"byte_length\":12}\n"
	validRepresentationJSON = "{\"schema\":\"mousa.representation.v1\",\"id\":\"c30b39a8e1595804b964dfb86b4ce16f8fbcffe0af4c65d0c23742c1cc6774ae\",\"inputs\":[{\"kind\":\"artifact\",\"id\":\"4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839\"}],\"processor_id\":\"example.text-extractor\",\"processor_version\":\"1.0.0\",\"parameters_sha256\":\"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855\",\"media_type\":\"text/plain; charset=utf-8\",\"content_sha256\":\"f0df22d2bcadf69b874b74b12a790a236cf590850b779e427dcdebd4d464ae3d\",\"byte_length\":14}\n"
	validSegmentJSON        = "{\"schema\":\"mousa.segment.v1\",\"id\":\"a454fa6d452787a2ec079bb3f453a9842a9a7d36da099123676093247e423492\",\"representation_id\":\"c30b39a8e1595804b964dfb86b4ce16f8fbcffe0af4c65d0c23742c1cc6774ae\",\"selector\":{\"schema\":\"mousa.selector.text_byte_range.v1\",\"start\":0,\"end\":5},\"content_sha256\":\"185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969\"}\n"
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

func TestArtifactExactRoundTripAndDeterministicEncoding(t *testing.T) {
	artifact := validArtifact(t)
	first, err := EncodeArtifact(artifact)
	if err != nil {
		t.Fatalf("EncodeArtifact(): %v", err)
	}
	second, err := EncodeArtifact(artifact)
	if err != nil {
		t.Fatalf("EncodeArtifact() again: %v", err)
	}
	if string(first) != validArtifactJSON || !bytes.Equal(first, second) {
		t.Fatalf("EncodeArtifact() = %q, second = %q", first, second)
	}
	decoded, err := DecodeArtifact(first)
	if err != nil || decoded != artifact {
		t.Fatalf("DecodeArtifact() = %#v, %v", decoded, err)
	}
}

func TestRepresentationExactRoundTripAndDeterministicEncoding(t *testing.T) {
	representation := validRepresentation(t)
	first, err := EncodeRepresentation(representation)
	if err != nil {
		t.Fatalf("EncodeRepresentation(): %v", err)
	}
	second, err := EncodeRepresentation(representation)
	if err != nil {
		t.Fatalf("EncodeRepresentation() again: %v", err)
	}
	if string(first) != validRepresentationJSON || !bytes.Equal(first, second) {
		t.Fatalf("EncodeRepresentation() = %q, second = %q", first, second)
	}
	decoded, err := DecodeRepresentation(first)
	if err != nil || !reflect.DeepEqual(decoded, representation) {
		t.Fatalf("DecodeRepresentation() = %#v, %v", decoded, err)
	}
}

func TestSegmentExactRoundTripAndDeterministicEncoding(t *testing.T) {
	segment := validSegment(t)
	first, err := EncodeSegment(segment)
	if err != nil {
		t.Fatalf("EncodeSegment(): %v", err)
	}
	second, err := EncodeSegment(segment)
	if err != nil {
		t.Fatalf("EncodeSegment() again: %v", err)
	}
	if string(first) != validSegmentJSON || !bytes.Equal(first, second) {
		t.Fatalf("EncodeSegment() = %q, second = %q", first, second)
	}
	decoded, err := DecodeSegment(first)
	if err != nil || decoded != segment {
		t.Fatalf("DecodeSegment() = %#v, %v", decoded, err)
	}
}

func TestArtifactValidationOrderAndIdentityBoundary(t *testing.T) {
	tests := []struct {
		name, field string
		code        ValidationCode
		mutate      func(*Artifact)
	}{
		{"schema", "schema", ValidationCodeInvalidSchema, func(value *Artifact) { value.Schema = "wrong" }},
		{"ID", "id", ValidationCodeInvalidID, func(value *Artifact) { value.ID = ArtifactID{} }},
		{"Observation ID", "observation_id", ValidationCodeInvalidID, func(value *Artifact) { value.ObservationID = ObservationID{} }},
		{"artifact key", "artifact_key", ValidationCodeInvalidValue, func(value *Artifact) { value.ArtifactKey = "" }},
		{"media type", "media_type", ValidationCodeInvalidValue, func(value *Artifact) { value.MediaType = string([]byte{0xff}) }},
		{"content digest", "content_sha256", ValidationCodeInvalidDigest, func(value *Artifact) { value.ContentSHA256 = SHA256{} }},
		{"identity mismatch", "id", ValidationCodeInvalidID, func(value *Artifact) { value.ID[0] ^= 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validArtifact(t)
			test.mutate(&value)
			requireValidationError(t, value.Validate(), test.field, test.code)
		})
	}
	value := validArtifact(t)
	originalID := value.ID
	value.MediaType = "application/octet-stream"
	value.ContentSHA256[0] ^= 1
	value.ByteLength = 0
	if err := value.Validate(); err != nil || value.ID != originalID {
		t.Fatalf("Artifact evidence altered identity: ID=%s error=%v", value.ID, err)
	}
}

func TestRepresentationValidationOrder(t *testing.T) {
	tests := []struct {
		name, field string
		code        ValidationCode
		mutate      func(*Representation)
	}{
		{"schema", "schema", ValidationCodeInvalidSchema, func(value *Representation) { value.Schema = "wrong" }},
		{"ID", "id", ValidationCodeInvalidID, func(value *Representation) { value.ID = RepresentationID{} }},
		{"inputs", "inputs", ValidationCodeInvalidValue, func(value *Representation) { value.Inputs = nil }},
		{"input kind", "inputs[0].kind", ValidationCodeInvalidEnum, func(value *Representation) { value.Inputs[0] = DerivationInput{} }},
		{"input ID", "inputs[0].id", ValidationCodeInvalidID, func(value *Representation) { value.Inputs[0] = NewArtifactDerivationInput(ArtifactID{}) }},
		{"processor ID", "processor_id", ValidationCodeInvalidValue, func(value *Representation) { value.ProcessorID = "" }},
		{"processor version", "processor_version", ValidationCodeInvalidValue, func(value *Representation) { value.ProcessorVersion = string([]byte{0xff}) }},
		{"parameters digest", "parameters_sha256", ValidationCodeInvalidDigest, func(value *Representation) { value.ParametersSHA256 = SHA256{} }},
		{"media type", "media_type", ValidationCodeInvalidValue, func(value *Representation) { value.MediaType = "" }},
		{"content digest", "content_sha256", ValidationCodeInvalidDigest, func(value *Representation) { value.ContentSHA256 = SHA256{} }},
		{"identity mismatch", "id", ValidationCodeInvalidID, func(value *Representation) { value.ID[0] ^= 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validRepresentation(t)
			test.mutate(&value)
			requireValidationError(t, value.Validate(), test.field, test.code)
		})
	}
	lengthOnly := validRepresentation(t)
	lengthOnly.ByteLength++
	if err := lengthOnly.Validate(); err != nil {
		t.Fatalf("byte length incorrectly changed Representation identity: %v", err)
	}
}

func TestRepresentationMixedTypedInputsRoundTrip(t *testing.T) {
	value := validRepresentation(t)
	parentID := value.ID
	artifactID := value.Inputs[0].artifactID
	value.Inputs = []DerivationInput{
		NewArtifactDerivationInput(artifactID),
		NewRepresentationDerivationInput(parentID),
		NewArtifactDerivationInput(artifactID),
	}
	var err error
	value.ID, err = NewRepresentationID(value.Inputs, value.ProcessorID, value.ProcessorVersion, value.ParametersSHA256, value.MediaType, value.ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeRepresentation(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRepresentation(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Inputs[0].kind != derivationInputKindArtifact || decoded.Inputs[0].artifactID != artifactID || decoded.Inputs[0].representationID != (RepresentationID{}) || decoded.Inputs[1].kind != derivationInputKindRepresentation || decoded.Inputs[1].representationID != parentID || decoded.Inputs[1].artifactID != (ArtifactID{}) || !reflect.DeepEqual(decoded, value) {
		t.Fatalf("mixed typed inputs did not round-trip: %#v", decoded.Inputs)
	}
}

func TestSegmentValidationAndParentBoundary(t *testing.T) {
	for _, test := range []struct {
		name, field string
		code        ValidationCode
		mutate      func(*Segment)
	}{
		{"schema", "schema", ValidationCodeInvalidSchema, func(value *Segment) { value.Schema = "wrong" }},
		{"ID", "id", ValidationCodeInvalidID, func(value *Segment) { value.ID = SegmentID{} }},
		{"Representation ID", "representation_id", ValidationCodeInvalidID, func(value *Segment) { value.RepresentationID = RepresentationID{} }},
		{"unknown selector", "selector.schema", ValidationCodeUnsupportedSelector, func(value *Segment) { value.Selector = SegmentSelector{} }},
		{"empty range", "selector.end", ValidationCodeInvalidRange, func(value *Segment) { value.Selector = NewTextByteRangeSelector(5, 5) }},
		{"reversed range", "selector.end", ValidationCodeInvalidRange, func(value *Segment) { value.Selector = NewTextByteRangeSelector(6, 5) }},
		{"content digest", "content_sha256", ValidationCodeInvalidDigest, func(value *Segment) { value.ContentSHA256 = SHA256{} }},
		{"identity mismatch", "id", ValidationCodeInvalidID, func(value *Segment) { value.ID[0] ^= 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := validSegment(t)
			test.mutate(&value)
			requireValidationError(t, value.Validate(), test.field, test.code)
		})
	}
	segment := validSegment(t)
	parent := validRepresentation(t)
	if err := segment.ValidateAgainst(parent); err != nil {
		t.Fatalf("ValidateAgainst(valid): %v", err)
	}
	otherParent := parent
	otherParent.ID[0] ^= 1
	segmentForOtherParent := segment
	segmentForOtherParent.RepresentationID = otherParent.ID
	var err error
	segmentForOtherParent.ID, err = NewSegmentID(otherParent.ID, segment.Selector, segment.ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	requireValidationError(t, segmentForOtherParent.ValidateAgainst(parent), "representation_id", ValidationCodeInvalidID)
	nonText := parent
	nonText.MediaType = "text/plain"
	nonText.ID, err = NewRepresentationID(nonText.Inputs, nonText.ProcessorID, nonText.ProcessorVersion, nonText.ParametersSHA256, nonText.MediaType, nonText.ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	segmentForNonText := segment
	segmentForNonText.RepresentationID = nonText.ID
	segmentForNonText.ID, err = NewSegmentID(nonText.ID, segment.Selector, segment.ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	requireValidationError(t, segmentForNonText.ValidateAgainst(nonText), "selector.schema", ValidationCodeUnsupportedSelector)
	shortParent := parent
	shortParent.ByteLength = 4
	requireValidationError(t, segment.ValidateAgainst(shortParent), "selector.end", ValidationCodeInvalidRange)
}

func TestStrictRecordDecoderBoundaries(t *testing.T) {
	decoders := []struct {
		name, valid, schema string
		decode              func([]byte) error
	}{
		{"Artifact", validArtifactJSON, ArtifactSchema, func(data []byte) error { _, err := DecodeArtifact(data); return err }},
		{"Representation", validRepresentationJSON, RepresentationSchema, func(data []byte) error { _, err := DecodeRepresentation(data); return err }},
		{"Segment", validSegmentJSON, SegmentSchema, func(data []byte) error { _, err := DecodeSegment(data); return err }},
	}
	for _, decoder := range decoders {
		t.Run(decoder.name, func(t *testing.T) {
			unknown := strings.TrimSuffix(decoder.valid, "}\n") + ",\"extra\":true}\n"
			requireValidationError(t, decoder.decode([]byte(unknown)), "extra", ValidationCodeUnknownField)
			wrongType := strings.Replace(decoder.valid, `"schema":"`+decoder.schema+`"`, `"schema":1`, 1)
			requireValidationError(t, decoder.decode([]byte(wrongType)), "schema", ValidationCodeInvalidJSON)
			requireValidationError(t, decoder.decode([]byte(decoder.valid+`{}`)), "", ValidationCodeTrailingData)
			requireValidationError(t, decoder.decode([]byte(decoder.valid+`{`)), "", ValidationCodeInvalidJSON)
			requireValidationError(t, decoder.decode([]byte{'{', '"', 0xff, '"', ':', '1', '}'}), "", ValidationCodeInvalidJSON)
		})
	}
}

func TestRepresentationNestedInputDecodeEvidence(t *testing.T) {
	tests := []struct {
		name, old, replacement, field string
		code                          ValidationCode
	}{
		{"unknown field", "\"id\":\"4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839\"", "\"id\":\"4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839\",\"extra\":true", "inputs[0].extra", ValidationCodeUnknownField},
		{"wrong kind type", "\"kind\":\"artifact\"", "\"kind\":1", "inputs[0].kind", ValidationCodeInvalidJSON},
		{"wrong ID type", "\"id\":\"4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839\"", "\"id\":1", "inputs[0].id", ValidationCodeInvalidJSON},
		{"unknown kind", "\"kind\":\"artifact\"", "\"kind\":\"other\"", "inputs[0].kind", ValidationCodeInvalidEnum},
		{"malformed ID", "\"id\":\"4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839\"", "\"id\":\"bad\"", "inputs[0].id", ValidationCodeInvalidID},
		{"zero ID", "\"id\":\"4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839\"", "\"id\":\"0000000000000000000000000000000000000000000000000000000000000000\"", "inputs[0].id", ValidationCodeInvalidID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := strings.Replace(validRepresentationJSON, test.old, test.replacement, 1)
			_, err := DecodeRepresentation([]byte(input))
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestSegmentSelectorDecodeEvidence(t *testing.T) {
	tests := []struct {
		name, old, replacement, field string
		code                          ValidationCode
	}{
		{"unknown field", "\"end\":5", "\"end\":5,\"extra\":true", "selector.extra", ValidationCodeUnknownField},
		{"unknown schema", TextByteRangeSelectorSchema, "mousa.selector.other.v1", "selector.schema", ValidationCodeUnsupportedSelector},
		{"wrong start type", "\"start\":0", "\"start\":\"0\"", "selector.start", ValidationCodeInvalidJSON},
		{"overflow", "\"end\":5", "\"end\":18446744073709551616", "selector.end", ValidationCodeInvalidJSON},
		{"empty range", "\"start\":0,\"end\":5", "\"start\":5,\"end\":5", "selector.end", ValidationCodeInvalidRange},
		{"reversed range", "\"start\":0,\"end\":5", "\"start\":6,\"end\":5", "selector.end", ValidationCodeInvalidRange},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := strings.Replace(validSegmentJSON, test.old, test.replacement, 1)
			_, err := DecodeSegment([]byte(input))
			requireValidationError(t, err, test.field, test.code)
		})
	}
}

func TestZeroDigestDecodeEvidence(t *testing.T) {
	const zero = "0000000000000000000000000000000000000000000000000000000000000000"
	for _, test := range []struct {
		name, input, old, field string
		decode                  func([]byte) error
	}{
		{"Artifact content", validArtifactJSON, "fc02a0cd21f2db893e609e488ef366fa232b9b0788a7d12ae2a42b25460a2421", "content_sha256", func(data []byte) error { _, err := DecodeArtifact(data); return err }},
		{"Representation parameters", validRepresentationJSON, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "parameters_sha256", func(data []byte) error { _, err := DecodeRepresentation(data); return err }},
		{"Representation content", validRepresentationJSON, "f0df22d2bcadf69b874b74b12a790a236cf590850b779e427dcdebd4d464ae3d", "content_sha256", func(data []byte) error { _, err := DecodeRepresentation(data); return err }},
		{"Segment content", validSegmentJSON, "185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969", "content_sha256", func(data []byte) error { _, err := DecodeSegment(data); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireValidationError(t, test.decode([]byte(strings.Replace(test.input, test.old, zero, 1))), test.field, ValidationCodeInvalidDigest)
		})
	}
}

func TestRecordDecoderSemanticValidationOrder(t *testing.T) {
	const zero = "0000000000000000000000000000000000000000000000000000000000000000"
	artifact := strings.Replace(validArtifactJSON, "4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839", zero, 1)
	artifact = strings.Replace(artifact, canonicalObservationID, "", 1)
	_, err := DecodeArtifact([]byte(artifact))
	requireValidationError(t, err, "id", ValidationCodeInvalidID)
	representation := strings.Replace(validRepresentationJSON, "c30b39a8e1595804b964dfb86b4ce16f8fbcffe0af4c65d0c23742c1cc6774ae", zero, 1)
	representation = strings.Replace(representation, `[{"kind":"artifact","id":"4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839"}]`, `[]`, 1)
	_, err = DecodeRepresentation([]byte(representation))
	requireValidationError(t, err, "id", ValidationCodeInvalidID)
	parametersFirst := strings.Replace(validRepresentationJSON, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", zero, 1)
	parametersFirst = strings.Replace(parametersFirst, UTF8TextMediaType, "", 1)
	_, err = DecodeRepresentation([]byte(parametersFirst))
	requireValidationError(t, err, "parameters_sha256", ValidationCodeInvalidDigest)
	segment := strings.Replace(validSegmentJSON, "a454fa6d452787a2ec079bb3f453a9842a9a7d36da099123676093247e423492", zero, 1)
	segment = strings.Replace(segment, "c30b39a8e1595804b964dfb86b4ce16f8fbcffe0af4c65d0c23742c1cc6774ae", "", 1)
	_, err = DecodeSegment([]byte(segment))
	requireValidationError(t, err, "id", ValidationCodeInvalidID)
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

func validArtifact(t testing.TB) Artifact {
	t.Helper()
	observationID := mustParseObservationID(t, canonicalObservationID)
	id, err := NewArtifactID(observationID, "raw-message")
	if err != nil {
		t.Fatal(err)
	}
	contentSHA256, err := ParseSHA256("fc02a0cd21f2db893e609e488ef366fa232b9b0788a7d12ae2a42b25460a2421")
	if err != nil {
		t.Fatal(err)
	}
	return Artifact{
		Schema:        ArtifactSchema,
		ID:            id,
		ObservationID: observationID,
		ArtifactKey:   "raw-message",
		MediaType:     "message/rfc822",
		ContentSHA256: contentSHA256,
		ByteLength:    12,
	}
}

func validRepresentation(t testing.TB) Representation {
	t.Helper()
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
	inputs := []DerivationInput{NewArtifactDerivationInput(artifactID)}
	id, err := NewRepresentationID(inputs, "example.text-extractor", "1.0.0", parametersSHA256, UTF8TextMediaType, contentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	return Representation{
		Schema:           RepresentationSchema,
		ID:               id,
		Inputs:           inputs,
		ProcessorID:      "example.text-extractor",
		ProcessorVersion: "1.0.0",
		ParametersSHA256: parametersSHA256,
		MediaType:        UTF8TextMediaType,
		ContentSHA256:    contentSHA256,
		ByteLength:       14,
	}
}

func validSegment(t testing.TB) Segment {
	t.Helper()
	representation := validRepresentation(t)
	contentSHA256, err := ParseSHA256("185f8db32271fe25f561a6fc938b2e264306ec304eda518007d1764826381969")
	if err != nil {
		t.Fatal(err)
	}
	selector := NewTextByteRangeSelector(0, 5)
	id, err := NewSegmentID(representation.ID, selector, contentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	return Segment{
		Schema:           SegmentSchema,
		ID:               id,
		RepresentationID: representation.ID,
		Selector:         selector,
		ContentSHA256:    contentSHA256,
	}
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

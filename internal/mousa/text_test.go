package mousa

import (
	"bytes"
	"crypto/sha256"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeUTF8TextCanonicalVector(t *testing.T) {
	content := []byte{0xef, 0xbb, 0xbf, 'H', 'e', 'l', 'l', 'o', ',', '\r', '\n', 'M', 'o', 'u', 's', 'a', '.', '\r'}
	artifact := Artifact{
		Schema:        ArtifactSchema,
		ID:            mustParseArtifactID(t, "4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839"),
		ObservationID: mustParseObservationID(t, canonicalObservationID),
		ArtifactKey:   "raw-message",
		MediaType:     "message/rfc822",
		ContentSHA256: mustParseSHA256(t, "ddde5dfbbb07ca50e5a51741c6e5955361661366fa4c00779ef5737527a8661f"),
		ByteLength:    18,
	}

	representation, normalized, err := NormalizeUTF8Text(artifact, content)
	if err != nil {
		t.Fatalf("NormalizeUTF8Text(): %v", err)
	}
	want := []byte("Hello,\nMousa.\n")
	if !bytes.Equal(normalized, want) {
		t.Fatalf("normalized bytes = %x, want %x", normalized, want)
	}
	if representation.Schema != RepresentationSchema ||
		representation.ID.String() != "c9aa5073f7a314b2029d5eb6165f42d208f68614c75a84cabc1fbb3c187b88df" ||
		representation.ProcessorID != "mousa.text.normalize_utf8" ||
		representation.ProcessorVersion != "1" ||
		representation.ParametersSHA256.String() != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" ||
		representation.MediaType != UTF8TextMediaType ||
		representation.ContentSHA256.String() != "77524d7f00bce3238c5b0ac4cd2634145bb1996ab427841bd34c8477910adf78" ||
		representation.ByteLength != 14 {
		t.Fatalf("Representation = %#v", representation)
	}
}

func TestNormalizeUTF8TextLineEndingsTable(t *testing.T) {
	for _, test := range []struct {
		name, input, want string
	}{
		{"unchanged", "caf\u00e9", "caf\u00e9"},
		{"CRLF", "a\r\nb", "a\nb"},
		{"lone CR", "a\rb", "a\nb"},
		{"mixed", "a\r\nb\rc\n", "a\nb\nc\n"},
		{"combining sequence", "e\u0301", "e\u0301"},
		{"embedded NUL", "a\x00b", "a\x00b"},
		{"no trailing LF", "a", "a"},
		{"present trailing LF", "a\n", "a\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := []byte(test.input)
			original := bytes.Clone(input)
			representation, got, err := NormalizeUTF8Text(artifactForContent(t, input), input)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != test.want || !bytes.Equal(input, original) {
				t.Fatalf("normalized = %q, input = %x", got, input)
			}
			if len(input) > 0 && len(got) > 0 {
				got[0] ^= 1
				if !bytes.Equal(input, original) {
					t.Fatal("normalized output aliases input")
				}
			}
			secondRepresentation, second, err := NormalizeUTF8Text(artifactForContent(t, original), original)
			if err != nil || !bytes.Equal(second, []byte(test.want)) || !reflect.DeepEqual(secondRepresentation, representation) {
				t.Fatalf("deterministic replay = %#v, %q, %v", secondRepresentation, second, err)
			}
		})
	}
}

func TestNormalizeUTF8TextBOMOnlyAtStart(t *testing.T) {
	bom := "\ufeff"
	for _, test := range []struct {
		name, input, want string
	}{
		{"none", "a", "a"},
		{"one leading", bom + "a", "a"},
		{"two leading", bom + bom + "a", bom + "a"},
		{"interior", "a" + bom + "b", "a" + bom + "b"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := []byte(test.input)
			_, got, err := NormalizeUTF8Text(artifactForContent(t, input), input)
			if err != nil || string(got) != test.want {
				t.Fatalf("normalized = %q, %v", got, err)
			}
		})
	}
}

func TestNormalizeUTF8TextEmptyContent(t *testing.T) {
	input := []byte{}
	_, got, err := NormalizeUTF8Text(artifactForContent(t, input), input)
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("normalized = %#v, %v", got, err)
	}
}

func TestNormalizeUTF8TextRejectsInvalidUTF8(t *testing.T) {
	input := []byte{0xff}
	_, _, err := NormalizeUTF8Text(artifactForContent(t, input), input)
	requireValidationError(t, err, "content", ValidationCodeInvalidValue)
}

func TestNormalizeUTF8TextRejectsContentMismatch(t *testing.T) {
	input := []byte("abc")
	artifact := artifactForContent(t, input)
	artifact.ByteLength++
	_, _, err := NormalizeUTF8Text(artifact, input)
	requireValidationError(t, err, "byte_length", ValidationCodeInvalidValue)

	artifact = artifactForContent(t, input)
	artifact.ContentSHA256[0] ^= 1
	_, _, err = NormalizeUTF8Text(artifact, input)
	requireValidationError(t, err, "content_sha256", ValidationCodeInvalidDigest)
}

func TestNormalizeUTF8TextValidationOrder(t *testing.T) {
	input := bytes.Repeat([]byte{'a'}, MaxUTF8TextBytes+1)
	artifact := artifactForContent(t, input)
	artifact.Schema = "wrong"
	_, _, err := NormalizeUTF8Text(artifact, input)
	requireValidationError(t, err, "schema", ValidationCodeInvalidSchema)

	artifact = artifactForContent(t, []byte{0xff})
	artifact.ByteLength++
	_, _, err = NormalizeUTF8Text(artifact, []byte{0xff})
	requireValidationError(t, err, "byte_length", ValidationCodeInvalidValue)
}

func TestNormalizeUTF8TextRejectsOversizedInput(t *testing.T) {
	input := bytes.Repeat([]byte{'a'}, MaxUTF8TextBytes)
	if _, _, err := NormalizeUTF8Text(artifactForContent(t, input), input); err != nil {
		t.Fatalf("exact limit: %v", err)
	}
	input = append(input, 'a')
	_, _, err := NormalizeUTF8Text(artifactForContent(t, input), input)
	requireValidationError(t, err, "content", ValidationCodeInvalidRange)
}

func TestNormalizeUTF8TextRejectsOversizedInputBeforeLengthConversion(t *testing.T) {
	input := []byte(strings.Repeat("a", MaxUTF8TextBytes+1))
	artifact := artifactForContent(t, input)
	artifact.ByteLength = 0
	_, _, err := NormalizeUTF8Text(artifact, input)
	requireValidationError(t, err, "content", ValidationCodeInvalidRange)
}

func TestSegmentUTF8TextCanonicalVectors(t *testing.T) {
	content := []byte("Hello,\nMousa.\n")
	representation := representationForContent(t, content)
	if representation.ID.String() != "c9aa5073f7a314b2029d5eb6165f42d208f68614c75a84cabc1fbb3c187b88df" {
		t.Fatalf("Representation ID = %s", representation.ID)
	}
	segments, err := SegmentUTF8Text(representation, content)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 1 {
		t.Fatalf("segment count = %d", len(segments))
	}
	textByteRange, ok := segments[0].Selector.TextByteRange()
	if !ok || textByteRange.Start != 0 || textByteRange.End != 14 || segments[0].ContentSHA256.String() != "77524d7f00bce3238c5b0ac4cd2634145bb1996ab427841bd34c8477910adf78" || segments[0].ID.String() != "86ae7e4bf6ac0f8170dea93342b1e3e7adb4016011960f30d9ee7daf9d25c98d" {
		t.Fatalf("canonical Segment = %#v", segments[0])
	}
	if err := segments[0].ValidateContentAgainst(representation, content); err != nil {
		t.Fatalf("ValidateContentAgainst(): %v", err)
	}
}

func TestSegmentUTF8TextBoundaryTable(t *testing.T) {
	for _, test := range []struct {
		name    string
		content []byte
		ranges  [][2]uint64
	}{
		{"empty", []byte{}, [][2]uint64{}},
		{"one byte", []byte("a"), [][2]uint64{{0, 1}}},
		{"exact segment", bytes.Repeat([]byte("a"), 4096), [][2]uint64{{0, 4096}}},
		{"three ASCII segments", bytes.Repeat([]byte("a"), 8193), [][2]uint64{{0, 4096}, {4096, 8192}, {8192, 8193}}},
		{"multibyte boundary", []byte(strings.Repeat("a", 4095) + "€b"), [][2]uint64{{0, 4095}, {4095, 4099}}},
		{"combining mark boundary", []byte(strings.Repeat("a", 4095) + "e\u0301"), [][2]uint64{{0, 4096}, {4096, 4098}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			representation := representationForContent(t, test.content)
			first, err := SegmentUTF8Text(representation, test.content)
			if err != nil {
				t.Fatal(err)
			}
			second, err := SegmentUTF8Text(representation, test.content)
			if err != nil || !reflect.DeepEqual(first, second) || first == nil || len(first) != len(test.ranges) {
				t.Fatalf("segments = %#v, replay = %#v, error = %v", first, second, err)
			}
			for index, segment := range first {
				textByteRange, ok := segment.Selector.TextByteRange()
				start, end := textByteRange.Start, textByteRange.End
				if !ok || [2]uint64{start, end} != test.ranges[index] || end-start > MaxUTF8TextSegmentBytes || !utf8Boundary(test.content, int(start)) || !utf8Boundary(test.content, int(end)) {
					t.Fatalf("segment %d range = [%d,%d)", index, start, end)
				}
				if segment.ContentSHA256 != SHA256(sha256.Sum256(test.content[start:end])) {
					t.Fatalf("segment %d digest mismatch", index)
				}
			}
		})
	}
}

func TestSegmentUTF8TextRejectsInvalidRepresentation(t *testing.T) {
	content := []byte("abc")
	representation := representationForContent(t, content)
	representation.Schema = "wrong"
	_, err := SegmentUTF8Text(representation, bytes.Repeat([]byte{'a'}, MaxUTF8TextBytes+1))
	requireValidationError(t, err, "schema", ValidationCodeInvalidSchema)

	representation = representationForContent(t, content)
	representation.MediaType = "text/plain"
	representation.ID, err = NewRepresentationID(representation.Inputs, representation.ProcessorID, representation.ProcessorVersion, representation.ParametersSHA256, representation.MediaType, representation.ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	_, err = SegmentUTF8Text(representation, content)
	requireValidationError(t, err, "selector.schema", ValidationCodeUnsupportedSelector)
}

func TestSegmentUTF8TextRejectsInvalidContent(t *testing.T) {
	atLimit := bytes.Repeat([]byte{'a'}, MaxUTF8TextBytes)
	segments, err := SegmentUTF8Text(representationForContent(t, atLimit), atLimit)
	if err != nil || len(segments) != MaxUTF8TextBytes/MaxUTF8TextSegmentBytes {
		t.Fatalf("exact limit produced %d segments: %v", len(segments), err)
	}

	oversized := bytes.Repeat([]byte{'a'}, MaxUTF8TextBytes+1)
	_, err = SegmentUTF8Text(representationForContent(t, oversized), oversized)
	requireValidationError(t, err, "content", ValidationCodeInvalidRange)

	content := []byte("abc")
	representation := representationForContent(t, content)
	representation.ByteLength++
	_, err = SegmentUTF8Text(representation, content)
	requireValidationError(t, err, "byte_length", ValidationCodeInvalidValue)

	representation = representationForContent(t, content)
	_, err = SegmentUTF8Text(representation, []byte("abd"))
	requireValidationError(t, err, "content_sha256", ValidationCodeInvalidDigest)

	invalid := []byte{0xff}
	_, err = SegmentUTF8Text(representationForContent(t, invalid), invalid)
	requireValidationError(t, err, "content", ValidationCodeInvalidValue)
}

func TestSegmentValidateContentAgainstFailures(t *testing.T) {
	content := []byte("a€b")
	representation := representationForContent(t, content)
	segments, err := SegmentUTF8Text(representation, content)
	if err != nil {
		t.Fatal(err)
	}
	segment := segments[0]

	wrongParent := representationForContent(t, []byte("z€b"))
	requireValidationError(t, segment.ValidateContentAgainst(wrongParent, content), "representation_id", ValidationCodeInvalidID)

	for _, test := range []struct {
		name, field string
		code        ValidationCode
		mutate      func(*Segment, *Representation, *[]byte)
	}{
		{"length", "byte_length", ValidationCodeInvalidValue, func(_ *Segment, parent *Representation, _ *[]byte) { parent.ByteLength++ }},
		{"parent digest", "content_sha256", ValidationCodeInvalidDigest, func(_ *Segment, _ *Representation, data *[]byte) { (*data)[0] ^= 1 }},
		{"invalid UTF-8", "content", ValidationCodeInvalidValue, func(value *Segment, parent *Representation, data *[]byte) {
			*data = []byte{0xff}
			*parent = representationForContent(t, *data)
			value.RepresentationID = parent.ID
			value.Selector = NewTextByteRangeSelector(0, 1)
			value.ContentSHA256 = SHA256(sha256.Sum256(*data))
			value.ID, _ = NewSegmentID(value.RepresentationID, value.Selector, value.ContentSHA256)
		}},
		{"unaligned start", "selector.start", ValidationCodeInvalidRange, func(value *Segment, _ *Representation, _ *[]byte) {
			value.Selector = NewTextByteRangeSelector(2, 4)
			value.ContentSHA256 = SHA256(sha256.Sum256(content[2:4]))
			value.ID, _ = NewSegmentID(value.RepresentationID, value.Selector, value.ContentSHA256)
		}},
		{"unaligned end", "selector.end", ValidationCodeInvalidRange, func(value *Segment, _ *Representation, _ *[]byte) {
			value.Selector = NewTextByteRangeSelector(0, 2)
			value.ContentSHA256 = SHA256(sha256.Sum256(content[0:2]))
			value.ID, _ = NewSegmentID(value.RepresentationID, value.Selector, value.ContentSHA256)
		}},
		{"selected digest", "content_sha256", ValidationCodeInvalidDigest, func(value *Segment, _ *Representation, _ *[]byte) {
			value.ContentSHA256[0] ^= 1
			value.ID, _ = NewSegmentID(value.RepresentationID, value.Selector, value.ContentSHA256)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, parent, data := segment, representation, bytes.Clone(content)
			test.mutate(&value, &parent, &data)
			requireValidationError(t, value.ValidateContentAgainst(parent, data), test.field, test.code)
		})
	}

	unaligned := segment
	unaligned.Selector = NewTextByteRangeSelector(2, 3)
	unaligned.ContentSHA256 = SHA256(sha256.Sum256(content[2:3]))
	unaligned.ID, err = NewSegmentID(unaligned.RepresentationID, unaligned.Selector, unaligned.ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := unaligned.ValidateAgainst(representation); err != nil {
		t.Fatalf("metadata-only ValidateAgainst rejected byte range: %v", err)
	}
	requireValidationError(t, unaligned.ValidateContentAgainst(representation, content), "selector.start", ValidationCodeInvalidRange)

	differentContent := bytes.Clone(content)
	differentContent[0] ^= 1
	requireValidationError(t, unaligned.ValidateContentAgainst(representation, differentContent), "content_sha256", ValidationCodeInvalidDigest)
}

func TestSegmentValidateContentAgainstRejectsInvalidSegmentMetadata(t *testing.T) {
	content := bytes.Repeat([]byte{'a'}, MaxUTF8TextBytes+1)
	representation := representationForContent(t, content)
	segment := Segment{}
	requireValidationError(t, segment.ValidateContentAgainst(representation, content), "schema", ValidationCodeInvalidSchema)
}

func TestSegmentValidateContentAgainstRejectsInvalidRepresentation(t *testing.T) {
	content := []byte("abc")
	representation := representationForContent(t, content)
	segment, err := SegmentUTF8Text(representation, content)
	if err != nil {
		t.Fatal(err)
	}
	representation.MediaType = "text/plain"
	representation.ID, err = NewRepresentationID(representation.Inputs, representation.ProcessorID, representation.ProcessorVersion, representation.ParametersSHA256, representation.MediaType, representation.ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	segment[0].RepresentationID = representation.ID
	segment[0].ID, err = NewSegmentID(representation.ID, segment[0].Selector, segment[0].ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	requireValidationError(t, segment[0].ValidateContentAgainst(representation, content), "selector.schema", ValidationCodeUnsupportedSelector)
}

func TestSegmentValidateContentAgainstRejectsOversizedContent(t *testing.T) {
	content := bytes.Repeat([]byte{'a'}, MaxUTF8TextBytes+1)
	representation := representationForContent(t, content)
	selector := NewTextByteRangeSelector(0, 1)
	digest := SHA256(sha256.Sum256(content[:1]))
	id, err := NewSegmentID(representation.ID, selector, digest)
	if err != nil {
		t.Fatal(err)
	}
	segment := Segment{Schema: SegmentSchema, ID: id, RepresentationID: representation.ID, Selector: selector, ContentSHA256: digest}
	requireValidationError(t, segment.ValidateContentAgainst(representation, content), "content", ValidationCodeInvalidRange)
}

func representationForContent(t testing.TB, content []byte) Representation {
	t.Helper()
	artifact := artifactForContent(t, content)
	parameters := SHA256(sha256.Sum256(nil))
	digest := SHA256(sha256.Sum256(content))
	inputs := []DerivationInput{NewArtifactDerivationInput(artifact.ID)}
	id, err := NewRepresentationID(inputs, UTF8TextProcessorID, UTF8TextProcessorVersion, parameters, UTF8TextMediaType, digest)
	if err != nil {
		t.Fatal(err)
	}
	return Representation{Schema: RepresentationSchema, ID: id, Inputs: inputs, ProcessorID: UTF8TextProcessorID, ProcessorVersion: UTF8TextProcessorVersion, ParametersSHA256: parameters, MediaType: UTF8TextMediaType, ContentSHA256: digest, ByteLength: uint64(len(content))}
}

func utf8Boundary(content []byte, index int) bool {
	return index == 0 || index == len(content) || content[index]&0xc0 != 0x80
}

func artifactForContent(t testing.TB, content []byte) Artifact {
	t.Helper()
	digest := SHA256(sha256.Sum256(content))
	return Artifact{
		Schema:        ArtifactSchema,
		ID:            mustParseArtifactID(t, "4e177fd6c764534dd4c12c5f745da6e0daff5a81dcfe55f7a4b6596d1559d839"),
		ObservationID: mustParseObservationID(t, canonicalObservationID),
		ArtifactKey:   "raw-message",
		MediaType:     "message/rfc822",
		ContentSHA256: digest,
		ByteLength:    uint64(len(content)),
	}
}

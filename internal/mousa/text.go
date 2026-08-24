package mousa

import (
	"crypto/sha256"
	"unicode/utf8"
)

const (
	UTF8TextProcessorID      = "mousa.text.normalize_utf8"
	UTF8TextProcessorVersion = "1"
	MaxUTF8TextBytes         = 16 * 1024 * 1024
	MaxUTF8TextSegmentBytes  = 4096
)

func NormalizeUTF8Text(artifact Artifact, content []byte) (Representation, []byte, error) {
	if err := artifact.Validate(); err != nil {
		return Representation{}, nil, err
	}
	if len(content) > MaxUTF8TextBytes {
		return Representation{}, nil, newValidationError("content", ValidationCodeInvalidRange, "must not exceed maximum text byte length", nil)
	}
	if uint64(len(content)) != artifact.ByteLength {
		return Representation{}, nil, newValidationError("byte_length", ValidationCodeInvalidValue, "does not match content length", nil)
	}
	if SHA256(sha256.Sum256(content)) != artifact.ContentSHA256 {
		return Representation{}, nil, newValidationError("content_sha256", ValidationCodeInvalidDigest, "does not match content", nil)
	}
	if !utf8.Valid(content) {
		return Representation{}, nil, newValidationError("content", ValidationCodeInvalidValue, "must be valid UTF-8", nil)
	}

	start := 0
	if len(content) >= 3 && content[0] == 0xef && content[1] == 0xbb && content[2] == 0xbf {
		start = 3
	}
	normalized := make([]byte, 0, len(content)-start)
	for index := start; index < len(content); index++ {
		if content[index] != '\r' {
			normalized = append(normalized, content[index])
			continue
		}
		normalized = append(normalized, '\n')
		if index+1 < len(content) && content[index+1] == '\n' {
			index++
		}
	}

	parametersSHA256 := SHA256(sha256.Sum256(nil))
	contentSHA256 := SHA256(sha256.Sum256(normalized))
	inputs := []DerivationInput{NewArtifactDerivationInput(artifact.ID)}
	id, err := NewRepresentationID(inputs, UTF8TextProcessorID, UTF8TextProcessorVersion, parametersSHA256, UTF8TextMediaType, contentSHA256)
	if err != nil {
		return Representation{}, nil, err
	}
	return Representation{
		Schema:           RepresentationSchema,
		ID:               id,
		Inputs:           inputs,
		ProcessorID:      UTF8TextProcessorID,
		ProcessorVersion: UTF8TextProcessorVersion,
		ParametersSHA256: parametersSHA256,
		MediaType:        UTF8TextMediaType,
		ContentSHA256:    contentSHA256,
		ByteLength:       uint64(len(normalized)),
	}, normalized, nil
}

func SegmentUTF8Text(representation Representation, content []byte) ([]Segment, error) {
	if err := validateUTF8TextContent(representation, content); err != nil {
		return nil, err
	}

	const minimumNonFinalSegmentBytes = MaxUTF8TextSegmentBytes - utf8.UTFMax + 1
	segments := make([]Segment, 0, (len(content)+minimumNonFinalSegmentBytes-1)/minimumNonFinalSegmentBytes)
	for start := 0; start < len(content); {
		end := start + MaxUTF8TextSegmentBytes
		if end >= len(content) {
			end = len(content)
		} else {
			for !utf8.RuneStart(content[end]) {
				end--
			}
		}
		selector := NewTextByteRangeSelector(uint64(start), uint64(end))
		contentSHA256 := SHA256(sha256.Sum256(content[start:end]))
		id, err := NewSegmentID(representation.ID, selector, contentSHA256)
		if err != nil {
			return nil, err
		}
		segments = append(segments, Segment{
			Schema:           SegmentSchema,
			ID:               id,
			RepresentationID: representation.ID,
			Selector:         selector,
			ContentSHA256:    contentSHA256,
		})
		start = end
	}
	return segments, nil
}

func (segment Segment) ValidateContentAgainst(representation Representation, content []byte) error {
	if err := segment.ValidateAgainst(representation); err != nil {
		return err
	}
	if len(content) > MaxUTF8TextBytes {
		return newValidationError("content", ValidationCodeInvalidRange, "must not exceed maximum text byte length", nil)
	}
	if uint64(len(content)) != representation.ByteLength {
		return newValidationError("byte_length", ValidationCodeInvalidValue, "does not match content length", nil)
	}
	if SHA256(sha256.Sum256(content)) != representation.ContentSHA256 {
		return newValidationError("content_sha256", ValidationCodeInvalidDigest, "does not match parent content", nil)
	}
	if !utf8.Valid(content) {
		return newValidationError("content", ValidationCodeInvalidValue, "must be valid UTF-8", nil)
	}
	textByteRange, _ := segment.Selector.TextByteRange()
	start, end := textByteRange.Start, textByteRange.End
	if start != 0 && !utf8.RuneStart(content[start]) {
		return newValidationError("selector.start", ValidationCodeInvalidRange, "must be a UTF-8 code-point boundary", nil)
	}
	if end != uint64(len(content)) && !utf8.RuneStart(content[end]) {
		return newValidationError("selector.end", ValidationCodeInvalidRange, "must be a UTF-8 code-point boundary", nil)
	}
	if SHA256(sha256.Sum256(content[start:end])) != segment.ContentSHA256 {
		return newValidationError("content_sha256", ValidationCodeInvalidDigest, "does not match selected content", nil)
	}
	return nil
}

func validateUTF8TextContent(representation Representation, content []byte) error {
	if err := representation.Validate(); err != nil {
		return err
	}
	if representation.MediaType != UTF8TextMediaType {
		return newValidationError("selector.schema", ValidationCodeUnsupportedSelector, "requires UTF-8 text representation", nil)
	}
	if len(content) > MaxUTF8TextBytes {
		return newValidationError("content", ValidationCodeInvalidRange, "must not exceed maximum text byte length", nil)
	}
	if uint64(len(content)) != representation.ByteLength {
		return newValidationError("byte_length", ValidationCodeInvalidValue, "does not match content length", nil)
	}
	if SHA256(sha256.Sum256(content)) != representation.ContentSHA256 {
		return newValidationError("content_sha256", ValidationCodeInvalidDigest, "does not match parent content", nil)
	}
	if !utf8.Valid(content) {
		return newValidationError("content", ValidationCodeInvalidValue, "must be valid UTF-8", nil)
	}
	return nil
}

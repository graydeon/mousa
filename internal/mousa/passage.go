package mousa

import (
	"bytes"
	"crypto/sha256"
	"unicode/utf8"
)

const (
	TextSegmentFixedV1   = "fixed-v1"
	TextSegmentPassageV1 = "passage-v1"
	MaxUTF8PassageBytes  = 1024
	MaxUTF8Passages      = 65536
)

func textPolicyParameters(policy string) (SHA256, error) {
	switch policy {
	case TextSegmentFixedV1:
		return SHA256(sha256.Sum256(nil)), nil
	case TextSegmentPassageV1:
		return SHA256(sha256.Sum256([]byte(`{"segmentation":"passage-v1"}`))), nil
	default:
		return SHA256{}, newValidationError("segmentation", ValidationCodeInvalidValue, "must be fixed-v1 or passage-v1", nil)
	}
}

// TextSegmentationPolicy reads the normalizer's canonical parameters. Other
// processors retain the original fixed-byte segmentation contract.
func TextSegmentationPolicy(representation Representation) (string, error) {
	if representation.ProcessorID != UTF8TextProcessorID {
		return TextSegmentFixedV1, nil
	}
	if representation.ProcessorVersion == UTF8TextProcessorVersion {
		for _, policy := range []string{TextSegmentFixedV1, TextSegmentPassageV1} {
			parameters, _ := textPolicyParameters(policy)
			if representation.ParametersSHA256 == parameters {
				return policy, nil
			}
		}
	}
	return "", newValidationError("segmentation", ValidationCodeInvalidValue, "unsupported text normalizer version or parameters", nil)
}

func segmentPassages(representation Representation, content []byte) ([]Segment, error) {
	segments := make([]Segment, 0, (len(content)+MaxUTF8PassageBytes-1)/MaxUTF8PassageBytes)
	emit := func(start, end int) error {
		if start == end {
			return nil
		}
		if len(segments) == MaxUTF8Passages {
			return newValidationError("segmentation", ValidationCodeInvalidRange, "too many passages", nil)
		}
		selector := NewTextByteRangeSelector(uint64(start), uint64(end))
		digest := SHA256(sha256.Sum256(content[start:end]))
		id, err := NewSegmentID(representation.ID, selector, digest)
		if err != nil {
			return err
		}
		segments = append(segments, Segment{Schema: SegmentSchema, ID: id, RepresentationID: representation.ID, Selector: selector, ContentSHA256: digest})
		return nil
	}
	start := 0
	for pos := 0; pos < len(content); {
		end, heading := passageBlock(content, pos)
		if end-start > MaxUTF8PassageBytes || heading {
			if err := emit(start, pos); err != nil {
				return nil, err
			}
			start = pos
		}
		if end-pos > MaxUTF8PassageBytes {
			for start < end {
				cut := passageCut(content, start, end)
				if err := emit(start, cut); err != nil {
					return nil, err
				}
				start = cut
			}
		}
		pos = end
	}
	if err := emit(start, len(content)); err != nil {
		return nil, err
	}
	return segments, nil
}

// passageBlock recognizes only line-level boundaries, not a Markdown syntax
// tree. Whitespace remains attached to its preceding block (or initial block).
func passageBlock(content []byte, start int) (int, bool) {
	pos := start
	for pos < len(content) && len(bytes.TrimSpace(passageLine(content, pos))) == 0 {
		pos += len(passageLine(content, pos))
	}
	if pos == len(content) {
		return pos, false
	}
	line := passageLine(content, pos)
	marker, width, _ := passageFence(line)
	kind := passageLineKind(line)
	heading := kind == 'h'
	pos += len(line)
	if width >= 3 {
		for pos < len(content) {
			line = passageLine(content, pos)
			pos += len(line)
			closeMarker, closeWidth, tail := passageFence(line)
			if closeMarker == marker && closeWidth >= width && len(bytes.Trim(tail, " \t\n")) == 0 {
				break
			}
		}
	} else if !heading {
		for pos < len(content) {
			line = passageLine(content, pos)
			if len(bytes.TrimSpace(line)) == 0 {
				break
			}
			_, fenceWidth, _ := passageFence(line)
			next := passageLineKind(line)
			if fenceWidth >= 3 || next == 'h' || (next == 'l' && kind != 'l') || (next == 't') != (kind == 't') {
				break
			}
			pos += len(line)
		}
	}
	for pos < len(content) && len(bytes.TrimSpace(passageLine(content, pos))) == 0 {
		pos += len(passageLine(content, pos))
	}
	return pos, heading
}

func passageLine(content []byte, start int) []byte {
	if n := bytes.IndexByte(content[start:], '\n'); n >= 0 {
		return content[start : start+n+1]
	}
	return content[start:]
}

func passageFence(line []byte) (byte, int, []byte) {
	trimmed := bytes.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || len(trimmed) == 0 || (trimmed[0] != '`' && trimmed[0] != '~') {
		return 0, 0, nil
	}
	n := 1
	for n < len(trimmed) && trimmed[n] == trimmed[0] {
		n++
	}
	return trimmed[0], n, trimmed[n:]
}

func passageLineKind(line []byte) byte {
	trimmed := bytes.TrimLeft(line, " ")
	if len(line)-len(trimmed) <= 3 {
		n := 0
		for n < len(trimmed) && trimmed[n] == '#' {
			n++
		}
		if n > 0 && n <= 6 && (n == len(trimmed) || bytes.ContainsAny(trimmed[n:n+1], " \t\n")) {
			return 'h'
		}
	}
	trimmed = bytes.TrimSpace(line)
	if len(trimmed) >= 2 && bytes.ContainsAny(trimmed[:1], "-*+") && bytes.ContainsAny(trimmed[1:2], " \t") {
		return 'l'
	}
	n := 0
	for n < len(trimmed) && n < 9 && trimmed[n] >= '0' && trimmed[n] <= '9' {
		n++
	}
	if n > 0 && n+1 < len(trimmed) && (trimmed[n] == '.' || trimmed[n] == ')') && bytes.ContainsAny(trimmed[n+1:n+2], " \t") {
		return 'l'
	}
	if bytes.ContainsRune(trimmed, '|') {
		return 't'
	}
	return 'p'
}

func passageCut(content []byte, start, end int) int {
	if end-start <= MaxUTF8PassageBytes {
		return end
	}
	cut := start + MaxUTF8PassageBytes
	if n := bytes.LastIndexByte(content[start:cut], '\n'); n >= 0 {
		return start + n + 1
	}
	if n := bytes.LastIndexAny(content[start:cut], " \t"); n >= 0 {
		return start + n + 1
	}
	for !utf8.RuneStart(content[cut]) {
		cut--
	}
	return cut
}

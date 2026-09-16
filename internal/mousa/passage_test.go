package mousa

import (
	"bytes"
	"crypto/sha256"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPassageBoundariesAndSourceCorrespondence(t *testing.T) {
	paragraph := strings.Repeat("prose ", 100) + "\n\n"
	list := "- first operation\n  continuation\n- second operation\n\n"
	table := "| Name | Value |\n|---|---|\n| amber | 17 |\n\n"
	fence := "```sh\nservice amber stop\n\nservice amber start\n```\n\n"
	for _, tc := range []struct {
		name, input string
		want        []string
	}{
		{"empty", "", []string{}},
		{"normalization", "\ufeff# Café\r\n\r\n日本語 e\u0301 🦊\rfin", []string{"# Café\n\n日本語 e\u0301 🦊\nfin"}},
		{"paragraphs", paragraph + paragraph, []string{paragraph, paragraph}},
		{"heading", paragraph + "# Next\n\n" + list, []string{paragraph, "# Next\n\n" + list}},
		{"list", strings.Repeat("x", 1000) + "\n\n" + list, []string{strings.Repeat("x", 1000) + "\n\n", list}},
		{"table", strings.Repeat("x", 1000) + "\n\n" + table, []string{strings.Repeat("x", 1000) + "\n\n", table}},
		{"fence", strings.Repeat("x", 1000) + "\n\n" + fence, []string{strings.Repeat("x", 1000) + "\n\n", fence}},
		{"long rune", strings.Repeat("界", 400), []string{strings.Repeat("界", 341), strings.Repeat("界", 59)}},
		{"long word", strings.Repeat("x", 2050), []string{strings.Repeat("x", 1024), strings.Repeat("x", 1024), "xx"}},
		{"word fallback", strings.Repeat("word ", 240), []string{strings.Repeat("word ", 204), strings.Repeat("word ", 36)}},
		{"line fallback", "```sh\n" + strings.Repeat("x", 1100) + "\n```\n", []string{"```sh\n", strings.Repeat("x", 1024), strings.Repeat("x", 76) + "\n```\n"}},
		{"unclosed fence", "~~~\n# not a heading\n\nbody", []string{"~~~\n# not a heading\n\nbody"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.input)
			rep, text, err := NormalizeUTF8TextWithPolicy(artifactForContent(t, raw), raw, TextSegmentPassageV1)
			if err != nil {
				t.Fatal(err)
			}
			segments, err := SegmentUTF8Text(rep, text)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := SegmentUTF8Text(rep, text)
			if err != nil || !reflect.DeepEqual(segments, replay) {
				t.Fatalf("nondeterministic replay: %v", err)
			}
			got := make([]string, 0, len(segments))
			var end uint64
			for _, s := range segments {
				r, _ := s.Selector.TextByteRange()
				if r.Start != end || r.End <= r.Start || r.End-r.Start > MaxUTF8PassageBytes {
					t.Fatalf("invalid partition: %v", r)
				}
				slice := text[r.Start:r.End]
				if !utf8.Valid(slice) || s.ContentSHA256 != SHA256(sha256.Sum256(slice)) {
					t.Fatal("invalid slice or hash")
				}
				if err := s.ValidateContentAgainst(rep, text); err != nil {
					t.Fatal(err)
				}
				got = append(got, string(slice))
				end = r.End
			}
			if end != uint64(len(text)) || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("passages = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPassagePolicyIdentityAndValidation(t *testing.T) {
	raw := []byte("unchanged source")
	artifact := artifactForContent(t, raw)
	fixed, text, err := NormalizeUTF8Text(artifact, raw)
	if err != nil {
		t.Fatal(err)
	}
	explicit, _, err := NormalizeUTF8TextWithPolicy(artifact, raw, TextSegmentFixedV1)
	if err != nil || !reflect.DeepEqual(fixed, explicit) {
		t.Fatal("fixed policy changed historical representation")
	}
	passage, normalized, err := NormalizeUTF8TextWithPolicy(artifact, raw, TextSegmentPassageV1)
	if err != nil {
		t.Fatal(err)
	}
	if passage.ID == fixed.ID || !bytes.Equal(text, normalized) || !reflect.DeepEqual(passage.Inputs, fixed.Inputs) {
		t.Fatal("policy must change identity, not bytes or ancestry")
	}
	encoded, err := EncodeRepresentation(passage)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRepresentation(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if policy, err := TextSegmentationPolicy(decoded); err != nil || policy != TextSegmentPassageV1 {
		t.Fatalf("persisted policy = %q, %v", policy, err)
	}
	if _, _, err := NormalizeUTF8TextWithPolicy(artifact, raw, "unknown"); err == nil {
		t.Fatal("unknown policy accepted")
	}
	passage.ParametersSHA256 = SHA256(sha256.Sum256([]byte("unknown")))
	passage.ID, err = NewRepresentationID(passage.Inputs, passage.ProcessorID, passage.ProcessorVersion, passage.ParametersSHA256, passage.MediaType, passage.ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SegmentUTF8Text(passage, text); err == nil {
		t.Fatal("unknown canonical parameters accepted")
	}
}

func TestPassageFenceClosingAndHeadingBoundaries(t *testing.T) {
	// A shorter run, another marker, or trailing nonspace must not end a fence.
	text := []byte("````sh\n```\n~~~\n```` nope\n\n# inside\n````\n\n# outside\nnext\n")
	end, heading := passageBlock(text, 0)
	if heading || string(text[end:]) != "# outside\nnext\n" {
		t.Fatalf("fence ended at %d", end)
	}
	// The table and list remain intact even without separating blank lines.
	for _, text := range []string{"paragraph\n- one\n- two\n", "paragraph\n| a | b |\n|---|---|\n"} {
		end, _ := passageBlock([]byte(text), 0)
		if text[:end] != "paragraph\n" {
			t.Fatalf("missing block boundary: %q", text[:end])
		}
	}
}

func TestPassageCountBound(t *testing.T) {
	raw := []byte(strings.Repeat("# h\n", MaxUTF8Passages+1))
	rep, text, err := NormalizeUTF8TextWithPolicy(artifactForContent(t, raw), raw, TextSegmentPassageV1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SegmentUTF8Text(rep, text); err == nil {
		t.Fatal("unbounded passage count accepted")
	}
}

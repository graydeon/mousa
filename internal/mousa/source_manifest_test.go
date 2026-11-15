package mousa

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSourceManifestEmptyGolden(t *testing.T) {
	manifest, err := NewSourceManifest(SourceID{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	const golden = "3f837bab2c92e6344cf8b39e139dd19c625c3cd3f724638859e86f51118d59dc"
	if manifest.ID.String() != golden {
		t.Fatalf("empty identity = %s", manifest.ID)
	}
	data, err := EncodeSourceManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("{\"schema\":\"mousa.source_manifest.v1\",\"id\":\"%s\",\"source_id\":\"01%s\",\"items\":[]}\n", golden, strings.Repeat("00", 31))
	if string(data) != want {
		t.Fatalf("canonical bytes = %s", data)
	}
	decoded, err := DecodeSourceManifest(data)
	if err != nil || decoded.ID != manifest.ID {
		t.Fatalf("roundtrip: %+v %v", decoded, err)
	}
	digest, err := SourceManifestSegmentsSHA256(nil)
	if err != nil || digest.String() != "c959fe71d144f1a98628c27119f57e9f262fad91f59cfd618fd7fa767380633d" {
		t.Fatalf("empty inventory = %s %v", digest, err)
	}
	for _, malformed := range []string{
		strings.Replace(want, `"items":[]`, `"items":null`, 1),
		strings.Replace(want, `"items":[]`, `"items":[],"items":[]`, 1),
		strings.Replace(want, `"schema":`, `"Schema":`, 1),
		strings.Replace(want, golden, strings.Repeat("0", 64), 1),
		strings.Replace(want, `"items":[]`, `"items":[],"extra":0`, 1),
	} {
		if _, err := DecodeSourceManifest([]byte(malformed)); err == nil {
			t.Fatalf("accepted %s", malformed)
		}
	}
	for _, value := range []string{"", strings.Repeat("0", 64), strings.ToUpper(golden), golden + " ", " " + golden, "0x" + golden} {
		if _, err := ParseSourceManifestID(value); err == nil {
			t.Fatalf("accepted ID %q", value)
		}
	}
}

func manifestTestItem() SourceManifestItem {
	parameters, _ := textPolicyParameters(TextSegmentFixedV1)
	return SourceManifestItem{Item: "a", ObservationID: ObservationID{1}, ArtifactID: ArtifactID{2}, RepresentationID: RepresentationID{3}, RawSHA256: SHA256{4}, RawByteLength: 1, NormalizedSHA256: SHA256{5}, NormalizedByteLength: 1, ProcessorID: UTF8TextProcessorID, ProcessorVersion: UTF8TextProcessorVersion, ParametersSHA256: parameters, SegmentationPolicy: TextSegmentFixedV1, SegmentCount: 1, SegmentsSHA256: SHA256{6}}
}

func TestSourceManifestIndependentInventoryRecipe(t *testing.T) {
	text := []byte("hello")
	content := SHA256(sha256.Sum256(text))
	rep := RepresentationID{1}
	selector := NewTextByteRangeSelector(0, 5)
	id, err := NewSegmentID(rep, selector, content)
	if err != nil {
		t.Fatal(err)
	}
	segment := Segment{Schema: SegmentSchema, ID: id, RepresentationID: rep, Selector: selector, ContentSHA256: content}
	got, err := SourceManifestSegmentsSHA256([]Segment{segment})
	if err != nil {
		t.Fatal(err)
	}
	payload := fmt.Sprintf("[{\"segment_id\":\"%s\",\"selector\":{\"schema\":\"mousa.selector.text_byte_range.v1\",\"start\":0,\"end\":5},\"content_sha256\":\"%s\"}]\n", id, content)
	var tuple bytes.Buffer
	for _, part := range []string{"mousa.source_manifest_segments.v1", payload} {
		if err := binary.Write(&tuple, binary.BigEndian, uint64(len(part))); err != nil {
			t.Fatal(err)
		}
		tuple.WriteString(part)
	}
	if got != SHA256(sha256.Sum256(tuple.Bytes())) {
		t.Fatal("inventory differs from independent tuple recipe")
	}
}

func TestSourceManifestConjunctiveBounds(t *testing.T) {
	item := manifestTestItem()
	item.NormalizedByteLength = MaxSourceManifestTextBytes
	item.SegmentCount = MaxSourceManifestSegments
	if _, err := NewSourceManifest(SourceID{1}, []SourceManifestItem{item}); err != nil {
		t.Fatalf("exact text/segment bounds: %v", err)
	}
	for _, mutate := range []func(*SourceManifestItem){
		func(i *SourceManifestItem) { i.NormalizedByteLength++ },
		func(i *SourceManifestItem) { i.SegmentCount++ },
		func(i *SourceManifestItem) { i.Item = strings.Repeat("x", 4097) },
	} {
		candidate := item
		mutate(&candidate)
		if _, err := NewSourceManifest(SourceID{1}, []SourceManifestItem{candidate}); !errors.Is(err, ErrSourceManifestLimit) {
			t.Fatalf("over bound: %v", err)
		}
	}
	items := make([]SourceManifestItem, MaxSourceManifestItems+1)
	for i := range items {
		items[i] = manifestTestItem()
		items[i].Item = fmt.Sprintf("%04d", i)
	}
	if _, err := NewSourceManifest(SourceID{1}, items); !errors.Is(err, ErrSourceManifestLimit) {
		t.Fatalf("item cap: %v", err)
	}
	// The encoded bound is conjunctive: even 1000 short, otherwise valid
	// metadata entries do not fit this schema's 512 KiB certificate budget.
	if _, err := NewSourceManifest(SourceID{1}, items[:MaxSourceManifestItems]); !errors.Is(err, ErrSourceManifestLimit) {
		t.Fatalf("encoded cap: %v", err)
	}
}

func TestSourceManifestExactEncodedByteLimit(t *testing.T) {
	items := make([]SourceManifestItem, 110)
	for i := range items {
		items[i] = manifestTestItem()
		items[i].Item = fmt.Sprintf("%03d", i)
	}
	base, err := NewSourceManifest(SourceID{1}, items)
	if err != nil {
		t.Fatal(err)
	}
	data, err := EncodeSourceManifest(base)
	if err != nil {
		t.Fatal(err)
	}
	remaining := MaxSourceManifestBytes - len(data)
	for i := range items {
		add := min(remaining, 4096-len(items[i].Item))
		items[i].Item += strings.Repeat("x", add)
		remaining -= add
	}
	if remaining != 0 {
		t.Fatal("fixture cannot reach encoded boundary")
	}
	exact, err := NewSourceManifest(SourceID{1}, items)
	if err != nil {
		t.Fatalf("exact encoded boundary: %v", err)
	}
	data, err = EncodeSourceManifest(exact)
	if err != nil || len(data) != MaxSourceManifestBytes {
		t.Fatalf("exact encoding: %d %v", len(data), err)
	}
	items[len(items)-1].Item += "x"
	if _, err := NewSourceManifest(SourceID{1}, items); !errors.Is(err, ErrSourceManifestLimit) {
		t.Fatalf("one byte over encoded bound: %v", err)
	}
}

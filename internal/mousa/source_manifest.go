package mousa

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

const (
	SourceManifestSchema       = "mousa.source_manifest.v1"
	MaxSourceManifestItems     = 1000
	MaxSourceManifestSegments  = 10000
	MaxSourceManifestTextBytes = 16 << 20
	MaxSourceManifestBytes     = 512 << 10
)

var ErrSourceManifestLimit = errors.New("source manifest resource limit exceeded")

type SourceManifest struct {
	Schema   string               `json:"schema"`
	ID       SHA256               `json:"id"`
	SourceID SourceID             `json:"source_id"`
	Items    []SourceManifestItem `json:"items"`
}

type SourceManifestItem struct {
	Item                 string           `json:"item"`
	ObservationID        ObservationID    `json:"observation_id"`
	ArtifactID           ArtifactID       `json:"artifact_id"`
	RepresentationID     RepresentationID `json:"representation_id"`
	RawSHA256            SHA256           `json:"raw_sha256"`
	RawByteLength        uint64           `json:"raw_byte_length"`
	NormalizedSHA256     SHA256           `json:"normalized_sha256"`
	NormalizedByteLength uint64           `json:"normalized_byte_length"`
	ProcessorID          string           `json:"processor_id"`
	ProcessorVersion     string           `json:"processor_version"`
	ParametersSHA256     SHA256           `json:"parameters_sha256"`
	SegmentationPolicy   string           `json:"segmentation_policy"`
	SegmentCount         int              `json:"segment_count"`
	SegmentsSHA256       SHA256           `json:"segments_sha256"`
}

func ParseSourceManifestID(value string) (SHA256, error) {
	id, err := ParseSHA256(value)
	if err == nil && id == (SHA256{}) {
		err = errors.New("manifest ID must be nonzero")
	}
	return id, err
}

func NewSourceManifest(source SourceID, items []SourceManifestItem) (SourceManifest, error) {
	if items == nil {
		items = []SourceManifestItem{}
	}
	manifest := SourceManifest{Schema: SourceManifestSchema, SourceID: source, Items: items}
	id, err := manifest.identity()
	if err != nil {
		return SourceManifest{}, err
	}
	manifest.ID = id
	return manifest, nil
}

func (manifest SourceManifest) identity() (SHA256, error) {
	if manifest.Schema != SourceManifestSchema || manifest.SourceID == (SourceID{}) || manifest.Items == nil {
		return SHA256{}, errors.New("invalid source manifest schema, source or inventory")
	}
	if len(manifest.Items) > MaxSourceManifestItems {
		return SHA256{}, ErrSourceManifestLimit
	}
	segments, size := 0, uint64(0)
	encodedSize := 0
	for i, item := range manifest.Items {
		if item.Item == "" || !utf8.ValidString(item.Item) || (i > 0 && manifest.Items[i-1].Item >= item.Item) {
			return SHA256{}, errors.New("manifest items require nonempty UTF-8 identities in strict byte order")
		}
		if len(item.Item) > 4096 || item.SegmentCount < 0 || item.SegmentCount > MaxSourceManifestSegments-segments || item.NormalizedByteLength > MaxSourceManifestTextBytes-size {
			return SHA256{}, ErrSourceManifestLimit
		}
		segments += item.SegmentCount
		size += item.NormalizedByteLength
		if item.ObservationID == (ObservationID{}) || item.ArtifactID == (ArtifactID{}) || item.RepresentationID == (RepresentationID{}) || item.RawSHA256 == (SHA256{}) || item.NormalizedSHA256 == (SHA256{}) || item.ParametersSHA256 == (SHA256{}) || item.SegmentsSHA256 == (SHA256{}) {
			return SHA256{}, errors.New("manifest item requires nonzero provenance pins")
		}
		if item.ProcessorID != UTF8TextProcessorID || item.ProcessorVersion != UTF8TextProcessorVersion {
			return SHA256{}, errors.New("manifest requires supported local text normalizer")
		}
		parameters, err := textPolicyParameters(item.SegmentationPolicy)
		if err != nil || parameters != item.ParametersSHA256 {
			return SHA256{}, errors.New("manifest segmentation parameters disagree")
		}
		if (item.NormalizedByteLength == 0) != (item.SegmentCount == 0) {
			return SHA256{}, errors.New("manifest segment count disagrees with content length")
		}
		data, err := encodeJSONContract(item, "source manifest item")
		if err != nil {
			return SHA256{}, err
		}
		if len(data) > MaxSourceManifestBytes-encodedSize {
			return SHA256{}, ErrSourceManifestLimit
		}
		encodedSize += len(data)
	}
	payload := struct {
		SourceID SourceID             `json:"source_id"`
		Items    []SourceManifestItem `json:"items"`
	}{manifest.SourceID, manifest.Items}
	data, err := encodeJSONContract(payload, "source manifest identity")
	if err != nil {
		return SHA256{}, err
	}
	const envelopeBytes = len(`"schema":"`) + len(SourceManifestSchema) + len(`","id":"`) + 64 + len(`",`)
	if len(data) > MaxSourceManifestBytes-envelopeBytes {
		return SHA256{}, ErrSourceManifestLimit
	}
	digest := sha256.New()
	writeTuple(digest, []byte(SourceManifestSchema), data)
	return SHA256(digest.Sum(nil)), nil
}

func (manifest SourceManifest) Validate() error {
	id, err := manifest.identity()
	if err != nil {
		return err
	}
	if id != manifest.ID {
		return errors.New("manifest ID disagrees with content")
	}
	return nil
}

func EncodeSourceManifest(manifest SourceManifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	data, err := encodeJSONContract(manifest, "source manifest")
	if len(data) > MaxSourceManifestBytes {
		return nil, ErrSourceManifestLimit
	}
	return data, err
}

func DecodeSourceManifest(data []byte) (SourceManifest, error) {
	var manifest SourceManifest
	if len(data) > MaxSourceManifestBytes {
		return manifest, ErrSourceManifestLimit
	}
	object, err := vectorObject(data, "schema", "id", "source_id", "items")
	if err != nil {
		return manifest, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(object["items"], &items); err != nil {
		return manifest, err
	}
	if len(items) > MaxSourceManifestItems {
		return manifest, ErrSourceManifestLimit
	}
	for _, item := range items {
		if _, err := vectorObject(item, "item", "observation_id", "artifact_id", "representation_id", "raw_sha256", "raw_byte_length", "normalized_sha256", "normalized_byte_length", "processor_id", "processor_version", "parameters_sha256", "segmentation_policy", "segment_count", "segments_sha256"); err != nil {
			return manifest, err
		}
	}
	if err := decodeJSONContract(data, &manifest); err != nil {
		return manifest, err
	}
	return manifest, manifest.Validate()
}

// SourceManifestSegmentsSHA256 binds the exact ordered canonical segment inventory.
func SourceManifestSegmentsSHA256(segments []Segment) (SHA256, error) {
	if len(segments) > MaxSourceManifestSegments {
		return SHA256{}, ErrSourceManifestLimit
	}
	type entry struct {
		SegmentID     SegmentID       `json:"segment_id"`
		Selector      SegmentSelector `json:"selector"`
		ContentSHA256 SHA256          `json:"content_sha256"`
	}
	entries := make([]entry, 0, len(segments))
	var end uint64
	for i, segment := range segments {
		if err := segment.Validate(); err != nil {
			return SHA256{}, err
		}
		span, ok := segment.Selector.TextByteRange()
		if !ok || span.Start != end || (i > 0 && segment.RepresentationID != segments[0].RepresentationID) {
			return SHA256{}, fmt.Errorf("segment %d is not a contiguous current inventory", i)
		}
		end = span.End
		entries = append(entries, entry{segment.ID, segment.Selector, segment.ContentSHA256})
	}
	data, err := encodeJSONContract(entries, "source manifest segments")
	if err != nil {
		return SHA256{}, err
	}
	digest := sha256.New()
	writeTuple(digest, []byte("mousa.source_manifest_segments.v1"), data)
	return SHA256(digest.Sum(nil)), nil
}

package beir

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
)

// SegmentPolicy selects the core text segmentation policy.
type SegmentPolicy string

const (
	SegmentFixedV1   SegmentPolicy = mousa.TextSegmentFixedV1
	SegmentPassageV1 SegmentPolicy = mousa.TextSegmentPassageV1
)

// NormalizeSegmentPolicy preserves fixed-v1 for callers omitting the option.
func NormalizeSegmentPolicy(policy SegmentPolicy) (SegmentPolicy, error) {
	switch policy {
	case "", SegmentFixedV1:
		return SegmentFixedV1, nil
	case SegmentPassageV1:
		return policy, nil
	default:
		return "", fmt.Errorf("unknown segmentation policy %q", policy)
	}
}

// SegmentationIdentity binds the dispatcher and canonical normalizer parameters.
// A zero identity is historical evidence, not certified segmentation provenance.
type SegmentationIdentity struct {
	Policy           SegmentPolicy `json:"policy"`
	ProcessorID      string        `json:"processor_id"`
	ProcessorVersion string        `json:"processor_version"`
	ParametersSHA256 string        `json:"parameters_sha256"`
}

// SegmentIdentity obtains the parameters from the core normalizer, not a copy of
// its parameter encoding. The versioned policy names pin segmentation behavior.
func SegmentIdentity(policy SegmentPolicy) (SegmentationIdentity, error) {
	policy, err := NormalizeSegmentPolicy(policy)
	if err != nil {
		return SegmentationIdentity{}, err
	}
	source, err := mousa.NewSourceID("beir-eval", "segmentation-identity")
	if err != nil {
		return SegmentationIdentity{}, err
	}
	observation, err := mousa.NewObservationID(source, "parameters")
	if err != nil {
		return SegmentationIdentity{}, err
	}
	artifactID, err := mousa.NewArtifactID(observation, "body")
	if err != nil {
		return SegmentationIdentity{}, err
	}
	artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observation, ArtifactKey: "body", MediaType: mousa.UTF8TextMediaType, ContentSHA256: mousa.SHA256(sha256.Sum256(nil))}
	representation, _, err := mousa.NormalizeUTF8TextWithPolicy(artifact, nil, string(policy))
	if err != nil {
		return SegmentationIdentity{}, err
	}
	return SegmentationIdentity{Policy: policy, ProcessorID: representation.ProcessorID, ProcessorVersion: representation.ProcessorVersion, ParametersSHA256: fmt.Sprintf("%x", representation.ParametersSHA256[:])}, nil
}

// ValidateSegmentationIdentity rejects missing, unknown and altered identities.
func ValidateSegmentationIdentity(identity SegmentationIdentity) error {
	if identity.Policy == "" {
		return fmt.Errorf("missing segmentation policy provenance")
	}
	expected, err := SegmentIdentity(identity.Policy)
	if err != nil {
		return err
	}
	if identity != expected {
		return fmt.Errorf("segmentation identity does not match core policy %q", identity.Policy)
	}
	return nil
}

type storeManifest struct {
	Version         int                  `json:"version"`
	Dataset         string               `json:"dataset"`
	CorpusSHA256    string               `json:"corpus_sha256"`
	CorpusDocuments int                  `json:"corpus_documents"`
	Segmentation    SegmentationIdentity `json:"segmentation"`
	Complete        bool                 `json:"complete"`
}

func corpusManifest(dataset *Dataset, identity SegmentationIdentity, complete bool) storeManifest {
	return storeManifest{Version: 1, Dataset: dataset.Name, CorpusSHA256: dataset.Content.CorpusSHA256, CorpusDocuments: len(dataset.Corpus), Segmentation: identity, Complete: complete}
}

func writeStoreManifest(path string, manifest storeManifest) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(path+".manifest.json", append(raw, '\n'), 0o644)
}

func checkStoreManifest(path string, dataset *Dataset, identity SegmentationIdentity) error {
	raw, err := os.ReadFile(path + ".manifest.json")
	if os.IsNotExist(err) && identity.Policy == SegmentFixedV1 {
		// Legacy fixed stores require exact canonical and indexed correspondence
		// before OpenExistingWithPolicy returns. Missing metadata never implies passage.
		return nil
	}
	if err != nil {
		return fmt.Errorf("store segmentation manifest: %w", err)
	}
	if _, err := ReadSegmentationMetadata(raw, true); err != nil {
		return err
	}
	var stored storeManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return fmt.Errorf("store segmentation manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("store segmentation manifest contains trailing data")
	}
	if err := ValidateSegmentationIdentity(stored.Segmentation); err != nil {
		return err
	}
	if stored != corpusManifest(dataset, identity, true) {
		return fmt.Errorf("store segmentation manifest is incomplete or does not match this corpus and policy")
	}
	return nil
}

// ReadSegmentationMetadata validates the identity before permissive struct decoding.
// Historical reports alone may omit it; manifests require an explicit identity.
func ReadSegmentationMetadata(raw []byte, required bool) (SegmentationIdentity, error) {
	fields, err := uniqueMetadataObject(raw)
	if err != nil {
		return SegmentationIdentity{}, err
	}
	for key := range fields {
		if strings.EqualFold(key, "segmentation") && key != "segmentation" {
			return SegmentationIdentity{}, fmt.Errorf("noncanonical segmentation member %q", key)
		}
	}
	value, present := fields["segmentation"]
	if !present && !required {
		return SegmentationIdentity{}, nil
	}
	parts, err := uniqueMetadataObject(value)
	if err != nil {
		return SegmentationIdentity{}, fmt.Errorf("segmentation: %w", err)
	}
	var identity SegmentationIdentity
	policy := ""
	expected := map[string]*string{
		"policy": &policy, "processor_id": &identity.ProcessorID,
		"processor_version": &identity.ProcessorVersion, "parameters_sha256": &identity.ParametersSHA256,
	}
	if len(parts) != len(expected) {
		return SegmentationIdentity{}, fmt.Errorf("segmentation requires exactly four identity members")
	}
	for key, target := range expected {
		part, ok := parts[key]
		if !ok || bytes.Equal(bytes.TrimSpace(part), []byte("null")) {
			return SegmentationIdentity{}, fmt.Errorf("missing or null segmentation member %q", key)
		}
		if err := json.Unmarshal(part, target); err != nil {
			return SegmentationIdentity{}, err
		}
	}
	identity.Policy = SegmentPolicy(policy)
	if err := ValidateSegmentationIdentity(identity); err != nil {
		return SegmentationIdentity{}, err
	}
	return identity, nil
}

func uniqueMetadataObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("metadata must be a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("invalid metadata member")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("duplicate metadata member %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		fields[key] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing metadata")
	}
	return fields, nil
}

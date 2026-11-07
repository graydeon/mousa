package beir

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/graydeon/mousa/internal/mousa"
)

func segmentationDataset(t *testing.T) *Dataset {
	t.Helper()
	dir, initial := ingestFixture(t,
		map[string]string{"a": "\ufeff" + strings.Repeat("猫 café needle.\r\n", 420) + "\r\nend", "b": strings.Repeat("界", 1500) + " orchard"},
		map[string]string{"q1": "needle", "q2": "orchard"},
		map[string]map[string]int{"q1": {"a": 1}, "q2": {"b": 1}})
	initial.Close()
	dataset, err := LoadDataset("fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	return dataset
}

func TestSegmentationExactCanonicalMappingAndReuse(t *testing.T) {
	dataset := segmentationDataset(t)
	for _, policy := range []SegmentPolicy{SegmentFixedV1, SegmentPassageV1} {
		t.Run(string(policy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.sqlite")
			ingested, err := IngestCorpusWithPolicy(t.Context(), dataset, path, policy)
			if err != nil {
				t.Fatal(err)
			}
			defer ingested.Close()
			wantSegments := map[mousa.SegmentID]string{}
			wantRepresentations := map[mousa.RepresentationID]string{}
			ids := make([]string, 0, len(dataset.Corpus))
			for id := range dataset.Corpus {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for index, id := range ids {
				observation, err := mousa.NewObservationID(ingested.Source.ID, fmt.Sprintf("doc-%d", index))
				if err != nil {
					t.Fatal(err)
				}
				artifactID, err := mousa.NewArtifactID(observation, "body")
				if err != nil {
					t.Fatal(err)
				}
				content := []byte(dataset.Corpus[id])
				artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observation, ArtifactKey: "body", MediaType: mousa.UTF8TextMediaType, ByteLength: uint64(len(content)), ContentSHA256: mousa.SHA256(sha256.Sum256(content))}
				representation, normalized, err := mousa.NormalizeUTF8TextWithPolicy(artifact, content, string(policy))
				if err != nil {
					t.Fatal(err)
				}
				wantRepresentations[representation.ID] = id
				segments, err := mousa.SegmentUTF8Text(representation, normalized)
				if err != nil {
					t.Fatal(err)
				}
				var end uint64
				for _, segment := range segments {
					span, ok := segment.Selector.TextByteRange()
					if !ok || span.Start != end || !utf8.Valid(normalized[span.Start:span.End]) {
						t.Fatal("non-contiguous or invalid Unicode slice")
					}
					bound := uint64(4096)
					if policy == SegmentPassageV1 {
						bound = 1024
					}
					if span.End-span.Start > bound {
						t.Fatal("segment exceeded policy byte bound")
					}
					if err := segment.ValidateContentAgainst(representation, normalized); err != nil {
						t.Fatal(err)
					}
					end = span.End
					wantSegments[segment.ID] = id
				}
				if end != uint64(len(normalized)) {
					t.Fatal("source bytes omitted")
				}
			}
			if !reflect.DeepEqual(ingested.SegmentToDoc, wantSegments) || !reflect.DeepEqual(ingested.DocRepresentations, wantRepresentations) {
				t.Fatal("ingestion differs from canonical policy mapping")
			}
			config := RunConfig{Mode: ModeTraced, Policy: PolicyOriginal, Limit: 100, Budget: 1024, SegmentPolicy: policy}
			before, err := SearchAll(t.Context(), ingested, dataset, config, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			ingested.Close()
			reused, err := OpenExistingWithPolicy(t.Context(), dataset, path, policy)
			if err != nil {
				t.Fatal(err)
			}
			defer reused.Close()
			if !reflect.DeepEqual(reused.SegmentToDoc, wantSegments) || !reflect.DeepEqual(reused.DocRepresentations, wantRepresentations) {
				t.Fatal("reuse changed exact mapping")
			}
			after, err := SearchAll(t.Context(), reused, dataset, config, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			for i := range before {
				if !reflect.DeepEqual(before[i].RankedDocIDs, after[i].RankedDocIDs) || !reflect.DeepEqual(before[i].SelectedDocIDs, after[i].SelectedDocIDs) || before[i].UsedBytes != after[i].UsedBytes {
					t.Fatal("reuse changed document results")
				}
			}
			wrong := SegmentFixedV1
			if policy == wrong {
				wrong = SegmentPassageV1
			}
			if opened, err := OpenExistingWithPolicy(t.Context(), dataset, path, wrong); err == nil {
				opened.Close()
				t.Fatal("wrong-policy store reuse accepted")
			}
			config.SegmentPolicy = wrong
			calls := 0
			if results, err := SearchAll(t.Context(), reused, dataset, config, map[string]SearchResult{before[0].QueryID: before[0]}, func(SearchResult) { calls++ }); err == nil || len(results) != 0 || calls != 0 {
				t.Fatal("wrong policy produced or resumed results")
			}
		})
	}
}

func TestSegmentationInvalidBeforeStoreWrites(t *testing.T) {
	dataset := segmentationDataset(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.sqlite")
	if err := os.WriteFile(path, []byte("sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{path, filepath.Join(dir, "absent", "store.sqlite")} {
		if store, err := IngestCorpusWithPolicy(t.Context(), dataset, target, "unknown"); err == nil {
			store.Close()
			t.Fatal("unknown policy accepted")
		}
		if store, err := OpenExistingWithPolicy(t.Context(), dataset, target, "unknown"); err == nil {
			store.Close()
			t.Fatal("unknown reuse policy accepted")
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "sentinel" {
		t.Fatal("invalid policy modified existing store")
	}
	if _, err := os.Stat(filepath.Join(dir, "absent")); !os.IsNotExist(err) {
		t.Fatal("invalid policy created parent directory")
	}
}

func TestSegmentationStoreManifestRefusalsAndLegacyFixed(t *testing.T) {
	dataset := segmentationDataset(t)
	for _, policy := range []SegmentPolicy{SegmentFixedV1, SegmentPassageV1} {
		t.Run(string(policy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.sqlite")
			store, err := IngestCorpusWithPolicy(t.Context(), dataset, path, policy)
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			manifest := path + ".manifest.json"
			original, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			for name, mutate := range map[string]func(map[string]any){
				"incomplete":       func(m map[string]any) { m["complete"] = false },
				"missing-identity": func(m map[string]any) { delete(m, "segmentation") },
				"unknown-policy":   func(m map[string]any) { m["segmentation"].(map[string]any)["policy"] = "future-v9" },
				"parameters": func(m map[string]any) {
					m["segmentation"].(map[string]any)["parameters_sha256"] = strings.Repeat("0", 64)
				},
				"corpus": func(m map[string]any) { m["corpus_sha256"] = "changed" },
				"count":  func(m map[string]any) { m["corpus_documents"] = 999 },
			} {
				t.Run(name, func(t *testing.T) {
					var fields map[string]any
					if err := json.Unmarshal(original, &fields); err != nil {
						t.Fatal(err)
					}
					mutate(fields)
					raw, err := json.Marshal(fields)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(manifest, raw, 0600); err != nil {
						t.Fatal(err)
					}
					if reused, err := OpenExistingWithPolicy(t.Context(), dataset, path, policy); err == nil {
						reused.Close()
						t.Fatal("damaged provenance accepted")
					}
				})
			}
			if err := os.Remove(manifest); err != nil {
				t.Fatal(err)
			}
			reused, err := OpenExistingWithPolicy(t.Context(), dataset, path, policy)
			if policy == SegmentFixedV1 {
				if err != nil {
					t.Fatalf("provable legacy fixed store refused: %v", err)
				}
				reused.Close()
			} else if err == nil {
				reused.Close()
				t.Fatal("missing manifest certified passage store")
			}
			if _, err := os.Stat(manifest); !os.IsNotExist(err) {
				t.Fatal("legacy reuse manufactured metadata")
			}
		})
	}
}

func TestSegmentationInterruptedIngestionAndQueryRecovery(t *testing.T) {
	dataset := segmentationDataset(t)
	for _, policy := range []SegmentPolicy{SegmentFixedV1, SegmentPassageV1} {
		t.Run(string(policy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.sqlite")
			ctx, cancel := context.WithCancel(t.Context())
			oldProgress := IngestProgress
			defer func() { IngestProgress = oldProgress }()
			IngestProgress = func(done, total int, elapsed float64) { cancel() }
			if store, err := IngestCorpusWithPolicy(ctx, dataset, path, policy); err == nil {
				store.Close()
				t.Fatal("canceled partial ingestion succeeded")
			}
			IngestProgress = oldProgress
			if store, err := OpenExistingWithPolicy(t.Context(), dataset, path, policy); err == nil {
				store.Close()
				t.Fatal("partial ingestion reused")
			}
			store, err := IngestCorpusWithPolicy(t.Context(), dataset, path, policy)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			config := RunConfig{Mode: ModeTraced, Policy: PolicyOriginal, Limit: 100, Budget: 1024, SegmentPolicy: policy}
			ctx, cancel = context.WithCancel(t.Context())
			defer cancel()
			skip := map[string]SearchResult{}
			_, err = SearchAll(ctx, store, dataset, config, nil, func(r SearchResult) { skip[r.QueryID] = r; cancel() })
			if err == nil || len(skip) != 1 {
				t.Fatalf("expected interrupted one-query prefix, got %d: %v", len(skip), err)
			}
			calls := 0
			results, err := SearchAll(t.Context(), store, dataset, config, skip, func(SearchResult) { calls++ })
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 2 || calls != 1 {
				t.Fatal("resume did not retain complete population exactly once")
			}
			for i, id := range dataset.JudgedQueries() {
				if results[i].QueryID != id {
					t.Fatal("resume changed query order")
				}
				if saved, ok := skip[id]; ok && !reflect.DeepEqual(saved, results[i]) {
					t.Fatal("resume changed journaled result")
				}
			}
		})
	}
}

func TestSegmentationDefaultNamespaceAndMapping(t *testing.T) {
	dataset := segmentationDataset(t)
	legacy, err := IngestCorpus(t.Context(), dataset, filepath.Join(t.TempDir(), "default.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	explicit, err := IngestCorpusWithPolicy(t.Context(), dataset, filepath.Join(t.TempDir(), "fixed.sqlite"), SegmentFixedV1)
	if err != nil {
		t.Fatal(err)
	}
	defer explicit.Close()
	if !reflect.DeepEqual(legacy.SegmentToDoc, explicit.SegmentToDoc) {
		t.Fatal("default is no longer fixed-v1")
	}
	config := RunConfig{Mode: ModeTraced, Policy: PolicyOriginal, Limit: 100}
	defaultNamespace := config.Namespace()
	config.SegmentPolicy = SegmentFixedV1
	if config.Namespace() != defaultNamespace {
		t.Fatal("implicit and explicit fixed identities differ")
	}
	config.SegmentPolicy = SegmentPassageV1
	if config.Namespace() == defaultNamespace {
		t.Fatal("namespace ignores segmentation")
	}
}

func TestMissingManifestCannotRelabelPassageAsLegacyFixed(t *testing.T) {
	dataset := segmentationDataset(t)
	path := filepath.Join(t.TempDir(), "passage.sqlite")
	store, err := IngestCorpusWithPolicy(t.Context(), dataset, path, SegmentPassageV1)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	if err := os.Remove(path + ".manifest.json"); err != nil {
		t.Fatal(err)
	}
	if reused, err := OpenExisting(t.Context(), dataset, path); err == nil {
		reused.Close()
		t.Fatal("unmanifested passage store relabeled as legacy fixed")
	}
}

func TestInvalidSearchPolicyProducesNoResults(t *testing.T) {
	dataset := segmentationDataset(t)
	store, err := IngestCorpus(t.Context(), dataset, filepath.Join(t.TempDir(), "fixed.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	calls := 0
	config := RunConfig{Mode: ModeTraced, Limit: 100, Budget: 1024, SegmentPolicy: "unknown"}
	results, err := SearchAll(t.Context(), store, dataset, config, nil, func(SearchResult) { calls++ })
	if err == nil || len(results) != 0 || calls != 0 {
		t.Fatal("invalid search segmentation produced results")
	}
}

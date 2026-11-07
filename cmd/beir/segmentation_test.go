package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/eval/beir"
)

func TestResumeSegmentationProvenance(t *testing.T) {
	for _, policy := range []beir.SegmentPolicy{beir.SegmentFixedV1, beir.SegmentPassageV1} {
		t.Run(string(policy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "run.manifest.json")
			config := beir.RunConfig{Mode: beir.ModeTraced, Policy: beir.PolicyOriginal, Limit: 100, Budget: 1024, Dataset: "fixture", Corpus: 2, Judged: 2, SegmentPolicy: policy}
			if err := writeManifest(path, config, "store.sqlite", time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := checkManifest(path, config, time.Minute); err != nil {
				t.Fatal(err)
			}
			wrong := config
			wrong.SegmentPolicy = beir.SegmentFixedV1
			if policy == wrong.SegmentPolicy {
				wrong.SegmentPolicy = beir.SegmentPassageV1
			}
			if err := checkManifest(path, wrong, time.Minute); err == nil {
				t.Fatal("wrong-policy resume accepted")
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, extra := range []string{`,"segmentation":null}`, `,"segmentation":{}}`} {
				raw := strings.TrimSuffix(strings.TrimSpace(string(original)), "}") + extra
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
				if err := checkManifest(path, config, time.Minute); err == nil {
					t.Fatal("duplicate resume identity accepted")
				}
			}
			for name, mutate := range map[string]func(map[string]any){
				"missing": func(m map[string]any) { delete(m, "segmentation") },
				"unknown": func(m map[string]any) { m["segmentation"].(map[string]any)["policy"] = "unknown" },
				"tampered": func(m map[string]any) {
					m["segmentation"].(map[string]any)["parameters_sha256"] = strings.Repeat("0", 64)
				},
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
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
					if err := checkManifest(path, config, time.Minute); err == nil {
						t.Fatal("untrusted resume identity accepted")
					}
					after, err := os.ReadFile(path)
					if err != nil || string(after) != string(raw) {
						t.Fatal("rejected resume modified manifest")
					}
				})
			}
		})
	}
}

func TestCompareSegmentationUsesDocumentRankings(t *testing.T) {
	dir := t.TempDir()
	baseline := validComparableReport("fixture", "traced", "original", 1024)
	candidate := validComparableReport("fixture", "traced", "original", 1024)
	candidate.Segmentation, _ = beir.SegmentIdentity(beir.SegmentPassageV1)
	basePath, candidatePath, out := filepath.Join(dir, "fixed.json"), filepath.Join(dir, "passage.json"), filepath.Join(dir, "comparison.json")
	writeReport(t, basePath, baseline)
	writeReport(t, candidatePath, candidate)
	if err := compareReportTo(out, []string{basePath, candidatePath}, "segmentation"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var result compareReport
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.Identical || result.Axis != "segmentation" || result.BaselineSegmentation != baseline.Segmentation || result.CandidateSegmentation != candidate.Segmentation {
		t.Fatal("comparison lost axis identities or compared segment identities")
	}
	candidate.Queries[0].RankedDocIDs = []string{"d2", "d1"}
	writeReport(t, candidatePath, candidate)
	if err := compareReportTo(out, []string{basePath, candidatePath}, "segmentation"); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Identical || result.Criteria.Identity.Passed {
		t.Fatal("segmentation ranking change hidden")
	}
	for _, axis := range []string{"policy", "mode", "budget"} {
		refused := filepath.Join(dir, axis+".json")
		if err := compareReportTo(refused, []string{basePath, candidatePath}, axis); err == nil {
			t.Fatal("hidden segmentation mismatch accepted")
		}
		if _, err := os.Stat(refused); !os.IsNotExist(err) {
			t.Fatal("invalid comparison wrote output")
		}
	}
}

func TestRescoreSegmentationAndLegacyReadability(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fixture-qrels.tsv"), []byte("query-id\tcorpus-id\tscore\nq1\td1\t1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, policy := range []beir.SegmentPolicy{"", beir.SegmentFixedV1, beir.SegmentPassageV1} {
		t.Run("policy-"+string(policy), func(t *testing.T) {
			report := validComparableReport("fixture", "traced", "original", 1024)
			report.Segmentation = beir.SegmentationIdentity{}
			if policy != "" {
				report.Segmentation, _ = beir.SegmentIdentity(policy)
			}
			path := filepath.Join(dir, string(policy)+"report.json")
			writeReport(t, path, report)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, string(policy)+"rescore.json")
			if err := rescoreReportTo(dir, out, []string{path}, true); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			var result rescoreReport
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Entries) != 1 || result.Entries[0].NDCGAt10 != 1 {
				t.Fatal("rescore changed saved document evidence")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("rescore rewrote historical input")
			}
			if policy != "" {
				report.Segmentation.ParametersSHA256 = "tampered"
				writeReport(t, path, report)
				refused := filepath.Join(dir, string(policy)+"invalid-rescore.json")
				if err := rescoreReportTo(dir, refused, []string{path}, true); err == nil {
					t.Fatal("rescore accepted tampered identity")
				}
				if _, err := os.Stat(refused); !os.IsNotExist(err) {
					t.Fatal("invalid rescore wrote results")
				}
			}
		})
	}
}

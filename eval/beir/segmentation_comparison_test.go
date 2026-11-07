package beir

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSegmentationComparisonAxis(t *testing.T) {
	base := tracedReport("fixture", 1024, 100, &PackCoverage{Queries: 2})
	candidate := tracedReport("fixture", 1024, 100, &PackCoverage{Queries: 2})
	candidate.Segmentation, _ = SegmentIdentity(SegmentPassageV1)
	if err := RequireMatchingFields(base, candidate, "segmentation"); err != nil {
		t.Fatal(err)
	}
	if err := RequireMatchingFields(base, base, "segmentation"); err == nil {
		t.Fatal("segmentation axis accepted identical policies")
	}
	for _, axis := range []string{"policy", "mode", "budget"} {
		t.Run(axis, func(t *testing.T) {
			other := candidate
			switch axis {
			case "policy":
				other.ExpressionPolicy = string(PolicyDedup)
			case "mode":
				other.Mode = string(ModeVerified)
			case "budget":
				other.Budget++
			}
			if err := RequireMatchingFields(base, other, axis); err == nil {
				t.Fatal("hidden segmentation mismatch accepted")
			}
			if err := RequireMatchingFields(base, other, "segmentation"); err == nil {
				t.Fatal("second varying axis accepted")
			}
			other.Segmentation = base.Segmentation
			if err := RequireMatchingFields(base, other, axis); err != nil {
				t.Fatalf("existing axis refused: %v", err)
			}
		})
	}
	for name, mutate := range map[string]func(*RunReport){
		"dataset":         func(r *RunReport) { r.Dataset += "changed" },
		"corpus":          func(r *RunReport) { r.Content.CorpusSHA256 += "changed" },
		"queries":         func(r *RunReport) { r.Content.QueriesSHA256 += "changed" },
		"qrels":           func(r *RunReport) { r.Content.QrelsSHA256 += "changed" },
		"corpus-count":    func(r *RunReport) { r.CorpusDocuments++ },
		"judged-count":    func(r *RunReport) { r.JudgedQueries++ },
		"query-limit":     func(r *RunReport) { r.QueryLimit++ },
		"candidate-limit": func(r *RunReport) { r.Limit++ },
		"protocol":        func(r *RunReport) { r.Protocol.Name += "changed" },
		"order":           func(r *RunReport) { r.Queries[0], r.Queries[1] = r.Queries[1], r.Queries[0] },
		"population":      func(r *RunReport) { r.Queries = r.Queries[:1] },
	} {
		t.Run(name, func(t *testing.T) {
			other := candidate
			other.Queries = append([]QueryReport(nil), candidate.Queries...)
			mutate(&other)
			if err := RequireMatchingFields(base, other, "segmentation"); err == nil {
				t.Fatal("segmentation axis relaxed population or experiment identity")
			}
		})
	}
}

func TestSegmentationEvidenceAndCoverageRefusals(t *testing.T) {
	base := tracedReport("fixture", 1024, 100, &PackCoverage{Queries: 2})
	for name, mutate := range map[string]func(*SegmentationIdentity){
		"missing":    func(s *SegmentationIdentity) { *s = SegmentationIdentity{} },
		"unknown":    func(s *SegmentationIdentity) { s.Policy = "future-v9" },
		"parameters": func(s *SegmentationIdentity) { s.ParametersSHA256 = "tampered" },
		"processor":  func(s *SegmentationIdentity) { s.ProcessorID += "tampered" },
		"version":    func(s *SegmentationIdentity) { s.ProcessorVersion += "tampered" },
	} {
		t.Run(name, func(t *testing.T) {
			other := base
			mutate(&other.Segmentation)
			if err := ValidateComparable(&other); err == nil {
				t.Fatal("invalid segmentation accepted as comparable")
			}
			if err := RequireMatchingFields(base, other, "segmentation"); err == nil {
				t.Fatal("segmentation axis accepted invalid identity")
			}
			if _, err := CoverageCurve([]RunReport{other}); err == nil {
				t.Fatal("single-report curve accepted invalid identity")
			}
		})
	}
	other := base
	other.Budget = 4096
	other.Segmentation, _ = SegmentIdentity(SegmentPassageV1)
	if _, err := CoverageCurve([]RunReport{base, other}); err == nil {
		t.Fatal("budget curve mixed segmentations")
	}
}

func TestLegacyReportReadableWithoutCertification(t *testing.T) {
	report := tracedReport("fixture", 1024, 100, &PackCoverage{Queries: 2})
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "segmentation")
	raw, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRunReport(path)
	if err != nil {
		t.Fatalf("legacy report unreadable: %v", err)
	}
	if loaded.Segmentation != (SegmentationIdentity{}) {
		t.Fatal("legacy report acquired invented provenance")
	}
	if err := ValidateComparable(&loaded); err == nil {
		t.Fatal("legacy report certified")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("legacy bytes rewritten")
	}
}

func TestReportRejectsExplicitInvalidSegmentation(t *testing.T) {
	report := tracedReport("fixture", 1024, 100, &PackCoverage{Queries: 1})
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for name, identity := range map[string]string{
		"null":               "null",
		"empty":              "{}",
		"unknown":            `{"policy":"future-v9"}`,
		"missing-parameters": `{"policy":"fixed-v1","processor_id":"mousa.text.normalize_utf8","processor_version":"1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			fields["segmentation"] = json.RawMessage(identity)
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(path, encoded, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadRunReport(path); err == nil {
				t.Fatal("explicit invalid provenance treated as legacy omission")
			}
		})
	}
}

func TestReportRejectsCaseVariantSegmentation(t *testing.T) {
	for _, key := range []string{"Segmentation", "SEGMENTATION"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.json")
			raw := []byte(`{"` + key + `":{"policy":"unknown"}}`)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadRunReport(path); err == nil {
				t.Fatal("case-variant metadata bypassed validation")
			}
		})
	}
}

func TestReportRejectsDuplicateSegmentation(t *testing.T) {
	identity, _ := SegmentIdentity(SegmentFixedV1)
	valid, _ := json.Marshal(identity)
	for _, last := range []string{"null", "{}", string(valid)} {
		path := filepath.Join(t.TempDir(), "duplicate.json")
		raw := []byte(`{"segmentation":` + string(valid) + `,"segmentation":` + last + `}`)
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadRunReport(path); err == nil {
			t.Fatal("duplicate segmentation accepted")
		}
	}
}

func TestMetadataRejectsAmbiguousIdentityMembers(t *testing.T) {
	identity, _ := SegmentIdentity(SegmentFixedV1)
	valid, _ := json.Marshal(identity)
	for _, extra := range []string{`,"policy":null}`, `,"policy":"fixed-v1"}`, `,"Policy":"fixed-v1"}`} {
		raw := append([]byte(`{"segmentation":`), valid[:len(valid)-1]...)
		raw = append(raw, []byte(extra+"}")...)
		if _, err := ReadSegmentationMetadata(raw, true); err == nil {
			t.Fatal("ambiguous identity accepted")
		}
		path := filepath.Join(t.TempDir(), "store.sqlite")
		if err := os.WriteFile(path+".manifest.json", raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkStoreManifest(path, &Dataset{}, identity); err == nil {
			t.Fatal("ambiguous store metadata accepted")
		}
	}
}

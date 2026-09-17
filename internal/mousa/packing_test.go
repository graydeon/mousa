package mousa

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"testing"
)

func exactCandidates(t *testing.T, texts ...string) []VerifiedLexicalCandidate {
	t.Helper()
	out := make([]VerifiedLexicalCandidate, len(texts))
	for index, text := range texts {
		segment := trailSegment(t, byte(index+1))
		segment.ContentSHA256 = SHA256(sha256.Sum256([]byte(text)))
		segment.Selector = NewTextByteRangeSelector(0, uint64(len(text)))
		segment.ID, _ = NewSegmentID(segment.RepresentationID, segment.Selector, segment.ContentSHA256)
		out[index] = VerifiedLexicalCandidate{Segment: segment, Text: text, Disposition: CandidateAccepted, FinalRank: index + 1}
	}
	return out
}

func exactTrail(t *testing.T, candidates []VerifiedLexicalCandidate, budget uint64) SourceTrail {
	t.Helper()
	request := testTrailRequest(t)
	trail, err := NewSourceTrail(request, trailDecision(t, request, PolicyOutcomeAllow), "amber", candidates, budget, PackingExactV1)
	if err != nil {
		t.Fatal(err)
	}
	return trail
}

func TestExactPackingBoundaries(t *testing.T) {
	for _, test := range []struct {
		name      string
		texts     []string
		budget    uint64
		selected  []bool
		omissions []string
	}{
		{"displacement", []string{"amber amber", "amber amber", "amber repair code ZX17"}, 33, []bool{true, false, true}, []string{"", "duplicate", ""}},
		{"oversized never reserves", []string{"oversized", "oversized", "fit"}, 3, []bool{false, false, true}, []string{"budget", "budget", ""}},
		{"exact fit and duplicate after full", []string{"é", "é", "x"}, 2, []bool{true, false, false}, []string{"", "duplicate", "budget"}},
		{"no duplicates", []string{"a", "b", "c"}, 3, []bool{true, true, true}, []string{"", "", ""}},
		{"byte not semantic equality", []string{"é", "é", "É"}, 7, []bool{true, true, true}, []string{"", "", ""}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidates := exactCandidates(t, test.texts...)
			trail := exactTrail(t, candidates, test.budget)
			for index, candidate := range trail.Candidates {
				if candidate.Selected != test.selected[index] || candidate.Omission != test.omissions[index] {
					t.Fatalf("candidate %d: %+v", index, candidate)
				}
			}
			data, err := EncodeSourceTrail(trail)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeSourceTrail(data)
			if err != nil || decoded.ID != trail.ID {
				t.Fatalf("round trip: %v", err)
			}
			request := testTrailRequest(t)
			original, err := NewSourceTrail(request, trailDecision(t, request, PolicyOutcomeAllow), "amber", candidates, test.budget, PackingOriginal)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "no duplicates" && (original.PacketID != trail.PacketID || original.ID == trail.ID) {
				t.Fatal("packet semantics or policy binding changed")
			}
		})
	}
}

func TestExactPackingIgnoresRejectedText(t *testing.T) {
	candidates := exactCandidates(t, "same", "same")
	candidates[0].Disposition = CandidateRejected
	candidates[0].FinalRank = 0
	candidates[0].Reasons = []LifecycleReason{ReasonSourceWithdrawn}
	candidates[1].FinalRank = 1
	trail := exactTrail(t, candidates, 4)
	if trail.Candidates[0].Omission != "" || !trail.Candidates[1].Selected {
		t.Fatal("rejection suppressed authorized evidence")
	}
	candidates[1].Text = "fake"
	request := testTrailRequest(t)
	if _, err := NewSourceTrail(request, trailDecision(t, request, PolicyOutcomeAllow), "amber", candidates, 4, PackingExactV1); err == nil {
		t.Fatal("creation accepted unverified text")
	}
}

func TestExactTrailRejectsInvalidRelationships(t *testing.T) {
	base := exactTrail(t, exactCandidates(t, "same", "same", "tail"), 8)
	for name, mutate := range map[string]func(*SourceTrail){
		"dangling":           func(x *SourceTrail) { x.Candidates[1].DuplicateOf = trailSegment(t, 9).ID.String() },
		"self cycle":         func(x *SourceTrail) { x.Candidates[1].DuplicateOf = x.Candidates[1].SegmentID.String() },
		"forward":            func(x *SourceTrail) { x.Candidates[1].DuplicateOf = x.Candidates[2].SegmentID.String() },
		"selected duplicate": func(x *SourceTrail) { x.Candidates[0].DuplicateOf = x.Candidates[0].SegmentID.String() },
		"budget fits":        func(x *SourceTrail) { x.Candidates[1].Omission = "budget"; x.Candidates[1].DuplicateOf = "" },
		"wrong digest":       func(x *SourceTrail) { x.Candidates[1].ContentSHA256 = x.Candidates[2].ContentSHA256 },
		"wrong size":         func(x *SourceTrail) { x.Candidates[1].TextBytes++ },
		"duplicate segment":  func(x *SourceTrail) { x.Candidates[1].SegmentID = x.Candidates[0].SegmentID },
		"rank order":         func(x *SourceTrail) { x.Candidates[1].FinalRank = 1 },
		"unknown policy":     func(x *SourceTrail) { x.PackingPolicy = "semantic" },
		"v1 policy":          func(x *SourceTrail) { x.Schema = SourceTrailSchema },
		"v2 original":        func(x *SourceTrail) { x.PackingPolicy = PackingOriginal },
		"unknown version":    func(x *SourceTrail) { x.Schema = "mousa.source_trail.v4" },
		"denied candidates":  func(x *SourceTrail) { x.Outcome = string(PolicyOutcomeDeny) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			changed.Candidates = append([]TrailCandidate(nil), base.Candidates...)
			mutate(&changed)
			changed.ID, _ = NewSourceTrailID(changed)
			if changed.Validate() == nil {
				t.Fatal("invalid assertion accepted after ID recomputation")
			}
			data, _ := json.Marshal(changed)
			if _, err := DecodeSourceTrail(data); err == nil {
				t.Fatal("invalid encoded assertion accepted")
			}
		})
	}
}

func TestHistoricalV1Golden(t *testing.T) {
	golden, err := os.ReadFile("testdata/trail-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSourceTrail(golden)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeSourceTrail(decoded)
	if err != nil || !bytes.Equal(encoded, golden) {
		t.Fatal("historical bytes changed")
	}
	request := testTrailRequest(t)
	created, err := NewSourceTrail(request, trailDecision(t, request, PolicyOutcomeAllow), "alpha", trailCandidates(t), 100, PackingOriginal)
	if err != nil {
		t.Fatal(err)
	}
	current, err := EncodeSourceTrail(created)
	if err != nil || !bytes.Equal(current, golden) || created.ID.String() != "ec013c770fa45717522dd32f3ed3a02e7fef67738f59d7b4cd3958b2ca0172de" {
		t.Fatal("default v1 constructor changed historical bytes or identity")
	}
}

func TestHistoricalV1RejectsV2Fields(t *testing.T) {
	golden, err := os.ReadFile("testdata/trail-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"packing_policy", "omission", "duplicate_of"} {
		for _, value := range []string{`""`, `null`, `"exact-v1"`} {
			t.Run(field+"/"+value, func(t *testing.T) {
				var record map[string]any
				if err := json.Unmarshal(golden, &record); err != nil {
					t.Fatal(err)
				}
				target := record
				if field != "packing_policy" {
					target = record["candidates"].([]any)[0].(map[string]any)
				}
				target[field] = json.RawMessage(value)
				data, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeSourceTrail(data); err == nil {
					t.Fatal("v1 accepted a v2-only field")
				}
			})
		}
	}
}

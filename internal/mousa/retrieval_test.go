package mousa

import (
	"bytes"
	"crypto/sha256"
	"math"
	"reflect"
	"testing"
)

func TestRankAndVerifyLexicalCandidatesLifecycle(t *testing.T) {
	segment := func(representationByte byte, text string) Segment {
		var representationID RepresentationID
		representationID[0] = representationByte
		digest := SHA256(sha256.Sum256([]byte(text)))
		selector := NewTextByteRangeSelector(0, uint64(len(text)))
		id, err := NewSegmentID(representationID, selector, digest)
		if err != nil {
			t.Fatalf("new segment ID: %v", err)
		}
		return Segment{
			Schema:           SegmentSchema,
			ID:               id,
			RepresentationID: representationID,
			Selector:         selector,
			ContentSHA256:    digest,
		}
	}
	path := func(segment Segment, sourceByte byte) EvidencePath {
		var artifactID ArtifactID
		artifactID[0] = sourceByte
		var observationID ObservationID
		observationID[0] = sourceByte
		var sourceID SourceID
		sourceID[0] = sourceByte
		return EvidencePath{
			RepresentationIDs: []RepresentationID{segment.RepresentationID},
			ArtifactID:        artifactID,
			ObservationID:     observationID,
			SourceID:          sourceID,
		}
	}
	state := func(sourceByte byte, present bool, collection CollectionState) SourceLifecycleEvidence {
		var sourceID SourceID
		sourceID[0] = sourceByte
		evidence := SourceLifecycleEvidence{SourceID: sourceID, StatePresent: present, CollectionState: collection}
		if collection == CollectionWithdrawn {
			var withdrawalID WithdrawalID
			withdrawalID[0] = sourceByte
			evidence.CurrentWithdrawalID = &withdrawalID
		}
		return evidence
	}

	rejected := segment(3, "rejected")
	acceptedTieLow, acceptedTieLowText := segment(1, "accepted-low"), "accepted-low"
	acceptedTieHigh, acceptedTieHighText := segment(2, "accepted-high"), "accepted-high"
	if bytes.Compare(acceptedTieLow.ID[:], acceptedTieHigh.ID[:]) > 0 {
		acceptedTieLow, acceptedTieHigh = acceptedTieHigh, acceptedTieLow
		acceptedTieLowText, acceptedTieHighText = acceptedTieHighText, acceptedTieLowText
	}
	withdrawnPath := path(rejected, 3)
	missingPath := path(rejected, 4)

	got, err := RankAndVerifyLexicalCandidates([]LexicalCandidateEvidence{
		{Segment: acceptedTieHigh, Text: acceptedTieHighText, BM25: -1, Paths: []EvidencePath{path(acceptedTieHigh, 2)}, Sources: []SourceLifecycleEvidence{state(2, true, CollectionActive)}},
		{Segment: rejected, Text: "rejected", BM25: -2, Paths: []EvidencePath{missingPath, withdrawnPath, withdrawnPath}, Sources: []SourceLifecycleEvidence{state(4, false, ""), state(3, true, CollectionWithdrawn)}},
		{Segment: acceptedTieLow, Text: acceptedTieLowText, BM25: -1, Paths: []EvidencePath{path(acceptedTieLow, 1)}, Sources: []SourceLifecycleEvidence{state(1, true, CollectionActive)}},
	})
	if err != nil {
		t.Fatalf("rank and verify: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("candidate count = %d, want 3", len(got))
	}
	if got[0].Segment.ID != rejected.ID || got[0].LexicalRank != 1 || got[0].FinalRank != 0 || got[0].Disposition != CandidateRejected || got[0].Text != "" {
		t.Fatalf("rejected candidate = %#v", got[0])
	}
	if want := []LifecycleReason{ReasonSourceWithdrawn, ReasonSourceStateMissing}; !reflect.DeepEqual(got[0].Reasons, want) {
		t.Fatalf("rejection reasons = %#v, want %#v", got[0].Reasons, want)
	}
	if want := []EvidencePath{withdrawnPath, missingPath}; !reflect.DeepEqual(got[0].Paths, want) {
		t.Fatalf("rejected paths = %#v, want %#v", got[0].Paths, want)
	}
	if got[1].Segment.ID != acceptedTieLow.ID || got[1].LexicalRank != 2 || got[1].FinalRank != 1 || got[1].Disposition != CandidateAccepted || got[1].Text != acceptedTieLowText {
		t.Fatalf("first accepted candidate = %#v", got[1])
	}
	if got[2].Segment.ID != acceptedTieHigh.ID || got[2].LexicalRank != 3 || got[2].FinalRank != 2 || got[2].Disposition != CandidateAccepted || got[2].Text != acceptedTieHighText {
		t.Fatalf("second accepted candidate = %#v", got[2])
	}

	empty, err := RankAndVerifyLexicalCandidates(nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty result = %#v, err = %v", empty, err)
	}
}

func TestRankAndVerifyLexicalCandidatesRejectsCyclicAncestry(t *testing.T) {
	text := "cycle"
	var representationID RepresentationID
	representationID[0] = 1
	digest := SHA256(sha256.Sum256([]byte(text)))
	selector := NewTextByteRangeSelector(0, uint64(len(text)))
	segmentID, err := NewSegmentID(representationID, selector, digest)
	if err != nil {
		t.Fatalf("new segment ID: %v", err)
	}
	segment := Segment{Schema: SegmentSchema, ID: segmentID, RepresentationID: representationID, Selector: selector, ContentSHA256: digest}
	var artifactID ArtifactID
	artifactID[0] = 1
	var observationID ObservationID
	observationID[0] = 1
	var sourceID SourceID
	sourceID[0] = 1

	_, err = RankAndVerifyLexicalCandidates([]LexicalCandidateEvidence{{
		Segment: segment,
		Text:    text,
		BM25:    -1,
		Paths: []EvidencePath{{
			RepresentationIDs: []RepresentationID{representationID, representationID},
			ArtifactID:        artifactID,
			ObservationID:     observationID,
			SourceID:          sourceID,
		}},
		Sources: []SourceLifecycleEvidence{{SourceID: sourceID, StatePresent: true, CollectionState: CollectionActive}},
	}})
	validation, ok := err.(*ValidationError)
	if !ok || validation.Field != "candidates[0].paths[0].representation_ids[1]" || validation.Code != ValidationCodeInvalidID {
		t.Fatalf("error = %#v, want cyclic ancestry validation", err)
	}
}

func TestRankAndVerifyLexicalCandidatesRejectsInvalidEvidence(t *testing.T) {
	for _, test := range []struct {
		name  string
		build func() []LexicalCandidateEvidence
		field string
		code  ValidationCode
	}{
		{
			name: "zero segment",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "zero segment")
				candidate.Segment = Segment{}
				return []LexicalCandidateEvidence{candidate}
			},
			field: "candidates[0].segment", code: ValidationCodeInvalidSchema,
		},
		{
			name: "text content mismatch",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "canonical text")
				candidate.Text = "different text"
				return []LexicalCandidateEvidence{candidate}
			},
			field: "candidates[0].text", code: ValidationCodeInvalidDigest,
		},
		{
			name: "non-finite score",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "nan")
				candidate.BM25 = math.NaN()
				return []LexicalCandidateEvidence{candidate}
			},
			field: "candidates[0].bm25", code: ValidationCodeInvalidValue,
		},
		{
			name: "duplicate segment",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "duplicate")
				return []LexicalCandidateEvidence{candidate, candidate}
			},
			field: "candidates[1].segment.id", code: ValidationCodeInvalidID,
		},
		{
			name: "missing paths",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "paths")
				candidate.Paths = nil
				return []LexicalCandidateEvidence{candidate}
			},
			field: "candidates[0].paths", code: ValidationCodeInvalidRange,
		},
		{
			name: "duplicate source fact",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "sources")
				candidate.Sources = append(candidate.Sources, candidate.Sources[0])
				return []LexicalCandidateEvidence{candidate}
			},
			field: "candidates[0].sources[1].source_id", code: ValidationCodeInvalidID,
		},
		{
			name: "missing referenced source fact",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "missing source")
				secondPath := candidate.Paths[0]
				secondPath.ArtifactID[0] = 2
				secondPath.ObservationID[0] = 2
				secondPath.SourceID[0] = 2
				candidate.Paths = append(candidate.Paths, secondPath)
				return []LexicalCandidateEvidence{candidate}
			},
			field: "candidates[0].sources", code: ValidationCodeInvalidID,
		},
		{
			name: "unrelated source fact",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "unrelated source")
				unrelated := candidate.Sources[0]
				unrelated.SourceID[0] = 2
				candidate.Sources = append(candidate.Sources, unrelated)
				return []LexicalCandidateEvidence{candidate}
			},
			field: "candidates[0].sources[1].source_id", code: ValidationCodeInvalidID,
		},
		{
			name: "impossible lifecycle enum",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "invalid lifecycle")
				candidate.Sources[0].CollectionState = CollectionState("paused")
				return []LexicalCandidateEvidence{candidate}
			},
			field: "candidates[0].sources[0].collection_state", code: ValidationCodeInvalidEnum,
		},
		{
			name: "absent state carries fields",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "absent")
				candidate.Sources[0].StatePresent = false
				return []LexicalCandidateEvidence{candidate}
			},
			field: "candidates[0].sources[0]", code: ValidationCodeInvalidValue,
		},
		{
			name: "candidate bound",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "candidate bound")
				candidates := make([]LexicalCandidateEvidence, maxLexicalCandidates+1)
				for index := range candidates {
					candidates[index] = candidate
				}
				return candidates
			},
			field: "candidates", code: ValidationCodeInvalidRange,
		},
		{
			name: "path bound",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "path bound")
				candidate.Paths = make([]EvidencePath, maxEvidencePaths+1)
				return []LexicalCandidateEvidence{candidate}
			},
			field: "candidates[0].paths", code: ValidationCodeInvalidRange,
		},
		{
			name: "source lifecycle bound",
			build: func() []LexicalCandidateEvidence {
				candidate := validLexicalCandidateForTest(t, 1, "source bound")
				candidate.Sources = make([]SourceLifecycleEvidence, maxSourceLifecycleRecords+1)
				return []LexicalCandidateEvidence{candidate}
			},
			field: "candidates[0].sources", code: ValidationCodeInvalidRange,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := RankAndVerifyLexicalCandidates(test.build())
			if got != nil {
				t.Fatalf("result = %#v, want nil", got)
			}
			validation, ok := err.(*ValidationError)
			if !ok || validation.Field != test.field || validation.Code != test.code {
				t.Fatalf("error = %#v, want field %q code %q", err, test.field, test.code)
			}
		})
	}
}

func TestRankAndVerifyLexicalCandidatesDeterministicAndNonMutating(t *testing.T) {
	candidate := validLexicalCandidateForTest(t, 2, "deterministic")
	secondPath := candidate.Paths[0]
	secondPath.ArtifactID[0] = 1
	secondPath.ObservationID[0] = 1
	candidate.Paths = []EvidencePath{candidate.Paths[0], secondPath, candidate.Paths[0]}
	original := cloneEvidencePaths(candidate.Paths)

	first, err := RankAndVerifyLexicalCandidates([]LexicalCandidateEvidence{candidate})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RankAndVerifyLexicalCandidates([]LexicalCandidateEvidence{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(candidate.Paths, original) {
		t.Fatalf("results are not deterministic or input was mutated: first=%#v second=%#v input=%#v", first, second, candidate.Paths)
	}
	if len(first) != 1 || len(first[0].Paths) != 2 || compareEvidencePaths(first[0].Paths[0], first[0].Paths[1]) >= 0 {
		t.Fatalf("canonical paths = %#v", first)
	}
}

func TestRankAndVerifyLexicalCandidatesRequiresEverySourceActive(t *testing.T) {
	candidate := validLexicalCandidateForTest(t, 1, "mixed lifecycle")
	secondPath := candidate.Paths[0]
	secondPath.SourceID[0] = 2
	secondPath.ObservationID[0] = 2
	secondPath.ArtifactID[0] = 2
	var withdrawalID WithdrawalID
	withdrawalID[0] = 2
	candidate.Paths = append(candidate.Paths, secondPath)
	candidate.Sources = append(candidate.Sources, SourceLifecycleEvidence{
		SourceID: secondPath.SourceID, StatePresent: true, CollectionState: CollectionWithdrawn, CurrentWithdrawalID: &withdrawalID,
	})

	got, err := RankAndVerifyLexicalCandidates([]LexicalCandidateEvidence{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Disposition != CandidateRejected || got[0].Text != "" || !reflect.DeepEqual(got[0].Reasons, []LifecycleReason{ReasonSourceWithdrawn}) {
		t.Fatalf("mixed lifecycle candidate = %#v", got)
	}
}

func validLexicalCandidateForTest(t *testing.T, key byte, text string) LexicalCandidateEvidence {
	t.Helper()
	var representationID RepresentationID
	representationID[0] = key
	digest := SHA256(sha256.Sum256([]byte(text)))
	selector := NewTextByteRangeSelector(0, uint64(len(text)))
	segmentID, err := NewSegmentID(representationID, selector, digest)
	if err != nil {
		t.Fatal(err)
	}
	var artifactID ArtifactID
	artifactID[0] = key
	var observationID ObservationID
	observationID[0] = key
	var sourceID SourceID
	sourceID[0] = key
	return LexicalCandidateEvidence{
		Segment: Segment{Schema: SegmentSchema, ID: segmentID, RepresentationID: representationID, Selector: selector, ContentSHA256: digest},
		Text:    text,
		BM25:    -1,
		Paths:   []EvidencePath{{RepresentationIDs: []RepresentationID{representationID}, ArtifactID: artifactID, ObservationID: observationID, SourceID: sourceID}},
		Sources: []SourceLifecycleEvidence{{SourceID: sourceID, StatePresent: true, CollectionState: CollectionActive}},
	}
}

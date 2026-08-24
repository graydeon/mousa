package mousa

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"unicode/utf8"
)

const (
	maxLexicalCandidates      = 100
	maxEvidencePaths          = 4096
	maxPathRepresentations    = 4096
	maxSourceLifecycleRecords = 4096
)

type CandidateDisposition string

type LifecycleReason string

const (
	CandidateAccepted CandidateDisposition = "accepted"
	CandidateRejected CandidateDisposition = "rejected"

	ReasonSourceWithdrawn    LifecycleReason = "source_withdrawn"
	ReasonSourceStateMissing LifecycleReason = "source_state_missing"
)

// EvidencePath is one complete canonical ancestry chain for a Segment.
type EvidencePath struct {
	RepresentationIDs []RepresentationID
	ArtifactID        ArtifactID
	ObservationID     ObservationID
	SourceID          SourceID
}

// SourceLifecycleEvidence is the exact current ingest-state fact for one source.
type SourceLifecycleEvidence struct {
	SourceID            SourceID
	StatePresent        bool
	CollectionState     CollectionState
	LastObservationID   *ObservationID
	CurrentWithdrawalID *WithdrawalID
	LastCapturedAtUsec  *int64
}

// LexicalCandidateEvidence is a raw lexical candidate with verified ancestry and lifecycle facts.
type LexicalCandidateEvidence struct {
	Segment Segment
	Text    string
	BM25    float64
	Paths   []EvidencePath
	Sources []SourceLifecycleEvidence
}

// VerifiedLexicalCandidate is one deterministically ranked lifecycle decision.
type VerifiedLexicalCandidate struct {
	Segment     Segment
	Text        string
	BM25        float64
	LexicalRank int
	FinalRank   int
	Paths       []EvidencePath
	Sources     []SourceLifecycleEvidence
	Disposition CandidateDisposition
	Reasons     []LifecycleReason
}

// RankAndVerifyLexicalCandidates validates, ranks, and applies restrictive source lifecycle policy.
func RankAndVerifyLexicalCandidates(candidates []LexicalCandidateEvidence) ([]VerifiedLexicalCandidate, error) {
	if len(candidates) > maxLexicalCandidates {
		return nil, retrievalValidationError("candidates", ValidationCodeInvalidRange, "must contain at most 100 candidates")
	}
	verified := make([]VerifiedLexicalCandidate, len(candidates))
	seenSegments := make(map[SegmentID]struct{}, len(candidates))
	for index, candidate := range candidates {
		field := fmt.Sprintf("candidates[%d]", index)
		if _, duplicate := seenSegments[candidate.Segment.ID]; duplicate {
			return nil, retrievalValidationError(field+".segment.id", ValidationCodeInvalidID, "must be unique")
		}
		seenSegments[candidate.Segment.ID] = struct{}{}
		if err := validateLexicalCandidate(field, candidate); err != nil {
			return nil, err
		}
		paths := cloneEvidencePaths(candidate.Paths)
		sort.Slice(paths, func(i, j int) bool { return compareEvidencePaths(paths[i], paths[j]) < 0 })
		paths = deduplicateEvidencePaths(paths)
		sources := cloneSourceLifecycleEvidence(candidate.Sources)
		sort.Slice(sources, func(i, j int) bool {
			return bytes.Compare(sources[i].SourceID[:], sources[j].SourceID[:]) < 0
		})
		disposition, reasons := lifecycleDisposition(paths, sources)
		text := candidate.Text
		if disposition == CandidateRejected {
			text = ""
		}
		verified[index] = VerifiedLexicalCandidate{
			Segment: candidate.Segment, Text: text, BM25: candidate.BM25,
			Paths: paths, Sources: sources, Disposition: disposition, Reasons: reasons,
		}
	}
	sort.Slice(verified, func(i, j int) bool {
		if verified[i].BM25 != verified[j].BM25 {
			return verified[i].BM25 < verified[j].BM25
		}
		return bytes.Compare(verified[i].Segment.ID[:], verified[j].Segment.ID[:]) < 0
	})
	finalRank := 0
	for index := range verified {
		verified[index].LexicalRank = index + 1
		if verified[index].Disposition == CandidateAccepted {
			finalRank++
			verified[index].FinalRank = finalRank
		}
	}
	return verified, nil
}

func validateLexicalCandidate(field string, candidate LexicalCandidateEvidence) error {
	if err := candidate.Segment.Validate(); err != nil {
		return retrievalValidationError(field+".segment", validationCode(err), err.Error())
	}
	if !utf8.ValidString(candidate.Text) || len(candidate.Text) > MaxUTF8TextSegmentBytes {
		return retrievalValidationError(field+".text", ValidationCodeInvalidValue, "must be valid UTF-8 within the segment byte limit")
	}
	selector, _ := candidate.Segment.Selector.TextByteRange()
	if selector.End-selector.Start != uint64(len(candidate.Text)) || SHA256(sha256.Sum256([]byte(candidate.Text))) != candidate.Segment.ContentSHA256 {
		return retrievalValidationError(field+".text", ValidationCodeInvalidDigest, "must match the canonical Segment")
	}
	if math.IsNaN(candidate.BM25) || math.IsInf(candidate.BM25, 0) {
		return retrievalValidationError(field+".bm25", ValidationCodeInvalidValue, "must be finite")
	}
	if len(candidate.Paths) == 0 || len(candidate.Paths) > maxEvidencePaths {
		return retrievalValidationError(field+".paths", ValidationCodeInvalidRange, "must contain between 1 and 4096 paths")
	}
	artifactObservations := make(map[ArtifactID]ObservationID)
	observationSources := make(map[ObservationID]SourceID)
	pathSources := make(map[SourceID]struct{})
	for index, path := range candidate.Paths {
		pathField := fmt.Sprintf("%s.paths[%d]", field, index)
		if len(path.RepresentationIDs) == 0 || len(path.RepresentationIDs) > maxPathRepresentations || path.RepresentationIDs[0] != candidate.Segment.RepresentationID {
			return retrievalValidationError(pathField+".representation_ids", ValidationCodeInvalidValue, "must be a bounded chain beginning at the Segment representation")
		}
		seenRepresentations := make(map[RepresentationID]struct{}, len(path.RepresentationIDs))
		for representationIndex, id := range path.RepresentationIDs {
			if id == (RepresentationID{}) {
				return retrievalValidationError(fmt.Sprintf("%s.representation_ids[%d]", pathField, representationIndex), ValidationCodeInvalidID, "must not be zero")
			}
			if _, duplicate := seenRepresentations[id]; duplicate {
				return retrievalValidationError(fmt.Sprintf("%s.representation_ids[%d]", pathField, representationIndex), ValidationCodeInvalidID, "must not repeat")
			}
			seenRepresentations[id] = struct{}{}
		}
		if path.ArtifactID == (ArtifactID{}) || path.ObservationID == (ObservationID{}) || path.SourceID == (SourceID{}) {
			return retrievalValidationError(pathField, ValidationCodeInvalidID, "artifact, observation, and source IDs must not be zero")
		}
		if observationID, exists := artifactObservations[path.ArtifactID]; exists && observationID != path.ObservationID {
			return retrievalValidationError(pathField+".artifact_id", ValidationCodeInvalidID, "conflicts with another ancestry fact")
		}
		artifactObservations[path.ArtifactID] = path.ObservationID
		if sourceID, exists := observationSources[path.ObservationID]; exists && sourceID != path.SourceID {
			return retrievalValidationError(pathField+".observation_id", ValidationCodeInvalidID, "conflicts with another ancestry fact")
		}
		observationSources[path.ObservationID] = path.SourceID
		pathSources[path.SourceID] = struct{}{}
	}
	if len(candidate.Sources) == 0 || len(candidate.Sources) > maxSourceLifecycleRecords {
		return retrievalValidationError(field+".sources", ValidationCodeInvalidRange, "must contain between 1 and 4096 source facts")
	}
	seenSources := make(map[SourceID]struct{}, len(candidate.Sources))
	for index, source := range candidate.Sources {
		sourceField := fmt.Sprintf("%s.sources[%d]", field, index)
		if source.SourceID == (SourceID{}) {
			return retrievalValidationError(sourceField+".source_id", ValidationCodeInvalidID, "must not be zero")
		}
		if _, duplicate := seenSources[source.SourceID]; duplicate {
			return retrievalValidationError(sourceField+".source_id", ValidationCodeInvalidID, "must be unique")
		}
		seenSources[source.SourceID] = struct{}{}
		if _, referenced := pathSources[source.SourceID]; !referenced {
			return retrievalValidationError(sourceField+".source_id", ValidationCodeInvalidID, "has no ancestry path")
		}
		if err := validateSourceLifecycle(sourceField, source); err != nil {
			return err
		}
	}
	for sourceID := range pathSources {
		if _, exists := seenSources[sourceID]; !exists {
			return retrievalValidationError(field+".sources", ValidationCodeInvalidID, "must include an exact fact for every ancestry source")
		}
	}
	return nil
}

func validateSourceLifecycle(field string, source SourceLifecycleEvidence) error {
	if !source.StatePresent {
		if source.CollectionState != "" || source.LastObservationID != nil || source.CurrentWithdrawalID != nil || source.LastCapturedAtUsec != nil {
			return retrievalValidationError(field, ValidationCodeInvalidValue, "absent state must not carry state fields")
		}
		return nil
	}
	if source.CollectionState != CollectionActive && source.CollectionState != CollectionWithdrawn {
		return retrievalValidationError(field+".collection_state", ValidationCodeInvalidEnum, "must be active or withdrawn")
	}
	if source.LastObservationID != nil && *source.LastObservationID == (ObservationID{}) {
		return retrievalValidationError(field+".last_observation_id", ValidationCodeInvalidID, "must not be zero")
	}
	if source.CurrentWithdrawalID != nil && *source.CurrentWithdrawalID == (WithdrawalID{}) {
		return retrievalValidationError(field+".current_withdrawal_id", ValidationCodeInvalidID, "must not be zero")
	}
	if (source.LastObservationID == nil) != (source.LastCapturedAtUsec == nil) || source.LastCapturedAtUsec != nil && *source.LastCapturedAtUsec <= 0 {
		return retrievalValidationError(field+".last_captured_at_usec", ValidationCodeInvalidValue, "must be positive and present exactly with last_observation_id")
	}
	if (source.CollectionState == CollectionActive) != (source.CurrentWithdrawalID == nil) {
		return retrievalValidationError(field+".current_withdrawal_id", ValidationCodeInvalidValue, "must agree with collection_state")
	}
	return nil
}

func lifecycleDisposition(paths []EvidencePath, sources []SourceLifecycleEvidence) (CandidateDisposition, []LifecycleReason) {
	byID := make(map[SourceID]SourceLifecycleEvidence, len(sources))
	for _, source := range sources {
		byID[source.SourceID] = source
	}
	withdrawn := false
	missing := false
	for _, path := range paths {
		source := byID[path.SourceID]
		if source.StatePresent && source.CollectionState == CollectionWithdrawn {
			withdrawn = true
		} else if !source.StatePresent {
			missing = true
		}
	}
	if !withdrawn && !missing {
		return CandidateAccepted, []LifecycleReason{}
	}
	reasons := make([]LifecycleReason, 0, 2)
	if withdrawn {
		reasons = append(reasons, ReasonSourceWithdrawn)
	}
	if missing {
		reasons = append(reasons, ReasonSourceStateMissing)
	}
	return CandidateRejected, reasons
}

func compareEvidencePaths(left, right EvidencePath) int {
	for index := 0; index < len(left.RepresentationIDs) && index < len(right.RepresentationIDs); index++ {
		if order := bytes.Compare(left.RepresentationIDs[index][:], right.RepresentationIDs[index][:]); order != 0 {
			return order
		}
	}
	if len(left.RepresentationIDs) < len(right.RepresentationIDs) {
		return -1
	}
	if len(left.RepresentationIDs) > len(right.RepresentationIDs) {
		return 1
	}
	if order := bytes.Compare(left.ArtifactID[:], right.ArtifactID[:]); order != 0 {
		return order
	}
	if order := bytes.Compare(left.ObservationID[:], right.ObservationID[:]); order != 0 {
		return order
	}
	return bytes.Compare(left.SourceID[:], right.SourceID[:])
}

func deduplicateEvidencePaths(paths []EvidencePath) []EvidencePath {
	result := make([]EvidencePath, 0, len(paths))
	for _, path := range paths {
		if len(result) == 0 || compareEvidencePaths(result[len(result)-1], path) != 0 {
			result = append(result, path)
		}
	}
	return result
}

func cloneEvidencePaths(paths []EvidencePath) []EvidencePath {
	cloned := make([]EvidencePath, len(paths))
	for index, path := range paths {
		cloned[index] = path
		cloned[index].RepresentationIDs = append([]RepresentationID(nil), path.RepresentationIDs...)
	}
	return cloned
}

func cloneSourceLifecycleEvidence(sources []SourceLifecycleEvidence) []SourceLifecycleEvidence {
	cloned := make([]SourceLifecycleEvidence, len(sources))
	for index, source := range sources {
		cloned[index] = source
		if source.LastObservationID != nil {
			value := *source.LastObservationID
			cloned[index].LastObservationID = &value
		}
		if source.CurrentWithdrawalID != nil {
			value := *source.CurrentWithdrawalID
			cloned[index].CurrentWithdrawalID = &value
		}
		if source.LastCapturedAtUsec != nil {
			value := *source.LastCapturedAtUsec
			cloned[index].LastCapturedAtUsec = &value
		}
	}
	return cloned
}

func retrievalValidationError(field string, code ValidationCode, message string) error {
	return newValidationError(field, code, message, nil)
}

func validationCode(err error) ValidationCode {
	if validation, ok := err.(*ValidationError); ok {
		return validation.Code
	}
	return ValidationCodeInvalidValue
}

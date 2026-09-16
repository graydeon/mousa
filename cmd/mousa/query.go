package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// queryItems uses the canonical evaluation, retrieval, packing, and tracing
// transaction. Only selected, accepted text is released from that snapshot.
func queryItems(ctx context.Context, store *sqlite.Store, source mousa.Source, query, policy string, budgetBytes uint64, packingPolicy string) (*evidenceResult, error) {
	needsRecovery, err := store.LocalSourceNeedsRecovery(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	if needsRecovery {
		return nil, fmt.Errorf("source requires a complete directory sync after migration before querying")
	}
	terms, err := mousa.PrepareLexicalTerms(query, policy == "dedup")
	if err != nil {
		return nil, err
	}
	expression := strings.Join(terms, " OR ")
	request, err := newRetrievalRequest(source.ID)
	if err != nil {
		return nil, err
	}
	traced, err := store.EvaluateAndTraceLexical(ctx, request, expression, queryCandidateLimit, budgetBytes, packingPolicy)
	if err != nil {
		return nil, err
	}
	result := &evidenceResult{
		RequestID: request.ID.String(), DecisionID: traced.Decision.ID.String(),
		DecisionOutcome: string(traced.Decision.Outcome), DecisionReasons: traced.Decision.ReasonCodes,
		EvaluatedAtUsec: traced.Decision.EvaluatedAtUsec,
		TrailID:         traced.Trail.ID.String(), PacketID: traced.Trail.PacketID,
		Expression: expression, QueryPolicy: policy,
		PackingPolicy: traced.Trail.PackingPolicy,
		UsedBytes:     traced.Trail.UsedBytes, BudgetBytes: traced.Trail.BudgetBytes,
		MatchedCandidates: len(traced.Candidates), CandidateLimit: queryCandidateLimit,
		Evidence: []evidenceHit{},
	}
	if packingPolicy == mousa.PackingExactV1 {
		result.DuplicateOmitted = new(int)
	}
	type representationLocation struct {
		record mousa.Representation
		item   string
		policy string
	}
	locations := make(map[mousa.RepresentationID]representationLocation)
	for index, candidate := range traced.Candidates {
		if candidate.Disposition != mousa.CandidateAccepted {
			result.LifecycleExcluded++
			continue
		}
		if !traced.Trail.Candidates[index].Selected {
			if traced.Trail.Candidates[index].Omission == "duplicate" {
				*result.DuplicateOmitted++
			} else {
				result.BudgetOmitted++
			}
			continue
		}
		segment := candidate.Segment
		location, exists := locations[segment.RepresentationID]
		if !exists {
			representation, err := store.GetRepresentation(ctx, segment.RepresentationID)
			if err != nil {
				return nil, err
			}
			if representation.ProcessorID != mousa.UTF8TextProcessorID {
				return nil, fmt.Errorf("selected evidence is not normalized UTF-8 text")
			}
			policy, err := mousa.TextSegmentationPolicy(representation)
			if err != nil {
				return nil, err
			}
			item, err := itemPathForSegment(ctx, store, source, segment.ID)
			if err != nil {
				return nil, err
			}
			location = representationLocation{record: representation, item: item, policy: policy}
			locations[segment.RepresentationID] = location
		}
		if err := segment.ValidateAgainst(location.record); err != nil {
			return nil, err
		}
		byteRange, ok := segment.Selector.TextByteRange()
		if !ok || byteRange.End-byteRange.Start != uint64(len(candidate.Text)) {
			return nil, fmt.Errorf("selected evidence range disagrees with released text")
		}
		result.Evidence = append(result.Evidence, evidenceHit{
			Item: location.item, SegmentID: segment.ID.String(),
			RepresentationID:     segment.RepresentationID.String(),
			RepresentationSHA256: location.record.ContentSHA256.String(),
			ByteStart:            byteRange.Start, ByteEnd: byteRange.End, SegmentPolicy: location.policy,
			ContentSHA256: candidate.Segment.ContentSHA256.String(),
			Rank:          candidate.FinalRank, Score: candidate.BM25,
			ByteLength: len(candidate.Text), Text: candidate.Text,
		})
	}
	switch {
	case traced.Decision.Outcome != mousa.PolicyOutcomeAllow:
		result.Outcome = "policy_excluded"
		if !traced.Decision.StatePresent || traced.Decision.CollectionState == nil || *traced.Decision.CollectionState != mousa.CollectionActive {
			result.Outcome = "lifecycle_excluded"
		}
	case len(result.Evidence) > 0:
		result.Outcome = "evidence"
	case result.BudgetOmitted > 0:
		result.Outcome = "budget_omitted"
	case result.LifecycleExcluded > 0:
		result.Outcome = "lifecycle_excluded"
	default:
		result.Outcome = "no_matches"
	}
	return result, nil
}

const queryCandidateLimit = 100

func newRetrievalRequest(sourceID mousa.SourceID) (mousa.PolicyEvaluationRequest, error) {
	request := mousa.PolicyEvaluationRequest{
		Schema: mousa.PolicyEvaluationRequestSchema, Action: mousa.SourceRetrievalAction,
		CallerNamespace: localNamespace + ".caller", ExternalCallerID: "cli",
		ExternalRequestID: "cli-" + rand.Text(),
		PurposeNamespace:  localNamespace + ".purpose", ExternalPurposeID: "retrieval",
		SourceID: sourceID, RequestedAtUsec: time.Now().UnixMicro(),
	}
	id, err := mousa.NewPolicyEvaluationRequestID(request)
	if err != nil {
		return mousa.PolicyEvaluationRequest{}, err
	}
	request.ID = id
	return request, nil
}

// itemPathForSegment derives an item identifier from verified immutable
// ancestry. It also works for retired revisions; it does not infer activation.
func itemPathForSegment(ctx context.Context, store *sqlite.Store, source mousa.Source, segmentID mousa.SegmentID) (string, error) {
	segment, err := store.GetSegment(ctx, segmentID)
	if err != nil {
		return "", err
	}
	artifact, err := store.RepresentationSourceArtifact(ctx, segment.RepresentationID)
	if err != nil {
		return "", err
	}
	observation, err := store.GetObservation(ctx, artifact.ObservationID)
	if err != nil {
		return "", err
	}
	if observation.SourceID != source.ID {
		return "", fmt.Errorf("segment does not belong to the queried source")
	}
	external, ok := strings.CutPrefix(observation.ExternalObservationID, itemPrefix)
	if !ok {
		return "", fmt.Errorf("observation is not a local item")
	}
	suffix := fmt.Sprintf("@%x", artifact.ContentSHA256)
	relative := strings.TrimSuffix(external, suffix)
	if relative == external || relative == "" {
		return "", fmt.Errorf("item revision identity disagrees with artifact digest")
	}
	return relative, nil
}

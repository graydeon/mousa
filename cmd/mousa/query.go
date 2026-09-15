package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// queryItems runs one authorized, source-scoped lexical query and releases
// only accepted candidates' text under the byte budget. The store's enforced
// path verifies ancestry and lifecycle inside one transaction and evaluates
// the exact stored decision for the request; the response carries the
// decision ID so provenance is explicit, and rejected candidates (which carry
// text and dispositions) are never serialized.
func queryItems(ctx context.Context, store *sqlite.Store, source mousa.Source, query string, budgetBytes uint64) (*evidenceResult, error) {
	expression, err := buildQueryExpression(query)
	if err != nil {
		return nil, err
	}
	request := mousa.PolicyEvaluationRequest{
		Schema:           mousa.PolicyEvaluationRequestSchema,
		Action:           mousa.SourceRetrievalAction,
		CallerNamespace:  localNamespace + ".caller",
		ExternalCallerID: "cli",
		// Every query is a fresh request: the identity includes nanosecond
		// time and process id, so each invocation evaluates its own immutable
		// decision against the current lifecycle/policy snapshot. Reusing one
		// identity across invocations would freeze authorization at the first
		// query's snapshot.
		ExternalRequestID: fmt.Sprintf("query-%s-%x-%d-%d", source.ExternalSourceID, requestDigest(query), time.Now().UnixNano(), os.Getpid()),
		PurposeNamespace:  localNamespace + ".purpose",
		ExternalPurposeID: "retrieval",
		SourceID:          source.ID,
		RequestedAtUsec:   time.Now().UnixMicro(),
	}
	requestID, err := mousa.NewPolicyEvaluationRequestID(request)
	if err != nil {
		return nil, err
	}
	request.ID = requestID
	// Evaluate and store the immutable decision for this exact request; the
	// decision binds the lifecycle snapshot at evaluation time.
	if _, err := store.EvaluateSourceRetrieval(ctx, request); err != nil {
		return nil, fmt.Errorf("evaluate retrieval policy: %w", err)
	}
	enforced, err := store.SearchEnforcedLexical(ctx, request, expression, queryCandidateLimit)
	if err != nil {
		return nil, err
	}
	result := &evidenceResult{DecisionID: fmt.Sprintf("%x", enforced.Decision.ID[:8])}
	var used uint64
	for _, candidate := range enforced.Candidates {
		if candidate.Disposition != mousa.CandidateAccepted {
			continue
		}
		if used+uint64(len(candidate.Text)) > budgetBytes {
			continue
		}
		relative, err := itemPathForSegment(ctx, store, source, candidate.Segment.ID)
		if err != nil {
			return nil, err
		}
		used += uint64(len(candidate.Text))
		result.UsedBytes = used
		result.TotalMatches++
		result.Evidence = append(result.Evidence, evidenceHit{
			Item:       relative,
			SegmentID:  fmt.Sprintf("%x", candidate.Segment.ID),
			Rank:       candidate.FinalRank,
			Score:      candidate.BM25,
			ByteLength: len(candidate.Text),
			Text:       candidate.Text,
		})
	}
	return result, nil
}

const queryCandidateLimit = 100

func requestDigest(query string) [32]byte {
	return sha256.Sum256([]byte(query))
}

// buildQueryExpression tokenizes to quoted OR terms under the store's bound.
// Repeated terms are folded: repetition multiplies a term's BM25 contribution
// and is not the documented local-slice protocol.
func buildQueryExpression(query string) (string, error) {
	terms := make([]string, 0)
	seen := map[string]struct{}{}
	for _, term := range queryTerms(query) {
		if _, duplicate := seen[term]; duplicate {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, quoteTerm(term))
	}
	if len(terms) == 0 {
		return "", fmt.Errorf("query produced no searchable terms")
	}
	for len(strings.Join(terms, " OR ")) > maxExpressionBytes && len(terms) > 1 {
		terms = terms[:len(terms)-1]
	}
	return strings.Join(terms, " OR "), nil
}

const maxExpressionBytes = 4096

func quoteTerm(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}

func queryTerms(query string) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		isLetter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		isLetter = isLetter || r >= 0x80
		return !isLetter
	})
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		term := strings.ToLower(strings.TrimSpace(field))
		if term == "" {
			continue
		}
		terms = append(terms, term)
	}
	return terms
}

// itemPathForSegment resolves which item a retrieved segment belongs to via
// the segment→representation→artifact→observation chain, so provenance is
// derived from verified records rather than a parallel mapping. Stale
// revisions never reach this point: their index rows were removed on update,
// and the FTS5 index only serves current content.
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
	external := observation.ExternalObservationID
	relative := strings.TrimPrefix(external, itemPrefix)
	// Drop the revision suffix: the provenance reports the item path, and the
	// content digest is recoverable from the artifact record when needed.
	if index := strings.Index(relative, "@"); index >= 0 {
		relative = relative[:index]
	}
	return relative, nil
}

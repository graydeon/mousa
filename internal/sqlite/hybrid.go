package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
)

// EvaluateAndTraceHybrid ranks every matching current passage and every current
// vector in one source/space. Authorization, both branches, packing and readback
// share the immediate writer snapshot; a denied request enumerates neither branch.
func (store *Store) EvaluateAndTraceHybrid(ctx context.Context, request mousa.PolicyEvaluationRequest, query, termPolicy string, spaceID mousa.SHA256, values []float64, budget uint64, packing string) (TracedVectorResult, error) {
	return store.traceHybrid(ctx, request, query, termPolicy, spaceID, values, budget, packing, nil, false)
}

// EvaluateAndTraceSupersessionHybrid withholds exact predecessor pins after fusion
// without changing branch ranks, coverage or the full source admission limits.
func (store *Store) EvaluateAndTraceSupersessionHybrid(ctx context.Context, request mousa.PolicyEvaluationRequest, query, termPolicy string, spaceID mousa.SHA256, values []float64, budget uint64, packing string) (TracedVectorResult, error) {
	return store.traceHybrid(ctx, request, query, termPolicy, spaceID, values, budget, packing, nil, true)
}

func (store *Store) traceHybrid(ctx context.Context, request mousa.PolicyEvaluationRequest, query, termPolicy string, spaceID mousa.SHA256, values []float64, budget uint64, packing string, token *TokenQuery, supersession bool) (TracedVectorResult, error) {
	if err := store.requireWritable("query hybrid"); err != nil {
		return TracedVectorResult{}, err
	}
	if err := request.Validate(); err != nil {
		return TracedVectorResult{}, wrap(CodeInvalidRecord, "query hybrid", err)
	}
	if termPolicy != "original" && termPolicy != "dedup" {
		return TracedVectorResult{}, wrap(CodeInvalidQuery, "query hybrid", errors.New("term policy must be original or dedup"))
	}
	terms, err := mousa.PrepareLexicalTerms(query, termPolicy == "dedup")
	if err != nil {
		return TracedVectorResult{}, wrap(CodeInvalidQuery, "query hybrid", err)
	}
	expression := strings.Join(terms, " OR ")
	if err := validateLexicalQuery(expression, mousa.MaxVectorPassages); err != nil {
		return TracedVectorResult{}, err
	}
	if err := mousa.ValidateVector(values, len(values)); err != nil {
		return TracedVectorResult{}, wrap(CodeInvalidQuery, "query hybrid", err)
	}
	if budget == 0 || budget > mousa.MaxVectorTextBytes || packing != mousa.PackingOriginal && packing != mousa.PackingExactV1 {
		return TracedVectorResult{}, wrap(CodeInvalidQuery, "query hybrid", errors.New("invalid hybrid byte budget or packing policy"))
	}
	var result TracedVectorResult
	err = store.writeImmediate(ctx, "query hybrid", func(conn *sql.Conn) error {
		space, err := getVectorSpace(ctx, conn, spaceID)
		if err != nil {
			return err
		}
		if err := mousa.ValidateVector(values, space.Dimensions); err != nil {
			return wrap(CodeInvalidQuery, "query hybrid", err)
		}
		decision, err := evaluateSourceRetrieval(ctx, conn, request, false)
		if err != nil {
			return err
		}
		qid, err := mousa.QueryVectorIdentity(spaceID, values)
		if err != nil {
			return err
		}
		h := mousa.HybridSelection{Query: query, TermPolicy: termPolicy, Terms: terms, FusionPolicy: mousa.HybridFusionPolicy, SpaceID: spaceID, Space: space, QueryVectorID: qid, QueryValues: values, Candidates: []mousa.HybridCandidate{}}
		passages := []VectorPassage{}
		if decision.Outcome == mousa.PolicyOutcomeAllow {
			passages, err = currentVectorPassages(ctx, conn, request.SourceID)
			if err != nil {
				return err
			}
			h.Eligible = len(passages)
		}
		current := make(map[mousa.SegmentID]VectorPassage, len(passages))
		union := make(map[mousa.SegmentID]mousa.HybridCandidate, len(passages))
		vectors := []mousa.HybridCandidate{}
		for _, p := range passages {
			if _, exists := current[p.SegmentID]; exists {
				return integrity("query hybrid", "duplicate current segment identity")
			}
			current[p.SegmentID] = p
			var raw []byte
			err := conn.QueryRowContext(ctx, `SELECT id FROM embeddings WHERE space_id=? AND source_id=? AND segment_id=?`, spaceID[:], request.SourceID[:], p.SegmentID[:]).Scan(&raw)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return classify("hybrid current embedding", err)
			}
			if len(raw) != 32 {
				return integrity("query hybrid", "invalid embedding identity")
			}
			var id mousa.SHA256
			copy(id[:], raw)
			e, err := getEmbedding(ctx, conn, id)
			if err != nil {
				return err
			}
			if e.EmbeddingInput != p.EmbeddingInput || e.SpaceID != spaceID {
				return integrity("query hybrid", "embedding does not bind current passage")
			}
			score, err := mousa.CosineSimilarity(values, e.Values)
			if err != nil {
				return wrap(CodeIntegrity, "hybrid cosine", err)
			}
			vectors = append(vectors, mousa.HybridCandidate{EmbeddingInput: p.EmbeddingInput, Vector: &mousa.HybridVectorContribution{Cosine: score, EmbeddingID: id}})
		}
		sort.Slice(vectors, func(i, j int) bool {
			a, b := vectors[i], vectors[j]
			if a.Vector.Cosine != b.Vector.Cosine {
				return a.Vector.Cosine > b.Vector.Cosine
			}
			return bytes.Compare(a.SegmentID[:], b.SegmentID[:]) < 0
		})
		for i, c := range vectors {
			c.Vector.Rank = i + 1
			union[c.SegmentID] = c
		}
		h.Indexed = len(vectors)
		if decision.Outcome == mousa.PolicyOutcomeAllow {
			lexical, err := searchVerifiedLexicalForSource(ctx, conn, expression, mousa.MaxVectorPassages, request.SourceID)
			if err != nil {
				return err
			}
			for _, c := range lexical {
				p, exists := current[c.Segment.ID]
				if !exists || c.Disposition != mousa.CandidateAccepted || p.RepresentationID != c.Segment.RepresentationID || p.ContentSHA256 != c.Segment.ContentSHA256 || p.Text != c.Text {
					return integrity("query hybrid", "lexical candidate is not an authorized current passage")
				}
				row, exists := union[c.Segment.ID]
				if !exists {
					row.EmbeddingInput = p.EmbeddingInput
				}
				row.Lexical = &mousa.HybridLexicalContribution{Rank: c.LexicalRank, BM25: c.BM25}
				union[c.Segment.ID] = row
			}
		}
		for _, c := range union {
			lr, vr := 0, 0
			if c.Lexical != nil {
				lr = c.Lexical.Rank
			}
			if c.Vector != nil {
				vr = c.Vector.Rank
			}
			c.RRF = mousa.HybridRRFScore(lr, vr)
			h.Candidates = append(h.Candidates, c)
		}
		sort.Slice(h.Candidates, func(i, j int) bool {
			a, b := h.Candidates[i], h.Candidates[j]
			if a.RRF != b.RRF {
				return a.RRF > b.RRF
			}
			return bytes.Compare(a.SegmentID[:], b.SegmentID[:]) < 0
		})
		candidates := make([]mousa.VerifiedLexicalCandidate, len(h.Candidates))
		result.Passages = make([]VectorPassage, len(h.Candidates))
		for i, c := range h.Candidates {
			p := current[c.SegmentID]
			segment, err := getSegment(ctx, conn, c.SegmentID)
			if err != nil {
				return err
			}
			candidates[i] = mousa.VerifiedLexicalCandidate{Segment: segment, Text: p.Text, FinalRank: i + 1, Disposition: mousa.CandidateAccepted}
			result.Passages[i] = p
		}
		var trail mousa.SourceTrail
		if supersession {
			consultation, err := consultRankedSupersession(ctx, conn, request.SourceID, decision.Outcome, candidates)
			if err != nil {
				return err
			}
			trail, err = mousa.NewHybridSourceTrailWithSupersession(request, decision, expression, h, candidates, budget, packing, consultation.state, consultation.declaration)
		} else {
			trail, err = mousa.NewHybridSourceTrail(request, decision, expression, h, candidates, budget, packing)
		}
		if err != nil {
			return wrap(CodeInvalidRecord, "build hybrid trail", err)
		}
		if token != nil {
			result.Verified = candidates
			trail, result.Context, err = mousa.PackTokenContext(trail, request.SourceID, candidates, token.Budget, token.Counter)
			if err != nil {
				return err
			}
		}
		if err := insertSourceTrail(ctx, conn, trail); err != nil {
			return err
		}
		written, err := getSourceTrail(ctx, conn, trail.ID)
		if err != nil {
			return err
		}
		result.Decision, result.Trail = decision, written
		clearWithheldPassages(&result)
		return nil
	})
	if err != nil {
		return TracedVectorResult{}, err
	}
	return result, nil
}

func verifyHybridTrail(ctx context.Context, q queryer, trail mousa.SourceTrail, source mousa.SourceID) error {
	h := trail.Hybrid
	space, err := getVectorSpace(ctx, q, h.SpaceID)
	if err != nil {
		return lexicalStructuralError("hybrid space", err)
	}
	if space != h.Space {
		return integrity("hybrid trail", "space descriptor disagrees with producing record")
	}
	for _, c := range h.Candidates {
		if c.SourceID != source {
			return integrity("hybrid trail", "candidate belongs to another source")
		}
		if err := verifyEmbeddingPins(ctx, q, c.EmbeddingInput); err != nil {
			return lexicalStructuralError("hybrid pins", err)
		}
		if c.Vector != nil {
			e, err := getEmbedding(ctx, q, c.Vector.EmbeddingID)
			if err != nil {
				return lexicalStructuralError("hybrid embedding", err)
			}
			if e.EmbeddingInput != c.EmbeddingInput || e.SpaceID != h.SpaceID {
				return integrity("hybrid trail", "candidate disagrees with producing embedding")
			}
			score, err := mousa.CosineSimilarity(h.QueryValues, e.Values)
			if err != nil || score != c.Vector.Cosine {
				return integrity("hybrid trail", "cosine disagrees with producing values")
			}
		}
	}
	return lexicalStructuralError("hybrid content", verifyTrailContent(ctx, q, trail, source))
}

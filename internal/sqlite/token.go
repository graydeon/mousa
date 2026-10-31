package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/tokenizer"
)

// TokenQuery owns a ready query-scoped counter; callers close it on all paths.
type TokenQuery struct {
	Budget       mousa.TokenBudget
	Counter      *tokenizer.Counter
	Supersession bool
}

func (options TokenQuery) validate() error {
	if options.Counter == nil {
		return errors.New("token query requires a ready local tokenizer")
	}
	_, err := options.Budget.Available()
	return err
}

// EvaluateAndTraceTokenLexical authorizes, retrieves, counts and persists one snapshot.
func (store *Store) EvaluateAndTraceTokenLexical(ctx context.Context, request mousa.PolicyEvaluationRequest, query, policy string, budget uint64, packing string, options TokenQuery) (TracedLexicalResult, string, error) {
	if err := store.requireWritable("query tokens"); err != nil {
		return TracedLexicalResult{}, "", err
	}
	if err := request.Validate(); err != nil {
		return TracedLexicalResult{}, "", err
	}
	if err := options.validate(); err != nil {
		return TracedLexicalResult{}, "", err
	}
	if policy != "original" && policy != "dedup" {
		return TracedLexicalResult{}, "", errors.New("invalid query policy")
	}
	terms, err := mousa.PrepareLexicalTerms(query, policy == "dedup")
	if err != nil {
		return TracedLexicalResult{}, "", err
	}
	expression := strings.Join(terms, " OR ")
	if err := validateLexicalQuery(expression, maxTrailCandidates); err != nil {
		return TracedLexicalResult{}, "", err
	}
	var result TracedLexicalResult
	var rendered string
	err = store.writeImmediate(ctx, "query tokens", func(conn *sql.Conn) error {
		decision, err := evaluateSourceRetrieval(ctx, conn, request, false)
		if err != nil {
			return err
		}
		if decision.Outcome == mousa.PolicyOutcomeAllow {
			var recovery bool
			if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM local_recovery_sources WHERE source_id=?)`, request.SourceID[:]).Scan(&recovery); err != nil {
				return classify("token recovery state", err)
			}
			if recovery {
				return errors.New("source requires complete directory sync after migration")
			}
		}
		enforced, err := searchEnforcedLexical(ctx, conn, request, expression, maxTrailCandidates)
		if err != nil {
			return err
		}
		var base mousa.SourceTrail
		if options.Supersession {
			var consultation supersessionConsultation
			if enforced.Decision.Outcome == mousa.PolicyOutcomeAllow {
				consultation, err = readSupersessionConsultation(ctx, conn, request.SourceID)
				if err != nil {
					return err
				}
			}
			base, err = mousa.NewSourceTrailWithSupersession(request, enforced.Decision, expression, enforced.Candidates, budget, packing, consultation.state, consultation.declaration)
		} else {
			base, err = mousa.NewSourceTrail(request, enforced.Decision, expression, enforced.Candidates, budget, packing)
		}
		if err != nil {
			return err
		}
		trail, text, err := mousa.PackTokenContext(base, request.SourceID, enforced.Candidates, options.Budget, options.Counter)
		if err != nil {
			return err
		}
		if err := insertSourceTrail(ctx, conn, trail); err != nil {
			return err
		}
		written, err := getSourceTrail(ctx, conn, trail.ID)
		if err != nil {
			return err
		}
		candidates := enforced.Candidates
		if written.Supersession != nil && len(written.Supersession.Dispositions) > 0 {
			candidates = slices.Clone(candidates)
			withheld := withheldSegments(written)
			for i := range candidates {
				if _, ok := withheld[candidates[i].Segment.ID]; ok {
					candidates[i].Text = ""
				}
			}
		}
		result = TracedLexicalResult{Decision: enforced.Decision, Trail: written, Candidates: candidates}
		rendered = text
		return nil
	})
	if err != nil {
		return TracedLexicalResult{}, "", err
	}
	return result, rendered, nil
}

// EvaluateAndTraceTokenVector keeps token packing inside the existing cosine snapshot.
func (store *Store) EvaluateAndTraceTokenVector(ctx context.Context, request mousa.PolicyEvaluationRequest, space mousa.SHA256, values []float64, budget uint64, packing string, options TokenQuery) (TracedVectorResult, error) {
	if err := options.validate(); err != nil {
		return TracedVectorResult{}, err
	}
	return store.traceVector(ctx, request, space, values, budget, packing, &options, options.Supersession)
}

// EvaluateAndTraceTokenHybrid keeps token packing inside the existing fused snapshot.
func (store *Store) EvaluateAndTraceTokenHybrid(ctx context.Context, request mousa.PolicyEvaluationRequest, query, policy string, space mousa.SHA256, values []float64, budget uint64, packing string, options TokenQuery) (TracedVectorResult, error) {
	if err := options.validate(); err != nil {
		return TracedVectorResult{}, err
	}
	return store.traceHybrid(ctx, request, query, policy, space, values, budget, packing, &options, options.Supersession)
}

func verifyTokenTrail(ctx context.Context, q queryer, t mousa.SourceTrail, source mousa.SourceID) error {
	if t.Token.SourceID != source {
		return integrity("token trail", "rendering source disagrees with decision")
	}
	if t.Vector != nil {
		if err := verifyVectorTrail(ctx, q, t, source); err != nil {
			return err
		}
	}
	if t.Hybrid != nil {
		if err := verifyHybridTrail(ctx, q, t, source); err != nil {
			return err
		}
	} else if err := verifyTrailContent(ctx, q, t, source); err != nil {
		return lexicalStructuralError("token content", err)
	}
	_, _, err := retainedTokenContext(ctx, q, t)
	return err
}

// retainedTokenContext cannot recreate retired bytes. No archived text is consulted.
func retainedTokenContext(ctx context.Context, q queryer, t mousa.SourceTrail) (string, bool, error) {
	var b strings.Builder
	complete := true
	for _, r := range t.Candidates {
		if !r.Selected {
			continue
		}
		segment, err := getSegment(ctx, q, r.SegmentID)
		if err != nil {
			return "", false, lexicalStructuralError("token segment", err)
		}
		text, available, err := retainedIndexedText(ctx, q, segment)
		if err != nil {
			return "", false, err
		}
		if !available {
			complete = false
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(mousa.ContextPassage(t.Token.SourceID, segment, text))
	}
	if !complete {
		return "", false, nil
	}
	text := b.String()
	if uint64(len(text)) != t.Token.RenderedBytes || mousa.SHA256(sha256.Sum256([]byte(text))) != t.Token.RenderedSHA256 {
		return "", false, integrity("token rendering", "retained context disagrees with measured digest")
	}
	return text, true, nil
}

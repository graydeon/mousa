package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
	"github.com/graydeon/mousa/internal/tokenizer"
)

type tokenEvidence struct {
	Segment mousa.Segment `json:"segment"`
	Rank    int           `json:"rank"`
	Text    string        `json:"text"`
}

func tokenQueryCommand(ctx context.Context, storePath, external string, args []string, space, mode, policy string, budget uint64, packing string, tokenBudget mousa.TokenBudget, python, cache string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if _, err := tokenBudget.Available(); err != nil {
		return usageError{err.Error()}
	}
	if budget == 0 || budget > mousa.MaxVectorTextBytes {
		return usageError{"token mode requires budget-bytes 1..262144"}
	}
	if packing != mousa.PackingOriginal && packing != mousa.PackingExactV1 {
		return usageError{"packing policy must be original or exact-v1"}
	}
	if policy != "original" && policy != "dedup" {
		return usageError{"query policy must be original or dedup"}
	}
	sourceArgs := args
	query := ""
	if mode != "vector" {
		if len(args) == 0 {
			return usageError{"token query requires query text"}
		}
		query = args[len(args)-1]
		sourceArgs = args[:len(args)-1]
	}
	source, label, err := selectSource(external, sourceArgs, "token query")
	if err != nil {
		return err
	}
	var sid mousa.SHA256
	var values []float64
	if mode != "lexical" {
		sid, err = mousa.ParseSHA256(space)
		if err != nil || sid == (mousa.SHA256{}) {
			return usageError{"token vector space must be a nonzero SHA-256 identity"}
		}
		data, err := readBoundedInput(os.Stdin, 128<<10, "token query vector")
		if err != nil {
			return err
		}
		v, err := mousa.DecodeVectorQuery(data)
		if err != nil {
			return usageError{err.Error()}
		}
		values = v.Values
	}
	if mode != "vector" {
		terms, err := mousa.PrepareLexicalTerms(query, policy == "dedup")
		if err != nil {
			return usageError{err.Error()}
		}
		if len(strings.Join(terms, " OR ")) > mousa.MaxLexicalExpressionBytes {
			return usageError{"query expression exceeds byte limit"}
		}
	}
	counter, err := startTokenCounter(ctx, python, cache)
	if err != nil {
		return err
	}
	defer counter.Close()
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	request, err := newRetrievalRequest(source.ID)
	if err != nil {
		return err
	}
	options := sqlite.TokenQuery{Budget: tokenBudget, Counter: counter}
	var trail mousa.SourceTrail
	var decision mousa.PolicyDecision
	var rendered string
	var verified []mousa.VerifiedLexicalCandidate
	if mode == "lexical" {
		got, text, err := store.EvaluateAndTraceTokenLexical(ctx, request, query, policy, budget, packing, options)
		if err != nil {
			return err
		}
		trail, decision, rendered = got.Trail, got.Decision, text
		verified = got.Candidates
	} else {
		var got sqlite.TracedVectorResult
		if mode == "hybrid" {
			got, err = store.EvaluateAndTraceTokenHybrid(ctx, request, query, policy, sid, values, budget, packing, options)
		} else {
			got, err = store.EvaluateAndTraceTokenVector(ctx, request, sid, values, budget, packing, options)
		}
		if err != nil {
			return err
		}
		trail, decision, rendered = got.Trail, got.Decision, got.Context
		verified = got.Verified
	}
	if trail.Token == nil {
		return errors.New("missing token selection record")
	}
	evidence := make([]tokenEvidence, 0, len(trail.Candidates))
	for i, row := range trail.Candidates {
		if row.Selected {
			candidate := verified[i]
			evidence = append(evidence, tokenEvidence{candidate.Segment, row.FinalRank, candidate.Text})
		}
	}
	outcome := "budget_omitted"
	if decision.Outcome != mousa.PolicyOutcomeAllow {
		outcome = "policy_excluded"
	} else if len(evidence) > 0 {
		outcome = "evidence"
	} else if len(trail.Candidates) == 0 {
		outcome = "no_candidates"
	}
	return emit(struct {
		Schema          string                       `json:"schema"`
		Mode            string                       `json:"mode"`
		Source          string                       `json:"source"`
		DecisionID      mousa.PolicyDecisionID       `json:"decision_id"`
		DecisionOutcome mousa.PolicyDecisionOutcome  `json:"decision_outcome"`
		DecisionReasons []mousa.PolicyDecisionReason `json:"decision_reasons"`
		TrailID         mousa.SourceTrailID          `json:"trail_id"`
		PacketID        string                       `json:"packet_id"`
		Outcome         string                       `json:"outcome"`
		BudgetBytes     uint64                       `json:"budget_bytes"`
		UsedBytes       uint64                       `json:"used_bytes"`
		PackingPolicy   string                       `json:"packing_policy"`
		Token           *mousa.TokenSelection        `json:"token"`
		Context         string                       `json:"context"`
		Evidence        []tokenEvidence              `json:"evidence"`
		Vector          *mousa.VectorSelection       `json:"vector,omitempty"`
		Hybrid          *mousa.HybridSelection       `json:"hybrid,omitempty"`
	}{"mousa.token_result.v1", mode, label, decision.ID, decision.Outcome, decision.ReasonCodes, trail.ID, trail.PacketID, outcome, budget, trail.UsedBytes, packing, trail.Token, rendered, evidence, trail.Vector, trail.Hybrid})
}

func startTokenCounter(ctx context.Context, python, cache string) (*tokenizer.Counter, error) {
	if cache == "" {
		root, err := os.UserCacheDir()
		if err != nil {
			return nil, err
		}
		cache = filepath.Join(root, "mousa", "tiktoken")
	}
	return tokenizer.Start(ctx, python, cache)
}

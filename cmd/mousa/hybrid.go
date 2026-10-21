package main

import (
	"context"
	"os"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

type hybridEvidence struct {
	sqlite.VectorPassage
	Rank    int                              `json:"rank"`
	RRF     float64                          `json:"rrf"`
	Lexical *mousa.HybridLexicalContribution `json:"lexical,omitempty"`
	Vector  *mousa.HybridVectorContribution  `json:"vector,omitempty"`
}

func hybridQueryCommand(ctx context.Context, storePath, external string, args []string, space, policy string, budget uint64, packing string) error {
	if len(args) == 0 {
		return usageError{"hybrid query requires query text"}
	}
	source, label, err := selectSource(external, args[:len(args)-1], "hybrid query")
	if err != nil {
		return err
	}
	queryText := args[len(args)-1]
	id, err := mousa.ParseSHA256(space)
	if err != nil || id == (mousa.SHA256{}) {
		return usageError{"hybrid-space must be a nonzero lowercase SHA-256 identity"}
	}
	if policy != "original" && policy != "dedup" {
		return usageError{"query policy must be original or dedup"}
	}
	terms, err := mousa.PrepareLexicalTerms(queryText, policy == "dedup")
	if err != nil {
		return usageError{err.Error()}
	}
	if len(strings.Join(terms, " OR ")) > mousa.MaxLexicalExpressionBytes {
		return usageError{"query expression exceeds byte limit"}
	}
	if packing != mousa.PackingOriginal && packing != mousa.PackingExactV1 {
		return usageError{"packing policy must be original or exact-v1"}
	}
	if budget == 0 || budget > mousa.MaxVectorTextBytes {
		return usageError{"hybrid budget must be 1..262144 bytes"}
	}
	data, err := readBoundedInput(os.Stdin, 128<<10, "hybrid query vector")
	if err != nil {
		return err
	}
	query, err := mousa.DecodeVectorQuery(data)
	if err != nil {
		return usageError{err.Error()}
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	request, err := newRetrievalRequest(source.ID)
	if err != nil {
		return err
	}
	traced, err := store.EvaluateAndTraceHybrid(ctx, request, queryText, policy, id, query.Values, budget, packing)
	if err != nil {
		return err
	}
	h := traced.Trail.Hybrid
	result := struct {
		Schema          string                       `json:"schema"`
		Mode            string                       `json:"mode"`
		Source          string                       `json:"source"`
		DecisionID      mousa.PolicyDecisionID       `json:"decision_id"`
		DecisionOutcome mousa.PolicyDecisionOutcome  `json:"decision_outcome"`
		DecisionReasons []mousa.PolicyDecisionReason `json:"decision_reasons"`
		TrailID         mousa.SourceTrailID          `json:"trail_id"`
		PacketID        string                       `json:"packet_id"`
		Selection       *mousa.HybridSelection       `json:"hybrid"`
		Missing         int                          `json:"missing"`
		Outcome         string                       `json:"outcome"`
		PackingPolicy   string                       `json:"packing_policy"`
		BudgetBytes     uint64                       `json:"budget_bytes"`
		UsedBytes       uint64                       `json:"used_bytes"`
		Evidence        []hybridEvidence             `json:"evidence"`
	}{Schema: "mousa.hybrid_result.v1", Mode: "hybrid", Source: label, DecisionID: traced.Decision.ID, DecisionOutcome: traced.Decision.Outcome, DecisionReasons: traced.Decision.ReasonCodes, TrailID: traced.Trail.ID, PacketID: traced.Trail.PacketID, Selection: h, Missing: h.Eligible - h.Indexed, PackingPolicy: packing, BudgetBytes: budget, UsedBytes: traced.Trail.UsedBytes, Evidence: []hybridEvidence{}}
	for i, row := range traced.Trail.Candidates {
		if row.Selected {
			c := h.Candidates[i]
			result.Evidence = append(result.Evidence, hybridEvidence{VectorPassage: traced.Passages[i], Rank: row.FinalRank, RRF: c.RRF, Lexical: c.Lexical, Vector: c.Vector})
		}
	}
	switch {
	case traced.Decision.Outcome != mousa.PolicyOutcomeAllow:
		result.Outcome = "policy_excluded"
	case len(result.Evidence) > 0:
		result.Outcome = "evidence"
	case h.Eligible == 0:
		result.Outcome = "no_passages"
	case len(h.Candidates) == 0:
		result.Outcome = "no_candidates"
	default:
		result.Outcome = "budget_omitted"
	}
	return emit(result)
}

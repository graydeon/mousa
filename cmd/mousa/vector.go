package main

import (
	"context"
	"fmt"
	"os"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

func vectorCommand(ctx context.Context, storePath string, args []string) error {
	if len(args) == 0 || (args[0] != "inputs" && args[0] != "import") {
		return usageError{"vector requires inputs or import"}
	}
	operation := args[0]
	flags := newCommandFlags("vector " + operation)
	external := flags.String("source", "", "external JSONL source instead of directory")
	if err := flags.Parse(args[1:]); err != nil {
		return usageError{err.Error()}
	}
	source, _, err := selectSource(*external, flags.Args(), "vector "+operation)
	if err != nil {
		return err
	}
	var input mousa.VectorImport
	if operation == "import" {
		data, err := readBoundedInput(os.Stdin, mousa.MaxVectorInputBytes, "vector import")
		if err != nil {
			return err
		}
		input, err = mousa.DecodeVectorImport(data)
		if err != nil {
			return usageError{err.Error()}
		}
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if operation == "inputs" {
		request, err := newRetrievalRequest(source.ID)
		if err != nil {
			return err
		}
		result, err := store.ExportEmbeddingInputs(ctx, request)
		if err != nil {
			return err
		}
		return emit(struct {
			Schema          string                       `json:"schema"`
			DecisionID      mousa.PolicyDecisionID       `json:"decision_id"`
			DecisionOutcome mousa.PolicyDecisionOutcome  `json:"decision_outcome"`
			DecisionReasons []mousa.PolicyDecisionReason `json:"decision_reasons"`
			Passages        []sqlite.VectorPassage       `json:"passages"`
		}{result.Schema, result.Decision.ID, result.Decision.Outcome, result.Decision.ReasonCodes, result.Passages})
	}
	id, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		return err
	}
	return emit(struct {
		Schema   string       `json:"schema"`
		SpaceID  mousa.SHA256 `json:"space_id"`
		Imported int          `json:"imported"`
	}{"mousa.vector_import_result.v1", id, len(input.Embeddings)})
}

type vectorEvidence struct {
	sqlite.VectorPassage
	Rank        int          `json:"rank"`
	Cosine      float64      `json:"cosine"`
	EmbeddingID mousa.SHA256 `json:"embedding_id"`
}

func vectorQueryCommand(ctx context.Context, storePath, external string, args []string, space string, budget uint64, packing string, supersession bool) error {
	source, label, err := selectSource(external, args, "vector query")
	if err != nil {
		return err
	}
	id, err := mousa.ParseSHA256(space)
	if err != nil || id == (mousa.SHA256{}) {
		return usageError{"vector-space must be a nonzero lowercase SHA-256 identity"}
	}
	if packing != mousa.PackingOriginal && packing != mousa.PackingExactV1 {
		return usageError{"packing policy must be original or exact-v1"}
	}
	if budget == 0 || budget > mousa.MaxVectorTextBytes {
		return usageError{"vector budget must be 1..262144 bytes"}
	}
	data, err := readBoundedInput(os.Stdin, 128<<10, "query vector")
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
	var traced sqlite.TracedVectorResult
	if supersession {
		traced, err = store.EvaluateAndTraceSupersessionVector(ctx, request, id, query.Values, budget, packing)
	} else {
		traced, err = store.EvaluateAndTraceVector(ctx, request, id, query.Values, budget, packing)
	}
	if err != nil {
		return err
	}
	selection := traced.Trail.Vector
	result := struct {
		Schema               string                       `json:"schema"`
		Mode                 string                       `json:"mode"`
		Source               string                       `json:"source"`
		SpaceID              mousa.SHA256                 `json:"space_id"`
		QueryVectorID        mousa.SHA256                 `json:"query_vector_id"`
		DecisionID           mousa.PolicyDecisionID       `json:"decision_id"`
		DecisionOutcome      mousa.PolicyDecisionOutcome  `json:"decision_outcome"`
		DecisionReasons      []mousa.PolicyDecisionReason `json:"decision_reasons"`
		TrailID              mousa.SourceTrailID          `json:"trail_id"`
		PacketID             string                       `json:"packet_id"`
		Eligible             int                          `json:"eligible"`
		Indexed              int                          `json:"indexed"`
		Missing              int                          `json:"missing"`
		Outcome              string                       `json:"outcome"`
		PackingPolicy        string                       `json:"packing_policy"`
		BudgetBytes          uint64                       `json:"budget_bytes"`
		UsedBytes            uint64                       `json:"used_bytes"`
		Evidence             []vectorEvidence             `json:"evidence"`
		SupersessionExcluded *int                         `json:"supersession_excluded,omitempty"`
	}{
		Schema: "mousa.vector_result.v1", Mode: "vector", Source: label, SpaceID: id, QueryVectorID: selection.QueryVectorID,
		DecisionID: traced.Decision.ID, DecisionOutcome: traced.Decision.Outcome, DecisionReasons: traced.Decision.ReasonCodes,
		TrailID: traced.Trail.ID, PacketID: traced.Trail.PacketID, Eligible: selection.Eligible, Indexed: selection.Indexed, Missing: selection.Eligible - selection.Indexed,
		PackingPolicy: packing, BudgetBytes: budget, UsedBytes: traced.Trail.UsedBytes, Evidence: []vectorEvidence{},
	}
	if traced.Trail.Supersession != nil {
		result.SupersessionExcluded = new(int)
		*result.SupersessionExcluded = len(traced.Trail.Supersession.Dispositions)
	}
	for i, row := range traced.Trail.Candidates {
		if row.Selected {
			c := selection.Candidates[i]
			result.Evidence = append(result.Evidence, vectorEvidence{VectorPassage: traced.Passages[i], Rank: row.FinalRank, Cosine: c.Cosine, EmbeddingID: c.EmbeddingID})
		}
	}
	switch {
	case traced.Decision.Outcome != mousa.PolicyOutcomeAllow:
		result.Outcome = "policy_excluded"
	case len(result.Evidence) > 0:
		result.Outcome = "evidence"
	case selection.Eligible == 0:
		result.Outcome = "no_passages"
	case selection.Indexed == 0:
		result.Outcome = "no_indexed_passages"
	case result.SupersessionExcluded != nil && *result.SupersessionExcluded == len(traced.Trail.Candidates):
		result.Outcome = "supersession_excluded"
	default:
		result.Outcome = "budget_omitted"
	}
	if result.Missing < 0 {
		return fmt.Errorf("vector coverage disagrees with canonical selection")
	}
	return emit(result)
}

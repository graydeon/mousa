package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/graydeon/mousa/internal/mousa"
)

const maxLexicalEvidence = 4096

// SearchVerifiedLexical returns raw lexical candidates with canonical ancestry and restrictive lifecycle decisions.
func (store *Store) SearchVerifiedLexical(ctx context.Context, expression string, limit int) ([]mousa.VerifiedLexicalCandidate, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, classify("begin verified lexical search", err)
	}
	defer tx.Rollback()
	candidates, err := searchVerifiedLexical(ctx, tx, expression, limit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, classify("finish verified lexical search", err)
	}
	return candidates, nil
}

// SearchEnforcedLexical verifies the exact stored decision for request and returns the verified
// lexical candidates of the decision Source. A stored deny decision authorizes no candidate, and an
// allow decision authorizes only candidates that derive from the decision Source. Enforcement never
// writes and never re-evaluates policy, so a read-only store can enforce.
func (store *Store) SearchEnforcedLexical(ctx context.Context, request mousa.PolicyEvaluationRequest, expression string, limit int) (mousa.EnforcedLexicalResult, error) {
	if err := request.Validate(); err != nil {
		return mousa.EnforcedLexicalResult{}, wrap(CodeInvalidRecord, "search enforced lexical", err)
	}
	if err := validateLexicalQuery(expression, limit); err != nil {
		return mousa.EnforcedLexicalResult{}, err
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return mousa.EnforcedLexicalResult{}, ctxErr
		}
		return mousa.EnforcedLexicalResult{}, classify("begin enforced lexical search", err)
	}
	defer tx.Rollback()
	result, err := searchEnforcedLexical(ctx, tx, request, expression, limit)
	if err != nil {
		return mousa.EnforcedLexicalResult{}, err
	}
	if err := tx.Commit(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return mousa.EnforcedLexicalResult{}, ctxErr
		}
		return mousa.EnforcedLexicalResult{}, classify("finish enforced lexical search", err)
	}
	return result, nil
}

// searchEnforcedLexical reads one decision snapshot and enforces its outcome against the scoped
// candidate search. Decision lookup and candidate retrieval must stay in one read transaction.
func searchEnforcedLexical(ctx context.Context, q queryer, request mousa.PolicyEvaluationRequest, expression string, limit int) (mousa.EnforcedLexicalResult, error) {
	decision, err := getPolicyDecisionByRequest(ctx, q, request.ID)
	if IsCode(err, CodeNotFound) {
		return mousa.EnforcedLexicalResult{}, wrap(CodeNotFound, "search enforced lexical", errors.New("no stored decision for request"))
	}
	if err != nil {
		return mousa.EnforcedLexicalResult{}, err
	}
	if decision.Request != request {
		return mousa.EnforcedLexicalResult{}, integrity("search enforced lexical", "stored request disagrees with request identity")
	}
	result := mousa.EnforcedLexicalResult{Decision: decision, Candidates: []mousa.VerifiedLexicalCandidate{}}
	if decision.Outcome != mousa.PolicyOutcomeAllow {
		return result, nil
	}
	candidates, err := searchVerifiedLexicalForSource(ctx, q, expression, limit, decision.Request.SourceID)
	if err != nil {
		return mousa.EnforcedLexicalResult{}, err
	}
	result.Candidates = candidates
	return result, nil
}

func searchVerifiedLexical(ctx context.Context, q queryer, expression string, limit int) ([]mousa.VerifiedLexicalCandidate, error) {
	raw, err := searchLexical(ctx, q, expression, limit)
	if err != nil {
		return nil, err
	}
	return verifyLexicalEvidence(ctx, q, raw)
}

func searchVerifiedLexicalForSource(ctx context.Context, q queryer, expression string, limit int, sourceID mousa.SourceID) ([]mousa.VerifiedLexicalCandidate, error) {
	raw, err := searchLexicalForSource(ctx, q, expression, limit, sourceID)
	if err != nil {
		return nil, err
	}
	return verifyLexicalEvidence(ctx, q, raw)
}

func verifyLexicalEvidence(ctx context.Context, q queryer, raw []LexicalCandidate) ([]mousa.VerifiedLexicalCandidate, error) {
	evidence := make([]mousa.LexicalCandidateEvidence, 0, len(raw))
	// All candidates share the caller's transaction; ancestry depends on the
	// representation, while segment bytes and lifecycle remain candidate checks.
	ancestry := make(map[mousa.RepresentationID][]mousa.EvidencePath)
	for _, candidate := range raw {
		paths, exists := ancestry[candidate.Segment.RepresentationID]
		if !exists {
			var err error
			paths, err = lexicalEvidencePaths(ctx, q, candidate.Segment)
			if err != nil {
				return nil, err
			}
			ancestry[candidate.Segment.RepresentationID] = paths
		}
		sources, err := lexicalSourceEvidence(ctx, q, paths)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, mousa.LexicalCandidateEvidence{
			Segment: candidate.Segment, Text: candidate.Text, BM25: candidate.BM25,
			Paths: paths, Sources: sources,
		})
	}
	verified, err := mousa.RankAndVerifyLexicalCandidates(evidence)
	if err != nil {
		return nil, wrap(CodeIntegrity, "verify lexical candidates", err)
	}
	return verified, nil
}

func lexicalEvidencePaths(ctx context.Context, q queryer, segment mousa.Segment) ([]mousa.EvidencePath, error) {
	visited := 0
	return lexicalRepresentationPaths(ctx, q, segment.RepresentationID, nil, make(map[mousa.RepresentationID]struct{}), &visited)
}

func lexicalRepresentationPaths(ctx context.Context, q queryer, id mousa.RepresentationID, chain []mousa.RepresentationID, visiting map[mousa.RepresentationID]struct{}, visited *int) ([]mousa.EvidencePath, error) {
	if _, cycle := visiting[id]; cycle {
		return nil, integrity("load lexical ancestry", "representation ancestry contains a cycle")
	}
	*visited = *visited + 1
	if *visited > maxLexicalEvidence {
		return nil, wrap(CodeResourceLimit, "load lexical ancestry", errors.New("visited representation count exceeds 4096"))
	}
	representation, err := getRepresentation(ctx, q, id)
	if err != nil {
		return nil, lexicalStructuralError("load lexical representation", err)
	}
	inputs, err := projectedInputs(representation.Inputs)
	if err != nil {
		return nil, wrap(CodeIntegrity, "load lexical ancestry", err)
	}
	chain = append(append([]mousa.RepresentationID(nil), chain...), id)
	visiting[id] = struct{}{}
	defer delete(visiting, id)
	paths := make([]mousa.EvidencePath, 0)
	for _, input := range inputs {
		switch input.kind {
		case "representation":
			var inputID mousa.RepresentationID
			copy(inputID[:], input.id)
			nested, err := lexicalRepresentationPaths(ctx, q, inputID, chain, visiting, visited)
			if err != nil {
				return nil, err
			}
			paths = append(paths, nested...)
		case "artifact":
			var artifactID mousa.ArtifactID
			copy(artifactID[:], input.id)
			artifact, err := getArtifact(ctx, q, artifactID)
			if err != nil {
				return nil, lexicalStructuralError("load lexical artifact", err)
			}
			observation, err := getObservation(ctx, q, artifact.ObservationID)
			if err != nil {
				return nil, lexicalStructuralError("load lexical observation", err)
			}
			if _, err := getSource(ctx, q, observation.SourceID); err != nil {
				return nil, lexicalStructuralError("load lexical source", err)
			}
			paths = append(paths, mousa.EvidencePath{
				RepresentationIDs: append([]mousa.RepresentationID(nil), chain...),
				ArtifactID:        artifact.ID, ObservationID: observation.ID, SourceID: observation.SourceID,
			})
		}
		if len(paths) > maxLexicalEvidence {
			return nil, wrap(CodeResourceLimit, "load lexical ancestry", errors.New("completed path count exceeds 4096"))
		}
	}
	return paths, nil
}

func lexicalSourceEvidence(ctx context.Context, q queryer, paths []mousa.EvidencePath) ([]mousa.SourceLifecycleEvidence, error) {
	seen := make(map[mousa.SourceID]struct{}, len(paths))
	sources := make([]mousa.SourceLifecycleEvidence, 0, len(paths))
	for _, path := range paths {
		if _, exists := seen[path.SourceID]; exists {
			continue
		}
		seen[path.SourceID] = struct{}{}
		state, err := getIngestState(ctx, q, path.SourceID)
		if err != nil {
			if IsCode(err, CodeNotFound) {
				sources = append(sources, mousa.SourceLifecycleEvidence{SourceID: path.SourceID})
				continue
			}
			return nil, err
		}
		sources = append(sources, mousa.SourceLifecycleEvidence{
			SourceID: state.SourceID, StatePresent: true, CollectionState: state.CollectionState,
			LastObservationID: state.LastObservationID, CurrentWithdrawalID: state.CurrentWithdrawalID, LastCapturedAtUsec: state.LastCapturedAtUsec,
		})
	}
	return sources, nil
}

func lexicalStructuralError(operation string, err error) error {
	if IsCode(err, CodeNotFound) {
		return wrap(CodeIntegrity, operation, err)
	}
	return err
}

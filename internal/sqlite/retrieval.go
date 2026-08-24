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

func searchVerifiedLexical(ctx context.Context, q queryer, expression string, limit int) ([]mousa.VerifiedLexicalCandidate, error) {
	raw, err := searchLexical(ctx, q, expression, limit)
	if err != nil {
		return nil, err
	}
	evidence := make([]mousa.LexicalCandidateEvidence, 0, len(raw))
	for _, candidate := range raw {
		paths, err := lexicalEvidencePaths(ctx, q, candidate.Segment)
		if err != nil {
			return nil, err
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

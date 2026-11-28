package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/graydeon/mousa/internal/mousa"
)

const maxLexicalEvidence = 4096

// lexicalEvidenceReader memoizes canonical record reads inside one snapshot.
// The cache is admission-bounded in count and record size; it never replaces
// a check and never outlives its transaction.
type lexicalEvidenceReader struct {
	q               queryer
	representations map[mousa.RepresentationID]mousa.Representation
	artifacts       map[mousa.ArtifactID]mousa.Artifact
	observations    map[mousa.ObservationID]mousa.Observation
	sources         map[mousa.SourceID]mousa.Source
}

func readLexicalRecord[Q any, K comparable, V any](ctx context.Context, q Q, cache map[K]V, id K, read func(context.Context, Q, K) (V, error), small func(V) bool) (V, error) {
	if record, found := cache[id]; found {
		return record, nil
	}
	record, err := read(ctx, q, id)
	if err == nil && cache != nil && len(cache) < 1024 && small(record) {
		cache[id] = record
	}
	return record, err
}

func (r lexicalEvidenceReader) representation(ctx context.Context, id mousa.RepresentationID) (mousa.Representation, error) {
	return readLexicalRecord(ctx, r.q, r.representations, id, getRepresentation, func(v mousa.Representation) bool {
		return len(v.Inputs) <= 16 && len(v.ProcessorID)+len(v.ProcessorVersion)+len(v.MediaType) <= 4096
	})
}

func (r lexicalEvidenceReader) artifact(ctx context.Context, id mousa.ArtifactID) (mousa.Artifact, error) {
	return readLexicalRecord[queryRower](ctx, r.q, r.artifacts, id, getArtifact, func(v mousa.Artifact) bool {
		return len(v.ArtifactKey)+len(v.MediaType) <= 4096
	})
}

func (r lexicalEvidenceReader) observation(ctx context.Context, id mousa.ObservationID) (mousa.Observation, error) {
	return readLexicalRecord[queryRower](ctx, r.q, r.observations, id, getObservation, func(v mousa.Observation) bool {
		return len(v.ExternalObservationID) <= 4096
	})
}

func (r lexicalEvidenceReader) source(ctx context.Context, id mousa.SourceID) (mousa.Source, error) {
	return readLexicalRecord[queryRower](ctx, r.q, r.sources, id, getSource, func(v mousa.Source) bool {
		return len(v.Namespace)+len(v.ExternalSourceID) <= 4096
	})
}

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
	return (lexicalEvidenceReader{q: q}).paths(ctx, segment)
}

func (r lexicalEvidenceReader) paths(ctx context.Context, segment mousa.Segment) ([]mousa.EvidencePath, error) {
	visited := 0
	return r.representationPaths(ctx, segment.RepresentationID, nil, make(map[mousa.RepresentationID]struct{}), &visited)
}

func lexicalRepresentationPaths(ctx context.Context, q queryer, id mousa.RepresentationID, chain []mousa.RepresentationID, visiting map[mousa.RepresentationID]struct{}, visited *int) ([]mousa.EvidencePath, error) {
	return (lexicalEvidenceReader{q: q}).representationPaths(ctx, id, chain, visiting, visited)
}

func (r lexicalEvidenceReader) representationPaths(ctx context.Context, id mousa.RepresentationID, chain []mousa.RepresentationID, visiting map[mousa.RepresentationID]struct{}, visited *int) ([]mousa.EvidencePath, error) {
	if _, cycle := visiting[id]; cycle {
		return nil, integrity("load lexical ancestry", "representation ancestry contains a cycle")
	}
	*visited = *visited + 1
	if *visited > maxLexicalEvidence {
		return nil, wrap(CodeResourceLimit, "load lexical ancestry", errors.New("visited representation count exceeds 4096"))
	}
	representation, err := r.representation(ctx, id)
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
			nested, err := r.representationPaths(ctx, inputID, chain, visiting, visited)
			if err != nil {
				return nil, err
			}
			paths = append(paths, nested...)
		case "artifact":
			var artifactID mousa.ArtifactID
			copy(artifactID[:], input.id)
			artifact, err := r.artifact(ctx, artifactID)
			if err != nil {
				return nil, lexicalStructuralError("load lexical artifact", err)
			}
			observation, err := r.observation(ctx, artifact.ObservationID)
			if err != nil {
				return nil, lexicalStructuralError("load lexical observation", err)
			}
			if _, err := r.source(ctx, observation.SourceID); err != nil {
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

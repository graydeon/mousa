package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/graydeon/mousa/internal/mousa"
)

func (store *Store) EvaluateAndTraceAssociatedVector(ctx context.Context, request mousa.PolicyEvaluationRequest, space mousa.SHA256, values []float64, budget uint64, packing string, declarations []mousa.AssociationDeclaration, supersession bool) (TracedVectorResult, error) {
	return store.traceVector(ctx, request, space, values, budget, packing, nil, supersession, declarations)
}

func (store *Store) EvaluateAndTraceAssociatedHybrid(ctx context.Context, request mousa.PolicyEvaluationRequest, query, policy string, space mousa.SHA256, values []float64, budget uint64, packing string, declarations []mousa.AssociationDeclaration, supersession bool) (TracedVectorResult, error) {
	return store.traceHybrid(ctx, request, query, policy, space, values, budget, packing, nil, supersession, declarations)
}

func validateRetrievalDeclarations(declarations []mousa.AssociationDeclaration) error {
	if len(declarations) > mousa.MaxAssociationDeclarations {
		return errors.New("too many association declarations")
	}
	seen := make(map[string]bool, len(declarations))
	for _, d := range declarations {
		if err := d.Validate(); err != nil {
			return err
		}
		key := d.FromItem + "\x00" + d.ToItem
		if seen[key] {
			return errors.New("duplicate association declaration")
		}
		seen[key] = true
	}
	return nil
}

func packRetrievalAssociations(ctx context.Context, conn *sql.Conn, source mousa.SourceID, trail mousa.SourceTrail, candidates []mousa.VerifiedLexicalCandidate, universe []VectorPassage, declarations []mousa.AssociationDeclaration, supersession *mousa.SupersessionDeclaration, token *TokenQuery, result *TracedVectorResult) (mousa.SourceTrail, error) {
	resolve := func(primary mousa.SourceTrail) ([]mousa.AssociatedPassage, []mousa.AssociationOmission, error) {
		for i, row := range primary.Candidates {
			if !row.Selected {
				continue
			}
			active, err := retrievalAssociationLifecycle(ctx, conn, candidates[i].Segment)
			if err != nil {
				return nil, nil, err
			}
			if !active {
				return nil, nil, wrap(CodeInvalidQuery, "retrieval association", errors.New("selected primary has inactive ancestry"))
			}
		}
		associated, omissions, err := resolveAssociatedPassages(ctx, conn, source, declarations, candidates, primary)
		if err != nil {
			return nil, nil, err
		}
		admitted := make(map[mousa.SegmentID]VectorPassage, len(universe))
		for _, p := range universe {
			admitted[p.SegmentID] = p
		}
		retained := associated[:0]
		inactive := make(map[mousa.RepresentationID]bool)
		for _, p := range associated {
			current, ok := admitted[p.Segment.ID]
			if !ok || current.RepresentationID != p.Segment.RepresentationID || current.ContentSHA256 != p.Segment.ContentSHA256 || current.Text != p.Text || current.Item != p.Item {
				return nil, nil, integrity("retrieval association", "target is outside admitted current source universe")
			}
			active, err := retrievalAssociationLifecycle(ctx, conn, p.Segment)
			if err != nil {
				return nil, nil, err
			}
			if !active {
				if !inactive[p.Segment.RepresentationID] {
					omissions = append(omissions, mousa.AssociationOmission{
						FromItem: p.Declaration.FromItem, ToItem: p.Declaration.ToItem,
						Reason: mousa.AssociationTargetInactive,
					})
					inactive[p.Segment.RepresentationID] = true
				}
				continue
			}
			retained = append(retained, p)
		}
		clear(associated[len(retained):])
		retained = retained[:len(retained):len(retained)]
		result.Associated = retained
		return retained, omissions, nil
	}
	var err error
	if token != nil {
		result.Verified = candidates
		if len(declarations) == 0 {
			trail, result.Context, err = mousa.PackTokenContext(trail, source, candidates, token.Budget, token.Counter)
		} else {
			trail, result.Context, err = mousa.PackAssociatedTokenContext(trail, source, candidates, token.Budget, token.Counter, supersession, resolve)
		}
	} else if len(declarations) > 0 {
		var associated []mousa.AssociatedPassage
		var omissions []mousa.AssociationOmission
		associated, omissions, err = resolve(trail)
		if err != nil {
			return mousa.SourceTrail{}, err
		}
		if trail.Supersession != nil {
			trail, err = mousa.AppendSupersessionAssociatedPassages(trail, candidates, associated, omissions, supersession)
		} else {
			trail, err = mousa.AppendAssociatedPassages(trail, candidates, associated, omissions)
		}
	}
	if err != nil {
		return mousa.SourceTrail{}, err
	}
	for i, r := range trail.Associated {
		if r.Omission == string(mousa.SupersessionSuperseded) {
			result.Associated[i].Text = ""
		}
	}
	return trail, nil
}

func retrievalAssociationLifecycle(ctx context.Context, q queryer, segment mousa.Segment) (bool, error) {
	paths, err := lexicalEvidencePaths(ctx, q, segment)
	if err != nil {
		return false, err
	}
	sources, err := lexicalSourceEvidence(ctx, q, paths)
	if err != nil {
		return false, err
	}
	for _, source := range sources {
		if !source.StatePresent || source.CollectionState != mousa.CollectionActive {
			return false, nil
		}
	}
	return true, nil
}

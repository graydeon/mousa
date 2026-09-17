package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
)

// indexedSegment is one canonical Segment with its verified indexed text.
type indexedSegment struct {
	Segment mousa.Segment
	Text    string
}

// localItemPathOfRepresentation derives the item identity of one revision of a local item,
// verifying the observation identity that binds it. It is the store-side counterpart of the
// consumer's item derivation for selected evidence.
func localItemPathOfRepresentation(ctx context.Context, q queryer, sourceID mousa.SourceID, id mousa.RepresentationID) (string, error) {
	artifact, err := representationSourceArtifact(ctx, q, id)
	if err != nil {
		return "", err
	}
	observation, err := getObservation(ctx, q, artifact.ObservationID)
	if err != nil {
		return "", err
	}
	if observation.SourceID != sourceID {
		return "", integrity("local item path", "representation belongs to another source")
	}
	suffix := fmt.Sprintf("@%x", artifact.ContentSHA256)
	rest, ok := strings.CutPrefix(observation.ExternalObservationID, "item/")
	if !ok || !strings.HasSuffix(rest, suffix) {
		return "", integrity("local item path", "observation is not a local item")
	}
	item := strings.TrimSuffix(rest, suffix)
	if item == "" {
		return "", integrity("local item path", "item identity disagrees with artifact digest")
	}
	return item, nil
}

// indexedSegmentText reads one segment's verified indexed text. The lexical row is the
// released-text source for retrieval, so an active item without its index rows is an
// integrity condition instead of a silent omission.
func indexedSegmentText(ctx context.Context, q queryer, segment mousa.Segment) (string, error) {
	var text string
	var digest []byte
	err := q.QueryRowContext(ctx, `
		SELECT f.text, f.content_sha256
		FROM segment_lexical_rows AS r
		JOIN segment_lexical_fts AS f ON f.rowid = r.rowid
		WHERE r.segment_id = ?`, segment.ID[:]).Scan(&text, &digest)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", integrity("associated passage text", "active segment has no lexical row")
		}
		return "", classify("associated passage text", err)
	}
	if err := verifyLexicalPayload(segment, []byte(text), digest); err != nil {
		return "", err
	}
	return text, nil
}

// resolveAssociatedPassages reads the association stage: every passage of a target item whose
// declaring item contributed selected primary evidence, in declaration order and selector order,
// plus one omission for every honored declaration that releases nothing. Depth is one: only
// declarations whose source item appears in the primary selection are resolved, and the target's
// own passages are never followed by further declarations. Resolution never crosses sources and
// only reads the current active revision, so a declaration grants nothing.
func resolveAssociatedPassages(ctx context.Context, q queryer, sourceID mousa.SourceID, declarations []mousa.AssociationDeclaration, candidates []mousa.VerifiedLexicalCandidate, trail mousa.SourceTrail) ([]mousa.AssociatedPassage, []mousa.AssociationOmission, error) {
	primaryItems := make(map[string]struct{}, len(candidates))
	for index, candidate := range trail.Candidates {
		if !candidate.Selected {
			continue
		}
		item, err := localItemPathOfRepresentation(ctx, q, sourceID, candidates[index].Segment.RepresentationID)
		if err != nil {
			return nil, nil, err
		}
		primaryItems[item] = struct{}{}
	}
	var passages []mousa.AssociatedPassage
	var omissions []mousa.AssociationOmission
	applied := make(map[string]struct{}, mousa.MaxAssociationTargets)
	attempts := 0
	for _, declaration := range declarations {
		if _, fires := primaryItems[declaration.FromItem]; !fires {
			continue
		}
		if _, repeated := applied[declaration.ToItem]; repeated {
			omissions = append(omissions, mousa.AssociationOmission{
				FromItem: declaration.FromItem, ToItem: declaration.ToItem, Reason: mousa.AssociationDuplicateTarget,
			})
			continue
		}
		if attempts >= mousa.MaxAssociationTargets {
			omissions = append(omissions, mousa.AssociationOmission{
				FromItem: declaration.FromItem, ToItem: declaration.ToItem, Reason: mousa.AssociationFanOut,
			})
			continue
		}
		attempts++
		applied[declaration.ToItem] = struct{}{}
		item, err := getLocalItem(ctx, q, sourceID, declaration.ToItem)
		if IsCode(err, CodeNotFound) {
			omissions = append(omissions, mousa.AssociationOmission{
				FromItem: declaration.FromItem, ToItem: declaration.ToItem, Reason: mousa.AssociationTargetUnknown,
			})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if !item.Active {
			omissions = append(omissions, mousa.AssociationOmission{
				FromItem: declaration.FromItem, ToItem: declaration.ToItem, Reason: mousa.AssociationTargetInactive,
			})
			continue
		}
		segments, err := lexicalSegments(ctx, q, item.RepresentationID)
		if err != nil {
			return nil, nil, err
		}
		for _, segment := range segments {
			text, err := indexedSegmentText(ctx, q, segment)
			if err != nil {
				return nil, nil, err
			}
			passages = append(passages, mousa.AssociatedPassage{
				Segment: segment, Item: declaration.ToItem, Text: text, Declaration: declaration,
			})
		}
	}
	return passages, omissions, nil
}

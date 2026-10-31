package sqlite

import (
	"context"

	"github.com/graydeon/mousa/internal/mousa"
)

// consultRankedSupersession verifies ancestry for the already admitted producing
// order. A denial reads no administration; no successor retrieval is required.
func consultRankedSupersession(ctx context.Context, q queryer, source mousa.SourceID, outcome mousa.PolicyDecisionOutcome, candidates []mousa.VerifiedLexicalCandidate) (supersessionConsultation, error) {
	if outcome != mousa.PolicyOutcomeAllow {
		return supersessionConsultation{}, nil
	}
	for i := range candidates {
		paths, err := lexicalEvidencePaths(ctx, q, candidates[i].Segment)
		if err != nil {
			return supersessionConsultation{}, err
		}
		candidates[i].Paths = paths
	}
	return readSupersessionConsultation(ctx, q, source)
}

func clearWithheldPassages(result *TracedVectorResult) {
	if result.Trail.Supersession == nil {
		return
	}
	rows := result.Trail.Supersession.Dispositions
	next := 0
	for i := range result.Passages {
		if next == len(rows) {
			break
		}
		if result.Passages[i].SegmentID == rows[next].SegmentID {
			result.Passages[i].Text = ""
			if len(result.Verified) > 0 {
				result.Verified[i].Text = ""
			}
			next++
		}
	}
}

package sqlite

import (
	"github.com/graydeon/mousa/internal/mousa"
)

// packRetrievalTokens applies token-budget packing to a ranked vector or hybrid trail when the
// query carries a token budget; otherwise the trail keeps its byte packing.
func packRetrievalTokens(trail mousa.SourceTrail, source mousa.SourceID, candidates []mousa.VerifiedLexicalCandidate, token *TokenQuery, result *TracedVectorResult) (mousa.SourceTrail, error) {
	if token == nil {
		return trail, nil
	}
	result.Verified = candidates
	trail, rendered, err := mousa.PackTokenContext(trail, source, candidates, token.Budget, token.Counter)
	result.Context = rendered
	return trail, err
}

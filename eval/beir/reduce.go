package beir

import (
	"errors"
	"fmt"
	"strings"
)

// floorMagnitudeLimit bounds the per-document BM25 magnitude a query term may
// have for the floor-reduction policy to drop it. FTS5 clamps a term's inverse
// document frequency at a small positive constant (measured ~1e-6; the floor
// probe reads up to 1.9e-6 after the length/tf factor, bounded by k1+1 = 2.2),
// so a term whose strongest evidence is below this limit contributes at most
// the floor to any document while still costing a full postings scan per
// instance. The nearest measured non-floor terms score 1e-3 and up, four
// orders above the limit, so the exact value is not delicate. The limit is a
// guard on a measured property, not a formula: TermEvidence reports the
// engine's own strongest magnitude per term, so a build without the clamp
// simply stops classifying terms as floor.
const floorMagnitudeLimit = 3e-6

// TermEvidence is one query term's measured evidence in an indexed corpus: the
// strongest per-document BM25 magnitude the engine assigns to the term (0 when
// the term has no postings) and the number of indexed rows containing it.
type TermEvidence struct {
	MaxMagnitude      float64
	DocumentFrequency int
}

// TermEvidenceOracle reports measured term evidence for an indexed corpus.
// The harness implements it with read-only probes against the store's index;
// tests substitute hand-computed values.
type TermEvidenceOracle interface {
	TermEvidence(term string) (TermEvidence, error)
}

// Reduction accounts for one query's floor-weight expression reduction.
type Reduction struct {
	// Expression is the reduced MATCH expression.
	Expression string
	// Baseline is the expression the published protocol would have built.
	Baseline string
	// Kept and Dropped carry the surviving and removed terms after the byte
	// cap, in query order with duplicates.
	Kept    []string
	Dropped []string
}

// ReduceFloorTerms builds the query's MATCH expression under the published
// protocol (tokenize, cap to the store's expression bound), then removes the
// terms whose measured evidence is at or below floorMagnitudeLimit. Keeping
// every other term in order makes the reduced expression a subset of the
// baseline expression, so the only score difference is the removed terms'
// contribution. A reduction that would empty the expression is refused: an
// all-floor query has no evidence to rank by, and falling back to the baseline
// expression would silently reintroduce the cost the policy exists to remove.
func ReduceFloorTerms(query string, oracle TermEvidenceOracle) (Reduction, error) {
	baselineTerms, err := cappedTerms(query)
	if err != nil {
		return Reduction{}, err
	}
	baseline := strings.Join(baselineTerms, " OR ")
	kept := make([]string, 0, len(baselineTerms))
	var dropped []string
	for _, quoted := range baselineTerms {
		evidence, err := oracle.TermEvidence(unquoteTerm(quoted))
		if err != nil {
			return Reduction{}, fmt.Errorf("term evidence for %s: %w", quoted, err)
		}
		if evidence.MaxMagnitude <= floorMagnitudeLimit {
			dropped = append(dropped, unquoteTerm(quoted))
			continue
		}
		kept = append(kept, quoted)
	}
	if len(kept) == 0 {
		return Reduction{}, errNoEvidenceTerms
	}
	return Reduction{
		Expression: strings.Join(kept, " OR "),
		Baseline:   baseline,
		Kept:       kept,
		Dropped:    dropped,
	}, nil
}

// errNoEvidenceTerms reports that a query's every term measured at or below
// the floor limit, so the reduction would produce an expression with no
// evidence-bearing term. Callers may fall back to the baseline expression and
// account the fallback; other errors are failures.
var errNoEvidenceTerms = errors.New("query reduced to no evidence-bearing terms")

// cappedTerms tokenizes one query into quoted OR terms under the published
// protocol: alphanumeric terms lowercased and quoted, repeats kept, trailing
// terms dropped until the joined expression fits the store's bound.
func cappedTerms(query string) ([]string, error) {
	terms := make([]string, 0)
	for _, term := range queryTerms(query) {
		terms = append(terms, quoteTerm(term))
	}
	if len(terms) == 0 {
		return nil, fmt.Errorf("query produced no searchable terms")
	}
	for len(strings.Join(terms, " OR ")) > maxExpressionBytes && len(terms) > 1 {
		terms = terms[:len(terms)-1]
	}
	return terms, nil
}

func quoteTerm(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}

func unquoteTerm(quoted string) string {
	return strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(quoted, `"`), `"`), `""`, `"`)
}

// queryTerms splits one query into lowercased terms the way the store's
// tokenizer will match them: runs of ASCII alphanumerics or any non-ASCII
// rune. The index itself folds tokens (unicode61 remove_diacritics 2), which
// is why term evidence must be probed through MATCH and never through
// vocabulary strings.
func queryTerms(query string) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		isLetter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		isLetter = isLetter || r >= 0x80
		return !isLetter
	})
	terms := make([]string, 0, len(fields))
	for _, field := range fields {
		term := strings.ToLower(strings.TrimSpace(field))
		if term == "" {
			continue
		}
		terms = append(terms, term)
	}
	return terms
}

package beir

import (
	"errors"
	"fmt"
	"strings"
)

// CONTRACT (experimental policy, not an adopted optimization): the predicate
// that drops a term is the MEASURED MAGNITUDE predicate — MaxMagnitude <=
// floorMagnitudeLimit, where MaxMagnitude is read from the index through a
// MATCH probe. It is not a document-frequency rule. On a clamping SQLite build
// the two coincide for present terms (df*2 >= rows implies magnitude at the
// floor), but the magnitude predicate is authoritative and it also classifies
// ABSENT terms (zero postings, zero magnitude) as floor terms. The premise
// verifier checks the df boundary as a proxy for the risk surface; a premise
// failure refuses the run fail-closed — there is no keep-terms fallback on a
// changed build. A query whose every term is dropped is refused here
// (errNoEvidenceTerms) and the caller falls back to the baseline expression
// with explicit FallbackQueries accounting.
//
// Phase 17 measured this policy against the strict pre-registered rule that
// required every ordered ranking to match; 1398/1401 matched, so the strict
// identity criterion FAILED. The measured speed is promising evidence for an
// approximate policy, not an adopted optimization; docs/RESEARCH.md records
// the dated correction.
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
// vocabulary strings (Phase 16).
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

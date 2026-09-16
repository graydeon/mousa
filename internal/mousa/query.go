package mousa

import (
	"errors"
	"strings"
)

const MaxLexicalExpressionBytes = 4096

// LexicalQueryTerms preserves the published query-splitting rule: ASCII
// alphanumerics and non-ASCII runes, lowercased after trimming whitespace.
// It is not a complete implementation of the index's unicode61 tokenizer.
func LexicalQueryTerms(query string) []string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r >= 0x80)
	})
	terms := fields[:0]
	for _, field := range fields {
		term := strings.ToLower(strings.TrimSpace(field))
		if term != "" {
			terms = append(terms, term)
		}
	}
	return terms
}

// QuoteLexicalTerm escapes one literal FTS5 phrase, not a query operator.
func QuoteLexicalTerm(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}

// PrepareLexicalTerms quotes terms and retains the longest leading prefix whose
// OR-joined expression fits the bound. Deduplication precedes capping. As in the
// published protocol, an oversized first term is retained for the store to reject.
func PrepareLexicalTerms(query string, deduplicate bool) ([]string, error) {
	terms := LexicalQueryTerms(query)
	var seen map[string]struct{}
	if deduplicate {
		seen = make(map[string]struct{})
	}
	used, count := 0, 0
	for _, term := range terms {
		if deduplicate {
			if _, duplicate := seen[term]; duplicate {
				continue
			}
			seen[term] = struct{}{}
		}
		quoted := QuoteLexicalTerm(term)
		additional := len(quoted)
		if count > 0 {
			additional += len(" OR ")
		}
		if count > 0 && used+additional > MaxLexicalExpressionBytes {
			break
		}
		terms[count] = quoted
		used += additional
		count++
	}
	if count == 0 {
		return nil, errors.New("query produced no searchable terms")
	}
	return terms[:count], nil
}

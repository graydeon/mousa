package mousa

import (
	"errors"
	"math"
)

// Shared BM25 scores one text's candidates with statistics summed over every
// eligible ancestry shard. A single text merges by that score; several texts
// fuse the per-text rankings with the existing reciprocal-rank rule.
const (
	SharedBM25    = "bm25-shared-v1"
	SharedBM25RRF = "bm25-shared-rrf-k60-v1"
)

// BM25Stats is the shared evidence recorded on a lexical branch: the summed
// document count, the document frequency of each original branch phrase, and
// the summed token count across every eligible ancestry shard, including
// shards that proposed no hits. Frequencies aligns with the branch's terms;
// Documents and Tokens together encode the average document length exactly.
type BM25Stats struct {
	Documents   uint64   `json:"documents"`
	Frequencies []uint64 `json:"frequencies"`
	Tokens      uint64   `json:"tokens"`
}

// BM25Match is one candidate's shared-BM25 evidence: its token length and the
// occurrence count of each branch phrase inside its text.
type BM25Match struct {
	Frequencies []uint64 `json:"frequencies"`
	Length      uint64   `json:"length"`
}

// IDF validates the recorded statistics and derives the FTS5 inverse document
// frequency weight of each phrase, substituting 1e-6 for a nonpositive weight.
func (s BM25Stats) IDF() ([]float64, error) {
	if s.Documents > maxJSONSafeInteger || s.Tokens > maxJSONSafeInteger ||
		(s.Documents == 0 && s.Tokens != 0) || s.Frequencies == nil {
		return nil, errors.New("invalid shared BM25 statistics")
	}
	idf := make([]float64, len(s.Frequencies))
	for i, df := range s.Frequencies {
		if df > s.Documents || (s.Tokens == 0 && df != 0) {
			return nil, errors.New("invalid shared BM25 statistics")
		}
		weight := math.Log((float64(s.Documents-df) + 0.5) / (float64(df) + 0.5))
		if weight <= 0 {
			weight = 1e-6
		}
		idf[i] = weight
	}
	return idf, nil
}

// Score recomputes the shared BM25 value of one candidate from its recorded
// match evidence. The formula and constants are FTS5's own (k1 = 1.2,
// b = 0.75); the explicit conversions prevent fused operations.
func (s BM25Stats) Score(idf []float64, m BM25Match) (float64, error) {
	if m.Length == 0 || m.Length > s.Tokens || len(m.Frequencies) != len(idf) ||
		len(idf) != len(s.Frequencies) {
		return 0, errors.New("invalid shared BM25 match")
	}
	matched := false
	for i, tf := range m.Frequencies {
		if tf > m.Length || (tf != 0 && s.Frequencies[i] == 0) {
			return 0, errors.New("invalid shared BM25 match")
		}
		matched = matched || tf != 0
	}
	if !matched {
		return 0, errors.New("invalid shared BM25 match")
	}
	average := float64(s.Tokens) / float64(s.Documents)
	norm := float64(1.2 * (0.25 + float64(0.75*float64(m.Length))/average))
	var score float64
	for i, frequency := range m.Frequencies {
		tf := float64(frequency)
		score += float64(idf[i] * ((tf * 2.2) / (tf + norm)))
	}
	return -score, nil
}

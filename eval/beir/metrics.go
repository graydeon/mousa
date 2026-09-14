// Package beir computes the ranking metrics used by the Mousa BEIR-subset
// harness: nDCG@10 (binary gain), Recall@100, and MRR@10, plus the document-level
// ranking used before scoring. Every metric is deterministic over the given ranks.
package beir

import (
	"math"
	"sort"
)

// RankResult is one query's document ranking: candidate corpus IDs in rank order.
type RankResult struct {
	QueryID string
	Ranked  []string
}

// Qrel is one graded relevance judgment for one query and corpus ID.
type Qrel struct {
	QueryID   string
	CorpusID  string
	Relevance int
}

// NDCGAt10 computes binary-gain nDCG@10: gains are 1 for any judged relevant
// document and 0 otherwise, with log2 discounting and an ideal ranking that
// places min(10, relevantCount) gains in the first positions. Returns 0 when a
// query has no relevant judgments.
func NDCGAt10(ranked []string, relevant map[string]int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	dcg := 0.0
	limit := len(ranked)
	if limit > 10 {
		limit = 10
	}
	for position, id := range ranked[:limit] {
		if relevant[id] > 0 {
			dcg += 1.0 / log2(float64(position+2))
		}
	}
	idealCount := len(relevant)
	if idealCount > 10 {
		idealCount = 10
	}
	idcg := 0.0
	for position := 0; position < idealCount; position++ {
		idcg += 1.0 / log2(float64(position+2))
	}
	return dcg / idcg
}

// RecallAt100 computes the fraction of relevant documents present in the top
// 100 ranked results.
func RecallAt100(ranked []string, relevant map[string]int) float64 {
	if len(relevant) == 0 {
		return 0
	}
	limit := len(ranked)
	if limit > 100 {
		limit = 100
	}
	found := make(map[string]struct{}, limit)
	for _, id := range ranked[:limit] {
		if relevant[id] > 0 {
			found[id] = struct{}{}
		}
	}
	return float64(len(found)) / float64(len(relevant))
}

// MRRAt10 computes the reciprocal rank of the first relevant document within
// the top 10, or 0 when none appears.
func MRRAt10(ranked []string, relevant map[string]int) float64 {
	limit := len(ranked)
	if limit > 10 {
		limit = 10
	}
	for position, id := range ranked[:limit] {
		if relevant[id] > 0 {
			return 1.0 / float64(position+1)
		}
	}
	return 0
}

// Aggregate folds per-query metric values into a mean.
func Aggregate(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

// DocRank converts segment-level BM25 candidates into a document ranking. Each
// candidate carries a document ID and a rank position (1 = best). A document's
// rank is the best rank among its segments; ties break by smaller document ID.
func DocRank(candidates []SegmentCandidate) []string {
	type docRank struct {
		id   string
		rank int
	}
	entries := make([]docRank, 0, len(candidates))
	best := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		if existing, ok := best[candidate.DocID]; !ok || candidate.Rank < existing {
			best[candidate.DocID] = candidate.Rank
		}
	}
	for id, rank := range best {
		entries = append(entries, docRank{id: id, rank: rank})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].rank != entries[j].rank {
			return entries[i].rank < entries[j].rank
		}
		return entries[i].id < entries[j].id
	})
	ranked := make([]string, 0, len(entries))
	for _, entry := range entries {
		ranked = append(ranked, entry.id)
	}
	return ranked
}

// SegmentCandidate is one retrieved segment with its 1-based verified rank and
// the corpus document it belongs to.
type SegmentCandidate struct {
	DocID string
	Rank  int
}

func log2(value float64) float64 {
	return math.Log(value) / ln2
}

const ln2 = 0.6931471805599453

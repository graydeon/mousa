// Package beir computes the ranking metrics the Mousa BEIR-subset harness
// reports, the document-level ranking that feeds them, and the validation that
// makes a ranking scorable.
//
// The reported nDCG@10 and Recall@100 follow the reference BEIR evaluation
// protocol, which delegates to pytrec_eval's ndcg_cut and recall measures:
//
//   - a document's gain is its relevance value in the qrels file (graded and
//     linear, not collapsed to 1);
//   - a document at 1-based rank r contributes gain/log2(r+1), for gain > 0;
//   - the ideal gain sequence is the query's positive judgments in descending
//     order, so zero-relevance judgments contribute to neither the numerator nor
//     the ideal denominator;
//   - Recall@100 divides by the number of positive judgments only;
//   - a retrieved document whose ID equals the query ID is removed before
//     scoring, matching BEIR's ignore_identical_ids default.
//
// Pinned references (recorded in every report):
//
//	BEIR       af4a85e7f601a697039c88ab83c1dc88dc975b3f  beir/retrieval/evaluation.py
//	trec_eval  dc0c991c80bae2087de774ed76278e11d9d9f4c6  m_ndcg_cut.c
//
// BinaryNDCGAt10 and MRRAt10 are project-defined and named as such: the binary
// form is retained for comparison with Mousa reports written before the
// reference protocol, and MRR@10 is not part of the BEIR evaluate() result set.
package beir

import (
	"fmt"
	"math"
	"sort"
)

// Reported metric identities. They are recorded in every report so a stored
// number can be traced to the protocol that defined it.
const (
	// ReferenceProtocol names the metric semantics the harness reports.
	ReferenceProtocol = "beir/retrieval/evaluation.py evaluate() (pytrec_eval ndcg_cut.10, recall.100)"
	// BEIRRevision pins the reference evaluator source revision.
	BEIRRevision = "af4a85e7f601a697039c88ab83c1dc88dc975b3f"
	// TrecEvalRevision pins the ndcg_cut definition pytrec_eval implements.
	TrecEvalRevision = "dc0c991c80bae2087de774ed76278e11d9d9f4c6"
)

// ndcgCutoff and recallCutoff are the reported cutoffs.
const (
	ndcgCutoff   = 10
	recallCutoff = 100
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

// QueryMetrics is one query's scores under the harness metrics.
type QueryMetrics struct {
	// NDCGAt10 is the reference graded nDCG@10.
	NDCGAt10 float64
	// RecallAt100 is the reference recall at 100 over positive judgments.
	RecallAt100 float64
	// MRRAt10 is the project-defined reciprocal rank of the first positive
	// judgment within the top 10.
	MRRAt10 float64
	// BinaryNDCGAt10 is the project-defined binary-gain nDCG@10 retained for
	// comparability with reports written before the reference protocol.
	BinaryNDCGAt10 float64
	// IdenticalIDsRemoved counts ranked documents dropped because their ID
	// equals the query ID.
	IdenticalIDsRemoved int
}

// ScoreQuery scores one query under the reference protocol. When
// ignoreIdenticalIDs is set, ranked entries equal to queryID are removed first,
// which is BEIR's default and the only defensible treatment for corpora whose
// query IDs are also corpus IDs.
func ScoreQuery(queryID string, ranked []string, relevant map[string]int, ignoreIdenticalIDs bool) QueryMetrics {
	scored := ranked
	removed := 0
	if ignoreIdenticalIDs {
		scored = make([]string, 0, len(ranked))
		for _, id := range ranked {
			if id == queryID {
				removed++
				continue
			}
			scored = append(scored, id)
		}
	}
	return QueryMetrics{
		NDCGAt10:            NDCGAt10(scored, relevant),
		RecallAt100:         RecallAt100(scored, relevant),
		MRRAt10:             MRRAt10(scored, relevant),
		BinaryNDCGAt10:      BinaryNDCGAt10(scored, relevant),
		IdenticalIDsRemoved: removed,
	}
}

// ValidateRanking rejects a ranking the reported metrics cannot be computed for.
// A duplicated document ID is an invalid run rather than a measurement: the
// reference protocol's run format carries one rank per document, so scoring a
// repeat would depend on a convention the reference does not define.
func ValidateRanking(ranked []string) error {
	seen := make(map[string]struct{}, len(ranked))
	for position, id := range ranked {
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("ranking is not a valid run: document %q appears twice (rank %d)", id, position+1)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// NDCGAt10 computes the reference graded nDCG@10: each document's gain is its
// relevance value, discounted by log2(rank+1), normalized by the ideal ranking
// over the query's positive judgments. Returns 0 when no judgment is positive.
func NDCGAt10(ranked []string, relevant map[string]int) float64 {
	ideal := idealDCGAt10(relevant)
	if ideal == 0 {
		return 0
	}
	return discountedGain(ranked, relevant, func(gain int) float64 { return float64(gain) }) / ideal
}

// BinaryNDCGAt10 computes the project-defined binary-gain nDCG@10 the harness
// reported before the reference protocol: every positive judgment counts as
// gain 1. It is never reported as a BEIR metric.
func BinaryNDCGAt10(ranked []string, relevant map[string]int) float64 {
	ideal := idealBinaryDCGAt10(relevant)
	if ideal == 0 {
		return 0
	}
	return discountedGain(ranked, relevant, func(int) float64 { return 1 }) / ideal
}

// RecallAt100 computes the reference recall at 100: the fraction of the query's
// positive judgments retrieved within the top 100. Zero-relevance judgments are
// not judged-relevant documents and are excluded from the denominator.
func RecallAt100(ranked []string, relevant map[string]int) float64 {
	total := 0
	for _, gain := range relevant {
		if gain > 0 {
			total++
		}
	}
	if total == 0 {
		return 0
	}
	limit := len(ranked)
	if limit > recallCutoff {
		limit = recallCutoff
	}
	found := make(map[string]struct{}, limit)
	for _, id := range ranked[:limit] {
		if relevant[id] > 0 {
			found[id] = struct{}{}
		}
	}
	return float64(len(found)) / float64(total)
}

// MRRAt10 computes the reciprocal rank of the first positive judgment within the
// top 10, or 0 when none appears.
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

// discountedGain sums gain/log2(rank+1) over the top ndcgCutoff ranks.
func discountedGain(ranked []string, relevant map[string]int, gainOf func(int) float64) float64 {
	limit := len(ranked)
	if limit > ndcgCutoff {
		limit = ndcgCutoff
	}
	sum := 0.0
	for position, id := range ranked[:limit] {
		judged := relevant[id]
		if judged <= 0 {
			continue
		}
		sum += gainOf(judged) / log2(float64(position+2))
	}
	return sum
}

// idealDCGAt10 is the perfect graded ranking's DCG@10: the query's positive
// judgments sorted by descending relevance, discounted by rank.
func idealDCGAt10(relevant map[string]int) float64 {
	gains := make([]int, 0, len(relevant))
	for _, gain := range relevant {
		if gain > 0 {
			gains = append(gains, gain)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(gains)))
	if len(gains) > ndcgCutoff {
		gains = gains[:ndcgCutoff]
	}
	ideal := 0.0
	for position, gain := range gains {
		ideal += float64(gain) / log2(float64(position+2))
	}
	return ideal
}

// idealBinaryDCGAt10 is the same ideal under gain 1 per positive judgment.
func idealBinaryDCGAt10(relevant map[string]int) float64 {
	count := 0
	for _, gain := range relevant {
		if gain > 0 {
			count++
		}
	}
	if count > ndcgCutoff {
		count = ndcgCutoff
	}
	ideal := 0.0
	for position := 0; position < count; position++ {
		ideal += 1.0 / log2(float64(position+2))
	}
	return ideal
}

func log2(value float64) float64 {
	return math.Log(value) / ln2
}

const ln2 = 0.6931471805599453

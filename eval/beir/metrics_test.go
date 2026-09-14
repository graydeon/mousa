package beir

import (
	"math"
	"testing"
)

func TestNDCGAt10HandComputed(t *testing.T) {
	d1 := 1.0                // 1/log2(2)
	d2 := 1.0 / math.Log2(3) // 0.6309...
	d3 := 1.0 / math.Log2(4) // 0.5
	idcg2 := d1 + d2         // ideal for two relevant docs
	cases := []struct {
		name     string
		ranked   []string
		relevant map[string]int
		want     float64
	}{
		{
			name:     "perfect ranking of two relevant docs",
			ranked:   []string{"a", "b", "c"},
			relevant: map[string]int{"a": 1, "b": 1},
			want:     1.0,
		},
		{
			name:     "one relevant doc at rank 2",
			ranked:   []string{"x", "a", "y"},
			relevant: map[string]int{"a": 1},
			want:     d2 / d1,
		},
		{
			name:     "rank 2 gain against ideal rank1+rank2",
			ranked:   []string{"x", "a", "y"},
			relevant: map[string]int{"a": 1, "b": 1},
			want:     d2 / idcg2,
		},
		{
			name:     "rank 3 gain against ideal rank1+rank2",
			ranked:   []string{"x", "y", "a"},
			relevant: map[string]int{"a": 1, "b": 1},
			want:     d3 / idcg2,
		},
		{
			name:     "rank 4 gain discounts by 1/log2(5)",
			ranked:   []string{"w", "x", "y", "a"},
			relevant: map[string]int{"a": 1},
			want:     (1.0 / math.Log2(5)) / d1,
		},
		{
			name:     "gain beyond rank 10 is ignored",
			ranked:   []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "a"},
			relevant: map[string]int{"a": 1},
			want:     0,
		},
		{
			name:     "ten ranked hits against eleven relevant docs is perfect",
			ranked:   []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"},
			relevant: map[string]int{"a": 1, "b": 1, "c": 1, "d": 1, "e": 1, "f": 1, "g": 1, "h": 1, "i": 1, "j": 1, "k": 1, "l": 1},
			want:     1.0,
		},
		{
			name:     "no relevant judgments scores zero",
			ranked:   []string{"a"},
			relevant: map[string]int{},
			want:     0,
		},
		{
			name:     "zero relevant docs in ranking scores zero",
			ranked:   []string{"x", "y"},
			relevant: map[string]int{"a": 1},
			want:     0,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := NDCGAt10(test.ranked, test.relevant); !approx(got, test.want) {
				t.Fatalf("NDCGAt10 = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRecallAt100HandComputed(t *testing.T) {
	ranked := []string{"a", "x", "b"}
	relevant := map[string]int{"a": 1, "b": 1, "c": 1}
	if got, want := RecallAt100(ranked, relevant), 2.0/3.0; !approx(got, want) {
		t.Fatalf("RecallAt100 = %v, want %v", got, want)
	}
	beyond := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "a"}
	if got := RecallAt100(beyond, map[string]int{"a": 1}); !approx(got, 1.0) {
		t.Fatalf("rank-12 relevant doc should count within Recall@100, got %v", got)
	}
	if got := RecallAt100(nil, map[string]int{"a": 1}); got != 0 {
		t.Fatalf("empty ranking should score 0, got %v", got)
	}
}

func TestMRRAt10HandComputed(t *testing.T) {
	if got := MRRAt10([]string{"x", "y", "a"}, map[string]int{"a": 1}); !approx(got, 1.0/3.0) {
		t.Fatalf("MRRAt10 = %v, want %v", got, 1.0/3.0)
	}
	if got := MRRAt10([]string{"a"}, map[string]int{"a": 1}); got != 1.0 {
		t.Fatalf("MRRAt10 = %v, want 1", got)
	}
	beyond := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "a"}
	if got := MRRAt10(beyond, map[string]int{"a": 1}); got != 0 {
		t.Fatalf("rank-11 hit must not count for MRR@10, got %v", got)
	}
}

func TestDocRankBestSegmentPerDocument(t *testing.T) {
	candidates := []SegmentCandidate{
		{DocID: "doc-b", Rank: 1},
		{DocID: "doc-a", Rank: 2},
		{DocID: "doc-b", Rank: 5},
		{DocID: "doc-c", Rank: 2},
	}
	ranked := DocRank(candidates)
	want := []string{"doc-b", "doc-a", "doc-c"}
	if len(ranked) != len(want) {
		t.Fatalf("DocRank = %v, want %v", ranked, want)
	}
	for index := range want {
		if ranked[index] != want[index] {
			t.Fatalf("DocRank = %v, want %v", ranked, want)
		}
	}
}

func TestAggregateMean(t *testing.T) {
	if got := Aggregate([]float64{1, 2, 3, 4}); !approx(got, 2.5) {
		t.Fatalf("Aggregate = %v, want 2.5", got)
	}
	if got := Aggregate(nil); got != 0 {
		t.Fatalf("Aggregate(nil) = %v, want 0", got)
	}
}

func approx(got, want float64) bool {
	const epsilon = 1e-9
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff < epsilon
}

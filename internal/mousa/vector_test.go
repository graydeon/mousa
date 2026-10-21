package mousa

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestCosineFiniteScaleAndDirection(t *testing.T) {
	for _, tc := range []struct {
		a, b []float64
		want float64
	}{
		{[]float64{1, 0}, []float64{1, 0}, 1},
		{[]float64{1, 0}, []float64{0, 1}, 0},
		{[]float64{1, 0}, []float64{-1, 0}, -1},
		{[]float64{1, 1}, []float64{1, 0}, 1 / math.Sqrt2},
		{[]float64{math.MaxFloat64, math.MaxFloat64}, []float64{math.SmallestNonzeroFloat64, math.SmallestNonzeroFloat64}, 1},
	} {
		got, err := CosineSimilarity(tc.a, tc.b)
		if err != nil || math.Abs(got-tc.want) > 1e-14 {
			t.Fatalf("cosine(%v,%v)=%v,%v; want %v", tc.a, tc.b, got, err, tc.want)
		}
	}
	for _, bad := range [][]float64{nil, {0, 0}, {math.NaN(), 1}, {math.Inf(1), 1}, {1}} {
		if _, err := CosineSimilarity([]float64{1, 0}, bad); err == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}

func TestVectorStrictImportAndQueryBoundaries(t *testing.T) {
	space := VectorSpace{Schema: VectorSpaceSchema, Provider: "synthetic", Model: "axes", Version: "1", Dimensions: 2, Metric: "cosine"}
	input := VectorImport{Schema: VectorImportSchema, Space: space, Embeddings: []SuppliedEmbedding{{
		EmbeddingInput: EmbeddingInput{SourceID: SourceID{1}, SegmentID: SegmentID{2}, RepresentationID: RepresentationID{3}, RepresentationSHA256: SHA256{4}, ContentSHA256: SHA256{5}},
		Values:         []float64{1, 0},
	}}}
	data, _ := json.Marshal(input)
	if _, err := DecodeVectorImport(data); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"duplicate key":   strings.Replace(string(data), `"dimensions":2`, `"dimensions":2,"dimensions":2`, 1),
		"case alias":      strings.Replace(string(data), `"model"`, `"MODEL"`, 1),
		"unknown":         strings.Replace(string(data), `"model"`, `"extra"`, 1),
		"trailing":        string(data) + `{}`,
		"dimension":       strings.Replace(string(data), `"dimensions":2`, `"dimensions":3`, 1),
		"null coordinate": strings.Replace(string(data), `[1,0]`, `[1,null]`, 1),
		"zero":            strings.Replace(string(data), `[1,0]`, `[0,0]`, 1),
		"nan":             strings.Replace(string(data), `[1,0]`, `[NaN,0]`, 1),
		"infinity":        strings.Replace(string(data), `[1,0]`, `[1e999,0]`, 1),
		"null array":      strings.Replace(string(data), `[1,0]`, `null`, 1),
		"oversized":       strings.Repeat(" ", MaxVectorInputBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeVectorImport([]byte(text)); err == nil {
				t.Fatal("invalid import accepted")
			}
		})
	}
	input.Embeddings = append(input.Embeddings, input.Embeddings[0])
	data, _ = json.Marshal(input)
	if _, err := DecodeVectorImport(data); err == nil {
		t.Fatal("duplicate segment accepted")
	}
	for _, values := range []string{`[1,null]`, `[0,0]`, `[NaN]`, `[1e999]`, `[]`, `null`, `["1"]`} {
		if _, err := DecodeVectorQuery([]byte(`{"schema":"mousa.vector_query.v1","values":` + values + `}`)); err == nil {
			t.Fatalf("accepted query %s", values)
		}
	}
	boundary := make([]float64, MaxVectorDimensions)
	boundary[0] = 1
	if err := ValidateVector(boundary, MaxVectorDimensions); err != nil {
		t.Fatal(err)
	}
	if err := ValidateVector(append(boundary, 0), MaxVectorDimensions+1); err == nil {
		t.Fatal("dimension bound exceeded")
	}
	first, _ := space.Identity()
	space.Model = "another-model"
	second, _ := space.Identity()
	if first == second {
		t.Fatal("equal dimensions mixed model spaces")
	}
}

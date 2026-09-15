package beir

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Regression coverage for indexed-corpus reuse and query accounting.

// TestChangedCorpusReuseRefused: reusing an indexed store whose corpus
// text changed under the same dataset name and document count must fail
// up-front, not succeed and fail (or mis-rank) later at search time.
func TestChangedCorpusReuseRefused(t *testing.T) {
	dir, ingested := ingestFixture(t,
		map[string]string{"d1": "cat sat", "d2": "dog bark"},
		map[string]string{"q": "cat"},
		map[string]map[string]int{"q": {"d1": 1}})
	storePath := ingested.Path
	dataset, err := LoadDataset("fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	ingested.Close()
	dataset.Corpus["d1"] = "completely replaced text"
	reused, err := OpenExisting(context.Background(), dataset, storePath)
	if err == nil {
		reused.Close()
		t.Fatal("OpenExisting accepted changed corpus content with the same name and count")
	}
}

// TestUnchangedCorpusReuseAccepted: the correspondence check must accept
// the exact content that indexed the store.
func TestUnchangedCorpusReuseAccepted(t *testing.T) {
	dir, ingested := ingestFixture(t,
		map[string]string{"d1": "cat sat", "d2": "dog bark"},
		map[string]string{"q": "cat"},
		map[string]map[string]int{"q": {"d1": 1}})
	storePath := ingested.Path
	ingested.Close()
	dataset, err := LoadDataset("fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	reused, err := OpenExisting(context.Background(), dataset, storePath)
	if err != nil {
		t.Fatalf("OpenExisting refused unchanged content: %v", err)
	}
	reused.Close()
}

// TestJournalAccountingMatchesResult: the journal callback must observe
// the same accepted/considered accounting the returned result carries; the
// Journaled counts must equal the returned counts.
func TestJournalAccountingMatchesResult(t *testing.T) {
	dir, ingested := ingestFixture(t,
		map[string]string{"d1": "cat sat", "d2": "dog bark"},
		map[string]string{"q": "cat"},
		map[string]map[string]int{"q": {"d1": 1}})
	defer ingested.Close()
	dataset, err := LoadDataset("fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	var recorded SearchResult
	got, err := SearchAll(context.Background(), ingested, dataset,
		RunConfig{Mode: ModeVerified, Policy: PolicyOriginal, Limit: 100}, nil,
		func(r SearchResult) { recorded = r })
	if err != nil {
		t.Fatal(err)
	}
	if recorded.AcceptedSegments != got[0].AcceptedSegments ||
		recorded.ConsideredSegments != got[0].ConsideredSegments {
		t.Fatalf("journal recorded %d/%d but the run returned %d/%d",
			recorded.AcceptedSegments, recorded.ConsideredSegments,
			got[0].AcceptedSegments, got[0].ConsideredSegments)
	}
}

// TestNamespaceBindsContentIdentity: equal names and counts with different
// content digests must produce different run namespaces.
func TestNamespaceBindsContentIdentity(t *testing.T) {
	base := RunConfig{Mode: ModeVerified, Limit: 100, Dataset: "x", Corpus: 2, Judged: 1}
	other := base
	other.Content = ContentIdentity{CorpusSHA256: "aa", QueriesSHA256: "bb", QrelsSHA256: "cc"}
	if base.Namespace() == other.Namespace() {
		t.Fatal("namespace ignores content identity")
	}
	if base.Content.empty() != true {
		t.Fatal("empty content identity misdetected")
	}
}

// TestLoadDatasetDigestsInputs: the loader records the exact file digests a run
// measured, so resume and reuse can bind to bytes rather than names.
func TestLoadDatasetDigestsInputs(t *testing.T) {
	dir, ingested := ingestFixture(t,
		map[string]string{"d1": "cat sat"},
		map[string]string{"q": "cat"},
		map[string]map[string]int{"q": {"d1": 1}})
	defer ingested.Close()
	dataset, err := LoadDataset("fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	if dataset.Content.CorpusSHA256 == "" || dataset.Content.QueriesSHA256 == "" || dataset.Content.QrelsSHA256 == "" {
		t.Fatalf("loader left content digests empty: %+v", dataset.Content)
	}
	again, err := LoadDataset("fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Content != dataset.Content {
		t.Fatalf("digests are not deterministic across loads: %+v vs %+v", again.Content, dataset.Content)
	}
}

// TestFullRequestPathLatencyCoversPreprocessing: the measured latency includes
// preprocessing, and the deadline applies to the whole request path. Under
// drop-floor the probe/reduction work happens inside the timed window, so the
// full-path latency must be at least the search component.
func TestFullRequestPathLatencyCoversPreprocessing(t *testing.T) {
	dir, ingested := ingestFixture(t,
		map[string]string{"d1": "the cat sat quietly on the mat", "d2": "the dog barked loudly", "d3": "zebra grass", "d4": "zebra stripes"},
		map[string]string{"q": "cat quietly zebra"},
		map[string]map[string]int{"q": {"d1": 1}})
	defer ingested.Close()
	dataset, err := LoadDataset("fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	results, err := SearchAll(context.Background(), ingested, dataset,
		RunConfig{Mode: ModeVerified, Policy: PolicyDropFloor, Limit: 10}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	result := results[0]
	if result.LatencyMicros < result.SearchMicros {
		t.Fatalf("full path %d us is less than the search component %d us",
			result.LatencyMicros, result.SearchMicros)
	}
	if result.LatencyMicros < result.PreprocessMicros {
		t.Fatalf("full path %d us is less than the preprocessing component %d us",
			result.LatencyMicros, result.PreprocessMicros)
	}
}

// TestRequestDeadlineCoversPreprocessing: a deadline that expires during
// preprocessing must abort the query with a deadline error, proving the
// timeout starts at request start, not at the store call.
func TestRequestDeadlineCoversPreprocessing(t *testing.T) {
	dir, ingested := ingestFixture(t,
		map[string]string{"d1": "cat sat", "d2": "dog bark", "d3": "zebra grass", "d4": "zebra stripes"},
		map[string]string{"q": "cat zebra"},
		map[string]map[string]int{"q": {"d1": 1}})
	defer ingested.Close()
	dataset, err := LoadDataset("fixture", dir)
	if err != nil {
		t.Fatal(err)
	}
	oldTimeout := QueryTimeout
	QueryTimeout = time.Nanosecond // expires during preprocessing
	defer func() { QueryTimeout = oldTimeout }()
	_, err = SearchAll(context.Background(), ingested, dataset,
		RunConfig{Mode: ModeVerified, Policy: PolicyDropFloor, Limit: 10}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("expired request deadline must abort the query, got %v", err)
	}
}

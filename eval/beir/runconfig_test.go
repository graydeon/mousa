package beir

import (
	"testing"
)

// TestRunConfigNamespace separates experiments and preserves retries: distinct
// run configurations must never share a request namespace on a reused store,
// while identical configurations must derive the identical namespace so retries
// inside one run keep the same request identity.
func TestRunConfigNamespace(t *testing.T) {
	base := RunConfig{Mode: ModeEnforced, Limit: 100, Budget: 1 << 20, Dataset: "scifact", Corpus: 100, Judged: 50}
	namespace := base.Namespace()
	if namespace == "" {
		t.Fatal("namespace is empty")
	}
	if again := base.Namespace(); again != namespace {
		t.Fatalf("namespace changed for identical config: %q then %q", namespace, again)
	}
	for _, changed := range []RunConfig{
		{Mode: ModeTraced, Limit: 100, Budget: 1 << 20, Dataset: "scifact", Corpus: 100, Judged: 50},
		{Mode: ModeEnforced, Limit: 10, Budget: 1 << 20, Dataset: "scifact", Corpus: 100, Judged: 50},
		{Mode: ModeEnforced, Limit: 100, Budget: 512, Dataset: "scifact", Corpus: 100, Judged: 50},
		{Mode: ModeEnforced, Limit: 100, Budget: 1 << 20, Dataset: "nfcorpus", Corpus: 100, Judged: 50},
		{Mode: ModeEnforced, Limit: 100, Budget: 1 << 20, Dataset: "scifact", Corpus: 200, Judged: 50},
		{Mode: ModeEnforced, Limit: 100, Budget: 1 << 20, Dataset: "scifact", Corpus: 100, Judged: 51},
	} {
		if other := changed.Namespace(); other == namespace {
			t.Fatalf("config %+v shares namespace %q with base", changed, namespace)
		}
	}
}

// TestBuildExpressionDedupFoldsRepeats pins the folding behavior: the original
// policy keeps every instance (published baseline evaluation protocol) and the dedup
// policy folds repeated terms to one instance. Folding is a measured policy
// change, so both paths must stay available and distinct.
func TestBuildExpressionPolicy(t *testing.T) {
	for _, test := range []struct {
		query, wantOriginal, wantDedup string
	}{
		{"Alpha beta ALPHA alpha", `"alpha" OR "beta" OR "alpha" OR "alpha"`, `"alpha" OR "beta"`},
		{"alpha beta", `"alpha" OR "beta"`, `"alpha" OR "beta"`},
	} {
		got, err := BuildExpressionWithPolicy(test.query, PolicyOriginal)
		if err != nil || got != test.wantOriginal {
			t.Fatalf("original(%q) = %q, %v; want %q", test.query, got, err, test.wantOriginal)
		}
		got, err = BuildExpressionWithPolicy(test.query, PolicyDedup)
		if err != nil || got != test.wantDedup {
			t.Fatalf("dedup(%q) = %q, %v; want %q", test.query, got, err, test.wantDedup)
		}
	}
	if _, err := BuildExpression("..."); err == nil {
		t.Fatal("punctuation-only query accepted")
	}
}

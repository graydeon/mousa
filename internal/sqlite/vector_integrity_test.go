package sqlite

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestVectorImportRejectsForeignAndUnknownPins(t *testing.T) {
	ctx := context.Background()
	store, source, input := vectorFixture(t)
	defer store.Close()
	original := input.Embeddings[0]
	for name, mutate := range map[string]func(*mousa.SuppliedEmbedding){
		"foreign source":            func(e *mousa.SuppliedEmbedding) { e.SourceID = mousa.SourceID{42} },
		"unknown segment":           func(e *mousa.SuppliedEmbedding) { e.SegmentID = mousa.SegmentID{42} },
		"foreign representation":    func(e *mousa.SuppliedEmbedding) { e.RepresentationID = mousa.RepresentationID{42} },
		"wrong normalized revision": func(e *mousa.SuppliedEmbedding) { e.RepresentationSHA256 = testDigest("other revision") },
	} {
		t.Run(name, func(t *testing.T) {
			input.Embeddings[0] = original
			mutate(&input.Embeddings[0])
			if _, err := store.ImportEmbeddings(ctx, source.ID, input); !IsCode(err, CodeInvalidRecord) {
				t.Fatalf("foreign pins: %v", err)
			}
		})
	}
	if _, err := store.EvaluateAndTraceVector(ctx, testEvaluationRequest(t, source.ID, "unknown-space"), mousa.SHA256{42}, []float64{1, 0}, 100, mousa.PackingOriginal); !IsCode(err, CodeNotFound) {
		t.Fatalf("unknown space: %v", err)
	}
	input.Embeddings[0] = original
	first, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Space.Version = "2"
	input.Embeddings = input.Embeddings[:1]
	input.Embeddings[0].Values = []float64{0, 1}
	second, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("distinct versions mixed spaces")
	}
	result, err := store.EvaluateAndTraceVector(ctx, testEvaluationRequest(t, source.ID, "separate-space"), second, []float64{1, 0}, 100, mousa.PackingOriginal)
	if err != nil || result.Trail.Vector.Indexed != 1 || result.Trail.Vector.Eligible != 2 || result.Trail.Vector.Candidates[0].Cosine != 0 {
		t.Fatalf("space coverage: %+v %v", result, err)
	}
}

func TestVectorStoredValuesAndCanonicalSourceCannotBeRewritten(t *testing.T) {
	for _, kind := range []string{"values", "source"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store, source, input := vectorFixture(t)
			if _, err := store.ImportEmbeddings(ctx, source.ID, input); err != nil {
				t.Fatal(err)
			}
			embedding, err := mousa.NewEmbedding(input.Space, input.Embeddings[0])
			if err != nil {
				t.Fatal(err)
			}
			originalID := embedding.ID
			if kind == "values" {
				embedding.Values = []float64{0, 1}
			} else {
				foreignID, err := mousa.NewSourceID("example", "foreign-vector-source")
				if err != nil {
					t.Fatal(err)
				}
				foreign := mousa.Source{Schema: mousa.SourceSchema, ID: foreignID, Namespace: "example", ExternalSourceID: "foreign-vector-source"}
				if err := store.PutSource(ctx, foreign); err != nil {
					t.Fatal(err)
				}
				embedding.SourceID = foreign.ID
				embedding, err = mousa.NewEmbedding(input.Space, embedding.SuppliedEmbedding)
				if err != nil {
					t.Fatal(err)
				}
			}
			encoded, err := json.Marshal(embedding)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.Exec(`UPDATE embeddings SET id=?,source_id=?,record_json=? WHERE id=?`, embedding.ID[:], embedding.SourceID[:], encoded, originalID[:]); err != nil {
				t.Fatal(err)
			}
			if _, err := getEmbedding(ctx, store.db, embedding.ID); !IsCode(err, CodeIntegrity) {
				t.Fatalf("corrupt %s accepted: %v", kind, err)
			}
			var after []byte
			if err := store.db.QueryRow(`SELECT record_json FROM embeddings WHERE id=?`, embedding.ID[:]).Scan(&after); err != nil || string(after) != string(encoded) {
				t.Fatalf("corrupt read repaired bytes: %v", err)
			}
			path := store.path
			store.Close()
			if reopened, err := OpenReadOnly(ctx, path); !IsCode(err, CodeIntegrity) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("corrupt reopen: %v", err)
			}
		})
	}
}

func TestVectorNormalizedTextBoundary(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	seedEvaluationFixture(t, store)
	for _, size := range []int{mousa.MaxVectorTextBytes, mousa.MaxVectorTextBytes + 1} {
		source, rep, normalized := prepareLocalRevision(t, store, "bounded", strings.Repeat("x", size))
		if _, err := store.ActivateLocalItem(ctx, source.ID, "bounded", rep.ID, normalized); err != nil {
			t.Fatal(err)
		}
		result, err := store.ExportEmbeddingInputs(ctx, testEvaluationRequest(t, source.ID, "text-boundary-"+strconv.Itoa(size)))
		if size == mousa.MaxVectorTextBytes {
			if err != nil || len(result.Passages) != 64 {
				t.Fatalf("exact text boundary: %d %v", len(result.Passages), err)
			}
		} else if !IsCode(err, CodeResourceLimit) {
			t.Fatalf("excess text boundary: %v", err)
		}
	}
}

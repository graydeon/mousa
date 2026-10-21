package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestHybridUnionArithmeticAndPacking(t *testing.T) {
	ctx := context.Background()
	store, source, input := vectorFixture(t)
	defer store.Close()
	seedLocalItem(t, store, source, "c", "Drain sockets.")
	// a overlaps, b is vector-only, c is lexical-only. Vector order b,a;
	// lexical order c,a. Overlap a wins fusion despite ranking second twice.
	input.Embeddings[0].Values = []float64{0, 1}
	input.Embeddings[1].Values = []float64{1, 0}
	id, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.EvaluateAndTraceHybrid(ctx, testEvaluationRequest(t, source.ID, "hybrid"), "Drain", "original", id, []float64{1, 0}, 40, mousa.PackingOriginal)
	if err != nil {
		t.Fatal(err)
	}
	h := got.Trail.Hybrid
	if h.Eligible != 3 || h.Indexed != 2 || len(h.Candidates) != 3 || got.Passages[0].Item != "a" || h.Candidates[0].Lexical.Rank != 2 || h.Candidates[0].Vector.Rank != 2 || math.Abs(h.Candidates[0].RRF-0.03225806451612903) > 1e-16 {
		t.Fatalf("fusion: %+v passages=%+v", h, got.Passages)
	}
	if !got.Trail.Candidates[0].Selected || got.Trail.UsedBytes != 39 {
		t.Fatalf("fused packing: %+v", got.Trail)
	}
	for _, c := range h.Candidates[1:] {
		if math.Abs(c.RRF-0.01639344262295082) > 1e-16 || (c.Lexical == nil) == (c.Vector == nil) {
			t.Fatalf("single branch: %+v", c)
		}
	}
	if h.Candidates[1].SegmentID.String() >= h.Candidates[2].SegmentID.String() {
		t.Fatal("unstable canonical tie")
	}
	history, err := store.GetSourceTrail(ctx, got.Trail.ID)
	if err != nil || history.Hybrid == nil {
		t.Fatalf("readback: %+v %v", history, err)
	}
}

func TestHybridCoverageTermsAndHistoricalPins(t *testing.T) {
	ctx := context.Background()
	store, source, input := vectorFixture(t)
	defer store.Close()
	input.Embeddings = input.Embeddings[:1]
	input.Embeddings[0].Values = []float64{-1, 0}
	id, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	query := func(name, text, policy string) TracedVectorResult {
		t.Helper()
		got, err := store.EvaluateAndTraceHybrid(ctx, testEvaluationRequest(t, source.ID, name), text, policy, id, []float64{1, 0}, 100, mousa.PackingExactV1)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	original := query("original", "Drain Drain", "original")
	dedup := query("dedup", "Drain Drain", "dedup")
	if len(original.Trail.Hybrid.Terms) != 2 || len(dedup.Trail.Hybrid.Terms) != 1 || original.Trail.Expression == dedup.Trail.Expression || original.Trail.Hybrid.Indexed != 1 || original.Trail.Hybrid.Eligible != 2 {
		t.Fatalf("term policy or coverage: %+v %+v", original.Trail, dedup.Trail)
	}
	noMatch := query("no-match", "nonmatching", "original")
	if len(noMatch.Passages) != 1 || noMatch.Trail.Hybrid.Candidates[0].Lexical != nil || noMatch.Trail.Hybrid.Candidates[0].Vector.Cosine != -1 {
		t.Fatalf("negative vector suppressed: %+v", noMatch)
	}
	saved, err := mousa.EncodeSourceTrail(original.Trail)
	if err != nil {
		t.Fatal(err)
	}
	seedLocalItem(t, store, source, "a", "Drain replacement.")
	zero := query("zero-coverage", "Drain", "original")
	if zero.Trail.Hybrid.Indexed != 0 || zero.Trail.Hybrid.Eligible != 2 || len(zero.Passages) != 1 || zero.Passages[0].Text != "Drain replacement." {
		t.Fatalf("zero coverage/stale text: %+v", zero)
	}
	if _, err := store.DeleteLocalItem(ctx, source.ID, "a"); err != nil {
		t.Fatal(err)
	}
	deleted := query("deleted", "Drain", "original")
	if deleted.Trail.Hybrid.Eligible != 1 || len(deleted.Passages) != 0 {
		t.Fatalf("deleted passage: %+v", deleted)
	}
	view, err := store.InspectSourceTrail(ctx, testEvaluationRequest(t, source.ID, "old-history"), original.Trail.ID)
	if err != nil || view.Historical.Hybrid.QueryVectorID != original.Trail.Hybrid.QueryVectorID || view.Historical.Candidates[0].IndexedNow {
		t.Fatalf("historical: %+v %v", view, err)
	}
	path := store.path
	store.Close()
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	history, err := reopened.GetSourceTrail(ctx, original.Trail.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := mousa.EncodeSourceTrail(history)
	if !bytes.Equal(saved, encoded) {
		t.Fatal("historical producing bytes changed")
	}
	withdrawVerifiedSource(t, reopened, source.ID, "hybrid-withdraw", 3)
	denied, err := reopened.EvaluateAndTraceHybrid(ctx, testEvaluationRequest(t, source.ID, "withdrawn"), "Drain", "original", id, []float64{1, 0}, 100, mousa.PackingOriginal)
	if err != nil || denied.Decision.Outcome != mousa.PolicyOutcomeDeny || denied.Trail.Hybrid.Eligible != 0 || denied.Trail.Hybrid.Indexed != 0 || len(denied.Passages) != 0 {
		t.Fatalf("withdrawal: %+v %v", denied, err)
	}
	hidden, err := reopened.InspectSourceTrail(ctx, testEvaluationRequest(t, source.ID, "hidden-history"), original.Trail.ID)
	if err != nil || hidden.Historical != nil {
		t.Fatalf("withdrawn history: %+v %v", hidden, err)
	}
}

func TestHybridExactDuplicatesAndBudgetSkip(t *testing.T) {
	ctx := context.Background()
	store, source, input := vectorFixture(t)
	defer store.Close()
	seedLocalItem(t, store, source, "a", "same")
	seedLocalItem(t, store, source, "b", "same")
	seedLocalItem(t, store, source, "c", "tail")
	seedLocalItem(t, store, source, "oversized", strings.Repeat("large ", 20))
	exported, err := store.ExportEmbeddingInputs(ctx, testEvaluationRequest(t, source.ID, "duplicates"))
	if err != nil {
		t.Fatal(err)
	}
	input.Embeddings = nil
	for _, p := range exported.Passages {
		values := []float64{1, 0}
		if p.Item == "oversized" {
			values = []float64{2, 0}
		}
		if p.Item == "c" {
			values = []float64{0, 1}
		}
		input.Embeddings = append(input.Embeddings, mousa.SuppliedEmbedding{EmbeddingInput: p.EmbeddingInput, Values: values})
	}
	id, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	// The oversized lexical hit leads fusion but cannot seed the retained set.
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		got, err := store.EvaluateAndTraceHybrid(ctx, testEvaluationRequest(t, source.ID, packing), "large", "original", id, []float64{1, 0}, 8, packing)
		if err != nil {
			t.Fatal(err)
		}
		if got.Passages[0].Item != "oversized" || got.Trail.Candidates[0].Selected {
			t.Fatalf("budget skip: %+v", got)
		}
		selected := []string{}
		duplicates := 0
		first := ""
		for i, c := range got.Trail.Candidates {
			if c.Selected {
				selected = append(selected, got.Passages[i].Item)
				if got.Passages[i].Text == "same" {
					first = c.SegmentID.String()
				}
			}
			if c.Omission == "duplicate" {
				duplicates++
				if c.DuplicateOf != first {
					t.Fatal("not first retained duplicate")
				}
			}
		}
		if packing == mousa.PackingOriginal && (len(selected) != 2 || got.Trail.UsedBytes != 8 || duplicates != 0) {
			t.Fatalf("original: %v %+v", selected, got.Trail)
		}
		if packing == mousa.PackingExactV1 && (len(selected) != 2 || selected[1] != "c" || duplicates != 1 || got.Trail.UsedBytes != 8) {
			t.Fatalf("exact: %v %+v", selected, got.Trail)
		}
	}
}

func TestHybridInvalidRequestsAndCorruption(t *testing.T) {
	ctx := context.Background()
	store, source, input := vectorFixture(t)
	defer store.Close()
	id, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, text, policy string
		space              mousa.SHA256
		values             []float64
		code               Code
	}{
		{"unknown", "Drain", "original", mousa.SHA256{42}, []float64{1, 0}, CodeNotFound},
		{"dimensions", "Drain", "original", id, []float64{1}, CodeInvalidQuery},
		{"unpreparable", "!!!", "original", id, []float64{1, 0}, CodeInvalidQuery},
		{"policy", "Drain", "unsupported", id, []float64{1, 0}, CodeInvalidQuery},
		{"oversized", strings.Repeat("x", 4096), "original", id, []float64{1, 0}, CodeInvalidQuery},
	} {
		_, err := store.EvaluateAndTraceHybrid(ctx, testEvaluationRequest(t, source.ID, test.name), test.text, test.policy, test.space, test.values, 100, mousa.PackingOriginal)
		if !IsCode(err, test.code) {
			t.Fatalf("%s: %v", test.name, err)
		}
	}
	got, err := store.EvaluateAndTraceHybrid(ctx, testEvaluationRequest(t, source.ID, "producing"), "Drain", "original", id, []float64{1, 0}, 100, mousa.PackingOriginal)
	if err != nil {
		t.Fatal(err)
	}
	// Rebind the hash and projections so the producing-record check, not a stale
	// trail identity, must detect the altered cosine.
	got.Trail.Hybrid.Candidates[0].Vector.Cosine = 0.5
	got.Trail.ID, err = mousa.NewSourceTrailID(got.Trail)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertSourceTrailForTest(ctx, store, got.Trail); !IsCode(err, CodeIntegrity) {
		t.Fatalf("false producing cosine: %v", err)
	}
	e, err := mousa.NewEmbedding(input.Space, input.Embeddings[0])
	if err != nil {
		t.Fatal(err)
	}
	var before []byte
	if err := store.db.QueryRow(`SELECT record_json FROM embeddings WHERE id=?`, e.ID[:]).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(before, &record); err != nil {
		t.Fatal(err)
	}
	record["values"] = []float64{0, 1}
	damaged, _ := json.Marshal(record)
	if _, err := store.db.Exec(`UPDATE embeddings SET record_json=? WHERE id=?`, damaged, e.ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EvaluateAndTraceHybrid(ctx, testEvaluationRequest(t, source.ID, "damaged"), "Drain", "original", id, []float64{1, 0}, 100, mousa.PackingOriginal); !IsCode(err, CodeIntegrity) {
		t.Fatalf("damaged vector: %v", err)
	}
	var after []byte
	if err := store.db.QueryRow(`SELECT record_json FROM embeddings WHERE id=?`, e.ID[:]).Scan(&after); err != nil || !bytes.Equal(after, damaged) {
		t.Fatalf("read repaired corruption: %v", err)
	}
}

func insertSourceTrailForTest(ctx context.Context, store *Store, trail mousa.SourceTrail) error {
	return store.writeImmediate(ctx, "test producing verification", func(conn *sql.Conn) error {
		if err := insertSourceTrail(ctx, conn, trail); err != nil {
			return err
		}
		_, err := getSourceTrail(ctx, conn, trail.ID)
		return err
	})
}

func TestHybridWriterOrderingAndRollback(t *testing.T) {
	ctx := context.Background()
	store, source, input := vectorFixture(t)
	defer store.Close()
	id, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := connect(ctx, store.path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	conn, err := writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE; UPDATE local_items SET active=0; DELETE FROM segment_lexical_fts; DELETE FROM segment_lexical_rows;`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	started := make(chan struct{})
	request := testEvaluationRequest(t, source.ID, "ordered-hybrid")
	go func() {
		close(started)
		got, err := store.EvaluateAndTraceHybrid(ctx, request, "Drain", "original", id, []float64{1, 0}, 100, mousa.PackingOriginal)
		if err == nil && (got.Trail.Hybrid.Eligible != 0 || got.Trail.Hybrid.Indexed != 0 || len(got.Passages) != 0) {
			err = fmt.Errorf("mixed snapshot: %+v", got)
		}
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("passed active writer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Restore real branch candidates before forcing persistence failure.
	seedLocalItem(t, store, source, "a", "Drain sockets before stopping the host.")
	var decisions, trails int
	if err := store.db.QueryRow(`SELECT count(*) FROM policy_decisions`).Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM source_trails`).Scan(&trails); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TEMP TRIGGER fail_hybrid BEFORE INSERT ON source_trails BEGIN SELECT RAISE(ABORT,'hybrid failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EvaluateAndTraceHybrid(ctx, testEvaluationRequest(t, source.ID, "rollback-hybrid"), "Drain", "original", id, []float64{1, 0}, 100, mousa.PackingOriginal); err == nil {
		t.Fatal("failed trail committed")
	}
	var after int
	if err := store.db.QueryRow(`SELECT count(*) FROM policy_decisions`).Scan(&after); err != nil || after != decisions {
		t.Fatalf("partial decision: %d %v", after, err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM source_trails`).Scan(&after); err != nil || after != trails {
		t.Fatalf("partial trail: %d %v", after, err)
	}
}

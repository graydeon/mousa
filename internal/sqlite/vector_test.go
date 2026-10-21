package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

func vectorSpace() mousa.VectorSpace {
	return mousa.VectorSpace{Schema: mousa.VectorSpaceSchema, Provider: "synthetic", Model: "arithmetic-fixture", Version: "1", Dimensions: 2, Metric: "cosine"}
}

func vectorFixture(t *testing.T) (*Store, mousa.Source, mousa.VectorImport) {
	t.Helper()
	store := openLexicalStore(t)
	seedEvaluationFixture(t, store)
	batch := testPushBatch(t, "vector-fixture", nil)
	if err := store.ApplyIngest(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	seedLocalItem(t, store, batch.Source, "a", "Drain sockets before stopping the host.")
	seedLocalItem(t, store, batch.Source, "b", "Radishes prefer damp soil.")
	inputs, err := store.ExportEmbeddingInputs(context.Background(), testEvaluationRequest(t, batch.Source.ID, "vector-export"))
	if err != nil {
		t.Fatal(err)
	}
	if inputs.Decision.Outcome != mousa.PolicyOutcomeAllow || len(inputs.Passages) != 2 {
		t.Fatalf("inputs: %+v", inputs)
	}
	imported := mousa.VectorImport{Schema: mousa.VectorImportSchema, Space: vectorSpace()}
	for _, p := range inputs.Passages {
		values := []float64{1, 0}
		if p.Item == "b" {
			values = []float64{0, 1}
		}
		imported.Embeddings = append(imported.Embeddings, mousa.SuppliedEmbedding{EmbeddingInput: p.EmbeddingInput, Values: values})
	}
	return store, batch.Source, imported
}

func TestVectorCurrentPinsCoverageAndHistoricalReadback(t *testing.T) {
	ctx := context.Background()
	store, source, input := vectorFixture(t)
	defer store.Close()
	id, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportEmbeddings(ctx, source.ID, input); err != nil {
		t.Fatalf("retry: %v", err)
	}
	query := func(name string) TracedVectorResult {
		t.Helper()
		got, err := store.EvaluateAndTraceVector(ctx, testEvaluationRequest(t, source.ID, name), id, []float64{1, 0}, 40, mousa.PackingOriginal)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	got := query("vector-query")
	if got.Trail.Schema != mousa.SourceTrailSchemaV5 || got.Trail.Expression != "" || got.Trail.Vector.Eligible != 2 || got.Trail.Vector.Indexed != 2 || got.Passages[0].Item != "a" || got.Trail.Vector.Candidates[0].Cosine != 1 || got.Trail.Vector.Candidates[1].Cosine != 0 || got.Trail.UsedBytes != 39 {
		t.Fatalf("selection: %+v", got)
	}
	saved, err := mousa.EncodeSourceTrail(got.Trail)
	if err != nil {
		t.Fatal(err)
	}
	seedLocalItem(t, store, source, "a", "Revised instructions use the new switch.")
	current := query("after-revision")
	if current.Trail.Vector.Eligible != 2 || current.Trail.Vector.Indexed != 1 || current.Passages[0].Item != "b" {
		t.Fatalf("stale selection: %+v", current)
	}
	if _, err := store.ImportEmbeddings(ctx, source.ID, input); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("stale import: %v", err)
	}
	if _, err := store.DeleteLocalItem(ctx, source.ID, "b"); err != nil {
		t.Fatal(err)
	}
	empty := query("after-deletion")
	if empty.Trail.Vector.Eligible != 1 || empty.Trail.Vector.Indexed != 0 || len(empty.Passages) != 0 {
		t.Fatalf("deleted: %+v", empty)
	}
	history, err := store.InspectSourceTrail(ctx, testEvaluationRequest(t, source.ID, "history"), got.Trail.ID)
	if err != nil {
		t.Fatal(err)
	}
	if history.Historical.Vector.QueryVectorID != got.Trail.Vector.QueryVectorID || history.Historical.Candidates[0].IndexedNow {
		t.Fatalf("history changed: %+v", history)
	}
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	old, err := reopened.GetSourceTrail(ctx, got.Trail.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := mousa.EncodeSourceTrail(old)
	if string(encoded) != string(saved) {
		t.Fatal("historical vector trail bytes changed")
	}
	withdrawVerifiedSource(t, reopened, source.ID, "vector-withdraw", 3)
	denied, err := reopened.ExportEmbeddingInputs(ctx, testEvaluationRequest(t, source.ID, "denied-export"))
	if err != nil || denied.Decision.Outcome != mousa.PolicyOutcomeDeny || len(denied.Passages) != 0 {
		t.Fatalf("withdraw export: %+v %v", denied, err)
	}
	deniedQuery, err := reopened.EvaluateAndTraceVector(ctx, testEvaluationRequest(t, source.ID, "denied-query"), id, []float64{1, 0}, 100, mousa.PackingOriginal)
	if err != nil || len(deniedQuery.Passages) != 0 || deniedQuery.Trail.Vector.Eligible != 0 {
		t.Fatalf("withdraw query: %+v %v", deniedQuery, err)
	}
	deniedHistory, err := reopened.InspectSourceTrail(ctx, testEvaluationRequest(t, source.ID, "denied-history"), old.ID)
	if err != nil || deniedHistory.Historical != nil {
		t.Fatalf("withdraw history: %+v %v", deniedHistory, err)
	}
}

func TestVectorImportConflictsAndBatchRollback(t *testing.T) {
	ctx := context.Background()
	store, source, input := vectorFixture(t)
	defer store.Close()
	id, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Embeddings[0].Values = []float64{-1, 0}
	if _, err := store.ImportEmbeddings(ctx, source.ID, input); !IsCode(err, CodeConflict) {
		t.Fatalf("conflict: %v", err)
	}
	input.Space.Version = "2"
	input.Embeddings[1].ContentSHA256 = testDigest("wrong")
	if _, err := store.ImportEmbeddings(ctx, source.ID, input); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("wrong content: %v", err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM vector_spaces`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial space: %d %v", count, err)
	}
	if _, err := store.EvaluateAndTraceVector(ctx, testEvaluationRequest(t, source.ID, "wrong-dimension"), id, []float64{1}, 100, mousa.PackingOriginal); !IsCode(err, CodeInvalidQuery) {
		t.Fatalf("dimension: %v", err)
	}
	inputs, err := store.ExportEmbeddingInputs(ctx, testEvaluationRequest(t, source.ID, "new-pins"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range input.Embeddings {
		input.Embeddings[i].EmbeddingInput = inputs.Passages[i].EmbeddingInput
	}
	// Fail the second row after the first vector and its space have been inserted.
	trigger := fmt.Sprintf(`CREATE TEMP TRIGGER fail_embedding BEFORE INSERT ON embeddings WHEN NEW.segment_id=X'%s' BEGIN SELECT RAISE(ABORT,'vector failure'); END`, input.Embeddings[1].SegmentID.String())
	if _, err := store.db.Exec(trigger); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportEmbeddings(ctx, source.ID, input); err == nil {
		t.Fatal("trigger failure accepted")
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM vector_spaces`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial failed import: %d %v", count, err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM embeddings`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("partial failed vector prefix: %d %v", count, err)
	}
}

func TestVectorTiePackingAndRecordedCorruption(t *testing.T) {
	ctx := context.Background()
	store, source, input := vectorFixture(t)
	defer store.Close()
	seedLocalItem(t, store, source, "c", "Drain sockets before stopping the host.")
	inputs, err := store.ExportEmbeddingInputs(ctx, testEvaluationRequest(t, source.ID, "tie-export"))
	if err != nil {
		t.Fatal(err)
	}
	input.Embeddings = nil
	for _, p := range inputs.Passages {
		input.Embeddings = append(input.Embeddings, mousa.SuppliedEmbedding{EmbeddingInput: p.EmbeddingInput, Values: []float64{1, 0}})
	}
	id, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		result, err := store.EvaluateAndTraceVector(ctx, testEvaluationRequest(t, source.ID, "tie-"+policy), id, []float64{1, 0}, 200, policy)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(result.Trail.Candidates); i++ {
			if result.Trail.Candidates[i-1].SegmentID.String() >= result.Trail.Candidates[i].SegmentID.String() {
				t.Fatal("unstable canonical tie")
			}
		}
		want := uint64(104)
		if policy == mousa.PackingExactV1 {
			want = 65
		}
		if result.Trail.UsedBytes != want {
			t.Fatalf("%s bytes=%d want=%d", policy, result.Trail.UsedBytes, want)
		}
		if policy == mousa.PackingExactV1 {
			altered := result.Trail
			copySelection := *altered.Vector
			copySelection.Candidates = append([]mousa.VectorCandidate(nil), copySelection.Candidates...)
			altered.Vector = &copySelection
			// Keep order structurally valid while falsifying every producing score.
			for i := range altered.Vector.Candidates {
				altered.Vector.Candidates[i].Cosine = 0.5
			}
			altered.ID, err = mousa.NewSourceTrailID(altered)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.writeImmediate(ctx, "insert false vector trail", func(conn *sql.Conn) error { return insertSourceTrail(ctx, conn, altered) }); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetSourceTrail(ctx, altered.ID); !IsCode(err, CodeIntegrity) {
				t.Fatalf("false score accepted: %v", err)
			}
			before, _ := mousa.EncodeSourceTrail(altered)
			var raw []byte
			if err := store.db.QueryRow(`SELECT record_json FROM source_trails WHERE id=?`, altered.ID[:]).Scan(&raw); err != nil || string(raw) != string(before) {
				t.Fatal("read repaired record")
			}
		}
	}
	path := store.path
	store.Close()
	if reopened, err := OpenReadOnly(ctx, path); !IsCode(err, CodeIntegrity) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("corruption reopen: %v", err)
	}
}

func TestVectorSelectionWriterOrderingAndRollback(t *testing.T) {
	ctx := context.Background()
	store, source, input := vectorFixture(t)
	defer store.Close()
	id, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := connect(ctx, store.path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	conn, err := reader.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `UPDATE local_items SET active=0 WHERE source_id=?`, source.ID[:]); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	request := testEvaluationRequest(t, source.ID, "ordered-query")
	go func() {
		close(started)
		result, err := store.EvaluateAndTraceVector(ctx, request, id, []float64{1, 0}, 100, mousa.PackingOriginal)
		if err == nil && (result.Trail.Vector.Eligible != 0 || len(result.Passages) != 0) {
			err = fmt.Errorf("query mixed pre-writer state: %+v", result)
		}
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("query passed uncommitted writer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	// Delete text through the writer snapshot, as a real deactivation does.
	if _, err := conn.ExecContext(ctx, `DELETE FROM segment_lexical_fts; DELETE FROM segment_lexical_rows; COMMIT`); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var before int
	if err := store.db.QueryRow(`SELECT count(*) FROM policy_decisions`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TEMP TRIGGER fail_vector_trail BEFORE INSERT ON source_trails BEGIN SELECT RAISE(ABORT,'trail failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EvaluateAndTraceVector(ctx, testEvaluationRequest(t, source.ID, "rolled-back"), id, []float64{1, 0}, 100, mousa.PackingOriginal); err == nil {
		t.Fatal("trail trigger accepted")
	}
	var after int
	if err := store.db.QueryRow(`SELECT count(*) FROM policy_decisions`).Scan(&after); err != nil || after != before {
		t.Fatalf("partial decision: %d -> %d %v", before, after, err)
	}
}

func TestVectorSourceBounds(t *testing.T) {
	store, source, _ := vectorFixture(t)
	defer store.Close()
	for i := range 99 {
		seedLocalItem(t, store, source, fmt.Sprintf("item-%03d", i), "x")
	}
	if _, err := store.ExportEmbeddingInputs(context.Background(), testEvaluationRequest(t, source.ID, "bounded-export")); !IsCode(err, CodeResourceLimit) || !strings.Contains(err.Error(), "100 active items") {
		t.Fatalf("source bound: %v", err)
	}
}

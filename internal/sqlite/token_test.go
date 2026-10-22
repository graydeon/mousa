package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/tokenizer"
)

func tokenOptions(t *testing.T) TokenQuery {
	t.Helper()
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	c, err := tokenizer.Start(context.Background(), python, cache)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return TokenQuery{Budget: mousa.TokenBudget{Cap: 1000}, Counter: c}
}

func queryTokenFixture(t *testing.T, store *Store, source mousa.Source, id mousa.SHA256, mode, name string, options TokenQuery) (mousa.SourceTrail, string, error) {
	t.Helper()
	request := testEvaluationRequest(t, source.ID, name)
	switch mode {
	case "lexical":
		r, text, err := store.EvaluateAndTraceTokenLexical(context.Background(), request, "Drain", "original", 1024, mousa.PackingExactV1, options)
		return r.Trail, text, err
	case "vector":
		r, err := store.EvaluateAndTraceTokenVector(context.Background(), request, id, []float64{1, 0}, 1024, mousa.PackingExactV1, options)
		return r.Trail, r.Context, err
	default:
		r, err := store.EvaluateAndTraceTokenHybrid(context.Background(), request, "Drain", "original", id, []float64{1, 0}, 1024, mousa.PackingExactV1, options)
		return r.Trail, r.Context, err
	}
}

func TestTokenSnapshotLifecycleAndRecount(t *testing.T) {
	if os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON") == "" || os.Getenv("MOUSA_TEST_TOKENIZER_CACHE") == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	store, source, input := vectorFixture(t)
	defer store.Close()
	ctx := context.Background()
	id, err := store.ImportEmbeddings(ctx, source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	modes := []string{"lexical", "vector", "hybrid"}
	originals := make(map[string]mousa.SourceTrail)
	encoded := make(map[string][]byte)
	for _, mode := range modes {
		original, text, err := queryTokenFixture(t, store, source, id, mode, "token-original-"+mode, tokenOptions(t))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(text, "Drain sockets") || original.Token == nil {
			t.Fatalf("%s selection: %s", mode, text)
		}
		verified, err := store.InspectSourceTrailWithTokens(ctx, testEvaluationRequest(t, source.ID, "token-verify-"+mode), original.ID, tokenOptions(t).Counter)
		if err != nil || verified.Historical.TokenVerification != "official_recount_verified" {
			t.Fatalf("%s recount: %+v %v", mode, verified, err)
		}
		originals[mode] = original
		encoded[mode], err = mousa.EncodeSourceTrail(original)
		if err != nil {
			t.Fatal(err)
		}
	}
	seedLocalItem(t, store, source, "a", "Drain replacement.")
	for _, mode := range modes {
		original := originals[mode]
		fresh, rendered, err := queryTokenFixture(t, store, source, id, mode, "token-fresh-"+mode, tokenOptions(t))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(rendered, "before stopping") || fresh.Vector != nil && fresh.Vector.Indexed != 1 || fresh.Hybrid != nil && fresh.Hybrid.Indexed != 1 {
			t.Fatalf("%s stale evidence: %q %+v", mode, rendered, fresh)
		}
		history, err := store.InspectSourceTrail(ctx, testEvaluationRequest(t, source.ID, "token-history-"+mode), original.ID)
		if err != nil || history.Historical.TokenVerification != "retired_text_unavailable" {
			t.Fatalf("%s retired history: %+v %v", mode, history, err)
		}
		counts := freshAuditCounts(t, store)
		options := tokenOptions(t)
		_, err = store.InspectSourceTrailWithTokens(ctx, testEvaluationRequest(t, source.ID, "token-retired-recount-"+mode), original.ID, options.Counter)
		options.Counter.Close()
		if err == nil || !strings.Contains(err.Error(), "retired") || !reflect.DeepEqual(counts, freshAuditCounts(t, store)) {
			t.Fatalf("%s retired verification committed: %v", mode, err)
		}
	}
	path := store.path
	store.Close()
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, mode := range modes {
		saved, err := store.GetSourceTrail(ctx, originals[mode].ID)
		if err != nil {
			t.Fatal(err)
		}
		after, err := mousa.EncodeSourceTrail(saved)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded[mode], after) {
			t.Fatalf("%s restart rewrote producing facts", mode)
		}
	}
	withdrawVerifiedSource(t, store, source.ID, "token-withdraw", 3)
	for _, mode := range modes {
		denied, rendered, err := queryTokenFixture(t, store, source, id, mode, "token-deny-"+mode, tokenOptions(t))
		if err != nil {
			t.Fatal(err)
		}
		if rendered != "" || len(denied.Candidates) != 0 || denied.Token.MeasuredTokens != 0 {
			t.Fatalf("%s withdrawal released source facts", mode)
		}
		hidden, err := store.InspectSourceTrail(ctx, testEvaluationRequest(t, source.ID, "token-hidden-"+mode), originals[mode].ID)
		if err != nil || hidden.Historical != nil {
			t.Fatalf("%s withdrawn history disclosed: %v", mode, err)
		}
	}
}

func TestTokenWriterOrderingAndRollback(t *testing.T) {
	options := tokenOptions(t)
	store, source, input := vectorFixture(t)
	defer store.Close()
	ctx := context.Background()
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
	request := testEvaluationRequest(t, source.ID, "ordered-token")
	go func() {
		close(started)
		r, err := store.EvaluateAndTraceTokenHybrid(ctx, request, "Drain", "original", id, []float64{1, 0}, 1024, mousa.PackingOriginal, options)
		if err == nil && (r.Context != "" || len(r.Trail.Candidates) != 0) {
			err = fmt.Errorf("mixed snapshot: %+v", r)
		}
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("passed writer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	seedLocalItem(t, store, source, "a", "Drain new.")
	for _, mode := range []string{"lexical", "vector", "hybrid"} {
		broken := tokenOptions(t)
		broken.Counter.Close()
		before := freshAuditCounts(t, store)
		got, text, err := queryTokenFixture(t, store, source, id, mode, "failed-counter-"+mode, broken)
		if err == nil || got.ID != (mousa.SourceTrailID{}) || text != "" || !reflect.DeepEqual(before, freshAuditCounts(t, store)) {
			t.Fatalf("counter failure committed/released: %+v %q %v", got, text, err)
		}
	}
	if _, err := store.db.Exec(`CREATE TEMP TRIGGER corrupt_token_projection AFTER INSERT ON source_trail_candidates BEGIN UPDATE source_trail_candidates SET final_rank=final_rank+1 WHERE trail_id=NEW.trail_id; END`); err != nil {
		t.Fatal(err)
	}
	before := freshAuditCounts(t, store)
	_, text, err := queryTokenFixture(t, store, source, id, "lexical", "failed-token-readback", tokenOptions(t))
	if !IsCode(err, CodeIntegrity) || text != "" || !reflect.DeepEqual(before, freshAuditCounts(t, store)) {
		t.Fatalf("readback rollback: %q %v", text, err)
	}
}

// Rebind the documented v3 packet and v7 record so content validation, not a stale hash, rejects damage.
func rebindTokenRecord(t *testing.T, trail mousa.SourceTrail) mousa.SourceTrail {
	t.Helper()
	p := trail
	p.ID = mousa.SourceTrailID{}
	p.RequestID = ""
	p.DecisionID = ""
	p.PacketID = ""
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(struct {
		Schema    string            `json:"schema"`
		Selection mousa.SourceTrail `json:"selection"`
	}{"mousa.context_packet.v3", p}); err != nil {
		t.Fatal(err)
	}
	trail.PacketID = mousa.ContextPacketID(sha256.Sum256(b.Bytes())).String()
	id, err := mousa.NewSourceTrailID(trail)
	if err != nil {
		t.Fatal(err)
	}
	trail.ID = id
	return trail
}

func TestTokenRetainedDigestCorruption(t *testing.T) {
	options := tokenOptions(t)
	store, source, _ := vectorFixture(t)
	defer store.Close()
	original, _, err := queryTokenFixture(t, store, source, mousa.SHA256{}, "lexical", "token-corruption", options)
	if err != nil {
		t.Fatal(err)
	}
	original.Token.RenderedSHA256 = mousa.SHA256{42}
	altered := rebindTokenRecord(t, original)
	if err := store.writeImmediate(context.Background(), "inject token damage", func(conn *sql.Conn) error { return insertSourceTrail(context.Background(), conn, altered) }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSourceTrail(context.Background(), altered.ID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("retained rendering damage accepted: %v", err)
	}
	path := store.path
	store.Close()
	if reopened, err := OpenReadOnly(context.Background(), path); !IsCode(err, CodeIntegrity) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("startup accepted rendering damage: %v", err)
	}
}

func TestTokenRecordedCountRequiresExplicitRecount(t *testing.T) {
	options := tokenOptions(t)
	store, source, _ := vectorFixture(t)
	defer store.Close()
	trail, _, err := queryTokenFixture(t, store, source, mousa.SHA256{}, "lexical", "recorded-count", options)
	if err != nil {
		t.Fatal(err)
	}
	trail.Token.MeasuredTokens--
	for i := len(trail.Candidates) - 1; i >= 0; i-- {
		if trail.Candidates[i].Selected {
			value := trail.Token.MeasuredTokens
			trail.Token.ProposedTokens[i] = &value
			break
		}
	}
	altered := rebindTokenRecord(t, trail)
	if err := store.writeImmediate(context.Background(), "inject recorded count", func(conn *sql.Conn) error { return insertSourceTrail(context.Background(), conn, altered) }); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSourceTrail(context.Background(), altered.ID); err != nil {
		t.Fatalf("normal reads must not pretend to re-execute a tokenizer: %v", err)
	}
	before := freshAuditCounts(t, store)
	_, err = store.InspectSourceTrailWithTokens(context.Background(), testEvaluationRequest(t, source.ID, "verify-recorded-count"), altered.ID, tokenOptions(t).Counter)
	if !IsCode(err, CodeIntegrity) || !reflect.DeepEqual(before, freshAuditCounts(t, store)) {
		t.Fatalf("false measured count accepted or committed: %v", err)
	}
}

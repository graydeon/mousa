package sqlite

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/tokenizer"
)

func TestTokenSupersessionWriterHeldThroughCounting(t *testing.T) {
	for _, mode := range []string{"lexical", "vector", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			testTokenSupersessionWriterHeldThroughCounting(t, mode)
		})
	}
}

func TestTokenAssociationsWriterHeldThroughCounting(t *testing.T) {
	t.Parallel()
	testTokenSupersessionWriterHeldThroughCounting(t, "associations")
}

func TestTokenAssociationsCancellationRollsBack(t *testing.T) {
	t.Parallel()
	testTokenSupersessionWriterHeldThroughCounting(t, "association-cancel")
}

func TestTokenAssociatedSupersessionWriterSnapshot(t *testing.T) {
	testTokenSupersessionWriterHeldThroughCounting(t, "triple")
}

func TestTokenAssociatedSupersessionCancellationRollsBack(t *testing.T) {
	testTokenSupersessionWriterHeldThroughCounting(t, "triple-cancel")
}

func TestTokenRetrievalAssociationsWriterSnapshot(t *testing.T) {
	for _, mode := range []string{"vector-associations", "hybrid-associations", "vector-associations-cancel", "hybrid-associations-cancel", "vector-associations-supersession", "hybrid-associations-supersession", "vector-associations-supersession-cancel", "hybrid-associations-supersession-cancel"} {
		t.Run(mode, func(t *testing.T) { testTokenSupersessionWriterHeldThroughCounting(t, mode) })
	}
}

func testTokenSupersessionWriterHeldThroughCounting(t *testing.T, mode string) {
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	f := newV4Fixture(t)
	defer f.store.Close()
	triple := mode == "triple" || mode == "triple-cancel"
	retrievalAssociations := strings.Contains(mode, "-associations")
	associations := mode == "associations" || mode == "association-cancel" || triple || retrievalAssociations
	var space mousa.SHA256
	if mode != "lexical" && (!associations || retrievalAssociations) {
		space = importSupersessionVectors(t, f)
	}
	_, state := f.activate(t)
	if associations {
		seedLocalItem(t, f.store, f.source, "qualification", "Close idle sockets explicitly.")
	}
	other, err := Open(t.Context(), f.store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gated-counter")
	gateStage := "primary"
	if associations {
		gateStage = "association"
	}
	script := fmt.Sprintf(`#!%s
import sys,struct,socket,tiktoken
enc=tiktoken.get_encoding('o200k_base')
o=sys.stdout.buffer;i=sys.stdin.buffer
o.write(b'mousa.tokens.v1/tiktoken/0.12.0/o200k_base/ordinary\n');o.flush()
first=True
while True:
 h=i.read(4)
 if not h: break
 n=struct.unpack('>I',h)[0];text=i.read(n).decode('utf-8')
 if first and (%q == "primary" or "Close idle sockets explicitly." in text):
  first=False
  with socket.create_connection(('127.0.0.1',%d)) as s:
   s.sendall(b'counting');s.recv(1)
 o.write(struct.pack('>Q',len(enc.encode_ordinary(text))));o.flush()
`, python, gateStage, listener.Addr().(*net.TCPAddr).Port)
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	counterContext, cancel := context.WithCancel(t.Context())
	defer cancel()
	counter, err := tokenizer.Start(counterContext, path, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer counter.Close()
	type result struct {
		trail mousa.SourceTrail
		err   error
	}
	done := make(chan result, 1)
	before := freshAuditCounts(t, f.store)
	go func() {
		options := TokenQuery{Budget: mousa.TokenBudget{Cap: 1000}, Counter: counter, Supersession: true}
		if associations {
			options.Supersession = false
			options.Associations = []mousa.AssociationDeclaration{{FromItem: "predecessor", ToItem: "qualification", Basis: "explicit cleanup", Author: "example.operator"}}
			if triple {
				options.Supersession = true
				options.Associations[0].FromItem = "successor"
			}
			if retrievalAssociations {
				options.Supersession = strings.Contains(mode, "-supersession")
				options.Associations[0].FromItem = "peer"
			}
		}
		if mode == "lexical" || associations && !retrievalAssociations {
			r, _, err := f.store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "gated-query"), "sharedterm", "original", 1024, mousa.PackingExactV1, options)
			done <- result{r.Trail, err}
		} else {
			retrievalMode := strings.Split(mode, "-")[0]
			r, err := queryTokenRetrieval(t, f, space, retrievalMode, mousa.PackingExactV1, "gated-query", 1024, options)
			done <- result{r.Trail, err}
		}
	}()
	gate, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	if err := gate.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	signal := make([]byte, 8)
	if _, err := gate.Read(signal); err != nil {
		t.Fatal(err)
	}
	// The worker cannot reply until released; a second connection must fail to
	// acquire the writer at this exact count boundary, not after an elapsed delay.
	conn, err := other.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), "PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	_, lockErr := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE")
	if lockErr == nil {
		conn.ExecContext(t.Context(), "ROLLBACK")
		t.Fatal("counting did not retain writer snapshot")
	}
	if !IsCode(classify("competing writer", lockErr), CodeBusy) {
		t.Fatalf("unexpected competing writer error: %v", lockErr)
	}
	conn.Close()
	if strings.HasSuffix(mode, "-cancel") {
		cancel()
		gate.Close()
		got := <-done
		if got.err == nil || got.trail.ID != (mousa.SourceTrailID{}) || !reflect.DeepEqual(before, freshAuditCounts(t, f.store)) {
			t.Fatal("associated cancellation committed producing state", got.err)
		}
		return
	}
	gate.Close()
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if mode == "associations" || triple || retrievalAssociations {
		schema := mousa.SourceTrailSchemaV11
		if triple {
			schema = mousa.SourceTrailSchemaV13
		}
		if retrievalAssociations {
			schema = mousa.SourceTrailSchemaV15
		}
		if got.trail.Schema != schema || len(got.trail.Associated) != 1 || !got.trail.Associated[0].Selected {
			t.Fatal("association did not share measured snapshot")
		}
		seedLocalItem(t, other, f.source, "qualification", "Replacement qualification.")
		historical, err := f.store.GetSourceTrail(t.Context(), got.trail.ID)
		if err != nil || historical.Associated[0] != got.trail.Associated[0] {
			t.Fatal("later target revision reinterpreted selection", err)
		}
		if !triple && !strings.Contains(mode, "-supersession") {
			return
		}
	}
	if got.trail.Supersession.ActivationID == nil || *got.trail.Supersession.ActivationID != state.CurrentActivationID || len(got.trail.Supersession.Dispositions) != 1 {
		t.Fatal("mixed consultation")
	}
	transition := activationTestTransition(t, f.source.ID, state.CurrentActivationID, mousa.SupersessionDeclarationID{}, "after measured query", activationBaseUsec+1)
	if err := other.ApplySupersessionActivation(t.Context(), transition); err != nil {
		t.Fatal(err)
	}
	historical, err := f.store.GetSourceTrail(t.Context(), got.trail.ID)
	if err != nil || *historical.Supersession.ActivationID != state.CurrentActivationID {
		t.Fatal("later activation reinterpreted snapshot", err)
	}
}

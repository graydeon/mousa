package sqlite

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/tokenizer"
)

func TestTokenSupersessionWriterHeldThroughCounting(t *testing.T) {
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	f := newV4Fixture(t)
	defer f.store.Close()
	_, state := f.activate(t)
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
 if first:
  first=False
  with socket.create_connection(('127.0.0.1',%d)) as s:
   s.sendall(b'counting');s.recv(1)
 o.write(struct.pack('>Q',len(enc.encode_ordinary(text))));o.flush()
`, python, listener.Addr().(*net.TCPAddr).Port)
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	counter, err := tokenizer.Start(t.Context(), path, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer counter.Close()
	type result struct {
		r   TracedLexicalResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		r, _, err := f.store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "gated-query"), "sharedterm", "original", 1024, mousa.PackingExactV1, TokenQuery{Budget: mousa.TokenBudget{Cap: 1000}, Counter: counter, Supersession: true})
		done <- result{r, err}
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
	gate.Close()
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.r.Trail.Supersession.ActivationID == nil || *got.r.Trail.Supersession.ActivationID != state.CurrentActivationID || len(got.r.Trail.Supersession.Dispositions) != 1 {
		t.Fatal("mixed consultation")
	}
	transition := activationTestTransition(t, f.source.ID, state.CurrentActivationID, mousa.SupersessionDeclarationID{}, "after measured query", activationBaseUsec+1)
	if err := other.ApplySupersessionActivation(t.Context(), transition); err != nil {
		t.Fatal(err)
	}
	historical, err := f.store.GetSourceTrail(t.Context(), got.r.Trail.ID)
	if err != nil || *historical.Supersession.ActivationID != state.CurrentActivationID {
		t.Fatal("later activation reinterpreted snapshot", err)
	}
}

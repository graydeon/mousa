package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/tokenizer"
)

func TestTokenAssociatedCanonicalHistory(t *testing.T) {
	t.Parallel()
	for _, damage := range []string{"target-item", "declaring-item", "omitted-size", "rendering", "missing-segment", "missing-representation"} {
		t.Run(damage, func(t *testing.T) {
			t.Parallel()
			store := openLexicalStore(t)
			defer store.Close()
			source, _, _ := seedAssociatedItems(t, store)
			options := tokenOptions(t)
			options.Associations = []mousa.AssociationDeclaration{drainDeclaration()}
			got, _, err := store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, source, damage), "restart", "original", 8192, mousa.PackingOriginal, options)
			if err != nil {
				t.Fatal(err)
			}
			trail := got.Trail
			switch damage {
			case "target-item":
				trail.Associated[0].ToItem = "foreign-or-unknown"
			case "declaring-item":
				trail.Associated[0].FromItem = "unselected"
			case "omitted-size":
				// Omit all notes by body bytes, then alter one omitted canonical size.
				options = tokenOptions(t)
				options.Associations = []mousa.AssociationDeclaration{drainDeclaration()}
				primaryBytes := uint64(0)
				for _, r := range trail.Candidates {
					if r.Selected {
						primaryBytes += r.TextBytes
					}
				}
				got, _, err = store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, source, "omitted-original"), "restart", "original", primaryBytes, mousa.PackingOriginal, options)
				if err != nil {
					t.Fatal(err)
				}
				trail = got.Trail
				if trail.Associated[0].Omission != "bytes" {
					t.Fatal("fixture did not omit by bytes")
				}
				trail.Associated[0].TextBytes++
			case "rendering":
				trail.Token.RenderedSHA256 = mousa.SHA256{42}
			case "missing-segment":
				id := got.Associated[0].Segment.ID
				damageCanonicalParent(t, store, `DELETE FROM segments WHERE id = ?`, id[:])
			case "missing-representation":
				id := got.Associated[0].Segment.RepresentationID
				damageCanonicalParent(t, store, `DELETE FROM representations WHERE id = ?`, id[:])
			}
			altered := rebindTokenRecord(t, trail)
			if err := store.writeImmediate(t.Context(), "inject combined damage", func(conn *sql.Conn) error { return insertSourceTrail(t.Context(), conn, altered) }); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetSourceTrail(t.Context(), altered.ID); !IsCode(err, CodeIntegrity) {
				t.Fatalf("rehashed %s accepted: %v", damage, err)
			}
			path := store.path
			store.Close()
			reopened, err := OpenReadOnly(t.Context(), path)
			if reopened != nil {
				reopened.Close()
			}
			if !IsCode(err, CodeIntegrity) {
				t.Fatalf("startup accepted %s: %v", damage, err)
			}
		})
	}
}

func TestTokenAssociatedRetiredHistoryAndDenial(t *testing.T) {
	t.Parallel()
	store := openLexicalStore(t)
	defer store.Close()
	source, _, _ := seedAssociatedItems(t, store)
	options := tokenOptions(t)
	options.Associations = []mousa.AssociationDeclaration{drainDeclaration()}
	original, _, err := store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, source, "before-revision"), "restart", "original", 8192, mousa.PackingOriginal, options)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := mousa.EncodeSourceTrail(original.Trail)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.GetSource(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	seedLocalItem(t, store, record, "drain.md", "A replacement note that was not selected historically.")
	history, err := store.InspectSourceTrail(t.Context(), testEvaluationRequest(t, source, "after-revision"), original.Trail.ID)
	if err != nil || history.Historical.TokenVerification != "retired_text_unavailable" || !reflect.DeepEqual(history.Historical.Token, mousa.ReleasedTokenSelection(original.Trail)) {
		t.Fatalf("historical producing facts changed: %+v %v", history, err)
	}
	before := freshAuditCounts(t, store)
	_, err = store.InspectSourceTrailWithTokens(t.Context(), testEvaluationRequest(t, source, "retired-recount"), original.Trail.ID, tokenOptions(t).Counter)
	if err == nil || !strings.Contains(err.Error(), "retired") || !reflect.DeepEqual(before, freshAuditCounts(t, store)) {
		t.Fatal("retired recount did not fail closed", err)
	}
	saved, err := store.GetSourceTrail(t.Context(), original.Trail.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := mousa.EncodeSourceTrail(saved)
	if err != nil || string(after) != string(canonical) {
		t.Fatal("history rewrote canonical producing record", err)
	}
	withdrawVerifiedSource(t, store, source, "deny-combined", 3)
	options = tokenOptions(t)
	options.Associations = []mousa.AssociationDeclaration{drainDeclaration()}
	denied, text, err := store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, source, "fresh-denial"), "restart", "original", 8192, mousa.PackingOriginal, options)
	if err != nil || text != "" || len(denied.Associated) != 0 || len(denied.Candidates) != 0 || denied.Trail.Schema != mousa.SourceTrailSchemaV7 {
		t.Fatalf("denial exposed association payload: %+v %q %v", denied, text, err)
	}
}

func TestTokenAssociatedCounterFailureRollsBack(t *testing.T) {
	testTokenAssociatedCounterFailureRollsBack(t, false)
}

func TestTokenAssociatedSupersessionCounterFailureRollsBack(t *testing.T) {
	testTokenAssociatedCounterFailureRollsBack(t, true)
}

func testTokenAssociatedCounterFailureRollsBack(t *testing.T, supersession bool) {
	t.Parallel()
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	f := newV4Fixture(t)
	defer f.store.Close()
	if supersession {
		f.activate(t)
	}
	seedLocalItem(t, f.store, f.source, "qualification", "Close idle sockets explicitly.")
	executable := filepath.Join(t.TempDir(), "fault-counter")
	marker := filepath.Join(t.TempDir(), "associated-count")
	script := fmt.Sprintf(`#!%s
import sys,struct,tiktoken
enc=tiktoken.get_encoding('o200k_base')
o=sys.stdout.buffer;i=sys.stdin.buffer
print('mousa.tokens.v1/tiktoken/0.12.0/o200k_base/ordinary',flush=True)
calls=0
while True:
 h=i.read(4)
 if not h: break
 text=i.read(struct.unpack('>I',h)[0]).decode()
 calls+=1
 if calls==2:
  open(%q,'w').write(text)
  sys.exit(42)
 o.write(struct.pack('>Q',len(enc.encode_ordinary(text))));o.flush()
`, python, marker)
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	counter, err := tokenizer.Start(t.Context(), executable, cache)
	if err != nil {
		t.Fatal(err)
	}
	defer counter.Close()
	before := freshAuditCounts(t, f.store)
	options := TokenQuery{Budget: mousa.TokenBudget{Cap: 1000}, Counter: counter, Associations: []mousa.AssociationDeclaration{{FromItem: "successor", ToItem: "qualification", Basis: "cleanup", Author: "example.operator"}}}
	options.Supersession = supersession
	got, text, err := f.store.EvaluateAndTraceTokenLexical(context.Background(), testEvaluationRequest(t, f.source.ID, "associated-counter-fault"), "successor", "original", 8192, mousa.PackingOriginal, options)
	if err == nil || text != "" || got.Trail.ID != (mousa.SourceTrailID{}) || !reflect.DeepEqual(before, freshAuditCounts(t, f.store)) {
		t.Fatal("associated counting failure committed or released", err)
	}
	payload, err := os.ReadFile(marker)
	if err != nil || !strings.Contains(string(payload), "Close idle sockets explicitly.") {
		t.Fatal("fault did not exercise associated count", err)
	}
}

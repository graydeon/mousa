package mousa

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/tokenizer"
)

func TestV8FixedTrailAndPacket(t *testing.T) {
	// Independently derived using Python JSON/SHA-256 and official encode_ordinary,
	// with the unchanged v4 fixture fields and a retained rendering of "bbbb".
	data, err := os.ReadFile("testdata/trail-v8.json")
	if err != nil {
		t.Fatal(err)
	}
	trail, err := DecodeSourceTrail(data)
	if err != nil {
		t.Fatal(err)
	}
	if trail.ID.String() != "8f328d93c323a56865757bd0b53e4f1a787b26779b75516daa46253602a08981" || trail.PacketID != "8d52ae3f5c4a27a430343800caf9efa77a50bf95b4106510f81f60ae81676dd1" {
		t.Fatal("fixed identity changed")
	}
	encoded, err := EncodeSourceTrail(trail)
	if err != nil || !bytes.Equal(encoded, data) {
		t.Fatal("canonical v8 bytes changed", err)
	}
	for _, name := range []string{"schema", "supersession", "consulted", "activation_id", "declaration_id", "dispositions", "extra", "order", "size", "selected", "count"} {
		t.Run(name, func(t *testing.T) {
			var obj map[string]any
			if err := json.Unmarshal(data, &obj); err != nil {
				t.Fatal(err)
			}
			member := obj["supersession"].(map[string]any)
			switch name {
			case "schema":
				obj["schema"] = SourceTrailSchemaV7
			case "supersession":
				delete(obj, name)
			case "consulted", "activation_id", "declaration_id", "dispositions":
				delete(member, name)
			case "extra":
				member["unexpected"] = true
			case "order":
				obj["candidates"].([]any)[0], obj["candidates"].([]any)[1] = obj["candidates"].([]any)[1], obj["candidates"].([]any)[0]
			case "size":
				obj["candidates"].([]any)[0].(map[string]any)["text_bytes"] = 0
			case "selected":
				obj["candidates"].([]any)[0].(map[string]any)["selected"] = true
			case "count":
				obj["token"].(map[string]any)["proposed_tokens"].([]any)[0] = 1
			}
			changed, err := json.Marshal(obj)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSourceTrail(changed); err == nil {
				t.Fatal("malformed v8 accepted")
			}
		})
	}
	for _, name := range []string{"schema", "size", "selected", "count", "duplicate-row", "wrong-row-digest", "packing"} {
		t.Run("recomputed/"+name, func(t *testing.T) {
			var changed SourceTrail
			if err := json.Unmarshal(data, &changed); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "schema":
				changed.Schema = SourceTrailSchemaV7
			case "size":
				changed.Candidates[0].TextBytes = 0
			case "selected":
				changed.Candidates[0].Selected = true
			case "count":
				n := uint64(1)
				changed.Token.ProposedTokens[0] = &n
			case "duplicate-row":
				changed.Supersession.Dispositions = append(changed.Supersession.Dispositions, changed.Supersession.Dispositions[0])
			case "wrong-row-digest":
				changed.Supersession.Dispositions[0].ContentSHA256 = SHA256{19}
			case "packing":
				changed.Candidates[2].Omission = "tokens"
			}
			changed.PacketID, err = tokenPacketIdentity(changed)
			if err != nil {
				t.Fatal(err)
			}
			changed.ID, err = NewSourceTrailID(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err := changed.Validate(); err == nil {
				t.Fatal("recomputed malformed selection accepted")
			}
		})
	}
}

func TestV8WithheldTwinDoesNotSeedPacking(t *testing.T) {
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	for _, after := range []bool{false, true} {
		for _, policy := range []string{PackingOriginal, PackingExactV1} {
			t.Run(policy+map[bool]string{false: "/before", true: "/after"}[after], func(t *testing.T) {
				d := v4Declaration(t)
				state := supersessionSelectionState(t, d)
				request := v4Request(t, d.SourceID)
				candidates := v4DuplicateCandidates(t, d)
				if after {
					slices.Reverse(candidates)
				}
				for i := range candidates {
					candidates[i].FinalRank = i + 1
					candidates[i].LexicalRank = i + 1
					candidates[i].BM25 = float64(i)
				}
				base, err := NewSourceTrailWithSupersession(request, trailDecision(t, request, PolicyOutcomeAllow), "same", candidates, 9, policy, &state, &d)
				if err != nil {
					t.Fatal(err)
				}
				counter, err := tokenizer.Start(context.Background(), python, cache)
				if err != nil {
					t.Fatal(err)
				}
				defer counter.Close()
				var survivor int
				if !after {
					survivor = 1
				}
				want := ContextPassage(request.SourceID, candidates[survivor].Segment, candidates[survivor].Text)
				count, err := counter.Count(want)
				if err != nil {
					t.Fatal(err)
				}
				trail, text, err := PackTokenContext(base, request.SourceID, candidates, TokenBudget{Cap: count}, counter)
				if err != nil {
					t.Fatal(err)
				}
				if text != want || trail.UsedBytes != 9 || trail.Token.MeasuredTokens != count || !trail.Candidates[survivor].Selected {
					t.Fatalf("survivor rendering differs: %q", text)
				}
				withheld := 0
				if after {
					withheld = 2
				}
				row := trail.Candidates[withheld]
				if row.Selected || row.Omission != "" || row.TextBytes != 9 || row.FinalRank != withheld+1 || trail.Token.ProposedTokens[withheld] != nil {
					t.Fatalf("withheld metadata: %+v", row)
				}
			})
		}
	}
}

func TestV8LaterSurvivorFitsTokenPressure(t *testing.T) {
	python, cache := os.Getenv("MOUSA_TEST_TOKENIZER_PYTHON"), os.Getenv("MOUSA_TEST_TOKENIZER_CACHE")
	if python == "" || cache == "" {
		t.Skip("official tokenizer acceptance environment not configured")
	}
	for _, policy := range []string{PackingOriginal, PackingExactV1} {
		t.Run(policy, func(t *testing.T) {
			d := v4Declaration(t)
			state := supersessionSelectionState(t, d)
			request := v4Request(t, d.SourceID)
			candidates := v4Candidates(t, d)
			candidates[1] = supersessionCandidate(t, supersessionRepresentation("oversized-v8"), d.SourceID, strings.Repeat("oversized ", 60), 2, CandidateAccepted)
			for i := range candidates {
				candidates[i].LexicalRank = i + 1
				candidates[i].BM25 = float64(i)
			}
			base, err := NewSourceTrailWithSupersession(request, trailDecision(t, request, PolicyOutcomeAllow), "fit", candidates, 1024, policy, &state, &d)
			if err != nil {
				t.Fatal(err)
			}
			counter, err := tokenizer.Start(t.Context(), python, cache)
			if err != nil {
				t.Fatal(err)
			}
			defer counter.Close()
			want := ContextPassage(request.SourceID, candidates[2].Segment, candidates[2].Text)
			count, err := counter.Count(want)
			if err != nil {
				t.Fatal(err)
			}
			trail, text, err := PackTokenContext(base, request.SourceID, candidates, TokenBudget{Cap: count}, counter)
			if err != nil {
				t.Fatal(err)
			}
			if text != want || trail.Candidates[1].Omission != "tokens" || !trail.Candidates[2].Selected || trail.Token.ProposedTokens[0] != nil {
				t.Fatalf("later survivor did not fit: %+v", trail)
			}
		})
	}
}

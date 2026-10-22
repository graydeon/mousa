package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

func captureTokenProjection(t *testing.T, emit func() error) map[string]any {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "projection")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	stdout := os.Stdout
	os.Stdout = file
	defer func() { os.Stdout = stdout }()
	if err := emit(); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.NewDecoder(file).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

// Exercise the native serializers directly: public ingestion cannot create an
// arbitrary multi-source derivation. SQLite tests cover the real verified graph.
func TestNativeTokenProjectionRejectsParallelMetadata(t *testing.T) {
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		for _, filtered := range []bool{false, true} {
			name := packing + "/all-accepted"
			if filtered {
				name = packing + "/rejected-before-between-after"
			}
			t.Run(name, func(t *testing.T) {
				trail := mousa.SourceTrail{PackingPolicy: packing, Token: &mousa.TokenSelection{Mode: "lexical", Backend: mousa.TokenBackend, Rendering: mousa.ContextRenderingV1, Budget: mousa.TokenBudget{Cap: 100}, Limit: 100, MeasuredTokens: 50, RenderedBytes: 7}}
				verified := []mousa.VerifiedLexicalCandidate{}
				history := &sqlite.SourceTrailView{Candidates: []sqlite.SourceTrailCandidateView{}}
				accepted := 0
				for i := range 5 {
					id := mousa.SegmentID{byte(i + 1)}
					disposition := mousa.CandidateAccepted
					if filtered && i%2 == 0 {
						disposition = mousa.CandidateRejected
					}
					row := mousa.TrailCandidate{SegmentID: id, Disposition: disposition}
					var count *uint64
					if disposition == mousa.CandidateAccepted {
						accepted++
						row.FinalRank = accepted
						row.Selected = accepted == 1
						if row.Selected {
							n := uint64(50)
							count = &n
						} else {
							row.Omission = "bytes"
						}
						history.Candidates = append(history.Candidates, sqlite.SourceTrailCandidateView{SegmentID: id, Rank: accepted, Selected: row.Selected, Omission: row.Omission})
					} else {
						history.LifecycleExcluded++
					}
					trail.Candidates = append(trail.Candidates, row)
					trail.Token.Lexical = append(trail.Token.Lexical, mousa.TokenLexicalFact{Rank: i + 1, BM25: -float64(10 - i)})
					trail.Token.ProposedTokens = append(trail.Token.ProposedTokens, count)
					verified = append(verified, mousa.VerifiedLexicalCandidate{Segment: mousa.Segment{ID: id, Selector: mousa.NewTextByteRangeSelector(0, 7)}, Text: "context"})
				}
				before, _ := json.Marshal(trail)
				query := captureTokenProjection(t, func() error {
					return emitTokenQuery("lexical", "source", 1024, packing, trail, mousa.PolicyDecision{Outcome: mousa.PolicyOutcomeAllow}, "context", verified)
				})
				history.Token = mousa.ReleasedTokenSelection(trail)
				inspection := sqlite.TrailInspection{Historical: history}
				wireHistory := captureTokenProjection(t, func() error { return emitTrailInspection("source", trail.ID, inspection) })
				for _, result := range []map[string]any{query, wireHistory["historical"].(map[string]any)} {
					token := result["token"].(map[string]any)
					lexical := token["lexical"].([]any)
					proposed := token["proposed_tokens"].([]any)
					if len(lexical) != accepted || len(proposed) != accepted {
						t.Fatalf("rejected metadata leaked: accepted=%d lexical_facts=%d proposed_slots=%d", accepted, len(lexical), len(proposed))
					}
					ids, ok := token["candidate_ids"].([]any)
					if !ok || len(ids) != accepted {
						t.Fatalf("accepted identity correspondence missing: %v", token)
					}
					wantKeys := []string{"mode", "backend", "rendering", "source_id", "limit", "budget", "measured_tokens", "rendered_bytes", "rendered_sha256", "candidate_ids", "proposed_tokens", "lexical"}
					if len(token) != len(wantKeys) {
						t.Fatalf("unassessed parallel fields: %v", token)
					}
					for _, key := range wantKeys {
						if _, ok := token[key]; !ok {
							t.Fatalf("missing %s", key)
						}
					}
					for i := range accepted {
						original := i
						if filtered {
							original = 2*i + 1
						}
						fact := lexical[i].(map[string]any)
						if ids[i] != trail.Candidates[original].SegmentID.String() || fact["rank"] != float64(i+1) || fact["bm25"] != -float64(10-original) {
							t.Fatalf("accepted correspondence: %v", token)
						}
						if i == 0 && proposed[i] != float64(50) || i > 0 && proposed[i] != nil {
							t.Fatalf("accepted proposed count: %v", proposed)
						}
					}
					if token["measured_tokens"] != float64(50) || token["limit"] != float64(100) {
						t.Fatal("lost accepted measurements")
					}
				}
				if !reflect.DeepEqual(query["token"], wireHistory["historical"].(map[string]any)["token"]) || query["context"] != "context" || len(query["evidence"].([]any)) != 1 {
					t.Fatal("query/history release disagreement")
				}
				after, _ := json.Marshal(trail)
				if !bytes.Equal(before, after) {
					t.Fatal("serializer mutated canonical facts")
				}
			})
		}
	}
	hidden := captureTokenProjection(t, func() error {
		return emitTrailInspection("source", mousa.SourceTrailID{}, sqlite.TrailInspection{AuthorizationOutcome: mousa.PolicyOutcomeDeny})
	})
	if _, ok := hidden["historical"]; ok {
		t.Fatalf("denied history: %v", hidden)
	}
}

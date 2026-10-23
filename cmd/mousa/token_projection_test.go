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
				mcpHistory := captureTokenProjection(t, func() error {
					return emit(mcpTrailProjection("source", trail.ID.String(), inspection, false, true))
				})
				for _, result := range []map[string]any{query, wireHistory["historical"].(map[string]any), mcpHistory["historical"].(map[string]any)} {
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

func TestNativeTokenQueryOutcomes(t *testing.T) {
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		for _, tc := range []struct {
			name     string
			rows     []mousa.TrailCandidate
			denied   bool
			want     string
			accepted int
			evidence int
		}{
			{name: "all-lifecycle-rejected", rows: []mousa.TrailCandidate{{Disposition: mousa.CandidateRejected}}, want: "lifecycle_excluded"},
			{name: "no-match", want: "no_candidates"},
			{name: "denied", denied: true, want: "policy_excluded"},
			{name: "selected", rows: []mousa.TrailCandidate{{Disposition: mousa.CandidateAccepted, Selected: true, FinalRank: 1}}, want: "evidence", accepted: 1, evidence: 1},
			{name: "token-budget", rows: []mousa.TrailCandidate{{Disposition: mousa.CandidateAccepted, Omission: "tokens"}}, want: "budget_omitted", accepted: 1},
			{name: "byte-budget", rows: []mousa.TrailCandidate{{Disposition: mousa.CandidateAccepted, Omission: "bytes"}}, want: "budget_omitted", accepted: 1},
			{name: "mixed-token-budget", rows: []mousa.TrailCandidate{{Disposition: mousa.CandidateRejected}, {Disposition: mousa.CandidateAccepted, Omission: "tokens"}}, want: "budget_omitted", accepted: 1},
			{name: "mixed-byte-budget", rows: []mousa.TrailCandidate{{Disposition: mousa.CandidateAccepted, Omission: "bytes"}, {Disposition: mousa.CandidateRejected}}, want: "budget_omitted", accepted: 1},
			{name: "mixed-selected", rows: []mousa.TrailCandidate{{Disposition: mousa.CandidateRejected}, {Disposition: mousa.CandidateAccepted, Selected: true, FinalRank: 1}}, want: "evidence", accepted: 1, evidence: 1},
		} {
			t.Run(packing+"/"+tc.name, func(t *testing.T) {
				trail := mousa.SourceTrail{Candidates: tc.rows, PackingPolicy: packing, Token: &mousa.TokenSelection{Mode: "lexical", Budget: mousa.TokenBudget{Cap: 1}, Limit: 1}}
				verified := make([]mousa.VerifiedLexicalCandidate, len(tc.rows))
				for i := range trail.Candidates {
					id := mousa.SegmentID{byte(i + 1)}
					trail.Candidates[i].SegmentID = id
					trail.Token.Lexical = append(trail.Token.Lexical, mousa.TokenLexicalFact{Rank: i + 1, BM25: -10})
					var count *uint64
					if trail.Candidates[i].Omission == "tokens" {
						n := uint64(100)
						count = &n
					}
					trail.Token.ProposedTokens = append(trail.Token.ProposedTokens, count)
					verified[i] = mousa.VerifiedLexicalCandidate{Segment: mousa.Segment{ID: id, Selector: mousa.NewTextByteRangeSelector(0, 8)}, Text: "evidence"}
				}
				decision := mousa.PolicyDecision{Outcome: mousa.PolicyOutcomeAllow}
				if tc.denied {
					decision.Outcome = mousa.PolicyOutcomeDeny
				}
				context := ""
				if tc.evidence > 0 {
					context = "evidence"
				}
				before, _ := json.Marshal(trail)
				result := captureTokenProjection(t, func() error {
					return emitTokenQuery("lexical", "source", 1, packing, trail, decision, context, verified)
				})
				if result["outcome"] != tc.want {
					t.Errorf("outcome=%v want %s", result["outcome"], tc.want)
				}
				if len(result["evidence"].([]any)) != tc.evidence || result["context"] != context {
					t.Fatalf("released evidence: %v", result)
				}
				token := result["token"].(map[string]any)
				for _, key := range []string{"candidate_ids", "lexical", "proposed_tokens"} {
					if tc.accepted == 0 && token[key] == nil {
						continue
					}
					if rows, ok := token[key].([]any); !ok || len(rows) != tc.accepted {
						t.Fatalf("%s contains rejected slots: %v", key, token[key])
					}
				}
				if tc.accepted == 1 {
					if token["lexical"].([]any)[0].(map[string]any)["rank"] != float64(1) {
						t.Fatal("released original rejected ordinal")
					}
				}
				after, _ := json.Marshal(trail)
				if !bytes.Equal(before, after) {
					t.Fatal("classification mutated canonical facts")
				}
			})
		}
	}
}

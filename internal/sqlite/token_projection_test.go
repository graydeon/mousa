package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestTokenRejectedHistoryFacts(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	request, active := seedEnforcedRetrieval(t, store, "sharedterm active evidence")
	other := addVerifiedLexicalDocument(t, store, "token-withdrawn", "sharedterm withdrawn ancestor")
	putVerifiedRepresentation(t, store, []mousa.DerivationInput{mousa.NewRepresentationDerivationInput(active.representation.ID), mousa.NewRepresentationDerivationInput(other.representation.ID)}, "token.merge", []byte("sharedterm rejected merge"), true)
	withdrawVerifiedSource(t, store, other.source.ID, "token-withdraw", 200)
	traced, _, err := store.EvaluateAndTraceTokenLexical(ctx, request, "sharedterm", "original", 1024, mousa.PackingOriginal, tokenOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := store.InspectSourceTrail(ctx, testEvaluationRequest(t, request.SourceID, "token-inspection"), traced.Trail.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := inspection.Historical
	if h == nil || h.LifecycleExcluded != 1 || len(h.Candidates) != 1 {
		t.Fatalf("mixed-source fixture: %+v", h)
	}
	if len(h.Token.Lexical) != len(h.Candidates) || len(h.Token.ProposedTokens) != len(h.Candidates) {
		t.Fatalf("rejected metadata leaked: accepted=%d rejected=%d lexical_facts=%d proposed_slots=%d", len(h.Candidates), h.LifecycleExcluded, len(h.Token.Lexical), len(h.Token.ProposedTokens))
	}
}

func TestTokenHistoryProjectionPositionsAndRecount(t *testing.T) {
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			ctx := context.Background()
			store := openLexicalStore(t)
			defer store.Close()
			request, active := seedEnforcedRetrieval(t, store, "root passage")
			ancestors := make(map[mousa.SegmentID]mousa.SourceID)
			for i := range 5 {
				other := addVerifiedLexicalDocument(t, store, fmt.Sprintf("ancestor-%d", i), "ancestor passage")
				mixed := putVerifiedRepresentation(t, store, []mousa.DerivationInput{mousa.NewRepresentationDerivationInput(active.representation.ID), mousa.NewRepresentationDerivationInput(other.representation.ID)}, fmt.Sprintf("token.merge.%d", i), []byte("sharedterm retained passage"), true)
				ancestors[mixed.segments[0].ID] = other.source.ID
			}
			all, _, err := store.EvaluateAndTraceTokenLexical(ctx, request, "sharedterm", "original", 1024, packing, tokenOptions(t))
			if err != nil {
				t.Fatal(err)
			}
			control, err := store.InspectSourceTrail(ctx, testEvaluationRequest(t, request.SourceID, "all-accepted"), all.Trail.ID)
			if err != nil {
				t.Fatal(err)
			}
			if control.Historical == nil || len(control.Historical.Candidates) != 5 || len(control.Historical.Token.Lexical) != 5 || control.Historical.LifecycleExcluded != 0 {
				t.Fatalf("all-accepted control: %+v", control)
			}
			for _, i := range []int{0, 2, 4} {
				withdrawVerifiedSource(t, store, ancestors[all.Trail.Candidates[i].SegmentID], fmt.Sprintf("withdraw-%d", i), 200)
			}
			traced, text, err := store.EvaluateAndTraceTokenLexical(ctx, testEvaluationRequest(t, request.SourceID, "filtered-query"), "sharedterm", "original", 1024, packing, tokenOptions(t))
			if err != nil {
				t.Fatal(err)
			}
			for i, row := range traced.Trail.Candidates {
				want := mousa.CandidateAccepted
				if i%2 == 0 {
					want = mousa.CandidateRejected
				}
				if row.Disposition != want {
					t.Fatalf("position %d: %s", i, row.Disposition)
				}
			}
			before, err := mousa.EncodeSourceTrail(traced.Trail)
			if err != nil {
				t.Fatal(err)
			}
			var storedBefore []byte
			if err := store.db.QueryRow(`SELECT record_json FROM source_trails WHERE id=?`, traced.Trail.ID[:]).Scan(&storedBefore); err != nil {
				t.Fatal(err)
			}
			counts := freshAuditCounts(t, store)
			for _, recount := range []bool{false, true} {
				req := testEvaluationRequest(t, request.SourceID, fmt.Sprintf("history-%v", recount))
				var inspection TrailInspection
				if recount {
					inspection, err = store.InspectSourceTrailWithTokens(ctx, req, traced.Trail.ID, tokenOptions(t).Counter)
				} else {
					inspection, err = store.InspectSourceTrail(ctx, req, traced.Trail.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				h := inspection.Historical
				if h == nil || h.LifecycleExcluded != 3 || len(h.Candidates) != 2 || len(h.Token.CandidateIDs) != 2 || len(h.Token.Lexical) != 2 || len(h.Token.ProposedTokens) != 2 {
					t.Fatalf("filtered history: %+v", h)
				}
				for i, original := range []int{1, 3} {
					if h.Token.CandidateIDs[i] != traced.Trail.Candidates[original].SegmentID || h.Candidates[i].SegmentID != h.Token.CandidateIDs[i] || h.Candidates[i].Rank != i+1 || h.Token.Lexical[i].Rank != i+1 || h.Token.Lexical[i].BM25 != traced.Trail.Token.Lexical[original].BM25 || !reflect.DeepEqual(h.Token.ProposedTokens[i], traced.Trail.Token.ProposedTokens[original]) {
						t.Fatalf("accepted correspondence: %+v", h)
					}
				}
				if h.Token.MeasuredTokens != traced.Trail.Token.MeasuredTokens || h.Token.RenderedBytes != uint64(len(text)) || h.Token.RenderedSHA256 != mousa.SHA256(sha256.Sum256([]byte(text))) || h.PacketID != traced.Trail.PacketID || h.Token.Budget != traced.Trail.Token.Budget {
					t.Fatal("changed rendering, count, budget or identity")
				}
				if recount && h.TokenVerification != "official_recount_verified" || !recount && h.TokenVerification != "retained_digest_verified_count_recorded" {
					t.Fatal(h.TokenVerification)
				}
				// The native view is owned independently of the canonical producing record.
				projected := mousa.ReleasedTokenSelection(traced.Trail)
				projected.CandidateIDs[0] = mousa.SegmentID{}
				projected.Lexical[0].BM25 = 12345
				*projected.ProposedTokens[0] = 999999
				after, _ := mousa.EncodeSourceTrail(traced.Trail)
				if !bytes.Equal(before, after) {
					t.Fatal("projection mutated producing record")
				}
			}
			afterCounts := freshAuditCounts(t, store)
			if afterCounts["policy_decisions"] != counts["policy_decisions"]+2 || afterCounts["source_trails"] != counts["source_trails"] || afterCounts["source_trail_candidates"] != counts["source_trail_candidates"] {
				t.Fatalf("authorization audit behavior: %v -> %v", counts, afterCounts)
			}
			saved, err := store.GetSourceTrail(ctx, traced.Trail.ID)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := mousa.EncodeSourceTrail(saved)
			var storedAfter []byte
			if err := store.db.QueryRow(`SELECT record_json FROM source_trails WHERE id=?`, traced.Trail.ID[:]).Scan(&storedAfter); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || !bytes.Equal(storedBefore, storedAfter) {
				t.Fatal("history rewrote canonical bytes or projections")
			}
			withdrawVerifiedSource(t, store, request.SourceID, "withdraw-query-source", 300)
			for _, recount := range []bool{false, true} {
				req := testEvaluationRequest(t, request.SourceID, fmt.Sprintf("denied-history-%v", recount))
				var hidden TrailInspection
				if recount {
					hidden, err = store.InspectSourceTrailWithTokens(ctx, req, saved.ID, tokenOptions(t).Counter)
				} else {
					hidden, err = store.InspectSourceTrail(ctx, req, saved.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				wire, _ := json.Marshal(hidden)
				if hidden.Historical != nil || bytes.Contains(wire, []byte("token")) || bytes.Contains(wire, []byte(saved.ID.String())) {
					t.Fatalf("denied history: %s", wire)
				}
			}
		})
	}
}

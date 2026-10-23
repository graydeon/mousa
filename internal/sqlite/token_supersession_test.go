package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func combinedQuery(t *testing.T, f *v4Fixture, name, packing string, bytes uint64) (TracedLexicalResult, string) {
	t.Helper()
	options := tokenOptions(t)
	options.Supersession = true
	r, text, err := f.store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, f.source.ID, name), "sharedterm", "original", bytes, packing, options)
	if err != nil {
		t.Fatal(err)
	}
	if r.Trail.Schema != mousa.SourceTrailSchemaV8 {
		t.Fatal(r.Trail.Schema)
	}
	return r, text
}

func TestTokenSupersessionCanonicalHistory(t *testing.T) {
	for _, packing := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(packing, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			noHistory, _ := combinedQuery(t, f, "no-history", packing, 1024)
			if !noHistory.Trail.Supersession.Consulted || noHistory.Trail.Supersession.ActivationID != nil {
				t.Fatal("no-history consultation")
			}
			_, state := f.activate(t)
			active, text := combinedQuery(t, f, "active", packing, 1024)
			if len(active.Trail.Supersession.Dispositions) != 1 {
				t.Fatal("missing exact-pin suppression")
			}
			withheld := active.Trail.Supersession.Dispositions[0].SegmentID
			for i, row := range active.Trail.Candidates {
				if row.SegmentID == withheld {
					if row.Selected || row.TextBytes == 0 || row.FinalRank == 0 || row.Omission != "" || active.Trail.Token.ProposedTokens[i] != nil || active.Candidates[i].Text != "" {
						t.Fatalf("withheld accounting: %+v", row)
					}
				}
			}
			if text == "" {
				t.Fatal("byte-equal survivor should remain")
			}
			before, err := mousa.EncodeSourceTrail(active.Trail)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.store.InspectSourceTrailWithTokens(t.Context(), testEvaluationRequest(t, f.source.ID, "recount"), active.Trail.ID, tokenOptions(t).Counter); err != nil {
				t.Fatal(err)
			}
			replacement := supersessionTestDeclaration(t, f.source.ID, "twin", f.twin.ID, "peer", f.peer.ID, "example.operations", "peer replaces twin")
			if err := f.store.PutSupersessionDeclaration(t.Context(), replacement); err != nil {
				t.Fatal(err)
			}
			replace := activationTestTransition(t, f.source.ID, state.CurrentActivationID, replacement.ID, "replace active declaration", activationBaseUsec+1)
			if err := f.store.ApplySupersessionActivation(t.Context(), replace); err != nil {
				t.Fatal(err)
			}
			replaced, _ := combinedQuery(t, f, "replaced", packing, 1024)
			if len(replaced.Trail.Supersession.Dispositions) != 1 || replaced.Trail.Supersession.Dispositions[0].PredecessorRepresentationID != f.twin.ID {
				t.Fatal("replacement retained old suppression")
			}
			transition := activationTestTransition(t, f.source.ID, replace.ID, mousa.SupersessionDeclarationID{}, "deactivate", activationBaseUsec+2)
			if err := f.store.ApplySupersessionActivation(t.Context(), transition); err != nil {
				t.Fatal(err)
			}
			deactivated, _ := combinedQuery(t, f, "deactivated", packing, 1024)
			if deactivated.Trail.Supersession.ActivationID == nil || deactivated.Trail.Supersession.DeclarationID != nil || len(deactivated.Trail.Supersession.Dispositions) != 0 {
				t.Fatal("deactivation not recorded")
			}
			old, err := f.store.GetSourceTrail(t.Context(), active.Trail.ID)
			if err != nil {
				t.Fatal(err)
			}
			after, err := mousa.EncodeSourceTrail(old)
			if err != nil || string(before) != string(after) {
				t.Fatal("history reinterpreted")
			}
			path := f.store.path
			f.store.Close()
			reopened, err := OpenReadOnly(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			reopened.Close()
		})
	}
}

func TestTokenSupersessionCanonicalTampering(t *testing.T) {
	for _, name := range []string{"missing-row", "extra-row", "wrong-pin", "missing-event", "foreign-event", "wrong-declaration", "rendering"} {
		t.Run(name, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			f.activate(t)
			r, _ := combinedQuery(t, f, "tamper-"+name, mousa.PackingOriginal, 1)
			trail := r.Trail
			switch name {
			case "missing-row":
				id := trail.Supersession.Dispositions[0].SegmentID
				trail.Supersession.Dispositions = []mousa.SupersessionDisposition{}
				for i := range trail.Candidates {
					if trail.Candidates[i].SegmentID == id {
						trail.Candidates[i].Omission = "bytes"
					}
				}
			case "extra-row":
				row := trail.Supersession.Dispositions[0]
				for i := range trail.Candidates {
					c := &trail.Candidates[i]
					if c.SegmentID == row.SegmentID {
						continue
					}
					extra := row
					extra.SegmentID = c.SegmentID
					extra.ContentSHA256 = c.ContentSHA256
					c.Omission = ""
					trail.Supersession.Dispositions = append(trail.Supersession.Dispositions, extra)
					break
				}
				// Row ordering is structural; order the fabricated membership by accepted rank.
				ranks := map[mousa.SegmentID]int{}
				for _, c := range trail.Candidates {
					ranks[c.SegmentID] = c.FinalRank
				}
				if ranks[trail.Supersession.Dispositions[0].SegmentID] > ranks[trail.Supersession.Dispositions[1].SegmentID] {
					trail.Supersession.Dispositions[0], trail.Supersession.Dispositions[1] = trail.Supersession.Dispositions[1], trail.Supersession.Dispositions[0]
				}
			case "wrong-pin":
				trail.Supersession.Dispositions[0].SuccessorItemID = "peer"
			case "missing-event":
				id := mousa.SupersessionActivationID{99}
				trail.Supersession.ActivationID = &id
			case "foreign-event":
				foreign := foreignDeclaration(t, f.store)
				transition := activationTestTransition(t, foreign.SourceID, mousa.SupersessionActivationID{}, foreign.ID, "foreign", activationBaseUsec)
				if err := f.store.ApplySupersessionActivation(t.Context(), transition); err != nil {
					t.Fatal(err)
				}
				trail.Supersession.ActivationID = &transition.ID
			case "wrong-declaration":
				second := supersessionTestDeclaration(t, f.source.ID, "predecessor", f.predecessor.ID, "peer", f.peer.ID, "example.operations", "peer replaces predecessor")
				if err := f.store.PutSupersessionDeclaration(t.Context(), second); err != nil {
					t.Fatal(err)
				}
				trail.Supersession.DeclarationID = &second.ID
				for i := range trail.Supersession.Dispositions {
					trail.Supersession.Dispositions[i].DeclarationID = second.ID
				}
			case "rendering":
				// A nonempty rendering is required to reach content validation rather than empty accounting.
				full, _ := combinedQuery(t, f, "tamper-rendering-full", mousa.PackingOriginal, 1024)
				trail = full.Trail
				trail.Token.RenderedSHA256 = mousa.SHA256{77}
			}
			trail = rebindTokenRecord(t, trail)
			data, err := mousa.EncodeSourceTrail(trail)
			if err != nil {
				t.Fatalf("structural fixture invalid: %v", err)
			}
			if _, err := mousa.DecodeSourceTrail(data); err != nil {
				t.Fatalf("codec rejected content-only damage: %v", err)
			}
			commitV4Trail(t, f.store, trail)
			if _, err := f.store.GetSourceTrail(context.Background(), trail.ID); !IsCode(err, CodeIntegrity) {
				t.Fatalf("canonical read: %v", err)
			}
			path := f.store.path
			f.store.Close()
			if reopened, err := OpenReadOnly(t.Context(), path); !IsCode(err, CodeIntegrity) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("startup: %v", err)
			}
		})
	}
}

func TestTokenSupersessionFailedCounterRollback(t *testing.T) {
	f := newV4Fixture(t)
	defer f.store.Close()
	f.activate(t)
	options := tokenOptions(t)
	options.Supersession = true
	options.Counter.Close()
	before := freshAuditCounts(t, f.store)
	r, text, err := f.store.EvaluateAndTraceTokenLexical(t.Context(), testEvaluationRequest(t, f.source.ID, "counter-failed"), "sharedterm", "original", 1024, mousa.PackingExactV1, options)
	if err == nil || r.Trail.ID != (mousa.SourceTrailID{}) || text != "" || !reflect.DeepEqual(before, freshAuditCounts(t, f.store)) {
		t.Fatalf("failed counter committed: %v", err)
	}
}

func TestTokenSupersessionGraphFilteringAndExactAncestry(t *testing.T) {
	f := newV4Fixture(t)
	defer f.store.Close()
	f.activate(t)
	other := addVerifiedLexicalDocument(t, f.store, "foreign-token-source", "sharedterm other source")
	merged := putVerifiedRepresentation(t, f.store, []mousa.DerivationInput{mousa.NewRepresentationDerivationInput(f.predecessor.ID), mousa.NewRepresentationDerivationInput(other.representation.ID)}, "token.supersession.merge", []byte("sharedterm derived evidence"), true)
	all, _ := combinedQuery(t, f, "graph-all", mousa.PackingExactV1, 1024)
	if len(all.Trail.Candidates) != 5 || len(all.Trail.Supersession.Dispositions) != 1 {
		t.Fatal("nonmatching ancestry was suppressed")
	}
	withdrawVerifiedSource(t, f.store, other.source.ID, "withdraw-foreign", 200)
	filtered, _ := combinedQuery(t, f, "graph-filtered", mousa.PackingExactV1, 1024)
	for _, row := range filtered.Trail.Candidates {
		if row.SegmentID == merged.segments[0].ID && row.Disposition != mousa.CandidateRejected {
			t.Fatal("foreign ancestry not rejected")
		}
	}
	inspection, err := f.store.InspectSourceTrail(t.Context(), testEvaluationRequest(t, f.source.ID, "graph-history"), filtered.Trail.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := inspection.Historical
	if h.LifecycleExcluded != 1 || len(h.Token.CandidateIDs) != 4 || len(h.Token.Lexical) != 4 || len(h.Token.ProposedTokens) != 4 || len(h.Supersession.Dispositions) != 1 {
		t.Fatalf("unsafe accepted-only projection: %+v", h)
	}
	for i, id := range h.Token.CandidateIDs {
		if id == merged.segments[0].ID || h.Token.Lexical[i].Rank != i+1 {
			t.Fatal("rejected identity or original search rank leaked")
		}
	}
}

func TestTokenSupersessionMissingNamedParents(t *testing.T) {
	for _, parent := range []string{"segment", "representation"} {
		t.Run(parent, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			f.activate(t)
			r, _ := combinedQuery(t, f, "missing-parent", mousa.PackingOriginal, 1024)
			id := r.Trail.Supersession.Dispositions[0].SegmentID
			statement, victim := "DELETE FROM segments WHERE id = ?", id[:]
			if parent == "representation" {
				statement, victim = "DELETE FROM representations WHERE id = ?", f.predecessor.ID[:]
			}
			damageCanonicalParent(t, f.store, statement, victim)
			if _, err := f.store.GetSourceTrail(t.Context(), r.Trail.ID); !IsCode(err, CodeIntegrity) {
				t.Fatalf("named parent classification: %v", err)
			}
			path := f.store.path
			f.store.Close()
			if reopened, err := OpenReadOnly(t.Context(), path); !IsCode(err, CodeIntegrity) {
				if reopened != nil {
					reopened.Close()
				}
				t.Fatalf("startup accepted missing parent: %v", err)
			}
		})
	}
}

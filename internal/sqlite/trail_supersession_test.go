package sqlite

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

// v4NoSelectionBudget is a budget no fixture candidate fits, so an original-policy record releases
// nothing and a withheld candidate without a suppression row stays unselected. A record mutated that
// way still passes structural validation, so only the canonical membership check can reject it.
const v4NoSelectionBudget = 1

// v4Fixture is one store whose source holds the four current indexed items a stored v4 trail needs: a
// pinned predecessor, a byte-identical twin of it, the declared successor and an unrelated peer.
// Every item is a current active revision, so every one is an accepted lexical candidate.
type v4Fixture struct {
	store       *Store
	source      mousa.Source
	predecessor mousa.Representation
	twin        mousa.Representation
	successor   mousa.Representation
	peer        mousa.Representation
}

func newV4Fixture(t *testing.T) *v4Fixture {
	t.Helper()
	ctx := context.Background()
	store := openLexicalStore(t)
	seedEvaluationFixture(t, store)
	source, predecessor, predecessorText := prepareLocalRevision(t, store, "predecessor", "sharedterm predecessor evidence")
	_, twin, twinText := prepareLocalRevision(t, store, "twin", "sharedterm predecessor evidence")
	_, successor, successorText := prepareLocalRevision(t, store, "successor", "sharedterm successor evidence")
	_, peer, peerText := prepareLocalRevision(t, store, "peer", "sharedterm peer evidence")
	for _, item := range []struct {
		name           string
		representation mousa.Representation
		text           []byte
	}{
		{"predecessor", predecessor, predecessorText},
		{"twin", twin, twinText},
		{"successor", successor, successorText},
		{"peer", peer, peerText},
	} {
		action, err := store.ActivateLocalItem(ctx, source.ID, item.name, item.representation.ID, item.text)
		if err != nil || action != "added" {
			t.Fatalf("activate %s = %q, %v", item.name, action, err)
		}
	}
	return &v4Fixture{store: store, source: source, predecessor: predecessor, twin: twin, successor: successor, peer: peer}
}

// v4Retrieval is one verified retrieval input a fixture trail is built from.
type v4Retrieval struct {
	request    mousa.PolicyEvaluationRequest
	decision   mousa.PolicyDecision
	candidates []mousa.VerifiedLexicalCandidate
}

func (fixture *v4Fixture) retrieve(t *testing.T, requestID string) v4Retrieval {
	t.Helper()
	ctx := context.Background()
	request := testEvaluationRequest(t, fixture.source.ID, requestID)
	decision, err := fixture.store.EvaluateSourceRetrieval(ctx, request)
	if err != nil {
		t.Fatalf("EvaluateSourceRetrieval: %v", err)
	}
	result, err := fixture.store.SearchEnforcedLexical(ctx, request, "sharedterm", 10)
	if err != nil {
		t.Fatalf("SearchEnforcedLexical: %v", err)
	}
	if decision.Outcome != mousa.PolicyOutcomeAllow || len(result.Candidates) != 4 {
		t.Fatalf("fixture retrieval = outcome %q / %d candidates, want an allow decision over four candidates", decision.Outcome, len(result.Candidates))
	}
	for _, candidate := range result.Candidates {
		if candidate.Disposition != mousa.CandidateAccepted || candidate.FinalRank == 0 {
			t.Fatalf("fixture candidate = %#v, want an accepted ranked candidate", candidate)
		}
	}
	return v4Retrieval{request: request, decision: decision, candidates: result.Candidates}
}

// activate records one declaration pinning the fixture predecessor to the fixture successor and
// activates it, returning the verified declaration and the stored current state.
func (fixture *v4Fixture) activate(t *testing.T) (mousa.SupersessionDeclaration, mousa.SupersessionActivationState) {
	t.Helper()
	ctx := context.Background()
	declaration := supersessionTestDeclaration(t, fixture.source.ID, "predecessor", fixture.predecessor.ID, "successor", fixture.successor.ID, "example.operations", "successor replaces the predecessor")
	if err := fixture.store.PutSupersessionDeclaration(ctx, declaration); err != nil {
		t.Fatalf("PutSupersessionDeclaration: %v", err)
	}
	transition := activationTestTransition(t, fixture.source.ID, mousa.SupersessionActivationID{}, declaration.ID, "activate the recorded declaration", activationBaseUsec)
	if err := fixture.store.ApplySupersessionActivation(ctx, transition); err != nil {
		t.Fatalf("ApplySupersessionActivation: %v", err)
	}
	state, err := fixture.store.GetSupersessionActivationState(ctx, fixture.source.ID)
	if err != nil {
		t.Fatalf("GetSupersessionActivationState: %v", err)
	}
	return declaration, state
}

// buildV4Trail builds one v4 record through the opt-in domain constructor.
func buildV4Trail(t *testing.T, input v4Retrieval, budget uint64, policy string, state *mousa.SupersessionActivationState, declaration *mousa.SupersessionDeclaration) mousa.SourceTrail {
	t.Helper()
	trail, err := mousa.NewSourceTrailWithSupersession(input.request, input.decision, "sharedterm", input.candidates, budget, policy, state, declaration)
	if err != nil {
		t.Fatalf("NewSourceTrailWithSupersession: %v", err)
	}
	return trail
}

// storeV4Trail writes one v4 record through the existing trail insert helper and reads it back inside
// the same transaction, exactly as the production write path does.
func storeV4Trail(t *testing.T, store *Store, trail mousa.SourceTrail) mousa.SourceTrail {
	t.Helper()
	ctx := context.Background()
	var stored mousa.SourceTrail
	err := store.writeImmediate(ctx, "store v4 trail fixture", func(conn *sql.Conn) error {
		if err := insertSourceTrail(ctx, conn, trail); err != nil {
			return err
		}
		read, err := getSourceTrail(ctx, conn, trail.ID)
		if err != nil {
			return err
		}
		stored = read
		return nil
	})
	if err != nil {
		t.Fatalf("store v4 trail: %v", err)
	}
	return stored
}

// commitV4Trail commits one structurally valid v4 record through the existing trail insert helper
// without the production read-back, so a test can store a record whose canonical verification must
// reject what the structural codec accepts. The record still encodes and validates as a v4 record;
// only the content and administration checks can refuse it.
func commitV4Trail(t *testing.T, store *Store, trail mousa.SourceTrail) {
	t.Helper()
	ctx := context.Background()
	err := store.writeImmediate(ctx, "commit v4 tamper fixture", func(conn *sql.Conn) error {
		return insertSourceTrail(ctx, conn, trail)
	})
	if err != nil {
		t.Fatalf("commit v4 tamper fixture: %v", err)
	}
}

// resealV4Trail recomputes the released byte total and the packet and trail identities a mutated v4
// record must carry, so only the mutation itself is re-derived rather than repaired.
func resealV4Trail(t *testing.T, trail mousa.SourceTrail) mousa.SourceTrail {
	t.Helper()
	selected := make([]bool, len(trail.Candidates))
	used := uint64(0)
	for index, candidate := range trail.Candidates {
		selected[index] = candidate.Selected
		if candidate.Selected {
			used += candidate.TextBytes
		}
	}
	trail.UsedBytes = used
	packet, err := mousa.NewContextPacketID(trail.Candidates, trail.BudgetBytes, selected)
	if err != nil {
		t.Fatalf("NewContextPacketID: %v", err)
	}
	trail.PacketID = packet.String()
	id, err := mousa.NewSourceTrailID(trail)
	if err != nil {
		t.Fatalf("NewSourceTrailID: %v", err)
	}
	trail.ID = id
	return trail
}

func trailCandidateBySegment(t *testing.T, candidates []mousa.TrailCandidate, id mousa.SegmentID) mousa.TrailCandidate {
	t.Helper()
	for _, candidate := range candidates {
		if candidate.SegmentID == id {
			return candidate
		}
	}
	t.Fatalf("no recorded candidate for segment %s", id)
	return mousa.TrailCandidate{}
}

func TestStoreV4TrailReadVerifiesRecordedSuppression(t *testing.T) {
	ctx := context.Background()
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(policy, func(t *testing.T) {
			fixture := newV4Fixture(t)
			declaration, state := fixture.activate(t)
			input := fixture.retrieve(t, "request-v4-read-"+policy)
			trail := buildV4Trail(t, input, 1<<20, policy, &state, &declaration)
			if len(trail.Supersession.Dispositions) != 1 {
				t.Fatalf("recorded dispositions = %#v, want exactly one", trail.Supersession.Dispositions)
			}
			row := trail.Supersession.Dispositions[0]
			if row.PredecessorItemID != declaration.PredecessorItemID ||
				row.PredecessorRepresentationID != declaration.PredecessorRepresentationID ||
				row.SuccessorItemID != declaration.SuccessorItemID ||
				row.SuccessorRepresentationID != declaration.SuccessorRepresentationID {
				t.Fatalf("recorded pins = %#v, want the consulted declaration's pins %#v", row, declaration)
			}
			withheld := trailCandidateBySegment(t, trail.Candidates, row.SegmentID)
			if withheld.Disposition != mousa.CandidateAccepted || withheld.Selected || withheld.TextBytes == 0 {
				t.Fatalf("withheld candidate = %#v, want an accepted unselected candidate with a canonical size", withheld)
			}
			// The twin is byte-equal to the withheld candidate, so it proves withholding removes a
			// candidate from the selection without removing its own content from consideration.
			for _, candidate := range trail.Candidates {
				if candidate.ContentSHA256 == withheld.ContentSHA256 && candidate.SegmentID != withheld.SegmentID && !candidate.Selected {
					t.Fatalf("byte-equal survivor was not released: %#v", candidate)
				}
			}

			stored := storeV4Trail(t, fixture.store, trail)
			if !reflect.DeepEqual(stored, trail) {
				t.Fatalf("stored trail = %#v, want %#v", stored, trail)
			}
			path := fixture.store.path
			if err := fixture.store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(ctx, path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer reopened.Close()
			reread, err := reopened.GetSourceTrail(ctx, stored.ID)
			if err != nil || !reflect.DeepEqual(reread, stored) {
				t.Fatalf("reopened GetSourceTrail = %#v, %v, want %#v", reread, err, stored)
			}
		})
	}
}

// TestStoreV4TrailSuppressedCandidateSeedsNoRetainedSet places the withheld duplicate directly after
// the byte-equal passage that is retained, so the byte-aware duplicate check must leave the withheld
// candidate alone instead of requiring a packing omission it cannot carry.
func TestStoreV4TrailSuppressedCandidateSeedsNoRetainedSet(t *testing.T) {
	fixture := newV4Fixture(t)
	defer fixture.store.Close()
	declaration, state := fixture.activate(t)
	input := fixture.retrieve(t, "request-v4-withheld-duplicate")
	twin := segmentOf(t, input.candidates, fixture.twin.ID)
	withheld := segmentOf(t, input.candidates, fixture.predecessor.ID)
	if twin.Segment.ContentSHA256 != withheld.Segment.ContentSHA256 {
		t.Fatalf("fixture twins are not byte-equal: %#v / %#v", twin.Segment, withheld.Segment)
	}
	input.candidates = placeV4CandidateAfter(t, input.candidates, twin.Segment.ID, withheld.Segment.ID)

	trail := buildV4Trail(t, input, 1<<20, mousa.PackingExactV1, &state, &declaration)
	withheldIndex, retainedIndex := -1, -1
	for index, candidate := range trail.Candidates {
		switch candidate.SegmentID {
		case withheld.Segment.ID:
			withheldIndex = index
		case twin.Segment.ID:
			retainedIndex = index
		}
	}
	if withheldIndex != retainedIndex+1 {
		t.Fatalf("fixture order = retained %d / withheld %d, want the withheld duplicate second", retainedIndex, withheldIndex)
	}
	if trail.Candidates[withheldIndex].Selected || trail.Candidates[withheldIndex].Omission != "" {
		t.Fatalf("withheld candidate = %#v, want accepted unselected with no packing omission", trail.Candidates[withheldIndex])
	}
	if !trail.Candidates[retainedIndex].Selected || trail.Candidates[retainedIndex].Omission != "" {
		t.Fatalf("retained twin = %#v, want a released passage", trail.Candidates[retainedIndex])
	}
	// A stored v4 record is read back canonically here, so the withheld duplicate must not be
	// mistaken for a duplicate omission of its retained twin.
	stored := storeV4Trail(t, fixture.store, trail)
	if !reflect.DeepEqual(stored, trail) {
		t.Fatalf("stored trail = %#v, want %#v", stored, trail)
	}
}

// segmentOf finds one verified candidate of a representation.
func segmentOf(t *testing.T, candidates []mousa.VerifiedLexicalCandidate, representation mousa.RepresentationID) mousa.VerifiedLexicalCandidate {
	t.Helper()
	for _, candidate := range candidates {
		if candidate.Segment.RepresentationID == representation {
			return candidate
		}
	}
	t.Fatalf("no candidate derives from representation %s", representation)
	return mousa.VerifiedLexicalCandidate{}
}

// placeV4CandidateAfter moves one candidate directly after another and renumbers the accepted ranks,
// so a fixture record can place a withheld duplicate after its retained byte-equal twin. The store
// verifies recorded ranks as recorded facts, so the reordered list is still a valid v4 record.
func placeV4CandidateAfter(t *testing.T, candidates []mousa.VerifiedLexicalCandidate, anchor, move mousa.SegmentID) []mousa.VerifiedLexicalCandidate {
	t.Helper()
	var moved mousa.VerifiedLexicalCandidate
	found := false
	reordered := make([]mousa.VerifiedLexicalCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Segment.ID == move {
			moved = candidate
			found = true
			continue
		}
		reordered = append(reordered, candidate)
		if candidate.Segment.ID == anchor {
			reordered = append(reordered, moved)
		}
	}
	if !found {
		t.Fatalf("no candidate for segment %s", move)
	}
	rank := 0
	for index := range reordered {
		if reordered[index].Disposition == mousa.CandidateAccepted {
			rank++
			reordered[index].FinalRank = rank
		}
	}
	return reordered
}

func TestStoreV4TrailRecordsNoHistoryDeactivationAndDenial(t *testing.T) {
	ctx := context.Background()

	t.Run("no activation history", func(t *testing.T) {
		fixture := newV4Fixture(t)
		defer fixture.store.Close()
		input := fixture.retrieve(t, "request-v4-no-history")
		trail := buildV4Trail(t, input, 1<<20, mousa.PackingOriginal, nil, nil)
		if !trail.Supersession.Consulted || trail.Supersession.ActivationID != nil || trail.Supersession.DeclarationID != nil || len(trail.Supersession.Dispositions) != 0 {
			t.Fatalf("no-history member = %#v", trail.Supersession)
		}
		stored := storeV4Trail(t, fixture.store, trail)
		if !reflect.DeepEqual(stored, trail) {
			t.Fatalf("stored trail = %#v, want %#v", stored, trail)
		}
	})

	t.Run("deactivation", func(t *testing.T) {
		fixture := newV4Fixture(t)
		defer fixture.store.Close()
		declaration, state := fixture.activate(t)
		input := fixture.retrieve(t, "request-v4-deactivation")
		active := storeV4Trail(t, fixture.store, buildV4Trail(t, input, 1<<20, mousa.PackingOriginal, &state, &declaration))

		deactivation := activationTestTransition(t, fixture.source.ID, state.CurrentActivationID, mousa.SupersessionDeclarationID{}, "deactivate the declaration", activationBaseUsec+1)
		if err := fixture.store.ApplySupersessionActivation(ctx, deactivation); err != nil {
			t.Fatalf("deactivation: %v", err)
		}
		deactivated, err := fixture.store.GetSupersessionActivationState(ctx, fixture.source.ID)
		if err != nil || deactivated.ActiveDeclarationID != nil {
			t.Fatalf("deactivated state = %#v, %v", deactivated, err)
		}
		// The stored trail keeps its own recorded event and must not be reinterpreted by the later
		// transition.
		reread, err := fixture.store.GetSourceTrail(ctx, active.ID)
		if err != nil || !reflect.DeepEqual(reread, active) {
			t.Fatalf("read after deactivation = %#v, %v, want %#v", reread, err, active)
		}
		recorded := storeV4Trail(t, fixture.store, buildV4Trail(t, input, 1<<20, mousa.PackingOriginal, &deactivated, nil))
		if recorded.Supersession.ActivationID == nil || *recorded.Supersession.ActivationID != deactivation.ID ||
			recorded.Supersession.DeclarationID != nil || len(recorded.Supersession.Dispositions) != 0 {
			t.Fatalf("deactivation member = %#v", recorded.Supersession)
		}
	})

	t.Run("denial", func(t *testing.T) {
		fixture := newV4Fixture(t)
		defer fixture.store.Close()
		withdrawVerifiedSource(t, fixture.store, fixture.source.ID, "v4-denial-withdrawal", 200)
		request := testEvaluationRequest(t, fixture.source.ID, "request-v4-denial")
		decision, err := fixture.store.EvaluateSourceRetrieval(ctx, request)
		if err != nil || decision.Outcome != mousa.PolicyOutcomeDeny {
			t.Fatalf("denied evaluation = %#v, %v", decision, err)
		}
		trail := buildV4Trail(t, v4Retrieval{request: request, decision: decision, candidates: []mousa.VerifiedLexicalCandidate{}}, 1<<20, mousa.PackingOriginal, nil, nil)
		if trail.Supersession.Consulted || trail.Supersession.ActivationID != nil || trail.Supersession.DeclarationID != nil || len(trail.Supersession.Dispositions) != 0 || len(trail.Candidates) != 0 {
			t.Fatalf("denial member = %#v", trail.Supersession)
		}
		stored := storeV4Trail(t, fixture.store, trail)
		if !reflect.DeepEqual(stored, trail) {
			t.Fatalf("stored trail = %#v, want %#v", stored, trail)
		}
	})
}

func TestStoreV4TrailReadRejectsRecordedAdministrationMismatch(t *testing.T) {
	ctx := context.Background()

	t.Run("recorded activation event is missing", func(t *testing.T) {
		fixture := newV4Fixture(t)
		defer fixture.store.Close()
		declaration, state := fixture.activate(t)
		input := fixture.retrieve(t, "request-v4-missing-event")
		trail := buildV4Trail(t, input, 1<<20, mousa.PackingOriginal, &state, &declaration)
		absent := absentActivationID()
		trail.Supersession.ActivationID = &absent
		trail = resealV4Trail(t, trail)
		assertV4CodecAccepts(t, trail)
		commitV4Trail(t, fixture.store, trail)
		if _, err := fixture.store.GetSourceTrail(ctx, trail.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSourceTrail = %v, want %s", err, CodeIntegrity)
		}
	})

	t.Run("recorded activation selects another declaration", func(t *testing.T) {
		fixture := newV4Fixture(t)
		defer fixture.store.Close()
		declaration, state := fixture.activate(t)
		input := fixture.retrieve(t, "request-v4-other-event")
		trail := buildV4Trail(t, input, 1<<20, mousa.PackingOriginal, &state, &declaration)

		second := supersessionTestDeclaration(t, fixture.source.ID, "predecessor", fixture.predecessor.ID, "peer", fixture.peer.ID, "example.operations", "the peer replaces the predecessor")
		if err := fixture.store.PutSupersessionDeclaration(ctx, second); err != nil {
			t.Fatalf("PutSupersessionDeclaration: %v", err)
		}
		replacement := activationTestTransition(t, fixture.source.ID, state.CurrentActivationID, second.ID, "replace the declaration", activationBaseUsec+1)
		if err := fixture.store.ApplySupersessionActivation(ctx, replacement); err != nil {
			t.Fatalf("ApplySupersessionActivation: %v", err)
		}
		trail.Supersession.ActivationID = &replacement.ID
		trail = resealV4Trail(t, trail)
		assertV4CodecAccepts(t, trail)
		commitV4Trail(t, fixture.store, trail)
		if _, err := fixture.store.GetSourceTrail(ctx, trail.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSourceTrail = %v, want %s", err, CodeIntegrity)
		}
	})

	t.Run("recorded declaration disagrees with the activation", func(t *testing.T) {
		fixture := newV4Fixture(t)
		defer fixture.store.Close()
		declaration, state := fixture.activate(t)
		input := fixture.retrieve(t, "request-v4-other-declaration")
		trail := buildV4Trail(t, input, 1<<20, mousa.PackingOriginal, &state, &declaration)

		second := supersessionTestDeclaration(t, fixture.source.ID, "predecessor", fixture.predecessor.ID, "peer", fixture.peer.ID, "example.operations", "the peer replaces the predecessor")
		if err := fixture.store.PutSupersessionDeclaration(ctx, second); err != nil {
			t.Fatalf("PutSupersessionDeclaration: %v", err)
		}
		trail.Supersession.DeclarationID = &second.ID
		for index := range trail.Supersession.Dispositions {
			trail.Supersession.Dispositions[index].DeclarationID = second.ID
		}
		trail = resealV4Trail(t, trail)
		assertV4CodecAccepts(t, trail)
		commitV4Trail(t, fixture.store, trail)
		if _, err := fixture.store.GetSourceTrail(ctx, trail.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSourceTrail = %v, want %s", err, CodeIntegrity)
		}
	})

	t.Run("recorded activation event belongs to another source", func(t *testing.T) {
		fixture := newV4Fixture(t)
		defer fixture.store.Close()
		declaration, state := fixture.activate(t)
		input := fixture.retrieve(t, "request-v4-foreign-event")
		trail := buildV4Trail(t, input, 1<<20, mousa.PackingOriginal, &state, &declaration)

		foreign := foreignDeclaration(t, fixture.store)
		foreignTransition := activationTestTransition(t, foreign.SourceID, mousa.SupersessionActivationID{}, foreign.ID, "activate a declaration of another source", activationBaseUsec)
		if err := fixture.store.ApplySupersessionActivation(ctx, foreignTransition); err != nil {
			t.Fatalf("foreign ApplySupersessionActivation: %v", err)
		}
		trail.Supersession.ActivationID = &foreignTransition.ID
		trail = resealV4Trail(t, trail)
		assertV4CodecAccepts(t, trail)
		commitV4Trail(t, fixture.store, trail)
		if _, err := fixture.store.GetSourceTrail(ctx, trail.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSourceTrail = %v, want %s", err, CodeIntegrity)
		}
	})

	t.Run("disposition pins disagree with the consulted declaration", func(t *testing.T) {
		fixture := newV4Fixture(t)
		defer fixture.store.Close()
		declaration, state := fixture.activate(t)
		input := fixture.retrieve(t, "request-v4-wrong-pins")
		trail := buildV4Trail(t, input, 1<<20, mousa.PackingOriginal, &state, &declaration)
		trail.Supersession.Dispositions[0].SuccessorItemID = "peer"
		trail = resealV4Trail(t, trail)
		assertV4CodecAccepts(t, trail)
		commitV4Trail(t, fixture.store, trail)
		if _, err := fixture.store.GetSourceTrail(ctx, trail.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSourceTrail = %v, want %s", err, CodeIntegrity)
		}
	})

	t.Run("suppression row is missing", func(t *testing.T) {
		fixture := newV4Fixture(t)
		defer fixture.store.Close()
		declaration, state := fixture.activate(t)
		input := fixture.retrieve(t, "request-v4-missing-row")
		trail := buildV4Trail(t, input, v4NoSelectionBudget, mousa.PackingOriginal, &state, &declaration)
		if len(trail.Supersession.Dispositions) != 1 {
			t.Fatalf("fixture dispositions = %#v, want exactly one", trail.Supersession.Dispositions)
		}
		trail.Supersession = &mousa.SupersessionSelection{
			Consulted:     trail.Supersession.Consulted,
			ActivationID:  trail.Supersession.ActivationID,
			DeclarationID: trail.Supersession.DeclarationID,
			Dispositions:  []mousa.SupersessionDisposition{},
		}
		trail = resealV4Trail(t, trail)
		assertV4CodecAccepts(t, trail)
		commitV4Trail(t, fixture.store, trail)
		if _, err := fixture.store.GetSourceTrail(ctx, trail.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSourceTrail = %v, want %s", err, CodeIntegrity)
		}
	})

	t.Run("suppression row is extra", func(t *testing.T) {
		fixture := newV4Fixture(t)
		defer fixture.store.Close()
		declaration, state := fixture.activate(t)
		input := fixture.retrieve(t, "request-v4-extra-row")
		trail := buildV4Trail(t, input, v4NoSelectionBudget, mousa.PackingOriginal, &state, &declaration)
		withheld := trailCandidateBySegment(t, trail.Candidates, trail.Supersession.Dispositions[0].SegmentID)
		trail.Supersession.Dispositions = append(trail.Supersession.Dispositions, extraSuppressionRow(t, trail, withheld))
		trail = resealV4Trail(t, trail)
		assertV4CodecAccepts(t, trail)
		commitV4Trail(t, fixture.store, trail)
		if _, err := fixture.store.GetSourceTrail(ctx, trail.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSourceTrail = %v, want %s", err, CodeIntegrity)
		}
	})
}

// extraSuppressionRow builds one more disposition row naming another accepted candidate while
// repeating the consulted declaration's identity and pins, the shape a record would need if it
// withheld a candidate the declaration does not pin.
func extraSuppressionRow(t *testing.T, trail mousa.SourceTrail, after mousa.TrailCandidate) mousa.SupersessionDisposition {
	t.Helper()
	row := trail.Supersession.Dispositions[0]
	for _, candidate := range trail.Candidates {
		if candidate.Disposition != mousa.CandidateAccepted || candidate.FinalRank <= after.FinalRank || candidate.SegmentID == after.SegmentID {
			continue
		}
		return mousa.SupersessionDisposition{
			Selection:                   mousa.SupersessionSuperseded,
			SegmentID:                   candidate.SegmentID,
			ContentSHA256:               candidate.ContentSHA256,
			DeclarationID:               row.DeclarationID,
			PredecessorItemID:           row.PredecessorItemID,
			PredecessorRepresentationID: row.PredecessorRepresentationID,
			SuccessorItemID:             row.SuccessorItemID,
			SuccessorRepresentationID:   row.SuccessorRepresentationID,
		}
	}
	t.Fatal("fixture has no accepted candidate after the withheld one")
	return mousa.SupersessionDisposition{}
}

func assertV4CodecAccepts(t *testing.T, trail mousa.SourceTrail) {
	t.Helper()
	data, err := mousa.EncodeSourceTrail(trail)
	if err != nil {
		t.Fatalf("the structural codec rejected the mutated record: %v", err)
	}
	if _, err := mousa.DecodeSourceTrail(data); err != nil {
		t.Fatalf("the structural codec rejected the mutated record: %v", err)
	}
}

// TestStoreV4TrailReadRejectsRecomputedDuplicate turns a constructor-produced duplicate omission
// into a second released survivor with recomputed identities and projection rows. The structural
// codec accepts it because the two released rows are individually consistent, while the canonical
// byte-aware check rejects it because the two passages hold equal bytes.
func TestStoreV4TrailReadRejectsRecomputedDuplicate(t *testing.T) {
	ctx := context.Background()
	fixture := newV4Fixture(t)
	input := fixture.retrieve(t, "request-v4-duplicate")
	trail := buildV4Trail(t, input, 1<<20, mousa.PackingExactV1, nil, nil)
	duplicateIndex := -1
	for index, candidate := range trail.Candidates {
		if candidate.Omission == "duplicate" {
			if duplicateIndex >= 0 {
				t.Fatal("fixture produced more than one duplicate omission")
			}
			duplicateIndex = index
		}
	}
	if duplicateIndex < 0 {
		t.Fatalf("fixture produced no duplicate omission: %#v", trail.Candidates)
	}

	mutated := trail
	mutated.Candidates = append([]mousa.TrailCandidate(nil), trail.Candidates...)
	mutated.Candidates[duplicateIndex].Selected = true
	mutated.Candidates[duplicateIndex].Omission = ""
	mutated.Candidates[duplicateIndex].DuplicateOf = ""
	mutated = resealV4Trail(t, mutated)
	assertV4CodecAccepts(t, mutated)

	commitV4Trail(t, fixture.store, mutated)
	if _, err := fixture.store.GetSourceTrail(ctx, mutated.ID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("GetSourceTrail = %v, want %s", err, CodeIntegrity)
	}

	path := fixture.store.path
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenReadOnly(ctx, path)
	if reopened != nil {
		reopened.Close()
	}
	if !IsCode(err, CodeIntegrity) {
		t.Fatalf("read-only open of the mutated store = %v, want %s", err, CodeIntegrity)
	}
}

func TestStoreV4TrailDamageRejectedWithoutRepair(t *testing.T) {
	ctx := context.Background()
	fixture := newV4Fixture(t)
	defer fixture.store.Close()
	declaration, state := fixture.activate(t)
	input := fixture.retrieve(t, "request-v4-damage")
	valid := storeV4Trail(t, fixture.store, buildV4Trail(t, input, 1<<20, mousa.PackingOriginal, &state, &declaration))

	damaged := buildV4Trail(t, input, 1<<20, mousa.PackingOriginal, &state, &declaration)
	damaged.Supersession.Dispositions[0].PredecessorItemID = "predecessor-alias"
	damaged = resealV4Trail(t, damaged)
	assertV4CodecAccepts(t, damaged)
	commitV4Trail(t, fixture.store, damaged)

	before := canonicalRowCounts(t, fixture.store)
	var beforeBytes []byte
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT record_json FROM source_trails WHERE id = ?`, damaged.ID[:]).Scan(&beforeBytes); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.store.GetSourceTrail(ctx, damaged.ID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("GetSourceTrail of the damaged record = %v, want %s", err, CodeIntegrity)
	}
	// A rejected read must neither repair the record nor write anything else, and a valid record in
	// the same store must stay readable.
	var afterBytes []byte
	if err := fixture.store.db.QueryRowContext(ctx, `SELECT record_json FROM source_trails WHERE id = ?`, damaged.ID[:]).Scan(&afterBytes); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeBytes, afterBytes) {
		t.Fatal("a rejected read rewrote the damaged record")
	}
	if after := canonicalRowCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
		t.Fatalf("canonical rows changed: before %#v after %#v", before, after)
	}
	reread, err := fixture.store.GetSourceTrail(ctx, valid.ID)
	if err != nil || !reflect.DeepEqual(reread, valid) {
		t.Fatalf("valid trail after a rejected read = %#v, %v, want %#v", reread, err, valid)
	}

	path := fixture.store.path
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if reopened != nil {
		reopened.Close()
	}
	if !IsCode(err, CodeIntegrity) {
		t.Fatalf("open of the damaged store = %v, want %s", err, CodeIntegrity)
	}
}

func TestStoreV4TrailHistoryReadableAfterAdministrationAndItemChanges(t *testing.T) {
	ctx := context.Background()
	fixture := newV4Fixture(t)
	defer fixture.store.Close()
	declaration, state := fixture.activate(t)
	input := fixture.retrieve(t, "request-v4-history")
	recorded := storeV4Trail(t, fixture.store, buildV4Trail(t, input, 1<<20, mousa.PackingOriginal, &state, &declaration))

	second := supersessionTestDeclaration(t, fixture.source.ID, "predecessor", fixture.predecessor.ID, "peer", fixture.peer.ID, "example.operations", "the peer replaces the predecessor")
	if err := fixture.store.PutSupersessionDeclaration(ctx, second); err != nil {
		t.Fatalf("PutSupersessionDeclaration: %v", err)
	}
	replacement := activationTestTransition(t, fixture.source.ID, state.CurrentActivationID, second.ID, "replace the declaration", activationBaseUsec+1)
	if err := fixture.store.ApplySupersessionActivation(ctx, replacement); err != nil {
		t.Fatalf("replacement: %v", err)
	}
	deactivation := activationTestTransition(t, fixture.source.ID, replacement.ID, mousa.SupersessionDeclarationID{}, "deactivate the declaration", activationBaseUsec+2)
	if err := fixture.store.ApplySupersessionActivation(ctx, deactivation); err != nil {
		t.Fatalf("deactivation: %v", err)
	}
	_, revised, revisedText := prepareLocalRevision(t, fixture.store, "successor", "sharedterm successor revised evidence")
	if action, err := fixture.store.ActivateLocalItem(ctx, fixture.source.ID, "successor", revised.ID, revisedText); err != nil || action != "updated" {
		t.Fatalf("revise the successor = %q, %v", action, err)
	}

	reread, err := fixture.store.GetSourceTrail(ctx, recorded.ID)
	if err != nil || !reflect.DeepEqual(reread, recorded) {
		t.Fatalf("recorded trail after later administration and item changes = %#v, %v, want %#v", reread, err, recorded)
	}
}

// damageCanonicalParent removes one named canonical parent row through a test-only foreign-key
// bypass and restores the connection setting before it returns. Production write paths enforce those
// foreign keys, so the bypass exists only to produce the damage a partially corrupted store would
// hold; the restored setting is read back so a leaked connection cannot weaken a later case.
func damageCanonicalParent(t *testing.T, store *Store, statement string, id []byte) {
	t.Helper()
	ctx := context.Background()
	writer, err := connect(ctx, store.path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable foreign keys for the damage fixture: %v", err)
	}
	result, err := writer.ExecContext(ctx, statement, id)
	if err != nil {
		t.Fatalf("damage the fixture: %v", err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("damage affected %d rows, %v; want exactly one", affected, err)
	}
	if _, err := writer.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("restore foreign keys after the damage fixture: %v", err)
	}
	var enabled int
	if err := writer.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys after the damage fixture = %d, %v; want enabled", enabled, err)
	}
}

// v4DamageVictim returns the verified candidate whose canonical dependency a damage case removes:
// the withheld candidate of an active consultation, so a suppressed passage's own parent is covered,
// or the first released passage of a record that consulted no history.
func v4DamageVictim(t *testing.T, input v4Retrieval, trail mousa.SourceTrail, withheld bool) mousa.VerifiedLexicalCandidate {
	t.Helper()
	var segmentID mousa.SegmentID
	if withheld {
		if len(trail.Supersession.Dispositions) != 1 {
			t.Fatalf("recorded dispositions = %#v, want exactly one", trail.Supersession.Dispositions)
		}
		segmentID = trail.Supersession.Dispositions[0].SegmentID
	} else {
		for _, candidate := range trail.Candidates {
			if candidate.Selected {
				segmentID = candidate.SegmentID
				break
			}
		}
		if segmentID == (mousa.SegmentID{}) {
			t.Fatal("record released no passage to damage")
		}
	}
	for _, candidate := range input.candidates {
		if candidate.Segment.ID == segmentID {
			return candidate
		}
	}
	t.Fatalf("no verified candidate for segment %s", segmentID)
	return mousa.VerifiedLexicalCandidate{}
}

// TestStoreV4TrailMissingCanonicalParentIsIntegrity removes one canonical parent row a stored v4
// record names and requires the read to report recorded damage. A v4 record is written only after the
// store verified the records it names, so a vanished named row means the record lost a parent rather
// than that the caller asked for an identity that was never stored. Each case proves a valid control
// read first, then that the rejected read neither rewrote the record nor changed canonical rows, and
// finally that reopening the damaged file still fails verification.
func TestStoreV4TrailMissingCanonicalParentIsIntegrity(t *testing.T) {
	ctx := context.Background()
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		for _, consult := range []string{"active", "no-history"} {
			for _, parent := range []string{"segment", "representation"} {
				t.Run(policy+"/"+consult+"/"+parent, func(t *testing.T) {
					fixture := newV4Fixture(t)
					defer fixture.store.Close()
					var state *mousa.SupersessionActivationState
					var declaration *mousa.SupersessionDeclaration
					if consult == "active" {
						activated, current := fixture.activate(t)
						declaration, state = &activated, &current
					}
					input := fixture.retrieve(t, "request-v4-missing-"+parent+"-"+consult+"-"+policy)
					trail := storeV4Trail(t, fixture.store, buildV4Trail(t, input, 1<<20, policy, state, declaration))
					if reread, err := fixture.store.GetSourceTrail(ctx, trail.ID); err != nil || !reflect.DeepEqual(reread, trail) {
						t.Fatalf("control read = %#v, %v, want %#v", reread, err, trail)
					}

					victim := v4DamageVictim(t, input, trail, consult == "active")
					statement := `DELETE FROM segments WHERE id = ?`
					id := victim.Segment.ID[:]
					if parent == "representation" {
						statement = `DELETE FROM representations WHERE id = ?`
						id = victim.Segment.RepresentationID[:]
					}
					damageCanonicalParent(t, fixture.store, statement, id)

					before := canonicalRowCounts(t, fixture.store)
					var beforeRecord []byte
					if err := fixture.store.db.QueryRowContext(ctx, `SELECT record_json FROM source_trails WHERE id = ?`, trail.ID[:]).Scan(&beforeRecord); err != nil {
						t.Fatal(err)
					}
					if _, err := fixture.store.GetSourceTrail(ctx, trail.ID); !IsCode(err, CodeIntegrity) {
						t.Fatalf("stored v4 trail with a missing named %s = %v, want %s", parent, err, CodeIntegrity)
					}
					var afterRecord []byte
					if err := fixture.store.db.QueryRowContext(ctx, `SELECT record_json FROM source_trails WHERE id = ?`, trail.ID[:]).Scan(&afterRecord); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(beforeRecord, afterRecord) {
						t.Fatal("a rejected v4 read rewrote the stored record")
					}
					if after := canonicalRowCounts(t, fixture.store); !reflect.DeepEqual(before, after) {
						t.Fatalf("canonical rows changed: before %#v after %#v", before, after)
					}

					// The in-process read above and startup verification are separate checks: the same
					// damaged file must also be refused when the store is opened again.
					path := fixture.store.path
					if err := fixture.store.Close(); err != nil {
						t.Fatal(err)
					}
					reopened, err := Open(ctx, path)
					if reopened != nil {
						reopened.Close()
					}
					if !IsCode(err, CodeIntegrity) {
						t.Fatalf("open of the damaged store = %v, want %s", err, CodeIntegrity)
					}
				})
			}
		}
	}
}

// TestSourceTrailReaderClassificationBoundary pins the classification the v4 correction must not
// move: an identity that was never stored stays not-found for the trail and for the canonical record
// readers, and a stored pre-v4 trail whose named segment is gone keeps the not-found it has always
// reported on the same damage the v4 case above now classifies.
func TestSourceTrailReaderClassificationBoundary(t *testing.T) {
	ctx := context.Background()
	fixture := newV4Fixture(t)
	defer fixture.store.Close()
	input := fixture.retrieve(t, "request-v4-legacy-boundary")

	traced, err := fixture.store.TraceEnforcedLexical(ctx, input.request, "sharedterm", 10, 1<<20, mousa.PackingExactV1)
	if err != nil {
		t.Fatalf("TraceEnforcedLexical: %v", err)
	}
	if traced.Trail.Schema != mousa.SourceTrailSchemaV2 {
		t.Fatalf("legacy fixture schema = %s, want %s", traced.Trail.Schema, mousa.SourceTrailSchemaV2)
	}
	var released mousa.SegmentID
	for _, candidate := range traced.Trail.Candidates {
		if candidate.Selected {
			released = candidate.SegmentID
			break
		}
	}
	if released == (mousa.SegmentID{}) {
		t.Fatal("legacy fixture released no passage")
	}
	damageCanonicalParent(t, fixture.store, `DELETE FROM segments WHERE id = ?`, released[:])
	if _, err := fixture.store.GetSourceTrail(ctx, traced.Trail.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("stored %s trail with a missing named segment = %v, want %s", traced.Trail.Schema, err, CodeNotFound)
	}

	var absentTrail mousa.SourceTrailID
	absentTrail[0] = 0x5a
	if _, err := fixture.store.GetSourceTrail(ctx, absentTrail); !IsCode(err, CodeNotFound) {
		t.Fatalf("unknown trail identity = %v, want %s", err, CodeNotFound)
	}
	absentSegment := mousa.SegmentID(testDigest("absent segment identity"))
	if _, err := fixture.store.GetSegment(ctx, absentSegment); !IsCode(err, CodeNotFound) {
		t.Fatalf("unknown segment identity = %v, want %s", err, CodeNotFound)
	}
	absentRepresentation := mousa.RepresentationID(testDigest("absent representation identity"))
	if _, err := fixture.store.GetRepresentation(ctx, absentRepresentation); !IsCode(err, CodeNotFound) {
		t.Fatalf("unknown representation identity = %v, want %s", err, CodeNotFound)
	}
}

// TestStoreV4TrailReadRejectsDuplicateAsBudgetOmission rebuilds a constructor-produced duplicate
// omission under the limited budget that already omits it by byte equality, relabels that row as a
// budget omission without a duplicate reference and recomputes the accounting and identities. The
// structural codec accepts the record while the canonical read rejects it, because an unselected
// byte-equal passage must still name the released passage it duplicates whatever omission label it
// carries.
func TestStoreV4TrailReadRejectsDuplicateAsBudgetOmission(t *testing.T) {
	ctx := context.Background()
	fixture := newV4Fixture(t)
	defer fixture.store.Close()
	input := fixture.retrieve(t, "request-v4-budget-duplicate")
	full := buildV4Trail(t, input, 1<<20, mousa.PackingExactV1, nil, nil)
	duplicateIndex := -1
	var budget uint64
	for index, candidate := range full.Candidates {
		if candidate.Omission == "duplicate" {
			if duplicateIndex >= 0 {
				t.Fatal("fixture produced more than one duplicate omission")
			}
			duplicateIndex = index
			continue
		}
		if candidate.Selected {
			budget += candidate.TextBytes
		}
	}
	if duplicateIndex < 0 {
		t.Fatalf("fixture produced no duplicate omission: %#v", full.Candidates)
	}
	limited := storeV4Trail(t, fixture.store, buildV4Trail(t, input, budget, mousa.PackingExactV1, nil, nil))
	if row := limited.Candidates[duplicateIndex]; row.Omission != "duplicate" || row.DuplicateOf == "" {
		t.Fatalf("limited-budget candidate = %#v, want a duplicate omission of the released passage", row)
	}

	mutated := limited
	mutated.Candidates = append([]mousa.TrailCandidate(nil), limited.Candidates...)
	mutated.Candidates[duplicateIndex].Omission = "budget"
	mutated.Candidates[duplicateIndex].DuplicateOf = ""
	mutated = resealV4Trail(t, mutated)
	if mutated.ID == limited.ID {
		t.Fatal("the mutation did not change the trail identity")
	}
	assertV4CodecAccepts(t, mutated)
	commitV4Trail(t, fixture.store, mutated)
	if _, err := fixture.store.GetSourceTrail(ctx, mutated.ID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("GetSourceTrail = %v, want %s", err, CodeIntegrity)
	}
}

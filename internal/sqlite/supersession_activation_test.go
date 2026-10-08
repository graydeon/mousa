package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

// activationBaseUsec is the first recorded transition time; every fixture transition is strictly
// later so the recorded history reads like a real administration sequence.
const activationBaseUsec = int64(1759622400000000)

// migrationActivationBytes and migrationActivationHash pin the exact append-only migration 0013.
const (
	migrationActivationBytes = 3274
	migrationActivationHash  = "2469c6b853acf12b9985b7cd7d2ca4d22ec61117daafd512d9343677c8816755"
)

// activationFixture prepares one local source with three current items, "doc" replaced by the
// "corrected" and "amended" revisions, and one stored declaration for each successor. Every
// revision is indexed because an indexed local revision must also be a current item.
func activationFixture(t *testing.T, store *Store) (mousa.Source, mousa.SupersessionDeclaration, mousa.SupersessionDeclaration) {
	t.Helper()
	ctx := context.Background()
	source, predecessor, predecessorText := prepareLocalRevision(t, store, "doc", "oldterm predecessor evidence")
	_, successor, successorText := prepareLocalRevision(t, store, "corrected", "newterm successor evidence")
	_, amended, amendedText := prepareLocalRevision(t, store, "amended", "amended successor evidence")
	for _, item := range []struct {
		name           string
		representation mousa.RepresentationID
		text           []byte
	}{
		{"doc", predecessor.ID, predecessorText},
		{"corrected", successor.ID, successorText},
		{"amended", amended.ID, amendedText},
	} {
		if action, err := store.ActivateLocalItem(ctx, source.ID, item.name, item.representation, item.text); err != nil || action != "added" {
			t.Fatalf("activate %s = %q, %v", item.name, action, err)
		}
	}
	first := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "corrected", successor.ID, "example.operations", "successor replaces the predecessor")
	second := supersessionTestDeclaration(t, source.ID, "doc", predecessor.ID, "amended", amended.ID, "example.operations", "amended revision replaces the predecessor")
	for _, declaration := range []mousa.SupersessionDeclaration{first, second} {
		if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
			t.Fatalf("put declaration %s: %v", declaration.Basis, err)
		}
	}
	return source, first, second
}

// activationTestTransition builds one transition whose identity is derived from its source,
// expected predecessor and selected declaration.
func activationTestTransition(t testing.TB, sourceID mousa.SourceID, expected mousa.SupersessionActivationID, declaration mousa.SupersessionDeclarationID, reason string, occurredAt int64) mousa.SupersessionActivation {
	t.Helper()
	var expectedPtr *mousa.SupersessionActivationID
	if expected != (mousa.SupersessionActivationID{}) {
		expectedPtr = &expected
	}
	var declarationPtr *mousa.SupersessionDeclarationID
	if declaration != (mousa.SupersessionDeclarationID{}) {
		declarationPtr = &declaration
	}
	id, err := mousa.NewSupersessionActivationID(sourceID, expectedPtr, declarationPtr)
	if err != nil {
		t.Fatalf("NewSupersessionActivationID(): %v", err)
	}
	var reasonPtr *string
	if reason != "" {
		reasonPtr = &reason
	}
	return mousa.SupersessionActivation{
		Schema:                       mousa.SupersessionActivationSchema,
		ID:                           id,
		SourceID:                     sourceID,
		ExpectedPreviousActivationID: expectedPtr,
		DeclarationID:                declarationPtr,
		ActorID:                      "example.operator",
		ActorVersion:                 "console/1.0",
		OccurredAtUsec:               occurredAt,
		Reason:                       reasonPtr,
	}
}

func activationRowCount(t testing.TB, store *Store) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM supersession_activations`).Scan(&count); err != nil {
		t.Fatalf("count supersession activations: %v", err)
	}
	return count
}

func activationStateRowCount(t testing.TB, store *Store) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM supersession_activation_state`).Scan(&count); err != nil {
		t.Fatalf("count supersession activation state: %v", err)
	}
	return count
}

func absentActivationID() mousa.SupersessionActivationID {
	var id mousa.SupersessionActivationID
	id[0] = 0x5a
	return id
}

func TestSupersessionActivationFirstReplacementDeactivationAndReactivation(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	path := store.path
	source, first, second := activationFixture(t, store)

	if _, err := store.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("state before any activation = %v, want %s", err, CodeNotFound)
	}
	beforeCounts := canonicalRowCounts(t, store)
	beforeDigest := canonicalRecordDigest(t, store)
	beforeCandidates, err := store.SearchLexical(ctx, "oldterm OR newterm OR amended", 10)
	if err != nil || len(beforeCandidates) != 3 {
		t.Fatalf("fixture uncurated lexical candidates = %#v, %v", beforeCandidates, err)
	}
	beforeVerified, err := store.SearchVerifiedLexical(ctx, "oldterm OR newterm OR amended", 10)
	if err != nil || len(beforeVerified) != 3 {
		t.Fatalf("fixture verified lexical candidates = %#v, %v", beforeVerified, err)
	}

	initial := activationTestTransition(t, source.ID, mousa.SupersessionActivationID{}, first.ID, "activate the declared successor", activationBaseUsec)
	if err := store.ApplySupersessionActivation(ctx, initial); err != nil {
		t.Fatalf("first activation: %v", err)
	}
	if err := store.ApplySupersessionActivation(ctx, initial); err != nil {
		t.Fatalf("exact retry while current: %v", err)
	}
	if count := activationRowCount(t, store); count != 1 {
		t.Fatalf("stored activations = %d, want 1", count)
	}
	wantState := mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: initial.ID, ActiveDeclarationID: &first.ID}
	if got, err := store.GetSupersessionActivationState(ctx, source.ID); err != nil || !reflect.DeepEqual(got, wantState) {
		t.Fatalf("current state = %#v, %v; want %#v", got, err, wantState)
	}

	// A transition that selects the declaration that is already active changes nothing.
	if err := store.ApplySupersessionActivation(ctx, activationTestTransition(t, source.ID, initial.ID, first.ID, "no change", activationBaseUsec+1)); !IsCode(err, CodeConflict) {
		t.Fatalf("redundant transition = %v, want %s", err, CodeConflict)
	}
	// A second root for a source that already has history is stale, not an initial transition.
	if err := store.ApplySupersessionActivation(ctx, activationTestTransition(t, source.ID, mousa.SupersessionActivationID{}, second.ID, "second root", activationBaseUsec+2)); !IsCode(err, CodeConflict) {
		t.Fatalf("second root = %v, want %s", err, CodeConflict)
	}

	replacement := activationTestTransition(t, source.ID, initial.ID, second.ID, "amended revision replaces it", activationBaseUsec+3)
	if err := store.ApplySupersessionActivation(ctx, replacement); err != nil {
		t.Fatalf("replacement: %v", err)
	}
	wantState = mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: replacement.ID, ActiveDeclarationID: &second.ID}
	if got, err := store.GetSupersessionActivationState(ctx, source.ID); err != nil || !reflect.DeepEqual(got, wantState) {
		t.Fatalf("state after replacement = %#v, %v; want %#v", got, err, wantState)
	}
	// Retrying the first event after the state advanced must not append or roll back anything.
	if err := store.ApplySupersessionActivation(ctx, initial); !IsCode(err, CodeConflict) {
		t.Fatalf("historical retry after advance = %v, want %s", err, CodeConflict)
	}

	deactivation := activationTestTransition(t, source.ID, replacement.ID, mousa.SupersessionDeclarationID{}, "withdraw the declaration", activationBaseUsec+4)
	if err := store.ApplySupersessionActivation(ctx, deactivation); err != nil {
		t.Fatalf("deactivation: %v", err)
	}
	wantState = mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: deactivation.ID}
	if got, err := store.GetSupersessionActivationState(ctx, source.ID); err != nil || !reflect.DeepEqual(got, wantState) {
		t.Fatalf("state after deactivation = %#v, %v; want %#v", got, err, wantState)
	}
	// Deactivation keeps the latest event as the current one, so another deactivation of the same
	// current state is redundant.
	if err := store.ApplySupersessionActivation(ctx, activationTestTransition(t, source.ID, deactivation.ID, mousa.SupersessionDeclarationID{}, "still absent", activationBaseUsec+5)); !IsCode(err, CodeConflict) {
		t.Fatalf("redundant deactivation = %v, want %s", err, CodeConflict)
	}

	reactivation := activationTestTransition(t, source.ID, deactivation.ID, first.ID, "reactivate the first declaration", activationBaseUsec+6)
	if err := store.ApplySupersessionActivation(ctx, reactivation); err != nil {
		t.Fatalf("reactivation: %v", err)
	}
	wantState = mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: reactivation.ID, ActiveDeclarationID: &first.ID}
	if got, err := store.GetSupersessionActivationState(ctx, source.ID); err != nil || !reflect.DeepEqual(got, wantState) {
		t.Fatalf("state after reactivation = %#v, %v; want %#v", got, err, wantState)
	}
	if count := activationRowCount(t, store); count != 4 {
		t.Fatalf("stored activations = %d, want 4", count)
	}
	if count := activationStateRowCount(t, store); count != 1 {
		t.Fatalf("stored activation states = %d, want 1", count)
	}

	for _, want := range []mousa.SupersessionActivation{initial, replacement, deactivation, reactivation} {
		got, err := store.GetSupersessionActivation(ctx, want.ID)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("historical read of %v = %#v, %v", want.Reason, got, err)
		}
	}
	if _, err := store.GetSupersessionActivation(ctx, absentActivationID()); !IsCode(err, CodeNotFound) {
		t.Fatalf("absent activation read = %v, want %s", err, CodeNotFound)
	}

	// Activation records administration state only: no canonical row, item pointer, index row or
	// selected evidence may change.
	if after := canonicalRowCounts(t, store); !reflect.DeepEqual(after, beforeCounts) {
		t.Fatalf("activation changed canonical rows: before=%v after=%v", beforeCounts, after)
	}
	if after := canonicalRecordDigest(t, store); after != beforeDigest {
		t.Fatalf("activation changed canonical records: before=%s after=%s", beforeDigest, after)
	}
	// The activation moves no item pointer: every current item still points at the revision the
	// fixture activated.
	for _, pin := range []struct {
		item     string
		revision mousa.RepresentationID
	}{
		{first.PredecessorItemID, first.PredecessorRepresentationID},
		{first.SuccessorItemID, first.SuccessorRepresentationID},
		{second.SuccessorItemID, second.SuccessorRepresentationID},
	} {
		item, err := store.GetLocalItem(ctx, source.ID, pin.item)
		if err != nil || !item.Active || item.RepresentationID != pin.revision {
			t.Fatalf("current item %q = %#v, %v; want the fixture revision", pin.item, item, err)
		}
	}
	afterCandidates, err := store.SearchLexical(ctx, "oldterm OR newterm OR amended", 10)
	if err != nil || !reflect.DeepEqual(afterCandidates, beforeCandidates) {
		t.Fatalf("activation changed uncurated lexical evidence: before=%#v after=%#v err=%v", beforeCandidates, afterCandidates, err)
	}
	afterVerified, err := store.SearchVerifiedLexical(ctx, "oldterm OR newterm OR amended", 10)
	if err != nil || !reflect.DeepEqual(afterVerified, beforeVerified) {
		t.Fatalf("activation changed verified lexical evidence: before=%#v after=%#v err=%v", beforeVerified, afterVerified, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	if got, err := reopened.GetSupersessionActivationState(ctx, source.ID); err != nil || !reflect.DeepEqual(got, wantState) {
		t.Fatalf("reopened current state = %#v, %v; want %#v", got, err, wantState)
	}
	for _, want := range []mousa.SupersessionActivation{initial, replacement, deactivation, reactivation} {
		if got, err := reopened.GetSupersessionActivation(ctx, want.ID); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("reopened historical read of %v = %#v, %v", want.Reason, got, err)
		}
	}
}

func TestSupersessionActivationCompetingTransitionsCommitOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "activation-competition.sqlite")
	left, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	source, first, second := activationFixture(t, left)
	_, third, _ := prepareLocalRevision(t, left, "revised", "revised successor evidence")
	thirdDeclaration := supersessionTestDeclaration(t, source.ID, "doc", first.PredecessorRepresentationID, "revised", third.ID, "example.operations", "revised revision replaces the predecessor")
	if err := left.PutSupersessionDeclaration(ctx, thirdDeclaration); err != nil {
		t.Fatal(err)
	}
	initial := activationTestTransition(t, source.ID, mousa.SupersessionActivationID{}, first.ID, "activate the declared successor", activationBaseUsec)
	if err := left.ApplySupersessionActivation(ctx, initial); err != nil {
		t.Fatal(err)
	}

	right, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()

	// Both connections name the same current event and select a different declaration, so the
	// loser must observe that the state advanced instead of forking the chain.
	branchA := activationTestTransition(t, source.ID, initial.ID, second.ID, "competitor A", activationBaseUsec+1)
	branchB := activationTestTransition(t, source.ID, initial.ID, thirdDeclaration.ID, "competitor B", activationBaseUsec+2)
	if err := left.ApplySupersessionActivation(ctx, branchA); err != nil {
		t.Fatalf("competitor A: %v", err)
	}
	if err := right.ApplySupersessionActivation(ctx, branchB); !IsCode(err, CodeConflict) {
		t.Fatalf("competitor B = %v, want %s", err, CodeConflict)
	}
	if count := activationRowCount(t, left); count != 2 {
		t.Fatalf("stored activations = %d, want 2", count)
	}
	winner := mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: branchA.ID, ActiveDeclarationID: &second.ID}
	if got, err := right.GetSupersessionActivationState(ctx, source.ID); err != nil || !reflect.DeepEqual(got, winner) {
		t.Fatalf("state after competition = %#v, %v; want %#v", got, err, winner)
	}
	// The same selection is valid on its own turn: only the stale expectation was refused.
	later := activationTestTransition(t, source.ID, branchA.ID, thirdDeclaration.ID, "competitor B reissued", activationBaseUsec+3)
	if err := right.ApplySupersessionActivation(ctx, later); err != nil {
		t.Fatalf("reissued competitor B: %v", err)
	}
	if count := activationRowCount(t, left); count != 3 {
		t.Fatalf("stored activations = %d, want 3", count)
	}

	t.Run("simultaneous writes", func(t *testing.T) {
		type result struct {
			name string
			err  error
		}
		raceA := activationTestTransition(t, source.ID, later.ID, second.ID, "simultaneous A", activationBaseUsec+4)
		raceB := activationTestTransition(t, source.ID, later.ID, thirdDeclaration.ID, "simultaneous B", activationBaseUsec+5)
		before := activationRowCount(t, left)
		results := make([]result, 2)
		var wait sync.WaitGroup
		for index, item := range []struct {
			name   string
			store  *Store
			record mousa.SupersessionActivation
		}{{"A", left, raceA}, {"B", right, raceB}} {
			wait.Add(1)
			go func(index int, name string, store *Store, record mousa.SupersessionActivation) {
				defer wait.Done()
				results[index] = result{name: name, err: store.ApplySupersessionActivation(ctx, record)}
			}(index, item.name, item.store, item.record)
		}
		wait.Wait()
		committed := 0
		var winnerID mousa.SupersessionActivationID
		var winnerStore *Store
		for _, item := range results {
			if item.err == nil {
				committed++
				winnerStore = left
				if item.name == "A" {
					winnerID = raceA.ID
				} else {
					winnerID = raceB.ID
					winnerStore = right
				}
				continue
			}
			if !IsCode(item.err, CodeConflict) {
				t.Fatalf("losing write %s = %v, want %s", item.name, item.err, CodeConflict)
			}
		}
		if committed != 1 {
			t.Fatalf("simultaneous writes committed %d transitions, want 1", committed)
		}
		if after := activationRowCount(t, winnerStore); after != before+1 {
			t.Fatalf("stored activations = %d, want %d", after, before+1)
		}
		state, err := winnerStore.GetSupersessionActivationState(ctx, source.ID)
		if err != nil || state.CurrentActivationID != winnerID {
			t.Fatalf("state after simultaneous writes = %#v, %v; want current %s", state, err, winnerID)
		}
	})
}

func TestSupersessionActivationRejectsWrongSourceAndMissingReferences(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	source, first, _ := activationFixture(t, store)
	defer store.Close()
	foreign := foreignDeclaration(t, store)
	initial := activationTestTransition(t, source.ID, mousa.SupersessionActivationID{}, first.ID, "activate the declared successor", activationBaseUsec)
	if err := store.ApplySupersessionActivation(ctx, initial); err != nil {
		t.Fatal(err)
	}

	absentSource, err := mousa.NewSourceID("mousa-local", "absent-activation-source")
	if err != nil {
		t.Fatal(err)
	}
	absentTransition := activationTestTransition(t, absentSource, mousa.SupersessionActivationID{}, first.ID, "absent source", activationBaseUsec+1)
	absentPredecessor := absentActivationID()
	cases := []struct {
		name  string
		build func() mousa.SupersessionActivation
		code  Code
	}{
		{"absent source", func() mousa.SupersessionActivation {
			return activationTestTransition(t, absentSource, initial.ID, mousa.SupersessionDeclarationID{}, "absent source", activationBaseUsec+1)
		}, CodeNotFound},
		{"absent declaration", func() mousa.SupersessionActivation {
			return activationTestTransition(t, source.ID, initial.ID, absentActivationDeclarationID(), "absent declaration", activationBaseUsec+2)
		}, CodeConflict},
		{"declaration from another source", func() mousa.SupersessionActivation {
			return activationTestTransition(t, source.ID, initial.ID, foreign.ID, "another source", activationBaseUsec+3)
		}, CodeConflict},
		{"absent predecessor event", func() mousa.SupersessionActivation {
			return activationTestTransition(t, source.ID, absentPredecessor, first.ID, "absent predecessor", activationBaseUsec+4)
		}, CodeConflict},
		{"deactivation without history", func() mousa.SupersessionActivation {
			return activationTestTransition(t, emptySourceID(t, store), initial.ID, mousa.SupersessionDeclarationID{}, "no history", activationBaseUsec+5)
		}, CodeConflict},
		{"absent source with reused declaration", func() mousa.SupersessionActivation { return absentTransition }, CodeNotFound},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			beforeEvents := activationRowCount(t, store)
			beforeStates := activationStateRowCount(t, store)
			if err := store.ApplySupersessionActivation(ctx, testCase.build()); !IsCode(err, testCase.code) {
				t.Fatalf("ApplySupersessionActivation = %v, want %s", err, testCase.code)
			}
			if after := activationRowCount(t, store); after != beforeEvents {
				t.Fatalf("rejected transition left %d events, want %d", after, beforeEvents)
			}
			if after := activationStateRowCount(t, store); after != beforeStates {
				t.Fatalf("rejected transition left %d states, want %d", after, beforeStates)
			}
			if got, err := store.GetSupersessionActivationState(ctx, source.ID); err != nil || !reflect.DeepEqual(got, mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: initial.ID, ActiveDeclarationID: &first.ID}) {
				t.Fatalf("rejected transition changed current state: %#v, %v", got, err)
			}
		})
	}

	// A source with no activation history can never receive a deactivation event: the transition
	// that would record one is not a recordable first transition.
	noHistory := activationTestTransition(t, emptySourceID(t, store), mousa.SupersessionActivationID{}, mousa.SupersessionDeclarationID{}, "deactivate nothing", activationBaseUsec+6)
	if err := store.ApplySupersessionActivation(ctx, noHistory); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("deactivation of a source with no history = %v, want %s", err, CodeInvalidRecord)
	}
	if count := activationRowCount(t, store); count != 1 {
		t.Fatalf("stored activations = %d, want 1", count)
	}
	// A structurally valid deactivation is still refused when the named predecessor event does not
	// exist for that source, so the store never invents the history a deactivation implies.
	if err := store.ApplySupersessionActivation(ctx, activationTestTransition(t, emptySourceID(t, store), initial.ID, mousa.SupersessionDeclarationID{}, "stale deactivation", activationBaseUsec+7)); !IsCode(err, CodeConflict) {
		t.Fatalf("deactivation with an unknown predecessor = %v, want %s", err, CodeConflict)
	}
}

func absentActivationDeclarationID() mousa.SupersessionDeclarationID {
	var id mousa.SupersessionDeclarationID
	id[0] = 0x6b
	return id
}

// foreignDeclaration stores one declaration that belongs to a different source than the source
// under test, so a transition can name the wrong source while the declaration exists.
func foreignDeclaration(t testing.TB, store *Store) mousa.SupersessionDeclaration {
	t.Helper()
	ctx := context.Background()
	foreignSource, err := mousa.NewSourceID("mousa-local", "activation-foreign-source")
	if err != nil {
		t.Fatal(err)
	}
	source := mousa.Source{Schema: mousa.SourceSchema, ID: foreignSource, Namespace: "mousa-local", ExternalSourceID: "activation-foreign-source"}
	if err := store.PutSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	digest := testDigest("foreign activation evidence")
	external := fmt.Sprintf("item/%s@%x", "foreign", digest)
	observationID, err := mousa.NewObservationID(foreignSource, external)
	if err != nil {
		t.Fatal(err)
	}
	observation := mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: foreignSource, ExternalObservationID: external}
	artifactID, err := mousa.NewArtifactID(observationID, "body")
	if err != nil {
		t.Fatal(err)
	}
	artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: "body", MediaType: mousa.UTF8TextMediaType, ContentSHA256: digest, ByteLength: uint64(len("foreign activation evidence"))}
	batch := mousa.IngestBatch{AdapterID: "test", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem, CapturedAtUsec: 1, Source: source, Observation: observation, Artifacts: []mousa.Artifact{artifact}}
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	representation, _, err := mousa.NormalizeUTF8TextWithPolicy(artifact, []byte("foreign activation evidence"), mousa.TextSegmentFixedV1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepresentation(ctx, representation); err != nil {
		t.Fatal(err)
	}
	digest = testDigest("foreign activation successor evidence")
	external = fmt.Sprintf("item/%s@%x", "foreign-successor", digest)
	observationID, err = mousa.NewObservationID(foreignSource, external)
	if err != nil {
		t.Fatal(err)
	}
	observation = mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: foreignSource, ExternalObservationID: external}
	artifactID, err = mousa.NewArtifactID(observationID, "body")
	if err != nil {
		t.Fatal(err)
	}
	artifact = mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: "body", MediaType: mousa.UTF8TextMediaType, ContentSHA256: digest, ByteLength: uint64(len("foreign activation successor evidence"))}
	batch = mousa.IngestBatch{AdapterID: "test", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem, CapturedAtUsec: 2, Source: source, Observation: observation, Artifacts: []mousa.Artifact{artifact}}
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	successor, _, err := mousa.NormalizeUTF8TextWithPolicy(artifact, []byte("foreign activation successor evidence"), mousa.TextSegmentFixedV1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepresentation(ctx, successor); err != nil {
		t.Fatal(err)
	}
	declaration := supersessionTestDeclaration(t, foreignSource, "foreign", representation.ID, "foreign-successor", successor.ID, "example.operations", "declaration of another source")
	if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
		t.Fatal(err)
	}
	return declaration
}

// emptySourceID stores one source that has no activation history at all.
func emptySourceID(t testing.TB, store *Store) mousa.SourceID {
	t.Helper()
	id, err := mousa.NewSourceID("mousa-local", "activation-"+strings.ReplaceAll(t.Name(), "/", "-"))
	if err != nil {
		t.Fatal(err)
	}
	record := mousa.Source{Schema: mousa.SourceSchema, ID: id, Namespace: "mousa-local", ExternalSourceID: "activation-" + strings.ReplaceAll(t.Name(), "/", "-")}
	if err := store.PutSource(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSupersessionActivationReadOnlyCancelledAndRolledBackWrites(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	path := store.path
	source, first, second := activationFixture(t, store)
	initial := activationTestTransition(t, source.ID, mousa.SupersessionActivationID{}, first.ID, "activate the declared successor", activationBaseUsec)
	replacement := activationTestTransition(t, source.ID, initial.ID, second.ID, "replacement", activationBaseUsec+1)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := readOnly.ApplySupersessionActivation(ctx, initial); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only apply = %v, want %s", err, CodeReadOnly)
	}
	if _, err := readOnly.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("read-only state = %v, want %s", err, CodeNotFound)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.ApplySupersessionActivation(cancelled, initial); err == nil {
		t.Fatal("cancelled apply succeeded")
	}
	if count := activationRowCount(t, store); count != 0 {
		t.Fatalf("cancelled write left %d activations", count)
	}
	if count := activationStateRowCount(t, store); count != 0 {
		t.Fatalf("cancelled write left %d states", count)
	}
	if err := store.ApplySupersessionActivation(ctx, initial); err != nil {
		t.Fatalf("write after cancellation: %v", err)
	}

	// Missing current state with retained history is corruption. Reject a new root without
	// appending an event or reconstructing the projection.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM supersession_activation_state`); err != nil {
		t.Fatal(err)
	}
	forked := activationTestTransition(t, source.ID, mousa.SupersessionActivationID{}, second.ID, "second root", activationBaseUsec+2)
	if err := store.ApplySupersessionActivation(ctx, forked); !IsCode(err, CodeIntegrity) {
		t.Fatalf("second root with missing state = %v, want %s", err, CodeIntegrity)
	}
	if count := activationRowCount(t, store); count != 1 {
		t.Fatalf("rolled-back write left %d activations, want 1", count)
	}
	if count := activationStateRowCount(t, store); count != 0 {
		t.Fatalf("rolled-back write left %d states, want 0", count)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO supersession_activation_state(source_id, current_activation_id, active_declaration_id) VALUES(?, ?, ?)`, source.ID[:], initial.ID[:], first.ID[:]); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplySupersessionActivation(ctx, replacement); err != nil {
		t.Fatalf("replacement after rollback: %v", err)
	}
	if count := activationRowCount(t, store); count != 2 {
		t.Fatalf("stored activations = %d, want 2", count)
	}
}

func TestSupersessionActivationTamperingFailsClosed(t *testing.T) {
	ctx := context.Background()

	t.Run("state projection replaced", func(t *testing.T) {
		store, source, tip := activatedChain(t)
		defer store.Close()
		if _, err := store.db.ExecContext(ctx, `UPDATE supersession_activation_state SET active_declaration_id = ? WHERE source_id = ?`, tip.declaration.First.ID[:], source.ID[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSupersessionActivationState = %v, want %s", err, CodeIntegrity)
		}
		if err := verifySupersessionActivationRecords(ctx, store.db); !IsCode(err, CodeIntegrity) {
			t.Fatalf("verifySupersessionActivationRecords = %v, want %s", err, CodeIntegrity)
		}
		assertOpenFailsClosed(t, store)
	})

	t.Run("state points at an older event", func(t *testing.T) {
		store, source, tip := activatedChain(t)
		defer store.Close()
		if _, err := store.db.ExecContext(ctx, `UPDATE supersession_activation_state SET current_activation_id = ?, active_declaration_id = ? WHERE source_id = ?`, tip.initial.ID[:], tip.declaration.First.ID[:], source.ID[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSupersessionActivationState = %v, want %s", err, CodeIntegrity)
		}
		if err := verifySupersessionActivationRecords(ctx, store.db); !IsCode(err, CodeIntegrity) {
			t.Fatalf("verifySupersessionActivationRecords = %v, want %s", err, CodeIntegrity)
		}
		assertOpenFailsClosed(t, store)
	})

	t.Run("event record replaced", func(t *testing.T) {
		store, _, tip := activatedChain(t)
		defer store.Close()
		replacementBytes, err := mousa.EncodeSupersessionActivation(tip.initial)
		if err != nil {
			t.Fatal(err)
		}
		otherBytes, err := mousa.EncodeSupersessionActivation(tip.replacement)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(replacementBytes, otherBytes) {
			t.Fatal("fixture transitions are not distinct")
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE supersession_activations SET record_json = ? WHERE id = ?`, otherBytes, tip.initial.ID[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSupersessionActivation(ctx, tip.initial.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSupersessionActivation = %v, want %s", err, CodeIntegrity)
		}
		if err := verifySupersessionActivationRecords(ctx, store.db); !IsCode(err, CodeIntegrity) {
			t.Fatalf("verifySupersessionActivationRecords = %v, want %s", err, CodeIntegrity)
		}
		assertOpenFailsClosed(t, store)
	})

	t.Run("event declaration projection replaced", func(t *testing.T) {
		store, _, tip := activatedChain(t)
		defer store.Close()
		if _, err := store.db.ExecContext(ctx, `UPDATE supersession_activations SET declaration_id = ? WHERE id = ?`, tip.declaration.Second.ID[:], tip.initial.ID[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSupersessionActivation(ctx, tip.initial.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSupersessionActivation = %v, want %s", err, CodeIntegrity)
		}
		if err := verifySupersessionActivationRecords(ctx, store.db); !IsCode(err, CodeIntegrity) {
			t.Fatalf("verifySupersessionActivationRecords = %v, want %s", err, CodeIntegrity)
		}
		assertOpenFailsClosed(t, store)
	})

	t.Run("current state removed", func(t *testing.T) {
		store, source, _ := activatedChain(t)
		defer store.Close()
		if _, err := store.db.ExecContext(ctx, `DELETE FROM supersession_activation_state`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSupersessionActivationState = %v, want %s", err, CodeIntegrity)
		}
		if err := verifySupersessionActivationRecords(ctx, store.db); !IsCode(err, CodeIntegrity) {
			t.Fatalf("verifySupersessionActivationRecords = %v, want %s", err, CodeIntegrity)
		}
		assertOpenFailsClosed(t, store)
	})

	t.Run("forked chain", func(t *testing.T) {
		store, source, tip := activatedChain(t)
		defer store.Close()
		if _, err := store.db.ExecContext(ctx, `DROP INDEX supersession_activations_predecessor_idx`); err != nil {
			t.Fatal(err)
		}
		forged := activationTestTransition(t, source.ID, tip.initial.ID, tip.declaration.First.ID, "forged branch", activationBaseUsec+9)
		forgedBytes, err := mousa.EncodeSupersessionActivation(forged)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `INSERT INTO supersession_activations(id, source_id, expected_previous_activation_id, declaration_id, record_json) VALUES(?, ?, ?, ?, ?)`,
			forged.ID[:], source.ID[:], forged.ExpectedPreviousActivationID[:], forged.DeclarationID[:], forgedBytes); err != nil {
			t.Fatalf("forged insert: %v", err)
		}
		if err := verifySupersessionActivationRecords(ctx, store.db); !IsCode(err, CodeIntegrity) {
			t.Fatalf("verifySupersessionActivationRecords = %v, want %s", err, CodeIntegrity)
		}
		// The dropped index is also a missing required object, so the store fails closed on open for
		// two independent reasons.
		assertOpenFailsClosed(t, store)
	})

	t.Run("tampered declaration behind the state", func(t *testing.T) {
		store, source, tip := activatedChain(t)
		defer store.Close()
		if _, err := store.db.ExecContext(ctx, `DELETE FROM representation_inputs WHERE representation_id = ?`, tip.declaration.First.PredecessorRepresentationID[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := store.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, CodeIntegrity) {
			t.Fatalf("GetSupersessionActivationState = %v, want %s", err, CodeIntegrity)
		}
		if err := verifySupersessionActivationRecords(ctx, store.db); !IsCode(err, CodeIntegrity) {
			t.Fatalf("verifySupersessionActivationRecords = %v, want %s", err, CodeIntegrity)
		}
	})
}

// assertOpenFailsClosed closes the store and requires a reopen to refuse the damaged database.
func assertOpenFailsClosed(t *testing.T, store *Store) {
	t.Helper()
	path := store.path
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), path)
	if reopened != nil {
		reopened.Close()
	}
	if !IsCode(err, CodeIntegrity) {
		t.Fatalf("open of the damaged store = %v, want %s", err, CodeIntegrity)
	}
}

type activationChain struct {
	initial     mousa.SupersessionActivation
	replacement mousa.SupersessionActivation
	declaration struct {
		First  mousa.SupersessionDeclaration
		Second mousa.SupersessionDeclaration
	}
}

// activatedChain builds one root activation followed by one replacement.
func activatedChain(t *testing.T) (*Store, mousa.Source, activationChain) {
	t.Helper()
	ctx := context.Background()
	store := openLexicalStore(t)
	source, first, second := activationFixture(t, store)
	initial := activationTestTransition(t, source.ID, mousa.SupersessionActivationID{}, first.ID, "activate the declared successor", activationBaseUsec)
	replacement := activationTestTransition(t, source.ID, initial.ID, second.ID, "replacement", activationBaseUsec+1)
	for _, record := range []mousa.SupersessionActivation{initial, replacement} {
		if err := store.ApplySupersessionActivation(ctx, record); err != nil {
			t.Fatalf("apply %v: %v", record.Reason, err)
		}
	}
	chain := activationChain{initial: initial, replacement: replacement}
	chain.declaration.First = first
	chain.declaration.Second = second
	return store, source, chain
}

func TestSupersessionActivationMigrationExactBytesHashAndObjects(t *testing.T) {
	ctx := context.Background()
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 14 {
		t.Fatalf("embedded migrations = %d, want 14", len(migrations))
	}
	if migrations[12].version != 13 || migrations[12].name != "supersession_activations" {
		t.Fatalf("migration 13 = %#v, want version 13 name supersession_activations", migrations[12])
	}
	if len(migrations[12].sql) != migrationActivationBytes || fmt.Sprintf("%x", migrations[12].hash) != migrationActivationHash || migrations[12].sql[len(migrations[12].sql)-1] != '\n' {
		t.Fatalf("migration 13 bytes/hash/newline = %d/%x/%v", len(migrations[12].sql), migrations[12].hash, migrations[12].sql[len(migrations[12].sql)-1] == '\n')
	}
	store, err := Open(ctx, filepath.Join(t.TempDir(), "activation-migration.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, table := range []string{"supersession_activations", "supersession_activation_state"} {
		var createSQL string
		if err := store.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?`, table).Scan(&createSQL); err != nil {
			t.Fatalf("%s table: %v", table, err)
		}
		if !strings.Contains(createSQL, "STRICT") {
			t.Fatalf("%s definition is not STRICT: %s", table, createSQL)
		}
	}
	for _, index := range []string{"supersession_activations_predecessor_idx", "supersession_activations_root_idx", "supersession_declarations_id_source_idx"} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE type = 'index' AND name = ?`, index).Scan(&count); err != nil || count != 1 {
			t.Fatalf("index %s count = %d, %v", index, count, err)
		}
	}
	// The composite parent key is the only index migration 0013 adds to the declaration table.
	var declarationsIndexes []string
	rows, err := store.db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type = 'index' AND tbl_name = 'supersession_declarations' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		declarationsIndexes = append(declarationsIndexes, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(declarationsIndexes, []string{"supersession_declarations_id_source_idx"}) {
		t.Fatalf("supersession_declarations indexes = %v", declarationsIndexes)
	}
	if err := verifyVersion(ctx, store.db, migrations, len(migrations), true, false); err != nil {
		t.Fatalf("startup verification at version %d: %v", len(migrations), err)
	}
	var storedHash []byte
	if err := store.db.QueryRowContext(ctx, `SELECT sha256 FROM schema_migrations WHERE version = 13 AND name = 'supersession_activations'`).Scan(&storedHash); err != nil || !bytes.Equal(storedHash, migrations[12].hash[:]) {
		t.Fatalf("stored migration hash = %x, %v", storedHash, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE schema_migrations SET sha256 = zeroblob(32) WHERE version = 13`); err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, store.db, migrations, len(migrations), true, false); !IsCode(err, CodeIntegrity) {
		t.Fatalf("verifyVersion with a rewritten migration hash = %v, want %s", err, CodeIntegrity)
	}
}

func TestSupersessionActivationMigrationUpgradesPriorVersionWithoutChangingRecords(t *testing.T) {
	ctx := context.Background()
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	store := openLexicalStore(t)
	path := store.path
	source, first, _ := activationFixture(t, store)
	initial := activationTestTransition(t, source.ID, mousa.SupersessionActivationID{}, first.ID, "activate the declared successor", activationBaseUsec)
	if err := store.ApplySupersessionActivation(ctx, initial); err != nil {
		t.Fatal(err)
	}
	beforeDigest := canonicalRecordDigest(t, store)
	beforeCandidates, err := store.SearchLexical(ctx, "oldterm OR newterm OR amended", 10)
	if err != nil || len(beforeCandidates) != 3 {
		t.Fatalf("fixture uncurated lexical candidates = %#v, %v", beforeCandidates, err)
	}
	beforeDeclarations := supersessionDeclarationRowCount(t, store)

	// Synthesize version 12: it has no activation tables and no composite parent-key index on the
	// declaration table, and this slice must change no canonical record or declaration on the way.
	if _, err := store.db.ExecContext(ctx, `DROP TABLE supersession_activation_state; DROP TABLE supersession_activations; DROP INDEX supersession_declarations_id_source_idx; DELETE FROM schema_migrations WHERE version > 12`); err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, store.db, migrations, 12, true, false); err != nil {
		t.Fatalf("invalid version-12 fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if readOnly, err := OpenReadOnly(ctx, path); readOnly != nil || !IsCode(err, CodeReadOnly) {
		if readOnly != nil {
			readOnly.Close()
		}
		t.Fatalf("read-only version-12 open = %v, %v", readOnly, err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("upgrade open: %v", err)
	}
	defer store.Close()
	if afterDigest := canonicalRecordDigest(t, store); afterDigest != beforeDigest {
		t.Fatalf("upgrade changed canonical records: before=%s after=%s", beforeDigest, afterDigest)
	}
	afterCandidates, err := store.SearchLexical(ctx, "oldterm OR newterm OR amended", 10)
	if err != nil || !reflect.DeepEqual(afterCandidates, beforeCandidates) {
		t.Fatalf("upgrade changed lexical evidence: before=%#v after=%#v err=%v", beforeCandidates, afterCandidates, err)
	}
	if after := supersessionDeclarationRowCount(t, store); after != beforeDeclarations {
		t.Fatalf("upgrade changed declarations: before=%d after=%d", beforeDeclarations, after)
	}
	if got, err := store.GetSupersessionDeclaration(ctx, first.ID); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("declaration after upgrade = %#v, %v", got, err)
	}
	if count := activationRowCount(t, store); count != 0 {
		t.Fatalf("upgraded store holds %d activations, want 0", count)
	}
	if _, err := store.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("upgraded state = %v, want %s", err, CodeNotFound)
	}
	backupPath := path + ".pre-migrate-v12-to-v13.sqlite"
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("pre-migration backup: %v", err)
	}
	backup, err := connect(ctx, backupPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := verifyVersion(ctx, backup, migrations, 12, false, true); err != nil {
		t.Fatalf("verify version-12 backup: %v", err)
	}
}

func TestSupersessionActivationKeepsDeclarationPinsAfterItemUpdateAndDeletion(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	path := store.path
	source, first, second := activationFixture(t, store)
	initial := activationTestTransition(t, source.ID, mousa.SupersessionActivationID{}, first.ID, "activate the declared successor", activationBaseUsec)
	replacement := activationTestTransition(t, source.ID, initial.ID, second.ID, "replacement", activationBaseUsec+1)
	for _, record := range []mousa.SupersessionActivation{initial, replacement} {
		if err := store.ApplySupersessionActivation(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	_, updated, updatedText := prepareLocalRevision(t, store, "doc", "updated predecessor evidence")
	if action, err := store.ActivateLocalItem(ctx, source.ID, "doc", updated.ID, updatedText); err != nil || action != "updated" {
		t.Fatalf("update doc = %q, %v", action, err)
	}
	if action, err := store.DeleteLocalItem(ctx, source.ID, "amended"); err != nil || action != "deleted" {
		t.Fatalf("delete amended = %q, %v", action, err)
	}
	wantState := mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: replacement.ID, ActiveDeclarationID: &second.ID}
	if got, err := store.GetSupersessionActivationState(ctx, source.ID); err != nil || !reflect.DeepEqual(got, wantState) {
		t.Fatalf("state after item changes = %#v, %v; want %#v", got, err, wantState)
	}
	// The declaration keeps its exact historical pins; the item update never retargets them.
	gotDeclaration, err := store.GetSupersessionDeclaration(ctx, second.ID)
	if err != nil || !reflect.DeepEqual(gotDeclaration, second) {
		t.Fatalf("declaration after item changes = %#v, %v", gotDeclaration, err)
	}
	if gotDeclaration.SuccessorRepresentationID == updated.ID {
		t.Fatal("declaration pin was retargeted to the updated revision")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen after item changes: %v", err)
	}
	defer reopened.Close()
	if got, err := reopened.GetSupersessionActivationState(ctx, source.ID); err != nil || !reflect.DeepEqual(got, wantState) {
		t.Fatalf("reopened state after item changes = %#v, %v; want %#v", got, err, wantState)
	}
	for _, want := range []mousa.SupersessionActivation{initial, replacement} {
		if got, err := reopened.GetSupersessionActivation(ctx, want.ID); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("reopened read of %v = %#v, %v", want.Reason, got, err)
		}
	}
}

func TestSupersessionActivationRejectsCorruptStateBeforeReplacement(t *testing.T) {
	ctx := context.Background()
	store, source, chain := activatedChain(t)
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `UPDATE supersession_activation_state SET active_declaration_id = ? WHERE source_id = ?`, chain.declaration.First.ID[:], source.ID[:]); err != nil {
		t.Fatal(err)
	}
	before := activationRowCount(t, store)
	transition := activationTestTransition(t, source.ID, chain.replacement.ID, mousa.SupersessionDeclarationID{}, "deactivate", activationBaseUsec+2)
	if err := store.ApplySupersessionActivation(ctx, transition); !IsCode(err, CodeIntegrity) {
		t.Fatalf("replacement over corrupt current projection = %v, want %s", err, CodeIntegrity)
	}
	if got := activationRowCount(t, store); got != before {
		t.Fatalf("rejected replacement changed event count: got %d, want %d", got, before)
	}
	var current, selected []byte
	if err := store.db.QueryRowContext(ctx, `SELECT current_activation_id, active_declaration_id FROM supersession_activation_state WHERE source_id = ?`, source.ID[:]).Scan(&current, &selected); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, chain.replacement.ID[:]) || !bytes.Equal(selected, chain.declaration.First.ID[:]) {
		t.Fatal("rejected replacement rewrote the damaged projection")
	}
}

func TestSupersessionActivationMissingStateIsIntegrityFailure(t *testing.T) {
	ctx := context.Background()
	store, source, _ := activatedChain(t)
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `DELETE FROM supersession_activation_state WHERE source_id = ?`, source.ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("state read with orphan activation history = %v, want %s", err, CodeIntegrity)
	}
}

func TestSupersessionActivationMissingStateRejectsNewTransitions(t *testing.T) {
	for _, root := range []bool{false, true} {
		name := "replacement"
		if root {
			name = "root"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, source, chain := activatedChain(t)
			defer store.Close()
			if _, err := store.db.ExecContext(ctx, `DELETE FROM supersession_activation_state WHERE source_id = ?`, source.ID[:]); err != nil {
				t.Fatal(err)
			}
			expected := chain.replacement.ID
			selected := mousa.SupersessionDeclarationID{}
			if root {
				expected = mousa.SupersessionActivationID{}
				selected = chain.declaration.Second.ID
			}
			transition := activationTestTransition(t, source.ID, expected, selected, "new transition", activationBaseUsec+2)
			if err := store.ApplySupersessionActivation(ctx, transition); !IsCode(err, CodeIntegrity) {
				t.Fatalf("transition with orphan history = %v, want %s", err, CodeIntegrity)
			}
			if activationRowCount(t, store) != 2 || activationStateRowCount(t, store) != 0 {
				t.Fatal("rejected transition changed history or repaired the missing state")
			}
			for _, want := range []mousa.SupersessionActivation{chain.initial, chain.replacement} {
				if got, err := store.GetSupersessionActivation(ctx, want.ID); err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("history after rejection = %#v, %v; want %#v", got, err, want)
				}
			}
		})
	}
}

func TestSupersessionActivationExactRetryRejectsHistoricalProjection(t *testing.T) {
	ctx := context.Background()
	store, source, chain := activatedChain(t)
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `UPDATE supersession_activation_state SET current_activation_id = ?, active_declaration_id = ? WHERE source_id = ?`, chain.initial.ID[:], chain.declaration.First.ID[:], source.ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("read of historical projection = %v, want %s", err, CodeIntegrity)
	}
	if err := store.ApplySupersessionActivation(ctx, chain.initial); !IsCode(err, CodeIntegrity) {
		t.Fatalf("exact retry over historical projection = %v, want %s", err, CodeIntegrity)
	}
	if activationRowCount(t, store) != 2 || activationStateRowCount(t, store) != 1 {
		t.Fatal("rejected retry changed history or state count")
	}
	var current, selected []byte
	if err := store.db.QueryRowContext(ctx, `SELECT current_activation_id, active_declaration_id FROM supersession_activation_state WHERE source_id = ?`, source.ID[:]).Scan(&current, &selected); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, chain.initial.ID[:]) || !bytes.Equal(selected, chain.declaration.First.ID[:]) {
		t.Fatal("rejected retry repaired the historical projection")
	}
	for _, want := range []mousa.SupersessionActivation{chain.initial, chain.replacement} {
		if got, err := store.GetSupersessionActivation(ctx, want.ID); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("history after rejection = %#v, %v; want %#v", got, err, want)
		}
	}
}

func TestSupersessionActivationHistoryLookupUsesSourceIndex(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	var sourceID mousa.SourceID
	sourceID[0] = 1
	rows, err := store.db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT 1 FROM supersession_activations WHERE source_id = ? LIMIT 1`, sourceID[:])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var indexed bool
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		t.Log(detail)
		if strings.Contains(detail, "SEARCH") && strings.Contains(detail, "supersession_activations_source_idx") {
			indexed = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Fatal("source-history lookup does not use an indexed source search")
	}
}

func TestSupersessionActivationSourceIndexUpgradePreservesHistory(t *testing.T) {
	ctx := context.Background()
	store, source, chain := activatedChain(t)
	path := store.path
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.GetSupersessionActivationState(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP INDEX supersession_activations_source_idx; DELETE FROM schema_migrations WHERE version = 14`); err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, store.db, migrations, 13, true, false); err != nil {
		t.Fatalf("version-13 fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if readOnly, err := OpenReadOnly(ctx, path); readOnly != nil || !IsCode(err, CodeReadOnly) {
		if readOnly != nil {
			readOnly.Close()
		}
		t.Fatalf("read-only version-13 open = %v, %v", readOnly, err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got, err := store.GetSupersessionActivationState(ctx, source.ID); err != nil || !reflect.DeepEqual(got, before) {
		t.Fatalf("state after index upgrade = %#v, %v; want %#v", got, err, before)
	}
	for _, want := range []mousa.SupersessionActivation{chain.initial, chain.replacement} {
		if got, err := store.GetSupersessionActivation(ctx, want.ID); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("event after index upgrade = %#v, %v; want %#v", got, err, want)
		}
	}
	for _, want := range []mousa.SupersessionDeclaration{chain.declaration.First, chain.declaration.Second} {
		if got, err := store.GetSupersessionDeclaration(ctx, want.ID); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("declaration after index upgrade = %#v, %v; want %#v", got, err, want)
		}
	}
	if activationRowCount(t, store) != 2 || activationStateRowCount(t, store) != 1 {
		t.Fatal("index upgrade changed history or projection counts")
	}
	var hash []byte
	if err := store.db.QueryRowContext(ctx, `SELECT sha256 FROM schema_migrations WHERE version = 14`).Scan(&hash); err != nil || !bytes.Equal(hash, migrations[13].hash[:]) {
		t.Fatalf("migration 14 hash = %x, %v", hash, err)
	}
	backup, err := connect(ctx, path+".pre-migrate-v13-to-v14.sqlite", true)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if err := verifyVersion(ctx, backup, migrations, 13, false, true); err != nil {
		t.Fatalf("version-13 backup verification: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP INDEX supersession_activations_source_idx`); err != nil {
		t.Fatal(err)
	}
	if err := verifyVersion(ctx, store.db, migrations, 14, true, false); !IsCode(err, CodeIntegrity) {
		t.Fatalf("missing index startup verification = %v, want %s", err, CodeIntegrity)
	}
}

// readConsultationInWriterTransaction reads one supersession consultation through the store's single
// writer transaction, which is the transaction a retrieval decision holds.
func readConsultationInWriterTransaction(t *testing.T, store *Store, sourceID mousa.SourceID) supersessionConsultation {
	t.Helper()
	var consultation supersessionConsultation
	err := store.writeImmediate(context.Background(), "consult supersession activation", func(conn *sql.Conn) error {
		read, err := readSupersessionConsultation(context.Background(), conn, sourceID)
		if err != nil {
			return err
		}
		consultation = read
		return nil
	})
	if err != nil {
		t.Fatalf("consult supersession activation in the writer transaction: %v", err)
	}
	return consultation
}

// assertConsultation requires one consultation to name exactly the expected verified records: no
// state for a source with no activation history, a state without a declaration for a deactivation,
// and a state with its selected declaration otherwise.
func assertConsultation(t testing.TB, got supersessionConsultation, wantState *mousa.SupersessionActivationState, wantDeclaration *mousa.SupersessionDeclaration) {
	t.Helper()
	switch {
	case wantState == nil:
		if got.state != nil || got.declaration != nil {
			t.Fatalf("consultation = %#v, want no state and no declaration", got)
		}
	case got.state == nil || !reflect.DeepEqual(*got.state, *wantState):
		t.Fatalf("consultation state = %#v, want %#v", got.state, wantState)
	case wantDeclaration == nil && got.declaration != nil:
		t.Fatalf("consultation declaration = %#v, want none", got.declaration)
	case wantDeclaration != nil && (got.declaration == nil || !reflect.DeepEqual(*got.declaration, *wantDeclaration)):
		t.Fatalf("consultation declaration = %#v, want %#v", got.declaration, wantDeclaration)
	}
}

// rawActivationState reads the stored current projection so a rejected read can be shown to leave
// the damaged rows exactly as it found them.
func rawActivationState(t testing.TB, store *Store, sourceID mousa.SourceID) (current, active []byte) {
	t.Helper()
	err := store.db.QueryRowContext(context.Background(), `SELECT current_activation_id, active_declaration_id FROM supersession_activation_state WHERE source_id = ?`, sourceID[:]).Scan(&current, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		t.Fatalf("read stored activation state: %v", err)
	}
	return current, active
}

// execDamagingStatement runs one statement that the projection still references, so foreign-key
// enforcement is disabled first on the store's single pooled connection. Damaging a fixture this way
// is the only way to reach the recorded-state failures a read must refuse.
func execDamagingStatement(t *testing.T, store *Store, statement string, args ...any) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	result, err := store.db.ExecContext(ctx, statement, args...)
	if err != nil {
		t.Fatal(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("damaging statement affected %d rows, %v", affected, err)
	}
}

// assertConsultationRejects requires one damaged store to fail the consultation inside the writer
// transaction with the expected code instead of yielding a state or a verified absence of history.
func assertConsultationRejects(t *testing.T, store *Store, sourceID mousa.SourceID, code Code) {
	t.Helper()
	var consultationErr error
	writeErr := store.writeImmediate(context.Background(), "consult supersession activation", func(conn *sql.Conn) error {
		_, consultationErr = readSupersessionConsultation(context.Background(), conn, sourceID)
		return consultationErr
	})
	if consultationErr == nil {
		t.Fatal("damaged activation state was accepted as a consultation")
	}
	if !IsCode(consultationErr, code) {
		t.Fatalf("consultation of damaged state = %v, want %s", consultationErr, code)
	}
	if !IsCode(writeErr, code) {
		t.Fatalf("writer transaction error = %v, want %s", writeErr, code)
	}
}

func TestSupersessionConsultationFollowsActivationHistory(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	source, first, second := activationFixture(t, store)
	other := emptySourceID(t, store)

	// A source with no activation history is a verified absence rather than a damaged projection,
	// and consulting it writes nothing.
	assertConsultation(t, readConsultationInWriterTransaction(t, store, source.ID), nil, nil)
	if _, err := store.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("state with no history = %v, want %s", err, CodeNotFound)
	}
	if activationRowCount(t, store) != 0 || activationStateRowCount(t, store) != 0 {
		t.Fatal("consulting a source with no history wrote activation rows")
	}

	initial := activationTestTransition(t, source.ID, mousa.SupersessionActivationID{}, first.ID, "activate the declared successor", activationBaseUsec)
	if err := store.ApplySupersessionActivation(ctx, initial); err != nil {
		t.Fatal(err)
	}
	want := mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: initial.ID, ActiveDeclarationID: &first.ID}
	assertConsultation(t, readConsultationInWriterTransaction(t, store, source.ID), &want, &first)

	// The read-only transaction a state reader holds verifies the same records.
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := readSupersessionConsultation(ctx, tx, source.ID)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertConsultation(t, readOnly, &want, &first)

	replacement := activationTestTransition(t, source.ID, initial.ID, second.ID, "amended revision replaces the successor", activationBaseUsec+1)
	if err := store.ApplySupersessionActivation(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	want = mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: replacement.ID, ActiveDeclarationID: &second.ID}
	assertConsultation(t, readConsultationInWriterTransaction(t, store, source.ID), &want, &second)

	deactivation := activationTestTransition(t, source.ID, replacement.ID, mousa.SupersessionDeclarationID{}, "withdraw the declaration", activationBaseUsec+2)
	if err := store.ApplySupersessionActivation(ctx, deactivation); err != nil {
		t.Fatal(err)
	}
	want = mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: deactivation.ID}
	assertConsultation(t, readConsultationInWriterTransaction(t, store, source.ID), &want, nil)

	reactivation := activationTestTransition(t, source.ID, deactivation.ID, first.ID, "reactivate the first declaration", activationBaseUsec+3)
	if err := store.ApplySupersessionActivation(ctx, reactivation); err != nil {
		t.Fatal(err)
	}
	want = mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: reactivation.ID, ActiveDeclarationID: &first.ID}
	assertConsultation(t, readConsultationInWriterTransaction(t, store, source.ID), &want, &first)

	// A source without history stays a verified absence while another source has history.
	assertConsultation(t, readConsultationInWriterTransaction(t, store, other), nil, nil)
	if count := activationRowCount(t, store); count != 4 {
		t.Fatalf("stored activations = %d, want 4", count)
	}
}

func TestSupersessionConsultationUsesSuppliedSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "consultation-snapshot.sqlite")
	left, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer left.Close()
	source, first, second := activationFixture(t, left)
	initial := activationTestTransition(t, source.ID, mousa.SupersessionActivationID{}, first.ID, "initial selection", activationBaseUsec)
	replacement := activationTestTransition(t, source.ID, initial.ID, second.ID, "later replacement", activationBaseUsec+1)
	if err := left.ApplySupersessionActivation(ctx, initial); err != nil {
		t.Fatal(err)
	}
	right, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer right.Close()

	tx, err := left.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	want := mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: initial.ID, ActiveDeclarationID: &first.ID}
	assertConsultation(t, mustConsult(t, tx, source.ID), &want, &first)

	// Another connection commits a replacement while the caller's transaction stays open. The
	// caller keeps the snapshot it already read, so the two states are never mixed: the sequence is
	// ordered by the explicit transaction boundary rather than by a timing assumption.
	if err := right.ApplySupersessionActivation(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	assertConsultation(t, mustConsult(t, tx, source.ID), &want, &first)

	later := mousa.SupersessionActivationState{SourceID: source.ID, CurrentActivationID: replacement.ID, ActiveDeclarationID: &second.ID}
	assertConsultation(t, mustConsult(t, right.db, source.ID), &later, &second)

	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got, err := left.GetSupersessionActivationState(ctx, source.ID); err != nil || !reflect.DeepEqual(got, later) {
		t.Fatalf("state after the snapshot closed = %#v, %v; want %#v", got, err, later)
	}
}

func mustConsult(t *testing.T, q queryer, sourceID mousa.SourceID) supersessionConsultation {
	t.Helper()
	consultation, err := readSupersessionConsultation(context.Background(), q, sourceID)
	if err != nil {
		t.Fatalf("read supersession consultation: %v", err)
	}
	return consultation
}

func TestSupersessionConsultationRejectsDamagedState(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		code   Code
		damage func(t *testing.T, store *Store, source mousa.Source, chain activationChain)
	}{
		{
			name: "missing current projection with retained history",
			code: CodeIntegrity,
			damage: func(t *testing.T, store *Store, source mousa.Source, _ activationChain) {
				if _, err := store.db.ExecContext(ctx, `DELETE FROM supersession_activation_state WHERE source_id = ?`, source.ID[:]); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "projection names an older event",
			code: CodeIntegrity,
			damage: func(t *testing.T, store *Store, source mousa.Source, chain activationChain) {
				if _, err := store.db.ExecContext(ctx, `UPDATE supersession_activation_state SET current_activation_id = ?, active_declaration_id = ? WHERE source_id = ?`,
					chain.initial.ID[:], chain.declaration.First.ID[:], source.ID[:]); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "projection disagrees with the latest event",
			code: CodeIntegrity,
			damage: func(t *testing.T, store *Store, source mousa.Source, chain activationChain) {
				if _, err := store.db.ExecContext(ctx, `UPDATE supersession_activation_state SET active_declaration_id = ? WHERE source_id = ?`,
					chain.declaration.First.ID[:], source.ID[:]); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "event record replaced",
			code: CodeIntegrity,
			damage: func(t *testing.T, store *Store, _ mousa.Source, chain activationChain) {
				otherBytes, err := mousa.EncodeSupersessionActivation(chain.replacement)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(ctx, `UPDATE supersession_activations SET record_json = ? WHERE id = ?`, otherBytes, chain.initial.ID[:]); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "current event missing",
			code: CodeIntegrity,
			damage: func(t *testing.T, store *Store, _ mousa.Source, chain activationChain) {
				execDamagingStatement(t, store, `DELETE FROM supersession_activations WHERE id = ?`, chain.replacement.ID[:])
			},
		},
		{
			name: "selected declaration missing",
			code: CodeIntegrity,
			damage: func(t *testing.T, store *Store, _ mousa.Source, chain activationChain) {
				execDamagingStatement(t, store, `DELETE FROM supersession_declarations WHERE id = ?`, chain.declaration.Second.ID[:])
			},
		},
		{
			name: "pinned provenance missing",
			code: CodeIntegrity,
			damage: func(t *testing.T, store *Store, _ mousa.Source, chain activationChain) {
				execDamagingStatement(t, store, `DELETE FROM representation_inputs WHERE representation_id = ?`, chain.declaration.Second.PredecessorRepresentationID[:])
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			store, source, chain := activatedChain(t)
			defer store.Close()
			testCase.damage(t, store, source, chain)
			events := activationRowCount(t, store)
			states := activationStateRowCount(t, store)
			declarations := supersessionDeclarationRowCount(t, store)
			damagedCurrent, damagedActive := rawActivationState(t, store, source.ID)
			assertConsultationRejects(t, store, source.ID, testCase.code)
			if activationRowCount(t, store) != events || activationStateRowCount(t, store) != states || supersessionDeclarationRowCount(t, store) != declarations {
				t.Fatal("rejected consultation changed stored rows")
			}
			current, active := rawActivationState(t, store, source.ID)
			if !bytes.Equal(current, damagedCurrent) || !bytes.Equal(active, damagedActive) {
				t.Fatal("rejected consultation repaired the damaged projection")
			}
			if _, err := store.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, testCase.code) {
				t.Fatalf("public state read of damaged store = %v, want %s", err, testCase.code)
			}
		})
	}
}

// TestSupersessionActivationMissingEventClassificationBoundary pins the boundary of the corruption
// classification: a current projection whose named event is missing is an integrity failure for both
// the public state read and a transaction-local consultation, while reading that event's identity
// through the historical event reader is still the not-found of a missing row, and a surviving
// historical event stays readable. Nothing is repaired or written.
func TestSupersessionActivationMissingEventClassificationBoundary(t *testing.T) {
	ctx := context.Background()
	store, source, chain := activatedChain(t)
	defer store.Close()
	if _, err := store.GetSupersessionActivation(ctx, chain.initial.ID); err != nil {
		t.Fatalf("historical read before the damage: %v", err)
	}
	execDamagingStatement(t, store, `DELETE FROM supersession_activations WHERE id = ?`, chain.replacement.ID[:])
	events := activationRowCount(t, store)
	states := activationStateRowCount(t, store)
	current, active := rawActivationState(t, store, source.ID)

	assertConsultationRejects(t, store, source.ID, CodeIntegrity)
	if _, err := store.GetSupersessionActivationState(ctx, source.ID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("public state read of a projection naming a missing event = %v, want %s", err, CodeIntegrity)
	}
	if _, err := store.GetSupersessionActivation(ctx, chain.replacement.ID); !IsCode(err, CodeNotFound) {
		t.Fatalf("historical read of a missing event identity = %v, want %s", err, CodeNotFound)
	}
	if got, err := store.GetSupersessionActivation(ctx, chain.initial.ID); err != nil || !reflect.DeepEqual(got, chain.initial) {
		t.Fatalf("historical read of a surviving event = %#v, %v; want %#v", got, err, chain.initial)
	}
	if activationRowCount(t, store) != events || activationStateRowCount(t, store) != states {
		t.Fatal("rejected state read changed stored rows")
	}
	afterCurrent, afterActive := rawActivationState(t, store, source.ID)
	if !bytes.Equal(current, afterCurrent) || !bytes.Equal(active, afterActive) {
		t.Fatal("rejected state read repaired the damaged projection")
	}
}

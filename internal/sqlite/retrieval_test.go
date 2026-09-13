package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestSearchVerifiedLexicalAncestryAndLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "verified.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	active := addVerifiedLexicalDocument(t, store, "active", "shared active")
	withdrawn := addVerifiedLexicalDocument(t, store, "withdrawn", "shared withdrawn")
	missingRepresentation, missingContent, missingSegments := addLexicalDocument(t, store, "missing", "shared missing")
	if err := store.IndexTextRepresentation(ctx, missingRepresentation.ID, missingContent); err != nil {
		t.Fatal(err)
	}
	withdrawalID := withdrawVerifiedSource(t, store, withdrawn.source.ID, "withdrawn-1", 200)

	bridge := putVerifiedRepresentation(t, store, []mousa.DerivationInput{mousa.NewRepresentationDerivationInput(active.representation.ID)}, "lexical-test.bridge", []byte("bridge"), false)
	derivedContent := []byte("shared derived")
	derivedInputs := []mousa.DerivationInput{
		mousa.NewRepresentationDerivationInput(active.representation.ID),
		mousa.NewRepresentationDerivationInput(bridge.representation.ID),
		mousa.NewRepresentationDerivationInput(withdrawn.representation.ID),
		mousa.NewRepresentationDerivationInput(active.representation.ID),
	}
	parameters := testDigest("merge-parameters")
	derivedID, err := mousa.NewRepresentationID(derivedInputs, "lexical-test.merge", "1", parameters, mousa.UTF8TextMediaType, testDigest(string(derivedContent)))
	if err != nil {
		t.Fatal(err)
	}
	derived := mousa.Representation{
		Schema: mousa.RepresentationSchema, ID: derivedID, Inputs: derivedInputs,
		ProcessorID: "lexical-test.merge", ProcessorVersion: "1", ParametersSHA256: parameters,
		MediaType: mousa.UTF8TextMediaType, ContentSHA256: testDigest(string(derivedContent)), ByteLength: uint64(len(derivedContent)),
	}
	derivedSegments, err := mousa.SegmentUTF8Text(derived, derivedContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepresentation(ctx, derived); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSegment(ctx, derivedSegments[0]); err != nil {
		t.Fatal(err)
	}
	if err := store.IndexTextRepresentation(ctx, derived.ID, derivedContent); err != nil {
		t.Fatal(err)
	}

	got, err := store.SearchVerifiedLexical(ctx, "shared", 10)
	if err != nil || len(got) != 4 {
		t.Fatalf("SearchVerifiedLexical = %#v, %v; want 4 candidates", got, err)
	}
	acceptedRank := 0
	byID := make(map[mousa.SegmentID]mousa.VerifiedLexicalCandidate, len(got))
	for index, candidate := range got {
		if candidate.LexicalRank != index+1 || index > 0 && (candidate.BM25 < got[index-1].BM25 || candidate.BM25 == got[index-1].BM25 && bytes.Compare(candidate.Segment.ID[:], got[index-1].Segment.ID[:]) < 0) {
			t.Fatalf("candidate %d has non-deterministic lexical order: %#v", index, candidate)
		}
		if candidate.Disposition == mousa.CandidateAccepted {
			acceptedRank++
			if candidate.FinalRank != acceptedRank || candidate.Text == "" {
				t.Fatalf("accepted candidate has invalid rank or text: %#v", candidate)
			}
		} else if candidate.FinalRank != 0 || candidate.Text != "" {
			t.Fatalf("rejected candidate leaked text or rank: %#v", candidate)
		}
		byID[candidate.Segment.ID] = candidate
	}
	if acceptedRank != 1 {
		t.Fatalf("accepted candidates = %d, want 1", acceptedRank)
	}
	if candidate := byID[active.segments[0].ID]; candidate.Disposition != mousa.CandidateAccepted || len(candidate.Paths) != 1 {
		t.Fatalf("active candidate = %#v", candidate)
	}
	if candidate := byID[withdrawn.segments[0].ID]; candidate.Disposition != mousa.CandidateRejected || !reflect.DeepEqual(candidate.Reasons, []mousa.LifecycleReason{mousa.ReasonSourceWithdrawn}) {
		t.Fatalf("withdrawn candidate = %#v", candidate)
	}
	if candidate := byID[missingSegments[0].ID]; candidate.Disposition != mousa.CandidateRejected || !reflect.DeepEqual(candidate.Reasons, []mousa.LifecycleReason{mousa.ReasonSourceStateMissing}) {
		t.Fatalf("missing-state candidate = %#v", candidate)
	}
	derivedCandidate := byID[derivedSegments[0].ID]
	if derivedCandidate.Disposition != mousa.CandidateRejected || !reflect.DeepEqual(derivedCandidate.Reasons, []mousa.LifecycleReason{mousa.ReasonSourceWithdrawn}) || len(derivedCandidate.Paths) != 3 || len(derivedCandidate.Sources) != 2 {
		t.Fatalf("derived candidate = %#v", derivedCandidate)
	}
	wantPaths := []mousa.EvidencePath{
		{RepresentationIDs: []mousa.RepresentationID{derived.ID, withdrawn.representation.ID}, ArtifactID: withdrawn.artifact.ID, ObservationID: withdrawn.observation.ID, SourceID: withdrawn.source.ID},
		{RepresentationIDs: []mousa.RepresentationID{derived.ID, active.representation.ID}, ArtifactID: active.artifact.ID, ObservationID: active.observation.ID, SourceID: active.source.ID},
		{RepresentationIDs: []mousa.RepresentationID{derived.ID, bridge.representation.ID, active.representation.ID}, ArtifactID: active.artifact.ID, ObservationID: active.observation.ID, SourceID: active.source.ID},
	}
	if !reflect.DeepEqual(derivedCandidate.Paths, wantPaths) {
		t.Fatalf("ancestry paths = %#v, want exact order %#v", derivedCandidate.Paths, wantPaths)
	}

	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	readOnlyGot, err := readOnly.SearchVerifiedLexical(ctx, "shared", 10)
	if err != nil || !reflect.DeepEqual(readOnlyGot, got) {
		t.Fatalf("read-only evidence differs: %#v, %v", readOnlyGot, err)
	}

	resumeVerifiedSource(t, store, withdrawn.source, withdrawalID, "withdrawn-resume", 300)
	resumed, err := store.SearchVerifiedLexical(ctx, "shared", 10)
	if err != nil {
		t.Fatal(err)
	}
	if candidate := candidateByID(resumed, withdrawn.segments[0].ID); candidate.Disposition != mousa.CandidateAccepted || candidate.Text != "shared withdrawn" {
		t.Fatalf("resumed direct candidate = %#v", candidate)
	}
	if candidate := candidateByID(resumed, derivedSegments[0].ID); candidate.Disposition != mousa.CandidateAccepted || candidate.Text != "shared derived" {
		t.Fatalf("resumed derived candidate = %#v", candidate)
	}
}

func TestSearchVerifiedLexicalReadSnapshotAndReadOnlyParity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "snapshot.sqlite")
	reader, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	fixture := addVerifiedLexicalDocument(t, reader, "snapshot", "snapshot evidence")

	tx, err := reader.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var pinnedCount int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM source_ingest_state`).Scan(&pinnedCount); err != nil || pinnedCount != 1 {
		t.Fatalf("pin snapshot = %d, %v", pinnedCount, err)
	}
	withdrawVerifiedSource(t, writer, fixture.source.ID, "snapshot-withdrawal", 200)

	pinned, err := searchVerifiedLexical(ctx, tx, "snapshot", 10)
	if err != nil || len(pinned) != 1 || pinned[0].Disposition != mousa.CandidateAccepted || pinned[0].Text != "snapshot evidence" {
		t.Fatalf("pinned snapshot = %#v, %v", pinned, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	current, err := reader.SearchVerifiedLexical(ctx, "snapshot", 10)
	if err != nil || len(current) != 1 || current[0].Disposition != mousa.CandidateRejected || current[0].Text != "" || !reflect.DeepEqual(current[0].Reasons, []mousa.LifecycleReason{mousa.ReasonSourceWithdrawn}) {
		t.Fatalf("current snapshot = %#v, %v", current, err)
	}
	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	readOnlyCurrent, err := readOnly.SearchVerifiedLexical(ctx, "snapshot", 10)
	if err != nil || !reflect.DeepEqual(readOnlyCurrent, current) {
		t.Fatalf("read-only current snapshot = %#v, %v", readOnlyCurrent, err)
	}
}

func TestSearchVerifiedLexicalIntegrityAndResourceBounds(t *testing.T) {
	t.Run("busy is retryable", func(t *testing.T) {
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "verified-busy.sqlite")
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		addVerifiedLexicalDocument(t, store, "busy", "busy evidence")
		locker, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer locker.Close()
		conn, err := locker.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(ctx, `PRAGMA locking_mode = EXCLUSIVE`); err != nil {
			t.Fatal(err)
		}
		store.db.SetMaxIdleConns(0)
		if _, err := conn.ExecContext(ctx, `BEGIN EXCLUSIVE`); err != nil {
			t.Fatal(err)
		}
		defer conn.ExecContext(context.Background(), `ROLLBACK`)
		got, err := store.SearchVerifiedLexical(ctx, "busy", 10)
		var storageErr *Error
		if got != nil || !errors.As(err, &storageErr) || storageErr.Code != CodeBusy || !storageErr.Retryable {
			t.Fatalf("busy search = %#v, %#v; want nil retryable busy", got, err)
		}
	})
	t.Run("empty and invalid query", func(t *testing.T) {
		ctx := context.Background()
		store := openLexicalStore(t)
		defer store.Close()
		if got, err := store.SearchVerifiedLexical(ctx, "missing", 10); err != nil || got == nil || len(got) != 0 {
			t.Fatalf("no match = %#v, %v", got, err)
		}
		if got, err := store.SearchVerifiedLexical(ctx, "", 10); got != nil || !IsCode(err, CodeInvalidQuery) {
			t.Fatalf("invalid query = %#v, %v", got, err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if got, err := store.SearchVerifiedLexical(cancelled, "missing", 10); got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled query = %#v, %v", got, err)
		}
	})
	t.Run("corrupt ancestry", func(t *testing.T) {
		ctx := context.Background()
		store := openLexicalStore(t)
		defer store.Close()
		fixture := addVerifiedLexicalDocument(t, store, "corrupt", "corrupt ancestry")
		if _, err := store.db.ExecContext(ctx, `UPDATE representations SET record_json = ? WHERE id = ?`, []byte("{}"), fixture.representation.ID[:]); err != nil {
			t.Fatal(err)
		}
		if got, err := store.SearchVerifiedLexical(ctx, "corrupt", 10); got != nil || !IsCode(err, CodeIntegrity) {
			t.Fatalf("corrupt ancestry = %#v, %v", got, err)
		}
	})
	t.Run("missing representation parent", func(t *testing.T) {
		ctx := context.Background()
		store := openLexicalStore(t)
		defer store.Close()
		leaf := addVerifiedLexicalDocument(t, store, "missing-parent-leaf", "missing parent leaf")
		root := putVerifiedRepresentation(t, store, []mousa.DerivationInput{mousa.NewRepresentationDerivationInput(leaf.representation.ID)}, "missing-parent-root", []byte("missingparent"), true)
		if _, err := store.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM representations WHERE id = ?`, leaf.representation.ID[:]); err != nil {
			t.Fatal(err)
		}
		if got, err := store.SearchVerifiedLexical(ctx, "missingparent", 10); got != nil || !IsCode(err, CodeIntegrity) {
			t.Fatalf("missing parent for %x = %#v, %v", root.representation.ID, got, err)
		}
	})
	t.Run("mixed healthy and missing artifact returns no partial evidence", func(t *testing.T) {
		ctx := context.Background()
		store := openLexicalStore(t)
		defer store.Close()
		first := addVerifiedLexicalDocument(t, store, "mixed-first", "mixedresult first")
		second := addVerifiedLexicalDocument(t, store, "mixed-second", "mixedresult second")
		raw, err := store.SearchLexical(ctx, "mixedresult", 10)
		if err != nil || len(raw) != 2 {
			t.Fatalf("raw order = %#v, %v; want two candidates", raw, err)
		}
		later := first
		if raw[1].Segment.ID == second.segments[0].ID {
			later = second
		} else if raw[1].Segment.ID != first.segments[0].ID {
			t.Fatalf("later raw candidate %x does not match fixture", raw[1].Segment.ID)
		}
		if _, err := store.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `DELETE FROM artifacts WHERE id = ?`, later.artifact.ID[:]); err != nil {
			t.Fatal(err)
		}
		if got, err := store.SearchVerifiedLexical(ctx, "mixedresult", 10); got != nil || !IsCode(err, CodeIntegrity) {
			t.Fatalf("mixed healthy and corrupt search = %#v, %v; want nil integrity", got, err)
		}
	})
	t.Run("recursion cycle guard", func(t *testing.T) {
		ctx := context.Background()
		store := openLexicalStore(t)
		defer store.Close()
		fixture := addVerifiedLexicalDocument(t, store, "cycle", "cycle ancestry")
		tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		visited := 0
		if got, err := lexicalRepresentationPaths(ctx, tx, fixture.representation.ID, nil, map[mousa.RepresentationID]struct{}{fixture.representation.ID: {}}, &visited); got != nil || !IsCode(err, CodeIntegrity) {
			t.Fatalf("cycle guard = %#v, %v", got, err)
		}
	})
	t.Run("completed path limit", func(t *testing.T) {
		ctx := context.Background()
		store := openLexicalStore(t)
		defer store.Close()
		fixture := addVerifiedLexicalDocument(t, store, "path-leaf", "leaf")
		leafInput := mousa.NewArtifactDerivationInput(fixture.artifact.ID)
		childInputs := make([]mousa.DerivationInput, maxLexicalEvidence/2+1)
		for index := range childInputs {
			childInputs[index] = leafInput
		}
		first := putVerifiedRepresentation(t, store, childInputs, "path-child-1", []byte("child one"), false)
		second := putVerifiedRepresentation(t, store, childInputs, "path-child-2", []byte("child two"), false)
		root := putVerifiedRepresentation(t, store, []mousa.DerivationInput{
			mousa.NewRepresentationDerivationInput(first.representation.ID),
			mousa.NewRepresentationDerivationInput(second.representation.ID),
		}, "path-root", []byte("pathbound"), true)
		if got, err := store.SearchVerifiedLexical(ctx, "pathbound", 10); got != nil || !IsCode(err, CodeResourceLimit) {
			t.Fatalf("path bound for %x = %#v, %v", root.representation.ID, got, err)
		}
	})
	t.Run("visited representation limit", func(t *testing.T) {
		ctx := context.Background()
		store := openLexicalStore(t)
		defer store.Close()
		fixture := addVerifiedLexicalDocument(t, store, "node-leaf", "node leaf")
		parent := fixture.representation
		for index := 0; index < maxLexicalEvidence; index++ {
			created := putVerifiedRepresentation(t, store, []mousa.DerivationInput{mousa.NewRepresentationDerivationInput(parent.ID)}, fmt.Sprintf("node-%04d", index), []byte("nodebound"), index == maxLexicalEvidence-1)
			parent = created.representation
		}
		if got, err := store.SearchVerifiedLexical(ctx, "nodebound", 10); got != nil || !IsCode(err, CodeResourceLimit) {
			t.Fatalf("node bound = %#v, %v", got, err)
		}
	})
}

type verifiedLexicalDocument struct {
	source         mousa.Source
	observation    mousa.Observation
	artifact       mousa.Artifact
	representation mousa.Representation
	segments       []mousa.Segment
}

func addVerifiedLexicalDocument(t *testing.T, store *Store, key, text string) verifiedLexicalDocument {
	t.Helper()
	ctx := context.Background()
	sourceID, err := mousa.NewSourceID("lexical-test", key)
	if err != nil {
		t.Fatal(err)
	}
	source := mousa.Source{Schema: mousa.SourceSchema, ID: sourceID, Namespace: "lexical-test", ExternalSourceID: key}
	observationID, err := mousa.NewObservationID(sourceID, key)
	if err != nil {
		t.Fatal(err)
	}
	observation := mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: sourceID, ExternalObservationID: key}
	artifactID, err := mousa.NewArtifactID(observationID, "body")
	if err != nil {
		t.Fatal(err)
	}
	content := []byte(text)
	artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: "body", MediaType: mousa.UTF8TextMediaType, ContentSHA256: testDigest(text), ByteLength: uint64(len(content))}
	if err := store.ApplyIngest(ctx, mousa.IngestBatch{
		AdapterID: "lexical-test", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem, CapturedAtUsec: 100,
		Source: source, Observation: observation, Artifacts: []mousa.Artifact{artifact},
	}); err != nil {
		t.Fatal(err)
	}
	representation, normalized, err := mousa.NormalizeUTF8Text(artifact, content)
	if err != nil || !bytes.Equal(normalized, content) {
		t.Fatalf("NormalizeUTF8Text = %v, normalized=%q", err, normalized)
	}
	segments, err := mousa.SegmentUTF8Text(representation, content)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepresentation(ctx, representation); err != nil {
		t.Fatal(err)
	}
	for _, segment := range segments {
		if err := store.PutSegment(ctx, segment); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
		t.Fatal(err)
	}
	return verifiedLexicalDocument{source: source, observation: observation, artifact: artifact, representation: representation, segments: segments}
}

func putVerifiedRepresentation(t *testing.T, store *Store, inputs []mousa.DerivationInput, processorID string, content []byte, index bool) verifiedLexicalDocument {
	t.Helper()
	parameters := testDigest("parameters:" + processorID)
	contentDigest := testDigest(string(content))
	id, err := mousa.NewRepresentationID(inputs, processorID, "1", parameters, mousa.UTF8TextMediaType, contentDigest)
	if err != nil {
		t.Fatal(err)
	}
	representation := mousa.Representation{
		Schema: mousa.RepresentationSchema, ID: id, Inputs: inputs,
		ProcessorID: processorID, ProcessorVersion: "1", ParametersSHA256: parameters,
		MediaType: mousa.UTF8TextMediaType, ContentSHA256: contentDigest, ByteLength: uint64(len(content)),
	}
	if err := store.PutRepresentation(context.Background(), representation); err != nil {
		t.Fatal(err)
	}
	document := verifiedLexicalDocument{representation: representation}
	if !index {
		return document
	}
	segments, err := mousa.SegmentUTF8Text(representation, content)
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range segments {
		if err := store.PutSegment(context.Background(), segment); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.IndexTextRepresentation(context.Background(), representation.ID, content); err != nil {
		t.Fatal(err)
	}
	document.segments = segments
	return document
}

func withdrawVerifiedSource(t *testing.T, store *Store, sourceID mousa.SourceID, externalID string, occurredAt int64) mousa.WithdrawalID {
	t.Helper()
	withdrawalID, err := mousa.NewWithdrawalID(sourceID, externalID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WithdrawSource(context.Background(), mousa.SourceWithdrawal{
		Schema: mousa.SourceWithdrawalSchema, ID: withdrawalID, SourceID: sourceID,
		ExternalWithdrawalID: externalID, AdapterID: "lexical-test", AdapterVersion: "1", OccurredAtUsec: occurredAt,
	}); err != nil {
		t.Fatal(err)
	}
	return withdrawalID
}

func resumeVerifiedSource(t *testing.T, store *Store, source mousa.Source, withdrawalID mousa.WithdrawalID, externalObservationID string, capturedAt int64) {
	t.Helper()
	observationID, err := mousa.NewObservationID(source.ID, externalObservationID)
	if err != nil {
		t.Fatal(err)
	}
	observation := mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: source.ID, ExternalObservationID: externalObservationID}
	artifactID, err := mousa.NewArtifactID(observationID, "resume")
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("resume")
	artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: "resume", MediaType: mousa.UTF8TextMediaType, ContentSHA256: testDigest(string(content)), ByteLength: uint64(len(content))}
	if err := store.ApplyIngest(context.Background(), mousa.IngestBatch{
		AdapterID: "lexical-test", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem, CapturedAtUsec: capturedAt,
		ResumeWithdrawalID: &withdrawalID, Source: source, Observation: observation, Artifacts: []mousa.Artifact{artifact},
	}); err != nil {
		t.Fatal(err)
	}
}

func candidateByID(candidates []mousa.VerifiedLexicalCandidate, id mousa.SegmentID) mousa.VerifiedLexicalCandidate {
	for _, candidate := range candidates {
		if candidate.Segment.ID == id {
			return candidate
		}
	}
	return mousa.VerifiedLexicalCandidate{}
}

// addEnforcedDocument indexes one text document for an existing pushed Source observation so the
// enforced source combines active ingest state with searchable content.
func addEnforcedDocument(t *testing.T, store *Store, observation mousa.Observation, key, text string) verifiedLexicalDocument {
	t.Helper()
	ctx := context.Background()
	artifactID, err := mousa.NewArtifactID(observation.ID, key)
	if err != nil {
		t.Fatalf("NewArtifactID: %v", err)
	}
	content := []byte(text)
	artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observation.ID, ArtifactKey: key, MediaType: mousa.UTF8TextMediaType, ContentSHA256: testDigest(text), ByteLength: uint64(len(content))}
	if err := store.PutArtifact(ctx, artifact); err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	representation, normalized, err := mousa.NormalizeUTF8Text(artifact, content)
	if err != nil || !bytes.Equal(normalized, content) {
		t.Fatalf("NormalizeUTF8Text = %v, normalized=%q", err, normalized)
	}
	segments, err := mousa.SegmentUTF8Text(representation, content)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepresentation(ctx, representation); err != nil {
		t.Fatalf("PutRepresentation: %v", err)
	}
	for _, segment := range segments {
		if err := store.PutSegment(ctx, segment); err != nil {
			t.Fatalf("PutSegment: %v", err)
		}
	}
	if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
		t.Fatalf("IndexTextRepresentation: %v", err)
	}
	return verifiedLexicalDocument{observation: observation, artifact: artifact, representation: representation, segments: segments}
}

func seedEnforcedRetrieval(t *testing.T, store *Store, text string) (mousa.PolicyEvaluationRequest, verifiedLexicalDocument) {
	t.Helper()
	seedEvaluationFixture(t, store)
	sequence := uint64(1)
	batch := testPushBatch(t, "enforced", &sequence)
	if err := store.ApplyIngest(context.Background(), batch); err != nil {
		t.Fatalf("ApplyIngest: %v", err)
	}
	document := addEnforcedDocument(t, store, batch.Observation, "enforced-body", text)
	request := testEvaluationRequest(t, batch.Source.ID, "request-enforced")
	return request, document
}

func acceptedSegmentIDs(candidates []mousa.VerifiedLexicalCandidate, sourceID mousa.SourceID) []mousa.SegmentID {
	ids := make([]mousa.SegmentID, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Disposition != mousa.CandidateAccepted || len(candidate.Sources) != 1 || candidate.Sources[0].SourceID != sourceID {
			continue
		}
		ids = append(ids, candidate.Segment.ID)
	}
	return ids
}

func TestSearchEnforcedLexicalScopesToDecisionSource(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	request, document := seedEnforcedRetrieval(t, store, "sharedterm enforced evidence")
	addVerifiedLexicalDocument(t, store, "enforced-peer", "sharedterm peer evidence")

	decision, err := store.EvaluateSourceRetrieval(ctx, request)
	if err != nil || decision.Outcome != mousa.PolicyOutcomeAllow {
		t.Fatalf("EvaluateSourceRetrieval = %#v, %v", decision, err)
	}
	result, err := store.SearchEnforcedLexical(ctx, request, "sharedterm", 10)
	if err != nil {
		t.Fatalf("SearchEnforcedLexical: %v", err)
	}
	if !reflect.DeepEqual(result.Decision, decision) {
		t.Fatalf("result decision = %#v, want %#v", result.Decision, decision)
	}
	if len(result.Candidates) == 0 || result.Candidates[0].Segment.ID != document.segments[0].ID {
		t.Fatalf("enforced candidates = %#v, want exactly the decision source segment", result.Candidates)
	}
	unrestricted, err := store.SearchVerifiedLexical(ctx, "sharedterm", 10)
	if err != nil {
		t.Fatalf("SearchVerifiedLexical: %v", err)
	}
	if !reflect.DeepEqual(acceptedSegmentIDs(result.Candidates, decision.Request.SourceID), acceptedSegmentIDs(unrestricted, decision.Request.SourceID)) {
		t.Fatalf("enforced candidates = %#v, want restricted %#v", result.Candidates, unrestricted)
	}
	for _, candidate := range result.Candidates {
		for _, source := range candidate.Sources {
			if source.SourceID != decision.Request.SourceID {
				t.Fatalf("candidate %x carries foreign source %x", candidate.Segment.ID, source.SourceID)
			}
		}
	}
	if len(result.Candidates) == 0 {
		t.Fatal("enforced search returned no candidates")
	}
	if _, err := store.GetPolicyDecision(ctx, result.Decision.ID); err != nil {
		t.Fatalf("GetPolicyDecision: %v", err)
	}

}

func TestSearchEnforcedLexicalLimitAppliesWithinDecisionSource(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	request, document := seedEnforcedRetrieval(t, store, "filler filler filler filler filler filler filler filler scopebound once")
	for index := range 3 {
		addVerifiedLexicalDocument(t, store, fmt.Sprintf("scope-peer-%d", index), "scopebound scopebound scopebound")
	}

	if _, err := store.EvaluateSourceRetrieval(ctx, request); err != nil {
		t.Fatalf("EvaluateSourceRetrieval: %v", err)
	}
	raw, err := store.SearchLexical(ctx, "scopebound", 100)
	if err != nil || len(raw) < 2 {
		t.Fatalf("SearchLexical = %#v, %v", raw, err)
	}
	position := 0
	for index, candidate := range raw {
		if candidate.Segment.ID == document.segments[0].ID {
			position = index + 1
		}
	}
	if position <= 1 {
		t.Fatalf("fixture premise: enforced candidate must rank below a peer candidate, position %d", position)
	}
	limit := position - 1
	unrestricted, err := store.SearchVerifiedLexical(ctx, "scopebound", limit)
	if err != nil {
		t.Fatalf("SearchVerifiedLexical: %v", err)
	}
	if len(acceptedSegmentIDs(unrestricted, mousa.SourceID{})) > 0 {
		// Presence alone does not break the premise; the enforced segment must be absent.
	}
	for _, candidate := range unrestricted {
		if candidate.Segment.ID == document.segments[0].ID {
			t.Fatalf("fixture premise: unenforced limit %d already returns the decision source candidate", limit)
		}
	}
	result, err := store.SearchEnforcedLexical(ctx, request, "scopebound", 1)
	if err != nil {
		t.Fatalf("SearchEnforcedLexical: %v", err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].Segment.ID != document.segments[0].ID {
		t.Fatalf("enforced candidates = %#v, want exactly the decision source candidate", result.Candidates)
	}
}

func TestSearchEnforcedLexicalDenyMissingInvalidAndTamper(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	request, _ := seedEnforcedRetrieval(t, store, "gate enforced evidence")
	if _, err := store.EvaluateSourceRetrieval(ctx, request); err != nil {
		t.Fatalf("EvaluateSourceRetrieval: %v", err)
	}

	t.Run("never evaluated request", func(t *testing.T) {
		missing := testEvaluationRequest(t, request.SourceID, "request-never-evaluated")
		result, err := store.SearchEnforcedLexical(ctx, missing, "gate", 10)
		if result.Decision.ID != (mousa.PolicyDecisionID{}) || len(result.Candidates) != 0 {
			t.Fatalf("missing decision result = %#v", result)
		}
		if !IsCode(err, CodeNotFound) {
			t.Fatalf("missing decision = %v, want not_found", err)
		}
	})
	t.Run("deny decision authorizes no candidate", func(t *testing.T) {
		deniedSource := mousa.Source{Schema: mousa.SourceSchema}
		deniedSourceID, err := mousa.NewSourceID("lexical-test", "enforced-denied")
		if err != nil {
			t.Fatal(err)
		}
		deniedSource = mousa.Source{Schema: mousa.SourceSchema, ID: deniedSourceID, Namespace: "lexical-test", ExternalSourceID: "enforced-denied"}
		if err := store.PutSource(ctx, deniedSource); err != nil {
			t.Fatalf("PutSource: %v", err)
		}
		deniedRequest := testEvaluationRequest(t, deniedSourceID, "request-denied")
		decision, err := store.EvaluateSourceRetrieval(ctx, deniedRequest)
		if err != nil || decision.Outcome != mousa.PolicyOutcomeDeny {
			t.Fatalf("deny decision = %#v, %v", decision, err)
		}
		result, err := store.SearchEnforcedLexical(ctx, deniedRequest, "gate", 10)
		if err != nil {
			t.Fatalf("enforced deny = %v, want decision data without error", err)
		}
		if !reflect.DeepEqual(result.Decision, decision) || len(result.Candidates) != 0 {
			t.Fatalf("enforced deny result = %#v", result)
		}
	})
	t.Run("invalid requests", func(t *testing.T) {
		if _, err := store.SearchEnforcedLexical(ctx, mousa.PolicyEvaluationRequest{}, "gate", 10); !IsCode(err, CodeInvalidRecord) {
			t.Fatalf("empty request = %v", err)
		}
		tampered := request
		tampered.ExternalRequestID = "request-tampered"
		if _, err := store.SearchEnforcedLexical(ctx, tampered, "gate", 10); !IsCode(err, CodeInvalidRecord) {
			t.Fatalf("identity-mismatched request = %v", err)
		}
		if _, err := store.SearchEnforcedLexical(ctx, request, "gate", 0); !IsCode(err, CodeInvalidQuery) {
			t.Fatalf("invalid limit = %v", err)
		}
	})
	t.Run("tampered projection is integrity", func(t *testing.T) {
		if _, err := store.db.ExecContext(ctx, `UPDATE policy_decisions SET outcome = 'deny' WHERE request_id = ?`, request.ID[:]); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SearchEnforcedLexical(ctx, request, "gate", 10); !IsCode(err, CodeIntegrity) {
			t.Fatalf("tampered outcome = %v, want integrity", err)
		}
	})
}

func TestSearchEnforcedLexicalReadOnlyWritesNothing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "enforced-readonly.sqlite")
	writer, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	request, document := seedEnforcedRetrieval(t, writer, "readonly enforced evidence")
	addVerifiedLexicalDocument(t, writer, "readonly-peer", "readonly peer")
	decision, err := writer.EvaluateSourceRetrieval(ctx, request)
	if err != nil || decision.Outcome != mousa.PolicyOutcomeAllow {
		t.Fatalf("EvaluateSourceRetrieval = %#v, %v", decision, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	before, err := snapshotRowCounts(ctx, readOnly)
	if err != nil {
		t.Fatal(err)
	}
	result, err := readOnly.SearchEnforcedLexical(ctx, request, "readonly", 10)
	if err != nil || len(result.Candidates) == 0 || result.Candidates[0].Segment.ID != document.segments[0].ID {
		t.Fatalf("read-only enforced search = %#v, %v", result, err)
	}
	after, err := snapshotRowCounts(ctx, readOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only enforcement changed rows: before %v after %v", before, after)
	}
	foreign, err := readOnly.SearchEnforcedLexical(ctx, request, "peer", 10)
	if err != nil || len(foreign.Candidates) != 0 {
		t.Fatalf("foreign expression = %#v, %v; want zero candidates", foreign, err)
	}
}

func snapshotRowCounts(ctx context.Context, store *Store) (map[string]int, error) {
	counts := map[string]int{}
	for _, table := range []string{"policy_decisions", "policy_decision_inputs", "segment_lexical_rows"} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil {
			return nil, err
		}
		counts[table] = count
	}
	return counts, nil
}

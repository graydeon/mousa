package sqlite

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestSourceRoundTripPreservesCanonicalBytes(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "records.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	want := testSource(t)
	if err := store.PutSource(ctx, want); err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	got, err := store.GetSource(ctx, want.ID)
	if err != nil {
		t.Fatalf("GetSource: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetSource = %#v, want %#v", got, want)
	}

	canonical, err := mousa.EncodeSource(want)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var stored []byte
	if err := store.db.QueryRow(`SELECT record_json FROM sources WHERE id = ?`, want.ID[:]).Scan(&stored); err != nil {
		t.Fatalf("read stored bytes: %v", err)
	}
	if !reflect.DeepEqual(stored, canonical) {
		t.Fatalf("stored bytes = %q, want %q", stored, canonical)
	}
}

func TestRecordGraphRoundTripsOrderedMixedInputs(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "records.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	source, observation, artifact, base, mixed, segment := testRecordGraph(t)
	if err := store.PutSource(ctx, source); err != nil {
		t.Fatalf("PutSource: %v", err)
	}
	if err := store.PutObservation(ctx, observation); err != nil {
		t.Fatalf("PutObservation: %v", err)
	}
	if err := store.PutArtifact(ctx, artifact); err != nil {
		t.Fatalf("PutArtifact: %v", err)
	}
	if err := store.PutRepresentation(ctx, base); err != nil {
		t.Fatalf("PutRepresentation(base): %v", err)
	}
	if err := store.PutRepresentation(ctx, mixed); err != nil {
		t.Fatalf("PutRepresentation(mixed): %v", err)
	}
	if err := store.PutSegment(ctx, segment); err != nil {
		t.Fatalf("PutSegment: %v", err)
	}
	gotObservation, err := store.GetObservation(ctx, observation.ID)
	if err != nil || !reflect.DeepEqual(gotObservation, observation) {
		t.Fatalf("GetObservation = %#v, %v; want %#v", gotObservation, err, observation)
	}
	gotArtifact, err := store.GetArtifact(ctx, artifact.ID)
	if err != nil || !reflect.DeepEqual(gotArtifact, artifact) {
		t.Fatalf("GetArtifact = %#v, %v; want %#v", gotArtifact, err, artifact)
	}
	gotMixed, err := store.GetRepresentation(ctx, mixed.ID)
	if err != nil || !reflect.DeepEqual(gotMixed, mixed) {
		t.Fatalf("GetRepresentation = %#v, %v; want %#v", gotMixed, err, mixed)
	}
	gotSegment, err := store.GetSegment(ctx, segment.ID)
	if err != nil || !reflect.DeepEqual(gotSegment, segment) {
		t.Fatalf("GetSegment = %#v, %v; want %#v", gotSegment, err, segment)
	}
	rows, err := store.db.Query(`SELECT ordinal, input_kind FROM representation_inputs WHERE representation_id = ? ORDER BY ordinal`, mixed.ID[:])
	if err != nil {
		t.Fatalf("query input order: %v", err)
	}
	defer rows.Close()
	for ordinal, wantKind := range []string{"representation", "artifact"} {
		if !rows.Next() {
			t.Fatalf("missing ordinal %d", ordinal)
		}
		var gotOrdinal int
		var gotKind string
		if err := rows.Scan(&gotOrdinal, &gotKind); err != nil {
			t.Fatalf("scan ordinal %d: %v", ordinal, err)
		}
		if gotOrdinal != ordinal || gotKind != wantKind {
			t.Fatalf("input = (%d, %q), want (%d, %q)", gotOrdinal, gotKind, ordinal, wantKind)
		}
	}
	if rows.Next() {
		t.Fatal("unexpected extra input")
	}
}

func TestRepresentationInputStorageLimitIsTyped(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "records.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	_, _, artifact, _, _, _ := testRecordGraph(t)
	inputs := make([]mousa.DerivationInput, maxRepresentationInputs+1)
	for index := range inputs {
		inputs[index] = mousa.NewArtifactDerivationInput(artifact.ID)
	}
	id, err := mousa.NewRepresentationID(inputs, "test.processor", "limit", testDigest("params-limit"), mousa.UTF8TextMediaType, testDigest("content-limit"))
	if err != nil {
		t.Fatalf("NewRepresentationID: %v", err)
	}
	record := mousa.Representation{
		Schema: mousa.RepresentationSchema, ID: id, Inputs: inputs,
		ProcessorID: "test.processor", ProcessorVersion: "limit",
		ParametersSHA256: testDigest("params-limit"), MediaType: mousa.UTF8TextMediaType,
		ContentSHA256: testDigest("content-limit"), ByteLength: 1,
	}
	if err := store.PutRepresentation(ctx, record); !IsCode(err, CodeResourceLimit) {
		t.Fatalf("PutRepresentation error = %v, want resource_limit", err)
	}
}

func TestRepresentationInputProjectionTamperFailsIntegrity(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Store, mousa.Artifact, mousa.Representation, mousa.Representation)
	}{
		{"gap", func(t *testing.T, store *Store, _ mousa.Artifact, _ mousa.Representation, mixed mousa.Representation) {
			if _, err := store.db.Exec(`DELETE FROM representation_inputs WHERE representation_id = ? AND ordinal = 0`, mixed.ID[:]); err != nil {
				t.Fatalf("delete input: %v", err)
			}
		}},
		{"reorder", func(t *testing.T, store *Store, artifact mousa.Artifact, base, mixed mousa.Representation) {
			if _, err := store.db.Exec(`UPDATE representation_inputs SET input_kind='artifact', artifact_id=?, input_representation_id=NULL WHERE representation_id=? AND ordinal=0`, artifact.ID[:], mixed.ID[:]); err != nil {
				t.Fatalf("rewrite input 0: %v", err)
			}
			if _, err := store.db.Exec(`UPDATE representation_inputs SET input_kind='representation', artifact_id=NULL, input_representation_id=? WHERE representation_id=? AND ordinal=1`, base.ID[:], mixed.ID[:]); err != nil {
				t.Fatalf("rewrite input 1: %v", err)
			}
		}},
		{"extra", func(t *testing.T, store *Store, artifact mousa.Artifact, _ mousa.Representation, mixed mousa.Representation) {
			if _, err := store.db.Exec(`INSERT INTO representation_inputs(representation_id, ordinal, input_kind, artifact_id, input_representation_id) VALUES(?, 2, 'artifact', ?, NULL)`, mixed.ID[:], artifact.ID[:]); err != nil {
				t.Fatalf("insert extra input: %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := Open(ctx, filepath.Join(t.TempDir(), "records.sqlite"))
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer store.Close()
			source, observation, artifact, base, mixed, _ := testRecordGraph(t)
			for _, put := range []func() error{
				func() error { return store.PutSource(ctx, source) },
				func() error { return store.PutObservation(ctx, observation) },
				func() error { return store.PutArtifact(ctx, artifact) },
				func() error { return store.PutRepresentation(ctx, base) },
				func() error { return store.PutRepresentation(ctx, mixed) },
			} {
				if err := put(); err != nil {
					t.Fatalf("populate: %v", err)
				}
			}
			test.mutate(t, store, artifact, base, mixed)
			if _, err := store.GetRepresentation(ctx, mixed.ID); !IsCode(err, CodeIntegrity) {
				t.Fatalf("GetRepresentation error = %v, want integrity", err)
			}
		})
	}
}

func TestRawBlobEncodingsPreserveFullUint64Range(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "encodings.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	source, observation, artifact, representation, _, _ := testRecordGraph(t)
	representation.ByteLength = math.MaxUint64
	selector := mousa.NewTextByteRangeSelector(math.MaxUint64-1, math.MaxUint64)
	segmentID, err := mousa.NewSegmentID(representation.ID, selector, testDigest("max-range"))
	if err != nil {
		t.Fatalf("NewSegmentID: %v", err)
	}
	segment := mousa.Segment{Schema: mousa.SegmentSchema, ID: segmentID, RepresentationID: representation.ID, Selector: selector, ContentSHA256: testDigest("max-range")}
	for _, put := range []func() error{
		func() error { return store.PutSource(ctx, source) },
		func() error { return store.PutObservation(ctx, observation) },
		func() error { return store.PutArtifact(ctx, artifact) },
		func() error { return store.PutRepresentation(ctx, representation) },
		func() error { return store.PutSegment(ctx, segment) },
	} {
		if err := put(); err != nil {
			t.Fatalf("populate: %v", err)
		}
	}
	var idType, hashType string
	var idLength, hashLength int
	if err := store.db.QueryRow(`SELECT typeof(id), length(id) FROM sources WHERE id = ?`, source.ID[:]).Scan(&idType, &idLength); err != nil {
		t.Fatalf("source ID projection: %v", err)
	}
	if err := store.db.QueryRow(`SELECT typeof(sha256), length(sha256) FROM schema_migrations WHERE version = 1`).Scan(&hashType, &hashLength); err != nil {
		t.Fatalf("migration hash projection: %v", err)
	}
	if idType != "blob" || idLength != 32 || hashType != "blob" || hashLength != 32 {
		t.Fatalf("raw types = id(%s,%d) hash(%s,%d), want 32-byte blobs", idType, idLength, hashType, hashLength)
	}
	var start, end []byte
	if err := store.db.QueryRow(`SELECT selector_start, selector_end FROM segments WHERE id = ?`, segment.ID[:]).Scan(&start, &end); err != nil {
		t.Fatalf("selector projection: %v", err)
	}
	wantStart := make([]byte, 8)
	wantEnd := make([]byte, 8)
	binary.BigEndian.PutUint64(wantStart, math.MaxUint64-1)
	binary.BigEndian.PutUint64(wantEnd, math.MaxUint64)
	if !bytes.Equal(start, wantStart) || !bytes.Equal(end, wantEnd) || bytes.Compare(start, end) >= 0 {
		t.Fatalf("selector blobs = %x..%x, want %x..%x in lexical order", start, end, wantStart, wantEnd)
	}
}

func TestRecordSizeBoundaryAndContextCancellation(t *testing.T) {
	if err := checkRecordSize(make([]byte, maxRecordBytes)); err != nil {
		t.Fatalf("exact record limit: %v", err)
	}
	if err := checkRecordSize(make([]byte, maxRecordBytes+1)); !IsCode(err, CodeResourceLimit) {
		t.Fatalf("one byte over limit error = %v, want resource_limit", err)
	}

	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "cancel.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = store.PutSource(ctx, testSourceNamed(t, "private-record-sentinel"))
	var storageErr *Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &storageErr) {
		t.Fatalf("canceled write error = %#v, want wrapped context cancellation", err)
	}
	if storageErr.Code != CodeInternal || storageErr.Retryable {
		t.Fatalf("canceled write fields = code %s retryable %t, want internal false", storageErr.Code, storageErr.Retryable)
	}
	if strings.Contains(err.Error(), "private-record-sentinel") || strings.Contains(strings.ToUpper(err.Error()), "INSERT") {
		t.Fatalf("canceled write leaked record or SQL: %v", err)
	}
}

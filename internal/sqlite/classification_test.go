package sqlite

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestClassificationStoreRoundTripAndIntegrity(t *testing.T) {
	ctx := context.Background()

	t.Run("round trip duplicate and read only", func(t *testing.T) {
		store, record := classificationStoreFixture(t)
		path := store.path
		if err := store.PutClassification(ctx, record); err != nil {
			t.Fatal(err)
		}
		if err := store.PutClassification(ctx, record); err != nil {
			t.Fatalf("idempotent duplicate: %v", err)
		}
		got, err := store.GetClassification(ctx, record.ID)
		if err != nil || !reflect.DeepEqual(got, record) {
			t.Fatalf("GetClassification = %#v, %v", got, err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		readOnly, err := OpenReadOnly(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer readOnly.Close()
		if got, err := readOnly.GetClassification(ctx, record.ID); err != nil || !reflect.DeepEqual(got, record) {
			t.Fatalf("read-only GetClassification = %#v, %v", got, err)
		}
		if err := readOnly.PutClassification(ctx, record); !IsCode(err, CodeReadOnly) {
			t.Fatalf("read-only PutClassification error = %v", err)
		}
	})

	t.Run("missing subject and basis parents", func(t *testing.T) {
		store, record := classificationStoreFixture(t)
		defer store.Close()
		missingSource := mousa.SourceID(testDigest("missing-source"))
		record.Subject = mousa.NewSourceClassificationSubject(missingSource)
		record.ID = classificationID(t, record)
		if err := store.PutClassification(ctx, record); !IsCode(err, CodeConflict) {
			t.Fatalf("missing subject error = %v", err)
		}
		_, record = classificationRecordGraph(t)
		record.Basis[0] = mousa.NewSourceClassificationSubject(missingSource)
		record.ID = classificationID(t, record)
		if err := store.PutClassification(ctx, record); !IsCode(err, CodeConflict) {
			t.Fatalf("missing basis error = %v", err)
		}
	})

	t.Run("resource limit", func(t *testing.T) {
		store, record := classificationStoreFixture(t)
		defer store.Close()
		record.Label = strings.Repeat("x", maxRecordBytes)
		record.ID = classificationID(t, record)
		if err := store.PutClassification(ctx, record); !IsCode(err, CodeResourceLimit) {
			t.Fatalf("oversized record error = %v", err)
		}
	})

	t.Run("stored bytes and projections are verified", func(t *testing.T) {
		for _, damage := range []struct {
			name string
			exec func(*Store, mousa.Classification)
		}{
			{"noncanonical JSON", func(store *Store, record mousa.Classification) {
				_, err := store.db.Exec(`UPDATE classifications SET record_json = ? WHERE id = ?`, []byte(" "+strings.TrimSpace(classificationJSON(t, record))+"\n"), record.ID[:])
				if err != nil {
					t.Fatal(err)
				}
			}},
			{"subject projection", func(store *Store, record mousa.Classification) {
				observationID, ok := record.Basis[1].ObservationID()
				if !ok {
					t.Fatal("basis is not an observation")
				}
				_, err := store.db.Exec(`UPDATE classifications SET subject_kind = 'observation', subject_segment_id = NULL, subject_observation_id = ? WHERE id = ?`, observationID[:], record.ID[:])
				if err != nil {
					t.Fatal(err)
				}
			}},
			{"missing basis", func(store *Store, record mousa.Classification) {
				_, err := store.db.Exec(`DELETE FROM classification_bases WHERE classification_id = ? AND ordinal = 1`, record.ID[:])
				if err != nil {
					t.Fatal(err)
				}
			}},
			{"basis order", func(store *Store, record mousa.Classification) {
				_, err := store.db.Exec(`UPDATE classification_bases SET ordinal = 99 WHERE classification_id = ? AND ordinal = 1`, record.ID[:])
				if err != nil {
					t.Fatal(err)
				}
			}},
		} {
			t.Run(damage.name, func(t *testing.T) {
				store, record := classificationStoreFixture(t)
				defer store.Close()
				if err := store.PutClassification(ctx, record); err != nil {
					t.Fatal(err)
				}
				damage.exec(store, record)
				if _, err := store.GetClassification(ctx, record.ID); !IsCode(err, CodeIntegrity) {
					t.Fatalf("GetClassification error = %v", err)
				}
			})
		}
	})

	t.Run("same ID with different stored bytes conflicts", func(t *testing.T) {
		store, record := classificationStoreFixture(t)
		defer store.Close()
		if err := store.PutClassification(ctx, record); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`UPDATE classifications SET record_json = ? WHERE id = ?`, []byte("{}\n"), record.ID[:]); err != nil {
			t.Fatal(err)
		}
		if err := store.PutClassification(ctx, record); !IsCode(err, CodeConflict) {
			t.Fatalf("collision error = %v", err)
		}
	})
}

func TestClassificationDuplicateBasisProjectionMismatchIsConflict(t *testing.T) {
	ctx := context.Background()
	store, record := classificationStoreFixture(t)
	defer store.Close()

	if err := store.PutClassification(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE classification_bases SET ordinal = 99 WHERE classification_id = ? AND ordinal = 1`, record.ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetClassification(ctx, record.ID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("GetClassification error = %v, want integrity", err)
	}
	if err := store.PutClassification(ctx, record); !IsCode(err, CodeConflict) {
		t.Fatalf("duplicate PutClassification error = %v, want conflict", err)
	}
}

func classificationStoreFixture(t *testing.T) (*Store, mousa.Classification) {
	t.Helper()
	store, err := Open(context.Background(), t.TempDir()+"/records.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	graph, record := classificationRecordGraph(t)
	for _, put := range []func() error{
		func() error { return store.PutSource(context.Background(), graph.source) },
		func() error { return store.PutObservation(context.Background(), graph.observation) },
		func() error { return store.PutArtifact(context.Background(), graph.artifact) },
		func() error { return store.PutRepresentation(context.Background(), graph.base) },
		func() error { return store.PutRepresentation(context.Background(), graph.mixed) },
		func() error { return store.PutSegment(context.Background(), graph.segment) },
	} {
		if err := put(); err != nil {
			store.Close()
			t.Fatal(err)
		}
	}
	return store, record
}

type classificationGraph struct {
	source      mousa.Source
	observation mousa.Observation
	artifact    mousa.Artifact
	base, mixed mousa.Representation
	segment     mousa.Segment
}

func classificationRecordGraph(t *testing.T) (classificationGraph, mousa.Classification) {
	t.Helper()
	source, observation, artifact, base, mixed, segment := testRecordGraph(t)
	graph := classificationGraph{source, observation, artifact, base, mixed, segment}
	record := mousa.Classification{Schema: mousa.ClassificationSchema, Subject: mousa.NewSegmentClassificationSubject(segment.ID), Taxonomy: "example.sensitivity", TaxonomyVersion: "2026-08-24", Label: "restricted", AsserterID: "example.classifier", AsserterVersion: "1.0.0", Basis: []mousa.ClassificationSubject{mousa.NewSourceClassificationSubject(source.ID), mousa.NewObservationClassificationSubject(observation.ID), mousa.NewArtifactClassificationSubject(artifact.ID), mousa.NewRepresentationClassificationSubject(mixed.ID), mousa.NewSegmentClassificationSubject(segment.ID)}, AssertedAtUsec: 1724544000123456, ConfidencePPM: 875000}
	record.ID = classificationID(t, record)
	return graph, record
}

func classificationID(t testing.TB, record mousa.Classification) mousa.ClassificationID {
	t.Helper()
	id, err := mousa.NewClassificationID(record.Subject, record.Taxonomy, record.TaxonomyVersion, record.Label, record.AsserterID, record.AsserterVersion, record.Basis, record.AssertedAtUsec, record.ConfidencePPM)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func classificationJSON(t testing.TB, record mousa.Classification) string {
	t.Helper()
	data, err := mousa.EncodeClassification(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"github.com/graydeon/mousa/internal/mousa"
)

const maxClassificationBases = 4096

type classificationProjection struct {
	kind             string
	sourceID         []byte
	observationID    []byte
	artifactID       []byte
	representationID []byte
	segmentID        []byte
}

func (projection classificationProjection) values() []any {
	return []any{projection.kind, projection.sourceID, projection.observationID, projection.artifactID, projection.representationID, projection.segmentID}
}

func (projection classificationProjection) equal(kind string, sourceID, observationID, artifactID, representationID, segmentID []byte) bool {
	return projection.kind == kind && bytes.Equal(projection.sourceID, sourceID) && bytes.Equal(projection.observationID, observationID) && bytes.Equal(projection.artifactID, artifactID) && bytes.Equal(projection.representationID, representationID) && bytes.Equal(projection.segmentID, segmentID)
}

func projectClassificationSubject(subject mousa.ClassificationSubject) (classificationProjection, error) {
	switch subject.Kind() {
	case mousa.ClassificationSubjectSource:
		id, ok := subject.SourceID()
		if !ok || id == (mousa.SourceID{}) {
			return classificationProjection{}, fmt.Errorf("invalid source subject")
		}
		return classificationProjection{kind: string(subject.Kind()), sourceID: append([]byte(nil), id[:]...)}, nil
	case mousa.ClassificationSubjectObservation:
		id, ok := subject.ObservationID()
		if !ok || id == (mousa.ObservationID{}) {
			return classificationProjection{}, fmt.Errorf("invalid observation subject")
		}
		return classificationProjection{kind: string(subject.Kind()), observationID: append([]byte(nil), id[:]...)}, nil
	case mousa.ClassificationSubjectArtifact:
		id, ok := subject.ArtifactID()
		if !ok || id == (mousa.ArtifactID{}) {
			return classificationProjection{}, fmt.Errorf("invalid artifact subject")
		}
		return classificationProjection{kind: string(subject.Kind()), artifactID: append([]byte(nil), id[:]...)}, nil
	case mousa.ClassificationSubjectRepresentation:
		id, ok := subject.RepresentationID()
		if !ok || id == (mousa.RepresentationID{}) {
			return classificationProjection{}, fmt.Errorf("invalid representation subject")
		}
		return classificationProjection{kind: string(subject.Kind()), representationID: append([]byte(nil), id[:]...)}, nil
	case mousa.ClassificationSubjectSegment:
		id, ok := subject.SegmentID()
		if !ok || id == (mousa.SegmentID{}) {
			return classificationProjection{}, fmt.Errorf("invalid segment subject")
		}
		return classificationProjection{kind: string(subject.Kind()), segmentID: append([]byte(nil), id[:]...)}, nil
	default:
		return classificationProjection{}, fmt.Errorf("unsupported classification subject kind %q", subject.Kind())
	}
}

func requireClassificationParent(ctx context.Context, q queryRower, projection classificationProjection) error {
	table, id := "", []byte(nil)
	switch projection.kind {
	case string(mousa.ClassificationSubjectSource):
		table, id = "sources", projection.sourceID
	case string(mousa.ClassificationSubjectObservation):
		table, id = "observations", projection.observationID
	case string(mousa.ClassificationSubjectArtifact):
		table, id = "artifacts", projection.artifactID
	case string(mousa.ClassificationSubjectRepresentation):
		table, id = "representations", projection.representationID
	case string(mousa.ClassificationSubjectSegment):
		table, id = "segments", projection.segmentID
	default:
		return integrity("check classification parent", "unsupported typed parent")
	}
	return requireParent(ctx, q, table, id)
}

func (store *Store) PutClassification(ctx context.Context, record mousa.Classification) error {
	if err := store.requireWritable("put classification"); err != nil {
		return err
	}
	data, err := mousa.EncodeClassification(record)
	if err != nil {
		return wrap(CodeInvalidRecord, "put classification", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	if len(record.Basis) > maxClassificationBases {
		return wrap(CodeResourceLimit, "put classification", fmt.Errorf("basis count %d exceeds %d", len(record.Basis), maxClassificationBases))
	}
	subject, err := projectClassificationSubject(record.Subject)
	if err != nil {
		return wrap(CodeInvalidRecord, "put classification", err)
	}
	basis := make([]classificationProjection, len(record.Basis))
	for index, entry := range record.Basis {
		basis[index], err = projectClassificationSubject(entry)
		if err != nil {
			return wrap(CodeInvalidRecord, "put classification", err)
		}
	}
	return store.writeImmediate(ctx, "put classification", func(conn *sql.Conn) error {
		if err := requireClassificationParent(ctx, conn, subject); err != nil {
			return err
		}
		for _, entry := range basis {
			if err := requireClassificationParent(ctx, conn, entry); err != nil {
				return err
			}
		}
		values := append([]any{record.ID[:]}, subject.values()...)
		values = append(values, data)
		result, err := conn.ExecContext(ctx, `INSERT INTO classifications(id, subject_kind, subject_source_id, subject_observation_id, subject_artifact_id, subject_representation_id, subject_segment_id, record_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, values...)
		if err != nil {
			if !sqliteConstraint(err) {
				return classify("put classification", err)
			}
			var kind string
			var sourceID, observationID, artifactID, representationID, segmentID, existing []byte
			queryErr := conn.QueryRowContext(ctx, `SELECT subject_kind, subject_source_id, subject_observation_id, subject_artifact_id, subject_representation_id, subject_segment_id, record_json FROM classifications WHERE id = ?`, record.ID[:]).Scan(&kind, &sourceID, &observationID, &artifactID, &representationID, &segmentID, &existing)
			if queryErr != nil {
				return classify("put classification", err)
			}
			if !bytes.Equal(existing, data) || !subject.equal(kind, sourceID, observationID, artifactID, representationID, segmentID) {
				return wrap(CodeConflict, "put classification", err)
			}
			if verifyErr := verifyClassification(ctx, conn, record, data); verifyErr != nil {
				if IsCode(verifyErr, CodeIntegrity) {
					return wrap(CodeConflict, "put classification", verifyErr)
				}
				return verifyErr
			}
			return nil
		}
		if err := oneRow(result); err != nil {
			return wrap(CodeIntegrity, "put classification", err)
		}
		for ordinal, entry := range basis {
			values := append([]any{record.ID[:], ordinal}, entry.values()...)
			if _, err := conn.ExecContext(ctx, `INSERT INTO classification_bases(classification_id, ordinal, basis_kind, basis_source_id, basis_observation_id, basis_artifact_id, basis_representation_id, basis_segment_id) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, values...); err != nil {
				return classify("put classification basis", err)
			}
		}
		return verifyClassification(ctx, conn, record, data)
	})
}

func (store *Store) GetClassification(ctx context.Context, id mousa.ClassificationID) (mousa.Classification, error) {
	return getClassification(ctx, store.db, id)
}

func getClassification(ctx context.Context, q queryer, id mousa.ClassificationID) (mousa.Classification, error) {
	var kind string
	var sourceID, observationID, artifactID, representationID, segmentID, data []byte
	if err := q.QueryRowContext(ctx, `SELECT subject_kind, subject_source_id, subject_observation_id, subject_artifact_id, subject_representation_id, subject_segment_id, record_json FROM classifications WHERE id = ?`, id[:]).Scan(&kind, &sourceID, &observationID, &artifactID, &representationID, &segmentID, &data); err != nil {
		return mousa.Classification{}, readError("get classification", err)
	}
	record, err := decodeCanonical(data, mousa.DecodeClassification, mousa.EncodeClassification)
	if err != nil {
		return mousa.Classification{}, wrap(CodeIntegrity, "get classification", err)
	}
	if record.ID != id {
		return mousa.Classification{}, integrity("get classification", "ID projection disagrees with record")
	}
	subject, err := projectClassificationSubject(record.Subject)
	if err != nil || !subject.equal(kind, sourceID, observationID, artifactID, representationID, segmentID) {
		return mousa.Classification{}, integrity("get classification", "subject projection disagrees with record")
	}
	if err := requireClassificationParent(ctx, q, subject); err != nil {
		return mousa.Classification{}, integrity("get classification", "subject parent is missing")
	}
	if err := verifyClassificationBasis(ctx, q, record); err != nil {
		return mousa.Classification{}, err
	}
	return record, nil
}

func verifyClassificationBasis(ctx context.Context, q queryer, record mousa.Classification) error {
	want := make([]classificationProjection, len(record.Basis))
	for index, entry := range record.Basis {
		var err error
		want[index], err = projectClassificationSubject(entry)
		if err != nil {
			return integrity("get classification basis", "record contains an invalid basis")
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT ordinal, basis_kind, basis_source_id, basis_observation_id, basis_artifact_id, basis_representation_id, basis_segment_id FROM classification_bases WHERE classification_id = ? ORDER BY ordinal`, record.ID[:])
	if err != nil {
		return classify("get classification basis", err)
	}
	got := make([]classificationProjection, 0, len(want))
	for ordinal, entry := range want {
		if !rows.Next() {
			rows.Close()
			return integrity("get classification basis", "missing basis entry")
		}
		var gotOrdinal int
		var kind string
		var sourceID, observationID, artifactID, representationID, segmentID []byte
		if err := rows.Scan(&gotOrdinal, &kind, &sourceID, &observationID, &artifactID, &representationID, &segmentID); err != nil {
			rows.Close()
			return classify("get classification basis", err)
		}
		if gotOrdinal != ordinal || !entry.equal(kind, sourceID, observationID, artifactID, representationID, segmentID) {
			rows.Close()
			return integrity("get classification basis", "ordered basis projection disagrees with record")
		}
		got = append(got, classificationProjection{kind: kind, sourceID: sourceID, observationID: observationID, artifactID: artifactID, representationID: representationID, segmentID: segmentID})
	}
	if rows.Next() {
		rows.Close()
		return integrity("get classification basis", "extra basis entry")
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return classify("get classification basis", err)
	}
	if err := rows.Close(); err != nil {
		return classify("get classification basis", err)
	}
	for _, entry := range got {
		if err := requireClassificationParent(ctx, q, entry); err != nil {
			return integrity("get classification basis", "basis parent is missing")
		}
	}
	return nil
}

func verifyClassification(ctx context.Context, q queryer, want mousa.Classification, wantData []byte) error {
	got, err := getClassification(ctx, q, want.ID)
	if err != nil {
		return err
	}
	encoded, err := mousa.EncodeClassification(got)
	if err != nil || !bytes.Equal(encoded, wantData) || !reflect.DeepEqual(got, want) {
		return integrity("verify classification", "exact read-back disagrees with write")
	}
	return nil
}

func verifyClassificationRecords(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM classifications ORDER BY id`)
	if err != nil {
		return startupError("scan classifications", err)
	}
	var ids [][]byte
	for rows.Next() {
		var id []byte
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return startupError("scan classifications", err)
		}
		ids = append(ids, append([]byte(nil), id...))
	}
	if err := rows.Close(); err != nil {
		return startupError("scan classifications", err)
	}
	for _, raw := range ids {
		if len(raw) != 32 {
			return integrity("scan classifications", "invalid ID length")
		}
		var id mousa.ClassificationID
		copy(id[:], raw)
		if _, err := getClassification(ctx, db, id); err != nil {
			return err
		}
	}
	return nil
}

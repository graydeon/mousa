package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/graydeon/mousa/internal/mousa"
)

// ClassificationInspection releases a classification only after fresh authorization
// and proof that both its projections and canonical ancestry belong to the source.
type ClassificationInspection struct {
	Decision       mousa.PolicyDecision
	Classification *mousa.Classification
}

// PutSourceClassification is the source-bounded administration path. It does not
// grant retrieval access or require current, indexed, or nonwithdrawn parents.
func (store *Store) PutSourceClassification(ctx context.Context, sourceID mousa.SourceID, record mousa.Classification) (mousa.Classification, error) {
	if err := store.requireWritable("put source classification"); err != nil {
		return mousa.Classification{}, err
	}
	data, err := mousa.EncodeClassification(record)
	if err != nil {
		return mousa.Classification{}, wrap(CodeInvalidRecord, "put source classification", err)
	}
	if err := checkRecordSize(data); err != nil {
		return mousa.Classification{}, err
	}
	if len(record.Basis) > maxClassificationBases {
		return mousa.Classification{}, wrap(CodeResourceLimit, "put source classification", errors.New("basis count exceeds 4096"))
	}
	err = store.writeImmediate(ctx, "put source classification", func(conn *sql.Conn) error {
		if err := classificationRecordScope(ctx, conn, sourceID, record); err != nil {
			return err
		}
		return putClassification(ctx, conn, record, data)
	})
	if err != nil {
		return mousa.Classification{}, err
	}
	// The append helper verified exact canonical readback before commit.
	return record, nil
}

// InspectClassification owns the authorization receipt and all consultation in
// one writer snapshot. Denial does not consult the classification at all.
func (store *Store) InspectClassification(ctx context.Context, request mousa.PolicyEvaluationRequest, id mousa.ClassificationID) (ClassificationInspection, error) {
	if err := store.requireWritable("inspect classification"); err != nil {
		return ClassificationInspection{}, err
	}
	if err := request.Validate(); err != nil {
		return ClassificationInspection{}, wrap(CodeInvalidRecord, "inspect classification", err)
	}
	var result ClassificationInspection
	err := store.writeImmediate(ctx, "inspect classification", func(conn *sql.Conn) error {
		var err error
		result.Decision, err = evaluateSourceRetrieval(ctx, conn, request, false)
		if err != nil || result.Decision.Outcome != mousa.PolicyOutcomeAllow {
			return err
		}
		record, err := getSourceClassification(ctx, conn, request.SourceID, id)
		if err != nil {
			return err
		}
		result.Classification = &record
		return nil
	})
	if err != nil {
		return ClassificationInspection{}, err
	}
	return result, nil
}

func classificationUnavailable(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return classify("classification scope", err)
	}
	return wrap(CodeNotFound, "classification scope", errors.New("classification unavailable"))
}

func classificationRecordScope(ctx context.Context, q queryer, sourceID mousa.SourceID, record mousa.Classification) error {
	if len(record.Basis) > maxClassificationBases {
		return classificationUnavailable(ctx)
	}
	projection, err := projectClassificationSubject(record.Subject)
	if err != nil || !classificationProjectionInSource(ctx, q, sourceID, projection) {
		return classificationUnavailable(ctx)
	}
	for _, entry := range record.Basis {
		projection, err := projectClassificationSubject(entry)
		if err != nil || !classificationProjectionInSource(ctx, q, sourceID, projection) {
			return classificationUnavailable(ctx)
		}
	}
	return nil
}

// A projection is eligible only with a known kind, exactly one non-null 32-byte
// typed ID, and verified all-root ownership. Malformed types, ordinal gaps and
// excess rows are unprovable (not_found), not integrity disclosures. Missing
// trailing basis rows cannot establish a foreign canonical basis: canonical
// ancestry is proved separately before native exact projection checks. Thus a
// missing local trailing row or a contradictory but wholly local projection is
// an integrity error only after BOTH views have established local ownership.
func getSourceClassification(ctx context.Context, q queryer, sourceID mousa.SourceID, id mousa.ClassificationID) (mousa.Classification, error) {
	var subject classificationProjection
	var data []byte
	err := q.QueryRowContext(ctx, `SELECT subject_kind, subject_source_id, subject_observation_id, subject_artifact_id, subject_representation_id, subject_segment_id, record_json FROM classifications WHERE id = ?`, id[:]).Scan(&subject.kind, &subject.sourceID, &subject.observationID, &subject.artifactID, &subject.representationID, &subject.segmentID, &data)
	if err != nil || !classificationProjectionInSource(ctx, q, sourceID, subject) {
		return mousa.Classification{}, classificationUnavailable(ctx)
	}
	rows, err := q.QueryContext(ctx, `SELECT ordinal, basis_kind, basis_source_id, basis_observation_id, basis_artifact_id, basis_representation_id, basis_segment_id FROM classification_bases WHERE classification_id = ? ORDER BY ordinal`, id[:])
	if err != nil {
		return mousa.Classification{}, classificationUnavailable(ctx)
	}
	var basis []classificationProjection
	for rows.Next() {
		var ordinal int
		var entry classificationProjection
		if err := rows.Scan(&ordinal, &entry.kind, &entry.sourceID, &entry.observationID, &entry.artifactID, &entry.representationID, &entry.segmentID); err != nil || ordinal != len(basis) || len(basis) >= maxClassificationBases {
			rows.Close()
			return mousa.Classification{}, classificationUnavailable(ctx)
		}
		basis = append(basis, entry)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil || closeErr != nil {
		return mousa.Classification{}, classificationUnavailable(ctx)
	}
	for _, entry := range basis {
		if !classificationProjectionInSource(ctx, q, sourceID, entry) {
			return mousa.Classification{}, classificationUnavailable(ctx)
		}
	}
	// Decode privately to prove canonical ownership, never releasing this draft.
	// Invalid canonical bytes cannot establish ownership and remain unavailable.
	if len(data) > maxRecordBytes {
		return mousa.Classification{}, classificationUnavailable(ctx)
	}
	record, err := mousa.DecodeClassification(data)
	if err != nil {
		return mousa.Classification{}, classificationUnavailable(ctx)
	}
	if err := classificationRecordScope(ctx, q, sourceID, record); err != nil {
		return mousa.Classification{}, err
	}
	return getClassification(ctx, q, id)
}

func classificationProjectionInSource(ctx context.Context, q queryer, sourceID mousa.SourceID, projection classificationProjection) bool {
	var selected []byte
	switch projection.kind {
	case string(mousa.ClassificationSubjectSource):
		selected = projection.sourceID
	case string(mousa.ClassificationSubjectObservation):
		selected = projection.observationID
	case string(mousa.ClassificationSubjectArtifact):
		selected = projection.artifactID
	case string(mousa.ClassificationSubjectRepresentation):
		selected = projection.representationID
	case string(mousa.ClassificationSubjectSegment):
		selected = projection.segmentID
	default:
		return false
	}
	populated := 0
	for _, id := range [][]byte{projection.sourceID, projection.observationID, projection.artifactID, projection.representationID, projection.segmentID} {
		if id != nil {
			populated++
		}
	}
	if populated != 1 || len(selected) != 32 {
		return false
	}
	switch projection.kind {
	case string(mousa.ClassificationSubjectSource):
		id := mousa.SourceID(selected)
		if id != sourceID {
			return false
		}
		_, err := getSource(ctx, q, id)
		return err == nil
	case string(mousa.ClassificationSubjectObservation):
		observation, err := getObservation(ctx, q, mousa.ObservationID(selected))
		if err != nil || observation.SourceID != sourceID {
			return false
		}
		_, err = getSource(ctx, q, sourceID)
		return err == nil
	case string(mousa.ClassificationSubjectArtifact):
		artifact, err := getArtifact(ctx, q, mousa.ArtifactID(selected))
		if err != nil {
			return false
		}
		return classificationProjectionInSource(ctx, q, sourceID, classificationProjection{kind: string(mousa.ClassificationSubjectObservation), observationID: artifact.ObservationID[:]})
	default:
		var id mousa.RepresentationID
		if projection.kind == string(mousa.ClassificationSubjectSegment) {
			segment, err := getSegment(ctx, q, mousa.SegmentID(selected))
			if err != nil {
				return false
			}
			id = segment.RepresentationID
		} else {
			id = mousa.RepresentationID(selected)
		}
		visited := 0
		paths, err := lexicalRepresentationPaths(ctx, q, id, nil, make(map[mousa.RepresentationID]struct{}), &visited)
		if err != nil || len(paths) == 0 {
			return false
		}
		for _, path := range paths {
			if path.SourceID != sourceID {
				return false
			}
		}
		return true
	}
}

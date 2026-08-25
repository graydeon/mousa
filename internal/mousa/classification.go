package mousa

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const ClassificationSchema = "mousa.classification.v1"

const maxClassificationBasis = 4096

// ClassificationID is the stable identity of one immutable classification assertion.
type ClassificationID [sha256.Size]byte

// ClassificationSubjectKind names the closed set of canonical evidence records.
type ClassificationSubjectKind string

const (
	ClassificationSubjectSource         ClassificationSubjectKind = "source"
	ClassificationSubjectObservation    ClassificationSubjectKind = "observation"
	ClassificationSubjectArtifact       ClassificationSubjectKind = "artifact"
	ClassificationSubjectRepresentation ClassificationSubjectKind = "representation"
	ClassificationSubjectSegment        ClassificationSubjectKind = "segment"
)

// ClassificationSubject is a closed typed reference to canonical evidence.
type ClassificationSubject struct {
	kind             ClassificationSubjectKind
	sourceID         SourceID
	observationID    ObservationID
	artifactID       ArtifactID
	representationID RepresentationID
	segmentID        SegmentID
}

// Classification is immutable classification evidence. It has no policy effect.
type Classification struct {
	Schema          string                  `json:"schema"`
	ID              ClassificationID        `json:"id"`
	Subject         ClassificationSubject   `json:"subject"`
	Taxonomy        string                  `json:"taxonomy"`
	TaxonomyVersion string                  `json:"taxonomy_version"`
	Label           string                  `json:"label"`
	AsserterID      string                  `json:"asserter_id"`
	AsserterVersion string                  `json:"asserter_version"`
	Basis           []ClassificationSubject `json:"basis"`
	AssertedAtUsec  int64                   `json:"asserted_at_usec"`
	ConfidencePPM   uint64                  `json:"confidence_ppm"`
}

func NewSourceClassificationSubject(id SourceID) ClassificationSubject {
	return ClassificationSubject{kind: ClassificationSubjectSource, sourceID: id}
}

func NewObservationClassificationSubject(id ObservationID) ClassificationSubject {
	return ClassificationSubject{kind: ClassificationSubjectObservation, observationID: id}
}

func NewArtifactClassificationSubject(id ArtifactID) ClassificationSubject {
	return ClassificationSubject{kind: ClassificationSubjectArtifact, artifactID: id}
}

func NewRepresentationClassificationSubject(id RepresentationID) ClassificationSubject {
	return ClassificationSubject{kind: ClassificationSubjectRepresentation, representationID: id}
}

func NewSegmentClassificationSubject(id SegmentID) ClassificationSubject {
	return ClassificationSubject{kind: ClassificationSubjectSegment, segmentID: id}
}

func (subject ClassificationSubject) Kind() ClassificationSubjectKind { return subject.kind }

func (subject ClassificationSubject) SourceID() (SourceID, bool) {
	return subject.sourceID, subject.kind == ClassificationSubjectSource
}

func (subject ClassificationSubject) ObservationID() (ObservationID, bool) {
	return subject.observationID, subject.kind == ClassificationSubjectObservation
}

func (subject ClassificationSubject) ArtifactID() (ArtifactID, bool) {
	return subject.artifactID, subject.kind == ClassificationSubjectArtifact
}

func (subject ClassificationSubject) RepresentationID() (RepresentationID, bool) {
	return subject.representationID, subject.kind == ClassificationSubjectRepresentation
}

func (subject ClassificationSubject) SegmentID() (SegmentID, bool) {
	return subject.segmentID, subject.kind == ClassificationSubjectSegment
}

func (subject ClassificationSubject) identityFields(field string) ([]byte, []byte, error) {
	switch subject.kind {
	case ClassificationSubjectSource:
		if subject.sourceID == (SourceID{}) {
			return nil, nil, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(subject.kind), subject.sourceID[:], nil
	case ClassificationSubjectObservation:
		if subject.observationID == (ObservationID{}) {
			return nil, nil, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(subject.kind), subject.observationID[:], nil
	case ClassificationSubjectArtifact:
		if subject.artifactID == (ArtifactID{}) {
			return nil, nil, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(subject.kind), subject.artifactID[:], nil
	case ClassificationSubjectRepresentation:
		if subject.representationID == (RepresentationID{}) {
			return nil, nil, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(subject.kind), subject.representationID[:], nil
	case ClassificationSubjectSegment:
		if subject.segmentID == (SegmentID{}) {
			return nil, nil, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return []byte(subject.kind), subject.segmentID[:], nil
	default:
		return nil, nil, newValidationError(field+".kind", ValidationCodeInvalidEnum, "must be source, observation, artifact, representation, or segment", nil)
	}
}

func (subject ClassificationSubject) MarshalJSON() ([]byte, error) {
	kind, id, err := subject.identityFields("subject")
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{Kind: string(kind), ID: hex.EncodeToString(id)})
}

func (subject *ClassificationSubject) UnmarshalJSON(data []byte) error {
	decoded, err := decodeClassificationSubject(data, "subject")
	if err != nil {
		return err
	}
	*subject = decoded
	return nil
}

func decodeClassificationSubject(data []byte, field string) (ClassificationSubject, error) {
	var wire struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return ClassificationSubject{}, prefixValidationError(err, field)
	}
	switch ClassificationSubjectKind(wire.Kind) {
	case ClassificationSubjectSource:
		id, err := ParseSourceID(wire.ID)
		if err != nil {
			return ClassificationSubject{}, validationErrorForField(err, field+".id")
		}
		if id == (SourceID{}) {
			return ClassificationSubject{}, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return NewSourceClassificationSubject(id), nil
	case ClassificationSubjectObservation:
		id, err := ParseObservationID(wire.ID)
		if err != nil {
			return ClassificationSubject{}, validationErrorForField(err, field+".id")
		}
		if id == (ObservationID{}) {
			return ClassificationSubject{}, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return NewObservationClassificationSubject(id), nil
	case ClassificationSubjectArtifact:
		id, err := ParseArtifactID(wire.ID)
		if err != nil {
			return ClassificationSubject{}, validationErrorForField(err, field+".id")
		}
		if id == (ArtifactID{}) {
			return ClassificationSubject{}, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return NewArtifactClassificationSubject(id), nil
	case ClassificationSubjectRepresentation:
		id, err := ParseRepresentationID(wire.ID)
		if err != nil {
			return ClassificationSubject{}, validationErrorForField(err, field+".id")
		}
		if id == (RepresentationID{}) {
			return ClassificationSubject{}, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return NewRepresentationClassificationSubject(id), nil
	case ClassificationSubjectSegment:
		id, err := ParseSegmentID(wire.ID)
		if err != nil {
			return ClassificationSubject{}, validationErrorForField(err, field+".id")
		}
		if id == (SegmentID{}) {
			return ClassificationSubject{}, newValidationError(field+".id", ValidationCodeInvalidID, "must not be zero", nil)
		}
		return NewSegmentClassificationSubject(id), nil
	default:
		return ClassificationSubject{}, newValidationError(field+".kind", ValidationCodeInvalidEnum, "must be source, observation, artifact, representation, or segment", nil)
	}
}

func NewClassificationID(subject ClassificationSubject, taxonomy, taxonomyVersion, label, asserterID, asserterVersion string, basis []ClassificationSubject, assertedAtUsec int64, confidencePPM uint64) (ClassificationID, error) {
	fields, err := classificationIdentityFields(subject, taxonomy, taxonomyVersion, label, asserterID, asserterVersion, basis, assertedAtUsec, confidencePPM)
	if err != nil {
		return ClassificationID{}, err
	}
	digest := sha256.New()
	writeTuple(digest, fields...)
	return ClassificationID(digest.Sum(nil)), nil
}

func classificationIdentityFields(subject ClassificationSubject, taxonomy, taxonomyVersion, label, asserterID, asserterVersion string, basis []ClassificationSubject, assertedAtUsec int64, confidencePPM uint64) ([][]byte, error) {
	subjectKind, subjectID, err := subject.identityFields("subject")
	if err != nil {
		return nil, err
	}
	for _, value := range []struct{ field, value string }{{"taxonomy", taxonomy}, {"taxonomy_version", taxonomyVersion}, {"label", label}, {"asserter_id", asserterID}, {"asserter_version", asserterVersion}} {
		if err := validateIdentityInput(value.field, value.value); err != nil {
			return nil, err
		}
	}
	if len(basis) == 0 || len(basis) > maxClassificationBasis {
		return nil, newValidationError("basis", ValidationCodeInvalidValue, fmt.Sprintf("must contain between 1 and %d entries", maxClassificationBasis), nil)
	}
	if assertedAtUsec <= 0 {
		return nil, newValidationError("asserted_at_usec", ValidationCodeInvalidValue, "must be positive", nil)
	}
	if confidencePPM > 1_000_000 {
		return nil, newValidationError("confidence_ppm", ValidationCodeInvalidRange, "must not exceed 1000000", nil)
	}
	var assertedAt, confidence, basisCount [8]byte
	binary.BigEndian.PutUint64(assertedAt[:], uint64(assertedAtUsec))
	binary.BigEndian.PutUint64(confidence[:], confidencePPM)
	binary.BigEndian.PutUint64(basisCount[:], uint64(len(basis)))
	fields := [][]byte{[]byte(ClassificationSchema), subjectKind, subjectID, []byte(taxonomy), []byte(taxonomyVersion), []byte(label), []byte(asserterID), []byte(asserterVersion), assertedAt[:], confidence[:], basisCount[:]}
	seen := make(map[ClassificationSubject]struct{}, len(basis))
	for index, entry := range basis {
		kind, id, err := entry.identityFields(fmt.Sprintf("basis[%d]", index))
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[entry]; duplicate {
			return nil, newValidationError(fmt.Sprintf("basis[%d]", index), ValidationCodeInvalidValue, "must not duplicate an earlier basis entry", nil)
		}
		seen[entry] = struct{}{}
		fields = append(fields, kind, id)
	}
	return fields, nil
}

func (record Classification) Validate() error {
	if record.Schema != ClassificationSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.classification.v1", nil)
	}
	if record.ID == (ClassificationID{}) {
		return newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	expected, err := NewClassificationID(record.Subject, record.Taxonomy, record.TaxonomyVersion, record.Label, record.AsserterID, record.AsserterVersion, record.Basis, record.AssertedAtUsec, record.ConfidencePPM)
	if err != nil {
		return err
	}
	if record.ID != expected {
		return newValidationError("id", ValidationCodeInvalidID, "does not match classification evidence", nil)
	}
	return nil
}

func EncodeClassification(record Classification) ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return encodeJSONContract(record, "classification")
}

func DecodeClassification(data []byte) (Classification, error) {
	var wire struct {
		Schema          string            `json:"schema"`
		ID              string            `json:"id"`
		Subject         json.RawMessage   `json:"subject"`
		Taxonomy        string            `json:"taxonomy"`
		TaxonomyVersion string            `json:"taxonomy_version"`
		Label           string            `json:"label"`
		AsserterID      string            `json:"asserter_id"`
		AsserterVersion string            `json:"asserter_version"`
		Basis           []json.RawMessage `json:"basis"`
		AssertedAtUsec  int64             `json:"asserted_at_usec"`
		ConfidencePPM   uint64            `json:"confidence_ppm"`
	}
	if err := decodeJSONContract(data, &wire); err != nil {
		return Classification{}, err
	}
	if wire.Schema != ClassificationSchema {
		return Classification{}, newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.classification.v1", nil)
	}
	id, err := ParseClassificationID(wire.ID)
	if err != nil {
		return Classification{}, validationErrorForField(err, "id")
	}
	if id == (ClassificationID{}) {
		return Classification{}, newValidationError("id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	subject, err := decodeClassificationSubject(wire.Subject, "subject")
	if err != nil {
		return Classification{}, err
	}
	basis := make([]ClassificationSubject, len(wire.Basis))
	for index, raw := range wire.Basis {
		basis[index], err = decodeClassificationSubject(raw, fmt.Sprintf("basis[%d]", index))
		if err != nil {
			return Classification{}, err
		}
	}
	record := Classification{Schema: wire.Schema, ID: id, Subject: subject, Taxonomy: wire.Taxonomy, TaxonomyVersion: wire.TaxonomyVersion, Label: wire.Label, AsserterID: wire.AsserterID, AsserterVersion: wire.AsserterVersion, Basis: basis, AssertedAtUsec: wire.AssertedAtUsec, ConfidencePPM: wire.ConfidencePPM}
	if err := record.Validate(); err != nil {
		return Classification{}, err
	}
	return record, nil
}

func (id ClassificationID) String() string { return hex.EncodeToString(id[:]) }

func ParseClassificationID(value string) (ClassificationID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return ClassificationID{}, err
	}
	return ClassificationID(decoded), nil
}

func (id ClassificationID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *ClassificationID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseClassificationID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

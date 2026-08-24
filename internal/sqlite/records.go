package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/graydeon/mousa/internal/mousa"
)

const (
	maxRecordBytes          = 32 * 1024 * 1024
	maxRepresentationInputs = 4096
)

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type queryer interface {
	queryRower
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (store *Store) PutSource(ctx context.Context, record mousa.Source) error {
	if err := store.requireWritable("put source"); err != nil {
		return err
	}
	data, err := mousa.EncodeSource(record)
	if err != nil {
		return wrap(CodeInvalidRecord, "put source", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	return store.writeImmediate(ctx, "put source", func(conn *sql.Conn) error {
		return putRoot(ctx, conn, "sources", record.ID[:], data, func() error {
			stored, err := getSource(ctx, conn, record.ID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(stored, record) {
				return integrity("verify source", "record disagrees with write")
			}
			return nil
		})
	})
}

func (store *Store) GetSource(ctx context.Context, id mousa.SourceID) (mousa.Source, error) {
	return getSource(ctx, store.db, id)
}

func getSource(ctx context.Context, q queryRower, id mousa.SourceID) (mousa.Source, error) {
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT record_json FROM sources WHERE id = ?`, id[:]).Scan(&data); err != nil {
		return mousa.Source{}, readError("get source", err)
	}
	record, err := decodeCanonical(data, mousa.DecodeSource, mousa.EncodeSource)
	if err != nil {
		return mousa.Source{}, wrap(CodeIntegrity, "get source", err)
	}
	if record.ID != id {
		return mousa.Source{}, integrity("get source", "ID projection disagrees with record")
	}
	return record, nil
}

func (store *Store) PutObservation(ctx context.Context, record mousa.Observation) error {
	if err := store.requireWritable("put observation"); err != nil {
		return err
	}
	data, err := mousa.EncodeObservation(record)
	if err != nil {
		return wrap(CodeInvalidRecord, "put observation", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	return store.writeImmediate(ctx, "put observation", func(conn *sql.Conn) error {
		if err := requireParent(ctx, conn, "sources", record.SourceID[:]); err != nil {
			return err
		}
		return putChild(ctx, conn, "observations", "source_id", record.ID[:], record.SourceID[:], data, func() error {
			stored, err := getObservation(ctx, conn, record.ID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(stored, record) {
				return integrity("verify observation", "record disagrees with write")
			}
			return nil
		})
	})
}

func (store *Store) GetObservation(ctx context.Context, id mousa.ObservationID) (mousa.Observation, error) {
	return getObservation(ctx, store.db, id)
}

func getObservation(ctx context.Context, q queryRower, id mousa.ObservationID) (mousa.Observation, error) {
	var sourceID []byte
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT source_id, record_json FROM observations WHERE id = ?`, id[:]).Scan(&sourceID, &data); err != nil {
		return mousa.Observation{}, readError("get observation", err)
	}
	record, err := decodeCanonical(data, mousa.DecodeObservation, mousa.EncodeObservation)
	if err != nil {
		return mousa.Observation{}, wrap(CodeIntegrity, "get observation", err)
	}
	if record.ID != id || !bytes.Equal(sourceID, record.SourceID[:]) {
		return mousa.Observation{}, integrity("get observation", "relational projection disagrees with record")
	}
	return record, nil
}

func (store *Store) PutArtifact(ctx context.Context, record mousa.Artifact) error {
	if err := store.requireWritable("put artifact"); err != nil {
		return err
	}
	data, err := mousa.EncodeArtifact(record)
	if err != nil {
		return wrap(CodeInvalidRecord, "put artifact", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	return store.writeImmediate(ctx, "put artifact", func(conn *sql.Conn) error {
		if err := requireParent(ctx, conn, "observations", record.ObservationID[:]); err != nil {
			return err
		}
		return putChild(ctx, conn, "artifacts", "observation_id", record.ID[:], record.ObservationID[:], data, func() error {
			stored, err := getArtifact(ctx, conn, record.ID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(stored, record) {
				return integrity("verify artifact", "record disagrees with write")
			}
			return nil
		})
	})
}

func (store *Store) GetArtifact(ctx context.Context, id mousa.ArtifactID) (mousa.Artifact, error) {
	return getArtifact(ctx, store.db, id)
}

func getArtifact(ctx context.Context, q queryRower, id mousa.ArtifactID) (mousa.Artifact, error) {
	var observationID []byte
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT observation_id, record_json FROM artifacts WHERE id = ?`, id[:]).Scan(&observationID, &data); err != nil {
		return mousa.Artifact{}, readError("get artifact", err)
	}
	record, err := decodeCanonical(data, mousa.DecodeArtifact, mousa.EncodeArtifact)
	if err != nil {
		return mousa.Artifact{}, wrap(CodeIntegrity, "get artifact", err)
	}
	if record.ID != id || !bytes.Equal(observationID, record.ObservationID[:]) {
		return mousa.Artifact{}, integrity("get artifact", "relational projection disagrees with record")
	}
	return record, nil
}

func (store *Store) PutRepresentation(ctx context.Context, record mousa.Representation) error {
	if err := store.requireWritable("put representation"); err != nil {
		return err
	}
	data, err := mousa.EncodeRepresentation(record)
	if err != nil {
		return wrap(CodeInvalidRecord, "put representation", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	if len(record.Inputs) > maxRepresentationInputs {
		return wrap(CodeResourceLimit, "put representation", fmt.Errorf("input count %d exceeds %d", len(record.Inputs), maxRepresentationInputs))
	}
	inputs, err := projectedInputs(record.Inputs)
	if err != nil {
		return wrap(CodeInvalidRecord, "put representation", err)
	}
	return store.writeImmediate(ctx, "put representation", func(conn *sql.Conn) error {
		for _, input := range inputs {
			table := "artifacts"
			if input.kind == "representation" {
				table = "representations"
			}
			if err := requireParent(ctx, conn, table, input.id); err != nil {
				return err
			}
		}
		result, err := conn.ExecContext(ctx, `INSERT INTO representations(id, record_json) VALUES(?, ?)`, record.ID[:], data)
		if err != nil {
			if !sqliteConstraint(err) {
				return classify("put representation", err)
			}
			var existing []byte
			if queryErr := conn.QueryRowContext(ctx, `SELECT record_json FROM representations WHERE id = ?`, record.ID[:]).Scan(&existing); queryErr != nil {
				return classify("put representation", err)
			}
			if !bytes.Equal(existing, data) {
				return wrap(CodeConflict, "put representation", err)
			}
			return verifyRepresentation(ctx, conn, record, data)
		}
		if err := oneRow(result); err != nil {
			return wrap(CodeIntegrity, "put representation", err)
		}
		for ordinal, input := range inputs {
			var artifactID, representationID any
			if input.kind == "artifact" {
				artifactID = input.id
			} else {
				representationID = input.id
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO representation_inputs(representation_id, ordinal, input_kind, artifact_id, input_representation_id) VALUES(?, ?, ?, ?, ?)`, record.ID[:], ordinal, input.kind, artifactID, representationID); err != nil {
				return classify("put representation input", err)
			}
		}
		return verifyRepresentation(ctx, conn, record, data)
	})
}

func (store *Store) GetRepresentation(ctx context.Context, id mousa.RepresentationID) (mousa.Representation, error) {
	return getRepresentation(ctx, store.db, id)
}

func getRepresentation(ctx context.Context, q queryer, id mousa.RepresentationID) (mousa.Representation, error) {
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT record_json FROM representations WHERE id = ?`, id[:]).Scan(&data); err != nil {
		return mousa.Representation{}, readError("get representation", err)
	}
	record, err := decodeCanonical(data, mousa.DecodeRepresentation, mousa.EncodeRepresentation)
	if err != nil {
		return mousa.Representation{}, wrap(CodeIntegrity, "get representation", err)
	}
	if record.ID != id {
		return mousa.Representation{}, integrity("get representation", "ID projection disagrees with record")
	}
	if err := verifyInputProjection(ctx, q, record); err != nil {
		return mousa.Representation{}, err
	}
	return record, nil
}

func verifyRepresentation(ctx context.Context, q queryer, want mousa.Representation, wantData []byte) error {
	got, err := getRepresentation(ctx, q, want.ID)
	if err != nil {
		return err
	}
	encoded, err := mousa.EncodeRepresentation(got)
	if err != nil || !bytes.Equal(encoded, wantData) || !reflect.DeepEqual(got, want) {
		return integrity("verify representation", "exact read-back disagrees with write")
	}
	return nil
}

func verifyInputProjection(ctx context.Context, q queryer, record mousa.Representation) error {
	want, err := projectedInputs(record.Inputs)
	if err != nil {
		return wrap(CodeIntegrity, "get representation inputs", err)
	}
	rows, err := q.QueryContext(ctx, `SELECT ordinal, input_kind, artifact_id, input_representation_id FROM representation_inputs WHERE representation_id = ? ORDER BY ordinal`, record.ID[:])
	if err != nil {
		return classify("get representation inputs", err)
	}
	defer rows.Close()
	for ordinal, input := range want {
		if !rows.Next() {
			return integrity("get representation inputs", "missing input")
		}
		var gotOrdinal int
		var kind string
		var artifactID, representationID []byte
		if err := rows.Scan(&gotOrdinal, &kind, &artifactID, &representationID); err != nil {
			return classify("get representation inputs", err)
		}
		gotID := artifactID
		if kind == "representation" {
			gotID = representationID
		}
		if gotOrdinal != ordinal || kind != input.kind || !bytes.Equal(gotID, input.id) || (kind == "artifact" && representationID != nil) || (kind == "representation" && artifactID != nil) {
			return integrity("get representation inputs", "ordered input projection disagrees with record")
		}
	}
	if rows.Next() {
		return integrity("get representation inputs", "extra input")
	}
	if err := rows.Err(); err != nil {
		return classify("get representation inputs", err)
	}
	return nil
}

func (store *Store) PutSegment(ctx context.Context, record mousa.Segment) error {
	if err := store.requireWritable("put segment"); err != nil {
		return err
	}
	data, err := mousa.EncodeSegment(record)
	if err != nil {
		return wrap(CodeInvalidRecord, "put segment", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	selector, _ := record.Selector.TextByteRange()
	start, end := uint64Blob(selector.Start), uint64Blob(selector.End)
	return store.writeImmediate(ctx, "put segment", func(conn *sql.Conn) error {
		parent, err := getRepresentation(ctx, conn, record.RepresentationID)
		if err != nil {
			if IsCode(err, CodeNotFound) {
				return wrap(CodeConflict, "put segment", err)
			}
			return err
		}
		if err := record.ValidateAgainst(parent); err != nil {
			return wrap(CodeInvalidRecord, "put segment", err)
		}
		result, err := conn.ExecContext(ctx, `INSERT INTO segments(id, representation_id, selector_start, selector_end, record_json) VALUES(?, ?, ?, ?, ?)`, record.ID[:], record.RepresentationID[:], start, end, data)
		if err != nil {
			if sqliteConstraint(err) {
				stored, getErr := getSegment(ctx, conn, record.ID)
				if getErr == nil && reflect.DeepEqual(stored, record) {
					return nil
				}
				return wrap(CodeConflict, "put segment", err)
			}
			return classify("put segment", err)
		}
		if err := oneRow(result); err != nil {
			return wrap(CodeIntegrity, "put segment", err)
		}
		stored, err := getSegment(ctx, conn, record.ID)
		if err != nil || !reflect.DeepEqual(stored, record) {
			return integrity("verify segment", "exact read-back disagrees with write")
		}
		return nil
	})
}

func (store *Store) GetSegment(ctx context.Context, id mousa.SegmentID) (mousa.Segment, error) {
	return getSegment(ctx, store.db, id)
}

func getSegment(ctx context.Context, q queryRower, id mousa.SegmentID) (mousa.Segment, error) {
	var representationID, start, end, data []byte
	if err := q.QueryRowContext(ctx, `SELECT representation_id, selector_start, selector_end, record_json FROM segments WHERE id = ?`, id[:]).Scan(&representationID, &start, &end, &data); err != nil {
		return mousa.Segment{}, readError("get segment", err)
	}
	record, err := decodeCanonical(data, mousa.DecodeSegment, mousa.EncodeSegment)
	if err != nil {
		return mousa.Segment{}, wrap(CodeIntegrity, "get segment", err)
	}
	selector, ok := record.Selector.TextByteRange()
	if !ok || record.ID != id || !bytes.Equal(representationID, record.RepresentationID[:]) || !bytes.Equal(start, uint64Blob(selector.Start)) || !bytes.Equal(end, uint64Blob(selector.End)) {
		return mousa.Segment{}, integrity("get segment", "relational projection disagrees with record")
	}
	return record, nil
}

type projectedInput struct {
	kind string
	id   []byte
}

func projectedInputs(inputs []mousa.DerivationInput) ([]projectedInput, error) {
	projected := make([]projectedInput, len(inputs))
	for index, input := range inputs {
		data, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		var wire struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(data, &wire); err != nil {
			return nil, err
		}
		switch wire.Kind {
		case "artifact":
			id, err := mousa.ParseArtifactID(wire.ID)
			if err != nil {
				return nil, err
			}
			projected[index] = projectedInput{kind: wire.Kind, id: append([]byte(nil), id[:]...)}
		case "representation":
			id, err := mousa.ParseRepresentationID(wire.ID)
			if err != nil {
				return nil, err
			}
			projected[index] = projectedInput{kind: wire.Kind, id: append([]byte(nil), id[:]...)}
		default:
			return nil, fmt.Errorf("unsupported input kind %q", wire.Kind)
		}
	}
	return projected, nil
}

func putRoot(ctx context.Context, conn *sql.Conn, table string, id, data []byte, verify func() error) error {
	result, err := conn.ExecContext(ctx, `INSERT INTO `+table+`(id, record_json) VALUES(?, ?)`, id, data)
	if err != nil {
		if sqliteConstraint(err) {
			var existing []byte
			if queryErr := conn.QueryRowContext(ctx, `SELECT record_json FROM `+table+` WHERE id = ?`, id).Scan(&existing); queryErr == nil {
				if bytes.Equal(existing, data) {
					return verify()
				}
				return wrap(CodeConflict, "put "+table, err)
			}
		}
		return classify("put "+table, err)
	}
	if err := oneRow(result); err != nil {
		return wrap(CodeIntegrity, "put "+table, err)
	}
	return verify()
}

func putChild(ctx context.Context, conn *sql.Conn, table, parentColumn string, id, parentID, data []byte, verify func() error) error {
	result, err := conn.ExecContext(ctx, `INSERT INTO `+table+`(id, `+parentColumn+`, record_json) VALUES(?, ?, ?)`, id, parentID, data)
	if err != nil {
		if sqliteConstraint(err) {
			var existingParent, existingData []byte
			if queryErr := conn.QueryRowContext(ctx, `SELECT `+parentColumn+`, record_json FROM `+table+` WHERE id = ?`, id).Scan(&existingParent, &existingData); queryErr == nil {
				if bytes.Equal(existingParent, parentID) && bytes.Equal(existingData, data) {
					return verify()
				}
				return wrap(CodeConflict, "put "+table, err)
			}
		}
		return classify("put "+table, err)
	}
	if err := oneRow(result); err != nil {
		return wrap(CodeIntegrity, "put "+table, err)
	}
	return verify()
}

func requireParent(ctx context.Context, q queryRower, table string, id []byte) error {
	var one int
	if err := q.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return wrap(CodeConflict, "check parent", err)
	} else if err != nil {
		return classify("check parent", err)
	}
	return nil
}

func decodeCanonical[T any](data []byte, decode func([]byte) (T, error), encode func(T) ([]byte, error)) (T, error) {
	var zero T
	if len(data) > maxRecordBytes {
		return zero, fmt.Errorf("stored record size %d exceeds %d", len(data), maxRecordBytes)
	}
	record, err := decode(data)
	if err != nil {
		return zero, err
	}
	canonical, err := encode(record)
	if err != nil {
		return zero, err
	}
	if !bytes.Equal(data, canonical) {
		return zero, errors.New("stored record is not canonical")
	}
	return record, nil
}

func checkRecordSize(data []byte) error {
	if len(data) > maxRecordBytes {
		return wrap(CodeResourceLimit, "encode record", fmt.Errorf("canonical record size %d exceeds %d", len(data), maxRecordBytes))
	}
	return nil
}

func readError(op string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return wrap(CodeNotFound, op, err)
	}
	return classify(op, err)
}

func integrity(op, message string) error {
	return wrap(CodeIntegrity, op, errors.New(message))
}

func oneRow(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("affected %d rows, want 1", affected)
	}
	return nil
}

func uint64Blob(value uint64) []byte {
	data := make([]byte, 8)
	binary.BigEndian.PutUint64(data, value)
	return data
}

func (store *Store) requireWritable(op string) error {
	if store.readOnly {
		return wrap(CodeReadOnly, op, errors.New("store is read-only"))
	}
	return nil
}

func (store *Store) writeImmediate(ctx context.Context, op string, write func(*sql.Conn) error) error {
	if err := store.requireWritable(op); err != nil {
		return err
	}
	conn, err := store.db.Conn(ctx)
	if err != nil {
		return classify(op, err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return classify(op, err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	if err := write(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return classify(op, err)
	}
	committed = true
	return nil
}

func sqliteConstraint(err error) bool {
	var sqliteErr interface{ Code() int }
	return errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 19
}

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"reflect"

	"github.com/graydeon/mousa/internal/mousa"
)

// PutCaller registers one durable caller identity. An exact retry returns success after verifying
// the stored bytes, and a reused external caller identity with different bytes is a conflict.
func (store *Store) PutCaller(ctx context.Context, record mousa.Caller) error {
	if err := store.requireWritable("put caller"); err != nil {
		return err
	}
	data, err := mousa.EncodeCaller(record)
	if err != nil {
		return wrap(CodeInvalidRecord, "put caller", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	return store.writeImmediate(ctx, "put caller", func(conn *sql.Conn) error {
		return putIdentityRecord(ctx, conn, "callers", "namespace", "external_caller_id", record.ID[:], data, record.Namespace, record.ExternalCallerID, func() error {
			stored, err := getCaller(ctx, conn, record.ID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(stored, record) {
				return integrity("verify caller", "record disagrees with write")
			}
			return nil
		})
	})
}

// GetCaller returns one stored caller identity with a verified relational projection.
func (store *Store) GetCaller(ctx context.Context, id mousa.CallerID) (mousa.Caller, error) {
	return getCaller(ctx, store.db, id)
}

func getCaller(ctx context.Context, q queryRower, id mousa.CallerID) (mousa.Caller, error) {
	var namespace, externalCallerID string
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT namespace, external_caller_id, record_json FROM callers WHERE id = ?`, id[:]).Scan(&namespace, &externalCallerID, &data); err != nil {
		return mousa.Caller{}, readError("get caller", err)
	}
	record, err := decodeCanonical(data, mousa.DecodeCaller, mousa.EncodeCaller)
	if err != nil {
		return mousa.Caller{}, wrap(CodeIntegrity, "get caller", err)
	}
	if record.ID != id || record.Namespace != namespace || record.ExternalCallerID != externalCallerID {
		return mousa.Caller{}, integrity("get caller", "relational projection disagrees with record")
	}
	return record, nil
}

// PutPurpose registers one durable purpose identity. An exact retry returns success after verifying
// the stored bytes, and a reused external purpose identity with different bytes is a conflict.
func (store *Store) PutPurpose(ctx context.Context, record mousa.Purpose) error {
	if err := store.requireWritable("put purpose"); err != nil {
		return err
	}
	data, err := mousa.EncodePurpose(record)
	if err != nil {
		return wrap(CodeInvalidRecord, "put purpose", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	return store.writeImmediate(ctx, "put purpose", func(conn *sql.Conn) error {
		return putIdentityRecord(ctx, conn, "purposes", "namespace", "external_purpose_id", record.ID[:], data, record.Namespace, record.ExternalPurposeID, func() error {
			stored, err := getPurpose(ctx, conn, record.ID)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(stored, record) {
				return integrity("verify purpose", "record disagrees with write")
			}
			return nil
		})
	})
}

// GetPurpose returns one stored purpose identity with a verified relational projection.
func (store *Store) GetPurpose(ctx context.Context, id mousa.PurposeID) (mousa.Purpose, error) {
	return getPurpose(ctx, store.db, id)
}

func getPurpose(ctx context.Context, q queryRower, id mousa.PurposeID) (mousa.Purpose, error) {
	var namespace, externalPurposeID string
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT namespace, external_purpose_id, record_json FROM purposes WHERE id = ?`, id[:]).Scan(&namespace, &externalPurposeID, &data); err != nil {
		return mousa.Purpose{}, readError("get purpose", err)
	}
	record, err := decodeCanonical(data, mousa.DecodePurpose, mousa.EncodePurpose)
	if err != nil {
		return mousa.Purpose{}, wrap(CodeIntegrity, "get purpose", err)
	}
	if record.ID != id || record.Namespace != namespace || record.ExternalPurposeID != externalPurposeID {
		return mousa.Purpose{}, integrity("get purpose", "relational projection disagrees with record")
	}
	return record, nil
}

// putIdentityRecord writes one identity row with its tuple projection. When the identity already
// exists, an identical record verifies and a differing record is a conflict, mirroring the root
// record writer.
func putIdentityRecord(ctx context.Context, conn *sql.Conn, table, namespaceColumn, externalColumn string, id, data []byte, namespace, externalID string, verify func() error) error {
	result, err := conn.ExecContext(ctx, `INSERT INTO `+table+`(id, `+namespaceColumn+`, `+externalColumn+`, record_json) VALUES(?, ?, ?, ?)`, id, namespace, externalID, data)
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

// verifyCallerIdentityRecords verifies every stored caller and purpose record, its relational
// projection, and that no identity row exists outside the canonical records.
func verifyCallerIdentityRecords(ctx context.Context, db *sql.DB) error {
	for _, check := range []struct {
		table string
		get   func(ctx context.Context, q queryRower, raw []byte) error
	}{
		{"callers", func(ctx context.Context, q queryRower, raw []byte) error {
			if len(raw) != 32 {
				return integrity("scan callers", "invalid ID length")
			}
			var id mousa.CallerID
			copy(id[:], raw)
			_, err := getCaller(ctx, q, id)
			return err
		}},
		{"purposes", func(ctx context.Context, q queryRower, raw []byte) error {
			if len(raw) != 32 {
				return integrity("scan purposes", "invalid ID length")
			}
			var id mousa.PurposeID
			copy(id[:], raw)
			_, err := getPurpose(ctx, q, id)
			return err
		}},
	} {
		rows, err := db.QueryContext(ctx, `SELECT id FROM `+check.table+` ORDER BY id`)
		if err != nil {
			return startupError("scan "+check.table, err)
		}
		var ids [][]byte
		for rows.Next() {
			var id []byte
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return startupError("scan "+check.table, err)
			}
			ids = append(ids, append([]byte(nil), id...))
		}
		if err := rows.Close(); err != nil {
			return startupError("scan "+check.table, err)
		}
		for _, id := range ids {
			if err := check.get(ctx, db, id); err != nil {
				return err
			}
		}
	}
	return nil
}

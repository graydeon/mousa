package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"reflect"

	"github.com/graydeon/mousa/internal/mousa"
)

func (store *Store) PutPolicyDefinition(ctx context.Context, record mousa.PolicyDefinition) error {
	if err := store.requireWritable("put policy definition"); err != nil {
		return err
	}
	data, err := mousa.EncodePolicyDefinition(record)
	if err != nil {
		return wrap(CodeInvalidRecord, "put policy definition", err)
	}
	if err := checkRecordSize(data); err != nil {
		return err
	}
	return store.writeImmediate(ctx, "put policy definition", func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `INSERT INTO policy_definitions(id, namespace, external_policy_id, external_policy_version, record_json) VALUES(?, ?, ?, ?, ?)`, record.ID[:], record.Namespace, record.ExternalPolicyID, record.ExternalPolicyVersion, data)
		if err != nil {
			if !sqliteConstraint(err) {
				return classify("put policy definition", err)
			}
			var rawID, existing []byte
			var namespace, externalPolicyID, externalPolicyVersion string
			queryErr := conn.QueryRowContext(ctx, `SELECT id, namespace, external_policy_id, external_policy_version, record_json FROM policy_definitions WHERE id = ? OR (namespace = ? AND external_policy_id = ? AND external_policy_version = ?)`, record.ID[:], record.Namespace, record.ExternalPolicyID, record.ExternalPolicyVersion).Scan(&rawID, &namespace, &externalPolicyID, &externalPolicyVersion, &existing)
			if queryErr != nil || !bytes.Equal(rawID, record.ID[:]) || namespace != record.Namespace || externalPolicyID != record.ExternalPolicyID || externalPolicyVersion != record.ExternalPolicyVersion || !bytes.Equal(existing, data) {
				return wrap(CodeConflict, "put policy definition", err)
			}
			if verifyErr := verifyPolicyDefinition(ctx, conn, record, data); verifyErr != nil {
				return wrap(CodeConflict, "put policy definition", verifyErr)
			}
			return nil
		}
		if err := oneRow(result); err != nil {
			return wrap(CodeIntegrity, "put policy definition", err)
		}
		return verifyPolicyDefinition(ctx, conn, record, data)
	})
}

func (store *Store) GetPolicyDefinition(ctx context.Context, id mousa.PolicyDefinitionID) (mousa.PolicyDefinition, error) {
	return getPolicyDefinition(ctx, store.db, id)
}

func getPolicyDefinition(ctx context.Context, q queryRower, id mousa.PolicyDefinitionID) (mousa.PolicyDefinition, error) {
	var namespace, externalPolicyID, externalPolicyVersion string
	var data []byte
	if err := q.QueryRowContext(ctx, `SELECT namespace, external_policy_id, external_policy_version, record_json FROM policy_definitions WHERE id = ?`, id[:]).Scan(&namespace, &externalPolicyID, &externalPolicyVersion, &data); err != nil {
		return mousa.PolicyDefinition{}, readError("get policy definition", err)
	}
	record, err := decodeCanonical(data, mousa.DecodePolicyDefinition, mousa.EncodePolicyDefinition)
	if err != nil {
		return mousa.PolicyDefinition{}, wrap(CodeIntegrity, "get policy definition", err)
	}
	if record.ID != id || record.Namespace != namespace || record.ExternalPolicyID != externalPolicyID || record.ExternalPolicyVersion != externalPolicyVersion {
		return mousa.PolicyDefinition{}, integrity("get policy definition", "relational projection disagrees with record")
	}
	return record, nil
}

func verifyPolicyDefinition(ctx context.Context, q queryRower, want mousa.PolicyDefinition, wantData []byte) error {
	got, err := getPolicyDefinition(ctx, q, want.ID)
	if err != nil {
		return err
	}
	encoded, err := mousa.EncodePolicyDefinition(got)
	if err != nil || !bytes.Equal(encoded, wantData) || !reflect.DeepEqual(got, want) {
		return integrity("verify policy definition", "exact read-back disagrees with write")
	}
	return nil
}

func verifyPolicyDefinitionRecords(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, `SELECT id FROM policy_definitions ORDER BY id`)
	if err != nil {
		return startupError("scan policy definitions", err)
	}
	var ids [][]byte
	for rows.Next() {
		var id []byte
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return startupError("scan policy definitions", err)
		}
		ids = append(ids, append([]byte(nil), id...))
	}
	if err := rows.Close(); err != nil {
		return startupError("scan policy definitions", err)
	}
	for _, raw := range ids {
		if len(raw) != 32 {
			return integrity("scan policy definitions", "invalid ID length")
		}
		var id mousa.PolicyDefinitionID
		copy(id[:], raw)
		if _, err := getPolicyDefinition(ctx, db, id); err != nil {
			return err
		}
	}
	return nil
}

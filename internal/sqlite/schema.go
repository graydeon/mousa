package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"slices"

	"github.com/graydeon/mousa/internal/mousa"
)

//go:embed schema.sql
var schemaSQL string

const (
	// applicationID ("MOSA" in the SQLite header) marks a store written with schema.sql.
	applicationID = 0x4D4F5341
	// preReworkApplicationID ("MOUS") marks a store written by the earlier numbered migrations.
	preReworkApplicationID = 0x4D4F5553
	// schemaVersion is stored in user_version. A store at any other version is refused.
	schemaVersion = 4
)

// errResyncRequired is the refusal for a Mousa store this binary cannot read. There is no
// migration: the store's sources are synced again into a new store.
var errResyncRequired = errors.New("store was written by an earlier Mousa schema; create a new store and re-sync it from its sources")

var requiredObjects = []string{
	"index:access_events_source_id_idx",
	"index:artifacts_observation_id_idx",
	"index:ingest_receipts_source_id_idx",
	"index:local_items_active_representation_idx",
	"index:log_source_kind_seq_idx",
	"index:log_source_seq_idx",
	"index:observations_source_id_idx",
	"index:representation_inputs_artifact_id_idx",
	"index:representation_inputs_representation_id_idx",
	"index:segments_representation_id_idx",
	"index:source_withdrawals_source_id_idx",
	"table:access_events",
	"table:artifacts",
	"table:ingest_gaps",
	"table:ingest_receipts",
	"table:local_items",
	"table:log",
	"table:note_citations",
	"table:notes",
	"table:observations",
	"table:representation_inputs",
	"table:representations",
	"table:segment_lexical_fts",
	"table:segment_lexical_fts_config",
	"table:segment_lexical_fts_content",
	"table:segment_lexical_fts_data",
	"table:segment_lexical_fts_docsize",
	"table:segment_lexical_fts_idx",
	"table:segment_lexical_rows",
	"table:segments",
	"table:source_ingest_state",
	"table:source_withdrawals",
	"table:sources",
	"table:store_identity",
	"table:trail_segments",
	"table:trail_sources",
	"table:trails",
}

// checkStoreIdentity reads only the SQLite header and the object count, so every store this
// binary cannot read is refused before any record is verified. It reports whether the
// database is empty and needs the schema.
func checkStoreIdentity(ctx context.Context, db *sql.DB) (empty bool, err error) {
	var id, version, objects int
	if err := db.QueryRowContext(ctx, `PRAGMA application_id`).Scan(&id); err != nil {
		return false, startupError("read application ID", err)
	}
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return false, startupError("read schema version", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&objects); err != nil {
		return false, startupError("inspect database", err)
	}
	switch {
	case id == 0 && version == 0 && objects == 0:
		return true, nil
	case id == applicationID && version == schemaVersion:
		return false, nil
	case id == applicationID && version > schemaVersion:
		return false, wrap(CodeIncompatibleSchema, "open store", fmt.Errorf("schema version %d is newer than this binary's %d", version, schemaVersion))
	case id == applicationID || id == preReworkApplicationID:
		return false, wrap(CodeIncompatibleSchema, "open store", errResyncRequired)
	default:
		return false, wrap(CodeIncompatibleSchema, "open store", fmt.Errorf("not a Mousa store (SQLite application ID %d)", id))
	}
}

// createSchema applies schema.sql, stamps the header and stores a random store ID in one
// transaction.
func createSchema(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return startupError("acquire schema connection", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return startupError("begin schema", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	statements := schemaSQL + fmt.Sprintf("\nPRAGMA application_id = %d;\nPRAGMA user_version = %d;\n", applicationID, schemaVersion)
	if _, err := conn.ExecContext(ctx, statements); err != nil {
		return startupError("apply schema", err)
	}
	storeID, err := newStoreID()
	if err != nil {
		return wrap(CodeInternal, "create store ID", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO store_identity(id) VALUES(?)`, storeID); err != nil {
		return startupError("store store ID", err)
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return startupError("commit schema", err)
	}
	committed = true
	return conn.Close()
}

// verifyStore checks a store whose identity checkStoreIdentity accepted.
func verifyStore(ctx context.Context, db *sql.DB, requireWAL bool, readOnly bool) error {
	objects, err := schemaObjects(ctx, db)
	if err != nil {
		return startupError("verify database", err)
	}
	if !equalStringSets(objects, requiredObjects) {
		return integrity("verify database", fmt.Sprintf("schema objects are %v, want %v", objects, requiredObjects))
	}

	var quickCheck string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&quickCheck); err != nil {
		return startupError("quick check", err)
	}
	if quickCheck != "ok" {
		return integrity("quick check", quickCheck)
	}
	foreignRows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return startupError("foreign key check", err)
	}
	if foreignRows.Next() {
		foreignRows.Close()
		return integrity("foreign key check", "foreign-key violation")
	}
	if err := foreignRows.Close(); err != nil {
		return startupError("foreign key check", err)
	}
	if err := verifyPragmas(ctx, db, requireWAL); err != nil {
		return err
	}
	if err := verifyCanonicalRecords(ctx, db); err != nil {
		return err
	}
	if err := verifyIngestRecords(ctx, db); err != nil {
		return err
	}
	if err := verifyLog(ctx, db); err != nil {
		return err
	}
	if err := verifyLexicalRecords(ctx, db); err != nil {
		return err
	}
	// The FTS5 integrity-check command is an INSERT, so it needs a writable
	// connection; read-only verification keeps the content-level checks above.
	if !readOnly {
		if err := checkLexicalIndexIntegrity(ctx, db); err != nil {
			return err
		}
	}
	if err := verifyQueryTrails(ctx, db); err != nil {
		return err
	}
	return verifyLocalItemRecords(ctx, db)
}

func schemaObjects(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []string
	for rows.Next() {
		var objectType, name string
		if err := rows.Scan(&objectType, &name); err != nil {
			return nil, err
		}
		objects = append(objects, objectType+":"+name)
	}
	return objects, rows.Err()
}

func verifyPragmas(ctx context.Context, db *sql.DB, requireWAL bool) error {
	queryOnly := 1
	if requireWAL {
		queryOnly = 0
	}
	checks := []struct {
		pragma string
		want   int
	}{
		{"foreign_keys", 1},
		{"busy_timeout", 1000},
		{"synchronous", 2},
		{"wal_autocheckpoint", 1000},
		{"trusted_schema", 0},
		{"query_only", queryOnly},
	}
	for _, check := range checks {
		var got int
		if err := db.QueryRowContext(ctx, `PRAGMA `+check.pragma).Scan(&got); err != nil {
			return startupError("verify pragma "+check.pragma, err)
		}
		if got != check.want {
			return integrity("verify pragma "+check.pragma, fmt.Sprintf("value is %d, want %d", got, check.want))
		}
	}
	if requireWAL {
		var journalMode string
		if err := db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
			return startupError("verify pragma journal_mode", err)
		}
		if journalMode != "wal" {
			return integrity("verify pragma journal_mode", fmt.Sprintf("value is %q, want wal", journalMode))
		}
		if err := db.QueryRowContext(ctx, `PRAGMA journal_mode=OFF`).Scan(&journalMode); err != nil {
			return startupError("verify defensive mode", err)
		}
		if journalMode != "wal" {
			return integrity("verify defensive mode", fmt.Sprintf("journal mode changed to %q", journalMode))
		}
	}
	var fts5 int
	if err := db.QueryRowContext(ctx, `SELECT sqlite_compileoption_used('ENABLE_FTS5')`).Scan(&fts5); err != nil {
		return startupError("verify FTS5", err)
	}
	if fts5 != 1 {
		return wrap(CodeIncompatibleSchema, "verify FTS5", errors.New("SQLite binding lacks ENABLE_FTS5"))
	}
	return nil
}

func verifyCanonicalRecords(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return startupError("begin canonical verification", err)
	}
	defer tx.Rollback()
	checks := []struct {
		table   string
		columns string
		get     func([]byte) error
		scan    func(*sql.Rows) error
	}{
		{"sources", "id", func(raw []byte) error {
			var id mousa.SourceID
			copy(id[:], raw)
			_, err := getSource(ctx, tx, id)
			return err
		}, nil},
		{"observations", "id, source_id, record_json", nil, func(rows *sql.Rows) error {
			var raw, parent, data sql.RawBytes
			if err := rows.Scan(&raw, &parent, &data); err != nil {
				return startupError("scan observations", err)
			}
			if len(raw) != len(mousa.ObservationID{}) {
				return integrity("scan observations", "invalid ID length")
			}
			var id mousa.ObservationID
			copy(id[:], raw)
			_, err := decodeObservationProjection(id, parent, data)
			return err
		}},
		{"artifacts", "id, observation_id, record_json", nil, func(rows *sql.Rows) error {
			var raw, parent, data sql.RawBytes
			if err := rows.Scan(&raw, &parent, &data); err != nil {
				return startupError("scan artifacts", err)
			}
			if len(raw) != len(mousa.ArtifactID{}) {
				return integrity("scan artifacts", "invalid ID length")
			}
			var id mousa.ArtifactID
			copy(id[:], raw)
			_, err := decodeArtifactProjection(id, parent, data)
			return err
		}},
		{"representations", "id", func(raw []byte) error {
			var id mousa.RepresentationID
			copy(id[:], raw)
			_, err := getRepresentation(ctx, tx, id)
			return err
		}, nil},
		{"segments", "id, representation_id, selector_start, selector_end, record_json", nil, func(rows *sql.Rows) error {
			var raw, parent, start, end, data sql.RawBytes
			if err := rows.Scan(&raw, &parent, &start, &end, &data); err != nil {
				return startupError("scan segments", err)
			}
			if len(raw) != len(mousa.SegmentID{}) {
				return integrity("scan segments", "invalid ID length")
			}
			var id mousa.SegmentID
			copy(id[:], raw)
			_, err := decodeSegmentProjection(id, parent, start, end, data)
			return err
		}},
	}
	for _, check := range checks {
		rows, err := tx.QueryContext(ctx, `SELECT `+check.columns+` FROM `+check.table+` ORDER BY id`)
		if err != nil {
			return startupError("scan "+check.table, err)
		}
		if check.scan != nil {
			for rows.Next() {
				if err := check.scan(rows); err != nil {
					rows.Close()
					return err
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return startupError("scan "+check.table, err)
			}
			if err := rows.Close(); err != nil {
				return startupError("scan "+check.table, err)
			}
			continue
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
		if err := rows.Err(); err != nil {
			rows.Close()
			return startupError("scan "+check.table, err)
		}
		if err := rows.Close(); err != nil {
			return startupError("scan "+check.table, err)
		}
		for _, id := range ids {
			if len(id) != 32 {
				return integrity("scan "+check.table, "invalid ID length")
			}
			if err := check.get(id); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return startupError("finish canonical verification", err)
	}
	return nil
}

func startupError(op string, err error) error {
	var storageErr *Error
	if errors.As(err, &storageErr) {
		return err
	}
	classified := classify(op, err)
	if IsCode(classified, CodeIntegrity) {
		return classified
	}
	return classified
}

func equalStringSets(left, right []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(left)), slices.Sorted(slices.Values(right)))
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

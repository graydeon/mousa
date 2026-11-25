package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Migrate upgrades a schema-7 or schema-8 store to the current schema in
// place. The whole migration is one immediate transaction: the header still
// names its old version on any failure, so a partial upgrade is impossible. A
// store at the current version is a no-op; any older Mousa store keeps the
// re-sync refusal; a missing file is never created.
func Migrate(ctx context.Context, path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return wrap(CodeInternal, "resolve database path", err)
	}
	if _, err := os.Stat(absolute); errors.Is(err, os.ErrNotExist) {
		return wrap(CodeNotFound, "migrate store", errors.New("store does not exist"))
	} else if err != nil {
		return wrap(CodeInternal, "stat database", err)
	}
	// Read-only preflight: exact application ID and the one migratable version
	// before the file is opened writable at all.
	preflight, err := connect(ctx, absolute, true)
	if err != nil {
		return err
	}
	id, version, err := storeHeader(ctx, preflight)
	if closeErr := preflight.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := migratable(id, version); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	db, err := connect(ctx, absolute, false)
	if err != nil {
		return err
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return startupError("acquire migration connection", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return startupError("begin migration", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	// A competing migration cannot pass this recheck: whoever lost the begin
	// sees the finished header and stops before the DDL.
	id, version, err = storeHeader(ctx, conn)
	if err != nil {
		return err
	}
	if err := migratable(id, version); err != nil {
		return err
	}
	if _, err := getStoreID(ctx, conn); err != nil {
		return err
	}
	if version == schemaVersion {
		return nil
	}
	// Nothing references log by name, and no triggers or views may exist;
	// refuse before rebuilding rather than after.
	var extras int
	if err := conn.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_schema WHERE type IN ('trigger','view')`).Scan(&extras); err != nil {
		return startupError("inspect migration objects", err)
	}
	if extras != 0 {
		return integrity("migrate store", "store contains triggers or views")
	}
	// Schema 8 already carries paths; schema 7 still needs its path column
	// and backfill before the shared log rebuild.
	statements := []string{}
	if version == 7 {
		statements = append(statements,
			`ALTER TABLE local_items ADD COLUMN path TEXT NOT NULL DEFAULT ''`,
			`UPDATE local_items SET path = CASE
				WHEN EXISTS (SELECT 1 FROM notes n
					WHERE n.source_id = local_items.source_id AND n.item_id = local_items.item_id)
				THEN 'unsorted' ELSE item_id END`)
	}
	statements = append(statements,
		`ALTER TABLE log RENAME TO log_old`,
		`DROP INDEX log_source_kind_seq_idx`,
		`DROP INDEX log_source_seq_idx`,
		logSQL)
	if version == 7 {
		statements = append(statements, pathsSQL)
	}
	statements = append(statements,
		historySQL,
		`INSERT INTO log SELECT * FROM log_old ORDER BY seq`,
		`DROP TABLE log_old`,
		fmt.Sprintf("PRAGMA user_version = %d", schemaVersion))
	for _, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return startupError("migrate store", err)
		}
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return startupError("commit migration", err)
	}
	committed = true
	return conn.Close()
}

// migratable accepts the current version as a no-op and every migratable
// version for upgrade; every other header gets the same refusal an open
// would give.
func migratable(id, version int) error {
	switch {
	case id == applicationID && version == schemaVersion:
		return nil
	case id == applicationID && version >= migratableVersion && version < schemaVersion:
		return nil
	case id == applicationID && version > schemaVersion:
		return wrap(CodeIncompatibleSchema, "migrate store", fmt.Errorf("schema version %d is newer than this binary's %d", version, schemaVersion))
	case id == applicationID || id == preReworkApplicationID:
		return wrap(CodeIncompatibleSchema, "migrate store", errResyncRequired)
	default:
		return wrap(CodeIncompatibleSchema, "migrate store", fmt.Errorf("not a Mousa store (SQLite application ID %d)", id))
	}
}

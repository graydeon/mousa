package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	modernsqlite "modernc.org/sqlite"
)

const applicationID = 1297044819

// Store owns one physical SQLite connection.
type Store struct {
	db       *sql.DB
	readOnly bool
	path     string
}

// Open opens or creates a writable canonical record store.
func Open(ctx context.Context, path string) (*Store, error) {
	return open(ctx, path, false)
}

// OpenReadOnly opens an existing canonical record store without mutating it.
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	return open(ctx, path, true)
}

func open(ctx context.Context, path string, readOnly bool) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, wrap(CodeInternal, "resolve database path", err)
	}
	migrations, err := loadMigrations(migrationFiles)
	if err != nil {
		return nil, wrap(CodeInternal, "load migration", err)
	}
	if readOnly {
		db, err := connect(ctx, absolute, true)
		if err != nil {
			return nil, err
		}
		if err := verifyReadOnly(ctx, db, migrations); err != nil {
			_ = db.Close()
			return nil, err
		}
		return &Store{db: db, readOnly: true, path: absolute}, nil
	}
	if err := preflightWritable(ctx, absolute, migrations); err != nil {
		return nil, err
	}
	db, err := connect(ctx, absolute, false)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db, path: absolute}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func connect(ctx context.Context, absolute string, readOnly bool) (*sql.DB, error) {
	query := url.Values{
		"mode":          {"rwc"},
		"_foreign_keys": {"1"},
		"_busy_timeout": {"1000"},
		"_defensive":    {"1"},
		"_pragma":       {"trusted_schema(0)"},
	}
	if readOnly {
		query.Set("mode", "ro")
		query.Set("_query_only", "1")
	} else {
		query.Set("_journal_mode", "WAL")
		query.Set("_synchronous", "FULL")
		query.Add("_pragma", "wal_autocheckpoint(1000)")
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: query.Encode()}).String()
	connector, err := modernsqlite.NewConnector(dsn)
	if err != nil {
		return nil, wrap(CodeInternal, "create sqlite connector", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, startupError("open sqlite database", err)
	}
	return db, nil
}

func preflightWritable(ctx context.Context, path string, migrations []migration) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return wrap(CodeInternal, "stat database", err)
	}
	if info.Size() == 0 {
		return nil
	}
	db, err := connect(ctx, path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	applicationIDValue, objects, err := inspectDatabase(ctx, db)
	if err != nil {
		return startupError("preflight database", err)
	}
	switch applicationIDValue {
	case 0:
		if len(objects) != 0 {
			return wrap(CodeIncompatibleSchema, "preflight database", errors.New("application ID is zero but user objects exist"))
		}
		return nil
	case applicationID:
		version, err := databaseVersion(ctx, db)
		if err != nil {
			return err
		}
		if version > len(migrations) {
			return wrap(CodeIncompatibleSchema, "preflight database", errors.New("database schema is newer than this binary"))
		}
		if err := verifyVersion(ctx, db, migrations, version, false, true); err != nil {
			return err
		}
		if version > 0 && version < len(migrations) {
			return createVerifiedMigrationBackup(ctx, path, migrations, version)
		}
		return nil
	default:
		return wrap(CodeIncompatibleSchema, "preflight database", errors.New("foreign SQLite application ID"))
	}
}

func verifyReadOnly(ctx context.Context, db *sql.DB, migrations []migration) error {
	applicationIDValue, objects, err := inspectDatabase(ctx, db)
	if err != nil {
		return startupError("verify read-only database", err)
	}
	if applicationIDValue == 0 && len(objects) == 0 {
		return wrap(CodeReadOnly, "verify read-only database", errors.New("database requires migration from version 0"))
	}
	if applicationIDValue != applicationID {
		return wrap(CodeIncompatibleSchema, "verify read-only database", errors.New("foreign SQLite application ID"))
	}
	version, err := databaseVersion(ctx, db)
	if err != nil {
		return err
	}
	if version > len(migrations) {
		return wrap(CodeIncompatibleSchema, "verify read-only database", errors.New("database schema is newer than this binary"))
	}
	if err := verifyVersion(ctx, db, migrations, version, false, true); err != nil {
		return err
	}
	if version < len(migrations) {
		return wrap(CodeReadOnly, "verify read-only database", errors.New("database requires migration"))
	}
	return nil
}

func createVerifiedMigrationBackup(ctx context.Context, path string, migrations []migration, version int) error {
	destination := path + fmt.Sprintf(".pre-migrate-v%d-to-v%d.sqlite", version, version+1)
	if _, err := os.Lstat(destination); err == nil {
		return wrap(CodeConflict, "pre-migration backup", errors.New("backup destination already exists"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return wrap(CodeInternal, "pre-migration backup", err)
	}
	vacuumSource, err := connectMigrationBackupSource(ctx, path)
	if err != nil {
		return err
	}
	defer vacuumSource.Close()
	if _, err := vacuumSource.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		return classify("pre-migration backup", err)
	}
	backup, err := connect(ctx, destination, true)
	if err != nil {
		return wrap(CodeIntegrity, "verify pre-migration backup", err)
	}
	defer backup.Close()
	if err := verifyVersion(ctx, backup, migrations, version, false, true); err != nil {
		return wrap(CodeIntegrity, "verify pre-migration backup", err)
	}
	return nil
}

func connectMigrationBackupSource(ctx context.Context, path string) (*sql.DB, error) {
	query := url.Values{
		"mode":          {"ro"},
		"_foreign_keys": {"1"},
		"_busy_timeout": {"1000"},
		"_defensive":    {"1"},
		"_pragma":       {"trusted_schema(0)"},
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
	connector, err := modernsqlite.NewConnector(dsn)
	if err != nil {
		return nil, wrap(CodeInternal, "open pre-migration backup source", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, startupError("open pre-migration backup source", err)
	}
	return db, nil
}

// Close releases the store connection.
func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

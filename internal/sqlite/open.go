package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	modernsqlite "modernc.org/sqlite"
)

// Store owns one physical SQLite connection.
type Store struct {
	db       *sql.DB
	readOnly bool
	path     string
	// noteConn is non-nil only on the scoped copy Remember passes to its item
	// writer, so the note's ingest work joins the caller's transaction.
	noteConn *sql.Conn
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
	if readOnly {
		db, err := connect(ctx, absolute, true)
		if err != nil {
			return nil, err
		}
		if err := verifyReadOnly(ctx, db); err != nil {
			_ = db.Close()
			return nil, err
		}
		return &Store{db: db, readOnly: true, path: absolute}, nil
	}
	// A writable connection switches the file to WAL, so identity is checked read-only first
	// and a store this binary refuses is never modified.
	if err := preflightWritable(ctx, absolute); err != nil {
		return nil, err
	}
	db, err := connect(ctx, absolute, false)
	if err != nil {
		return nil, err
	}
	if err := initializeWritable(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db, path: absolute}, nil
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

func preflightWritable(ctx context.Context, path string) error {
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
	_, err = checkStoreIdentity(ctx, db)
	return err
}

func initializeWritable(ctx context.Context, db *sql.DB) error {
	empty, err := checkStoreIdentity(ctx, db)
	if err != nil {
		return err
	}
	if empty {
		if err := createSchema(ctx, db); err != nil {
			return err
		}
	}
	return verifyStore(ctx, db, true, false)
}

func verifyReadOnly(ctx context.Context, db *sql.DB) error {
	empty, err := checkStoreIdentity(ctx, db)
	if err != nil {
		return err
	}
	if empty {
		return wrap(CodeReadOnly, "verify read-only database", errors.New("store is empty; open it writable to create it"))
	}
	return verifyStore(ctx, db, false, true)
}

// Close releases the store connection.
func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

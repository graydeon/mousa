package sqlite

import (
	"context"
	"database/sql"
	"errors"

	"github.com/graydeon/mousa/internal/mousa"
)

// SetSourceAccess stores the one access decision for an existing source and
// reports whether the stored value changed.
func (store *Store) SetSourceAccess(ctx context.Context, sourceID mousa.SourceID, access mousa.SourceAccess) (bool, error) {
	if err := store.requireWritable("set source access"); err != nil {
		return false, err
	}
	if err := access.Validate(); err != nil {
		return false, wrap(CodeInvalidRecord, "set source access", err)
	}
	changed := false
	err := store.writeImmediate(ctx, "set source access", func(conn *sql.Conn) error {
		if err := requireParent(ctx, conn, "sources", sourceID[:]); err != nil {
			return err
		}
		current, err := getSourceAccess(ctx, conn, sourceID)
		if err != nil {
			return err
		}
		if current == access {
			return nil
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO source_access(source_id, access) VALUES(?, ?) ON CONFLICT(source_id) DO UPDATE SET access = excluded.access`, sourceID[:], string(access)); err != nil {
			return classify("set source access", err)
		}
		changed = true
		return nil
	})
	return changed, err
}

// getSourceAccess returns the stored access decision for a source, or allow when none is stored.
func getSourceAccess(ctx context.Context, q queryRower, sourceID mousa.SourceID) (mousa.SourceAccess, error) {
	var raw string
	err := q.QueryRowContext(ctx, `SELECT access FROM source_access WHERE source_id = ?`, sourceID[:]).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return mousa.SourceAccessAllow, nil
	}
	if err != nil {
		return "", classify("get source access", err)
	}
	access := mousa.SourceAccess(raw)
	if access.Validate() != nil {
		return "", integrity("get source access", "unknown stored access value")
	}
	return access, nil
}

package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Checkpoint runs a truncating WAL checkpoint and fails if readers keep pages busy.
func (store *Store) Checkpoint(ctx context.Context) error {
	if store.readOnly {
		return wrap(CodeReadOnly, "checkpoint", errors.New("store is read-only"))
	}
	var busy, logFrames, checkpointedFrames int
	if err := store.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		return classify("checkpoint", err)
	}
	if busy != 0 {
		return wrap(CodeBusy, "checkpoint", fmt.Errorf("%d WAL frames remain of %d", logFrames-checkpointedFrames, logFrames))
	}
	return nil
}

// Backup writes a verified SQLite snapshot using VACUUM INTO.
func (store *Store) Backup(ctx context.Context, destination string) error {
	if store.readOnly {
		return wrap(CodeReadOnly, "backup", errors.New("store is read-only"))
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return wrap(CodeInternal, "backup", err)
	}
	if absolute == store.path {
		return wrap(CodeConflict, "backup", errors.New("destination is the live database"))
	}
	if _, err := os.Lstat(absolute); err == nil {
		return wrap(CodeConflict, "backup", errors.New("destination already exists"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return wrap(CodeInternal, "backup", err)
	}
	if _, err := store.db.ExecContext(ctx, `VACUUM INTO ?`, absolute); err != nil {
		return classify("backup", err)
	}
	backup, err := OpenReadOnly(ctx, absolute)
	if err != nil {
		return wrap(CodeIntegrity, "verify backup", err)
	}
	if err := backup.Close(); err != nil {
		return wrap(CodeInternal, "verify backup", err)
	}
	return nil
}

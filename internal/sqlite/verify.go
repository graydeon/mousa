package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"github.com/graydeon/mousa/internal/mousa"
	modernsqlite "modernc.org/sqlite"
)

const logCountsSQL = `
	SELECT 'access_event', count(*) FROM access_events
	UNION ALL SELECT 'ingest_receipt', count(*) FROM ingest_receipts
	UNION ALL SELECT 'item_change', count(*) FROM item_changes
	UNION ALL SELECT 'note', count(*) FROM notes
	UNION ALL SELECT 'path_move', count(*) FROM path_moves
	UNION ALL SELECT 'request_admission', count(*) FROM request_admissions
	UNION ALL SELECT 'request_outcome', count(*) FROM request_outcomes
	UNION ALL SELECT 'source_history', count(*) FROM source_history_events
	UNION ALL SELECT 'source_withdrawal', count(*) FROM source_withdrawals
	UNION ALL SELECT 'trail', count(*) FROM trails`

const storeCountsSQL = logCountsSQL + `
	UNION ALL SELECT 'artifact', count(*) FROM artifacts
	UNION ALL SELECT 'observation', count(*) FROM observations
	UNION ALL SELECT 'representation', count(*) FROM representations
	UNION ALL SELECT 'segment', count(*) FROM segments
	UNION ALL SELECT 'source', count(*) FROM sources
	UNION ALL SELECT 'source_ingest_state', count(*) FROM source_ingest_state`

func verificationCounts(ctx context.Context, q queryer, statement string) (map[string]int64, error) {
	rows, err := q.QueryContext(ctx, statement)
	if err != nil {
		return nil, classify("count verified records", err)
	}
	defer rows.Close()
	counts := make(map[string]int64)
	for rows.Next() {
		var kind string
		var total int64
		if err := rows.Scan(&kind, &total); err != nil {
			return nil, classify("count verified records", err)
		}
		counts[kind] = total
	}
	return counts, classify("count verified records", rows.Err())
}

type Verification struct {
	RecordCounts                  map[string]int64 `json:"record_counts"`
	UnverifiableMetaPathOmissions int64            `json:"unverifiable_meta_path_omissions,omitzero"`
}

// Verify checks a consistent page snapshot, without a writable connection to
// the original store. FTS5's integrity command needs write permission on the copy.
func (store *Store) Verify(ctx context.Context) (Verification, error) {
	dir, err := os.MkdirTemp("", "mousa-verify-")
	if err != nil {
		return Verification{}, wrap(CodeInternal, "create verify snapshot directory", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "snapshot.sqlite")
	if err := store.copyPages(ctx, path); err != nil {
		return Verification{}, err
	}
	db, err := connect(ctx, path, false)
	if err != nil {
		return Verification{}, err
	}
	defer db.Close()
	if _, err := checkStoreIdentity(ctx, db); err != nil {
		return Verification{}, err
	}
	unverifiable, err := verifyStore(ctx, db, true, false)
	if err != nil {
		return Verification{}, err
	}
	counts, err := verificationCounts(ctx, db, storeCountsSQL)
	if err != nil {
		return Verification{}, err
	}
	return Verification{RecordCounts: counts, UnverifiableMetaPathOmissions: unverifiable}, nil
}

func (store *Store) copyPages(ctx context.Context, path string) error {
	conn, err := store.db.Conn(ctx)
	if err != nil {
		return classify("open verify snapshot source", err)
	}
	defer conn.Close()
	err = conn.Raw(func(driverConn any) error {
		source, ok := driverConn.(interface {
			NewBackup(string) (*modernsqlite.Backup, error)
		})
		if !ok {
			return errors.New("SQLite binding has no online backup API")
		}
		destination := (&url.URL{Scheme: "file", Path: path}).String()
		backup, err := source.NewBackup(destination)
		if err != nil {
			return err
		}
		more, err := backup.Step(-1)
		if more && err == nil {
			err = errors.New("SQLite page snapshot did not finish")
		}
		return errors.Join(err, backup.Finish())
	})
	return classify("copy verify snapshot pages", err)
}

func (store *Store) VerifyLog(ctx context.Context) (map[string]int64, error) {
	return verifyLog(ctx, store.db, pathHistories{})
}

// VerifyTrails verifies the named records in one read snapshot, as the store owner.
func (store *Store) VerifyTrails(ctx context.Context, ids []mousa.SourceTrailID) (Verification, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Verification{}, classify("begin named trail verification", err)
	}
	defer tx.Rollback()
	seen := make(map[mousa.SourceTrailID]bool, len(ids))
	histories := pathHistories{}
	var unverifiable int64
	for _, id := range ids {
		if seen[id] {
			continue
		}
		trail, _, err := readTrailRecord(ctx, tx, id)
		if err != nil {
			return Verification{}, err
		}
		count, err := verifyTrailReferences(ctx, tx, trail, histories)
		if err != nil {
			return Verification{}, err
		}
		unverifiable += count
		seen[id] = true
	}
	if err := classify("finish named trail verification", tx.Commit()); err != nil {
		return Verification{}, err
	}
	return Verification{
		RecordCounts:                  map[string]int64{"trail": int64(len(seen))},
		UnverifiableMetaPathOmissions: unverifiable,
	}, nil
}

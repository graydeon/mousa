package sqlite

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

// The append-only log is the ordering of record: every receipt, access event and
// withdrawal is one leaf written atomically with its record, verified at open and on
// every current-access read.
func TestLogLeavesForEveryRecordInOrder(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "log.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedEvaluationFixture(t, store)
	batch := testPushBatch(t, "log-order", nil)
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatalf("ApplyIngest: %v", err)
	}
	allow, _, err := store.SetSourceAccess(ctx, batch.Source.ID, mousa.SourceAccessAllow, "test")
	if err != nil {
		t.Fatal(err)
	}
	deny, _, err := store.SetSourceAccess(ctx, batch.Source.ID, mousa.SourceAccessDeny, "test")
	if err != nil {
		t.Fatal(err)
	}
	withdrawalID := withdrawVerifiedSource(t, store, batch.Source.ID, "log-withdraw", 7)

	type leaf struct {
		seq      int64
		kind     string
		recordID []byte
		sourceID []byte
		hash     []byte
	}
	rows, err := store.db.QueryContext(ctx, `SELECT seq, kind, record_id, source_id, leaf_hash FROM log ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	var leaves []leaf
	for rows.Next() {
		var l leaf
		if err := rows.Scan(&l.seq, &l.kind, &l.recordID, &l.sourceID, &l.hash); err != nil {
			t.Fatal(err)
		}
		leaves = append(leaves, l)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		kind     string
		recordID []byte
	}{
		{mousa.LogKindIngestReceipt, batch.Observation.ID[:]},
		{mousa.LogKindAccessEvent, allow.ID[:]},
		{mousa.LogKindAccessEvent, deny.ID[:]},
		{mousa.LogKindSourceWithdrawal, withdrawalID[:]},
	}
	if len(leaves) != len(want) {
		t.Fatalf("leaves = %+v, want %d records", leaves, len(want))
	}
	for i, l := range leaves {
		if l.seq != int64(i+1) || l.kind != want[i].kind || !bytes.Equal(l.recordID, want[i].recordID) || !bytes.Equal(l.sourceID, batch.Source.ID[:]) {
			t.Fatalf("leaf %d = %+v, want seq %d kind %s", i, l, i+1, want[i].kind)
		}
		var record []byte
		var table, column string
		switch l.kind {
		case mousa.LogKindIngestReceipt:
			table, column = "ingest_receipts", "observation_id"
		case mousa.LogKindAccessEvent:
			table, column = "access_events", "id"
		default:
			table, column = "source_withdrawals", "id"
		}
		if err := store.db.QueryRowContext(ctx, `SELECT record_json FROM `+table+` WHERE `+column+` = ?`, l.recordID).Scan(&record); err != nil {
			t.Fatalf("record for %s leaf: %v", l.kind, err)
		}
		wantHash := mousa.LogLeafHash(record)
		if !bytes.Equal(l.hash, wantHash[:]) {
			t.Fatalf("leaf %d hash disagrees with its record", i)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	reopened.Close()
}

// A record that cannot write its leaf commits nothing: the leaf insert runs in the same
// transaction as the record. The access event ID depends on the wall-clock time chosen
// inside SetSourceAccess, so that case fails the leaf insert with a trigger instead of a
// pre-seeded leaf.
func TestLogLeafIsAtomicWithItsRecord(t *testing.T) {
	ctx := context.Background()
	insertLeaf := func(t *testing.T, store *Store, seq int64, kind string, recordID, sourceID []byte) {
		t.Helper()
		rawExec(t, store.path, fmt.Sprintf(`INSERT INTO log(seq, kind, record_id, source_id, leaf_hash) VALUES(%d, '%s', X'%s', X'%s', zeroblob(32))`, seq, kind, hex.EncodeToString(recordID), hex.EncodeToString(sourceID)))
	}
	count := func(t *testing.T, store *Store, table string) int {
		t.Helper()
		var n int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("ingest_receipt", func(t *testing.T) {
		store, err := Open(ctx, filepath.Join(t.TempDir(), "receipt.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		seedEvaluationFixture(t, store)
		batch := testPushBatch(t, "atomic-receipt", nil)
		insertLeaf(t, store, 1, mousa.LogKindIngestReceipt, batch.Observation.ID[:], batch.Source.ID[:])
		if err := store.ApplyIngest(ctx, batch); !IsCode(err, CodeIntegrity) {
			t.Fatalf("ApplyIngest = %v, want integrity", err)
		}
		if n := count(t, store, "ingest_receipts"); n != 0 {
			t.Fatalf("receipt committed without its leaf: %d", n)
		}
		if n := count(t, store, "observations"); n != 0 {
			t.Fatalf("batch committed without its receipt: %d", n)
		}
	})

	t.Run("source_withdrawal", func(t *testing.T) {
		store, err := Open(ctx, filepath.Join(t.TempDir(), "withdrawal.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		seedEvaluationFixture(t, store)
		batch := testPushBatch(t, "atomic-withdrawal", nil)
		if err := store.ApplyIngest(ctx, batch); err != nil {
			t.Fatal(err)
		}
		withdrawalID, err := mousa.NewWithdrawalID(batch.Source.ID, "atomic-withdrawal")
		if err != nil {
			t.Fatal(err)
		}
		insertLeaf(t, store, 2, mousa.LogKindSourceWithdrawal, withdrawalID[:], batch.Source.ID[:])
		withdrawal := mousa.SourceWithdrawal{Schema: mousa.SourceWithdrawalSchema, ID: withdrawalID, SourceID: batch.Source.ID, ExternalWithdrawalID: "atomic-withdrawal", AdapterID: "test.adapter", AdapterVersion: "1", OccurredAtUsec: 9}
		if err := store.WithdrawSource(ctx, withdrawal); !IsCode(err, CodeIntegrity) {
			t.Fatalf("WithdrawSource = %v, want integrity", err)
		}
		if n := count(t, store, "source_withdrawals"); n != 0 {
			t.Fatalf("withdrawal committed without its leaf: %d", n)
		}
		state, err := store.GetIngestState(ctx, batch.Source.ID)
		if err != nil || state.CollectionState != mousa.CollectionActive {
			t.Fatalf("ingest state = %+v, %v, want active", state, err)
		}
	})

	t.Run("access_event", func(t *testing.T) {
		store, err := Open(ctx, filepath.Join(t.TempDir(), "access.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		seedEvaluationFixture(t, store)
		batch := testPushBatch(t, "atomic-access", nil)
		if err := store.ApplyIngest(ctx, batch); err != nil {
			t.Fatal(err)
		}
		rawExec(t, store.path, `CREATE TRIGGER fail_access_leaf BEFORE INSERT ON log WHEN NEW.kind = 'access_event' BEGIN SELECT RAISE(ABORT, 'leaf refused'); END`)
		if _, _, err := store.SetSourceAccess(ctx, batch.Source.ID, mousa.SourceAccessAllow, "test"); err == nil {
			t.Fatal("SetSourceAccess succeeded without its leaf")
		}
		if n := count(t, store, "access_events"); n != 0 {
			t.Fatalf("access event committed without its leaf: %d", n)
		}
	})
}

// An access event or its leaf that is edited outside the store is refused on the next read,
// not only at open.
func TestTamperedAccessEventOrLeafRefusedOnRead(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name   string
		damage func(sourceID, otherSourceID mousa.SourceID) string
		target func(sourceID, otherSourceID mousa.SourceID) mousa.SourceID
	}{
		{"record json", func(sourceID, _ mousa.SourceID) string {
			return `UPDATE access_events SET record_json = CAST(replace(record_json, '"access":"allow"', '"access":"deny"') AS BLOB)`
		}, func(sourceID, _ mousa.SourceID) mousa.SourceID { return sourceID }},
		{"access column", func(sourceID, _ mousa.SourceID) string {
			return `UPDATE access_events SET access = 'deny'`
		}, func(sourceID, _ mousa.SourceID) mousa.SourceID { return sourceID }},
		{"leaf hash", func(sourceID, _ mousa.SourceID) string {
			return `UPDATE log SET leaf_hash = zeroblob(32) WHERE kind = 'access_event'`
		}, func(sourceID, _ mousa.SourceID) mousa.SourceID { return sourceID }},
		{"leaf source", func(_, otherSourceID mousa.SourceID) string {
			return fmt.Sprintf(`UPDATE log SET source_id = X'%s' WHERE kind = 'access_event'`, hex.EncodeToString(otherSourceID[:]))
		}, func(_, otherSourceID mousa.SourceID) mousa.SourceID { return otherSourceID }},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tampered.sqlite")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			seedEvaluationFixture(t, store)
			if err := store.PutSource(ctx, testSourceNamed(t, "second")); err != nil {
				t.Fatal(err)
			}
			batch := testPushBatch(t, "tampered", nil)
			if err := store.ApplyIngest(ctx, batch); err != nil {
				t.Fatal(err)
			}
			allowSource(t, store, batch.Source.ID)
			other := testSourceNamed(t, "second")
			rawExec(t, store.path, test.damage(batch.Source.ID, other.ID))
			if _, err := store.Query(ctx, auditRequest(test.target(batch.Source.ID, other.ID))); !IsCode(err, CodeIntegrity) {
				t.Fatalf("Query = %v, want integrity", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := verifyPath(ctx, path); !IsCode(err, CodeIntegrity) {
				t.Fatalf("verify = %v, want integrity", err)
			}
		})
	}
}

// The sequence is the ordering of record: a gap, a jump or a missing leaf is refused at
// open.
func TestLogSequenceGapRefused(t *testing.T) {
	ctx := context.Background()
	for name, damage := range map[string]string{
		"middle leaf deleted":  `DELETE FROM log WHERE seq = 2`,
		"last leaf renumbered": `UPDATE log SET seq = seq + 5 WHERE seq = (SELECT max(seq) FROM log)`,
		"last leaf deleted":    `DELETE FROM log WHERE seq = (SELECT max(seq) FROM log)`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "gap.sqlite")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			seedEvaluationFixture(t, store)
			batch := testPushBatch(t, "gap", nil)
			if err := store.ApplyIngest(ctx, batch); err != nil {
				t.Fatal(err)
			}
			allowSource(t, store, batch.Source.ID)
			if _, _, err := store.SetSourceAccess(ctx, batch.Source.ID, mousa.SourceAccessDeny, "test"); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			rawExec(t, path, damage)
			if err := verifyPath(ctx, path); !IsCode(err, CodeIntegrity) {
				t.Fatalf("verify = %v, want integrity", err)
			}
		})
	}
}

func TestStoreIDRandomAndStable(t *testing.T) {
	ctx := context.Background()
	pathA := filepath.Join(t.TempDir(), "a.sqlite")
	pathB := filepath.Join(t.TempDir(), "b.sqlite")
	storeA, err := Open(ctx, pathA)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	storeB, err := Open(ctx, pathB)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()
	idA, err := getStoreID(ctx, storeA.db)
	if err != nil {
		t.Fatal(err)
	}
	idB, err := getStoreID(ctx, storeB.db)
	if err != nil {
		t.Fatal(err)
	}
	if idA == (mousa.StoreID{}) || idA == idB {
		t.Fatalf("store IDs = %v, %v, want distinct nonzero", idA, idB)
	}
	if err := storeA.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, pathA)
	if err != nil {
		t.Fatal(err)
	}
	again, err := getStoreID(ctx, reopened.db)
	if err != nil || again != idA {
		t.Fatalf("reopened ID = %v, %v, want %v", again, err, idA)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	rawExec(t, pathA, `INSERT INTO store_identity(id) VALUES(randomblob(16))`)
	reopened, err = Open(ctx, pathA)
	if !IsCode(err, CodeIntegrity) {
		if reopened != nil {
			reopened.Close()
		}
		t.Fatalf("two identities = %v, want integrity", err)
	}
}

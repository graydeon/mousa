package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

// SetSourceAccess appends an access event for an existing source unless its latest event
// already sets the same access. It returns the current event and whether one was appended.
func (store *Store) SetSourceAccess(ctx context.Context, sourceID mousa.SourceID, access mousa.SourceAccess, actor string) (mousa.AccessEvent, bool, error) {
	return store.setSourceAccess(ctx, sourceID, access, actor, false, time.Now().UnixMicro())
}

// AllowSourceIfUnset appends an allow event only when the source has no access event, so it
// never reverses an explicit deny. It returns the current event and whether one was appended.
func (store *Store) AllowSourceIfUnset(ctx context.Context, sourceID mousa.SourceID, actor string) (mousa.AccessEvent, bool, error) {
	return store.setSourceAccess(ctx, sourceID, mousa.SourceAccessAllow, actor, true, time.Now().UnixMicro())
}

// setSourceAccess reads the latest event and appends in one write transaction. With ifUnset,
// any existing event is kept. The new event's time is the later of nowUsec and the previous
// event's time plus 1, so time strictly increases with seq within a source.
func (store *Store) setSourceAccess(ctx context.Context, sourceID mousa.SourceID, access mousa.SourceAccess, actor string, ifUnset bool, nowUsec int64) (mousa.AccessEvent, bool, error) {
	var current mousa.AccessEvent
	changed := false
	err := store.writeImmediate(ctx, "set source access", func(conn *sql.Conn) error {
		if err := requireParent(ctx, conn, "sources", sourceID[:]); err != nil {
			return err
		}
		latest, err := currentAccessEvent(ctx, conn, sourceID)
		if err != nil {
			return err
		}
		if latest != nil && (ifUnset || latest.Access == access) {
			current = *latest
			return nil
		}
		occurred := nowUsec
		if latest != nil {
			occurred = max(occurred, latest.OccurredAtUsec+1)
		}
		event, err := mousa.NewAccessEvent(sourceID, access, actor, occurred)
		if err != nil {
			return wrap(CodeInvalidRecord, "set source access", err)
		}
		data, err := mousa.EncodeAccessEvent(event)
		if err != nil {
			return wrap(CodeInvalidRecord, "set source access", err)
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO access_events(id, source_id, access, occurred_at_usec, record_json) VALUES(?, ?, ?, ?, ?)`, event.ID[:], sourceID[:], string(access), event.OccurredAtUsec, data); err != nil {
			if sqliteConstraint(err) {
				return wrap(CodeConflict, "set source access", err)
			}
			return classify("set source access", err)
		}
		if err := appendLog(ctx, conn, mousa.LogKindAccessEvent, event.ID[:], sourceID[:], data); err != nil {
			return err
		}
		stored, err := currentAccessEvent(ctx, conn, sourceID)
		if err != nil {
			return err
		}
		if stored == nil || *stored != event {
			return integrity("set source access", "access event read-back disagrees")
		}
		current = event
		changed = true
		return nil
	})
	if err != nil {
		return mousa.AccessEvent{}, false, err
	}
	return current, changed, nil
}

// currentAccessEvent reads every access-event leaf of the source in log order, checks each
// leaf against its event, that event times strictly increase, and that the source has exactly
// one event row per leaf. It returns the last event. An edited, reordered or unpaired event or
// leaf is refused on every read, not only at open.
func currentAccessEvent(ctx context.Context, q queryer, sourceID mousa.SourceID) (*mousa.AccessEvent, error) {
	type accessLeaf struct{ recordID, hash []byte }
	rows, err := q.QueryContext(ctx, `SELECT record_id, leaf_hash FROM log WHERE source_id = ? AND kind = 'access_event' ORDER BY seq`, sourceID[:])
	if err != nil {
		return nil, classify("get source access", err)
	}
	var leaves []accessLeaf
	for rows.Next() {
		var leaf accessLeaf
		if err := rows.Scan(&leaf.recordID, &leaf.hash); err != nil {
			rows.Close()
			return nil, classify("get source access", err)
		}
		leaves = append(leaves, leaf)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, classify("get source access", err)
	}
	if err := rows.Close(); err != nil {
		return nil, classify("get source access", err)
	}
	var events int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM access_events WHERE source_id = ?`, sourceID[:]).Scan(&events); err != nil {
		return nil, classify("get source access", err)
	}
	if events != len(leaves) {
		return nil, integrity("get source access", fmt.Sprintf("%d access events but %d leaves", events, len(leaves)))
	}
	var latest *mousa.AccessEvent
	for _, leaf := range leaves {
		if len(leaf.recordID) != len(mousa.AccessEventID{}) {
			return nil, integrity("get source access", "invalid access event ID")
		}
		event, data, err := getAccessEvent(ctx, q, mousa.AccessEventID(leaf.recordID))
		if IsCode(err, CodeNotFound) {
			return nil, integrity("get source access", "access event leaf names a missing record")
		}
		if err != nil {
			return nil, err
		}
		if err := checkLeaf(mousa.LogKindAccessEvent, sourceID[:], leaf.hash, data, event.SourceID[:]); err != nil {
			return nil, integrity("get source access", err.Error())
		}
		if latest != nil && event.OccurredAtUsec <= latest.OccurredAtUsec {
			return nil, integrity("get source access", "access event times do not increase in log order")
		}
		latest = &event
	}
	return latest, nil
}

func getAccessEvent(ctx context.Context, q queryRower, id mousa.AccessEventID) (mousa.AccessEvent, []byte, error) {
	var sourceID, data []byte
	var access string
	var occurred int64
	if err := q.QueryRowContext(ctx, `SELECT source_id, access, occurred_at_usec, record_json FROM access_events WHERE id = ?`, id[:]).Scan(&sourceID, &access, &occurred, &data); err != nil {
		return mousa.AccessEvent{}, nil, readError("get access event", err)
	}
	event, err := decodeCanonical(data, mousa.DecodeAccessEvent, mousa.EncodeAccessEvent)
	if err != nil {
		return mousa.AccessEvent{}, nil, wrap(CodeIntegrity, "get access event", err)
	}
	if event.ID != id || !bytes.Equal(sourceID, event.SourceID[:]) || access != string(event.Access) || occurred != event.OccurredAtUsec {
		return mousa.AccessEvent{}, nil, integrity("get access event", "relational projection disagrees with record")
	}
	return event, data, nil
}

// verifyAccessEvents decodes every access event; verifyLog has already matched each to its leaf.
func verifyAccessEvents(ctx context.Context, q queryer) error {
	rows, err := q.QueryContext(ctx, `SELECT id FROM access_events ORDER BY id`)
	if err != nil {
		return startupError("scan access events", err)
	}
	var ids []mousa.AccessEventID
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return startupError("scan access events", err)
		}
		if len(raw) != len(mousa.AccessEventID{}) {
			rows.Close()
			return integrity("scan access events", "invalid ID length")
		}
		ids = append(ids, mousa.AccessEventID(raw))
	}
	if err := rows.Close(); err != nil {
		return startupError("scan access events", err)
	}
	for _, id := range ids {
		if _, _, err := getAccessEvent(ctx, q, id); err != nil {
			return err
		}
	}
	return nil
}

// snapshotAccess returns the access state and cited event for one evaluation: deny with no
// event when the source has never had an access event.
func snapshotAccess(ctx context.Context, q queryer, sourceID mousa.SourceID) (mousa.SourceAccess, *mousa.AccessEventID, error) {
	event, err := currentAccessEvent(ctx, q, sourceID)
	if err != nil {
		return "", nil, err
	}
	if event == nil {
		return mousa.SourceAccessDeny, nil, nil
	}
	return event.Access, &event.ID, nil
}

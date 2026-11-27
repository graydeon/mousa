package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
)

// localItemLocation reads the projected location and activation evidence of
// one item without the full canonical ancestry check GetLocalItem performs.
func localItemLocation(ctx context.Context, q queryer, sourceID mousa.SourceID, itemID string) (string, uint64, error) {
	var path string
	var activationSeq uint64
	if err := q.QueryRowContext(ctx, `SELECT path, coalesce(activation_seq, 0) FROM local_items WHERE source_id = ? AND item_id = ?`, sourceID[:], itemID).Scan(&path, &activationSeq); err != nil {
		return "", 0, readError("get local item path", err)
	}
	if !validStoredPath(path, itemID) {
		return "", 0, integrity("get local item path", "invalid item path projection")
	}
	return path, activationSeq, nil
}

// localItemPath reads the projected location of one item. The move writer uses
// it inside its own transaction; the query reader uses localItemLocation.
func localItemPath(ctx context.Context, q queryer, sourceID mousa.SourceID, itemID string) (string, error) {
	path, _, err := localItemLocation(ctx, q, sourceID, itemID)
	return path, err
}

// moveEntries resolves one validated selector into the ordered move records
// it would write, inside the caller's transaction. A note identity resolves
// through the notes projection; an item ID names a document item, never a
// note's item; a prefix selects every stored path under it by a literal
// comparison, active and inactive rows alike. A single selector on a missing
// item is not found; a prefix that moves nothing and a single item already
// at the destination are both an empty result.
func moveEntries(ctx context.Context, q queryer, sourceID mousa.SourceID, selector mousa.MoveSelector) ([]mousa.PathMoveEntry, error) {
	entries := []mousa.PathMoveEntry{}
	add := func(itemID, old string) error {
		to := selector.To
		if selector.Prefix != "" {
			rest, ok := strings.CutPrefix(old, selector.Prefix)
			if !ok {
				return integrity("move", "item path disagrees with prefix selection")
			}
			to = strings.TrimSuffix(selector.To, "/") + "/" + rest
		}
		if !mousa.ValidPath(to) {
			return wrap(CodeInvalidRecord, "move", fmt.Errorf("destination %q is not a valid path", to))
		}
		if old != to {
			entries = append(entries, mousa.PathMoveEntry{ItemID: itemID, OldPath: old, NewPath: to})
		}
		return nil
	}
	switch {
	case selector.NoteID != (mousa.SHA256{}):
		var itemID, old string
		err := q.QueryRowContext(ctx, `SELECT li.item_id, li.path FROM notes n
			JOIN local_items li ON li.source_id = n.source_id AND li.item_id = n.item_id
			WHERE n.id = ? AND n.source_id = ?`, selector.NoteID[:], sourceID[:]).Scan(&itemID, &old)
		if err != nil {
			return nil, readError("resolve move note", err)
		}
		if err := add(itemID, old); err != nil {
			return nil, err
		}
	case selector.ItemID != "":
		var old string
		err := q.QueryRowContext(ctx, `SELECT li.path FROM local_items li
			WHERE li.source_id = ? AND li.item_id = ? AND NOT EXISTS (
				SELECT 1 FROM notes n WHERE n.source_id = li.source_id AND n.item_id = li.item_id
			)`, sourceID[:], selector.ItemID).Scan(&old)
		if err != nil {
			return nil, readError("resolve move item", err)
		}
		if err := add(selector.ItemID, old); err != nil {
			return nil, err
		}
	default:
		rows, err := q.QueryContext(ctx, `SELECT item_id, path FROM local_items
			WHERE source_id = ? AND substr(path, 1, ?) = ? ORDER BY item_id`,
			sourceID[:], len(selector.Prefix), selector.Prefix)
		if err != nil {
			return nil, classify("resolve move prefix", err)
		}
		defer rows.Close()
		for rows.Next() {
			var item, old string
			if err := rows.Scan(&item, &old); err != nil {
				return nil, classify("resolve move prefix", err)
			}
			if err := add(item, old); err != nil {
				return nil, err
			}
		}
		if err := rows.Err(); err != nil {
			return nil, classify("resolve move prefix", err)
		}
	}
	return entries, nil
}

// appendPathMove writes one move's record and log leaf inside an existing
// write transaction and applies the projection update. It returns the sealed
// record identity so callers can bind it as request evidence.
func appendPathMove(ctx context.Context, conn *sql.Conn, sourceID mousa.SourceID, entries []mousa.PathMoveEntry, actor string) (mousa.SHA256, error) {
	var tail uint64
	if err := conn.QueryRowContext(ctx, logIndexSQL).Scan(&tail); err != nil {
		return mousa.SHA256{}, classify("read path move index", err)
	}
	move := mousa.PathMove{Actor: actor, Records: entries, Seq: tail + 1, SourceID: sourceID}
	data, id, err := mousa.EncodePathMove(move)
	if err != nil {
		return mousa.SHA256{}, wrap(CodeInvalidRecord, "encode path move", err)
	}
	if err := checkRecordSize(data); err != nil {
		return mousa.SHA256{}, err
	}
	for _, entry := range entries {
		result, err := conn.ExecContext(ctx, `UPDATE local_items SET path = ? WHERE source_id = ? AND item_id = ? AND path = ?`,
			entry.NewPath, sourceID[:], entry.ItemID, entry.OldPath)
		if err != nil {
			return mousa.SHA256{}, classify("apply path move", err)
		}
		if err := oneRow(result); err != nil {
			return mousa.SHA256{}, integrity("apply path move", "item path changed since the move was read")
		}
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO path_moves(id, source_id, record_json) VALUES(?, ?, ?)`, id[:], sourceID[:], data); err != nil {
		return mousa.SHA256{}, classify("write path move", err)
	}
	if err := appendLog(ctx, conn, mousa.LogKindPathMove, id[:], sourceID[:], data); err != nil {
		return mousa.SHA256{}, err
	}
	stored, err := readPathMove(ctx, conn, id)
	if err != nil {
		return mousa.SHA256{}, err
	}
	if !reflect.DeepEqual(stored, move) {
		return mousa.SHA256{}, integrity("read back path move", "projection disagrees with write")
	}
	return id, nil
}

// readPathMove reads and verifies one stored move record with its leaf.
func readPathMove(ctx context.Context, q queryer, id mousa.SHA256) (mousa.PathMove, error) {
	var seq int64
	var kind string
	var recordID, leafSource, leaf, record, recordSource []byte
	err := q.QueryRowContext(ctx, logRecordSQL+` WHERE l.kind = 'path_move' AND l.record_id = ?`, id[:]).
		Scan(&seq, &kind, &recordID, &leafSource, &leaf, &record, &recordSource)
	if err != nil {
		return mousa.PathMove{}, readError("read path move", err)
	}
	return decodePathMoveLeaf(uint64(seq), kind, recordID, leafSource, leaf, record, recordSource)
}

func decodePathMoveLeaf(seq uint64, kind string, recordID, leafSource, leaf, record, recordSource []byte) (mousa.PathMove, error) {
	if err := checkLeaf(kind, leafSource, leaf, record, recordSource); err != nil {
		return mousa.PathMove{}, wrap(CodeIntegrity, "read path move", err)
	}
	move, err := mousa.DecodePathMove(record)
	if err != nil {
		return mousa.PathMove{}, wrap(CodeIntegrity, "decode path move", err)
	}
	// The record is known-canonical, so its identity is its hash directly.
	recordHash := sha256.Sum256(record)
	if move.Seq != seq || !bytes.Equal(recordHash[:], recordID) || !bytes.Equal(move.SourceID[:], leafSource) {
		return mousa.PathMove{}, integrity("read path move", "record disagrees with its leaf or identity")
	}
	return move, nil
}

// verifyPathHeads compares every stored item path with the path the logged
// move chain leaves, consuming each head it accounts for. Items never moved
// keep whatever path activation wrote; their projection still has to be a
// valid path or their literal item ID.
func verifyPathHeads(ctx context.Context, q queryer, heads map[localItemKey]string) error {
	rows, err := q.QueryContext(ctx, `SELECT source_id, item_id, path FROM local_items`)
	if err != nil {
		return classify("scan item paths", err)
	}
	defer rows.Close()
	for rows.Next() {
		var source []byte
		var item, path string
		if err := rows.Scan(&source, &item, &path); err != nil {
			return classify("scan item paths", err)
		}
		if len(source) != len(mousa.SourceID{}) {
			return integrity("verify item paths", "invalid item identity projection")
		}
		key := localItemKey{mousa.SourceID(source), item}
		if !validStoredPath(path, item) {
			return integrity("verify item paths", "item path is not a valid path")
		}
		if head, exists := heads[key]; exists {
			if head != path {
				return integrity("verify item paths", "local item path disagrees with log history")
			}
			delete(heads, key)
		}
	}
	if err := rows.Err(); err != nil {
		return classify("scan item paths", err)
	}
	if len(heads) != 0 {
		return integrity("verify item paths", "path move names an item with no projection")
	}
	return nil
}

// pathMoveAfter verifies the path-move leaves appended after a sequence and
// returns the single one covering this source and item, with its identity.
func pathMoveAfter(ctx context.Context, conn *sql.Conn, after uint64, source mousa.SourceID, itemID string) (mousa.PathMove, mousa.SHA256, error) {
	rows, err := conn.QueryContext(ctx, logRecordSQL+` WHERE l.kind = 'path_move' AND l.seq > ? ORDER BY l.seq`, after)
	if err != nil {
		return mousa.PathMove{}, mousa.SHA256{}, classify("read path move evidence", err)
	}
	defer rows.Close()
	var found *mousa.PathMove
	var foundID mousa.SHA256
	for rows.Next() {
		var seq int64
		var kind string
		var recordID, leafSource, leaf, record, recordSource []byte
		if err := rows.Scan(&seq, &kind, &recordID, &leafSource, &leaf, &record, &recordSource); err != nil {
			return mousa.PathMove{}, mousa.SHA256{}, classify("read path move evidence", err)
		}
		move, err := decodePathMoveLeaf(uint64(seq), kind, recordID, leafSource, leaf, record, recordSource)
		if err != nil {
			return mousa.PathMove{}, mousa.SHA256{}, err
		}
		if move.SourceID != source {
			continue
		}
		covers := false
		for _, entry := range move.Records {
			if entry.ItemID == itemID {
				covers = true
			}
		}
		if !covers {
			continue
		}
		if found != nil {
			return mousa.PathMove{}, mousa.SHA256{}, integrity("read path move evidence", "document operation wrote more than one move for its item")
		}
		found = &move
		copy(foundID[:], recordID)
	}
	if err := rows.Err(); err != nil {
		return mousa.PathMove{}, mousa.SHA256{}, classify("read path move evidence", err)
	}
	if found == nil {
		return mousa.PathMove{}, mousa.SHA256{}, integrity("read path move evidence", "document operation produced no path move")
	}
	return *found, foundID, nil
}

// SetDocumentPath moves one existing item to an explicit path inside the
// caller's write transaction, logging the path_move leaf. The representation
// and index are untouched; a repeat of the current path is a no-op.
func (store *Store) SetDocumentPath(ctx context.Context, sourceID mousa.SourceID, itemID, newPath, actor string) (bool, error) {
	if !mousa.ValidPath(newPath) {
		return false, wrap(CodeInvalidRecord, "set item path", errors.New("destination path is not a valid path"))
	}
	moved := false
	err := store.writeImmediate(ctx, "set item path", func(conn *sql.Conn) error {
		old, err := localItemPath(ctx, conn, sourceID, itemID)
		if err != nil {
			return err
		}
		if old == newPath {
			return nil
		}
		if _, err := appendPathMove(ctx, conn, sourceID,
			[]mousa.PathMoveEntry{{ItemID: itemID, OldPath: old, NewPath: newPath}}, moveActor(actor)); err != nil {
			return err
		}
		moved = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return moved, nil
}

// moveActor is the actor recorded on a move issued by a direct store caller.
func moveActor(actor string) string {
	if actor == "" {
		return "mousa-local.cli"
	}
	return actor
}

// Move relocates one note, one document item, or every item under a path
// prefix, logging exactly one path_move leaf per operation. An unchanged
// single destination or an empty prefix result is a no-op with no leaf; a
// served request still records its admission and outcome.
func (store *Store) Move(ctx context.Context, sourceID mousa.SourceID, selector mousa.MoveSelector, actor string, admission *mousa.RequestAdmission) ([]mousa.PathMoveEntry, error) {
	if err := store.requireWritable("move"); err != nil {
		return nil, err
	}
	selected := 0
	for _, set := range []bool{selector.NoteID != (mousa.SHA256{}), selector.ItemID != "", selector.Prefix != ""} {
		if set {
			selected++
		}
	}
	if selected != 1 || selector.To == "" {
		return nil, wrap(CodeInvalidRecord, "move", errors.New("choose exactly one of note, item or prefix, and a destination"))
	}
	if selector.Prefix != "" {
		old, ok := strings.CutSuffix(selector.Prefix, "/")
		to, okTo := strings.CutSuffix(selector.To, "/")
		if !ok || !okTo || old == "" || to == "" || !mousa.ValidPath(old) || !mousa.ValidPath(to) {
			return nil, wrap(CodeInvalidRecord, "move", errors.New("prefix moves need a valid path ending in / on both sides"))
		}
	} else if !mousa.ValidPath(selector.To) {
		return nil, wrap(CodeInvalidRecord, "move", errors.New("destination path is not a valid path"))
	}
	var entries []mousa.PathMoveEntry
	err := store.writeImmediate(ctx, "move", func(conn *sql.Conn) error {
		// The destination source must allow access and stay live; the check
		// reruns inside the write transaction on every call.
		finishAccess := store.StartStage(mousa.StageAccess)
		accessErr := requireAllowedSource(ctx, conn, sourceID)
		finishAccess(accessErr)
		if accessErr != nil {
			return accessErr
		}
		var err error
		entries, err = moveEntries(ctx, conn, sourceID, selector)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			if admission != nil {
				return finishRequest(ctx, conn, *admission, mousa.RequestStateNoOp, "", mousa.RequestEvidence{})
			}
			return nil
		}
		moveID, err := appendPathMove(ctx, conn, sourceID, entries, moveActor(actor))
		if err != nil {
			return err
		}
		if admission != nil {
			return finishRequest(ctx, conn, *admission, mousa.RequestStateCommitted, "", mousa.RequestEvidence{PathMoveID: &moveID})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

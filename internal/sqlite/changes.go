package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
)

const changePageSize = 256

type Change struct {
	Seq      uint64                  `json:"seq"`
	Kind     string                  `json:"kind"`
	SourceID mousa.SourceID          `json:"source_id"`
	RecordID mousa.SHA256            `json:"record_id"`
	ItemID   string                  `json:"item_id,omitempty"`
	Previous *mousa.RepresentationID `json:"previous,omitempty"`
	Current  *mousa.RepresentationID `json:"current,omitempty"`
	Access   mousa.SourceAccess      `json:"access,omitempty"`
}

type ChangePage struct {
	Changes   []Change `json:"changes"`
	NextIndex uint64   `json:"next_index"`
	More      bool     `json:"more"`
}

// Changes reads one page from a snapshot. A withdrawn but access-allowed
// source contributes withdrawal notices only, never its item history.
func (store *Store) Changes(ctx context.Context, since uint64, scope mousa.Scope, grants map[mousa.SourceID]bool) (ChangePage, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ChangePage{}, classify("begin changes", err)
	}
	defer tx.Rollback()
	page := ChangePage{Changes: []Change{}}
	if err := tx.QueryRowContext(ctx, logIndexSQL).Scan(&page.NextIndex); err != nil {
		return ChangePage{}, classify("read changes index", err)
	}
	if since > page.NextIndex {
		return ChangePage{}, wrap(CodeInvalidQuery, "changes", errors.New("since exceeds the store's log index"))
	}
	sources, err := changeScope(ctx, tx, scope, grants)
	if err != nil {
		return ChangePage{}, err
	}
	var allowed, withdrawn []mousa.SourceID
	for _, id := range sources {
		decision, err := currentSourceDecision(ctx, tx, id)
		if err != nil {
			return ChangePage{}, err
		}
		switch {
		case decision.Outcome == "allow":
			allowed = append(allowed, id)
		case decision.Access == mousa.SourceAccessAllow && decision.WithdrawalID != nil:
			withdrawn = append(withdrawn, id)
		}
	}
	if len(allowed)+len(withdrawn) > 30_000 {
		return ChangePage{}, wrap(CodeResourceLimit, "changes", errors.New("too many sources; select a collection"))
	}
	if len(allowed)+len(withdrawn) != 0 && since < page.NextIndex {
		if err := readChangePage(ctx, tx, since, allowed, withdrawn, &page); err != nil {
			return ChangePage{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ChangePage{}, classify("finish changes", err)
	}
	return page, nil
}

func changeScope(ctx context.Context, q queryer, scope mousa.Scope, grants map[mousa.SourceID]bool) ([]mousa.SourceID, error) {
	hasSource := scope.Source != (mousa.SourceID{})
	if (hasSource && scope.Collection != "") || scope.Sources != nil {
		return nil, wrap(CodeInvalidQuery, "changes", errors.New("choose one source or one collection"))
	}
	var sources []mousa.SourceID
	switch {
	case scope.Collection != "":
		var err error
		sources, err = collectionSources(ctx, q, scope.Collection)
		if err != nil {
			return nil, err
		}
	case hasSource:
		sources = []mousa.SourceID{scope.Source}
	case grants != nil:
		sources = slices.Collect(maps.Keys(grants))
		slices.SortFunc(sources, compareSourceIDs)
		return slices.DeleteFunc(sources, func(id mousa.SourceID) bool { return !grants[id] }), nil
	default:
		rows, err := q.QueryContext(ctx, `SELECT id FROM sources ORDER BY id`)
		if err != nil {
			return nil, classify("list change sources", err)
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			if err := rows.Scan(&raw); err != nil {
				return nil, classify("read change source", err)
			}
			if len(raw) != len(mousa.SourceID{}) {
				return nil, integrity("read change source", "invalid source ID")
			}
			sources = append(sources, mousa.SourceID(raw))
		}
		return sources, classify("finish listing change sources", rows.Err())
	}
	return sources, requireSourceGrants(sources, grants)
}

func readChangePage(ctx context.Context, q queryer, since uint64, allowed, withdrawn []mousa.SourceID, page *ChangePage) error {
	args := []any{since, page.NextIndex}
	var filters []string
	add := func(sources []mousa.SourceID, kinds string) {
		if len(sources) != 0 {
			filters = append(filters, `(l.kind IN (`+kinds+`) AND l.source_id IN (`+strings.Repeat("?,", len(sources)-1)+`?))`)
			for _, id := range sources {
				args = append(args, id[:])
			}
		}
	}
	add(allowed, `'item_change', 'source_withdrawal', 'access_event'`)
	add(withdrawn, `'source_withdrawal'`)
	args = append(args, changePageSize+1)
	rows, err := q.QueryContext(ctx, logRecordSQL+` WHERE l.seq > ? AND l.seq <= ? AND (`+
		strings.Join(filters, ` OR `)+`) ORDER BY l.seq LIMIT ?`, args...)
	if err != nil {
		return classify("read changes", err)
	}
	defer rows.Close()
	for rows.Next() {
		if len(page.Changes) == changePageSize {
			page.More = true
			page.NextIndex = page.Changes[changePageSize-1].Seq
			break
		}
		var entry Change
		var kind string
		var id, source, hash, data, recordSource []byte
		if err := rows.Scan(&entry.Seq, &kind, &id, &source, &hash, &data, &recordSource); err != nil {
			return classify("read log change", err)
		}
		if len(id) != len(entry.RecordID) || len(source) != len(entry.SourceID) {
			return integrity("read log change", "invalid source or record ID")
		}
		entry.RecordID, entry.SourceID = mousa.SHA256(id), mousa.SourceID(source)
		if err := checkLeaf(kind, source, hash, data, recordSource); err != nil {
			return wrap(CodeIntegrity, "read log change", err)
		}
		switch kind {
		case mousa.LogKindItemChange:
			change, err := readItemChange(entry.Seq, id, source, data)
			if err != nil {
				return err
			}
			entry.ItemID, entry.Previous, entry.Current = change.ItemID, change.Previous, change.Current
			switch {
			case entry.Current == nil:
				entry.Kind = "retired"
			case entry.Previous == nil:
				entry.Kind = "added"
			default:
				entry.Kind = "updated"
			}
		case mousa.LogKindSourceWithdrawal:
			record, err := getSourceWithdrawal(ctx, q, mousa.WithdrawalID(id))
			if err != nil {
				return lexicalStructuralError("read change withdrawal", err)
			}
			if record.SourceID != entry.SourceID {
				return integrity("read change withdrawal", "withdrawal belongs to another source")
			}
			entry.Kind = "withdrawn"
		case mousa.LogKindAccessEvent:
			event, verified, err := getAccessEvent(ctx, q, mousa.AccessEventID(id))
			if err != nil {
				return lexicalStructuralError("read change access event", err)
			}
			if event.SourceID != entry.SourceID || !bytes.Equal(data, verified) {
				return integrity("read change access", "access event disagrees with log record")
			}
			entry.Kind, entry.Access = "access_changed", event.Access
		default:
			return integrity("read log change", "unexpected change kind")
		}
		page.Changes = append(page.Changes, entry)
	}
	return classify("finish reading changes", rows.Err())
}

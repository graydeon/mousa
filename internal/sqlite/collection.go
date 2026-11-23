package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/graydeon/mousa/internal/mousa"
)

func (store *Store) CreateCollection(ctx context.Context, name string) error {
	if !mousa.ValidCollectionName(name) {
		return wrap(CodeInvalidRecord, "create collection", errors.New("invalid collection name"))
	}
	return store.writeImmediate(ctx, "create collection", func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, `INSERT INTO collections(name) VALUES(?) ON CONFLICT DO NOTHING`, name)
		return classify("create collection", err)
	})
}

func (store *Store) SetCollectionSource(ctx context.Context, name string, id mousa.SourceID, add bool) error {
	return store.writeImmediate(ctx, "change collection", func(conn *sql.Conn) error {
		sources, err := collectionSources(ctx, conn, name)
		if err != nil {
			return err
		}
		if !add {
			_, err := conn.ExecContext(ctx, `DELETE FROM collection_sources WHERE collection = ? AND source_id = ?`, name, id[:])
			return classify("remove collection source", err)
		}
		if slices.Contains(sources, id) {
			return nil
		}
		if len(sources) == mousa.MaxScopeSources {
			return wrap(CodeResourceLimit, "add collection source", errors.New("collection source limit reached"))
		}
		if _, err := getSource(ctx, conn, id); err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `INSERT INTO collection_sources(collection, source_id) VALUES(?, ?)`, name, id[:])
		return classify("add collection source", err)
	})
}

func (store *Store) Collections(ctx context.Context) ([]mousa.Collection, error) {
	return readCollections(ctx, store.db, "")
}

func collectionSources(ctx context.Context, q queryer, name string) ([]mousa.SourceID, error) {
	if !mousa.ValidCollectionName(name) {
		return nil, wrap(CodeInvalidRecord, "resolve collection", errors.New("invalid collection name"))
	}
	sets, err := readCollections(ctx, q, name)
	if err != nil {
		return nil, err
	}
	if len(sets) != 1 {
		return nil, readError("resolve collection", sql.ErrNoRows)
	}
	return sets[0].Sources, nil
}

func readCollections(ctx context.Context, q queryer, name string) ([]mousa.Collection, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT c.name, m.source_id
		FROM collections c LEFT JOIN collection_sources m ON m.collection = c.name
		WHERE ?1 = '' OR c.name = ?1 ORDER BY c.name, m.source_id`, name)
	if err != nil {
		return nil, classify("read collections", err)
	}
	defer rows.Close()
	sets := []mousa.Collection{}
	for rows.Next() {
		var setName string
		var source []byte
		if err := rows.Scan(&setName, &source); err != nil {
			return nil, classify("read collection row", err)
		}
		if !mousa.ValidCollectionName(setName) {
			return nil, integrity("read collection row", "invalid collection name")
		}
		if len(sets) == 0 || sets[len(sets)-1].Name != setName {
			sets = append(sets, mousa.Collection{Name: setName, Sources: []mousa.SourceID{}})
		}
		if source == nil {
			continue
		}
		set := &sets[len(sets)-1]
		if len(source) != len(mousa.SourceID{}) || len(set.Sources) >= mousa.MaxScopeSources {
			return nil, integrity("read collection row", "invalid source ID length or source count")
		}
		id := mousa.SourceID(source)
		if id == (mousa.SourceID{}) {
			return nil, integrity("read collection row", "zero source ID")
		}
		set.Sources = append(set.Sources, id)
	}
	return sets, classify("finish reading collections", rows.Err())
}

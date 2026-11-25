package main

import (
	"context"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// migrateCommand upgrades a schema-7 store to the current schema in place.
// It takes no arguments and reports the resulting schema version.
func migrateCommand(ctx context.Context, storePath string) error {
	if err := sqlite.Migrate(ctx, storePath); err != nil {
		return err
	}
	return emit(struct {
		Schema int `json:"schema"`
	}{Schema: 9})
}

// moveCommand relocates one note, one document item, or every item under a
// path prefix inside one source, logging the move. Offline the source may be
// a directory argument; a served move needs --source like query.
func moveCommand(ctx context.Context, storePath string, client *nativeClient, args []string) error {
	flags := newCommandFlags("move")
	sourceID := flags.String("source", "", "external source ID")
	note := flags.String("note", "", "note ID to move")
	item := flags.String("item", "", "document item ID to move")
	prefix := flags.String("prefix", "", "path prefix ending in / to rewrite")
	to := flags.String("to", "", "destination path, or destination prefix ending in /")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	selector := mousa.MoveSelector{ItemID: *item, Prefix: *prefix, To: *to}
	if *note != "" {
		id, err := mousa.ParseSHA256(*note)
		if err != nil {
			return usageError{"--note must be a lowercase SHA-256 note ID"}
		}
		selector.NoteID = id
	}
	if client != nil {
		if *sourceID == "" || len(flags.Args()) != 0 {
			return usageError{"endpoint move requires --source <external-id>; directory moves stay offline"}
		}
		result, err := client.post(ctx, "/v1/moves", nativeMove{
			nativeLabels: nativeLabels{Client: client.metadata},
			Source:       *sourceID, Note: *note, Item: *item, Prefix: *prefix, To: *to,
		})
		if err != nil {
			return err
		}
		return emitRawResult(result)
	}
	source, _, err := selectSource(*sourceID, flags.Args(), "move")
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	entries, err := store.Move(ctx, source.ID, selector, accessActor, nil)
	if err != nil {
		return err
	}
	return emit(struct {
		Source string                `json:"source"`
		Moved  []mousa.PathMoveEntry `json:"moved"`
	}{Source: source.ExternalSourceID, Moved: entries})
}

package main

import (
	"context"
	"encoding/json"
	"os"
	"strconv"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// trailCommand reads one recorded query trail as the store owner. It releases
// trail metadata only, never passage text.
func trailCommand(ctx context.Context, storePath string, args []string) error {
	if len(args) != 0 && args[0] == "list" {
		return trailListCommand(ctx, storePath, args[1:])
	}
	if len(args) != 1 {
		return usageError{"trail requires a trail ID"}
	}
	id, err := mousa.ParseSourceTrailID(args[0])
	if err != nil {
		return usageError{err.Error()}
	}
	store, err := sqlite.OpenReadOnly(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	trail, err := store.GetTrail(ctx, id, nil)
	if err != nil {
		return err
	}
	return emit(trail)
}

func trailListCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("trail list")
	var filter sqlite.TrailListFilter
	flags.Func("source", "external source ID named in the trail", func(value string) error {
		source, err := streamSource(value)
		filter.Source = source.ID
		return err
	})
	flags.Func("segment", "released segment ID", func(value string) error {
		id, err := mousa.ParseSegmentID(value)
		if err == nil && id == (mousa.SegmentID{}) {
			return usageError{"segment ID must be nonzero"}
		}
		filter.Segment = id
		return err
	})
	sinceArg := flags.String("since", "0", "exclude log entries at or before this index")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	since, err := strconv.ParseUint(*sinceArg, 10, 53)
	if err != nil || flags.NArg() != 0 {
		return usageError{"trail list accepts --source ID, --segment ID and --since SEQ"}
	}
	filter.Since = since
	store, err := sqlite.OpenReadOnly(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	encoder := json.NewEncoder(os.Stdout)
	return store.ListTrails(ctx, filter, func(entry sqlite.TrailListEntry) error {
		return encoder.Encode(entry)
	})
}

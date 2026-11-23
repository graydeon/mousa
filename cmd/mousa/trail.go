package main

import (
	"context"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// trailCommand reads one recorded query trail as the store owner. It releases
// trail metadata only, never passage text.
func trailCommand(ctx context.Context, storePath string, args []string) error {
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

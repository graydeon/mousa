package main

import (
	"context"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

func trailCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("trail")
	sourceID := flags.String("source", "", "external source ID instead of a directory root")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	positional := flags.Args()
	if len(positional) == 0 {
		return usageError{"trail requires a source and trail ID"}
	}
	source, label, err := selectSource(*sourceID, positional[:len(positional)-1], "trail")
	if err != nil {
		return err
	}
	id, err := mousa.ParseSourceTrailID(positional[len(positional)-1])
	if err != nil {
		return usageError{err.Error()}
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	request, err := newRetrievalRequest(source.ID)
	if err != nil {
		return err
	}
	inspection, err := store.InspectSourceTrail(ctx, request, id)
	if err != nil {
		return err
	}
	return emit(struct {
		Source  string `json:"source"`
		TrailID string `json:"trail_id"`
		sqlite.TrailInspection
	}{Source: label, TrailID: id.String(), TrailInspection: inspection})
}

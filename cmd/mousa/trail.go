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
	if inspection.Historical != nil && inspection.Historical.Vector != nil {
		// Keep the lexical/MCP projection unchanged. Native v5 history has
		// vector facts instead of an empty lexical expression.
		historical := struct {
			*sqlite.SourceTrailView
			Expression string                 `json:"expression,omitempty"`
			Vector     *mousa.VectorSelection `json:"vector"`
		}{SourceTrailView: inspection.Historical, Vector: inspection.Historical.Vector}
		return emit(struct {
			Source  string `json:"source"`
			TrailID string `json:"trail_id"`
			sqlite.TrailInspection
			Historical any `json:"historical"`
		}{Source: label, TrailID: id.String(), TrailInspection: inspection, Historical: historical})
	}
	return emit(struct {
		Source  string `json:"source"`
		TrailID string `json:"trail_id"`
		sqlite.TrailInspection
	}{Source: label, TrailID: id.String(), TrailInspection: inspection})
}

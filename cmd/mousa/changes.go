package main

import (
	"context"
	"strconv"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

func changesCommand(ctx context.Context, path string, args []string) error {
	flags := newCommandFlags("changes")
	sinceArg := flags.String("since", "", "last log index already seen (required)")
	source := flags.String("source", "", "external source ID")
	collection := flags.String("collection", "", "named collection")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	since, err := strconv.ParseUint(*sinceArg, 10, 53)
	if err != nil || flags.NArg() != 0 || (*source != "" && *collection != "") {
		return usageError{"changes requires --since SEQ and optional --source ID or --collection NAME"}
	}
	scope := mousa.Scope{Collection: *collection}
	if *source != "" {
		record, err := streamSource(*source)
		if err != nil {
			return err
		}
		scope.Source = record.ID
	}
	store, err := sqlite.OpenReadOnly(ctx, path)
	if err != nil {
		return err
	}
	defer store.Close()
	page, err := store.Changes(ctx, since, scope, nil)
	if err != nil {
		return err
	}
	return emit(page)
}

func recheckCommand(ctx context.Context, path string, args []string) error {
	flags := newCommandFlags("recheck")
	trail := flags.Bool("trail", false, "recheck a trail rather than a note")
	source := flags.String("source", "", "external source ID of a note")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if flags.NArg() != 1 || (*trail == (*source != "")) {
		return usageError{"recheck requires --trail TRAIL_ID or --source SOURCE_ID NOTE_ID"}
	}
	id, err := mousa.ParseSHA256(flags.Arg(0))
	if err != nil || id == (mousa.SHA256{}) {
		return usageError{"recheck requires a nonzero lowercase SHA-256 ID"}
	}
	store, err := sqlite.OpenReadOnly(ctx, path)
	if err != nil {
		return err
	}
	defer store.Close()
	var result sqlite.RecheckResult
	if *trail {
		result, err = store.RecheckTrail(ctx, mousa.SourceTrailID(id))
	} else {
		record, sourceErr := streamSource(*source)
		if sourceErr != nil {
			return sourceErr
		}
		result, err = store.RecheckNote(ctx, record.ID, id)
	}
	if err != nil {
		return err
	}
	return emit(result)
}

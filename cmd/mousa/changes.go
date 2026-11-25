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

func recheckCommand(ctx context.Context, path string, client *nativeClient, args []string) error {
	// The flag package stops at the first positional argument; accept the
	// spelled-out `--trail ID --text` form by hoisting a trailing literal.
	if n := len(args); n >= 2 && args[n-1] == "--text" {
		args = append([]string{"--text"}, args[:n-1]...)
	}
	flags := newCommandFlags("recheck")
	trail := flags.Bool("trail", false, "recheck a trail rather than a note")
	text := flags.Bool("text", false, "release each packed segment's text when available; requires --trail")
	source := flags.String("source", "", "external source ID of a note")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if flags.NArg() != 1 || (*trail == (*source != "")) || (*text && !*trail) {
		return usageError{"recheck requires --trail TRAIL_ID [--text] or --source SOURCE_ID NOTE_ID"}
	}
	id, err := mousa.ParseSHA256(flags.Arg(0))
	if err != nil || id == (mousa.SHA256{}) {
		return usageError{"recheck requires a nonzero lowercase SHA-256 ID"}
	}
	if client != nil {
		if !*trail || !*text {
			return usageError{"a served store supports only recheck --trail TRAIL_ID --text"}
		}
		result, err := client.get(ctx, "/v1/recheck/"+id.String())
		if err != nil {
			return err
		}
		return emitRawResult(result)
	}
	store, err := sqlite.OpenReadOnly(ctx, path)
	if err != nil {
		return err
	}
	defer store.Close()
	var result sqlite.RecheckResult
	switch {
	case *trail && *text:
		result, err = store.RecheckTrailText(ctx, mousa.SourceTrailID(id), nil)
	case *trail:
		result, err = store.RecheckTrail(ctx, mousa.SourceTrailID(id))
	default:
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

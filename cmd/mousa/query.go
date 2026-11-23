package main

import (
	"context"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// queryCommand names its source with a directory argument or --source, and
// always takes the query text as the remaining argument.
func queryCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("query")
	sourceID := flags.String("source", "", "external source ID instead of a directory root")
	budget := flags.Uint64("budget-bytes", 8<<10, "positive released-text byte budget")
	mode := flags.String("mode", string(mousa.QueryLexical), "query mode: lexical")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if *mode != string(mousa.QueryLexical) {
		return usageError{"query mode must be lexical"}
	}
	if *budget == 0 {
		return usageError{"query byte budget must be positive"}
	}
	positional := flags.Args()
	if len(positional) == 0 {
		return usageError{"query requires a source and query text"}
	}
	source, _, err := selectSource(*sourceID, positional[:len(positional)-1], "query")
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.Query(ctx, mousa.QueryRequest{
		Scope:  mousa.Scope{Source: source.ID},
		Texts:  []string{positional[len(positional)-1]},
		Mode:   mousa.QueryMode(*mode),
		Budget: mousa.Budget{Unit: mousa.BudgetBytes, Limit: *budget},
		Caller: accessActor,
	})
	if err != nil {
		return err
	}
	return emit(result)
}

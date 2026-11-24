package main

import (
	"context"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// queryCommand names its source with a directory argument or --source, and
// always takes the query text as the remaining argument. With an endpoint
// client the scope stays external and travels in the request body.
func queryCommand(ctx context.Context, storePath string, client *nativeClient, args []string) error {
	flags := newCommandFlags("query")
	var externalSources []string
	flags.Func("source", "external source ID (repeatable)", func(value string) error {
		if _, err := streamSource(value); err != nil {
			return err
		}
		externalSources = append(externalSources, value)
		return nil
	})
	collection := flags.String("collection", "", "named collection instead of explicit sources")
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
	if client != nil {
		return runEndpointQuery(ctx, client, externalSources, *collection, *budget, positional)
	}
	var sources []mousa.SourceID
	for _, external := range externalSources {
		source, err := streamSource(external)
		if err != nil {
			return err
		}
		sources = append(sources, source.ID)
	}
	scope := mousa.Scope{Collection: *collection}
	switch {
	case *collection != "" || len(sources) != 0:
		if len(positional) != 1 || (*collection != "" && len(sources) != 0) {
			return usageError{"choose a directory, --source IDs, or --collection"}
		}
		if len(sources) == 1 {
			scope.Source = sources[0]
		} else {
			scope.Sources = sources
		}
	default:
		source, _, err := selectSource("", positional[:len(positional)-1], "query")
		if err != nil {
			return err
		}
		scope.Source = source.ID
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := store.Query(ctx, mousa.QueryRequest{
		Scope:  scope,
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

// runEndpointQuery sends the query to the served store. Directory scopes have
// no external identifier and stay offline.
func runEndpointQuery(ctx context.Context, client *nativeClient, externalSources []string, collection string, budget uint64, positional []string) error {
	scope := nativeScope{Collection: collection}
	switch {
	case collection != "" || len(externalSources) != 0:
		if len(positional) != 1 || (collection != "" && len(externalSources) != 0) {
			return usageError{"choose --source IDs or --collection"}
		}
		if len(externalSources) == 1 {
			scope.Source = externalSources[0]
		} else {
			scope.Sources = externalSources
		}
	default:
		return usageError{"endpoint query requires --source or --collection; directory scopes stay offline"}
	}
	result, err := client.post(ctx, "/v1/query", nativeQuery{
		nativeLabels: nativeLabels{Client: client.metadata},
		Scope:        scope, Text: positional[len(positional)-1], BudgetBytes: budget,
	})
	if err != nil {
		return err
	}
	return emitRawResult(result)
}

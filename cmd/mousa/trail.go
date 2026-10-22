package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
	"github.com/graydeon/mousa/internal/tokenizer"
)

func trailCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("trail")
	sourceID := flags.String("source", "", "external source ID instead of a directory root")
	verifyTokens := flags.Bool("verify-tokens", false, "recount a token-aware trail when all selected bytes remain")
	python := flags.String("tokenizer-python", "python3", "local interpreter with tiktoken==0.12.0")
	cache := flags.String("tokenizer-cache", "", "prepared offline o200k_base cache")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	specific := false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "tokenizer-python" || option.Name == "tokenizer-cache" {
			specific = true
		}
	})
	if specific && !*verifyTokens {
		return usageError{"tokenizer options require --verify-tokens"}
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
	var counter *tokenizer.Counter
	if *verifyTokens {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		counter, err = startTokenCounter(ctx, *python, *cache)
		if err != nil {
			return err
		}
		defer counter.Close()
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
	var inspection sqlite.TrailInspection
	if *verifyTokens {
		inspection, err = store.InspectSourceTrailWithTokens(ctx, request, id, counter)
	} else {
		inspection, err = store.InspectSourceTrail(ctx, request, id)
	}
	if err != nil {
		return err
	}
	if inspection.Historical != nil && inspection.Historical.Token != nil {
		historical := struct {
			*sqlite.SourceTrailView
			Token             *mousa.TokenSelection  `json:"token"`
			Vector            *mousa.VectorSelection `json:"vector,omitempty"`
			Hybrid            *mousa.HybridSelection `json:"hybrid,omitempty"`
			TokenVerification string                 `json:"token_verification"`
		}{inspection.Historical, inspection.Historical.Token, inspection.Historical.Vector, inspection.Historical.Hybrid, inspection.Historical.TokenVerification}
		return emit(struct {
			Source  string `json:"source"`
			TrailID string `json:"trail_id"`
			sqlite.TrailInspection
			Historical any `json:"historical"`
		}{label, id.String(), inspection, historical})
	}
	if inspection.Historical != nil && inspection.Historical.Hybrid != nil {
		historical := struct {
			*sqlite.SourceTrailView
			Hybrid *mousa.HybridSelection `json:"hybrid"`
		}{SourceTrailView: inspection.Historical, Hybrid: inspection.Historical.Hybrid}
		return emit(struct {
			Source  string `json:"source"`
			TrailID string `json:"trail_id"`
			sqlite.TrailInspection
			Historical any `json:"historical"`
		}{Source: label, TrailID: id.String(), TrailInspection: inspection, Historical: historical})
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

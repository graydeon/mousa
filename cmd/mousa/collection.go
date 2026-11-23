package main

import (
	"context"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

func collectionCommand(ctx context.Context, path string, args []string) error {
	if len(args) == 1 && args[0] == "list" {
		store, err := sqlite.OpenReadOnly(ctx, path)
		if err != nil {
			return err
		}
		defer store.Close()
		sets, err := store.Collections(ctx)
		if err != nil {
			return err
		}
		return emit(struct {
			Collections []mousa.Collection `json:"collections"`
		}{Collections: sets})
	}
	if len(args) < 2 || (args[0] != "create" && args[0] != "add" && args[0] != "remove") {
		return usageError{"collection requires create NAME, list, or add|remove --source ID NAME"}
	}
	op := args[0]
	flags := newCommandFlags("collection " + op)
	var external string
	if op != "create" {
		flags.StringVar(&external, "source", "", "external source ID instead of a directory argument")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return usageError{err.Error()}
	}
	if flags.NArg() == 0 || !mousa.ValidCollectionName(flags.Arg(0)) {
		return usageError{"collection name must be 1-64 lowercase letters, digits, dots, hyphens or underscores, starting with a letter or digit"}
	}
	name := flags.Arg(0)
	var source mousa.Source
	if op == "create" {
		if flags.NArg() != 1 {
			return usageError{"collection create takes one name"}
		}
	} else {
		var err error
		source, _, err = selectSource(external, flags.Args()[1:], "collection "+op)
		if err != nil {
			return err
		}
	}
	store, err := sqlite.Open(ctx, path)
	if err != nil {
		return err
	}
	defer store.Close()
	if op == "create" {
		err = store.CreateCollection(ctx, name)
	} else {
		err = store.SetCollectionSource(ctx, name, source.ID, op == "add")
	}
	if err != nil {
		return err
	}
	return emit(struct {
		Name string `json:"name"`
	}{Name: name})
}

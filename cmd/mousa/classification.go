package main

import (
	"context"
	"os"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// classificationCommand is trusted local store-owner administration, not a
// retrieval access grant or an authenticated assertion of the label's truth.
func classificationCommand(ctx context.Context, storePath string, args []string) error {
	if len(args) == 0 || (args[0] != "put" && args[0] != "get") {
		return usageError{"classification requires put or get"}
	}
	operation := args[0]
	flags := newCommandFlags("classification " + operation)
	if err := flags.Parse(args[1:]); err != nil {
		return usageError{err.Error()}
	}
	if operation == "get" {
		if len(flags.Args()) != 1 {
			return usageError{"classification get takes one classification ID"}
		}
		id, err := mousa.ParseClassificationID(flags.Args()[0])
		if err != nil {
			return usageError{err.Error()}
		}
		store, err := sqlite.OpenReadOnly(ctx, storePath)
		if err != nil {
			return err
		}
		defer store.Close()
		record, err := store.GetClassification(ctx, id)
		if err != nil {
			return err
		}
		return emitClassification(record)
	}
	if len(flags.Args()) != 0 {
		return usageError{"classification put takes no arguments; it reads one draft object from stdin"}
	}
	data, err := readBoundedInput(os.Stdin, mousa.MaxClassificationInputBytes, "classification")
	if err != nil {
		return err
	}
	record, err := mousa.DecodeClassificationInput(data)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.PutClassification(ctx, record); err != nil {
		return err
	}
	record, err = store.GetClassification(ctx, record.ID)
	if err != nil {
		return err
	}
	return emitClassification(record)
}

func emitClassification(record mousa.Classification) error {
	data, err := mousa.EncodeClassification(record)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

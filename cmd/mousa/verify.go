package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

func verifyCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("verify")
	logOnly := flags.Bool("log", false, "check only the append-only log")
	fromStdin := flags.Bool("stdin", false, "read trail IDs, one per line")
	var ids []mousa.SourceTrailID
	addID := func(text string) error {
		id, err := mousa.ParseSourceTrailID(text)
		if err != nil {
			return fmt.Errorf("invalid trail ID: %w", err)
		}
		ids = append(ids, id)
		return nil
	}
	flags.Func("trail", "verify a trail ID; repeatable", addID)
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if flags.NArg() != 0 || (*logOnly && (*fromStdin || len(ids) != 0)) || (*fromStdin && len(ids) != 0) {
		return usageError{"verify accepts one of --log, --trail ID (repeatable), or --stdin"}
	}
	if *fromStdin {
		input, err := readBoundedInput(os.Stdin, 1<<20, "verify trail IDs")
		if err != nil {
			return err
		}
		for value := range strings.FieldsSeq(string(input)) {
			if err := addID(value); err != nil {
				return err
			}
		}
		if len(ids) == 0 {
			return usageError{"verify --stdin requires at least one trail ID"}
		}
	}
	store, err := sqlite.OpenReadOnly(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	result := struct {
		Scope string `json:"scope"`
		sqlite.Verification
	}{}
	switch {
	case *logOnly:
		result.Scope = "log"
		result.RecordCounts, err = store.VerifyLog(ctx)
	case len(ids) != 0:
		result.Scope = "trails"
		result.Verification, err = store.VerifyTrails(ctx, ids)
	default:
		result.Scope = "store"
		result.Verification, err = store.Verify(ctx)
	}
	if err != nil {
		return err
	}
	return emit(result)
}

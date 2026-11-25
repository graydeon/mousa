package main

import (
	"context"
	"crypto/rand"
	"flag"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

func accessCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("access")
	sourceID := flags.String("source", "", "external source ID instead of a directory root")
	create := flags.Bool("create", false, "register the explicit JSONL source first; requires --source and allow")
	history := flags.String("history", "", "retain retired segment text: on or off")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	var historySet bool
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "history" {
			historySet = true
		}
	})
	if historySet {
		return historyCommand(ctx, storePath, *sourceID, *history, flags.Args(), *create)
	}
	positional := flags.Args()
	if len(positional) == 0 {
		return usageError{"access requires a source and allow or deny"}
	}
	access := mousa.SourceAccess(positional[len(positional)-1])
	if access.Validate() != nil {
		return usageError{"access effect must be allow or deny"}
	}
	if *create && (*sourceID == "" || access != mousa.SourceAccessAllow) {
		return usageError{"access --create requires --source <external-id> and allow"}
	}
	source, label, err := selectSource(*sourceID, positional[:len(positional)-1], "access")
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if _, err := store.GetSource(ctx, source.ID); err != nil {
		if !*create || !sqlite.IsCode(err, sqlite.CodeNotFound) {
			return err
		}
		if err := store.PutSource(ctx, source); err != nil {
			return err
		}
	}
	event, changed, err := store.SetSourceAccess(ctx, source.ID, access, accessActor)
	if err != nil {
		return err
	}
	return emit(struct {
		Source        string              `json:"source"`
		Access        mousa.SourceAccess  `json:"access"`
		AccessEventID mousa.AccessEventID `json:"access_event_id"`
		Changed       bool                `json:"changed"`
	}{label, event.Access, event.ID, changed})
}

// historyCommand sets whether an existing source retains retiring segment
// text. It grants no access: --history never takes allow, deny, or --create.
func historyCommand(ctx context.Context, storePath, sourceID, setting string, positional []string, create bool) error {
	if sourceID == "" || len(positional) != 0 || create || (setting != "on" && setting != "off") {
		return usageError{"access requires exactly --source ID --history on|off"}
	}
	source, label, err := selectSource(sourceID, nil, "access")
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	id, changed, err := store.SetSourceHistory(ctx, source.ID, setting == "on", accessActor)
	if err != nil {
		return err
	}
	return emit(struct {
		Source         string       `json:"source"`
		History        string       `json:"history"`
		HistoryEventID mousa.SHA256 `json:"history_event_id"`
		Changed        bool         `json:"changed"`
	}{label, setting, id, changed})
}

// accessActor is the actor this CLI records on the access events it appends.
const accessActor = localNamespace + ".cli"

// allowAfterSync records allow for a source that sync has just written if it has no access
// event, and reports the current access either way; sync --allow uses it.
func allowAfterSync(ctx context.Context, store *sqlite.Store, sourceID mousa.SourceID, result *syncResult) error {
	event, _, err := store.AllowSourceIfUnset(ctx, sourceID, accessActor)
	if err != nil {
		return err
	}
	result.Access = string(event.Access)
	result.AccessEventID = event.ID.String()
	return nil
}

func withdrawCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("withdraw")
	sourceID := flags.String("source", "", "external source ID instead of a directory root")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	source, label, err := selectSource(*sourceID, flags.Args(), "withdraw")
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	state, err := store.GetIngestState(ctx, source.ID)
	if err != nil {
		return err
	}
	var id mousa.WithdrawalID
	changed := state.CollectionState != mousa.CollectionWithdrawn
	if changed {
		externalID := rand.Text()
		id, err = mousa.NewWithdrawalID(source.ID, externalID)
		if err != nil {
			return err
		}
		withdrawal := mousa.SourceWithdrawal{
			Schema: mousa.SourceWithdrawalSchema, ID: id, SourceID: source.ID,
			ExternalWithdrawalID: externalID, AdapterID: adapterID, AdapterVersion: adapterVersion,
			OccurredAtUsec: time.Now().UnixMicro(),
		}
		if err := store.WithdrawSource(ctx, withdrawal); err != nil {
			return err
		}
	} else {
		id = *state.CurrentWithdrawalID
	}
	return emit(struct {
		Source          string                `json:"source"`
		CollectionState mousa.CollectionState `json:"collection_state"`
		WithdrawalID    mousa.WithdrawalID    `json:"withdrawal_id"`
		Changed         bool                  `json:"changed"`
	}{Source: label, CollectionState: mousa.CollectionWithdrawn, WithdrawalID: id, Changed: changed})
}

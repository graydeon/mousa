package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

func putLocalPolicyDefinition(ctx context.Context, store *sqlite.Store, effect mousa.SourceRetrievalEffect) (mousa.PolicyDefinition, error) {
	inner := mousa.SourceRetrievalPolicy{
		Schema: mousa.SourceRetrievalPolicySchema, Action: mousa.SourceRetrievalAction,
		CallerNamespace: localNamespace + ".caller", ExternalCallerID: "cli",
		PurposeNamespace: localNamespace + ".purpose", ExternalPurposeID: "retrieval",
		Effect: effect,
	}
	data, err := mousa.EncodeSourceRetrievalPolicy(inner)
	if err != nil {
		return mousa.PolicyDefinition{}, err
	}
	digest := mousa.SHA256(sha256.Sum256(data))
	id, err := mousa.NewPolicyDefinitionID(localNamespace, string(effect), "1", mousa.SourceRetrievalPolicyMediaType, mousa.SourceRetrievalPolicySchema, digest)
	if err != nil {
		return mousa.PolicyDefinition{}, err
	}
	definition := mousa.PolicyDefinition{
		Schema: mousa.PolicyDefinitionSchema, ID: id, Namespace: localNamespace,
		ExternalPolicyID: string(effect), ExternalPolicyVersion: "1",
		DefinitionMediaType: mousa.SourceRetrievalPolicyMediaType, DefinitionSchema: mousa.SourceRetrievalPolicySchema,
		DefinitionSHA256: digest, Definition: string(data),
	}
	if err := store.PutPolicyDefinition(ctx, definition); err != nil {
		return mousa.PolicyDefinition{}, err
	}
	return definition, nil
}

type accessResult struct {
	Source       string                      `json:"source"`
	Effect       mousa.SourceRetrievalEffect `json:"effect"`
	BindingID    mousa.PolicyBindingID       `json:"binding_id"`
	ActivationID mousa.PolicyActivationID    `json:"activation_id"`
	Changed      bool                        `json:"changed"`
}

func accessCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("access")
	sourceID := flags.String("source", "", "external source ID instead of a directory root")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	positional := flags.Args()
	if len(positional) == 0 {
		return usageError{"access requires a source and allow or deny"}
	}
	effect := mousa.SourceRetrievalEffect(positional[len(positional)-1])
	if effect != mousa.SourceRetrievalEffectAllow && effect != mousa.SourceRetrievalEffectDeny {
		return usageError{"access effect must be allow or deny"}
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
		return err
	}
	definition, err := putLocalPolicyDefinition(ctx, store, effect)
	if err != nil {
		return err
	}
	series := "source/" + source.ID.String()
	scope := mousa.NewSourcePolicyScope(source.ID)
	bindingID, err := mousa.NewPolicyBindingID(localNamespace, series, string(effect), scope, definition.ID)
	if err != nil {
		return err
	}
	binding := mousa.PolicyBinding{
		Schema: mousa.PolicyBindingSchema, ID: bindingID, Namespace: localNamespace,
		ExternalBindingID: series, ExternalBindingVersion: string(effect),
		Scope: scope, PolicyDefinitionID: definition.ID,
	}
	if err := store.PutPolicyBinding(ctx, binding); err != nil {
		return err
	}
	result := accessResult{Source: label, Effect: effect, BindingID: bindingID}
	var previous *mousa.PolicyActivationID
	state, err := store.GetPolicyBindingState(ctx, localNamespace, series)
	if err == nil {
		if state.ActiveBindingID != nil && *state.ActiveBindingID == bindingID {
			result.ActivationID = state.CurrentActivationID
			return emit(result)
		}
		previous = &state.CurrentActivationID
	} else if !sqlite.IsCode(err, sqlite.CodeNotFound) {
		return err
	}
	externalID := rand.Text()
	activationID, err := mousa.NewPolicyActivationID(localNamespace, series, externalID)
	if err != nil {
		return err
	}
	activation := mousa.PolicyActivation{
		Schema: mousa.PolicyActivationSchema, ID: activationID, Namespace: localNamespace,
		ExternalBindingID: series, ExternalActivationID: externalID,
		ExpectedPreviousActivationID: previous, ActiveBindingID: &bindingID,
		ActorID: adapterID, ActorVersion: adapterVersion, OccurredAtUsec: time.Now().UnixMicro(),
	}
	if err := store.ApplyPolicyActivation(ctx, activation); err != nil {
		return err
	}
	result.ActivationID, result.Changed = activationID, true
	return emit(result)
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

package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// supersessionCommand dispatches local store administration for revision-pinned supersession
// records. It operates on the store named by -store as trusted local administration: the commands
// grant no retrieval access, change no grant, policy or authentication state, and are not MCP
// tools or remote endpoints. Activation administration records which declaration is active for one
// source; it selects no evidence and filters no query.
func supersessionCommand(ctx context.Context, storePath string, args []string) error {
	if len(args) == 0 {
		return usageError{"supersession requires a declaration or activation subcommand"}
	}
	switch args[0] {
	case "declaration":
		return declarationCommand(ctx, storePath, args[1:])
	case "activation":
		return activationCommand(ctx, storePath, args[1:])
	default:
		return usageError{fmt.Sprintf("unknown supersession subcommand %q", args[0])}
	}
}

func declarationCommand(ctx context.Context, storePath string, args []string) error {
	if len(args) == 0 {
		return usageError{"supersession declaration requires put or get"}
	}
	switch args[0] {
	case "put":
		return declarationPutCommand(ctx, storePath, args[1:])
	case "get":
		return declarationGetCommand(ctx, storePath, args[1:])
	default:
		return usageError{fmt.Sprintf("unknown supersession declaration subcommand %q", args[0])}
	}
}

// activationCommand dispatches activation administration: put and get administer the immutable
// events, and state inspects the verified current projection.
func activationCommand(ctx context.Context, storePath string, args []string) error {
	if len(args) == 0 {
		return usageError{"supersession activation requires put, get or state"}
	}
	switch args[0] {
	case "put":
		return activationPutCommand(ctx, storePath, args[1:])
	case "get":
		return activationGetCommand(ctx, storePath, args[1:])
	case "state":
		return activationStateCommand(ctx, storePath, args[1:])
	default:
		return usageError{fmt.Sprintf("unknown supersession activation subcommand %q", args[0])}
	}
}

// declarationPutCommand reads exactly one mousa.supersession_declaration.v1 object from stdin and
// appends it to the explicitly selected writable store. Storage semantics are the store's: an
// exact retry is idempotent, a conflicting identity is rejected, and nothing here creates a source,
// item or revision or repairs an identity. The command makes no claim about actor authentication;
// author and basis stay untrusted labels.
func declarationPutCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("supersession declaration put")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if len(flags.Args()) != 0 {
		return usageError{"supersession declaration put takes no arguments; it reads one declaration object from stdin"}
	}
	data, err := readBoundedInput(os.Stdin, mousa.MaxSupersessionDeclarationBytes, "declaration")
	if err != nil {
		return err
	}
	declaration, err := mousa.DecodeSupersessionDeclaration(data)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.PutSupersessionDeclaration(ctx, declaration); err != nil {
		return err
	}
	// PutSupersessionDeclaration verified this exact canonical encoding against its read-back
	// inside the write transaction, so it is the stored record byte for byte.
	return emitDeclaration(declaration)
}

// declarationGetCommand reads one stored declaration by its stable identity. The store opens
// read-only, so a read never migrates, repairs or creates a store.
func declarationGetCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("supersession declaration get")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	positional := flags.Args()
	if len(positional) != 1 {
		return usageError{"supersession declaration get takes one declaration ID"}
	}
	id, err := mousa.ParseSupersessionDeclarationID(positional[0])
	if err != nil {
		return usageError{err.Error()}
	}
	store, err := sqlite.OpenReadOnly(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	declaration, err := store.GetSupersessionDeclaration(ctx, id)
	if err != nil {
		return err
	}
	return emitDeclaration(declaration)
}

// activationPutCommand reads exactly one mousa.supersession_activation.v1 transition from stdin and
// applies it to the explicitly selected writable store. The caller supplies the existing event
// identity and the explicit expected predecessor and selected declaration fields; nothing here
// infers the current predecessor or constructs a transition the caller did not state. Store
// semantics govern initial selection, replacement, deactivation, reactivation, exact retry, stale
// or competing expectations, redundant selection and inconsistent history; actor, time and reason
// are recorded evidence, not authority.
func activationPutCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("supersession activation put")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if len(flags.Args()) != 0 {
		return usageError{"supersession activation put takes no arguments; it reads one activation object from stdin"}
	}
	data, err := readBoundedInput(os.Stdin, mousa.MaxSupersessionActivationBytes, "activation")
	if err != nil {
		return err
	}
	activation, err := mousa.DecodeSupersessionActivation(data)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.ApplySupersessionActivation(ctx, activation); err != nil {
		return err
	}
	// ApplySupersessionActivation verified this exact canonical encoding against its read-back
	// inside the write transaction, so it is the stored event byte for byte.
	return emitActivation(activation)
}

// activationGetCommand reads one stored activation event by its stable identity, keeping the
// store's historical event-read semantics: an event stays readable after the current state
// advances, because history is never rewritten. The store opens read-only, so a read never
// creates, migrates or repairs a store.
func activationGetCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("supersession activation get")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	positional := flags.Args()
	if len(positional) != 1 {
		return usageError{"supersession activation get takes one activation ID"}
	}
	id, err := mousa.ParseSupersessionActivationID(positional[0])
	if err != nil {
		return usageError{err.Error()}
	}
	store, err := sqlite.OpenReadOnly(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	activation, err := store.GetSupersessionActivation(ctx, id)
	if err != nil {
		return err
	}
	return emitActivation(activation)
}

// activationStateCommand reports the verified current activation state for one source: the event in
// effect and the declaration it selects, or an explicit null declaration when that source is
// deactivated. The store opens read-only, so the read never creates, migrates or repairs a store,
// and a source with no activation history is the store's not_found error rather than an empty
// success. The store verifies the current projection, the latest event's canonical bytes and the
// whole predecessor chain in one read snapshot, so damaged or missing state fails as an integrity
// error and is never rebuilt here.
//
// The output is a projection view over those verified records, not a canonical record with its own
// identity. It is also a snapshot rather than a reservation: a caller that transitions next must
// still state its own expected predecessor and can still conflict if another caller wins first.
func activationStateCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("supersession activation state")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	positional := flags.Args()
	if len(positional) != 1 {
		return usageError{"supersession activation state takes one source ID"}
	}
	sourceID, err := mousa.ParseSourceID(positional[0])
	if err != nil {
		return usageError{err.Error()}
	}
	store, err := sqlite.OpenReadOnly(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	state, err := store.GetSupersessionActivationState(ctx, sourceID)
	if err != nil {
		return err
	}
	return emitActivationState(state)
}

// readBoundedInput reads one bounded stdin payload for the named record kind. Reading at most one
// byte past the limit rejects an oversized stream before decoding and without buffering it.
func readBoundedInput(r io.Reader, limit int, kind string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%s input exceeds %d bytes", kind, limit)
	}
	return data, nil
}

// emitDeclaration writes the canonical declaration JSON, which is the same bytes the store keeps
// in record_json; the codec canonicalizes so a put and a later get emit identical output.
func emitDeclaration(declaration mousa.SupersessionDeclaration) error {
	data, err := mousa.EncodeSupersessionDeclaration(declaration)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

// emitActivation writes the canonical activation JSON, which is the same bytes the store keeps in
// record_json; the codec canonicalizes so an apply and a later get emit identical output.
func emitActivation(activation mousa.SupersessionActivation) error {
	data, err := mousa.EncodeSupersessionActivation(activation)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

// activationStateResponse is the CLI projection of one source's verified current activation state.
// It is a view over stored records rather than a canonical record: the identity strings are the
// same lowercase hexadecimal values the event and declaration records carry, and a null
// active_declaration_id states that the source is deactivated rather than that the field is absent.
type activationStateResponse struct {
	SourceID            string  `json:"source_id"`
	CurrentActivationID string  `json:"current_activation_id"`
	ActiveDeclarationID *string `json:"active_declaration_id"`
}

// emitActivationState writes one deterministic JSON object for a verified current state, using the
// same report-view encoder as the other inspection commands.
func emitActivationState(state mousa.SupersessionActivationState) error {
	response := activationStateResponse{
		SourceID:            state.SourceID.String(),
		CurrentActivationID: state.CurrentActivationID.String(),
	}
	if state.ActiveDeclarationID != nil {
		declarationID := state.ActiveDeclarationID.String()
		response.ActiveDeclarationID = &declarationID
	}
	return emit(response)
}

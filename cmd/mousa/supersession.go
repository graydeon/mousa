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
// tools or remote endpoints. Activation administration is not implemented here.
func supersessionCommand(ctx context.Context, storePath string, args []string) error {
	if len(args) == 0 {
		return usageError{"supersession requires a declaration subcommand"}
	}
	switch args[0] {
	case "declaration":
		return declarationCommand(ctx, storePath, args[1:])
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
	data, err := readDeclarationInput(os.Stdin)
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

// readDeclarationInput reads one bounded declaration payload. Reading at most one byte past the
// codec limit rejects an oversized stream before decoding and without buffering it.
func readDeclarationInput(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, mousa.MaxSupersessionDeclarationBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > mousa.MaxSupersessionDeclarationBytes {
		return nil, fmt.Errorf("declaration input exceeds %d bytes", mousa.MaxSupersessionDeclarationBytes)
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

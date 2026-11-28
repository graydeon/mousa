// Command mousa synchronizes local text items and returns source-linked evidence
// under an explicit byte budget. Canonical revisions are immutable; current-item
// activation and the lexical index change together in one transaction.
package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

const (
	localNamespace   = "mousa-local"
	adapterID        = "mousa-local"
	adapterVersion   = "1"
	itemPrefix       = "item/"
	segmentKeyPrefix = "body"
	syncBatchItems   = 16
)

func main() {
	ctx := context.Background()
	storePath := flag.String("store", "", "Mousa store file")
	endpoint := flag.String("endpoint", "", "served store base URL instead of -store")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 || (*storePath != "" && *endpoint != "") {
		usage()
	}
	command, rest := args[0], args[1:]
	// With neither flag the client configuration names the served store, so
	// an agent runs plain `mousa query`. An explicit -store never reads it.
	var config clientConfig
	if *storePath == "" && command != "agent" {
		var configErr error
		if config, configErr = loadClientConfig(); configErr != nil {
			fmt.Fprintf(os.Stderr, "mousa: %v\n", configErr)
			os.Exit(1)
		}
		if *endpoint == "" {
			*endpoint = cmp.Or(os.Getenv("MOUSA_ENDPOINT"), config.Endpoint)
		}
		if *endpoint == "" {
			usage()
		}
	}
	if *endpoint != "" || command == "serve" {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
	}
	// The endpoint client is built only for the commands that can use it;
	// everything else stays a direct-store command and never falls back.
	var client *nativeClient
	if *endpoint != "" {
		switch command {
		case "query", "remember", "sync", "move", "recheck":
			var clientErr error
			client, clientErr = newNativeClient(*endpoint, config)
			if clientErr != nil {
				fmt.Fprintf(os.Stderr, "mousa: %v\n", clientErr)
				os.Exit(1)
			}
		default:
			fmt.Fprintf(os.Stderr, "mousa: a served store supports only query, remember, sync --source, move and recheck --trail --text; use -store for %s\n", command)
			os.Exit(2)
		}
	}
	var err error
	switch command {
	case "sync":
		err = syncCommand(ctx, *storePath, client, rest)
	case "status":
		err = statusCommand(ctx, *storePath, rest)
	case "query":
		err = queryCommand(ctx, *storePath, client, rest)
	case "trail":
		err = trailCommand(ctx, *storePath, rest)
	case "verify":
		err = verifyCommand(ctx, *storePath, rest)
	case "migrate":
		if len(rest) != 0 {
			err = usageError{"migrate takes no arguments"}
		} else {
			err = migrateCommand(ctx, *storePath)
		}
	case "move":
		err = moveCommand(ctx, *storePath, client, rest)
	case "collection":
		err = collectionCommand(ctx, *storePath, rest)
	case "changes":
		err = changesCommand(ctx, *storePath, rest)
	case "recheck":
		err = recheckCommand(ctx, *storePath, client, rest)
	case "access":
		err = accessCommand(ctx, *storePath, rest)
	case "remember":
		err = rememberCommand(ctx, *storePath, client, rest)
	case "notes":
		err = notesCommand(ctx, *storePath, rest)
	case "withdraw":
		err = withdrawCommand(ctx, *storePath, rest)
	case "serve":
		err = serveCommand(ctx, *storePath, rest)
	case "agent":
		err = agentCommand(rest)
	default:
		fmt.Fprintf(os.Stderr, "mousa: unknown command %q\n", command)
		usage()
	}
	if err != nil {
		var invalid usageError
		if errors.As(err, &invalid) {
			fmt.Fprintf(os.Stderr, "mousa: %v\n", invalid)
			usage()
		}
		fmt.Fprintf(os.Stderr, "mousa: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: mousa -store <path> <command> [args]
       mousa [-endpoint <url>] query|remember|sync|move --source <id>
       mousa [-endpoint <url>] recheck --trail TRAIL_ID --text
       mousa agent add <name> --config <file> --source <id>

commands:
  sync <dir>                  import or re-sync a UTF-8/Markdown directory (JSON result)
  sync --source <id>          import or re-sync JSONL item records from stdin (JSON result)
  query <dir> <text>          query a synced directory, returns bounded evidence (JSON)
  query --source <id> <text>  query JSONL sources (repeat --source for a list)
  query --collection <name> <text>
                            query a named collection
  collection create|list|add|remove
                            manage a named set of sources; add/remove take --source ID NAME
  changes --since SEQ [--source ID|--collection NAME]
                            read new events for allowed sources
  recheck --trail TRAIL_ID [--text]
                            check whether a trail's released evidence is still
                            current; --text also releases each packed segment's
                            text when available, and is the only served form
  recheck --source ID NOTE_ID
                            check a durable note's current citation status
  status <dir>                report the source's ingest state (JSON)
  status --source <id>        report a JSONL-synced source's ingest state (JSON)
  trail <trail-id>            inspect one recorded query trail, never text (JSON)
  trail list [--source ID] [--segment ID] [--since SEQ]
                            list owner-visible trail metadata in log order (JSONL)
  verify [--log | --trail <trail-id> | --stdin]
                            verify the store, log, or named trails read-only (JSON)
  migrate                   upgrade a schema-7 through schema-9 store to the current
                            schema in place; takes no arguments, prints
                            {"schema":10}
  move <dir> | --source <id> --note ID|--item ID|--prefix P --to DEST
                            relocate one note, one document item, or every
                            item under a path prefix; --prefix and its --to
                            both end in /
  access <dir> allow|deny     record an access event for every caller of the source (JSON)
  access --source <id> allow|deny
                            a source with no access event is denied to every caller;
                            --create first registers an explicit JSONL source
  access --source <id> --history on|off
                            opt a source in or out of retaining retiring
                            segment text; grants no access
  remember --source <id> [text]
                            store one durable note; exact stdin text when no
                            argument is given; --from TRAIL[:SEG,SEG] cites a
                            query trail, repeatable; --path places a new note
                            (default unsorted); --supersedes NOTE_ID marks a
                            replaced note, repeatable
  notes export --source <id>
                            export the source's notes exactly, one JSONL record each
  notes import --source <id>
                            import previously exported notes from stdin JSONL
  withdraw <dir>             withdraw a directory source from retrieval (JSON)
  withdraw --source <id>     withdraw a JSONL source from retrieval (JSON)
  serve --config <file> [--listen 127.0.0.1:PORT] [--open-monitor]
                            own the store and serve it on loopback HTTP;
                            query, remember and sync --source also run against
                            a served store named by -endpoint, MOUSA_ENDPOINT
                            or the client configuration
  agent add <name> --config <file> --source <id> [--token-file <path>]
                            add a principal to the serve configuration and mint
                            its token; --read/--write grant one direction only

client configuration ($MOUSA_CONFIG, else <user config dir>/mousa/client.json):
  {"endpoint": "http://127.0.0.1:8787", "token_file": "PATH"}
  token comes from MOUSA_TOKEN, else token_file, else token_command (an argv
  array whose output is the token); {harness} in either expands to MOUSA_HARNESS

query options (before positional arguments):
  default                    one-line JSON with trail ID and passages
  --full                     show the full trail and passage metadata
  --budget-bytes <positive>  released UTF-8 text bytes; default 8192, not model tokens
  --limit <1-200>            lexical candidate limit; zero uses the default 200
  --mode lexical             query mode; only lexical is available
  --include-superseded       pack passages whose notes were superseded

sync options (before positional arguments):
  --segment-policy fixed-v1|passage-v1
                            ingestion boundaries; default fixed-v1, repeat on resync
  --allow                   after syncing, record allow if the source has no access event

directory sync options (before the root argument):
  --preview                 list selection without opening or changing the store
  --include <glob>           replace extension selection; repeatable
  --exclude <glob>           exclude files/directories; repeatable, always wins
  --all-text                override hidden/generated and extension defaults
  --max-file-bytes <N>       positive per-file limit; default 1048576
  --max-bytes <N>            positive total selected bytes; default 67108864
  --max-entries <N>          positive visited-entry limit; default 10000

Directory defaults select .md, .markdown, .txt, .rst and skip hidden/generated entries.
Observed symlinks and .git entries beneath the root are always skipped.
UTF-8 validity is not secret filtering; scope exclusions deactivate old selected items.`)
	os.Exit(2)
}

// usageError reports an invalid invocation: main exits 2 for it, as it did for
// the earlier positional-argument checks.
type usageError struct{ message string }

func (e usageError) Error() string { return e.message }

// newCommandFlags parses one command's flags. Flag output is discarded because
// main prints the error and the command usage itself.
func newCommandFlags(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	return flags
}

// syncCommand selects the input form: a directory root, or an explicit
// external source ID with JSONL item records on stdin. With an endpoint
// client only the --source form is available and --allow stays offline.
func syncCommand(ctx context.Context, storePath string, client *nativeClient, args []string) error {
	flags := newCommandFlags("sync")
	sourceID := flags.String("source", "", "external source ID; reads JSONL item records from stdin instead of a directory")
	segmentPolicy := flags.String("segment-policy", mousa.TextSegmentFixedV1, "ingestion segmentation: fixed-v1 or passage-v1")
	allow := flags.Bool("allow", false, "after syncing, record allow if the source has no access event")
	options := directoryFlags(flags)
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if *segmentPolicy != mousa.TextSegmentFixedV1 && *segmentPolicy != mousa.TextSegmentPassageV1 {
		return usageError{"segment policy must be fixed-v1 or passage-v1"}
	}
	rest := flags.Args()
	switch {
	case client != nil:
		if *sourceID == "" || len(rest) != 0 {
			return usageError{"endpoint sync requires --source <external-id> with JSONL records on stdin; directory sync stays offline"}
		}
		if *allow {
			return usageError{"--allow cannot be used with -endpoint; grant sources with offline access commands"}
		}
		directoryOption := false
		flags.Visit(func(option *flag.Flag) {
			if option.Name != "source" && option.Name != "segment-policy" && option.Name != "allow" {
				directoryOption = true
			}
		})
		if directoryOption {
			return usageError{"directory selection flags cannot be used with --source"}
		}
		return runSyncEndpoint(ctx, client, *sourceID, os.Stdin, *segmentPolicy)
	case *sourceID == "" && len(rest) == 1:
		return runSyncDirectory(ctx, storePath, rest[0], *options, *segmentPolicy, *allow)
	case *sourceID != "" && len(rest) == 0:
		directoryOption := false
		flags.Visit(func(option *flag.Flag) {
			if option.Name != "source" && option.Name != "segment-policy" && option.Name != "allow" {
				directoryOption = true
			}
		})
		if directoryOption {
			return usageError{"directory selection flags cannot be used with --source"}
		}
		return runSyncJSONL(ctx, storePath, *sourceID, os.Stdin, *segmentPolicy, *allow)
	default:
		return usageError{"sync takes one directory argument, or --source <external-id> with JSONL records on stdin"}
	}
}

// statusCommand names its source with a directory argument or --source.
func statusCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("status")
	sourceID := flags.String("source", "", "external source ID instead of a directory root")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	source, label, err := selectSource(*sourceID, flags.Args(), "status")
	if err != nil {
		return err
	}
	return runStatus(ctx, storePath, source, label)
}

// selectSource resolves the source a command acts on: either a directory root
// (the external source ID is its absolute path) or an explicit external source
// ID for a source that has no directory. Exactly one spelling is accepted.
func selectSource(externalSourceID string, positional []string, command string) (mousa.Source, string, error) {
	if externalSourceID != "" {
		if len(positional) != 0 {
			return mousa.Source{}, "", usageError{fmt.Sprintf("%s takes either a directory argument or --source <external-id>, not both", command)}
		}
		source, err := streamSource(externalSourceID)
		if err != nil {
			return mousa.Source{}, "", err
		}
		return source, externalSourceID, nil
	}
	if len(positional) != 1 {
		return mousa.Source{}, "", usageError{fmt.Sprintf("%s takes one directory argument or --source <external-id>", command)}
	}
	return directorySource(positional[0])
}

// directorySource derives the deterministic Source identity for one directory
// root. The absolute path is the external identity, so moving the root
// creates a new source rather than silently mixing content.
func directorySource(root string) (mousa.Source, string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return mousa.Source{}, "", err
	}
	sourceID, err := mousa.NewSourceID(localNamespace, absolute)
	if err != nil {
		return mousa.Source{}, "", err
	}
	return mousa.Source{Schema: mousa.SourceSchema, ID: sourceID, Namespace: localNamespace, ExternalSourceID: absolute}, absolute, nil
}

// streamSource uses a separate namespace so a stream ID cannot collide with a
// directory's absolute path.
func streamSource(externalSourceID string) (mousa.Source, error) {
	sourceID, err := mousa.NewSourceID("mousa-jsonl", externalSourceID)
	if err != nil {
		return mousa.Source{}, err
	}
	return mousa.Source{Schema: mousa.SourceSchema, ID: sourceID, Namespace: "mousa-jsonl", ExternalSourceID: externalSourceID}, nil
}

// itemRevisionObservationID derives the observation identity of a specific
// revision: the item path plus its content digest. A changed file therefore
// gets a fresh Observation (and fresh Artifact/Representation/Segments), while
// re-ingesting the same content stays an exact receipt-identical retry.
func itemRevisionObservationID(source mousa.Source, relativePath string, digest mousa.SHA256) (mousa.ObservationID, error) {
	return mousa.NewObservationID(source.ID, fmt.Sprintf("%s%s@%x", itemPrefix, relativePath, digest))
}

// itemInput is one item the local slice applies: its identifier within the
// source, and either its current content or an explicit removal. Directory
// sync builds inputs from files; the JSONL stream builds them from records.
// Both go through applyItem so the two input paths share one lifecycle.
type itemInput struct {
	ID       string
	Content  []byte
	Deleted  bool
	Captured int64
	// Path is an explicit destination location; nil leaves the current or
	// default path alone. Served inputs record the requesting actor.
	Path  *string
	Actor string
}

// Item actions reported in a sync result.
const (
	actionAdded     = "added"
	actionUpdated   = "updated"
	actionMoved     = "moved"
	actionRestored  = "restored"
	actionUnchanged = "unchanged"
	actionDeleted   = "deleted"
	actionAbsent    = "absent"
)

func applyItemBatch(ctx context.Context, store *sqlite.Store, source mousa.Source, items []itemInput, sequence *uint64, policy string, result *syncResult) (int, error) {
	actions := make([]string, len(items))
	completed, err := store.ApplyDocumentBatch(ctx, len(items), func(scoped *sqlite.Store, i int) error {
		if sequence != nil && !items[i].Deleted {
			(*sequence)++
		}
		action, err := applyItem(ctx, scoped, source, items[i], sequence, policy)
		actions[i] = action
		return err
	})
	for i := range completed {
		result.record(actions[i], items[i].ID)
	}
	return completed, err
}

// applyItem compares raw-content identity with the explicitly active revision.
// Canonical history may be prepared independently; index activation is atomic.
func applyItem(ctx context.Context, store *sqlite.Store, source mousa.Source, input itemInput, sequence *uint64, segmentPolicy string) (string, error) {
	if input.Deleted {
		if input.Path != nil {
			return "", usageError{"a deleted item cannot name a path"}
		}
		finishIndex := store.StartStage(mousa.StageIndex)
		action, err := store.DeleteLocalItem(ctx, source.ID, input.ID)
		finishIndex(err)
		return action, err
	}
	if input.Path != nil && !mousa.ValidPath(*input.Path) {
		return "", usageError{"path must be lowercase segments of letters, digits, '.', '_' and '-' joined by single slashes"}
	}
	digest := mousa.SHA256(sha256.Sum256(input.Content))
	current, err := store.GetLocalItem(ctx, source.ID, input.ID)
	if err != nil && !sqlite.IsCode(err, sqlite.CodeNotFound) {
		return "", err
	}
	if err == nil && current.Active && current.SegmentationPolicy == segmentPolicy && current.Artifact.ContentSHA256 == digest && current.Artifact.ByteLength == uint64(len(input.Content)) {
		// An unchanged item performs no representation or index work; an
		// explicit path still moves it without touching the revision.
		store.ObserveMarker(mousa.StageRepresent, mousa.StagePhaseSkip, 0)
		store.ObserveMarker(mousa.StageIndex, mousa.StagePhaseSkip, 0)
		if input.Path != nil && *input.Path != current.Path {
			moved, err := store.SetDocumentPath(ctx, source.ID, input.ID, *input.Path, input.Actor)
			if err != nil {
				return "", err
			}
			if moved {
				return actionMoved, nil
			}
		}
		return actionUnchanged, nil
	}
	return ingestRevision(ctx, store, source, input, digest, sequence, segmentPolicy)
}

// ingestRevision reuses accepted delivery evidence when restoring old content.
// Partial preparation is immutable and can be retried without changing activation.
func ingestRevision(ctx context.Context, store *sqlite.Store, source mousa.Source, input itemInput, digest mousa.SHA256, sequence *uint64, segmentPolicy string) (string, error) {
	observationID, err := itemRevisionObservationID(source, input.ID, digest)
	if err != nil {
		return "", err
	}
	artifactID, err := mousa.NewArtifactID(observationID, segmentKeyPrefix)
	if err != nil {
		return "", err
	}
	capturedAtUsec := input.Captured
	if capturedAtUsec <= 0 {
		capturedAtUsec = time.Now().UnixMicro()
	}
	if accepted, err := store.GetIngestReceipt(ctx, observationID); err == nil {
		capturedAtUsec, sequence = accepted.CapturedAtUsec, accepted.Sequence
	} else if !sqlite.IsCode(err, sqlite.CodeNotFound) {
		return "", err
	}
	artifact := mousa.Artifact{
		Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: segmentKeyPrefix,
		MediaType: mousa.UTF8TextMediaType, ContentSHA256: digest, ByteLength: uint64(len(input.Content)),
	}
	finishRepresent := store.StartStage(mousa.StageRepresent)
	representation, normalized, err := mousa.NormalizeUTF8TextWithPolicy(artifact, input.Content, segmentPolicy)
	if err != nil {
		finishRepresent(err)
		return "", err
	}
	segments, err := mousa.SegmentUTF8Text(representation, normalized)
	finishRepresent(err)
	if err != nil {
		return "", err
	}
	observation := mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: source.ID, ExternalObservationID: fmt.Sprintf("%s%s@%x", itemPrefix, input.ID, digest)}
	batch := mousa.IngestBatch{
		AdapterID: adapterID, AdapterVersion: adapterVersion, Initiative: mousa.InitiativePush, Form: mousa.FormItem,
		CapturedAtUsec: capturedAtUsec, Sequence: sequence,
		Source: source, Observation: observation, Artifacts: []mousa.Artifact{artifact},
	}
	if err := store.ApplyIngest(ctx, batch); err != nil {
		return "", err
	}
	if err := store.PutRepresentation(ctx, representation); err != nil {
		return "", err
	}
	for _, segment := range segments {
		if err := store.PutSegment(ctx, segment); err != nil {
			return "", err
		}
	}
	finishIndex := store.StartStage(mousa.StageIndex)
	action, err := store.ActivateLocalItem(ctx, source.ID, input.ID, representation.ID, normalized, input.Path, input.Actor)
	finishIndex(err)
	return action, err
}

// The result is machine-readable and reports every action. One struct serves
// both input forms: a directory root and a JSONL record stream.
type syncResult struct {
	Source        string          `json:"source"`
	Input         string          `json:"input"`
	Root          string          `json:"root,omitempty"`
	SegmentPolicy string          `json:"segment_policy"`
	Added         []string        `json:"added,omitempty"`
	Updated       []string        `json:"updated,omitempty"`
	Restored      []string        `json:"restored,omitempty"`
	Unchanged     []string        `json:"unchanged,omitempty"`
	Deleted       []string        `json:"deleted,omitempty"`
	Absent        []string        `json:"absent,omitempty"`
	Skipped       []directorySkip `json:"skipped,omitempty"`
	TotalItems    int             `json:"total_items"`
	StoreBytes    int64           `json:"store_bytes"`
	ElapsedSecs   float64         `json:"elapsed_seconds"`
	Access        string          `json:"access,omitempty"`
	AccessEventID string          `json:"access_event_id,omitempty"`
}

// record files one applied item under the action that was taken.
func (result *syncResult) record(action, item string) {
	switch action {
	case actionAdded:
		result.Added = append(result.Added, item)
	case actionUpdated, actionMoved:
		result.Updated = append(result.Updated, item)
	case actionRestored:
		result.Restored = append(result.Restored, item)
	case actionUnchanged:
		result.Unchanged = append(result.Unchanged, item)
	case actionDeleted:
		result.Deleted = append(result.Deleted, item)
	case actionAbsent:
		result.Absent = append(result.Absent, item)
	}
}

// runStatus reports the stored ingest state of one resolved source.
func runStatus(ctx context.Context, storePath string, source mousa.Source, label string) error {
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := sourceStatus(ctx, store, source, label)
	if err != nil {
		return err
	}
	return emit(result)
}

func sourceStatus(ctx context.Context, store *sqlite.Store, source mousa.Source, label string) (statusResult, error) {
	state, err := store.GetIngestState(ctx, source.ID)
	if err != nil {
		if sqlite.IsCode(err, sqlite.CodeNotFound) {
			return statusResult{Source: label, CollectionState: "absent"}, nil
		}
		return statusResult{}, err
	}
	active, observations, err := store.LocalSourceCounts(ctx, source.ID)
	if err != nil {
		return statusResult{}, err
	}
	return statusResult{Source: label, CollectionState: string(state.CollectionState), ActiveItems: active, Observations: observations}, nil
}

// runSyncDirectory applies the complete selected set. Scope exclusions deactivate
// old items just like removal; selection errors leave the store untouched.
func runSyncDirectory(ctx context.Context, storePath, root string, options directoryOptions, segmentPolicy string, allow bool) error {
	if options.Preview && allow {
		return usageError{"--allow cannot be used with --preview"}
	}
	started := time.Now()
	source, absolute, err := directorySource(root)
	if err != nil {
		return err
	}
	selection, err := selectDirectory(absolute, options)
	if err != nil {
		return err
	}
	if options.Preview {
		return emit(map[string]any{
			"preview": true, "source": absolute, "selected": selection.Items,
			"skipped": selection.Skipped, "selected_bytes": selection.Bytes,
			"segment_policy": segmentPolicy,
		})
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.RequireDocumentSource(ctx, source.ID); err != nil {
		return err
	}
	result := syncResult{Source: absolute, Root: absolute, Input: inputDirectory, Skipped: selection.Skipped, SegmentPolicy: segmentPolicy}
	var sequence uint64
	selected := make(map[string]struct{}, len(selection.Items))
	pending := make([]itemInput, 0, syncBatchItems)
	flush := func() error {
		n, err := applyItemBatch(ctx, store, source, pending, &sequence, segmentPolicy, &result)
		if err != nil {
			return fmt.Errorf("item %s: %w", pending[n].ID, err)
		}
		pending = pending[:0]
		return nil
	}
	add := func(item itemInput) error {
		pending = append(pending, item)
		if len(pending) == syncBatchItems {
			return flush()
		}
		return nil
	}
	for _, item := range selection.Items {
		selected[item.ID] = struct{}{}
		if err := add(itemInput{ID: item.ID, Content: item.content, Captured: item.captured}); err != nil {
			return err
		}
	}
	if err := flush(); err != nil {
		return err
	}
	// A completed selection defines both additions and removals from scope.
	previous, err := store.LocalItemIDs(ctx, source.ID)
	if err != nil {
		return err
	}
	for _, path := range previous {
		if _, exists := selected[path]; exists {
			continue
		}
		if err := add(itemInput{ID: path, Deleted: true}); err != nil {
			return err
		}
	}
	if err := flush(); err != nil {
		return err
	}
	result.TotalItems = len(selection.Items)
	if allow {
		if err := allowAfterSync(ctx, store, source.ID, &result); err != nil {
			return err
		}
	}
	if info, err := os.Stat(storePath); err == nil {
		result.StoreBytes = info.Size()
	}
	result.ElapsedSecs = time.Since(started).Seconds()
	return emit(result)
}

type statusResult struct {
	Source          string `json:"source"`
	CollectionState string `json:"collection_state"`
	Observations    int    `json:"observations"`
	ActiveItems     int    `json:"active_items"`
}

func emit(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
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

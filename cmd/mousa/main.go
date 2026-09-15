// Command mousa synchronizes local text items and returns source-linked evidence
// under an explicit byte budget. Canonical revisions are immutable; current-item
// activation and the lexical index change together in one transaction.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

const (
	localNamespace   = "mousa-local"
	adapterID        = "mousa-local"
	adapterVersion   = "1"
	itemPrefix       = "item/"
	segmentKeyPrefix = "body"
)

func main() {
	ctx := context.Background()
	storePath := flag.String("store", "", "Mousa store file (required)")
	flag.Parse()
	args := flag.Args()
	if *storePath == "" || len(args) == 0 {
		usage()
	}
	command, rest := args[0], args[1:]
	var err error
	switch command {
	case "sync":
		err = syncCommand(ctx, *storePath, rest)
	case "status":
		err = statusCommand(ctx, *storePath, rest)
	case "query":
		err = queryCommand(ctx, *storePath, rest)
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

commands:
  sync <dir>                  import or re-sync a UTF-8/Markdown directory (JSON result)
  sync --source <id>          import or re-sync JSONL item records from stdin (JSON result)
  query <dir> <text>          query a synced directory, returns bounded evidence (JSON)
  query --source <id> <text>  query a JSONL-synced source, returns bounded evidence (JSON)
  status <dir>                report the source's ingest state (JSON)
  status --source <id>        report a JSONL-synced source's ingest state (JSON)`)
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
// external source ID with JSONL item records on stdin.
func syncCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("sync")
	sourceID := flags.String("source", "", "external source ID; reads JSONL item records from stdin instead of a directory")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	rest := flags.Args()
	switch {
	case *sourceID == "" && len(rest) == 1:
		return runSyncDirectory(ctx, storePath, rest[0])
	case *sourceID != "" && len(rest) == 0:
		return runSyncJSONL(ctx, storePath, *sourceID, os.Stdin)
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

// queryCommand names its source with a directory argument or --source, and
// always takes the query text as the remaining argument.
func queryCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("query")
	sourceID := flags.String("source", "", "external source ID instead of a directory root")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	source, label, query, err := selectQuerySource(*sourceID, flags.Args())
	if err != nil {
		return err
	}
	return runQuery(ctx, storePath, source, label, query)
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

// selectQuerySource is selectSource for query, which also takes the query text.
func selectQuerySource(externalSourceID string, positional []string) (mousa.Source, string, string, error) {
	if externalSourceID != "" {
		if len(positional) != 1 {
			return mousa.Source{}, "", "", usageError{"query --source <external-id> takes exactly one query-text argument"}
		}
		source, err := streamSource(externalSourceID)
		if err != nil {
			return mousa.Source{}, "", "", err
		}
		return source, externalSourceID, positional[0], nil
	}
	if len(positional) != 2 {
		return mousa.Source{}, "", "", usageError{"query takes <dir> <query text> or --source <external-id> <query text>"}
	}
	source, label, err := directorySource(positional[0])
	if err != nil {
		return mousa.Source{}, "", "", err
	}
	return source, label, positional[1], nil
}

// directorySource derives the deterministic Source identity for one directory
// root. The absolute path is the external identity, so moving the root
// creates a new source rather than silently mixing content.
func directorySource(root string) (mousa.Source, string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return mousa.Source{}, "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return mousa.Source{}, "", err
	}
	if !info.IsDir() {
		return mousa.Source{}, "", fmt.Errorf("%s is not a directory", absolute)
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

// localItems walks one root and returns the current item set: relative POSIX
// path → absolute file path, sorted for deterministic order. Non-UTF-8 files
// and anything not a regular file are skipped and reported.
func localItems(root string) (map[string]string, []string, error) {
	items := map[string]string{}
	var skipped []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			skipped = append(skipped, path)
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !utf8.Valid(raw) {
			skipped = append(skipped, path)
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		items[filepath.ToSlash(relative)] = path
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return items, skipped, nil
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
}

// Item actions reported in a sync result.
const (
	actionAdded     = "added"
	actionUpdated   = "updated"
	actionRestored  = "restored"
	actionUnchanged = "unchanged"
	actionDeleted   = "deleted"
	actionAbsent    = "absent"
)

// applyItem compares raw-content identity with the explicitly active revision.
// Canonical history may be prepared independently; index activation is atomic.
func applyItem(ctx context.Context, store *sqlite.Store, source mousa.Source, input itemInput, sequence *uint64) (string, error) {
	if input.Deleted {
		return store.DeleteLocalItem(ctx, source.ID, input.ID)
	}
	digest := mousa.SHA256(sha256.Sum256(input.Content))
	current, err := store.GetLocalItem(ctx, source.ID, input.ID)
	if err != nil && !sqlite.IsCode(err, sqlite.CodeNotFound) {
		return "", err
	}
	if err == nil && current.Active && current.Artifact.ContentSHA256 == digest && current.Artifact.ByteLength == uint64(len(input.Content)) {
		return actionUnchanged, nil
	}
	return ingestRevision(ctx, store, source, input, digest, sequence)
}

// ingestRevision reuses accepted delivery evidence when restoring old content.
// Partial preparation is immutable and can be retried without changing activation.
func ingestRevision(ctx context.Context, store *sqlite.Store, source mousa.Source, input itemInput, digest mousa.SHA256, sequence *uint64) (string, error) {
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
	representation, normalized, err := mousa.NormalizeUTF8Text(artifact, input.Content)
	if err != nil {
		return "", err
	}
	segments, err := mousa.SegmentUTF8Text(representation, normalized)
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
	return store.ActivateLocalItem(ctx, source.ID, input.ID, representation.ID, normalized)
}

// fileCapturedAtUsec is the capture time of a first delivery from a file: its
// modification time. A missing or non-positive timestamp yields 0, which
// ingestRevision replaces with the local clock.
func fileCapturedAtUsec(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.ModTime().UnixMicro(), nil
}

// The result is machine-readable and reports every action. One struct serves
// both input forms: a directory root and a JSONL record stream.
type syncResult struct {
	Source      string   `json:"source"`
	Input       string   `json:"input"`
	Root        string   `json:"root,omitempty"`
	Added       []string `json:"added,omitempty"`
	Updated     []string `json:"updated,omitempty"`
	Restored    []string `json:"restored,omitempty"`
	Unchanged   []string `json:"unchanged,omitempty"`
	Deleted     []string `json:"deleted,omitempty"`
	Absent      []string `json:"absent,omitempty"`
	Skipped     []string `json:"skipped,omitempty"`
	TotalItems  int      `json:"total_items"`
	StoreBytes  int64    `json:"store_bytes"`
	ElapsedSecs float64  `json:"elapsed_seconds"`
}

// record files one applied item under the action that was taken.
func (result *syncResult) record(action, item string) {
	switch action {
	case actionAdded:
		result.Added = append(result.Added, item)
	case actionUpdated:
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
	state, err := store.GetIngestState(ctx, source.ID)
	if err != nil {
		if sqlite.IsCode(err, sqlite.CodeNotFound) {
			return emit(statusResult{Source: label, CollectionState: "absent"})
		}
		return err
	}
	active, observations, err := store.LocalSourceCounts(ctx, source.ID)
	if err != nil {
		return err
	}
	recovery, err := store.LocalSourceNeedsRecovery(ctx, source.ID)
	if err != nil {
		return err
	}
	return emit(statusResult{Source: label, CollectionState: string(state.CollectionState), ActiveItems: active, Observations: observations, NeedsRecovery: recovery})
}

// runSyncDirectory imports or re-syncs one directory root. Items are the
// root's UTF-8 regular files; an item's relative POSIX path is its identity.
// Deletion is derived from absence: an item whose file no longer exists stops
// being retrievable, because the directory scan is the complete item set.
func runSyncDirectory(ctx context.Context, storePath, root string) error {
	started := time.Now()
	source, absolute, err := directorySource(root)
	if err != nil {
		return err
	}
	items, skipped, err := localItems(absolute)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := deployLocalPolicy(ctx, store); err != nil {
		return fmt.Errorf("deploy policy: %w", err)
	}

	result := syncResult{Source: absolute, Root: absolute, Input: inputDirectory, Skipped: skipped}
	var sequence uint64
	paths := make([]string, 0, len(items))
	for path := range items {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		sequence++
		content, err := os.ReadFile(items[path])
		if err != nil {
			return fmt.Errorf("item %s: %w", path, err)
		}
		capturedAtUsec, err := fileCapturedAtUsec(items[path])
		if err != nil {
			return fmt.Errorf("item %s: %w", path, err)
		}
		action, err := applyItem(ctx, store, source, itemInput{ID: path, Content: content, Captured: capturedAtUsec}, &sequence)
		if err != nil {
			return fmt.Errorf("item %s: %w", path, err)
		}
		result.record(action, path)
	}
	// Deletions: items stored under this source whose file no longer exists.
	previous, err := store.LocalItemIDs(ctx, source.ID)
	if err != nil {
		return err
	}
	for _, path := range previous {
		if _, exists := items[path]; exists {
			continue
		}
		action, err := applyItem(ctx, store, source, itemInput{ID: path, Deleted: true}, &sequence)
		if err != nil {
			return fmt.Errorf("item %s: %w", path, err)
		}
		result.record(action, path)
	}
	if err := store.CompleteLocalRecovery(ctx, source.ID); err != nil {
		return err
	}
	result.TotalItems = len(items)
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
	NeedsRecovery   bool   `json:"needs_recovery"`
}
type evidenceResult struct {
	Query         string        `json:"query"`
	Source        string        `json:"source"`
	DecisionID    string        `json:"decision_id"`
	TotalMatches  int           `json:"total_matches"`
	UsedBytes     uint64        `json:"used_bytes"`
	BudgetBytes   uint64        `json:"budget_bytes"`
	Evidence      []evidenceHit `json:"evidence"`
	LatencyMicros int64         `json:"latency_micros"`
}

type evidenceHit struct {
	Item       string  `json:"item"`
	SegmentID  string  `json:"segment_id"`
	Rank       int     `json:"rank"`
	Score      float64 `json:"bm25_score"`
	ByteLength int     `json:"byte_length"`
	Text       string  `json:"text"`
}

// runQuery runs one authorized query against one resolved source.
func runQuery(ctx context.Context, storePath string, source mousa.Source, label, query string) error {
	started := time.Now()
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	const budgetBytes = 8 << 10
	result, err := queryItems(ctx, store, source, query, budgetBytes)
	if err != nil {
		return err
	}
	result.Query = query
	result.Source = label
	result.BudgetBytes = budgetBytes
	result.LatencyMicros = time.Since(started).Microseconds()
	return emit(result)
}

func emit(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// deployLocalPolicy stores the deployment-scoped allow policy the local
// queries evaluate against: caller mousa-local.caller/cli with purpose
// mousa-local.purpose/retrieval may retrieve any source. This mirrors the
// harness's deployment policy; per-source or deny policies are a later
// contract, not silently absent.
func deployLocalPolicy(ctx context.Context, store *sqlite.Store) error {
	inner := mousa.SourceRetrievalPolicy{
		Schema:            mousa.SourceRetrievalPolicySchema,
		Action:            mousa.SourceRetrievalAction,
		CallerNamespace:   localNamespace + ".caller",
		ExternalCallerID:  "cli",
		PurposeNamespace:  localNamespace + ".purpose",
		ExternalPurposeID: "retrieval",
		Effect:            mousa.SourceRetrievalEffectAllow,
	}
	data, err := mousa.EncodeSourceRetrievalPolicy(inner)
	if err != nil {
		return err
	}
	digest := mousa.SHA256(sha256.Sum256(data))
	definitionID, err := mousa.NewPolicyDefinitionID(localNamespace, "allow", "1", mousa.SourceRetrievalPolicyMediaType, mousa.SourceRetrievalPolicySchema, digest)
	if err != nil {
		return err
	}
	definition := mousa.PolicyDefinition{
		Schema: mousa.PolicyDefinitionSchema, ID: definitionID, Namespace: localNamespace,
		ExternalPolicyID: "allow", ExternalPolicyVersion: "1",
		DefinitionMediaType: mousa.SourceRetrievalPolicyMediaType, DefinitionSchema: mousa.SourceRetrievalPolicySchema,
		DefinitionSHA256: digest, Definition: string(data),
	}
	if err := store.PutPolicyDefinition(ctx, definition); err != nil {
		if sqlite.IsCode(err, sqlite.CodeConflict) {
			return nil // already deployed
		}
		return err
	}
	scope := mousa.NewDeploymentPolicyScope()
	bindingID, err := mousa.NewPolicyBindingID(localNamespace, "deployment", "1", scope, definition.ID)
	if err != nil {
		return err
	}
	binding := mousa.PolicyBinding{
		Schema: mousa.PolicyBindingSchema, ID: bindingID, Namespace: localNamespace,
		ExternalBindingID: "deployment", ExternalBindingVersion: "1", Scope: scope, PolicyDefinitionID: definition.ID,
	}
	if err := store.PutPolicyBinding(ctx, binding); err != nil {
		return err
	}
	activationID, err := mousa.NewPolicyActivationID(localNamespace, "deployment", "activate")
	if err != nil {
		return err
	}
	activation := mousa.PolicyActivation{
		Schema: mousa.PolicyActivationSchema, ID: activationID, Namespace: localNamespace,
		ExternalBindingID: "deployment", ExternalActivationID: "activate",
		ActiveBindingID: &binding.ID, ActorID: localNamespace, ActorVersion: "1", OccurredAtUsec: 1,
	}
	return store.ApplyPolicyActivation(ctx, activation)
}

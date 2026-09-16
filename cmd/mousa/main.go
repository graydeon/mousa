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
	case "trail":
		err = trailCommand(ctx, *storePath, rest)
	case "access":
		err = accessCommand(ctx, *storePath, rest)
	case "withdraw":
		err = withdrawCommand(ctx, *storePath, rest)
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
  status --source <id>        report a JSONL-synced source's ingest state (JSON)
  trail <dir> <trail-id>      inspect authorized historical metadata, never text (JSON)
  trail --source <id> <id>    inspect a JSONL source's trail (JSON)
  access <dir> allow|deny     set this CLI caller's source-scoped access policy (JSON)
  access --source <id> allow|deny
  withdraw <dir>             withdraw a directory source from retrieval (JSON)
  withdraw --source <id>     withdraw a JSONL source from retrieval (JSON)

query options (before positional arguments):
  --policy original|dedup    query-term policy; default original retains repetition
  --budget-bytes <positive>  released UTF-8 text bytes; default 8192, not model tokens
  --packing-policy original|exact-v1
                            exact-v1 omits byte-equal copies of selected passages

sync options (before positional arguments):
  --segment-policy fixed-v1|passage-v1
                            ingestion boundaries; default fixed-v1, repeat on resync

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
// external source ID with JSONL item records on stdin.
func syncCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("sync")
	sourceID := flags.String("source", "", "external source ID; reads JSONL item records from stdin instead of a directory")
	segmentPolicy := flags.String("segment-policy", mousa.TextSegmentFixedV1, "ingestion segmentation: fixed-v1 or passage-v1")
	options := directoryFlags(flags)
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if *segmentPolicy != mousa.TextSegmentFixedV1 && *segmentPolicy != mousa.TextSegmentPassageV1 {
		return usageError{"segment policy must be fixed-v1 or passage-v1"}
	}
	rest := flags.Args()
	switch {
	case *sourceID == "" && len(rest) == 1:
		return runSyncDirectory(ctx, storePath, rest[0], *options, *segmentPolicy)
	case *sourceID != "" && len(rest) == 0:
		directoryOption := false
		flags.Visit(func(option *flag.Flag) {
			if option.Name != "source" && option.Name != "segment-policy" {
				directoryOption = true
			}
		})
		if directoryOption {
			return usageError{"directory selection flags cannot be used with --source"}
		}
		return runSyncJSONL(ctx, storePath, *sourceID, os.Stdin, *segmentPolicy)
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
	policy := flags.String("policy", "original", "query-term policy: original or dedup")
	budget := flags.Uint64("budget-bytes", 8<<10, "positive released-text byte budget")
	packing := flags.String("packing-policy", mousa.PackingOriginal, "packing policy: original or exact-v1")
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	if *policy != "original" && *policy != "dedup" {
		return usageError{"query policy must be original or dedup"}
	}
	if *packing != mousa.PackingOriginal && *packing != mousa.PackingExactV1 {
		return usageError{"packing policy must be original or exact-v1"}
	}
	if *budget == 0 {
		return usageError{"query byte budget must be positive"}
	}
	positional := flags.Args()
	if len(positional) == 0 {
		return usageError{"query requires a source and query text"}
	}
	source, label, err := selectSource(*sourceID, positional[:len(positional)-1], "query")
	if err != nil {
		return err
	}
	return runQuery(ctx, storePath, source, label, positional[len(positional)-1], *policy, *budget, *packing)
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
func applyItem(ctx context.Context, store *sqlite.Store, source mousa.Source, input itemInput, sequence *uint64, segmentPolicy string) (string, error) {
	if input.Deleted {
		return store.DeleteLocalItem(ctx, source.ID, input.ID)
	}
	digest := mousa.SHA256(sha256.Sum256(input.Content))
	current, err := store.GetLocalItem(ctx, source.ID, input.ID)
	if err != nil && !sqlite.IsCode(err, sqlite.CodeNotFound) {
		return "", err
	}
	if err == nil && current.Active && current.SegmentationPolicy == segmentPolicy && current.Artifact.ContentSHA256 == digest && current.Artifact.ByteLength == uint64(len(input.Content)) {
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
	representation, normalized, err := mousa.NormalizeUTF8TextWithPolicy(artifact, input.Content, segmentPolicy)
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

// runSyncDirectory applies the complete selected set. Scope exclusions deactivate
// old items just like removal; selection errors leave the store untouched.
func runSyncDirectory(ctx context.Context, storePath, root string, options directoryOptions, segmentPolicy string) error {
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
	if err := deployLocalPolicy(ctx, store); err != nil {
		return fmt.Errorf("deploy policy: %w", err)
	}

	result := syncResult{Source: absolute, Root: absolute, Input: inputDirectory, Skipped: selection.Skipped, SegmentPolicy: segmentPolicy}
	var sequence uint64
	selected := make(map[string]struct{}, len(selection.Items))
	for _, item := range selection.Items {
		selected[item.ID] = struct{}{}
		sequence++
		action, err := applyItem(ctx, store, source, itemInput{ID: item.ID, Content: item.content, Captured: item.captured}, &sequence, segmentPolicy)
		if err != nil {
			return fmt.Errorf("item %s: %w", item.ID, err)
		}
		result.record(action, item.ID)
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
		action, err := applyItem(ctx, store, source, itemInput{ID: path, Deleted: true}, &sequence, segmentPolicy)
		if err != nil {
			return fmt.Errorf("item %s: %w", path, err)
		}
		result.record(action, path)
	}
	if err := store.CompleteLocalRecovery(ctx, source.ID); err != nil {
		return err
	}
	result.TotalItems = len(selection.Items)
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
	Query             string                       `json:"query"`
	Source            string                       `json:"source"`
	QueryPolicy       string                       `json:"query_policy"`
	Expression        string                       `json:"expression"`
	RequestID         string                       `json:"request_id"`
	DecisionID        string                       `json:"decision_id"`
	DecisionOutcome   string                       `json:"decision_outcome"`
	DecisionReasons   []mousa.PolicyDecisionReason `json:"decision_reasons"`
	EvaluatedAtUsec   int64                        `json:"evaluated_at_usec"`
	TrailID           string                       `json:"trail_id"`
	PacketID          string                       `json:"packet_id"`
	Outcome           string                       `json:"outcome"`
	MatchedCandidates int                          `json:"matched_candidates"`
	CandidateLimit    int                          `json:"candidate_limit"`
	LifecycleExcluded int                          `json:"lifecycle_excluded"`
	BudgetOmitted     int                          `json:"budget_omitted"`
	DuplicateOmitted  *int                         `json:"duplicate_omitted,omitempty"`
	PackingPolicy     string                       `json:"packing_policy,omitempty"`
	UsedBytes         uint64                       `json:"used_bytes"`
	BudgetBytes       uint64                       `json:"budget_bytes"`
	Evidence          []evidenceHit                `json:"evidence"`
	LatencyMicros     int64                        `json:"latency_micros"`
}

type evidenceHit struct {
	Item                 string  `json:"item"`
	SegmentID            string  `json:"segment_id"`
	ContentSHA256        string  `json:"content_sha256"`
	RepresentationID     string  `json:"representation_id"`
	RepresentationSHA256 string  `json:"representation_sha256"`
	ByteStart            uint64  `json:"byte_start"`
	ByteEnd              uint64  `json:"byte_end"`
	SegmentPolicy        string  `json:"segment_policy"`
	Rank                 int     `json:"rank"`
	Score                float64 `json:"bm25_score"`
	ByteLength           int     `json:"byte_length"`
	Text                 string  `json:"text"`
}

// runQuery runs one authorized query against one resolved source.
func runQuery(ctx context.Context, storePath string, source mousa.Source, label, query, policy string, budgetBytes uint64, packingPolicy string) error {
	started := time.Now()
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := queryItems(ctx, store, source, query, policy, budgetBytes, packingPolicy)
	if err != nil {
		return err
	}
	result.Query = query
	result.Source = label
	result.LatencyMicros = time.Since(started).Microseconds()
	return emit(result)
}

func emit(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// deployLocalPolicy supplies the CLI caller's deployment-level allow. A source
// policy can deny retrieval, and an allow never overrides lifecycle withdrawal.
func deployLocalPolicy(ctx context.Context, store *sqlite.Store) error {
	definition, err := putLocalPolicyDefinition(ctx, store, mousa.SourceRetrievalEffectAllow)
	if err != nil {
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

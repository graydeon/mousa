// Command mousa is the supported local vertical slice: a UTF-8/Markdown
// directory is imported and synced into a canonical Mousa store, and queries
// return authorized, source-linked evidence bounded by a byte budget.
//
// Design contract (recorded in the project DECISIONS and this file's
// subcommands' docs):
//
//   - Source identity: one source per configured directory root; the Source
//     identity is the existing deterministic tuple (namespace "mousa-local",
//     external source ID = the root's absolute path). One Mousa item = one
//     file. Item identity is the item's relative POSIX path within the root;
//     the external observation ID is "item/<relpath>".
//   - Sync semantics: an item's content digest decides. Unchanged digest →
//     no-op (nothing stored). Changed digest → a new revision (a fresh
//     Observation/Artifact/Representation/Segments set) that becomes the
//     currently retrievable content for the item; the previous revision's
//     records stay as historical evidence but its segments are removed from
//     the lexical index, so only the current revision is retrievable. Deleted
//     file → the item's segments are removed from the index (and the source
//     row is left intact as evidence). Renamed file → the old item is
//     deleted and the new item is imported; matching content digests make
//     that a cheap no-content-change move.
//   - Historical vs current: the store is append-only for records; the
//     lexical index maps segment → current content. Queries return only
//     currently indexed segments, never stale text.
//   - Partial failure: ApplyIngest and PutSegment/IndexTextRepresentation are
//     per-item transactions; a failing item aborts the sync with a typed
//     error naming the item, already-committed items stay committed, and a
//     retry re-syncs only the remaining differences (idempotent by digest).
//   - Policy freshness: each query evaluates one stored retrieval decision
//     for its own request identity; the response reports the decision ID so
//     a historical decision can never pass as current authorization.
//   - Output: the response contains only released text from selected
//     candidates under the byte budget, plus provenance. Internal candidate
//     objects (rejected text, dispositions) are never serialized.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
		fmt.Fprintln(os.Stderr, `usage: mousa -store <path> <command> [args]

commands:
  sync <dir>            import or re-sync a UTF-8/Markdown directory (JSON result)
  query <dir> <text>    query a synced directory, returns bounded evidence (JSON)
  status <dir>          report the source's ingest state (JSON)`)
		os.Exit(2)
	}
	command, rest := args[0], args[1:]
	var err error
	switch command {
	case "sync":
		if len(rest) != 1 {
			fatalf("sync requires exactly one directory argument")
		}
		err = runSync(ctx, *storePath, rest[0])
	case "status":
		if len(rest) != 1 {
			fatalf("status requires exactly one directory argument")
		}
		err = runStatus(ctx, *storePath, rest[0])
	case "query":
		if len(rest) != 2 {
			fatalf("query requires <dir> <query text>")
		}
		err = runQuery(ctx, *storePath, rest[0], rest[1])
	default:
		fatalf("unknown command %q", command)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "mousa: %v\n", err)
		os.Exit(1)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "mousa: "+format+"\n", args...)
	os.Exit(2)
}

// localSource derives the deterministic Source identity for one directory
// root. The absolute path is the external identity, so moving the root
// creates a new source rather than silently mixing content.
func localSource(root string) (mousa.Source, string, error) {
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

func itemObservationID(source mousa.Source, relativePath string) (mousa.ObservationID, error) {
	return mousa.NewObservationID(source.ID, itemPrefix+relativePath)
}

// itemRevisionObservationID derives the observation identity of a specific
// revision: the item path plus its content digest. A changed file therefore
// gets a fresh Observation (and fresh Artifact/Representation/Segments), while
// re-ingesting the same content stays an exact receipt-identical retry.
func itemRevisionObservationID(source mousa.Source, relativePath string, digest mousa.SHA256) (mousa.ObservationID, error) {
	return mousa.NewObservationID(source.ID, fmt.Sprintf("%s%s@%x", itemPrefix, relativePath, digest))
}

// itemRevision is one stored revision of one item.
type itemRevision struct {
	ObservationID    mousa.ObservationID
	ArtifactID       mousa.ArtifactID
	RepresentationID mousa.RepresentationID
	ContentSHA256    mousa.SHA256
	ByteLength       uint64
	SegmentIDs       []mousa.SegmentID
}

func ingestItem(ctx context.Context, store *sqlite.Store, source mousa.Source, relativePath, absolutePath string, sequence uint64) (itemRevision, error) {
	info, err := os.Stat(absolutePath)
	if err != nil {
		return itemRevision{}, err
	}
	// Captured time is the file's modification time, so re-ingesting the same
	// content is receipt-identical (an exact retry) and a changed file gets a
	// fresh capture timestamp: the receipt binds captured time, so wall-clock
	// now() would make every replay of one revision a conflict.
	capturedAtUsec := info.ModTime().UnixMicro()
	if capturedAtUsec <= 0 {
		capturedAtUsec = time.Now().UnixMicro()
	}
	content, err := os.ReadFile(absolutePath)
	if err != nil {
		return itemRevision{}, err
	}
	revisionDigest := mousa.SHA256(sha256.Sum256(content))
	observationID, err := itemRevisionObservationID(source, relativePath, revisionDigest)
	if err != nil {
		return itemRevision{}, err
	}
	artifactID, err := mousa.NewArtifactID(observationID, segmentKeyPrefix)
	if err != nil {
		return itemRevision{}, err
	}
	artifact := mousa.Artifact{
		Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: segmentKeyPrefix,
		MediaType: mousa.UTF8TextMediaType, ContentSHA256: revisionDigest, ByteLength: uint64(len(content)),
	}
	observation := mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: source.ID, ExternalObservationID: fmt.Sprintf("%s%s@%x", itemPrefix, relativePath, revisionDigest)}
	batch := mousa.IngestBatch{
		AdapterID: adapterID, AdapterVersion: adapterVersion, Initiative: mousa.InitiativePush, Form: mousa.FormItem,
		CapturedAtUsec: capturedAtUsec, Sequence: &sequence,
		Source: source, Observation: observation, Artifacts: []mousa.Artifact{artifact},
	}
	if err := store.ApplyIngest(ctx, batch); err != nil {
		return itemRevision{}, err
	}
	representation, normalized, err := mousa.NormalizeUTF8Text(artifact, content)
	if err != nil {
		return itemRevision{}, err
	}
	segments, err := mousa.SegmentUTF8Text(representation, normalized)
	if err != nil {
		return itemRevision{}, err
	}
	if err := store.PutRepresentation(ctx, representation); err != nil {
		return itemRevision{}, err
	}
	revision := itemRevision{
		ObservationID:    observationID,
		ArtifactID:       artifactID,
		RepresentationID: representation.ID,
		ContentSHA256:    artifact.ContentSHA256,
		ByteLength:       artifact.ByteLength,
	}
	for _, segment := range segments {
		if err := store.PutSegment(ctx, segment); err != nil {
			return itemRevision{}, err
		}
		revision.SegmentIDs = append(revision.SegmentIDs, segment.ID)
	}
	if err := store.IndexTextRepresentation(ctx, representation.ID, normalized); err != nil {
		return itemRevision{}, err
	}
	return revision, nil
}

// storedRevision rebuilds the stored revision descriptor for an item from the
// store, or returns a nil revision when the item has never been ingested.
func storedRevision(ctx context.Context, store *sqlite.Store, source mousa.Source, relativePath string) (*itemRevision, error) {
	// The latest stored revision of this item: observations are ordered and
	// the last one for the item prefix is the newest revision.
	ids, err := store.SourceObservationIDs(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	prefix := itemPrefix + relativePath + "@"
	var latest *mousa.ObservationID
	for _, id := range ids {
		observation, err := store.GetObservation(ctx, id)
		if err != nil {
			return nil, err
		}
		external := observation.ExternalObservationID
		if external == itemPrefix+relativePath || strings.HasPrefix(external, prefix) {
			latest = &id
		}
	}
	if latest == nil {
		return nil, nil
	}
	observationID := *latest
	observation, err := store.GetObservation(ctx, observationID)
	if err != nil {
		if sqlite.IsCode(err, sqlite.CodeNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if observation.SourceID != source.ID {
		return nil, fmt.Errorf("observation %x does not belong to source", observationID[:8])
	}
	artifactID, err := mousa.NewArtifactID(observationID, segmentKeyPrefix)
	if err != nil {
		return nil, err
	}
	artifact, err := store.GetArtifact(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	representation, err := store.GetRepresentation(ctx, deriveRepresentationID(artifact))
	if err != nil {
		return nil, err
	}
	segments, err := store.RepresentationSegments(ctx, representation.ID)
	if err != nil {
		return nil, err
	}
	revision := &itemRevision{
		ObservationID:    observationID,
		ArtifactID:       artifactID,
		RepresentationID: representation.ID,
		ContentSHA256:    artifact.ContentSHA256,
		ByteLength:       artifact.ByteLength,
	}
	for _, segment := range segments {
		revision.SegmentIDs = append(revision.SegmentIDs, segment.ID)
	}
	return revision, nil
}

// deriveRepresentationID recomputes a representation identity from its stored
// artifact the way NormalizeUTF8Text derives it, so a stored revision can be
// located without a parallel index.
func deriveRepresentationID(artifact mousa.Artifact) mousa.RepresentationID {
	parameters := mousa.SHA256(sha256.Sum256(nil))
	inputs := []mousa.DerivationInput{mousa.NewArtifactDerivationInput(artifact.ID)}
	id, err := mousa.NewRepresentationID(inputs, mousa.UTF8TextProcessorID, mousa.UTF8TextProcessorVersion, parameters, mousa.UTF8TextMediaType, artifact.ContentSHA256)
	if err != nil {
		return mousa.RepresentationID{}
	}
	return id
}

func fileDigest(path string) (mousa.SHA256, uint64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return mousa.SHA256{}, 0, err
	}
	return mousa.SHA256(sha256.Sum256(content)), uint64(len(content)), nil
}

// runSync imports or re-syncs one directory. The per-item decision is digest
// driven: only changed or new files are ingested; deleted items are
// de-indexed. The result is machine-readable and reports every action.
type syncResult struct {
	Source      string   `json:"source"`
	Root        string   `json:"root"`
	Added       []string `json:"added,omitempty"`
	Updated     []string `json:"updated,omitempty"`
	Unchanged   []string `json:"unchanged,omitempty"`
	Deleted     []string `json:"deleted,omitempty"`
	Skipped     []string `json:"skipped,omitempty"`
	TotalItems  int      `json:"total_items"`
	StoreBytes  int64    `json:"store_bytes"`
	ElapsedSecs float64  `json:"elapsed_seconds"`
}

func runSync(ctx context.Context, storePath, root string) error {
	started := time.Now()
	source, absolute, err := localSource(root)
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

	result := syncResult{Source: source.ExternalSourceID, Root: absolute, Skipped: skipped}
	var sequence uint64
	paths := make([]string, 0, len(items))
	for path := range items {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		sequence++
		absolutePath := items[path]
		stored, err := storedRevision(ctx, store, source, path)
		if err != nil {
			return fmt.Errorf("item %s: %w", path, err)
		}
		digest, length, err := fileDigest(absolutePath)
		if err != nil {
			return fmt.Errorf("item %s: %w", path, err)
		}
		if stored != nil && stored.ContentSHA256 == digest && stored.ByteLength == length {
			result.Unchanged = append(result.Unchanged, path)
			continue
		}
		if _, err := ingestItem(ctx, store, source, path, absolutePath, sequence); err != nil {
			return fmt.Errorf("item %s: %w", path, err)
		}
		if stored == nil {
			result.Added = append(result.Added, path)
		} else {
			if err := deindexRevision(ctx, store, stored); err != nil {
				return fmt.Errorf("item %s: %w", path, err)
			}
			result.Updated = append(result.Updated, path)
		}
	}
	// Deletions: items stored under this source whose file no longer exists.
	previous, err := sourceItems(ctx, store, source)
	if err != nil {
		return err
	}
	for _, path := range previous {
		if _, exists := items[path]; !exists {
			stored, err := storedRevision(ctx, store, source, path)
			if err != nil {
				return fmt.Errorf("item %s: %w", path, err)
			}
			if stored == nil {
				continue
			}
			if err := deindexRevision(ctx, store, stored); err != nil {
				return fmt.Errorf("item %s: %w", path, err)
			}
			result.Deleted = append(result.Deleted, path)
		}
	}
	result.TotalItems = len(items)
	if info, err := os.Stat(storePath); err == nil {
		result.StoreBytes = info.Size()
	}
	result.ElapsedSecs = time.Since(started).Seconds()
	return emit(result)
}

// deindexRevision removes one revision's segments from the lexical index so
// the revision stops being retrievable while its canonical records remain as
// historical evidence.
func deindexRevision(ctx context.Context, store *sqlite.Store, revision *itemRevision) error {
	for _, segmentID := range revision.SegmentIDs {
		if err := store.RemoveSegmentFromIndex(ctx, segmentID); err != nil {
			return err
		}
	}
	return nil
}

// sourceItems lists the relative item paths currently stored for one source
// by reading its observations through the existing verified read path.
func sourceItems(ctx context.Context, store *sqlite.Store, source mousa.Source) ([]string, error) {
	ids, err := store.SourceObservationIDs(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(ids))
	for _, id := range ids {
		observation, err := store.GetObservation(ctx, id)
		if err != nil {
			return nil, err
		}
		external := observation.ExternalObservationID
		if !strings.HasPrefix(external, itemPrefix) {
			continue
		}
		// Strip the revision suffix: "item/<path>@<digest>" -> "<path>".
		relative := strings.TrimPrefix(external, itemPrefix)
		if index := strings.Index(relative, "@"); index >= 0 {
			relative = relative[:index]
		}
		paths = append(paths, relative)
	}
	return paths, nil
}

type statusResult struct {
	Source          string `json:"source"`
	CollectionState string `json:"collection_state"`
	Observations    int    `json:"observations"`
}

func runStatus(ctx context.Context, storePath, root string) error {
	source, absolute, err := localSource(root)
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
		if sqlite.IsCode(err, sqlite.CodeNotFound) {
			return emit(statusResult{Source: absolute, CollectionState: "absent"})
		}
		return err
	}
	items, err := sourceItems(ctx, store, source)
	if err != nil {
		return err
	}
	return emit(statusResult{Source: absolute, CollectionState: string(state.CollectionState), Observations: len(items)})
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

func runQuery(ctx context.Context, storePath, root, query string) error {
	started := time.Now()
	source, absolute, err := localSource(root)
	if err != nil {
		return err
	}
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
	result.Source = absolute
	result.BudgetBytes = budgetBytes
	result.LatencyMicros = time.Since(started).Microseconds()
	return emit(result)
}

func emit(value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
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

package beir

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

// IngestedCorpus is the result of loading one dataset into a Mousa store: one
// Source per dataset, one Observation/Artifact/Representation per document, and
// canonical text segments indexed into FTS5.
type IngestedCorpus struct {
	Store              *sqlite.Store
	Path               string
	Source             mousa.Source
	SegmentToDoc       map[mousa.SegmentID]string
	DocRepresentations map[mousa.RepresentationID]string
	IndexBytes         int64
	IndexingSeconds    float64
}

type document struct {
	corpusID       string
	artifact       mousa.Artifact
	representation mousa.Representation
	content        []byte
}

// IngestCorpus loads every corpus document through the canonical engine and
// returns the ingested corpus plus the segment→document mapping used for
// document-level ranking.
// ProgressFunc receives periodic ingestion progress: documents done, total, and
// elapsed seconds. It may be nil.
type ProgressFunc func(done, total int, elapsedSeconds float64)

// IngestProgress is the package-level progress reporter used by IngestCorpus.
// Assigning a callback before calling IngestCorpus enables reporting; the
// default is no reporting.
var IngestProgress ProgressFunc

func IngestCorpus(ctx context.Context, dataset *Dataset, storePath string) (*IngestedCorpus, error) {
	if err := os.MkdirAll(filepath.Dir(storePath), 0o755); err != nil {
		return nil, err
	}
	_ = os.Remove(storePath)
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(storePath + suffix)
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	ingested := &IngestedCorpus{
		Store:              store,
		Path:               storePath,
		SegmentToDoc:       map[mousa.SegmentID]string{},
		DocRepresentations: map[mousa.RepresentationID]string{},
	}
	sourceID, err := mousa.NewSourceID("beir-eval", dataset.Name)
	if err != nil {
		store.Close()
		return nil, err
	}
	ingested.Source = mousa.Source{Schema: mousa.SourceSchema, ID: sourceID, Namespace: "beir-eval", ExternalSourceID: dataset.Name}

	corpusIDs := make([]string, 0, len(dataset.Corpus))
	for id := range dataset.Corpus {
		corpusIDs = append(corpusIDs, id)
	}
	sort.Strings(corpusIDs)

	started := time.Now()
	total := len(corpusIDs)
	nextReport := 0
	for index, corpusID := range corpusIDs {
		if err := ingested.ingestDocument(ctx, corpusID, dataset.Corpus[corpusID], index); err != nil {
			store.Close()
			return nil, fmt.Errorf("ingest %s: %w", corpusID, err)
		}
		if IngestProgress != nil && (index+1 >= total || index+1 >= nextReport) {
			IngestProgress(index+1, total, time.Since(started).Seconds())
			nextReport = index + 1 + total/20
		}
	}
	ingested.IndexingSeconds = time.Since(started).Seconds()
	if err := deployHarnessPolicy(ctx, ingested.Store); err != nil {
		store.Close()
		return nil, fmt.Errorf("deploy policy: %w", err)
	}
	info, err := os.Stat(storePath)
	if err == nil {
		ingested.IndexBytes = info.Size()
	}
	return ingested, nil
}

func (ingested *IngestedCorpus) ingestDocument(ctx context.Context, corpusID, text string, index int) error {
	externalObservationID := fmt.Sprintf("doc-%d", index)
	observationID, err := mousa.NewObservationID(ingested.Source.ID, externalObservationID)
	if err != nil {
		return err
	}
	observation := mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: ingested.Source.ID, ExternalObservationID: externalObservationID}
	artifactID, err := mousa.NewArtifactID(observationID, "body")
	if err != nil {
		return err
	}
	content := []byte(text)
	artifact := mousa.Artifact{
		Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: "body",
		MediaType: mousa.UTF8TextMediaType, ContentSHA256: mousa.SHA256(sha256.Sum256(content)), ByteLength: uint64(len(content)),
	}
	sequence := uint64(index + 1)
	batch := mousa.IngestBatch{
		AdapterID: "beir-eval", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem,
		CapturedAtUsec: int64(index + 1), Sequence: &sequence,
		Source: ingested.Source, Observation: observation, Artifacts: []mousa.Artifact{artifact},
	}
	if err := ingested.Store.ApplyIngest(ctx, batch); err != nil {
		return err
	}
	representation, normalized, err := mousa.NormalizeUTF8Text(artifact, content)
	if err != nil {
		return err
	}
	segments, err := mousa.SegmentUTF8Text(representation, normalized)
	if err != nil {
		return err
	}
	if err := ingested.Store.PutRepresentation(ctx, representation); err != nil {
		return err
	}
	for _, segment := range segments {
		if err := ingested.Store.PutSegment(ctx, segment); err != nil {
			return err
		}
		ingested.SegmentToDoc[segment.ID] = corpusID
	}
	if err := ingested.Store.IndexTextRepresentation(ctx, representation.ID, normalized); err != nil {
		return err
	}
	ingested.DocRepresentations[representation.ID] = corpusID
	return nil
}

// Close releases the underlying store.
func (ingested *IngestedCorpus) Close() error {
	if ingested.Store == nil {
		return nil
	}
	return ingested.Store.Close()
}

// SearchResult is one ranked query result with its measured latency. Traced
// runs additionally record the packet selection the engine packed: the
// documents with at least one selected candidate (in rank order) and the
// selection's used byte total. Non-traced runs leave both empty because no
// packet exists to account for.
type SearchResult struct {
	QueryID            string
	RankedDocIDs       []string
	SelectedDocIDs     []string
	UsedBytes          uint64
	LatencyMicros      int64
	AcceptedSegments   int
	ConsideredSegments int
}

// SearchMode selects which verified retrieval path the harness exercises.
type SearchMode string

const (
	// ModeVerified runs SearchVerifiedLexical (raw BM25 + ancestry + lifecycle verification).
	ModeVerified SearchMode = "verified"
	// ModeEnforced runs SearchEnforcedLexical (verified + one stored allow decision per query).
	ModeEnforced SearchMode = "enforced"
	// ModeTraced runs TraceEnforcedLexical (enforced + trail and packet records).
	ModeTraced SearchMode = "traced"
)

// RunConfig identifies one harness experiment. Enforced and traced modes
// evaluate one policy request per query, and the request identity is a pure
// function of its fields, so a run must own a request namespace: two runs with
// different mode, limit, or budget on a reused store would otherwise collide on
// stored request/decision identities, while retries inside one run must keep
// the same identity and stay idempotent. The namespace is derived
// deterministically from the configuration, so re-running the same experiment
// reuses the same namespace.
type RunConfig struct {
	Mode    SearchMode
	Limit   int
	Budget  uint64           // traced-mode pack budget in bytes; ignored by other modes
	Policy  ExpressionPolicy // term folding policy; PolicyOriginal by default
	Dataset string
	Corpus  int
	Judged  int
}

// Namespace returns the request-identity prefix for this run configuration.
func (config RunConfig) Namespace() string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("mousa-beir-run\x00%s\x00%s\x00%d\x00%d\x00%s\x00%d\x00%d",
		config.Dataset, string(config.Mode), config.Limit, config.Budget, string(config.Policy),
		config.Corpus, config.Judged)))
	return fmt.Sprintf("run-%x", digest[:6])
}

// SearchProgress receives per-query progress: queries done, total, and the last
// query ID. It may be nil.
var SearchProgress func(done, total int, lastQueryID string)

// QueryTimeout bounds each query's wall time. Zero disables the bound.
var QueryTimeout time.Duration

// SearchAll runs every judged query through the configured run and returns
// ranked document results in query file order. Results are deterministic for a
// fixed store and query order. Each query runs under QueryTimeout when set.
// Queries whose IDs appear in skip are not re-run; their recorded results are
// appended in file order so resume preserves the original ordering. Journal,
// when non-nil, receives each result as it completes so an interrupted run can
// resume from the completed prefix.
func SearchAll(ctx context.Context, ingested *IngestedCorpus, dataset *Dataset, config RunConfig, skip map[string]SearchResult, journal func(SearchResult)) ([]SearchResult, error) {
	if config.Limit < 1 || config.Limit > 100 {
		return nil, fmt.Errorf("run limit %d is out of bounds", config.Limit)
	}
	requests, err := evaluateRequests(ctx, ingested, dataset, config)
	if err != nil {
		return nil, err
	}
	judged := dataset.JudgedQueries()
	results := make([]SearchResult, 0, len(judged))
	for done, queryID := range judged {
		if skipped, resumable := skip[queryID]; resumable {
			results = append(results, skipped)
			continue
		}
		expression, err := BuildExpressionWithPolicy(dataset.Queries[queryID], config.Policy)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", queryID, err)
		}
		queryCtx := ctx
		cancel := func() {}
		if QueryTimeout > 0 {
			queryCtx, cancel = context.WithTimeout(ctx, QueryTimeout)
		}
		started := time.Now()
		var candidates []mousa.VerifiedLexicalCandidate
		var trail mousa.SourceTrail
		switch config.Mode {
		case ModeVerified:
			candidates, err = ingested.Store.SearchVerifiedLexical(queryCtx, expression, config.Limit)
		case ModeEnforced:
			var enforced mousa.EnforcedLexicalResult
			enforced, err = ingested.Store.SearchEnforcedLexical(queryCtx, requests[queryID], expression, config.Limit)
			candidates = enforced.Candidates
		case ModeTraced:
			var traced sqlite.TracedLexicalResult
			traced, err = ingested.Store.TraceEnforcedLexical(queryCtx, requests[queryID], expression, config.Limit, config.Budget)
			candidates = traced.Candidates
			trail = traced.Trail
		default:
			cancel()
			return nil, fmt.Errorf("unknown mode %q", config.Mode)
		}
		latency := time.Since(started)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("search %s (%s): %w", queryID, expression, err)
		}
		result := SearchResult{
			QueryID:       queryID,
			LatencyMicros: latency.Microseconds(),
		}
		seen := map[string]struct{}{}
		for _, candidate := range candidates {
			if candidate.Disposition != mousa.CandidateAccepted {
				continue
			}
			docID, ok := ingested.SegmentToDoc[candidate.Segment.ID]
			if !ok {
				return nil, fmt.Errorf("segment %x has no document mapping", candidate.Segment.ID)
			}
			if _, duplicate := seen[docID]; duplicate {
				continue
			}
			seen[docID] = struct{}{}
			result.RankedDocIDs = append(result.RankedDocIDs, docID)
		}
		// The trail's ordered candidate rows are the engine's own selection; the
		// harness reads it instead of re-deriving packing from byte counts.
		selectedSeen := map[string]struct{}{}
		for _, candidate := range trail.Candidates {
			if !candidate.Selected {
				continue
			}
			docID, ok := ingested.SegmentToDoc[candidate.SegmentID]
			if !ok {
				return nil, fmt.Errorf("segment %x has no document mapping", candidate.SegmentID)
			}
			if _, duplicate := selectedSeen[docID]; duplicate {
				continue
			}
			selectedSeen[docID] = struct{}{}
			result.SelectedDocIDs = append(result.SelectedDocIDs, docID)
		}
		result.UsedBytes = trail.UsedBytes
		if journal != nil {
			journal(result)
		}
		result.AcceptedSegments = len(result.RankedDocIDs)
		result.ConsideredSegments = len(candidates)
		results = append(results, result)
		if SearchProgress != nil {
			SearchProgress(done+1, len(judged), queryID)
		}
	}
	return results, nil
}

func evaluateRequests(ctx context.Context, ingested *IngestedCorpus, dataset *Dataset, config RunConfig) (map[string]mousa.PolicyEvaluationRequest, error) {
	requests := map[string]mousa.PolicyEvaluationRequest{}
	if config.Mode == ModeVerified {
		return requests, nil
	}
	namespace := config.Namespace()
	for index, queryID := range dataset.JudgedQueries() {
		request, err := newEvaluationRequest(ingested.Source.ID, fmt.Sprintf("%s-query-%s-%d", namespace, queryID, index))
		if err != nil {
			return nil, err
		}
		if _, err := ingested.Store.EvaluateSourceRetrieval(ctx, request); err != nil {
			return nil, fmt.Errorf("evaluate request for %s: %w", queryID, err)
		}
		requests[queryID] = request
	}
	return requests, nil
}

func newEvaluationRequest(sourceID mousa.SourceID, externalRequestID string) (mousa.PolicyEvaluationRequest, error) {
	request := mousa.PolicyEvaluationRequest{
		Schema:            mousa.PolicyEvaluationRequestSchema,
		Action:            mousa.SourceRetrievalAction,
		CallerNamespace:   "beir-eval.caller",
		ExternalCallerID:  "harness",
		ExternalRequestID: externalRequestID,
		PurposeNamespace:  "beir-eval.purpose",
		ExternalPurposeID: "ranking",
		SourceID:          sourceID,
		RequestedAtUsec:   time.Now().UnixMicro(),
	}
	id, err := mousa.NewPolicyEvaluationRequestID(request)
	if err != nil {
		return mousa.PolicyEvaluationRequest{}, err
	}
	request.ID = id
	return request, nil
}

// maxExpressionBytes is the store's query expression bound: validateLexicalQuery
// rejects expressions over 4096 bytes.
const maxExpressionBytes = 4096

// ExpressionPolicy controls term folding in BuildExpression.
//
// PolicyOriginal keeps every query term, duplicating repeated terms. BM25
// scores sum per matched term, so repetition multiplies a duplicated term's
// contribution: on ArguAna 38.5% of query terms are duplicates and nearly all
// of them are stopwords, which skews scores and dominates FTS5 cost (60.6% of
// search CPU in BM25 instance counting).
//
// PolicyDedup folds each repeated term to one instance. This is a retrieval-
// policy change, not a semantics-preserving optimization: on 50 real ArguAna
// queries the deduplicated top-10 differed for every query, while average query
// latency fell ~5x (1.15 s -> 0.23 s). Adoption is a measured quality/latency
// tradeoff, recorded in docs/RESEARCH.md, not a correctness fix.
type ExpressionPolicy string

const (
	// PolicyOriginal is the published baseline evaluation protocol.
	PolicyOriginal ExpressionPolicy = "original"
	// PolicyDedup folds repeated terms to one instance each.
	PolicyDedup ExpressionPolicy = "dedup"
)

// BuildExpression converts one natural-language query into the FTS5 MATCH
// expression the store accepts: every alphanumeric term is lowercased, quoted,
// and OR-joined so that BM25 ranking orders documents by term evidence. When
// the joined expression exceeds the store's expression bound, trailing terms
// are dropped (earliest terms first, which preserves leading query wording)
// until it fits. The policy decides whether repeated terms are folded.
func BuildExpressionWithPolicy(query string, policy ExpressionPolicy) (string, error) {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		isLetter := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		isLetter = isLetter || r >= 0x80
		return !isLetter
	})
	terms := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		term := strings.ToLower(strings.TrimSpace(field))
		if term == "" {
			continue
		}
		if policy == PolicyDedup {
			if _, duplicate := seen[term]; duplicate {
				continue
			}
			seen[term] = struct{}{}
		}
		terms = append(terms, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	if len(terms) == 0 {
		return "", fmt.Errorf("query produced no searchable terms")
	}
	expr := strings.Join(terms, " OR ")
	for len(expr) > maxExpressionBytes && len(terms) > 1 {
		terms = terms[:len(terms)-1]
		expr = strings.Join(terms, " OR ")
	}
	return expr, nil
}

// BuildExpression applies the published PolicyOriginal protocol.
func BuildExpression(query string) (string, error) {
	return BuildExpressionWithPolicy(query, PolicyOriginal)
}

// packBudgetBytes is the explicit byte budget the traced mode packs candidates
// into. It is generous enough to pack every accepted candidate for most BEIR
// queries; smaller budgets are exercised by the ablation runs.
const packBudgetBytes = 1 << 20

// deployHarnessPolicy stores the deployment-scoped allow policy the enforced and
// traced modes evaluate against: caller beir-eval.caller/harness with purpose
// beir-eval.purpose/ranking may retrieve any source.
func deployHarnessPolicy(ctx context.Context, store *sqlite.Store) error {
	inner := mousa.SourceRetrievalPolicy{
		Schema:            mousa.SourceRetrievalPolicySchema,
		Action:            mousa.SourceRetrievalAction,
		CallerNamespace:   "beir-eval.caller",
		ExternalCallerID:  "harness",
		PurposeNamespace:  "beir-eval.purpose",
		ExternalPurposeID: "ranking",
		Effect:            mousa.SourceRetrievalEffectAllow,
	}
	data, err := mousa.EncodeSourceRetrievalPolicy(inner)
	if err != nil {
		return err
	}
	digest := mousa.SHA256(sha256.Sum256(data))
	definitionID, err := mousa.NewPolicyDefinitionID("beir-eval", "allow", "1", mousa.SourceRetrievalPolicyMediaType, mousa.SourceRetrievalPolicySchema, digest)
	if err != nil {
		return err
	}
	definition := mousa.PolicyDefinition{
		Schema: mousa.PolicyDefinitionSchema, ID: definitionID, Namespace: "beir-eval",
		ExternalPolicyID: "allow", ExternalPolicyVersion: "1",
		DefinitionMediaType: mousa.SourceRetrievalPolicyMediaType, DefinitionSchema: mousa.SourceRetrievalPolicySchema,
		DefinitionSHA256: digest, Definition: string(data),
	}
	if err := store.PutPolicyDefinition(ctx, definition); err != nil {
		return err
	}
	scope := mousa.NewDeploymentPolicyScope()
	bindingID, err := mousa.NewPolicyBindingID("beir-eval", "deployment", "1", scope, definition.ID)
	if err != nil {
		return err
	}
	binding := mousa.PolicyBinding{
		Schema: mousa.PolicyBindingSchema, ID: bindingID, Namespace: "beir-eval",
		ExternalBindingID: "deployment", ExternalBindingVersion: "1", Scope: scope, PolicyDefinitionID: definition.ID,
	}
	if err := store.PutPolicyBinding(ctx, binding); err != nil {
		return err
	}
	activationID, err := mousa.NewPolicyActivationID("beir-eval", "deployment", "activate")
	if err != nil {
		return err
	}
	activation := mousa.PolicyActivation{
		Schema: mousa.PolicyActivationSchema, ID: activationID, Namespace: "beir-eval",
		ExternalBindingID: "deployment", ExternalActivationID: "activate",
		ActiveBindingID: &binding.ID, ActorID: "beir-eval", ActorVersion: "1", OccurredAtUsec: 1,
	}
	return store.ApplyPolicyActivation(ctx, activation)
}

// OpenExisting opens an already-indexed store and rebuilds the segment→document
// mapping from the dataset, so search-only reruns skip reindexing.
func OpenExisting(ctx context.Context, dataset *Dataset, storePath string) (*IngestedCorpus, error) {
	// Opened writable: enforced and traced modes must evaluate one policy request
	// per query, which writes decision records. Verified mode performs no writes.
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return nil, err
	}
	ingested := &IngestedCorpus{
		Store:              store,
		Path:               storePath,
		SegmentToDoc:       map[mousa.SegmentID]string{},
		DocRepresentations: map[mousa.RepresentationID]string{},
	}
	// The store's Source identity was fixed at ingest time from the original
	// dataset name. Reuse therefore requires this run's dataset name to match
	// the one that indexed the store; a mismatch produces no mapped segments and
	// is rejected below instead of silently mis-ranking.
	sourceID, err := mousa.NewSourceID("beir-eval", dataset.Name)
	if err != nil {
		store.Close()
		return nil, err
	}
	ingested.Source = mousa.Source{Schema: mousa.SourceSchema, ID: sourceID, Namespace: "beir-eval", ExternalSourceID: dataset.Name}
	corpusIDs := make([]string, 0, len(dataset.Corpus))
	for id := range dataset.Corpus {
		corpusIDs = append(corpusIDs, id)
	}
	sort.Strings(corpusIDs)
	if err := ingested.rebuildSegmentMapping(ctx, corpusIDs, dataset); err != nil {
		store.Close()
		return nil, err
	}
	if len(ingested.SegmentToDoc) == 0 {
		store.Close()
		return nil, fmt.Errorf("reuse validation failed: no corpus segments match store %s (dataset %q did not index this store)", storePath, dataset.Name)
	}
	// The Source identity must also verify: a store indexed under a different
	// dataset name shares no derived IDs with this run.
	if _, err := store.GetSource(ctx, ingested.Source.ID); err != nil {
		store.Close()
		return nil, fmt.Errorf("reuse validation failed: store %s was not indexed as dataset %q: %w", storePath, dataset.Name, err)
	}
	return ingested, nil
}

// rebuildSegmentMapping reconstructs the segment→document mapping for an
// existing store by recomputing the deterministic identities from the corpus
// text with the same pure functions IngestCorpus used. It touches no product
// API beyond those pure functions and no SQL.
func (ingested *IngestedCorpus) rebuildSegmentMapping(ctx context.Context, corpusIDs []string, dataset *Dataset) error {
	for index, corpusID := range corpusIDs {
		content := []byte(dataset.Corpus[corpusID])
		externalObservationID := fmt.Sprintf("doc-%d", index)
		observationID, err := mousa.NewObservationID(ingested.Source.ID, externalObservationID)
		if err != nil {
			return err
		}
		artifactID, err := mousa.NewArtifactID(observationID, "body")
		if err != nil {
			return err
		}
		artifact := mousa.Artifact{
			Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: "body",
			MediaType: mousa.UTF8TextMediaType, ContentSHA256: mousa.SHA256(sha256.Sum256(content)), ByteLength: uint64(len(content)),
		}
		representation, normalized, err := mousa.NormalizeUTF8Text(artifact, content)
		if err != nil {
			return err
		}
		segments, err := mousa.SegmentUTF8Text(representation, normalized)
		if err != nil {
			return err
		}
		for _, segment := range segments {
			ingested.SegmentToDoc[segment.ID] = corpusID
		}
		ingested.DocRepresentations[representation.ID] = corpusID
	}
	return nil
}

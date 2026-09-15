package beir

import (
	"context"
	"crypto/sha256"
	"errors"
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
	QueryID        string
	RankedDocIDs   []string
	SelectedDocIDs []string
	UsedBytes      uint64
	// LatencyMicros is the full request path: preprocessing (expression
	// build, term probes, reduction) plus the store call. SearchMicros and
	// PreprocessMicros carry the component split. Historical search-only
	// measurements correspond to SearchMicros, never to LatencyMicros.
	LatencyMicros    int64
	SearchMicros     int64
	PreprocessMicros int64
	// AcceptedSegments and ConsideredSegments are per-attempt segment counts for
	// the query that produced this result. AcceptedSegments currently counts the
	// accepted ranked documents' best segments (one per document), not every
	// accepted segment; the name predates that unit and is kept for report
	// compatibility.
	AcceptedSegments   int
	ConsideredSegments int
	// DroppedTerms and ExpressionBytes account for the expression the engine
	// evaluated: how many terms the drop-floor policy removed from the
	// published protocol's expression and its final byte size. Other policies
	// leave DroppedTerms at 0 and ExpressionBytes at the built expression's
	// length.
	DroppedTerms    int
	ExpressionBytes int
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
	Mode       SearchMode
	Limit      int
	Budget     uint64           // traced-mode pack budget in bytes; ignored by other modes
	Policy     ExpressionPolicy // term folding policy; PolicyOriginal by default
	Dataset    string
	Corpus     int
	Judged     int
	QueryLimit int // first N judged queries in file order; 0 = all
	// Content binds the experiment to the bytes it measured. Names and counts
	// are not identities: two corpora with the same name and document count can
	// differ in every document. CorpusSHA256, QueriesSHA256, and QrelsSHA256
	// are digests over the loaded corpus.jsonl, queries.jsonl, and qrels bytes;
	// a resume or reuse whose digests differ is a different experiment and must
	// be refused, not resumed.
	Content ContentIdentity
}

// ContentIdentity digests the actual dataset bytes one run measured. It is
// part of the run namespace and of every report and manifest.
type ContentIdentity struct {
	CorpusSHA256  string `json:"corpus_sha256"`
	QueriesSHA256 string `json:"queries_sha256"`
	QrelsSHA256   string `json:"qrels_sha256"`
}

func (identity ContentIdentity) empty() bool {
	return identity.CorpusSHA256 == "" && identity.QueriesSHA256 == "" && identity.QrelsSHA256 == ""
}

// Namespace returns the request-identity prefix for this run configuration.
// The content digests are part of the identity: a namespace names what was
// measured, and equal names with different bytes must never share one.
func (config RunConfig) Namespace() string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("mousa-beir-run\x00%s\x00%s\x00%d\x00%d\x00%s\x00%d\x00%d\x00%d\x00%s\x00%s\x00%s",
		config.Dataset, string(config.Mode), config.Limit, config.Budget, string(config.Policy),
		config.Corpus, config.Judged, config.QueryLimit,
		config.Content.CorpusSHA256, config.Content.QueriesSHA256, config.Content.QrelsSHA256)))
	return fmt.Sprintf("run-%x", digest[:6])
}

// ReductionReport is a drop-floor run's instrument accounting: how many
// queries had their expression reduced, how many term instances the probes
// classified, and how many the policy dropped. It is recorded outside the
// latency measurements because the probes are setup cost, not query cost.
type ReductionReport struct {
	Queries         int     `json:"queries"`
	QueriesReduced  int     `json:"queries_reduced"`
	TermsEvaluated  int     `json:"terms_evaluated"`
	TermsDropped    int     `json:"terms_dropped"`
	ProbeCount      int     `json:"probe_count"`
	ProbeSeconds    float64 `json:"probe_seconds"`
	IndexedRows     int     `json:"indexed_rows"`
	FloorThreshold  float64 `json:"floor_threshold"`
	PremiseVerified bool    `json:"premise_verified"`
	// FallbackQueries counts queries the policy could not reduce because
	// every term was at the floor; those queries ran the baseline expression.
	FallbackQueries int `json:"fallback_queries"`
}

// SearchProgress receives per-query progress: queries done, total, and the last
// query ID. It may be nil.
var SearchProgress func(done, total int, lastQueryID string)

// QueryTimeout bounds each query's wall time. Zero disables the bound.
var QueryTimeout time.Duration

// SearchAll runs the configured run's judged queries — every judged query, or
// the first config.QueryLimit of them in file order — and returns ranked
// document results in query file order. Results are deterministic for a fixed
// store and query order. Each query runs under QueryTimeout when set. Queries
// whose IDs appear in skip are not re-run; their recorded results are appended
// in file order so resume preserves the original ordering. Journal, when
// non-nil, receives each result as it completes so an interrupted run can
// resume from the completed prefix.
//
// Under PolicyDropFloor every query's expression is reduced before its timed
// search: an IndexProbe measures each distinct term once (cached), and the
// floor terms are dropped from the expression the engine evaluates. Probe time
// is instrumentation cost and is reported through SearchInstrumentation, never
// inside a query's measured latency.
var SearchInstrumentation func(report ReductionReport)

func SearchAll(ctx context.Context, ingested *IngestedCorpus, dataset *Dataset, config RunConfig, skip map[string]SearchResult, journal func(SearchResult)) ([]SearchResult, error) {
	if config.Limit < 1 || config.Limit > 100 {
		return nil, fmt.Errorf("run limit %d is out of bounds", config.Limit)
	}
	if config.QueryLimit < 0 {
		return nil, fmt.Errorf("run query limit %d is negative", config.QueryLimit)
	}
	requests, err := evaluateRequests(ctx, ingested, dataset, config)
	if err != nil {
		return nil, err
	}
	judged := judgedQueriesFor(config, dataset)
	var probe *IndexProbe
	var instrument ReductionReport
	if config.Policy == PolicyDropFloor {
		probe, err = OpenIndexProbe(ctx, ingested.Path)
		if err != nil {
			return nil, err
		}
		defer probe.Close()
		// The floor boundary must separate evidence on this index before the
		// first query runs; a contradiction refuses the run instead of
		// silently changing rankings.
		sample := make([]string, 0, 64)
		for _, queryID := range judged {
			sample = append(sample, TermList(dataset.Queries[queryID])...)
			if len(sample) >= 64 {
				break
			}
		}
		if err := VerifyFloorPremise(ctx, probe, sample); err != nil {
			return nil, fmt.Errorf("drop-floor policy refused: %w", err)
		}
	}
	if QueryTimeout < 0 {
		return nil, fmt.Errorf("query timeout %s is negative", QueryTimeout)
	}
	results := make([]SearchResult, 0, len(judged))
	for done, queryID := range judged {
		if skipped, resumable := skip[queryID]; resumable {
			results = append(results, skipped)
			continue
		}
		// Full-request timing and deadline: the measured latency and the
		// timeout cover everything the query needs before it can be served —
		// expression building, term-evidence probing, and reduction — not
		// only the store call. Component costs are also recorded separately
		// (PreprocessMicros vs the store's own share) so the breakdown stays
		// inspectable and older search-only numbers keep their label.
		queryCtx := ctx
		cancel := func() {}
		if QueryTimeout > 0 {
			queryCtx, cancel = context.WithTimeout(ctx, QueryTimeout)
		}
		requestStarted := time.Now()
		expression, err := BuildExpressionWithPolicy(dataset.Queries[queryID], config.Policy)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("query %s: %w", queryID, err)
		}
		droppedTerms, expressionBytes := 0, len(expression)
		if probe != nil {
			reduction, err := ReduceFloorTerms(dataset.Queries[queryID], NewReductionOracle(queryCtx, probe))
			if errors.Is(err, errNoEvidenceTerms) {
				// A query whose every term is at the floor has no
				// evidence-bearing term to rank by (e.g. a two-word
				// query whose words are both df-majority). The
				// baseline expression is the only sound evaluation,
				// so the policy degrades to it for that query and
				// the report accounts the fallback explicitly.
				instrument.Queries++
				instrument.FallbackQueries++
				instrument.TermsEvaluated += len(TermList(dataset.Queries[queryID]))
			} else if err != nil {
				cancel()
				return nil, fmt.Errorf("query %s: %w", queryID, err)
			} else {
				expression = reduction.Expression
				droppedTerms = len(reduction.Dropped)
				expressionBytes = len(expression)
				instrument.Queries++
				instrument.TermsEvaluated += len(reduction.Kept) + len(reduction.Dropped)
				instrument.TermsDropped += len(reduction.Dropped)
				if len(reduction.Dropped) > 0 {
					instrument.QueriesReduced++
				}
			}
		}
		preprocessMicros := time.Since(requestStarted).Microseconds()
		searchStarted := time.Now()
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
		searchMicros := time.Since(searchStarted)
		latency := time.Since(requestStarted)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return nil, fmt.Errorf("query %s exceeded its request deadline: %w", queryID, err)
			}
			return nil, fmt.Errorf("search %s (%s): %w", queryID, expression, err)
		}
		result := SearchResult{
			QueryID: queryID,
			// LatencyMicros is the full request path: preprocessing
			// (expression build + probes + reduction) plus the store call.
			// SearchMicros and PreprocessMicros carry the component split.
			LatencyMicros:    latency.Microseconds(),
			SearchMicros:     searchMicros.Microseconds(),
			PreprocessMicros: preprocessMicros,
		}
		result.DroppedTerms = droppedTerms
		result.ExpressionBytes = expressionBytes
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
		// Accounting is assigned before the journal callback so a resumed run's
		// journaled rows match an uninterrupted run's rows exactly (the
		// 2026-09-15 review found the callback recording 0/0 here).
		result.AcceptedSegments = len(result.RankedDocIDs)
		result.ConsideredSegments = len(candidates)
		if journal != nil {
			journal(result)
		}
		results = append(results, result)
		if SearchProgress != nil {
			SearchProgress(done+1, len(judged), queryID)
		}
	}
	if instrument.Queries > 0 {
		instrument.ProbeCount, instrument.ProbeSeconds = probe.Probes()
		instrument.IndexedRows = probe.Rows()
		instrument.FloorThreshold = floorMagnitudeLimit
		instrument.PremiseVerified = true
		if SearchInstrumentation != nil {
			SearchInstrumentation(instrument)
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
	for index, queryID := range judgedQueriesFor(config, dataset) {
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

// judgedQueriesFor returns the judged query IDs a run covers: every judged
// query, or the first config.QueryLimit in file order. SearchAll and request
// evaluation must agree on the slice, or a resumed run would evaluate
// requests for queries it never searches.
func judgedQueriesFor(config RunConfig, dataset *Dataset) []string {
	judged := dataset.JudgedQueries()
	if config.QueryLimit > 0 && config.QueryLimit < len(judged) {
		return judged[:config.QueryLimit]
	}
	return judged
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
	// PolicyOriginal is the published Phase 13 protocol.
	PolicyOriginal ExpressionPolicy = "original"
	// PolicyDedup folds repeated terms to one instance each.
	PolicyDedup ExpressionPolicy = "dedup"

	// PolicyDropFloor drops query terms whose measured BM25 evidence is at the
	// FTS5 inverse-document-frequency floor (ReduceFloorTerms). Terms whose
	// postings cover at least half the indexed rows score <= 2.2e-6 per
	// matched row there, so they cost a full postings scan per instance while
	// contributing almost no evidence. Dropping them is bounded-score-changing:
	// the kept terms are a subset of the baseline expression in the same
	// order; Phase 17 measured identical ranked output with a ~90% p50 win on
	// ArguAna. The published baseline stays PolicyOriginal.
	PolicyDropFloor ExpressionPolicy = "drop-floor"
)

// BuildExpression converts one natural-language query into the FTS5 MATCH
// expression the store accepts: every alphanumeric term is lowercased, quoted,
// and OR-joined so that BM25 ranking orders documents by term evidence. When
// the joined expression exceeds the store's expression bound, trailing terms
// are dropped (earliest terms first, which preserves leading query wording)
// until it fits. The policy decides whether repeated terms are folded.
func BuildExpressionWithPolicy(query string, policy ExpressionPolicy) (string, error) {
	terms := make([]string, 0, len(queryTerms(query)))
	seen := map[string]struct{}{}
	for _, term := range queryTerms(query) {
		if policy == PolicyDedup {
			if _, duplicate := seen[term]; duplicate {
				continue
			}
			seen[term] = struct{}{}
		}
		terms = append(terms, quoteTerm(term))
	}
	if len(terms) == 0 {
		return "", fmt.Errorf("query produced no searchable terms")
	}
	for len(strings.Join(terms, " OR ")) > maxExpressionBytes && len(terms) > 1 {
		terms = terms[:len(terms)-1]
	}
	return strings.Join(terms, " OR "), nil
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
//
// Reuse is validated against the store's actual indexed content, not against
// names and counts: for every corpus document the deterministic representation
// identity is recomputed from the text and the stored representation must exist
// with the same content digest and length. A dataset whose text changed under
// the same name and document count is a different experiment and is refused
// here instead of failing later (or mis-ranking silently) at search time. The
// mapping being nonempty and the Source record existing are necessary checks,
// not proof of correspondence; this validation is.
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
	if err := verifyIndexedCorrespondence(ctx, ingested, corpusIDs, dataset); err != nil {
		store.Close()
		return nil, fmt.Errorf("reuse validation failed: store %s does not match dataset %q content: %w", storePath, dataset.Name, err)
	}
	return ingested, nil
}

// verifyIndexedCorrespondence checks, for every corpus document, that the store
// holds the representation the dataset text derives to, with the same content
// digest and byte length. This is what "reusing an indexed store" means: the
// indexed content corresponds to the measured inputs, document by document.
func verifyIndexedCorrespondence(ctx context.Context, ingested *IngestedCorpus, corpusIDs []string, dataset *Dataset) error {
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
		representation, _, err := mousa.NormalizeUTF8Text(artifact, content)
		if err != nil {
			return err
		}
		stored, err := ingested.Store.GetRepresentation(ctx, representation.ID)
		if err != nil {
			return fmt.Errorf("document %d (%s): %w", index, corpusID, err)
		}
		if stored.ContentSHA256 != representation.ContentSHA256 || stored.ByteLength != representation.ByteLength {
			return fmt.Errorf("document %d (%s): stored content digest does not match the dataset text", index, corpusID)
		}
	}
	return nil
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

package beir

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	modernsqlite "modernc.org/sqlite"
)

// IndexProbe reads query-term evidence directly from an indexed store's FTS5
// index through a read-only connection. It is measurement instrumentation for
// the floor-reduction policy: it never writes, and it answers exactly two
// questions per term — how many indexed rows contain it, and the strongest
// per-document BM25 magnitude the engine assigns to it. Evidence is cached per
// term because a corpus has one answer per term, so a run's probes are bounded
// by its distinct query terms, not by query count.
//
// Probing through MATCH (never through vocabulary strings) is what makes this
// sound under the store's tokenizer folding: the engine tokenizes the probe
// term exactly as it would tokenize the same term inside a search expression
// under the configured tokenizer.
type IndexProbe struct {
	db   *sql.DB
	rows int

	mu           sync.Mutex
	evidence     map[string]TermEvidence
	probes       int
	probeSeconds float64
}

// OpenIndexProbe opens the store file read-only and counts its indexed rows.
// The probe shares nothing with the engine's own connection; it only reads
// committed index rows through a connector constructed the same way the
// engine builds its connections, so SQLite sees identical DSN semantics.
// mode=ro plus _query_only enforce that the probe never writes.
func OpenIndexProbe(ctx context.Context, storePath string) (*IndexProbe, error) {
	query := url.Values{
		"mode":          {"ro"},
		"_query_only":   {"1"},
		"_foreign_keys": {"0"},
		"_busy_timeout": {"10000"},
	}
	// The store path must be absolute in the URI: a relative path becomes an
	// invalid authority once the file scheme is applied.
	absolute, err := filepath.Abs(storePath)
	if err != nil {
		return nil, fmt.Errorf("resolve store path for index probe: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute, RawQuery: query.Encode()}).String()
	connector, err := modernsqlite.NewConnector(dsn)
	if err != nil {
		return nil, fmt.Errorf("open index probe connector: %w", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open index probe: %w", err)
	}
	var rows int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM segment_lexical_fts").Scan(&rows); err != nil {
		db.Close()
		return nil, fmt.Errorf("index probe row count: %w", err)
	}
	return &IndexProbe{db: db, rows: rows, evidence: map[string]TermEvidence{}}, nil
}

// Rows returns the number of indexed rows the probe was opened against.
func (probe *IndexProbe) Rows() int { return probe.rows }

// Close releases the probe's connection.
func (probe *IndexProbe) Close() error { return probe.db.Close() }

// Probes reports how many distinct terms were measured and the wall time the
// probes took, so a run can report instrumentation cost outside its latency
// measurements.
func (probe *IndexProbe) Probes() (count int, seconds float64) {
	probe.mu.Lock()
	defer probe.mu.Unlock()
	return probe.probes, probe.probeSeconds
}

// TermEvidence measures one term's evidence in the index: its row count and the
// strongest per-document BM25 magnitude the engine assigns to it (0 when the
// term has no postings). It implements TermEvidenceOracle.
func (probe *IndexProbe) TermEvidence(ctx context.Context, term string) (TermEvidence, error) {
	probe.mu.Lock()
	cached, ok := probe.evidence[term]
	probe.mu.Unlock()
	if ok {
		return cached, nil
	}
	started := time.Now()
	// The row count walks the term's posting list; the strongest magnitude is
	// the first row of the engine's own ranking order (bm25 ascending, so the
	// most negative score). bm25() is only legal inside a ranked MATCH query,
	// which is why the two answers share one scan shape rather than an
	// aggregate.
	var frequency int
	var strongest sql.NullFloat64
	err := probe.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM segment_lexical_fts WHERE segment_lexical_fts MATCH ?1),
		       (SELECT bm25(segment_lexical_fts) FROM segment_lexical_fts
		        WHERE segment_lexical_fts MATCH ?1 ORDER BY bm25(segment_lexical_fts) ASC LIMIT 1)`,
		quoteTerm(term)).Scan(&frequency, &strongest)
	if err != nil {
		return TermEvidence{}, fmt.Errorf("probe term %q: %w", term, err)
	}
	evidence := TermEvidence{DocumentFrequency: frequency}
	if strongest.Valid {
		if strongest.Float64 > 0 {
			return TermEvidence{}, fmt.Errorf("probe term %q: bm25 %v is not a FTS5 rank (expected <= 0)", term, strongest.Float64)
		}
		evidence.MaxMagnitude = -strongest.Float64
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	probe.evidence[term] = evidence
	probe.probes++
	probe.probeSeconds += time.Since(started).Seconds()
	return evidence, nil
}

// VerifyFloorPremise checks, against this index, that terms at or above half
// the rows carry at most floorMagnitudeLimit of evidence and terms below that
// boundary carry more. The boundary sample is the risk surface: the largest-df
// floor term, the floor term nearest the boundary, the non-floor term nearest
// the boundary, and the weakest non-floor term. A contradiction means this
// SQLite build does not clamp BM25 the way the policy requires, and the caller
// must refuse the policy instead of silently changing rankings.

// PremiseSource is what the premise verifier measures: term evidence plus the
// row count that defines the floor boundary. IndexProbe implements it; tests
// substitute hand-computed values.
type PremiseSource interface {
	TermEvidence(ctx context.Context, term string) (TermEvidence, error)
	Rows() int
}

func VerifyFloorPremise(ctx context.Context, source PremiseSource, terms []string) error {
	rows := source.Rows()
	var insideBest, insideBoundary *string
	var insideBoundaryGap, insideMaxDF int
	var outsideBoundary *string
	var outsideBoundaryGap, outsideMinDF int
	seenInside, seenOutside := 0, 0
	var weakestOutside *string
	weakestOutsideMagnitude := -1.0
	for index, term := range terms {
		evidence, err := source.TermEvidence(ctx, term)
		if err != nil {
			return err
		}
		floor := 2*evidence.DocumentFrequency >= rows
		gap := 2*evidence.DocumentFrequency - rows
		if gap < 0 {
			gap = -gap
		}
		if floor {
			seenInside++
			if insideMaxDF < evidence.DocumentFrequency {
				insideMaxDF, insideBest = evidence.DocumentFrequency, &terms[index]
			}
			if insideBoundary == nil || gap < insideBoundaryGap {
				insideBoundaryGap, insideBoundary = gap, &terms[index]
			}
		} else {
			seenOutside++
			if outsideBoundary == nil || gap < outsideBoundaryGap {
				outsideBoundaryGap, outsideBoundary = gap, &terms[index]
			}
			if outsideMinDF == 0 || evidence.DocumentFrequency < outsideMinDF {
				outsideMinDF = evidence.DocumentFrequency
			}
			if evidence.MaxMagnitude > weakestOutsideMagnitude {
				weakestOutsideMagnitude = evidence.MaxMagnitude
				weakestOutside = &terms[index]
			}
		}
	}
	check := func(term *string, inside bool) error {
		if term == nil {
			return nil
		}
		evidence, err := source.TermEvidence(ctx, *term)
		if err != nil {
			return err
		}
		if inside && evidence.MaxMagnitude > floorMagnitudeLimit {
			return fmt.Errorf("floor premise violated: term %q (df %d of %d rows) carries magnitude %g > %g; this index does not clamp BM25 as the policy requires",
				*term, evidence.DocumentFrequency, rows, evidence.MaxMagnitude, floorMagnitudeLimit)
		}
		if !inside && evidence.MaxMagnitude <= floorMagnitudeLimit {
			return fmt.Errorf("floor premise violated: term %q (df %d of %d rows) carries magnitude %g <= %g; the floor boundary does not separate evidence on this index",
				*term, evidence.DocumentFrequency, rows, evidence.MaxMagnitude, floorMagnitudeLimit)
		}
		return nil
	}
	if err := check(insideBest, true); err != nil {
		return err
	}
	if err := check(insideBoundary, true); err != nil {
		return err
	}
	if err := check(outsideBoundary, false); err != nil {
		return err
	}
	if err := check(weakestOutside, false); err != nil {
		return err
	}
	if seenInside == 0 || seenOutside == 0 {
		return fmt.Errorf("floor premise unverifiable: %d floor and %d non-floor terms in the sampled corpus", seenInside, seenOutside)
	}
	return nil
}

// ReductionOracle adapts an IndexProbe to the per-query TermEvidenceOracle the
// reducer consumes, threading the probe's context.
type ReductionOracle struct {
	probe *IndexProbe
	ctx   context.Context
}

// NewReductionOracle binds a probe to one search context.
func NewReductionOracle(ctx context.Context, probe *IndexProbe) *ReductionOracle {
	return &ReductionOracle{probe: probe, ctx: ctx}
}

// TermEvidence implements TermEvidenceOracle.
func (oracle *ReductionOracle) TermEvidence(term string) (TermEvidence, error) {
	return oracle.probe.TermEvidence(oracle.ctx, term)
}

// TermList returns the distinct terms of one query in first-occurrence order,
// the sample the premise verifier needs.
func TermList(query string) []string {
	seen := map[string]struct{}{}
	terms := make([]string, 0)
	for _, term := range queryTerms(query) {
		if _, duplicate := seen[term]; duplicate {
			continue
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}
	return terms
}

// JoinTerms renders quoted OR terms the way the expression builders do; used
// by report code that must echo an expression form.
func JoinTerms(terms []string) string {
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		quoted = append(quoted, quoteTerm(term))
	}
	return strings.Join(quoted, " OR ")
}

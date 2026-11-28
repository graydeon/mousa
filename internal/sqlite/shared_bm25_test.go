package sqlite

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

type sharedDoc struct{ key, text string }

func sharedRequest(sources []mousa.SourceID, limit int, texts ...string) mousa.QueryRequest {
	return mousa.QueryRequest{
		Scope:  mousa.Scope{Sources: sources},
		Texts:  texts,
		Budget: mousa.Budget{Limit: 8192, Unit: mousa.BudgetBytes},
		Limit:  limit,
		Caller: "audit-test",
	}
}

// seedSharedPeer pushes one source holding docs in the given order and allows
// it at a fixed access time. Segments come back keyed by document key.
func seedSharedPeer(t *testing.T, s *Store, name string, now int64, docs ...sharedDoc) (mousa.SourceID, mousa.Observation, map[string]mousa.Segment) {
	t.Helper()
	ctx := t.Context()
	source := testSourceNamed(t, name)
	observationID, err := mousa.NewObservationID(source.ID, name)
	if err != nil {
		t.Fatal(err)
	}
	batch := mousa.IngestBatch{AdapterID: "test.adapter", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem, CapturedAtUsec: 1, Source: source, Observation: mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: source.ID, ExternalObservationID: name}}
	if err := s.ApplyIngest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	segments := make(map[string]mousa.Segment, len(docs))
	for _, doc := range docs {
		segments[doc.key] = addEnforcedDocument(t, s, batch.Observation, doc.key, doc.text).segments[0]
	}
	if _, _, err := s.setSourceAccess(ctx, source.ID, mousa.SourceAccessAllow, "test", false, now); err != nil {
		t.Fatal(err)
	}
	return source.ID, batch.Observation, segments
}

func allowAt(t *testing.T, s *Store, source mousa.SourceID, now int64) {
	t.Helper()
	if _, _, err := s.setSourceAccess(t.Context(), source, mousa.SourceAccessAllow, "test", false, now); err != nil {
		t.Fatal(err)
	}
}

// TestSharedBM25ShortNote: the note's single-document shard gives every query
// term the 1e-6 IDF floor, so the raw local-score merge prefers the peer's
// long document where "harbor" is rare. Shared statistics make "buoy" rare
// across the corpus and the short note wins on both rarity and length.
func TestSharedBM25ShortNote(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	a, note := seedAuditItem(t, s, "notice", "harbor buoy")
	allowAt(t, s, a, 100)
	b, _, _ := seedSharedPeer(t, s, "peer-corpus", 200,
		sharedDoc{"ledger", strings.Repeat("harbor docks ", 200)},
		sharedDoc{"memo", "quarterly filing index"},
		sharedDoc{"summary", "annual review checklist"},
	)
	result, err := s.Query(ctx, sharedRequest([]mousa.SourceID{a, b}, 1, "harbor buoy"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Passages) != 1 || result.Passages[0].SegmentID != note.ID {
		t.Fatalf("passages = %+v, want only the short note", result.Passages)
	}
	branch := result.Trail.Retrieval.Branches[0]
	if result.Trail.Retrieval.Fusion != mousa.SharedBM25 || branch.Scoring != mousa.SharedBM25 || branch.SharedBM25 == nil {
		t.Fatalf("trail retrieval = %+v, want shared BM25 branch", result.Trail.Retrieval)
	}
	if branch.SharedBM25.Documents != 4 || !slices.Equal(branch.SharedBM25.Frequencies, []uint64{2, 1}) {
		t.Fatalf("shared stats = %+v, want 4 documents and frequencies [2 1]", branch.SharedBM25)
	}
}

// TestSharedBM25FullMatch: three shards each offer a single-term hit whose
// local IDF beats the one-document shard's floored full match; shared
// statistics must rank the document matching every term first.
func TestSharedBM25FullMatch(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	a, full := seedAuditItem(t, s, "notice", "coral kelp reef")
	allowAt(t, s, a, 100)
	b, _, _ := seedSharedPeer(t, s, "peer-b", 200,
		sharedDoc{"coral", "coral sandbar"}, sharedDoc{"tide", "tide chart"}, sharedDoc{"moon", "moon phase"})
	c, _, _ := seedSharedPeer(t, s, "peer-c", 300,
		sharedDoc{"kelp", "kelp forest"}, sharedDoc{"wave", "wave gauge"}, sharedDoc{"wind", "wind rose"})
	d, _, _ := seedSharedPeer(t, s, "peer-d", 400,
		sharedDoc{"reef", "reef crest"}, sharedDoc{"depth", "depth sounder"}, sharedDoc{"current", "current table"})
	result, err := s.Query(ctx, sharedRequest([]mousa.SourceID{a, b, c, d}, 4, "coral kelp reef"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Passages) != 4 || result.Passages[0].SegmentID != full.ID {
		t.Fatalf("passages = %+v, want the full match first of four", result.Passages)
	}
	stats := result.Trail.Retrieval.Branches[0].SharedBM25
	if stats == nil || stats.Documents != 10 || !slices.Equal(stats.Frequencies, []uint64{2, 2, 2}) {
		t.Fatalf("shared stats = %+v, want 10 documents and frequencies [2 2 2]", stats)
	}
}

// TestSharedBM25FTS5Parity scores one shard through the shared path and
// compares it with the shard's own FTS5 bm25() order and score bits.
func TestSharedBM25FTS5Parity(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	items := []sharedDoc{
		{"note-a", "été—été—été harbor"}, // overlapping two-token phrase after folding
		{"note-b", "reef reef reef kelp"},
		{"note-c", "coral reef buoy"},
		{"note-d", "harbor docks work"},
		{"note-e", "été reef"}, // single "ete": phrase frequency zero, DF nonzero
	}
	var a mousa.SourceID
	for _, item := range items {
		a, _ = seedAuditItem(t, s, item.key, item.text)
	}
	allowAt(t, s, a, 100)
	scopes, err := lexicalScopes(ctx, s.db, []mousa.SourceID{a})
	if err != nil || len(scopes) != 1 {
		t.Fatalf("lexical scopes = %v, %v; want one shard", scopes, err)
	}
	// Phrase slots: "ete ete", "reef", "reef", zero tokens, "harbor".
	quoted, err := mousa.PrepareLexicalTerms("été—été reef reef — harbor", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := scopedProposals(ctx, s.db, scopes[0], strings.Join(quoted, " OR "), 50, false)
	if err != nil {
		t.Fatal(err)
	}
	shared, stats, err := sharedProposals(ctx, s.db, scopes, quoted, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 5 || len(shared) != len(raw) {
		t.Fatalf("raw %d vs shared %d proposals", len(raw), len(shared))
	}
	if stats.Documents != 5 || stats.Tokens != 16 || !slices.Equal(stats.Frequencies, []uint64{1, 3, 3, 0, 2}) {
		t.Fatalf("shared stats = %+v, want 5 documents, 16 tokens and frequencies [1 3 3 0 2]", stats)
	}
	overlap := false
	for _, p := range shared {
		if len(p.Match.Frequencies) == 5 && p.Match.Frequencies[0] == 2 {
			overlap = true // "ete ete" occurs twice inside three consecutive tokens
		}
	}
	if !overlap {
		t.Fatalf("no proposal counted the overlapping phrase twice: %+v", shared)
	}
	ordered := slices.Clone(raw)
	slices.SortFunc(ordered, func(x, y proposal) int {
		return cmp.Or(cmp.Compare(x.Score, y.Score), bytes.Compare(x.SegmentID[:], y.SegmentID[:]))
	})
	for i := range ordered {
		if ordered[i].SegmentID != shared[i].SegmentID {
			t.Fatalf("shared order[%d] = %x, want FTS5 order %x", i, shared[i].SegmentID, ordered[i].SegmentID)
		}
		want, got := ordered[i].Score, shared[i].Score
		if math.Abs(got-want) > 1e-9*max(1, math.Abs(want)) {
			t.Fatalf("shared score[%d] = %.17g, want FTS5 score %.17g within tolerance", i, got, want)
		}
	}
}

func TestSharedBM25Verify(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	a, _ := seedAuditItem(t, s, "notice", "harbor buoy")
	allowAt(t, s, a, 100)
	b, _, _ := seedSharedPeer(t, s, "peer-corpus", 200,
		sharedDoc{"ledger", "harbor docks ledger"}, sharedDoc{"memo", "buoy tender memo"})
	if err := s.CreateCollection(ctx, "combo"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []mousa.SourceID{a, b} {
		if err := s.SetCollectionSource(ctx, "combo", id, true); err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Query(ctx, mousa.QueryRequest{
		Scope:  mousa.Scope{Collection: "combo"},
		Texts:  []string{"harbor", "buoy"},
		Budget: mousa.Budget{Limit: 8192, Unit: mousa.BudgetBytes},
		Limit:  4,
		Caller: "audit-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	retrieval := result.Trail.Retrieval
	if retrieval.Fusion != mousa.SharedBM25RRF {
		t.Fatalf("multi-text fusion = %q, want %q", retrieval.Fusion, mousa.SharedBM25RRF)
	}
	for i, branch := range retrieval.Branches {
		if branch.Scoring != mousa.SharedBM25 || branch.SharedBM25 == nil {
			t.Fatalf("branch %d = %+v, want shared BM25 evidence", i, branch)
		}
	}
	for _, candidate := range result.Trail.Candidates {
		for _, score := range candidate.Scores {
			if score.BM25 == nil {
				t.Fatalf("candidate %x score lacks shared BM25 match evidence", candidate.SegmentID)
			}
		}
	}
	counts, err := s.VerifyTrails(ctx, []mousa.SourceTrailID{result.Trail.ID})
	if err != nil || counts.RecordCounts["trail"] != 1 {
		t.Fatalf("shared trail verify = %v, %v", counts, err)
	}
	trail := result.Trail

	// A stored trail survives library log changes: one ULP still seals and
	// keeps its recorded bits, but anything outside the tolerance rejects.
	// Perturbing toward -Inf keeps rank 1 ahead of every other candidate, so
	// the replayed order cannot move.
	bits, err := strconv.ParseUint(trail.Candidates[0].Scores[0].ValueBits, 16, 64)
	if err != nil {
		t.Fatal(err)
	}
	recorded := math.Float64frombits(bits)
	for _, tt := range []struct {
		name  string
		value float64
		valid bool
	}{
		{"one_ULP", math.Nextafter(recorded, math.Inf(-1)), true},
		{"outside_tolerance", recorded - 1e-8*max(1, math.Abs(recorded)), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			forged := trail
			forged.Candidates = slices.Clone(trail.Candidates)
			forged.Candidates[0].Scores = slices.Clone(trail.Candidates[0].Scores)
			forged.Candidates[0].Scores[0].ValueBits = fmt.Sprintf("%016x", math.Float64bits(tt.value))
			sealed, err := mousa.NewTrail(forged)
			if !tt.valid {
				if err == nil {
					t.Fatal("accepted a score outside tolerance")
				}
				return
			}
			if err != nil {
				t.Fatalf("rejected a one-ULP perturbation: %v", err)
			}
			encoded, err := mousa.EncodeTrail(sealed)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := mousa.DecodeTrail(encoded)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Candidates[0].Scores[0].ValueBits != forged.Candidates[0].Scores[0].ValueBits {
				t.Fatalf("encode/decode lost the recorded bits %s", forged.Candidates[0].Scores[0].ValueBits)
			}
		})
	}

	// Enlarged token statistics change the recomputed score and must fail.
	forged := trail
	cloned := *trail.Retrieval
	cloned.Branches = slices.Clone(trail.Retrieval.Branches)
	stats := *trail.Retrieval.Branches[0].SharedBM25
	stats.Tokens = 1<<53 - 1
	cloned.Branches[0].SharedBM25 = &stats
	forged.Retrieval = &cloned
	if _, err := mousa.NewTrail(forged); err == nil {
		t.Fatal("accepted forged token statistics")
	}

	// A shared branch without statistics and a shared score without match
	// evidence are both rejected.
	forged = trail
	cloned = *trail.Retrieval
	cloned.Branches = slices.Clone(trail.Retrieval.Branches)
	cloned.Branches[0].SharedBM25 = nil
	forged.Retrieval = &cloned
	if _, err := mousa.NewTrail(forged); err == nil {
		t.Fatal("accepted shared scoring without statistics")
	}
	forged = trail
	forged.Candidates = slices.Clone(trail.Candidates)
	forged.Candidates[0].Scores = slices.Clone(trail.Candidates[0].Scores)
	forged.Candidates[0].Scores[0].BM25 = nil
	if _, err := mousa.NewTrail(forged); err == nil {
		t.Fatal("accepted a shared score without match evidence")
	}
	forged = trail
	forged.Candidates = slices.Clone(trail.Candidates)
	forged.Candidates[0].Scores = slices.Clone(trail.Candidates[0].Scores)
	empty := *forged.Candidates[0].Scores[0].BM25
	empty.Frequencies = make([]uint64, len(empty.Frequencies))
	forged.Candidates[0].Scores[0].BM25 = &empty
	if _, err := mousa.NewTrail(forged); err == nil {
		t.Fatal("accepted a match with no phrase occurrence")
	}

	// A repeated phrase keeps one frequency slot per original term.
	repeated, err := s.Query(ctx, sharedRequest([]mousa.SourceID{a, b}, 4, "harbor harbor"))
	if err != nil {
		t.Fatal(err)
	}
	stats = *repeated.Trail.Retrieval.Branches[0].SharedBM25
	if len(stats.Frequencies) != 2 || stats.Frequencies[0] != stats.Frequencies[1] {
		t.Fatalf("repeated-phrase frequencies = %v, want two equal slots", stats.Frequencies)
	}
	if counts, err := s.VerifyTrails(ctx, []mousa.SourceTrailID{repeated.Trail.ID}); err != nil || counts.RecordCounts["trail"] != 1 {
		t.Fatalf("repeated-phrase trail verify = %v, %v", counts, err)
	}
}

func TestSharedBM25Recheck(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	a, _ := seedAuditItem(t, s, "notice", "harbor buoy")
	allowAt(t, s, a, 100)
	b, observation, _ := seedSharedPeer(t, s, "peer-corpus", 200,
		sharedDoc{"ledger", "ledger filing"}, sharedDoc{"memo", "memo index"})
	sources := []mousa.SourceID{a, b}
	result, err := s.Query(ctx, sharedRequest(sources, 4, "harbor"))
	if err != nil {
		t.Fatal(err)
	}
	stats := result.Trail.Retrieval.Branches[0].SharedBM25
	if stats == nil || stats.Documents != 3 {
		t.Fatalf("shared stats = %+v, want the no-hit shard counted (3 documents)", stats)
	}
	recheck, err := s.RecheckTrailText(ctx, result.Trail.ID, nil)
	if err != nil || recheck.Status != "fresh" || recheck.Stale == nil || len(recheck.Stale) != 0 {
		t.Fatalf("recheck = %+v, %v; want fresh with an empty stale list", recheck, err)
	}
	// An eligible shard that returned no hits still shapes the statistics:
	// growing it stales the trail without touching any released passage.
	addEnforcedDocument(t, s, observation, "supplement", "memo supplement")
	recheck, err = s.RecheckTrailText(ctx, result.Trail.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recheck.Status != "stale" || len(recheck.Stale) != 1 ||
		recheck.Stale[0].TrailID != result.Trail.ID || recheck.Stale[0].Reason != "bm25_statistics_changed" {
		t.Fatalf("recheck = %+v, want stale bm25_statistics_changed", recheck)
	}
	if counts, err := s.VerifyTrails(ctx, []mousa.SourceTrailID{result.Trail.ID}); err != nil || counts.RecordCounts["trail"] != 1 {
		t.Fatalf("verify after shard growth = %v, %v", counts, err)
	}
	// The check runs read-only against the main database: no scratch writes.
	ro, err := OpenReadOnly(ctx, s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	recheck, err = ro.RecheckTrailText(ctx, result.Trail.ID, nil)
	if err != nil || recheck.Status != "stale" {
		t.Fatalf("read-only recheck = %+v, %v; want stale", recheck, err)
	}

	// An empty shared pack still reports the stale reason once the
	// statistics behind it move.
	empty, err := s.Query(ctx, sharedRequest(sources, 4, "xyzzy"))
	if err != nil {
		t.Fatal(err)
	}
	addEnforcedDocument(t, s, observation, "appendix", "memo appendix")
	recheck, err = s.RecheckTrailText(ctx, empty.Trail.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recheck.Status != "stale" || len(recheck.Stale) == 0 || recheck.Stale[0].Reason != "bm25_statistics_changed" {
		t.Fatalf("empty-pack recheck = %+v, want stale bm25_statistics_changed", recheck)
	}
}

func TestSharedBM25Deterministic(t *testing.T) {
	ctx := t.Context()
	seed := func(t *testing.T) (*Store, []mousa.SourceID) {
		s := openLexicalStore(t)
		a, _ := seedAuditItem(t, s, "notice", "harbor buoy")
		allowAt(t, s, a, 100)
		b, _, _ := seedSharedPeer(t, s, "peer-corpus", 200,
			sharedDoc{"ledger", strings.Repeat("harbor docks ", 200)},
			sharedDoc{"memo", "quarterly filing index"})
		return s, []mousa.SourceID{a, b}
	}
	first, sources := seed(t)
	defer first.Close()
	second, sources2 := seed(t)
	defer second.Close()
	one, err := first.Query(ctx, sharedRequest(sources, 2, "harbor buoy"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := second.Query(ctx, sharedRequest(sources2, 2, "harbor buoy"))
	if err != nil {
		t.Fatal(err)
	}
	if one.Trail.Retrieval.Fusion != mousa.SharedBM25 || two.Trail.Retrieval.Fusion != mousa.SharedBM25 {
		t.Fatalf("fusion = %q / %q, want shared BM25", one.Trail.Retrieval.Fusion, two.Trail.Retrieval.Fusion)
	}
	if one.Trail.ResultSHA256 != two.Trail.ResultSHA256 {
		t.Fatalf("result digests differ across identical stores: %s vs %s",
			one.Trail.ResultSHA256, two.Trail.ResultSHA256)
	}

	// Equal shard scores tie at the shard cutoff by segment ID, not rowid:
	// the largest segment ID is dropped even when it was ingested first.
	// Distinct texts keep packing's content dedup out of the tie.
	s := openLexicalStore(t)
	defer s.Close()
	a, segA := seedAuditItem(t, s, "tie-a", "harbor albacore")
	_, segB := seedAuditItem(t, s, "tie-b", "harbor brigantine")
	_, segC := seedAuditItem(t, s, "tie-c", "harbor cutter")
	allowAt(t, s, a, 100)
	b, _, _ := seedSharedPeer(t, s, "peer-corpus", 200, sharedDoc{"ledger", "ledger filing"})
	segments := []mousa.Segment{segA, segB, segC}
	slices.SortFunc(segments, func(x, y mousa.Segment) int { return bytes.Compare(x.ID[:], y.ID[:]) })
	var byRowID []byte
	if err := s.db.QueryRowContext(ctx, `SELECT segment_id FROM segment_lexical_rows
		WHERE segment_id IN (?, ?, ?) ORDER BY rowid DESC LIMIT 1`,
		segA.ID[:], segB.ID[:], segC.ID[:]).Scan(&byRowID); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(byRowID, segments[2].ID[:]) {
		t.Fatalf("tie fixture cannot distinguish cutoffs: largest segment ID %x is last by rowid", segments[2].ID)
	}
	result, err := s.Query(ctx, sharedRequest([]mousa.SourceID{a, b}, 2, "harbor"))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Passages) != 2 || result.Passages[0].SegmentID != segments[0].ID ||
		result.Passages[1].SegmentID != segments[1].ID {
		t.Fatalf("cutoff passages = %+v, want the two smallest segment IDs %x %x",
			result.Passages, segments[0].ID, segments[1].ID)
	}
}

func TestScopeFusionSingleScopeDigest(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	a, _ := seedAuditItem(t, s, "notice", "harbor buoy")
	if _, _, err := s.setSourceAccess(ctx, a, mousa.SourceAccessAllow, "test", false, 100); err != nil {
		t.Fatal(err)
	}
	result, err := s.Query(ctx, auditRequest(a))
	if err != nil {
		t.Fatal(err)
	}
	const want = "3a83733eddbcdf9f45c64018bbc53f42bbad2813a017fa13d55d7b410dfceee5"
	if result.Trail.ResultSHA256.String() != want {
		t.Fatalf("single-scope result digest = %s, want pinned %s", result.Trail.ResultSHA256, want)
	}
	forged := result.Trail
	forged.Retrieval = &mousa.Retrieval{Branches: result.Trail.Retrieval.Branches, Fusion: mousa.SharedBM25}
	if _, err := mousa.NewTrail(forged); err == nil {
		t.Fatal("a legacy single-source trail accepted a shared fusion name")
	}

	// A single allowed source keeps at most one eligible shard, so a trail
	// carrying fully valid shared evidence is still rejected.
	scopes, err := lexicalScopes(ctx, s.db, []mousa.SourceID{a})
	if err != nil || len(scopes) != 1 {
		t.Fatalf("lexical scopes = %v, %v; want one shard", scopes, err)
	}
	quoted, err := mousa.PrepareLexicalTerms("harbor", false)
	if err != nil {
		t.Fatal(err)
	}
	proposals, stats, err := sharedProposals(ctx, s.db, scopes, quoted, 50)
	if err != nil || len(proposals) == 0 {
		t.Fatalf("shared proposals = %v, %v; want the note", proposals, err)
	}
	matches := map[mousa.SegmentID]*mousa.BM25Match{}
	for _, p := range proposals {
		matches[p.SegmentID] = p.Match
	}
	idf, err := stats.IDF()
	if err != nil {
		t.Fatal(err)
	}
	forged = result.Trail
	cloned := *result.Trail.Retrieval
	cloned.Branches = slices.Clone(result.Trail.Retrieval.Branches)
	cloned.Branches[0].Scoring = mousa.SharedBM25
	cloned.Branches[0].SharedBM25 = stats
	cloned.Fusion = mousa.SharedBM25
	forged.Retrieval = &cloned
	forged.Candidates = slices.Clone(result.Trail.Candidates)
	forged.Candidates[0].Scores = slices.Clone(result.Trail.Candidates[0].Scores)
	match := matches[forged.Candidates[0].SegmentID]
	if match == nil {
		t.Fatalf("candidate %x has no shared match", forged.Candidates[0].SegmentID)
	}
	value, err := stats.Score(idf, *match)
	if err != nil {
		t.Fatal(err)
	}
	forged.Candidates[0].Scores[0].BM25 = match
	forged.Candidates[0].Scores[0].ValueBits = fmt.Sprintf("%016x", math.Float64bits(value))
	if _, err := mousa.NewTrail(forged); err == nil {
		t.Fatal("a single-source trail accepted shared BM25 with valid evidence")
	}
}

func TestScopeFusionLegacyTrail(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	a, note := seedAuditItem(t, s, "notice", "harbor buoy")
	allowAt(t, s, a, 100)
	batch := testPushBatch(t, "peer-corpus", nil)
	if err := s.ApplyIngest(ctx, batch); err != nil {
		t.Fatal(err)
	}
	doc := addEnforcedDocument(t, s, batch.Observation, "ledger", strings.Repeat("harbor docks ", 200)).segments[0]
	addEnforcedDocument(t, s, batch.Observation, "memo", "harbor dock work order")
	allowAt(t, s, batch.Source.ID, 200)
	s.propose = func(_ context.Context, _ queryer, _ []string, _ []mousa.SourceID, _ int) ([]proposal, *mousa.BM25Stats, error) {
		return []proposal{
			{SegmentID: note.ID, ContentSHA256: note.ContentSHA256, Score: -1},
			{SegmentID: doc.ID, ContentSHA256: doc.ContentSHA256, Score: -2},
		}, nil, nil
	}
	result, err := s.Query(ctx, sharedRequest([]mousa.SourceID{a, batch.Source.ID}, 4, "harbor", "buoy"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Trail.Retrieval.Fusion != "rrf-k60-v1" {
		t.Fatalf("legacy proposer trail fusion = %q, want rrf-k60-v1", result.Trail.Retrieval.Fusion)
	}
	const want = "72c8d80de17aaed468474655864e84ecad227bd5b90e28845524ff0f68136208"
	if result.Trail.ResultSHA256.String() != want {
		t.Fatalf("legacy trail result digest = %s, want pinned %s", result.Trail.ResultSHA256, want)
	}
	if _, err := s.GetTrail(ctx, result.Trail.ID, nil); err != nil {
		t.Fatalf("legacy trail unreadable: %v", err)
	}
	recheck, err := s.RecheckTrailText(ctx, result.Trail.ID, nil)
	if err != nil || recheck.Status != "fresh" || recheck.Stale == nil || len(recheck.Stale) != 0 {
		t.Fatalf("legacy trail recheck = %+v, %v; want fresh with an empty stale list", recheck, err)
	}
	counts, err := s.VerifyTrails(ctx, []mousa.SourceTrailID{result.Trail.ID})
	if err != nil || counts.RecordCounts["trail"] != 1 {
		t.Fatalf("legacy trail verify = %v, %v", counts, err)
	}
}

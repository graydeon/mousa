package sqlite

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

type proposer func(ctx context.Context, q queryer, text string, sources []mousa.SourceID, limit int) ([]proposal, error)

type proposal struct {
	SegmentID     mousa.SegmentID
	ContentSHA256 mousa.SHA256
	Score         float64
}

var errQueryHeadMoved = errors.New("query snapshot source head moved")

// Query reads a snapshot and records its trail before releasing passages. A
// source changing during the gap between read and write forces a fresh read.
func (store *Store) Query(ctx context.Context, request mousa.QueryRequest) (mousa.QueryResult, error) {
	if err := store.requireWritable("query"); err != nil {
		return mousa.QueryResult{}, err
	}
	scope := mousa.TrailScope{Kind: "source", Sources: []mousa.SourceID{request.Scope.Source}}
	if request.Scope.Source == (mousa.SourceID{}) && request.Scope.Sources != nil {
		scope = mousa.TrailScope{Kind: "list", Sources: slices.Clone(request.Scope.Sources)}
		slices.SortFunc(scope.Sources, compareSourceIDs)
	} else if request.Scope.Sources != nil {
		return mousa.QueryResult{}, wrap(CodeInvalidQuery, "query", errors.New("choose one scope kind"))
	}
	if request.Scope.Collection != "" {
		return mousa.QueryResult{}, wrap(CodeInvalidQuery, "query", errors.New("collection scope is not supported"))
	}
	req := mousa.TrailRequest{
		Budget: request.Budget, Caller: request.Caller, Limit: request.Limit, Mode: request.Mode,
		Purpose: request.Purpose, RequestID: request.RequestID, Scope: scope,
		RequestedAtUsec: uint64(time.Now().UnixMicro()), Texts: slices.Clone(request.Texts),
	}
	if req.Mode == "" {
		req.Mode = mousa.QueryLexical
	}
	if req.Budget.Unit == "" {
		req.Budget.Unit = mousa.BudgetBytes
	}
	if req.Limit == 0 {
		req.Limit = mousa.MaxQueryCandidates
	}
	if req.RequestID == "" {
		req.RequestID = rand.Text()
	}
	if err := req.Validate(); err != nil {
		return mousa.QueryResult{}, wrap(CodeInvalidQuery, "query", err)
	}
	for range 2 {
		result, err := store.queryReadThenWrite(ctx, req)
		if !errors.Is(err, errQueryHeadMoved) {
			return result, err
		}
	}
	var result mousa.QueryResult
	err := store.writeImmediate(ctx, "query and append trail", func(conn *sql.Conn) error {
		var err error
		result, err = store.querySnapshot(ctx, conn, req)
		if err != nil {
			return err
		}
		return putTrail(ctx, conn, result.Trail)
	})
	if err != nil {
		return mousa.QueryResult{}, err
	}
	return result, nil
}

func (store *Store) queryReadThenWrite(ctx context.Context, req mousa.TrailRequest) (mousa.QueryResult, error) {
	result, err := store.readQuerySnapshot(ctx, req)
	if err != nil {
		return mousa.QueryResult{}, err
	}
	err = store.writeImmediate(ctx, "append query trail", func(conn *sql.Conn) error {
		for _, source := range result.Trail.Sources {
			if source.Head == nil {
				continue
			}
			head, err := sourceHeadAt(ctx, conn, source.SourceID, math.MaxInt64)
			if err != nil {
				return err
			}
			if head != *source.Head {
				return errQueryHeadMoved
			}
		}
		return putTrail(ctx, conn, result.Trail)
	})
	if err != nil {
		return mousa.QueryResult{}, err
	}
	return result, nil
}

func (store *Store) readQuerySnapshot(ctx context.Context, req mousa.TrailRequest) (mousa.QueryResult, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return mousa.QueryResult{}, classify("begin query snapshot", err)
	}
	defer tx.Rollback()
	result, err := store.querySnapshot(ctx, tx, req)
	if err != nil {
		return mousa.QueryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return mousa.QueryResult{}, classify("finish query snapshot", err)
	}
	return result, nil
}

func (store *Store) querySnapshot(ctx context.Context, q queryer, req mousa.TrailRequest) (mousa.QueryResult, error) {
	trail := mousa.Trail{Request: req}
	if err := q.QueryRowContext(ctx, `SELECT coalesce(max(seq),0) FROM log`).Scan(&trail.AsOf); err != nil {
		return mousa.QueryResult{}, classify("query log index", err)
	}
	storeID, err := getStoreID(ctx, q)
	if err != nil {
		return mousa.QueryResult{}, err
	}
	trail.StoreID = storeID.String()
	var allowed []mousa.SourceID
	allowedSet := make(map[mousa.SourceID]bool, len(req.Scope.Sources))
	for _, id := range req.Scope.Sources {
		decision, err := currentSourceDecision(ctx, q, id)
		if err != nil {
			return mousa.QueryResult{}, err
		}
		if decision.Outcome == "allow" {
			head, err := sourceHeadAt(ctx, q, id, trail.AsOf)
			if err != nil {
				return mousa.QueryResult{}, err
			}
			decision.Head = &head
			allowed = append(allowed, id)
			allowedSet[id] = true
		}
		if err := checkTrailSource(ctx, q, trail.AsOf, decision); err != nil {
			return mousa.QueryResult{}, err
		}
		trail.Sources = append(trail.Sources, decision)
	}
	passages := []mousa.Passage{}
	if len(allowed) != 0 {
		passages, err = store.retrieveForTrail(ctx, q, &trail, allowed, allowedSet)
		if err != nil {
			return mousa.QueryResult{}, err
		}
	}
	sealed, err := mousa.NewTrail(trail)
	if err != nil {
		return mousa.QueryResult{}, wrap(CodeIntegrity, "seal query trail", err)
	}
	return mousa.QueryResult{Trail: sealed, Passages: passages}, nil
}

func currentSourceDecision(ctx context.Context, q queryer, id mousa.SourceID) (mousa.SourceDecision, error) {
	decision := mousa.SourceDecision{SourceID: id, Access: "default"}
	if _, err := getSource(ctx, q, id); err != nil {
		if IsCode(err, CodeNotFound) {
			decision.Outcome, decision.Reasons = mousa.DecideSource(decision.Access, "unknown_source")
			return decision, nil
		}
		return decision, err
	}
	event, err := currentAccessEvent(ctx, q, id)
	if err != nil {
		return decision, err
	}
	if event != nil {
		decision.Access, decision.AccessEventID = event.Access, &event.ID
	}
	var lifecycle string
	state, err := getIngestState(ctx, q, id)
	switch {
	case IsCode(err, CodeNotFound):
		lifecycle = "missing_source_state"
	case err != nil:
		return decision, err
	case state.CollectionState == mousa.CollectionWithdrawn:
		lifecycle = "source_withdrawn"
		decision.WithdrawalID = state.CurrentWithdrawalID
	}
	decision.Outcome, decision.Reasons = mousa.DecideSource(decision.Access, lifecycle)
	return decision, nil
}

type queryCandidate struct {
	candidate mousa.Candidate
	paths     []mousa.EvidencePath
	text      string
}

func (store *Store) retrieveForTrail(ctx context.Context, q queryer, trail *mousa.Trail, allowed []mousa.SourceID, allowedSet map[mousa.SourceID]bool) ([]mousa.Passage, error) {
	req := trail.Request
	getProposals := store.propose
	if getProposals == nil {
		getProposals = proposeLexical
	}
	retrieval := &mousa.Retrieval{}
	if len(req.Texts) > 1 {
		retrieval.Fusion = "rrf-k60-v1"
	}
	candidates := make(map[mousa.SegmentID]*queryCandidate)
	for i, text := range req.Texts {
		quoted, err := mousa.PrepareLexicalTerms(text, false)
		if err != nil {
			return nil, wrap(CodeInvalidQuery, "prepare lexical query", err)
		}
		branch := mousa.Branch{Kind: "lexical", Scoring: "fts5-bm25", TermPolicy: "original", Text: i}
		for _, term := range quoted {
			// The splitter does not retain quote characters.
			branch.Terms = append(branch.Terms, term[1:len(term)-1])
		}
		proposals, err := getProposals(ctx, q, strings.Join(quoted, " OR "), allowed, req.Limit)
		if err != nil {
			return nil, err
		}
		branch.Proposed = len(proposals)
		if len(proposals) > req.Limit {
			return nil, integrity("query proposals", "proposer exceeded the candidate limit")
		}
		for _, proposed := range proposals {
			row, exists := candidates[proposed.SegmentID]
			if !exists {
				row, err = checkProposal(ctx, q, proposed.SegmentID, allowedSet)
				switch {
				case IsCode(err, CodeNotFound):
					branch.DroppedUnknown++
					continue
				case err != nil:
					return nil, err
				case row == nil:
					branch.DroppedNotAllowed++
					continue
				}
				candidates[proposed.SegmentID] = row
			}
			if proposed.ContentSHA256 != row.candidate.ContentSHA256 {
				row.candidate.Rejected = []string{"content_mismatch"}
			}
			row.candidate.Scores = append(row.candidate.Scores, mousa.TrailScore{
				Branch: i, Level: "block", ValueBits: fmt.Sprintf("%016x", math.Float64bits(proposed.Score)),
			})
		}
		retrieval.Branches = append(retrieval.Branches, branch)
	}
	rows := make([]mousa.Candidate, 0, len(candidates))
	for _, row := range candidates {
		rows = append(rows, row.candidate)
	}
	if err := mousa.RankCandidates(rows, len(req.Texts)); err != nil {
		return nil, wrap(CodeIntegrity, "rank candidates", err)
	}
	packing := mousa.ApplyFirstFit(rows, req.Budget.Limit)
	trail.Candidates, trail.Packing, trail.Retrieval = rows, &packing, retrieval
	passages := []mousa.Passage{}
	for _, candidate := range rows {
		if candidate.Packed != "full" {
			continue
		}
		row := candidates[candidate.SegmentID]
		passage, err := queryPassage(ctx, q, row)
		if err != nil {
			return nil, err
		}
		passage.Rank = candidate.Rank
		passages = append(passages, passage)
	}
	return passages, nil
}

func checkProposal(ctx context.Context, q queryer, id mousa.SegmentID, allowed map[mousa.SourceID]bool) (*queryCandidate, error) {
	segment, err := getSegment(ctx, q, id)
	if err != nil {
		return nil, err
	}
	paths, err := queryEvidencePaths(ctx, q, segment)
	if err != nil {
		return nil, err
	}
	sources := evidenceSourceIDs(paths)
	for _, source := range sources {
		if !allowed[source] {
			return nil, nil
		}
	}
	rangeInRepr, ok := segment.Selector.TextByteRange()
	if !ok || len(sources) == 0 {
		return nil, integrity("verify query candidate", "segment has no text range or source ancestry")
	}
	row := &queryCandidate{paths: paths, candidate: mousa.Candidate{
		ContentSHA256: segment.ContentSHA256, SegmentID: segment.ID, RepresentationID: segment.RepresentationID,
		SourceIDs: sources, Range: mousa.ByteRange{End: rangeInRepr.End, Start: rangeInRepr.Start},
	}}
	text, err := indexedSegmentText(ctx, q, segment)
	if IsCode(err, CodeIntegrity) || (err == nil && uint64(len(text)) != rangeInRepr.End-rangeInRepr.Start) {
		row.candidate.Rejected = []string{"content_mismatch"}
		return row, nil
	}
	if err != nil {
		return nil, err
	}
	row.text = text
	return row, nil
}

func queryEvidencePaths(ctx context.Context, q queryer, segment mousa.Segment) ([]mousa.EvidencePath, error) {
	repr, err := getRepresentation(ctx, q, segment.RepresentationID)
	if err != nil {
		return nil, lexicalStructuralError("verify query representation", err)
	}
	if err := segment.ValidateAgainst(repr); err != nil {
		return nil, wrap(CodeIntegrity, "verify query coordinates", err)
	}
	return lexicalEvidencePaths(ctx, q, segment)
}

func evidenceSourceIDs(paths []mousa.EvidencePath) []mousa.SourceID {
	ids := make([]mousa.SourceID, 0, len(paths))
	for _, path := range paths {
		ids = append(ids, path.SourceID)
	}
	slices.SortFunc(ids, compareSourceIDs)
	return slices.Compact(ids)
}

func compareSourceIDs(a, b mousa.SourceID) int { return bytes.Compare(a[:], b[:]) }

func queryPassage(ctx context.Context, q queryer, row *queryCandidate) (mousa.Passage, error) {
	candidate := row.candidate
	id := candidate.SourceIDs[0]
	var path mousa.EvidencePath
	for _, p := range row.paths {
		if p.SourceID == id {
			path = p
			break
		}
	}
	observation, err := getObservation(ctx, q, path.ObservationID)
	if err != nil {
		return mousa.Passage{}, err
	}
	item := observation.ExternalObservationID
	if rest, ok := strings.CutPrefix(item, "item/"); ok {
		artifact, err := getArtifact(ctx, q, path.ArtifactID)
		if err != nil {
			return mousa.Passage{}, err
		}
		item, ok = strings.CutSuffix(rest, localRevisionSuffix(artifact.ContentSHA256))
		if !ok || item == "" {
			return mousa.Passage{}, integrity("query passage item", "item revision identity disagrees with artifact")
		}
	}
	passage := mousa.Passage{
		SourceID: id, Item: item, SegmentID: candidate.SegmentID, Range: candidate.Range,
		ContentSHA256: candidate.ContentSHA256, Text: row.text,
	}
	repr, err := getRepresentation(ctx, q, candidate.RepresentationID)
	if err != nil {
		return mousa.Passage{}, err
	}
	if repr.ProcessorID != mousa.UTF8TextProcessorID {
		return mousa.Passage{}, integrity("query passage", "indexed text is not a normalized UTF-8 representation")
	}
	passage.RepresentationID, passage.RepresentationSHA256 = repr.ID, repr.ContentSHA256
	passage.SegmentPolicy, err = mousa.TextSegmentationPolicy(repr)
	if err != nil {
		return mousa.Passage{}, wrap(CodeIntegrity, "read query passage policy", err)
	}
	if passage.SegmentPolicy == mousa.TextSegmentNoteV1 {
		passage.Note, err = queryNoteEvidence(ctx, q, id, item)
		if err != nil {
			return mousa.Passage{}, err
		}
	}
	return passage, nil
}

func localRevisionSuffix(digest mousa.SHA256) string {
	// Local observation IDs use fmt's %x on SHA256's Stringer, not raw bytes.
	return fmt.Sprintf("@%x", digest)
}

func proposeLexical(ctx context.Context, q queryer, expression string, sources []mousa.SourceID, limit int) ([]proposal, error) {
	if err := validateLexicalQuery(expression, limit); err != nil {
		return nil, err
	}
	onlySource := false
	if len(sources) == 1 {
		if err := q.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM sources WHERE id != ?)`, sources[0][:]).Scan(&onlySource); err != nil {
			return nil, classify("check lexical proposal scope", err)
		}
	}
	const selectMatches = `
		SELECT r.segment_id, f.content_sha256, bm25(segment_lexical_fts)
		FROM segment_lexical_fts AS f
		JOIN segment_lexical_rows AS r ON r.rowid = f.rowid`
	const orderMatches = ` ORDER BY bm25(segment_lexical_fts), r.segment_id LIMIT ?`
	statement := selectMatches + ` WHERE segment_lexical_fts MATCH ?`
	args := make([]any, 0, len(sources)+2)
	// A single-source store needs no ancestry prefilter. Proposal verification
	// still checks canonical ancestry and content before releasing passages.
	if !onlySource {
		for _, source := range sources {
			args = append(args, source[:])
		}
		statement = `
		WITH RECURSIVE source_representations(id) AS (
			SELECT ri.representation_id
			FROM representation_inputs AS ri
			JOIN artifacts AS a ON a.id = ri.artifact_id
			JOIN observations AS o ON o.id = a.observation_id
			WHERE o.source_id IN (` + strings.Repeat("?,", len(sources)-1) + `?)
			UNION
			SELECT ri.representation_id
			FROM representation_inputs AS ri
			JOIN source_representations AS sr ON sr.id = ri.input_representation_id
		)` + selectMatches + ` JOIN segments AS s ON s.id = r.segment_id
		WHERE segment_lexical_fts MATCH ?
			AND s.representation_id IN (SELECT id FROM source_representations)`
	}
	args = append(args, expression, limit)
	rows, err := q.QueryContext(ctx, statement+orderMatches, args...)
	if err != nil {
		return nil, lexicalQueryError(ctx, err)
	}
	defer rows.Close()
	proposals := make([]proposal, 0, limit)
	for rows.Next() {
		var id, digest []byte
		var score float64
		if err := rows.Scan(&id, &digest, &score); err != nil {
			return nil, lexicalIntegrityError(ctx, "read lexical proposal", err)
		}
		var proposed proposal
		proposed.Score = score
		if len(id) == len(proposed.SegmentID) {
			copy(proposed.SegmentID[:], id)
		}
		if len(digest) == len(proposed.ContentSHA256) {
			copy(proposed.ContentSHA256[:], digest)
		}
		proposals = append(proposals, proposed)
	}
	return proposals, classify("finish lexical proposals", rows.Err())
}

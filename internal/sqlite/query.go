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

type proposer func(ctx context.Context, q queryer, quoted []string, sources []mousa.SourceID, limit int) ([]proposal, *mousa.BM25Stats, error)

type proposal struct {
	SegmentID     mousa.SegmentID
	ContentSHA256 mousa.SHA256
	Score         float64
	Sources       []mousa.SourceID
	text          string
	length        uint64
	Match         *mousa.BM25Match
}

var errQueryHeadMoved = errors.New("query snapshot source head moved")

// Query reads a snapshot and records its trail before releasing passages. A
// source changing during the gap between read and write forces a fresh read.
func (store *Store) Query(ctx context.Context, request mousa.QueryRequest) (mousa.QueryResult, error) {
	return store.query(ctx, request, nil)
}

// QueryAdmitted runs a query under a served request admission. The caller and
// request id come from the admission, never the request body, and the stored
// trail binds the outcome in the transaction that commits it.
func (store *Store) QueryAdmitted(ctx context.Context, request mousa.QueryRequest, admission mousa.RequestAdmission) (mousa.QueryResult, error) {
	request.Caller, request.RequestID = admission.Principal, admission.RequestID
	return store.query(ctx, request, &admission)
}

func (store *Store) query(ctx context.Context, request mousa.QueryRequest, admission *mousa.RequestAdmission) (mousa.QueryResult, error) {
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
		if request.Scope.Source != (mousa.SourceID{}) || request.Scope.Sources != nil {
			return mousa.QueryResult{}, wrap(CodeInvalidQuery, "query", errors.New("choose one scope kind"))
		}
		scope = mousa.TrailScope{Kind: "collection", Name: request.Scope.Collection, Sources: []mousa.SourceID{}}
	}
	req := mousa.TrailRequest{
		Budget: request.Budget, Caller: request.Caller, Limit: request.Limit, Mode: request.Mode,
		Purpose: request.Purpose, RequestID: request.RequestID, Scope: scope,
		RequestedAtUsec: uint64(time.Now().UnixMicro()), Texts: slices.Clone(request.Texts),
		IncludeSuperseded: request.IncludeSuperseded,
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
	// Served callers see only the note lineage their own read grants could
	// resolve; direct reads keep the full durable citation record.
	var noteGrants map[mousa.SourceID]bool
	if admission != nil {
		noteGrants = request.PermittedSources
	}
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := store.queryReadThenWrite(ctx, req, request.PermittedSources, noteGrants, admission)
		if !errors.Is(err, errQueryHeadMoved) {
			return result, err
		}
		// A moved head retries the whole read; the marker carries the next
		// attempt number so retries are distinguishable from first runs.
		store.ObserveMarker(mousa.StageRetrieval, mousa.StagePhaseRetry, attempt+1)
	}
	var result mousa.QueryResult
	err := store.writeImmediate(ctx, "query and append trail", func(conn *sql.Conn) error {
		var err error
		result, err = store.querySnapshot(ctx, conn, req, request.PermittedSources, noteGrants)
		if err != nil {
			return err
		}
		if err := putTrail(ctx, conn, result.Trail); err != nil {
			return err
		}
		return finishTrailOutcome(ctx, conn, admission, result.Trail)
	})
	if err != nil {
		return mousa.QueryResult{}, err
	}
	return result, nil
}

func (store *Store) queryReadThenWrite(ctx context.Context, req mousa.TrailRequest, grants, noteGrants map[mousa.SourceID]bool, admission *mousa.RequestAdmission) (mousa.QueryResult, error) {
	result, err := store.readQuerySnapshot(ctx, req, grants, noteGrants)
	if err != nil {
		return mousa.QueryResult{}, err
	}
	err = store.writeImmediate(ctx, "append query trail", func(conn *sql.Conn) error {
		if req.Scope.Kind == "collection" {
			current, err := collectionSources(ctx, conn, req.Scope.Name)
			if err != nil {
				return err
			}
			if !slices.Equal(current, result.Trail.Request.Scope.Sources) {
				return errQueryHeadMoved
			}
		}
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
		if err := putTrail(ctx, conn, result.Trail); err != nil {
			return err
		}
		return finishTrailOutcome(ctx, conn, admission, result.Trail)
	})
	if err != nil {
		return mousa.QueryResult{}, err
	}
	return result, nil
}

// finishTrailOutcome binds a stored query trail to its request outcome in the
// same write transaction. A scope whose named sources all resolve denied is a
// denied outcome that keeps the trail; every other stored trail commits.
func finishTrailOutcome(ctx context.Context, conn *sql.Conn, admission *mousa.RequestAdmission, trail mousa.Trail) error {
	if admission == nil {
		return nil
	}
	state, code := mousa.RequestTrailState(trail)
	return finishRequest(ctx, conn, *admission, state, code, mousa.RequestEvidence{TrailID: &trail.ID})
}

// preparedQuery uses store-owned statements inside a read snapshot.
type preparedQuery struct {
	*sql.Tx
	statements map[string]*sql.Stmt
}

func (q preparedQuery) QueryRowContext(ctx context.Context, statement string, args ...any) *sql.Row {
	if stmt := q.statements[statement]; stmt != nil {
		return stmt.QueryRowContext(ctx, args...)
	}
	return q.Tx.QueryRowContext(ctx, statement, args...)
}

func (q preparedQuery) QueryContext(ctx context.Context, statement string, args ...any) (*sql.Rows, error) {
	if stmt := q.statements[statement]; stmt != nil {
		return stmt.QueryContext(ctx, args...)
	}
	return q.Tx.QueryContext(ctx, statement, args...)
}

func (store *Store) readQuerySnapshot(ctx context.Context, req mousa.TrailRequest, grants, noteGrants map[mousa.SourceID]bool) (mousa.QueryResult, error) {
	finishQueue := store.StartStage(mousa.StageQueue)
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	finishQueue(err)
	if err != nil {
		return mousa.QueryResult{}, classify("begin query snapshot", err)
	}
	defer tx.Rollback()
	// The transaction owns these bindings; the store owns the parent statements.
	q := preparedQuery{Tx: tx, statements: make(map[string]*sql.Stmt, len(store.queryStmts))}
	for statement, stmt := range store.queryStmts {
		q.statements[statement] = tx.StmtContext(ctx, stmt)
	}
	result, err := store.querySnapshot(ctx, q, req, grants, noteGrants)
	if err != nil {
		return mousa.QueryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return mousa.QueryResult{}, classify("finish query snapshot", err)
	}
	return result, nil
}

func (store *Store) querySnapshot(ctx context.Context, q queryer, req mousa.TrailRequest, grants, noteGrants map[mousa.SourceID]bool) (mousa.QueryResult, error) {
	// The access stage covers scope resolution, grant checks, and each
	// source's verified access decision — the reads that gate evidence.
	finishAccess := store.StartStage(mousa.StageAccess)
	if req.Scope.Kind == "collection" {
		sources, err := collectionSources(ctx, q, req.Scope.Name)
		if err != nil {
			finishAccess(err)
			return mousa.QueryResult{}, err
		}
		req.Scope.Sources = sources
	}
	if err := requireSourceGrants(req.Scope.Sources, grants); err != nil {
		finishAccess(err)
		return mousa.QueryResult{}, err
	}
	trail := mousa.Trail{Request: req, Sources: make([]mousa.SourceDecision, 0, len(req.Scope.Sources))}
	if err := q.QueryRowContext(ctx, logIndexSQL).Scan(&trail.AsOf); err != nil {
		finishAccess(err)
		return mousa.QueryResult{}, classify("query log index", err)
	}
	storeID, err := getStoreID(ctx, q)
	if err != nil {
		finishAccess(err)
		return mousa.QueryResult{}, err
	}
	trail.StoreID = storeID.String()
	var allowed []mousa.SourceID
	allowedSet := make(map[mousa.SourceID]bool, len(req.Scope.Sources))
	for _, id := range req.Scope.Sources {
		decision, err := currentSourceDecision(ctx, q, id)
		if err != nil {
			finishAccess(err)
			return mousa.QueryResult{}, err
		}
		if decision.Outcome == "allow" {
			head, err := sourceHeadAt(ctx, q, id, trail.AsOf)
			if err != nil {
				finishAccess(err)
				return mousa.QueryResult{}, err
			}
			decision.Head = &head
			allowed = append(allowed, id)
			allowedSet[id] = true
		}
		if err := checkTrailSource(ctx, q, trail.AsOf, decision); err != nil {
			finishAccess(err)
			return mousa.QueryResult{}, err
		}
		trail.Sources = append(trail.Sources, decision)
	}
	finishAccess(nil)
	passages := []mousa.Passage{}
	if len(allowed) != 0 {
		passages, err = store.retrieveForTrail(ctx, q, &trail, allowed, allowedSet, noteGrants)
		if err != nil {
			return mousa.QueryResult{}, err
		}
	} else {
		// A scope with no allowed source never reaches retrieval.
		for _, stage := range []mousa.Stage{mousa.StageRetrieval, mousa.StageRank, mousa.StagePack} {
			store.ObserveMarker(stage, mousa.StagePhaseSkip, 0)
		}
	}
	sealed, err := mousa.NewTrail(trail)
	if err != nil {
		return mousa.QueryResult{}, wrap(CodeIntegrity, "seal query trail", err)
	}
	return mousa.QueryResult{Trail: sealed, Passages: passages}, nil
}

func requireSourceGrants(sources []mousa.SourceID, grants map[mousa.SourceID]bool) error {
	if grants != nil {
		for _, id := range sources {
			if !grants[id] {
				return readError("resolve scope", sql.ErrNoRows)
			}
		}
	}
	return nil
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
	location  passageLocation
}

func (store *Store) retrieveForTrail(ctx context.Context, q queryer, trail *mousa.Trail, allowed []mousa.SourceID, allowedSet, noteGrants map[mousa.SourceID]bool) ([]mousa.Passage, error) {
	req := trail.Request
	getProposals := store.propose
	if getProposals == nil {
		getProposals = proposeLexical
	}
	retrieval := &mousa.Retrieval{}
	if len(req.Texts) > 1 {
		retrieval.Fusion = "rrf-k60-v1"
	}
	reader := lexicalEvidenceReader{
		q:               q,
		representations: make(map[mousa.RepresentationID]mousa.Representation),
		artifacts:       make(map[mousa.ArtifactID]mousa.Artifact),
		observations:    make(map[mousa.ObservationID]mousa.Observation),
		sources:         make(map[mousa.SourceID]mousa.Source),
	}
	candidates := make(map[mousa.SegmentID]*queryCandidate)
	finishRetrieval := store.StartStage(mousa.StageRetrieval)
	for i, text := range req.Texts {
		quoted, err := mousa.PrepareLexicalTerms(text, false)
		if err != nil {
			finishRetrieval(err)
			return nil, wrap(CodeInvalidQuery, "prepare lexical query", err)
		}
		branch := mousa.Branch{Kind: "lexical", Scoring: mousa.ScopedBM25, TermPolicy: "original", Text: i}
		for _, term := range quoted {
			// The splitter does not retain quote characters.
			branch.Terms = append(branch.Terms, term[1:len(term)-1])
		}
		proposals, stats, err := getProposals(ctx, q, quoted, allowed, req.Limit)
		if err != nil {
			finishRetrieval(err)
			return nil, err
		}
		if stats != nil {
			branch.Scoring = mousa.SharedBM25
			branch.SharedBM25 = stats
			retrieval.Fusion = mousa.SharedBM25
			if len(req.Texts) > 1 {
				retrieval.Fusion = mousa.SharedBM25RRF
			}
		}
		branch.Proposed = len(proposals)
		if len(proposals) > req.Limit {
			err := integrity("query proposals", "proposer exceeded the candidate limit")
			finishRetrieval(err)
			return nil, err
		}
		for _, proposed := range proposals {
			row, exists := candidates[proposed.SegmentID]
			if !exists {
				row, err = reader.checkProposal(ctx, proposed.SegmentID, allowedSet)
				switch {
				case IsCode(err, CodeNotFound):
					branch.DroppedUnknown++
					continue
				case err != nil:
					finishRetrieval(err)
					return nil, err
				case row == nil:
					branch.DroppedNotAllowed++
					continue
				}
				candidates[proposed.SegmentID] = row
			}
			if proposed.Sources != nil && !slices.Equal(proposed.Sources, row.candidate.SourceIDs) {
				err := integrity("verify query scope", "proposal shard disagrees with canonical ancestry")
				finishRetrieval(err)
				return nil, err
			}
			if proposed.ContentSHA256 != row.candidate.ContentSHA256 {
				row.candidate.Rejected = []string{"content_mismatch"}
			}
			row.candidate.Scores = append(row.candidate.Scores, mousa.TrailScore{
				Branch: i, Level: "block", BM25: proposed.Match, ValueBits: fmt.Sprintf("%016x", math.Float64bits(proposed.Score)),
			})
		}
		retrieval.Branches = append(retrieval.Branches, branch)
	}
	finishRetrieval(nil)
	rows := make([]mousa.Candidate, 0, len(candidates))
	for _, row := range candidates {
		rows = append(rows, row.candidate)
	}
	finishRank := store.StartStage(mousa.StageRank)
	err := mousa.RankCandidates(rows, len(req.Texts))
	finishRank(err)
	if err != nil {
		return nil, wrap(CodeIntegrity, "rank candidates", err)
	}
	finishPack := store.StartStage(mousa.StagePack)
	// Record snapshot exclusions without changing the ranked candidates.
	supersession := []mousa.SupersessionEffect{}
	var marked map[mousa.SegmentID][]mousa.SHA256
	if req.IncludeSuperseded {
		marked = make(map[mousa.SegmentID][]mousa.SHA256)
	}
	for i := range rows {
		candidate := &rows[i]
		if len(candidate.Rejected) != 0 {
			continue
		}
		if req.Scope.Kind == "collection" {
			row := candidates[candidate.SegmentID]
			location, err := reader.queryPassageLocation(ctx, row)
			if err != nil {
				finishPack(err)
				return nil, err
			}
			row.location = location
			if strings.HasPrefix(location.path, "_") {
				candidate.Omitted = mousa.MetaPathV1
			}
		}
		if len(candidate.SourceIDs) != 1 {
			continue
		}
		effect, err := noteSupersessionEffect(ctx, q, candidate.SourceIDs[0], candidate.RepresentationID, candidate.SegmentID, trail.AsOf)
		if err != nil {
			finishPack(err)
			return nil, err
		}
		if effect != nil {
			supersession = append(supersession, *effect)
			if marked != nil {
				marked[effect.SegmentID] = effect.SupersededBy
			}
		}
	}
	if len(supersession) == 0 {
		supersession = nil
	} else {
		slices.SortFunc(supersession, func(a, b mousa.SupersessionEffect) int {
			return bytes.Compare(a.SegmentID[:], b.SegmentID[:])
		})
	}
	trail.Supersession = supersession
	var packSupersession []mousa.SupersessionEffect
	if !req.IncludeSuperseded {
		packSupersession = supersession
	}
	packing := mousa.ApplyFirstFit(rows, req.Budget.Limit, packSupersession)
	trail.Candidates, trail.Packing, trail.Retrieval = rows, &packing, retrieval
	passages := []mousa.Passage{}
	for _, candidate := range rows {
		if candidate.Packed != "full" {
			continue
		}
		row := candidates[candidate.SegmentID]
		passage, err := reader.queryPassage(ctx, row, noteGrants)
		if err != nil {
			finishPack(err)
			return nil, err
		}
		passage.Rank = candidate.Rank
		if marked != nil && passage.Note != nil {
			passage.Note.SupersededBy = marked[candidate.SegmentID]
		}
		passages = append(passages, passage)
	}
	finishPack(nil)
	return passages, nil
}

const noteSupersessionEffectSQL = `SELECT n.id, r.id, r.record_json, l.source_id, l.leaf_hash
		FROM local_items li
		JOIN notes n ON n.source_id = li.source_id AND n.item_id = li.item_id
		JOIN note_supersessions ns ON ns.target_id = n.id
		JOIN notes r ON r.id = ns.replacement_id
		LEFT JOIN log l ON l.kind = 'note' AND l.record_id = r.id
		WHERE li.source_id = ? AND li.representation_id = ? AND li.active = 1
			AND (l.seq IS NULL OR l.seq <= ?)
		ORDER BY r.id`

// noteSupersessionEffect checks whether one candidate segment's note was
// already superseded at asOf. The replacement IDs come from the projection
// but are trusted only after each canonical record and its log leaf confirm
// the edge; a projection row that disagrees with the durable record fails
// integrity. It returns nil for documents and unmodified notes.
func noteSupersessionEffect(ctx context.Context, q queryer, sourceID mousa.SourceID, reprID mousa.RepresentationID, segmentID mousa.SegmentID, asOf uint64) (*mousa.SupersessionEffect, error) {
	rows, err := q.QueryContext(ctx, noteSupersessionEffectSQL, sourceID[:], reprID[:], asOf)
	if err != nil {
		return nil, classify("note supersession effect", err)
	}
	defer rows.Close()
	var targetID mousa.SHA256
	supersededBy := []mousa.SHA256{}
	for rows.Next() {
		var target, replacement, record, leafSource, leaf []byte
		if err := rows.Scan(&target, &replacement, &record, &leafSource, &leaf); err != nil {
			return nil, classify("note supersession effect", err)
		}
		if len(target) != len(targetID) || len(replacement) != len(targetID) {
			return nil, integrity("note supersession effect", "invalid note identity")
		}
		if len(supersededBy) == 0 {
			copy(targetID[:], target)
		} else if !bytes.Equal(target, targetID[:]) {
			return nil, integrity("note supersession effect", "supersession rows name different targets")
		}
		if err := checkLeaf(mousa.LogKindNote, leafSource, leaf, record, sourceID[:]); err != nil {
			return nil, integrity("note supersession effect", err.Error())
		}
		note, err := decodeCanonical(record, mousa.DecodeNote, mousa.EncodeNote)
		if err != nil {
			return nil, integrity("note supersession effect", "replacement record is not canonical")
		}
		var replacementID mousa.SHA256
		copy(replacementID[:], replacement)
		if note.ID != replacementID || note.SourceID != sourceID || !slices.Contains(note.Supersedes, targetID) {
			return nil, integrity("note supersession effect", "replacement record disagrees with the projection")
		}
		supersededBy = append(supersededBy, replacementID)
	}
	if err := rows.Err(); err != nil {
		return nil, classify("note supersession effect", err)
	}
	if len(supersededBy) == 0 {
		return nil, nil
	}
	return &mousa.SupersessionEffect{NoteID: targetID, SegmentID: segmentID, SupersededBy: supersededBy}, nil
}

func (r lexicalEvidenceReader) checkProposal(ctx context.Context, id mousa.SegmentID, allowed map[mousa.SourceID]bool) (*queryCandidate, error) {
	segment, err := getSegment(ctx, r.q, id)
	if err != nil {
		return nil, err
	}
	paths, err := r.queryEvidencePaths(ctx, segment)
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
	text, err := indexedSegmentText(ctx, r.q, segment)
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
	return (lexicalEvidenceReader{q: q}).queryEvidencePaths(ctx, segment)
}

func (r lexicalEvidenceReader) queryEvidencePaths(ctx context.Context, segment mousa.Segment) ([]mousa.EvidencePath, error) {
	repr, err := r.representation(ctx, segment.RepresentationID)
	if err != nil {
		return nil, lexicalStructuralError("verify query representation", err)
	}
	if err := segment.ValidateAgainst(repr); err != nil {
		return nil, wrap(CodeIntegrity, "verify query coordinates", err)
	}
	return r.paths(ctx, segment)
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

type passageLocation struct {
	item, path    string
	activationSeq uint64
}

func queryPassageItem(ctx context.Context, q queryer, paths []mousa.EvidencePath, id mousa.SourceID) (string, bool, error) {
	return (lexicalEvidenceReader{q: q}).queryPassageItem(ctx, paths, id)
}

func (r lexicalEvidenceReader) queryPassageItem(ctx context.Context, paths []mousa.EvidencePath, id mousa.SourceID) (string, bool, error) {
	var path mousa.EvidencePath
	for _, p := range paths {
		if p.SourceID == id {
			path = p
			break
		}
	}
	observation, err := r.observation(ctx, path.ObservationID)
	if err != nil {
		return "", false, err
	}
	item := observation.ExternalObservationID
	rest, isItem := strings.CutPrefix(item, "item/")
	if isItem {
		artifact, err := r.artifact(ctx, path.ArtifactID)
		if err != nil {
			return "", false, err
		}
		var ok bool
		item, ok = strings.CutSuffix(rest, localRevisionSuffix(artifact.ContentSHA256))
		if !ok || item == "" {
			return "", false, integrity("query passage item", "item revision identity disagrees with artifact")
		}
	}
	return item, isItem, nil
}

func (r lexicalEvidenceReader) queryPassageLocation(ctx context.Context, row *queryCandidate) (passageLocation, error) {
	candidate := row.candidate
	id := candidate.SourceIDs[0]
	item, isItem, err := r.queryPassageItem(ctx, row.paths, id)
	if err != nil {
		return passageLocation{}, err
	}
	location := passageLocation{item: item, path: mousa.DefaultNotePath}
	if isItem {
		stored, seq, err := localItemLocation(ctx, r.q, id, item)
		switch {
		case err == nil:
			location.path, location.activationSeq = stored, seq
		case IsCode(err, CodeNotFound):
		default:
			return passageLocation{}, err
		}
	}
	return location, nil
}

func (r lexicalEvidenceReader) queryPassage(ctx context.Context, row *queryCandidate, noteGrants map[mousa.SourceID]bool) (mousa.Passage, error) {
	candidate := row.candidate
	location := row.location
	if location.item == "" {
		var err error
		location, err = r.queryPassageLocation(ctx, row)
		if err != nil {
			return mousa.Passage{}, err
		}
	}
	id := candidate.SourceIDs[0]
	passage := mousa.Passage{
		SourceID: id, Item: location.item, SegmentID: candidate.SegmentID, Range: candidate.Range,
		ContentSHA256: candidate.ContentSHA256, Text: row.text, Path: location.path,
	}
	repr, err := r.representation(ctx, candidate.RepresentationID)
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
		passage.Note, err = queryNoteEvidence(ctx, r.q, id, location.item, noteGrants)
		if err != nil {
			return mousa.Passage{}, err
		}
	} else if location.activationSeq != 0 && (passage.SegmentPolicy == mousa.TextSegmentFixedV1 || passage.SegmentPolicy == mousa.TextSegmentPassageV1) {
		passage.ActivatedAtUsec, err = readItemActivationTime(ctx, r.q, location.activationSeq, id, location.item, repr.ID)
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

package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/graydeon/mousa/internal/mousa"
)

const queryTrailKind = "trail"

type TrailListFilter struct {
	Source  mousa.SourceID
	Segment mousa.SegmentID
	Since   uint64
}

type TrailListEntry struct {
	ID              mousa.SourceTrailID `json:"id"`
	Seq             uint64              `json:"seq"`
	RequestedAtUsec uint64              `json:"requested_at_usec"`
	ResultSHA256    mousa.SHA256        `json:"result_sha256"`
}

// ListTrails streams verified metadata in log order through the owner view.
// The lookup names all candidates, but a segment filter selects releases only.
func (store *Store) ListTrails(ctx context.Context, filter TrailListFilter, emit func(TrailListEntry) error) error {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return classify("begin trail list", err)
	}
	defer tx.Rollback()
	var source, segment any
	if filter.Source != (mousa.SourceID{}) {
		source = filter.Source[:]
	}
	if filter.Segment != (mousa.SegmentID{}) {
		segment = filter.Segment[:]
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT l.seq, l.record_id FROM log l WHERE l.kind = 'trail' AND l.seq > ?1
		AND (?2 IS NULL OR EXISTS (SELECT 1 FROM trail_sources s WHERE s.trail_id = l.record_id AND s.source_id = ?2))
		AND (?3 IS NULL OR EXISTS (SELECT 1 FROM trail_segments s WHERE s.trail_id = l.record_id AND s.segment_id = ?3))
		ORDER BY l.seq`, filter.Since, source, segment)
	if err != nil {
		return classify("list trails", err)
	}
	defer rows.Close()
	for rows.Next() {
		var entry TrailListEntry
		var raw []byte
		if err := rows.Scan(&entry.Seq, &raw); err != nil {
			return classify("scan trail list", err)
		}
		if len(raw) != len(entry.ID) {
			return integrity("scan trail list", "invalid trail ID length")
		}
		entry.ID = mousa.SourceTrailID(raw)
		trail, _, err := readTrailRecord(ctx, tx, entry.ID)
		if IsCode(err, CodeNotFound) {
			return integrity("list trails", "log leaf has no matching trail")
		}
		if err != nil {
			return err
		}
		if err := checkTrailLookups(ctx, tx, trail); err != nil {
			return err
		}
		if segment != nil && !slices.ContainsFunc(trail.Candidates, func(candidate mousa.Candidate) bool {
			return candidate.SegmentID == filter.Segment && candidate.Packed != "none"
		}) {
			continue
		}
		entry.RequestedAtUsec, entry.ResultSHA256 = trail.Request.RequestedAtUsec, trail.ResultSHA256
		if err := emit(entry); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return classify("finish listing trails", err)
	}
	return classify("finish trail list", tx.Commit())
}

func verifyQueryTrails(ctx context.Context, db *sql.DB, histories pathHistories) (int64, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, classify("begin verify trails", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM trails ORDER BY id`)
	if err != nil {
		return 0, classify("scan trails", err)
	}
	defer rows.Close()
	var unverifiable int64
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return 0, classify("scan trail ID", err)
		}
		var id mousa.SourceTrailID
		if len(raw) != len(id) {
			return 0, integrity("scan trail ID", "invalid trail ID length")
		}
		copy(id[:], raw)
		trail, _, err := readTrailRecord(ctx, tx, id)
		if err != nil {
			return 0, err
		}
		count, err := verifyTrailReferences(ctx, tx, trail, histories)
		if err != nil {
			return 0, err
		}
		unverifiable += count
	}
	if err := rows.Err(); err != nil {
		return 0, classify("finish scanning trails", err)
	}
	if err := classify("finish verify trails", tx.Commit()); err != nil {
		return 0, err
	}
	return unverifiable, nil
}

func sourceHeadAt(ctx context.Context, q queryer, source mousa.SourceID, asOf uint64) (mousa.SourceHead, error) {
	var head mousa.SourceHead
	var kind string
	var recordID, leafSource, hash, record, recordSource []byte
	// Source-history toggles are setting evidence, not retrieval evidence:
	// they never become the source head a trail pins.
	err := q.QueryRowContext(ctx, logRecordSQL+` WHERE l.source_id = ? AND l.seq <= ? AND l.kind != 'source_history'
		ORDER BY l.seq DESC LIMIT 1`, source[:], asOf).
		Scan(&head.Seq, &kind, &recordID, &leafSource, &hash, &record, &recordSource)
	if errors.Is(err, sql.ErrNoRows) {
		return head, integrity("read source head", "allowed source has no log head at snapshot")
	}
	if err != nil {
		return head, classify("read source head", err)
	}
	if err := checkLeaf(kind, leafSource, hash, record, recordSource); err != nil {
		return head, wrap(CodeIntegrity, "read source head", err)
	}
	copy(head.LeafSHA256[:], hash)
	return head, nil
}

func putTrail(ctx context.Context, conn *sql.Conn, trail mousa.Trail) error {
	data, err := mousa.EncodeTrail(trail)
	if err != nil {
		return wrap(CodeIntegrity, "encode query trail", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO trails(id, requested_at_usec, record_json) VALUES(?, ?, ?)`,
		trail.ID[:], trail.Request.RequestedAtUsec, data); err != nil {
		return classify("write query trail", err)
	}
	for _, source := range trail.Sources {
		if _, err := conn.ExecContext(ctx, `INSERT INTO trail_sources(trail_id, source_id, outcome) VALUES(?, ?, ?)`,
			trail.ID[:], source.SourceID[:], source.Outcome); err != nil {
			return classify("write query trail source", err)
		}
	}
	if len(trail.Candidates) > 0 {
		insert, err := conn.PrepareContext(ctx, `INSERT INTO trail_segments(trail_id, ordinal, segment_id) VALUES(?, ?, ?)`)
		if err != nil {
			return classify("prepare query trail segments", err)
		}
		defer insert.Close()
		for ordinal, candidate := range trail.Candidates {
			if _, err := insert.ExecContext(ctx, trail.ID[:], ordinal, candidate.SegmentID[:]); err != nil {
				return classify("write query trail segment", err)
			}
		}
	}
	if err := appendLog(ctx, conn, queryTrailKind, trail.ID[:], nil, data); err != nil {
		return err
	}
	written, persisted, err := readTrailRecord(ctx, conn, trail.ID)
	if err != nil {
		return err
	}
	if !bytes.Equal(persisted, data) {
		return integrity("write query trail", "exact read-back disagrees with write")
	}
	// Query already verified these references in its read snapshot. Check
	// what was just written without rereading candidates under the write lock.
	return checkTrailLookups(ctx, conn, written)
}

// GetTrail reads a verified historical trail without writing anything. A nil
// grant set means the owner. Ordinary callers see not-found when any formerly
// allowed source is not configured or does not currently allow retrieval.
func (store *Store) GetTrail(ctx context.Context, id mousa.SourceTrailID, grants map[mousa.SourceID]bool) (mousa.Trail, error) {
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return mousa.Trail{}, classify("begin trail read", err)
	}
	defer tx.Rollback()
	trail, _, err := readTrailRecord(ctx, tx, id)
	if err != nil {
		if grants != nil && IsCode(err, CodeIntegrity) {
			return mousa.Trail{}, missingTrail()
		}
		return mousa.Trail{}, err
	}
	if grants != nil {
		if err := checkTrailAccess(ctx, tx, trail, grants); err != nil {
			if IsCode(err, CodeIntegrity) {
				return mousa.Trail{}, missingTrail()
			}
			return mousa.Trail{}, err
		}
	}
	if err := checkTrailReferences(ctx, tx, trail); err != nil {
		return mousa.Trail{}, err
	}
	if err := tx.Commit(); err != nil {
		return mousa.Trail{}, classify("finish trail read", err)
	}
	return trail, nil
}

func checkTrailAccess(ctx context.Context, q queryer, trail mousa.Trail, grants map[mousa.SourceID]bool) error {
	for _, source := range trail.Sources {
		if source.Outcome != "allow" {
			continue
		}
		if grants != nil && !grants[source.SourceID] {
			return missingTrail()
		}
		current, err := currentSourceDecision(ctx, q, source.SourceID)
		if err != nil {
			return err
		}
		if current.Outcome != "allow" {
			return missingTrail()
		}
	}
	return nil
}

func missingTrail() error { return readError("get query trail", sql.ErrNoRows) }

func readTrailRecord(ctx context.Context, q queryer, id mousa.SourceTrailID) (mousa.Trail, []byte, error) {
	var requested uint64
	var seq sql.NullInt64
	var data, source, hash []byte
	err := q.QueryRowContext(ctx, `SELECT t.requested_at_usec, t.record_json, l.seq, l.source_id, l.leaf_hash
		FROM trails t LEFT JOIN log l ON l.kind = 'trail' AND l.record_id = t.id WHERE t.id = ?`, id[:]).
		Scan(&requested, &data, &seq, &source, &hash)
	if err != nil {
		return mousa.Trail{}, nil, readError("get query trail", err)
	}
	trail, err := mousa.DecodeTrail(data)
	if err != nil {
		return mousa.Trail{}, nil, wrap(CodeIntegrity, "decode query trail", err)
	}
	if trail.ID != id || trail.Request.RequestedAtUsec != requested {
		return mousa.Trail{}, nil, integrity("get query trail", "trail identity or time projection disagrees")
	}
	if !seq.Valid || seq.Int64 <= 0 || uint64(seq.Int64) <= trail.AsOf || source != nil {
		return mousa.Trail{}, nil, integrity("get query trail", "trail leaf is missing or does not follow its snapshot")
	}
	if err := checkLeaf(queryTrailKind, source, hash, data, nil); err != nil {
		return mousa.Trail{}, nil, wrap(CodeIntegrity, "get query trail", err)
	}
	storeID, err := getStoreID(ctx, q)
	if err != nil {
		return mousa.Trail{}, nil, err
	}
	if trail.StoreID != storeID.String() {
		return mousa.Trail{}, nil, integrity("get query trail", "trail belongs to a different store")
	}
	return trail, data, nil
}

func checkTrailReferences(ctx context.Context, q queryer, trail mousa.Trail) error {
	if err := checkTrailLookups(ctx, q, trail); err != nil {
		return err
	}
	for _, source := range trail.Sources {
		if err := checkTrailSource(ctx, q, trail.AsOf, source); err != nil {
			return err
		}
	}
	for _, candidate := range trail.Candidates {
		if err := checkTrailCandidate(ctx, q, candidate); err != nil {
			return err
		}
	}
	// Every eligible candidate's effect must recompute identically at the
	// trail's own snapshot — a forged effect, a dropped effect, and one
	// naming an ineligible or unknown candidate all fail.
	declared := make(map[mousa.SegmentID]mousa.SupersessionEffect, len(trail.Supersession))
	for _, effect := range trail.Supersession {
		declared[effect.SegmentID] = effect
	}
	for i := range trail.Candidates {
		candidate := &trail.Candidates[i]
		effect, listed := declared[candidate.SegmentID]
		if listed {
			delete(declared, candidate.SegmentID)
		}
		if len(candidate.Rejected) != 0 || len(candidate.SourceIDs) != 1 {
			if listed {
				return integrity("read query trail", "supersession effect names an ineligible candidate")
			}
			continue
		}
		current, err := noteSupersessionEffect(ctx, q, candidate.SourceIDs[0], candidate.RepresentationID, candidate.SegmentID, trail.AsOf)
		if err != nil {
			return err
		}
		if (current == nil) == listed {
			return integrity("read query trail", "supersession effect disagrees with the durable record")
		}
		if current != nil && (current.NoteID != effect.NoteID || !slices.Equal(current.SupersededBy, effect.SupersededBy)) {
			return integrity("read query trail", "supersession effect disagrees with the durable record")
		}
	}
	if len(declared) != 0 {
		return integrity("read query trail", "supersession effect names an unknown candidate")
	}
	return nil
}

func verifyTrailReferences(ctx context.Context, q queryer, trail mousa.Trail, histories pathHistories) (int64, error) {
	if err := checkTrailReferences(ctx, q, trail); err != nil {
		return 0, err
	}
	var unverifiable int64
	for _, candidate := range trail.Candidates {
		if candidate.Omitted != mousa.MetaPathV1 {
			continue
		}
		segment, err := getSegment(ctx, q, candidate.SegmentID)
		if err != nil {
			return 0, lexicalStructuralError("verify meta path omission", err)
		}
		paths, err := queryEvidencePaths(ctx, q, segment)
		if err != nil {
			return 0, err
		}
		source := candidate.SourceIDs[0]
		item, isItem, err := queryPassageItem(ctx, q, paths, source)
		if err != nil {
			return 0, err
		}
		path := mousa.DefaultNotePath
		known := true
		if isItem {
			if err := histories.loadSource(ctx, q, source); err != nil {
				return 0, err
			}
			path, known = histories[source][item].pathAtSeq(trail.AsOf)
		}
		if !known {
			unverifiable++
			continue
		}
		if !strings.HasPrefix(path, "_") {
			return 0, integrity("verify meta path omission",
				fmt.Sprintf("trail %s segment %s: meta-path-v1 path does not start with _", trail.ID, candidate.SegmentID))
		}
	}
	return unverifiable, nil
}

func checkTrailLookups(ctx context.Context, q queryer, trail mousa.Trail) error {
	sources, err := q.QueryContext(ctx, `SELECT source_id, outcome FROM trail_sources WHERE trail_id = ? ORDER BY source_id`, trail.ID[:])
	if err != nil {
		return classify("read query trail sources", err)
	}
	defer sources.Close()
	i := 0
	for ; sources.Next(); i++ {
		var raw []byte
		var outcome string
		if err := sources.Scan(&raw, &outcome); err != nil {
			return classify("read query trail source", err)
		}
		if i >= len(trail.Sources) || !bytes.Equal(raw, trail.Sources[i].SourceID[:]) || outcome != trail.Sources[i].Outcome {
			return integrity("read query trail", "source lookup projection disagrees with trail")
		}
	}
	if err := sources.Err(); err != nil {
		return classify("finish query trail sources", err)
	}
	if i != len(trail.Sources) {
		return integrity("read query trail", "source lookup rows are missing")
	}

	segments, err := q.QueryContext(ctx, `SELECT ordinal, segment_id FROM trail_segments WHERE trail_id = ? ORDER BY ordinal`, trail.ID[:])
	if err != nil {
		return classify("read query trail segments", err)
	}
	defer segments.Close()
	i = 0
	for ; segments.Next(); i++ {
		var ordinal int
		var raw []byte
		if err := segments.Scan(&ordinal, &raw); err != nil {
			return classify("read query trail segment", err)
		}
		if i >= len(trail.Candidates) || ordinal != i || !bytes.Equal(raw, trail.Candidates[i].SegmentID[:]) {
			return integrity("read query trail", "segment lookup projection disagrees with trail")
		}
	}
	if err := segments.Err(); err != nil {
		return classify("finish query trail segments", err)
	}
	if i != len(trail.Candidates) {
		return integrity("read query trail", "segment lookup rows are missing")
	}
	return nil
}

func checkTrailSource(ctx context.Context, q queryer, asOf uint64, source mousa.SourceDecision) error {
	id := source.SourceID
	var eventID, leaf []byte
	err := q.QueryRowContext(ctx, `SELECT record_id, leaf_hash FROM log
		WHERE source_id = ? AND kind = 'access_event' AND seq <= ? ORDER BY seq DESC LIMIT 1`, id[:], asOf).
		Scan(&eventID, &leaf)
	switch {
	case errors.Is(err, sql.ErrNoRows) && source.AccessEventID == nil:
	case errors.Is(err, sql.ErrNoRows):
		return integrity("verify trail source", "cited access event leaf is missing")
	case err != nil:
		return classify("verify trail access event", err)
	default:
		if source.AccessEventID == nil || !bytes.Equal(eventID, source.AccessEventID[:]) {
			return integrity("verify trail source", "access event disagrees with snapshot")
		}
		event, data, err := getAccessEvent(ctx, q, *source.AccessEventID)
		if err != nil {
			return lexicalStructuralError("verify trail access event", err)
		}
		if event.Access != source.Access {
			return integrity("verify trail source", "access state disagrees with cited event")
		}
		if err := checkLeaf(mousa.LogKindAccessEvent, id[:], leaf, data, event.SourceID[:]); err != nil {
			return wrap(CodeIntegrity, "verify trail access event", err)
		}
	}
	if source.WithdrawalID != nil {
		withdrawal, err := getSourceWithdrawal(ctx, q, *source.WithdrawalID)
		if err != nil {
			return lexicalStructuralError("verify trail withdrawal", err)
		}
		data, err := mousa.EncodeSourceWithdrawal(withdrawal)
		if err != nil {
			return wrap(CodeIntegrity, "verify trail withdrawal", err)
		}
		var rawSource, hash []byte
		err = q.QueryRowContext(ctx, `SELECT source_id, leaf_hash FROM log WHERE kind = 'source_withdrawal' AND record_id = ? AND seq <= ?`,
			source.WithdrawalID[:], asOf).Scan(&rawSource, &hash)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !bytes.Equal(rawSource, id[:])) {
			return integrity("verify trail source", "cited withdrawal leaf is missing or belongs to another source")
		}
		if err != nil {
			return classify("verify trail withdrawal", err)
		}
		if err := checkLeaf(mousa.LogKindSourceWithdrawal, rawSource, hash, data, withdrawal.SourceID[:]); err != nil {
			return wrap(CodeIntegrity, "verify trail withdrawal", err)
		}
	}
	if source.Head != nil {
		head, err := sourceHeadAt(ctx, q, id, asOf)
		if err != nil {
			return err
		}
		if head != *source.Head {
			return integrity("verify trail source", "recorded head disagrees with snapshot")
		}
	}
	return nil
}

func checkTrailCandidate(ctx context.Context, q queryer, candidate mousa.Candidate) error {
	segment, err := getSegment(ctx, q, candidate.SegmentID)
	if err != nil {
		return lexicalStructuralError("verify trail candidate", err)
	}
	byteRange, ok := segment.Selector.TextByteRange()
	if !ok || candidate.Range.Start != byteRange.Start || candidate.Range.End != byteRange.End ||
		candidate.ContentSHA256 != segment.ContentSHA256 || candidate.RepresentationID != segment.RepresentationID {
		return integrity("verify trail candidate", "trail candidate disagrees with canonical segment")
	}
	paths, err := queryEvidencePaths(ctx, q, segment)
	if err != nil {
		return err
	}
	if !slices.Equal(candidate.SourceIDs, evidenceSourceIDs(paths)) {
		return integrity("verify trail candidate", "trail candidate source ancestry disagrees")
	}
	if len(candidate.Rejected) != 0 {
		return nil
	}
	var text string
	var digest []byte
	err = q.QueryRowContext(ctx, `SELECT f.text, f.content_sha256
		FROM segment_lexical_rows r JOIN segment_lexical_fts f ON f.rowid = r.rowid
		WHERE r.segment_id = ?`, segment.ID[:]).Scan(&text, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		for _, path := range paths {
			retired, err := trailPathRetired(ctx, q, path, segment.RepresentationID)
			if err != nil {
				return err
			}
			if retired {
				return nil
			}
		}
		return integrity("verify trail candidate", "packed segment has no text and is not retired")
	}
	if err != nil {
		return classify("verify trail text", err)
	}
	if uint64(len(text)) != byteRange.End-byteRange.Start {
		return integrity("verify trail candidate", "packed text disagrees with candidate range")
	}
	return verifyLexicalPayload(segment, []byte(text), digest)
}

func trailPathRetired(ctx context.Context, q queryer, path mousa.EvidencePath, repr mousa.RepresentationID) (bool, error) {
	observation, err := getObservation(ctx, q, path.ObservationID)
	if err != nil {
		return false, lexicalStructuralError("get trail item observation", err)
	}
	external, local := strings.CutPrefix(observation.ExternalObservationID, "item/")
	if !local {
		return false, nil
	}
	artifact, err := getArtifact(ctx, q, path.ArtifactID)
	if err != nil {
		return false, lexicalStructuralError("get trail item artifact", err)
	}
	item, ok := strings.CutSuffix(external, localRevisionSuffix(artifact.ContentSHA256))
	if !ok || item == "" {
		return false, integrity("verify trail candidate", "item revision identity disagrees with artifact")
	}
	var retired bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM local_items WHERE source_id = ? AND item_id = ?
		AND (active = 0 OR representation_id != ?))`, path.SourceID[:], item, repr[:]).Scan(&retired)
	return retired, classify("check trail retirement", err)
}

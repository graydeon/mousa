package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func auditRequest(source mousa.SourceID) mousa.QueryRequest {
	return mousa.QueryRequest{
		Scope: mousa.Scope{Source: source}, Texts: []string{"harbor"},
		Budget: mousa.Budget{Limit: 8192, Unit: mousa.BudgetBytes}, Caller: "audit-test",
	}
}

func seedAuditItem(t *testing.T, s *Store, name, text string) (mousa.SourceID, mousa.Segment) {
	t.Helper()
	return seedAuditItemAtPath(t, s, name, nil, text)
}

func seedAuditItemAtPath(t *testing.T, s *Store, name string, path *string, text string) (mousa.SourceID, mousa.Segment) {
	t.Helper()
	source, repr, normalized := prepareLocalRevision(t, s, name, text)
	segments, err := mousa.SegmentUTF8Text(repr, normalized)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateLocalItem(t.Context(), source.ID, name, repr.ID, normalized, path, ""); err != nil {
		t.Fatal(err)
	}
	return source.ID, segments[0]
}

func TestQueryFaultyProposalsDoNotReleaseEvidence(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	a, good := seedAuditItem(t, s, "allowed", "harbor allowed evidence")
	_, sameSource := seedAuditItem(t, s, "wrong-digest", "harbor evidence for rejection")
	if _, _, err := s.SetSourceAccess(ctx, a, mousa.SourceAccessAllow, "test"); err != nil {
		t.Fatal(err)
	}
	b := testSourceNamed(t, "denied")
	if err := s.PutSource(ctx, b); err != nil {
		t.Fatal(err)
	}
	oid, err := mousa.NewObservationID(b.ID, "test observation")
	if err != nil {
		t.Fatal(err)
	}
	obs := mousa.Observation{ID: oid, Schema: mousa.ObservationSchema, SourceID: b.ID, ExternalObservationID: "test observation"}
	if err := s.PutObservation(ctx, obs); err != nil {
		t.Fatal(err)
	}
	denied := addEnforcedDocument(t, s, obs, "one", "harbor denied evidence").segments[0]
	if _, _, err := s.SetSourceAccess(ctx, b.ID, mousa.SourceAccessDeny, "test"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name                string
		fault               proposal
		notAllowed, unknown int
		retained            bool
	}{
		{"denied", proposal{SegmentID: denied.ID, ContentSHA256: denied.ContentSHA256, Score: -4}, 1, 0, false},
		{"stale digest", proposal{SegmentID: sameSource.ID, ContentSHA256: good.ContentSHA256, Score: -4}, 0, 0, true},
		{"unknown", proposal{SegmentID: mousa.SegmentID{1}, ContentSHA256: good.ContentSHA256, Score: -4}, 0, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s.propose = func(_ context.Context, _ queryer, _ []string, sources []mousa.SourceID, _ int) ([]proposal, *mousa.BM25Stats, error) {
				if !slices.Equal(sources, []mousa.SourceID{a}) {
					t.Fatalf("proposer got %v, want only %v", sources, a)
				}
				return []proposal{tt.fault, {SegmentID: good.ID, ContentSHA256: good.ContentSHA256, Score: -2}}, nil, nil
			}
			result, err := s.Query(t.Context(), auditRequest(a))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Passages) != 1 || result.Passages[0].SegmentID != good.ID ||
				result.Passages[0].Rank != 1 || result.Passages[0].Text != "harbor allowed evidence" {
				t.Fatalf("verified passages changed: %+v", result.Passages)
			}
			branch := result.Trail.Retrieval.Branches[0]
			if branch.Proposed != 2 || branch.DroppedNotAllowed != tt.notAllowed || branch.DroppedUnknown != tt.unknown {
				t.Fatalf("fault not accounted for: %+v", branch)
			}
			for _, candidate := range result.Trail.Candidates {
				if candidate.SegmentID != tt.fault.SegmentID {
					continue
				}
				if !tt.retained || candidate.Rank != 0 || candidate.Packed != "none" ||
					!slices.Equal(candidate.Rejected, []string{"content_mismatch"}) {
					t.Fatalf("fault leaked or was not rejected: %+v", candidate)
				}
			}
			if (len(result.Trail.Candidates) == 2) != tt.retained {
				t.Fatalf("unexpected recorded candidates: %+v", result.Trail.Candidates)
			}
		})
	}
}

func TestQueryTrailLeafIsAtomic(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	if _, err := s.db.ExecContext(ctx, `CREATE TEMP TRIGGER refuse_trail_leaf BEFORE INSERT ON log
		WHEN NEW.kind = 'trail' BEGIN SELECT RAISE(ABORT, 'injected leaf failure'); END`); err != nil {
		t.Fatal(err)
	}
	result, err := s.Query(ctx, auditRequest(mousa.SourceID{1}))
	if err == nil || !reflect.DeepEqual(result, mousa.QueryResult{}) {
		t.Fatalf("failed commit returned result: %+v, %v", result, err)
	}
	for _, query := range []string{
		`SELECT count(*) FROM trails`, `SELECT count(*) FROM trail_sources`, `SELECT count(*) FROM log WHERE kind = 'trail'`,
	} {
		var count int
		if err := s.db.QueryRowContext(ctx, query).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s: count=%d err=%v, want zero", query, count, err)
		}
	}
}

func TestTrailReadRefusesCorruptRows(t *testing.T) {
	for _, tt := range []struct{ name, statement string }{
		{"canonical bytes", `UPDATE trails SET record_json = x'7b7d' WHERE id = ?`},
		{"leaf", `UPDATE log SET leaf_hash = zeroblob(32) WHERE kind = 'trail' AND record_id = ?`},
		{"time projection", `UPDATE trails SET requested_at_usec = requested_at_usec + 1 WHERE id = ?`},
		{"source lookup", `DELETE FROM trail_sources WHERE trail_id = ?`},
		{"segment lookup", `DELETE FROM trail_segments WHERE trail_id = ?`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := openLexicalStore(t)
			defer s.Close()
			ctx := t.Context()
			a, _ := seedAuditItem(t, s, "the-item", "harbor evidence")
			if _, _, err := s.SetSourceAccess(ctx, a, mousa.SourceAccessAllow, "test"); err != nil {
				t.Fatal(err)
			}
			result, err := s.Query(ctx, auditRequest(a))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetTrail(ctx, result.Trail.ID, nil); err != nil {
				t.Fatalf("read before corruption: %v", err)
			}
			if _, err := s.db.ExecContext(ctx, tt.statement, result.Trail.ID[:]); err != nil {
				t.Fatal(err)
			}
			if _, err := s.GetTrail(ctx, result.Trail.ID, nil); !IsCode(err, CodeIntegrity) {
				t.Fatalf("trail corruption returned %v, want integrity", err)
			}
		})
	}
}

func TestQueryTrailSnapshotAndAllDenied(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	a, _ := seedAuditItem(t, s, "allowed", "harbor evidence")
	if _, _, err := s.SetSourceAccess(ctx, a, mousa.SourceAccessAllow, "test"); err != nil {
		t.Fatal(err)
	}
	var seq uint64
	var leaf []byte
	if err := s.db.QueryRowContext(ctx, `SELECT seq, leaf_hash FROM log WHERE source_id = ? ORDER BY seq DESC LIMIT 1`, a[:]).Scan(&seq, &leaf); err != nil {
		t.Fatal(err)
	}
	first, err := s.Query(ctx, auditRequest(a))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Query(ctx, auditRequest(a))
	if err != nil {
		t.Fatal(err)
	}
	head := second.Trail.Sources[0].Head
	if head == nil || head.Seq != seq || head.LeafSHA256 != mousa.SHA256(leaf) ||
		first.Trail.ResultSHA256 != second.Trail.ResultSHA256 || first.Trail.ID == second.Trail.ID ||
		second.Trail.AsOf <= first.Trail.AsOf {
		t.Fatalf("unstable allowed source identity: first=%+v second=%+v", first.Trail, second.Trail)
	}
	denied, err := s.Query(ctx, auditRequest(mousa.SourceID{7}))
	if err != nil {
		t.Fatal(err)
	}
	if denied.Trail.Retrieval != nil || denied.Trail.Candidates != nil || denied.Trail.Packing != nil ||
		len(denied.Passages) != 0 || len(denied.Trail.Sources) != 1 || denied.Trail.Sources[0].Head != nil {
		t.Fatalf("denied query carried retrieval or a head: %+v", denied)
	}
	if _, err := s.GetTrail(ctx, denied.Trail.ID, nil); err != nil {
		t.Fatalf("denied trail not stored: %v", err)
	}
	if _, err := s.GetTrail(ctx, second.Trail.ID, map[mousa.SourceID]bool{}); !IsCode(err, CodeNotFound) {
		t.Fatalf("ordinary caller without the allowed source got %v, want not_found", err)
	}
}

func TestQueryRetriesWhenAllowIsRevokedBeforeTrailWrite(t *testing.T) {
	ctx := t.Context()
	s := openLexicalStore(t)
	defer s.Close()
	a, _ := seedAuditItem(t, s, "allowed", "harbor proof")
	if _, _, err := s.SetSourceAccess(ctx, a, mousa.SourceAccessAllow, "test"); err != nil {
		t.Fatal(err)
	}
	writer, err := Open(ctx, s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	s.propose = func(ctx context.Context, q queryer, quoted []string, sources []mousa.SourceID, limit int) ([]proposal, *mousa.BM25Stats, error) {
		if _, changed, err := writer.SetSourceAccess(ctx, a, mousa.SourceAccessDeny, "test"); err != nil || !changed {
			t.Fatalf("revoke during read snapshot: changed=%t err=%v", changed, err)
		}
		return proposeLexical(ctx, q, quoted, sources, limit)
	}
	result, err := s.Query(ctx, auditRequest(a))
	if err != nil || len(result.Passages) != 0 || result.Trail.Sources[0].Outcome != "deny" {
		t.Fatalf("query returned obsolete allow: %+v, %v", result, err)
	}
	var stored int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM trails`).Scan(&stored); err != nil || stored != 1 {
		t.Fatalf("discarded snapshot was logged: count=%d, err=%v", stored, err)
	}
}

func TestQueryPreparedStatementsCloseAndReopen(t *testing.T) {
	ctx := t.Context()
	s := openLexicalStore(t)
	a, segment := seedAuditItem(t, s, "allowed", "harbor prepared evidence")
	if _, _, err := s.SetSourceAccess(ctx, a, mousa.SourceAccessAllow, "test"); err != nil {
		t.Fatal(err)
	}
	first, err := s.Observed(nil).Query(ctx, auditRequest(a))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Observed(nil).Query(ctx, auditRequest(a))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Passages) != 1 || first.Passages[0].SegmentID != segment.ID {
		t.Fatalf("query lost the seeded passage: %+v", first.Passages)
	}
	if !reflect.DeepEqual(first.Passages, second.Passages) || first.Trail.ResultSHA256 != second.Trail.ResultSHA256 {
		t.Fatalf("repeated query changed the result: %+v vs %+v", first, second)
	}
	stmt := s.queryStmts[getSegmentSQL]
	if stmt == nil {
		t.Fatal("getSegmentSQL was not prepared at open")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stmt.QueryRowContext(ctx, segment.ID[:]).Err(); err == nil {
		t.Fatal("closed prepared statement still queries")
	}
	reopened, err := Open(ctx, s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.queryStmts[getSegmentSQL] == nil || reopened.queryStmts[getSegmentSQL] == stmt {
		t.Fatal("reopen did not prepare a fresh handle")
	}
	again, err := reopened.Observed(nil).Query(ctx, auditRequest(a))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Passages, again.Passages) || first.Trail.ResultSHA256 != again.Trail.ResultSHA256 {
		t.Fatalf("reopen changed the result: %+v vs %+v", first, again)
	}
}

func TestQueryLimitAppliesAfterSourceScope(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	a, weak := seedAuditItem(t, s, "allowed", "harbor with many unrelated words in the passage")
	allowSource(t, s, a)
	b := testPushBatch(t, "peer", nil)
	if err := s.ApplyIngest(ctx, b); err != nil {
		t.Fatal(err)
	}
	strong := addEnforcedDocument(t, s, b.Observation, "peer", "harbor harbor").segments[0]
	global, err := s.SearchLexical(ctx, "harbor", 1)
	if err != nil || len(global) != 1 || global[0].Segment.ID != strong.ID {
		t.Fatalf("fixture did not make the denied result outrank the allowed one: %+v, %v", global, err)
	}
	req := auditRequest(a)
	req.Limit = 1
	result, err := s.Query(ctx, req)
	if err != nil || len(result.Passages) != 1 || result.Passages[0].SegmentID != weak.ID {
		t.Fatalf("limit let a source outside the scope crowd out evidence: %+v, %v", result, err)
	}
}

func TestCollectionOmitsMetaPath(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	a, meta := seedAuditItem(t, s, "layout", "harbor harbor")
	_, regular := seedAuditItem(t, s, "normal", "harbor with many unrelated words in the passage")
	allowSource(t, s, a)
	moved, err := s.SetDocumentPath(ctx, a, "layout", "_guide/index", "test")
	if err != nil || !moved {
		t.Fatalf("move layout to meta path: moved=%t err=%v", moved, err)
	}
	moved, err = s.SetDocumentPath(ctx, a, "normal", "topics/_guide", "test")
	if err != nil || !moved {
		t.Fatalf("move normal below an underscore segment: moved=%t err=%v", moved, err)
	}
	if err := s.CreateCollection(ctx, "fleet"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollectionSource(ctx, "fleet", a, true); err != nil {
		t.Fatal(err)
	}
	b := testPushBatch(t, "peer", nil)
	if err := s.ApplyIngest(ctx, b); err != nil {
		t.Fatal(err)
	}
	allowSource(t, s, b.Source.ID)

	req := auditRequest(a)
	sourceResult, err := s.Query(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	collectionReq := req
	collectionReq.Scope = mousa.Scope{Collection: "fleet"}
	collectionResult, err := s.Query(ctx, collectionReq)
	if err != nil {
		t.Fatal(err)
	}
	listReq := req
	listReq.Scope = mousa.Scope{Sources: []mousa.SourceID{a, b.Source.ID}}
	listResult, err := s.Query(ctx, listReq)
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name   string
		result mousa.QueryResult
	}{
		{"source", sourceResult},
		{"list", listResult},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.result.Passages) != 2 {
				t.Fatalf("passages = %+v, want two", tt.result.Passages)
			}
			first := tt.result.Passages[0]
			if first.SegmentID != meta.ID || first.Path != "_guide/index" || first.Rank != 1 {
				t.Fatalf("first passage = %+v, want meta at _guide/index rank 1", first)
			}
		})
	}

	if len(collectionResult.Passages) != 1 {
		t.Fatalf("collection passages = %+v, want only the regular passage", collectionResult.Passages)
	}
	released := collectionResult.Passages[0]
	if released.SegmentID != regular.ID || released.Path != "topics/_guide" || released.Rank != 2 {
		t.Fatalf("collection passage = %+v, want regular at topics/_guide rank 2", released)
	}
	if len(collectionResult.Trail.Candidates) != 2 {
		t.Fatalf("collection candidates = %+v, want two", collectionResult.Trail.Candidates)
	}
	omitted := collectionResult.Trail.Candidates[0]
	if omitted.SegmentID != meta.ID || omitted.Omitted != "meta-path-v1" || omitted.Packed != "none" || omitted.Cost != 0 || omitted.Rank != 1 {
		t.Fatalf("meta candidate = %+v, want rank 1 omitted as meta-path-v1 at no cost", omitted)
	}
	if !reflect.DeepEqual(collectionResult.Trail.Retrieval, sourceResult.Trail.Retrieval) {
		t.Fatalf("collection retrieval changed:\ncollection %+v\nsource %+v", collectionResult.Trail.Retrieval, sourceResult.Trail.Retrieval)
	}
	if len(sourceResult.Trail.Candidates) != 2 {
		t.Fatalf("source candidates = %+v, want two", sourceResult.Trail.Candidates)
	}
	for i := range collectionResult.Trail.Candidates {
		if collectionResult.Trail.Candidates[i].SegmentID != sourceResult.Trail.Candidates[i].SegmentID ||
			!reflect.DeepEqual(collectionResult.Trail.Candidates[i].Scores, sourceResult.Trail.Candidates[i].Scores) {
			t.Fatalf("candidate %d order or scores changed:\ncollection %+v\nsource %+v", i, collectionResult.Trail.Candidates[i], sourceResult.Trail.Candidates[i])
		}
	}

	limitedReq := collectionReq
	limitedReq.Limit = 1
	limitedResult, err := s.Query(ctx, limitedReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(limitedResult.Passages) != 0 || len(limitedResult.Trail.Candidates) != 1 {
		t.Fatalf("limited collection backfilled or lost candidates: passages=%+v candidates=%+v", limitedResult.Passages, limitedResult.Trail.Candidates)
	}
	if limitedResult.Trail.Candidates[0].SegmentID != meta.ID || limitedResult.Trail.Candidates[0].Omitted != "meta-path-v1" {
		t.Fatalf("limited candidate = %+v, want the first meta candidate omitted", limitedResult.Trail.Candidates[0])
	}

	moved, err = s.SetDocumentPath(ctx, a, "layout", "docs/layout", "test")
	if err != nil || !moved {
		t.Fatalf("move layout out of meta path: moved=%t err=%v", moved, err)
	}
	movedResult, err := s.Query(ctx, collectionReq)
	if err != nil {
		t.Fatal(err)
	}
	if len(movedResult.Passages) != 2 || len(movedResult.Trail.Candidates) != 2 {
		t.Fatalf("moved collection result = passages %+v candidates %+v, want two of each", movedResult.Passages, movedResult.Trail.Candidates)
	}
	packed := 0
	for _, candidate := range movedResult.Trail.Candidates {
		if candidate.Packed == "full" {
			packed++
		}
	}
	if packed != 2 || movedResult.Passages[0].SegmentID != meta.ID || movedResult.Passages[0].Path != "docs/layout" {
		t.Fatalf("moved collection packed %d passages %+v, want two packed with docs/layout first", packed, movedResult.Passages)
	}
	if _, err := s.Verify(ctx); err != nil {
		t.Fatalf("verify after path move: %v", err)
	}
	recheck, err := s.RecheckTrailText(ctx, collectionResult.Trail.ID, nil)
	if err != nil {
		t.Fatalf("recheck historical collection trail: %v", err)
	}
	if recheck.Released == nil || len(*recheck.Released) != 1 || (*recheck.Released)[0].SegmentID != regular.ID {
		t.Fatalf("historical released = %+v, want only regular", recheck.Released)
	}
}

func TestVerifyMetaPathContradiction(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	path := "docs/guide"
	a, _ := seedAuditItemAtPath(t, s, "guide", &path, "harbor evidence")
	allowSource(t, s, a)
	if err := s.CreateCollection(ctx, "fleet"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCollectionSource(ctx, "fleet", a, true); err != nil {
		t.Fatal(err)
	}
	req := auditRequest(a)
	req.Scope = mousa.Scope{Collection: "fleet"}
	result, err := s.Query(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trail.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want one", result.Trail.Candidates)
	}

	trail := result.Trail
	trail.Candidates[0].Omitted = mousa.MetaPathV1
	packing := mousa.ApplyFirstFit(trail.Candidates, trail.Request.Budget.Limit, nil)
	trail.Packing = &packing
	forged, err := mousa.NewTrail(trail)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.writeImmediate(ctx, "forge test trail", func(conn *sql.Conn) error { return putTrail(ctx, conn, forged) }); err != nil {
		t.Fatal(err)
	}

	assertContradiction := func(name string, err error) {
		t.Helper()
		if !IsCode(err, CodeIntegrity) ||
			!strings.Contains(err.Error(), "meta-path-v1") || !strings.Contains(err.Error(), "does not start with _") {
			t.Fatalf("%s error = %v, want integrity meta-path contradiction", name, err)
		}
	}
	_, err = s.VerifyTrails(ctx, []mousa.SourceTrailID{forged.ID})
	assertContradiction("named verify", err)
	_, err = s.Verify(ctx)
	assertContradiction("full verify", err)
	_, err = s.RecheckTrail(ctx, forged.ID)
	assertContradiction("recheck", err)
}

func TestVerifyCreationPathProjection(t *testing.T) {
	s := openLexicalStore(t)
	defer s.Close()
	ctx := t.Context()
	a, _ := seedAuditItem(t, s, "created", "harbor evidence")
	var record []byte
	if err := s.db.QueryRowContext(ctx, `SELECT record_json FROM item_changes WHERE source_id = ?`, a[:]).Scan(&record); err != nil {
		t.Fatal(err)
	}
	creation, err := mousa.DecodeItemChange(record)
	if err != nil {
		t.Fatal(err)
	}
	if creation.Schema != mousa.ItemChangeSchema || creation.CreationPath != "created" {
		t.Fatalf("default creation record schema/path = %q/%q, want current schema and item ID", creation.Schema, creation.CreationPath)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE local_items SET path = 'tampered' WHERE source_id = ? AND item_id = 'created'`, a[:]); err != nil {
		t.Fatal(err)
	}
	_, err = s.VerifyLog(ctx)
	if !IsCode(err, CodeIntegrity) || !strings.Contains(err.Error(), "path") || !strings.Contains(err.Error(), "log") {
		t.Fatalf("VerifyLog error = %v, want integrity path/log contradiction", err)
	}
}

func TestItemPathAtSeq(t *testing.T) {
	anchored := itemPathHistory{createdAt: 1, initial: "_x"}
	if path, known := anchored.pathAtSeq(1); !known || path != "_x" {
		t.Fatalf("anchored path at creation = %q, %t; want _x, true", path, known)
	}
	if path, known := anchored.pathAtSeq(0); !known || path != mousa.DefaultNotePath {
		t.Fatalf("precreation path = %q, %t; want %q, true", path, known, mousa.DefaultNotePath)
	}

	source := mousa.SourceID{1}
	repr := mousa.RepresentationID{2}
	activation, err := mousa.NewItemChange(source, "legacy", 1, nil, &repr, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	histories := pathHistories{}
	if err := histories.applyItemChange(activation); err != nil {
		t.Fatal(err)
	}
	for _, move := range []mousa.PathMove{
		{Actor: "test", Seq: 3, SourceID: source, Records: []mousa.PathMoveEntry{{ItemID: "legacy", OldPath: "_old", NewPath: "middle"}}},
		{Actor: "test", Seq: 5, SourceID: source, Records: []mousa.PathMoveEntry{{ItemID: "legacy", OldPath: "middle", NewPath: "end"}}},
	} {
		if err := histories.applyPathMove(move); err != nil {
			t.Fatal(err)
		}
	}
	history := histories[source]["legacy"]
	for _, tt := range []struct {
		asOf uint64
		want string
	}{
		{2, "_old"},
		{3, "middle"},
		{4, "middle"},
		{5, "end"},
	} {
		if path, known := history.pathAtSeq(tt.asOf); !known || path != tt.want {
			t.Fatalf("legacy path as of %d = %q, %t; want %q, true", tt.asOf, path, known, tt.want)
		}
	}

	unanchoredSource := mousa.SourceID{2}
	unanchoredActivation, err := mousa.NewItemChange(unanchoredSource, "unanchored", 1, nil, &repr, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := histories.applyItemChange(unanchoredActivation); err != nil {
		t.Fatal(err)
	}
	if path, known := histories[unanchoredSource]["unanchored"].pathAtSeq(1); known || path != "" {
		t.Fatalf("unanchored legacy path = %q, %t; want empty, false", path, known)
	}
}

func TestVerifyMetaPathEvidence(t *testing.T) {
	collectionTrail := func(t *testing.T, s *Store, source mousa.SourceID) mousa.Trail {
		t.Helper()
		allowSource(t, s, source)
		if err := s.CreateCollection(t.Context(), "fleet"); err != nil {
			t.Fatal(err)
		}
		if err := s.SetCollectionSource(t.Context(), "fleet", source, true); err != nil {
			t.Fatal(err)
		}
		req := auditRequest(source)
		req.Scope = mousa.Scope{Collection: "fleet"}
		result, err := s.Query(t.Context(), req)
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Trail.Candidates) != 1 || result.Trail.Candidates[0].Omitted != mousa.MetaPathV1 {
			t.Fatalf("collection candidates = %+v, want one meta omission", result.Trail.Candidates)
		}
		return result.Trail
	}
	assertUnverifiable := func(t *testing.T, s *Store, trail mousa.Trail, want int64) {
		t.Helper()
		ctx := t.Context()
		named, err := s.VerifyTrails(ctx, []mousa.SourceTrailID{trail.ID})
		if err != nil {
			t.Fatalf("named verify: %v", err)
		}
		if named.UnverifiableMetaPathOmissions != want {
			t.Fatalf("named unverifiable count = %d, want %d", named.UnverifiableMetaPathOmissions, want)
		}
		full, err := s.Verify(ctx)
		if err != nil {
			t.Fatalf("full verify: %v", err)
		}
		if full.UnverifiableMetaPathOmissions != want {
			t.Fatalf("full unverifiable count = %d, want %d", full.UnverifiableMetaPathOmissions, want)
		}
		recheck, err := s.RecheckTrail(ctx, trail.ID)
		if err != nil {
			t.Fatalf("recheck: %v", err)
		}
		if recheck.UnverifiableMetaPathOmissions != want {
			t.Fatalf("recheck unverifiable count = %d, want %d", recheck.UnverifiableMetaPathOmissions, want)
		}
		if recheck.Status != "empty" {
			t.Fatalf("recheck status = %q, want empty for the sole omitted candidate", recheck.Status)
		}
	}

	t.Run("logged creation and later move", func(t *testing.T) {
		s := openLexicalStore(t)
		defer s.Close()
		ctx := t.Context()
		path := "_x"
		source, _ := seedAuditItemAtPath(t, s, "x", &path, "harbor evidence")
		trail := collectionTrail(t, s, source)
		assertUnverifiable(t, s, trail, 0)
		moved, err := s.SetDocumentPath(ctx, source, "x", "docs/x", "test")
		if err != nil || !moved {
			t.Fatalf("move after trail: moved=%t err=%v", moved, err)
		}
		assertUnverifiable(t, s, trail, 0)
	})

	t.Run("legacy creation is unverifiable", func(t *testing.T) {
		s := openLexicalStore(t)
		defer s.Close()
		ctx := t.Context()
		path := "_legacy"
		source, _ := seedAuditItemAtPath(t, s, "legacy", &path, "harbor evidence")
		if err := s.writeImmediate(ctx, "simulate legacy item creation", func(conn *sql.Conn) error {
			var originalID, original []byte
			if err := conn.QueryRowContext(ctx, `SELECT id, record_json FROM item_changes WHERE source_id = ?`, source[:]).Scan(&originalID, &original); err != nil {
				return err
			}
			change, err := mousa.DecodeItemChange(original)
			if err != nil {
				return err
			}
			legacy, err := mousa.NewItemChange(change.SourceID, change.ItemID, change.Seq, change.Previous, change.Current, change.ActivatedAtUsec, "")
			if err != nil {
				return err
			}
			if legacy.Schema != mousa.ItemChangeSchemaV2 {
				return fmt.Errorf("legacy simulation schema = %q, want v2", legacy.Schema)
			}
			data, err := mousa.EncodeItemChange(legacy)
			if err != nil {
				return err
			}
			leaf := mousa.LogLeafHash(data)
			result, err := conn.ExecContext(ctx, `UPDATE item_changes SET id = ?, record_json = ? WHERE id = ?`, legacy.ID[:], data, originalID)
			if err != nil {
				return err
			}
			if err := oneRow(result); err != nil {
				return err
			}
			result, err = conn.ExecContext(ctx, `UPDATE log SET record_id = ?, leaf_hash = ? WHERE kind = 'item_change' AND record_id = ?`, legacy.ID[:], leaf[:], originalID)
			if err != nil {
				return err
			}
			return oneRow(result)
		}); err != nil {
			t.Fatal(err)
		}
		trail := collectionTrail(t, s, source)
		assertUnverifiable(t, s, trail, 1)
	})
}

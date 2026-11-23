package sqlite

import (
	"context"
	"reflect"
	"slices"
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
	source, repr, normalized := prepareLocalRevision(t, s, name, text)
	segments, err := mousa.SegmentUTF8Text(repr, normalized)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ActivateLocalItem(t.Context(), source.ID, name, repr.ID, normalized); err != nil {
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
		{"denied", proposal{denied.ID, denied.ContentSHA256, -4}, 1, 0, false},
		{"stale digest", proposal{sameSource.ID, good.ContentSHA256, -4}, 0, 0, true},
		{"unknown", proposal{mousa.SegmentID{1}, good.ContentSHA256, -4}, 0, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s.propose = func(_ context.Context, _ queryer, _ string, sources []mousa.SourceID, _ int) ([]proposal, error) {
				if !slices.Equal(sources, []mousa.SourceID{a}) {
					t.Fatalf("proposer got %v, want only %v", sources, a)
				}
				return []proposal{tt.fault, {good.ID, good.ContentSHA256, -2}}, nil
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
	s.propose = func(ctx context.Context, q queryer, text string, sources []mousa.SourceID, limit int) ([]proposal, error) {
		if _, changed, err := writer.SetSourceAccess(ctx, a, mousa.SourceAccessDeny, "test"); err != nil || !changed {
			t.Fatalf("revoke during read snapshot: changed=%t err=%v", changed, err)
		}
		return proposeLexical(ctx, q, text, sources, limit)
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

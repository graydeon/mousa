package sqlite

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func TestIndexTextRepresentationAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	representation, content, segments := addLexicalDocument(t, store, "atomic", "café\x00omega 東京")

	for attempt := 0; attempt < 2; attempt++ {
		if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
			t.Fatalf("IndexTextRepresentation attempt %d: %v", attempt+1, err)
		}
	}
	var relationRows, ftsRows int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM segment_lexical_rows`).Scan(&relationRows); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM segment_lexical_fts`).Scan(&ftsRows); err != nil {
		t.Fatal(err)
	}
	if relationRows != len(segments) || ftsRows != len(segments) {
		t.Fatalf("lexical rows = relation %d fts %d, want %d", relationRows, ftsRows, len(segments))
	}
	var text string
	var digest []byte
	if err := store.db.QueryRowContext(ctx, `SELECT text, content_sha256 FROM segment_lexical_fts`).Scan(&text, &digest); err != nil {
		t.Fatal(err)
	}
	if text != string(content) || !bytes.Equal(digest, segments[0].ContentSHA256[:]) {
		t.Fatalf("stored text/digest disagree: text=%q digest=%x", text, digest)
	}
	candidates, err := store.SearchLexical(ctx, "omega", 10)
	if err != nil || len(candidates) != 1 || candidates[0].Text != string(content) {
		t.Fatalf("NUL suffix search = %#v, %v", candidates, err)
	}
}

func TestIndexTextRepresentationAddsLaterSegment(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	content := []byte(strings.Repeat("alpha ", 800))
	representation, segments := addLexicalDocumentWithSegments(t, store, "later", content, 1)
	if len(segments) < 2 {
		t.Fatalf("segments = %d, want at least 2", len(segments))
	}
	if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
		t.Fatal(err)
	}
	var firstRowID int64
	if err := store.db.QueryRowContext(ctx, `SELECT rowid FROM segment_lexical_rows WHERE segment_id = ?`, segments[0].ID[:]).Scan(&firstRowID); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSegment(ctx, segments[1]); err != nil {
		t.Fatal(err)
	}
	if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
		t.Fatal(err)
	}
	var gotFirstRowID int64
	if err := store.db.QueryRowContext(ctx, `SELECT rowid FROM segment_lexical_rows WHERE segment_id = ?`, segments[0].ID[:]).Scan(&gotFirstRowID); err != nil {
		t.Fatal(err)
	}
	if gotFirstRowID != firstRowID {
		t.Fatalf("prior rowid changed from %d to %d", firstRowID, gotFirstRowID)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM segment_lexical_rows`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("relation count = %d err=%v, want 2", count, err)
	}
}

func TestIndexTextRepresentationRejectsInvalidAndRollsBack(t *testing.T) {
	ctx := context.Background()
	t.Run("invalid full content", func(t *testing.T) {
		store := openLexicalStore(t)
		defer store.Close()
		representation, content, _ := addLexicalDocument(t, store, "invalid", "alpha")
		content[0] = 'z'
		if err := store.IndexTextRepresentation(ctx, representation.ID, content); !IsCode(err, CodeInvalidRecord) {
			t.Fatalf("error = %v, want invalid_record", err)
		}
		assertLexicalCounts(t, store, 0)
	})
	t.Run("oversized canonical Segment", func(t *testing.T) {
		store := openLexicalStore(t)
		defer store.Close()
		content := []byte(strings.Repeat("x", mousa.MaxUTF8TextSegmentBytes+1))
		representation, segments := addLexicalDocumentWithSegments(t, store, "oversized", content, 0)
		selector := mousa.NewTextByteRangeSelector(0, uint64(len(content)))
		id, err := mousa.NewSegmentID(representation.ID, selector, testDigest(string(content)))
		if err != nil {
			t.Fatal(err)
		}
		segment := mousa.Segment{Schema: mousa.SegmentSchema, ID: id, RepresentationID: representation.ID, Selector: selector, ContentSHA256: testDigest(string(content))}
		if len(segments) < 2 || store.PutSegment(ctx, segment) != nil {
			t.Fatal("failed to seed oversized canonical Segment")
		}
		if err := store.IndexTextRepresentation(ctx, representation.ID, content); !IsCode(err, CodeResourceLimit) {
			t.Fatalf("error = %v, want resource_limit", err)
		}
		assertLexicalCounts(t, store, 0)
	})
	t.Run("same Segment disagreement", func(t *testing.T) {
		store := openLexicalStore(t)
		defer store.Close()
		representation, content, _ := addLexicalDocument(t, store, "conflict", "alpha")
		if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE segment_lexical_fts SET text = 'changed'`); err != nil {
			t.Fatal(err)
		}
		if err := store.IndexTextRepresentation(ctx, representation.ID, content); !IsCode(err, CodeConflict) {
			t.Fatalf("disagreement error = %v, want conflict", err)
		}
	})
	t.Run("injected read-back failure rolls back both writes", func(t *testing.T) {
		store := openLexicalStore(t)
		defer store.Close()
		_, _, replacementSegments := addLexicalDocument(t, store, "replacement", "replacement")
		representation, content, _ := addLexicalDocument(t, store, "readback", "rollback probe")
		triggerSQL := fmt.Sprintf(`
			CREATE TEMP TRIGGER fail_lexical_readback
			AFTER INSERT ON segment_lexical_rows
			BEGIN
				UPDATE segment_lexical_rows SET segment_id = X'%x' WHERE rowid = new.rowid;
			END`, replacementSegments[0].ID[:])
		if _, err := store.db.ExecContext(ctx, triggerSQL); err != nil {
			t.Fatal(err)
		}
		if err := store.IndexTextRepresentation(ctx, representation.ID, content); !IsCode(err, CodeIntegrity) {
			t.Fatalf("injected read-back error = %v, want integrity", err)
		}
		var relationRows int
		if err := store.db.QueryRow(`SELECT count(*) FROM segment_lexical_rows`).Scan(&relationRows); err != nil || relationRows != 0 {
			t.Fatalf("relation rows after rollback = %d err=%v, want 0", relationRows, err)
		}
		var ftsRows int
		if err := store.db.QueryRow(`SELECT count(*) FROM segment_lexical_fts`).Scan(&ftsRows); err != nil || ftsRows != 0 {
			t.Fatalf("FTS rows after rollback = %d err=%v, want 0", ftsRows, err)
		}
	})
}

func TestIndexTextRepresentationBusyCancellationAndReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lexical.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	representation, content, _ := addLexicalDocument(t, store, "busy", "alpha")
	locker, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := locker.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if err := store.IndexTextRepresentation(ctx, representation.ID, content); !IsCode(err, CodeBusy) {
		t.Fatalf("busy error = %v, want busy", err)
	}
	conn.ExecContext(ctx, `ROLLBACK`)
	conn.Close()
	locker.Close()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.IndexTextRepresentation(cancelled, representation.ID, content); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if err := readOnly.IndexTextRepresentation(ctx, mousa.RepresentationID{}, nil); !IsCode(err, CodeReadOnly) {
		t.Fatalf("read-only precedence error = %v", err)
	}
}

func TestSearchLexicalGrammarOrderingAndBounds(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	for _, document := range []struct{ key, text string }{
		{"one", "alpha beta café"},
		{"two", "alpha gamma"},
		{"three", "beta delta"},
		{"tie-a", "tieword"},
		{"tie-b", "tieword"},
	} {
		representation, content, _ := addLexicalDocument(t, store, document.key, document.text)
		if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		expression string
		want       int
	}{
		{"alpha", 2},
		{`"alpha beta"`, 1},
		{"gam*", 1},
		{"alpha AND gamma", 1},
		{"gamma OR delta", 2},
		{"alpha NOT gamma", 1},
		{"cafe", 1},
		{"absent", 0},
	} {
		got, err := store.SearchLexical(ctx, test.expression, 100)
		if err != nil || len(got) != test.want || got == nil {
			t.Fatalf("SearchLexical(%q) = %#v, %v; want %d non-nil", test.expression, got, err, test.want)
		}
		for _, candidate := range got {
			if math.IsNaN(candidate.BM25) || math.IsInf(candidate.BM25, 0) {
				t.Fatalf("non-finite BM25 for %q", test.expression)
			}
		}
	}
	ties, err := store.SearchLexical(ctx, "tieword", 2)
	if err != nil || len(ties) != 2 {
		t.Fatalf("ties = %#v, %v", ties, err)
	}
	wantIDs := []mousa.SegmentID{ties[0].Segment.ID, ties[1].Segment.ID}
	sort.Slice(wantIDs, func(i, j int) bool { return bytes.Compare(wantIDs[i][:], wantIDs[j][:]) < 0 })
	if ties[0].BM25 != ties[1].BM25 || ties[0].Segment.ID != wantIDs[0] || ties[1].Segment.ID != wantIDs[1] {
		t.Fatalf("tie order = %s %.17g, %s %.17g", ties[0].Segment.ID, ties[0].BM25, ties[1].Segment.ID, ties[1].BM25)
	}
	limited, err := store.SearchLexical(ctx, "alpha", 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("limited = %#v, %v", limited, err)
	}
	invalidUTF8 := string([]byte{0xff})
	for _, invalid := range []struct {
		expression string
		limit      int
	}{
		{"", 1}, {invalidUTF8, 1}, {"a\x00b", 1}, {strings.Repeat("a", 4097), 1}, {"alpha", 0}, {"alpha", 101}, {`"unterminated`, 1},
	} {
		if got, err := store.SearchLexical(ctx, invalid.expression, invalid.limit); got != nil || !IsCode(err, CodeInvalidQuery) {
			t.Fatalf("invalid query (%q,%d) = %#v, %v", invalid.expression, invalid.limit, got, err)
		}
	}
}

func TestSearchLexicalIntegrityAndReadOnlyParity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lexical.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	representation, content, _ := addLexicalDocument(t, store, "parity", "alpha café")
	if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
		t.Fatal(err)
	}
	want, err := store.SearchLexical(ctx, "cafe", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readOnly.SearchLexical(ctx, "cafe", 10)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("read-only candidates = %#v, %v; want %#v", got, err, want)
	}
	readOnly.Close()
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.db.ExecContext(ctx, `UPDATE segment_lexical_fts SET content_sha256 = randomblob(32)`); err != nil {
		t.Fatal(err)
	}
	if got, err := store.SearchLexical(ctx, "alpha", 10); got != nil || !IsCode(err, CodeIntegrity) {
		t.Fatalf("tampered search = %#v, %v; want integrity", got, err)
	}
}

func TestLexicalBackupRestorePreservesCandidates(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	representation, content, _ := addLexicalDocument(t, store, "backup", "alpha beta")
	if err := store.IndexTextRepresentation(ctx, representation.ID, content); err != nil {
		t.Fatal(err)
	}
	want, err := store.SearchLexical(ctx, "alpha", 10)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "backup.sqlite")
	if err := store.Backup(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	store.Close()
	backup, err := OpenReadOnly(ctx, backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	got, err := backup.SearchLexical(ctx, "alpha", 10)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("backup candidates = %#v, %v; want %#v", got, err, want)
	}
}

func openLexicalStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "lexical.sqlite"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func addLexicalDocument(t *testing.T, store *Store, key, text string) (mousa.Representation, []byte, []mousa.Segment) {
	t.Helper()
	content := []byte(text)
	representation, segments := addLexicalDocumentWithSegments(t, store, key, content, -1)
	return representation, content, segments
}

func addLexicalDocumentWithSegments(t *testing.T, store *Store, key string, content []byte, segmentLimit int) (mousa.Representation, []mousa.Segment) {
	t.Helper()
	ctx := context.Background()
	sourceID, err := mousa.NewSourceID("lexical-test", key)
	if err != nil {
		t.Fatal(err)
	}
	source := mousa.Source{Schema: mousa.SourceSchema, ID: sourceID, Namespace: "lexical-test", ExternalSourceID: key}
	observationID, err := mousa.NewObservationID(source.ID, key)
	if err != nil {
		t.Fatal(err)
	}
	observation := mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: source.ID, ExternalObservationID: key}
	artifactID, err := mousa.NewArtifactID(observation.ID, "body")
	if err != nil {
		t.Fatal(err)
	}
	artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observation.ID, ArtifactKey: "body", MediaType: mousa.UTF8TextMediaType, ContentSHA256: testDigest(string(content)), ByteLength: uint64(len(content))}
	representation, normalized, err := mousa.NormalizeUTF8Text(artifact, content)
	if err != nil || !bytes.Equal(normalized, content) {
		t.Fatalf("NormalizeUTF8Text = %v, normalized=%q", err, normalized)
	}
	segments, err := mousa.SegmentUTF8Text(representation, content)
	if err != nil {
		t.Fatal(err)
	}
	for _, put := range []func() error{
		func() error { return store.PutSource(ctx, source) },
		func() error { return store.PutObservation(ctx, observation) },
		func() error { return store.PutArtifact(ctx, artifact) },
		func() error { return store.PutRepresentation(ctx, representation) },
	} {
		if err := put(); err != nil {
			t.Fatalf("seed lexical parent: %v", err)
		}
	}
	count := len(segments)
	if segmentLimit >= 0 && segmentLimit < count {
		count = segmentLimit
	}
	for _, segment := range segments[:count] {
		if err := store.PutSegment(ctx, segment); err != nil {
			t.Fatalf("PutSegment: %v", err)
		}
	}
	return representation, segments
}

func assertLexicalCounts(t *testing.T, store *Store, want int) {
	t.Helper()
	for _, table := range []string{"segment_lexical_rows", "segment_lexical_fts"} {
		var got int
		if err := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count = %d err=%v, want %d", table, got, err, want)
		}
	}
}

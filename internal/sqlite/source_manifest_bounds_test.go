package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

// Bulk fixture insertion keeps boundary tests focused on inspection rather than
// thousands of independent writer commits. All rows use real canonical codecs.
func seedManifestBoundItem(t *testing.T, store *Store, source mousa.Source, name, text, policy string) {
	t.Helper()
	digest := testDigest(text)
	external := fmt.Sprintf("item/%s@%x", name, digest)
	observationID, err := mousa.NewObservationID(source.ID, external)
	if err != nil {
		t.Fatal(err)
	}
	artifactID, err := mousa.NewArtifactID(observationID, "body")
	if err != nil {
		t.Fatal(err)
	}
	artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: "body", MediaType: mousa.UTF8TextMediaType, ContentSHA256: digest, ByteLength: uint64(len(text))}
	batch := mousa.IngestBatch{AdapterID: "test", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem, CapturedAtUsec: 2, Source: source, Observation: mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: source.ID, ExternalObservationID: external}, Artifacts: []mousa.Artifact{artifact}}
	if err := store.ApplyIngest(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	rep, normalized, err := mousa.NormalizeUTF8TextWithPolicy(artifact, []byte(text), policy)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepresentation(t.Context(), rep); err != nil {
		t.Fatal(err)
	}
	segments, err := mousa.SegmentUTF8Text(rep, normalized)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.writeImmediate(t.Context(), "seed manifest bound", func(conn *sql.Conn) error {
		for _, segment := range segments {
			data, err := mousa.EncodeSegment(segment)
			if err != nil {
				return err
			}
			span, _ := segment.Selector.TextByteRange()
			if _, err := conn.ExecContext(t.Context(), `INSERT INTO segments(id,representation_id,selector_start,selector_end,record_json) VALUES(?,?,?,?,?)`, segment.ID[:], rep.ID[:], uint64Blob(span.Start), uint64Blob(span.End), data); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateLocalItem(t.Context(), source.ID, name, rep.ID, normalized); err != nil {
		t.Fatal(err)
	}
}

func TestSourceManifestExactAndOverTextBytes(t *testing.T) {
	store, source := manifestFixture(t)
	seedManifestBoundItem(t, store, source, "a", strings.Repeat("x", mousa.MaxSourceManifestTextBytes), mousa.TextSegmentFixedV1)
	exact := inspectManifest(t, store, source, "exact-text", nil)
	if exact.Manifest.Items[0].NormalizedByteLength != mousa.MaxSourceManifestTextBytes {
		t.Fatal("exact text bound was truncated")
	}
	seedManifestBoundItem(t, store, source, "b", "x", mousa.TextSegmentFixedV1)
	before := manifestDecisionCount(t, store)
	if _, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "over-text"), &exact.Manifest.ID); !IsCode(err, CodeResourceLimit) {
		t.Fatalf("over text bound: %v", err)
	}
	if manifestDecisionCount(t, store) != before {
		t.Fatal("resource failure committed authorization")
	}
}

func TestSourceManifestExactAndOverSegmentCount(t *testing.T) {
	store, source := manifestFixture(t)
	seedManifestBoundItem(t, store, source, "a", strings.Repeat("# h\n", mousa.MaxSourceManifestSegments), mousa.TextSegmentPassageV1)
	exact := inspectManifest(t, store, source, "exact-segments", nil)
	if exact.Manifest.Items[0].SegmentCount != mousa.MaxSourceManifestSegments {
		t.Fatalf("segment count = %d", exact.Manifest.Items[0].SegmentCount)
	}
	seedManifestBoundItem(t, store, source, "b", "x", mousa.TextSegmentFixedV1)
	if _, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "over-segments"), &exact.Manifest.ID); !IsCode(err, CodeResourceLimit) {
		t.Fatalf("over segment bound: %v", err)
	}
}

func TestSourceManifestStoreExactAndOverEncodedBound(t *testing.T) {
	store, source := manifestFixture(t)
	for i := range 110 {
		seedManifestBoundItem(t, store, source, fmt.Sprintf("%03d", i), "", mousa.TextSegmentPassageV1)
	}
	base := inspectManifest(t, store, source, "encoded-base", nil)
	data, err := mousa.EncodeSourceManifest(*base.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	padding := mousa.MaxSourceManifestBytes - len(data)
	lastName := ""
	for i := range 110 {
		oldName := fmt.Sprintf("%03d", i)
		extra := min(padding, 4092)
		name := oldName + strings.Repeat("x", extra)
		if name != oldName {
			if _, err := store.DeleteLocalItem(t.Context(), source.ID, oldName); err != nil {
				t.Fatal(err)
			}
			seedManifestBoundItem(t, store, source, name, "", mousa.TextSegmentPassageV1)
		}
		padding -= extra
		lastName = name
	}
	if padding != 0 {
		t.Fatalf("fixture cannot represent exact encoded bound: %d bytes remain", padding)
	}
	exact := inspectManifest(t, store, source, "encoded-exact", nil)
	data, err = mousa.EncodeSourceManifest(*exact.Manifest)
	if err != nil || len(data) != mousa.MaxSourceManifestBytes {
		t.Fatalf("exact encoded size = %d, error = %v", len(data), err)
	}
	if _, err := store.DeleteLocalItem(t.Context(), source.ID, lastName); err != nil {
		t.Fatal(err)
	}
	seedManifestBoundItem(t, store, source, lastName+"x", "", mousa.TextSegmentPassageV1)
	before := manifestDecisionCount(t, store)
	got, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "encoded-over"), &exact.Manifest.ID)
	if !IsCode(err, CodeResourceLimit) || got.Manifest != nil || got.Matches != nil {
		t.Fatalf("one byte over encoded bound: %+v %v", got, err)
	}
	if manifestDecisionCount(t, store) != before {
		t.Fatal("encoded limit failure committed authorization")
	}
}

func TestSourceManifestRejectsRehashedWrongSegmentation(t *testing.T) {
	store, source := manifestFixture(t)
	seedManifestBoundItem(t, store, source, "a", "abcd", mousa.TextSegmentFixedV1)
	item, err := store.GetLocalItem(t.Context(), source.ID, "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM segment_lexical_fts; DELETE FROM segment_lexical_rows; DELETE FROM segments`); err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"ab", "cd"} {
		selector := mousa.NewTextByteRangeSelector(uint64(i*2), uint64(i*2+2))
		digest := testDigest(text)
		id, err := mousa.NewSegmentID(item.RepresentationID, selector, digest)
		if err != nil {
			t.Fatal(err)
		}
		segment := mousa.Segment{Schema: mousa.SegmentSchema, ID: id, RepresentationID: item.RepresentationID, Selector: selector, ContentSHA256: digest}
		if err := store.PutSegment(t.Context(), segment); err != nil {
			t.Fatal(err)
		}
		if err := store.writeImmediate(t.Context(), "seed rehashed segmentation", func(conn *sql.Conn) error {
			return putLexicalRow(t.Context(), conn, segment, []byte(text))
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "rehashed-segmentation"), nil); !IsCode(err, CodeIntegrity) {
		t.Fatalf("self-consistent but incorrect segmentation certified: %v", err)
	}
}

func TestSourceManifestCanonicalBlobAdmission(t *testing.T) {
	for _, table := range []string{"sources", "representations", "artifacts", "observations", "segments"} {
		t.Run(table, func(t *testing.T) {
			store, source := manifestFixture(t)
			seedLocalItem(t, store, source, "a", "private")
			if _, err := store.db.Exec(`UPDATE `+table+` SET record_json=zeroblob(?)`, maxRecordBytes+1); err != nil {
				t.Fatal(err)
			}
			before := manifestDecisionCount(t, store)
			got, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "oversized"), nil)
			if !IsCode(err, CodeResourceLimit) || got.Manifest != nil || got.Matches != nil {
				t.Fatalf("oversized %s was not rejected at admission: %+v %v", table, got, err)
			}
			if manifestDecisionCount(t, store) != before {
				t.Fatal("oversized record committed authorization")
			}
			store, source = manifestFixture(t)
			seedLocalItem(t, store, source, "a", "private")
			withdrawVerifiedSource(t, store, source.ID, "deny-oversized", 3)
			if _, err := store.db.Exec(`UPDATE `+table+` SET record_json=zeroblob(?)`, maxRecordBytes+1); err != nil {
				t.Fatal(err)
			}
			expected := mousa.SHA256{1}
			got, err = store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "denied-oversized"), &expected)
			if err != nil || got.Decision.Outcome != mousa.PolicyOutcomeDeny || got.Manifest != nil || got.Matches != nil {
				t.Fatalf("denial consulted oversized %s: %+v %v", table, got, err)
			}
		})
	}
}

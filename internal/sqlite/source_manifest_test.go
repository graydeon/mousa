package sqlite

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
	driverSQLite "modernc.org/sqlite"
)

func manifestFixture(t *testing.T) (*Store, mousa.Source) {
	t.Helper()
	store := openLexicalStore(t)
	t.Cleanup(func() { store.Close() })
	seedEvaluationFixture(t, store)
	batch := testPushBatch(t, "manifest-fixture", nil)
	if err := store.ApplyIngest(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	return store, batch.Source
}

func inspectManifest(t *testing.T, store *Store, source mousa.Source, name string, expected *mousa.SHA256) SourceManifestInspection {
	t.Helper()
	got, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, name), expected)
	if err != nil || got.Manifest == nil || got.Decision.Outcome != mousa.PolicyOutcomeAllow {
		t.Fatalf("inspect: %+v %v", got, err)
	}
	return got
}

func TestSourceManifestCurrentStateTransitions(t *testing.T) {
	store, source := manifestFixture(t)
	empty := inspectManifest(t, store, source, "empty", nil)
	if len(empty.Manifest.Items) != 0 || empty.Matches != nil {
		t.Fatalf("empty source: %+v", empty)
	}
	seedLocalItem(t, store, source, "b", "second\r\nbody")
	seedManifestBoundItem(t, store, source, "a", "", mousa.TextSegmentPassageV1)
	first := inspectManifest(t, store, source, "first", nil)
	if len(first.Manifest.Items) != 2 || first.Manifest.Items[0].Item != "a" || first.Manifest.Items[0].SegmentCount != 0 || first.Manifest.Items[1].RawByteLength != 12 || first.Manifest.Items[1].NormalizedByteLength != 11 {
		t.Fatalf("inventory: %+v", first.Manifest.Items)
	}
	match := inspectManifest(t, store, source, "match", &first.Manifest.ID)
	if match.Matches == nil || !*match.Matches {
		t.Fatal("same state did not match")
	}
	other, sameSource := manifestFixture(t)
	seedManifestBoundItem(t, other, sameSource, "a", "", mousa.TextSegmentPassageV1)
	seedLocalItem(t, other, sameSource, "b", "second\r\nbody")
	independent := inspectManifest(t, other, sameSource, "independent", nil)
	if independent.Manifest.ID != first.Manifest.ID {
		t.Fatal("fresh store/input order changed identity")
	}
	seedLocalItem(t, store, source, "b", "replacement")
	revised := inspectManifest(t, store, source, "revised", &first.Manifest.ID)
	if revised.Matches == nil || *revised.Matches {
		t.Fatal("revision matched old identity")
	}
	if _, err := store.DeleteLocalItem(t.Context(), source.ID, "b"); err != nil {
		t.Fatal(err)
	}
	deleted := inspectManifest(t, store, source, "deleted", nil)
	if len(deleted.Manifest.Items) != 1 || deleted.Manifest.ID == revised.Manifest.ID {
		t.Fatal("deletion omitted from identity")
	}
	seedLocalItem(t, store, source, "b", "second\r\nbody")
	restored := inspectManifest(t, store, source, "restored", &first.Manifest.ID)
	if !*restored.Matches {
		t.Fatal("identical pins did not restore original fingerprint")
	}
	current, err := store.GetLocalItem(t.Context(), source.ID, "b")
	if err != nil {
		t.Fatal(err)
	}
	rep, normalized, err := mousa.NormalizeUTF8Text(current.Artifact, []byte("second\r\nbody"))
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
	for _, segment := range segments {
		if err := store.PutSegment(t.Context(), segment); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.ActivateLocalItem(t.Context(), source.ID, "b", rep.ID, normalized); err != nil {
		t.Fatal(err)
	}
	fixed := inspectManifest(t, store, source, "fixed", &first.Manifest.ID)
	if *fixed.Matches || fixed.Manifest.Items[1].SegmentationPolicy != mousa.TextSegmentFixedV1 {
		t.Fatal("segmentation replacement did not change fingerprint")
	}
}

func manifestDecisionCount(t *testing.T, store *Store) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM policy_decisions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSourceManifestCorruptionFailsWithoutReceipt(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"index bytes", `UPDATE segment_lexical_fts SET text='damaged'`},
		{"index missing", `DELETE FROM segment_lexical_fts`},
		{"index relation missing", `DELETE FROM segment_lexical_rows`},
		{"canonical missing", `DELETE FROM segment_lexical_fts; DELETE FROM segment_lexical_rows; DELETE FROM segments`},
		{"source root forged", `UPDATE sources SET record_json=CAST('{}' AS BLOB)`},
		{"representation forged", `UPDATE representations SET record_json=CAST('{}' AS BLOB)`},
		{"observation forged", `UPDATE observations SET record_json=CAST('{}' AS BLOB)`},
		{"selector projection", `UPDATE segments SET selector_end=x'0000000000000001'`},
		{"item substitution", `UPDATE local_items SET item_id='forged'`},
		{"recovery", `INSERT INTO local_recovery_sources(source_id) SELECT id FROM sources`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, source := manifestFixture(t)
			seedLocalItem(t, store, source, "a", "complete text")
			before := manifestDecisionCount(t, store)
			if _, err := store.db.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			got, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "corrupt"), nil)
			if err == nil || got.Manifest != nil || got.Matches != nil {
				t.Fatalf("certified corruption: %+v %v", got, err)
			}
			if after := manifestDecisionCount(t, store); after != before {
				t.Fatalf("partial receipt: %d -> %d", before, after)
			}
		})
	}
}

func TestSourceManifestDenialPrecedesCorruption(t *testing.T) {
	store, source := manifestFixture(t)
	seedLocalItem(t, store, source, "a", "private")
	withdrawVerifiedSource(t, store, source.ID, "withdraw-manifest", 3)
	if _, err := store.db.Exec(`UPDATE representations SET record_json=CAST('{}' AS BLOB); UPDATE sources SET record_json=CAST('{}' AS BLOB)`); err != nil {
		t.Fatal(err)
	}
	expected := mousa.SHA256{1}
	got, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "denied"), &expected)
	if err != nil || got.Decision.Outcome != mousa.PolicyOutcomeDeny || got.Manifest != nil || got.Matches != nil {
		t.Fatalf("denial: %+v %v", got, err)
	}
	unknown := mousa.SourceID{99}
	before := manifestDecisionCount(t, store)
	got, err = store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, unknown, "unknown"), &expected)
	if err != nil || got.Manifest != nil || got.Matches != nil || got.Decision.Outcome == mousa.PolicyOutcomeAllow {
		t.Fatalf("unknown denial: %+v %v", got, err)
	}
	if manifestDecisionCount(t, store) != before {
		t.Fatal("unknown source denial fabricated persistent state")
	}
}

func TestSourceManifestRejectsExtraHistoricalIndex(t *testing.T) {
	store, source := manifestFixture(t)
	seedLocalItem(t, store, source, "a", "old")
	old, err := store.GetLocalItem(t.Context(), source.ID, "a")
	if err != nil {
		t.Fatal(err)
	}
	seedLocalItem(t, store, source, "a", "new")
	inspectManifest(t, store, source, "history-allowed", nil)
	if err := store.IndexTextRepresentation(t.Context(), old.RepresentationID, []byte("old")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "extra-index"), nil); !IsCode(err, CodeIntegrity) {
		t.Fatalf("extra indexed historical row: %v", err)
	}
}

func TestSourceManifestBoundsAndVectorIndependence(t *testing.T) {
	store, source := manifestFixture(t)
	for i := range 101 {
		seedLocalItem(t, store, source, fmt.Sprintf("%03d", i), "x")
	}
	got := inspectManifest(t, store, source, "larger-than-vector", nil)
	if len(got.Manifest.Items) != 101 {
		t.Fatal("manifest truncated at vector limit")
	}
	if _, err := store.ExportEmbeddingInputs(t.Context(), testEvaluationRequest(t, source.ID, "vector-still-bounded")); !IsCode(err, CodeResourceLimit) {
		t.Fatalf("vector bound weakened: %v", err)
	}
	// Cap+1 enumeration must reject before following these deliberately invalid
	// pointers; a tiny result is not a certificate for the first 1000 items.
	if _, err := store.db.Exec(`WITH RECURSIVE n(x) AS (VALUES(101) UNION ALL SELECT x+1 FROM n WHERE x<1000) INSERT INTO local_items(source_id,item_id,representation_id,active) SELECT ?,printf('%04d',x),(SELECT representation_id FROM local_items LIMIT 1),1 FROM n`, source.ID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "over-items"), nil); !IsCode(err, CodeResourceLimit) {
		t.Fatalf("item bound: %v", err)
	}
}

func TestSourceManifestItemIdentifierBound(t *testing.T) {
	for _, length := range []int{4096, 4097} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			store, source := manifestFixture(t)
			seedLocalItem(t, store, source, strings.Repeat("x", length), "x")
			got, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "identifier"), nil)
			if length == 4096 && (err != nil || got.Manifest == nil) {
				t.Fatalf("exact bound: %v", err)
			}
			if length == 4097 && !IsCode(err, CodeResourceLimit) {
				t.Fatalf("over bound: %v", err)
			}
		})
	}
}

// This orders an already-held writer before inspection. It does not inject a
// competing write after the inspection has begun reading content.
func TestSourceManifestWriterOrdering(t *testing.T) {
	store, source := manifestFixture(t)
	seedLocalItem(t, store, source, "a", "before writer")
	old := inspectManifest(t, store, source, "old", nil)
	other, err := connect(t.Context(), store.path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), `BEGIN IMMEDIATE; UPDATE local_items SET active=0`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	started := make(chan struct{})
	done := make(chan error, 1)
	request := testEvaluationRequest(t, source.ID, "ordered-manifest")
	go func() {
		close(started)
		got, err := store.InspectSourceManifest(t.Context(), request, &old.Manifest.ID)
		if err == nil && (got.Manifest == nil || len(got.Manifest.Items) != 0 || got.Matches == nil || *got.Matches) {
			err = fmt.Errorf("mixed writer state: %+v", got)
		}
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("inspection bypassed writer: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := conn.ExecContext(t.Context(), `DELETE FROM segment_lexical_fts; DELETE FROM segment_lexical_rows; COMMIT`); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSourceManifestCancellationRollsBackAuthorization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{}, 1)
	functionName := fmt.Sprintf("manifest_cancel_%d", time.Now().UnixNano())
	if err := driverSQLite.RegisterScalarFunction(functionName, 0, func(*driverSQLite.FunctionContext, []driver.Value) (driver.Value, error) {
		called <- struct{}{}
		cancel()
		return int64(0), nil
	}); err != nil {
		t.Fatal(err)
	}
	store, source := manifestFixture(t)
	seedLocalItem(t, store, source, "a", "private")
	before := manifestDecisionCount(t, store)
	if _, err := store.db.Exec(`CREATE TEMP TRIGGER manifest_cancel AFTER INSERT ON policy_decisions BEGIN SELECT ` + functionName + `(); END`); err != nil {
		t.Fatal(err)
	}
	got, err := store.InspectSourceManifest(ctx, testEvaluationRequest(t, source.ID, "cancel-mid-operation"), nil)
	if err == nil || got.Manifest != nil {
		t.Fatalf("cancelled inspection: %+v %v", got, err)
	}
	select {
	case <-called:
	default:
		t.Fatal("cancellation was not injected after authorization write")
	}
	if after := manifestDecisionCount(t, store); after != before {
		t.Fatalf("cancellation retained receipt: %d -> %d", before, after)
	}
	if _, err := store.InspectSourceManifest(ctx, testEvaluationRequest(t, source.ID, "pre-cancelled"), nil); err == nil {
		t.Fatal("pre-cancelled inspection succeeded")
	}
}

func TestSourceManifestExcludesEmbeddingsAndQueryReceipts(t *testing.T) {
	store, source, input := vectorFixture(t)
	defer store.Close()
	before := inspectManifest(t, store, source, "before-vectors", nil)
	space, err := store.ImportEmbeddings(t.Context(), source.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EvaluateAndTraceVector(t.Context(), testEvaluationRequest(t, source.ID, "vector-receipt"), space, []float64{1, 0}, 100, mousa.PackingOriginal); err != nil {
		t.Fatal(err)
	}
	subject := mousa.NewRepresentationClassificationSubject(before.Manifest.Items[0].RepresentationID)
	if _, err := store.PutSourceClassification(t.Context(), source.ID, scopedClassification(t, subject, subject)); err != nil {
		t.Fatal(err)
	}
	after := inspectManifest(t, store, source, "after-vectors", &before.Manifest.ID)
	if !*after.Matches {
		t.Fatal("classification, embedding or query metadata changed content identity")
	}
}

func TestSourceManifestInvalidExpectationBeforeAuthorization(t *testing.T) {
	store, source := manifestFixture(t)
	before := manifestDecisionCount(t, store)
	zero := mousa.SHA256{}
	if _, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "invalid-expectation"), &zero); !IsCode(err, CodeInvalidRecord) {
		t.Fatalf("zero expectation: %v", err)
	}
	if manifestDecisionCount(t, store) != before {
		t.Fatal("invalid expectation wrote authorization")
	}
}

func TestSourceManifestMissingRootAndForeignCurrentPointer(t *testing.T) {
	t.Run("missing source root", func(t *testing.T) {
		store, source := manifestFixture(t)
		seedLocalItem(t, store, source, "a", "private")
		before := manifestDecisionCount(t, store)
		if _, err := store.db.Exec(`PRAGMA foreign_keys=OFF; DELETE FROM sources; PRAGMA foreign_keys=ON`); err != nil {
			t.Fatal(err)
		}
		got, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "missing-root"), nil)
		if err == nil || got.Manifest != nil || got.Matches != nil {
			t.Fatalf("missing root certified: %+v %v", got, err)
		}
		if manifestDecisionCount(t, store) != before {
			t.Fatal("missing root committed authorization")
		}
	})
	t.Run("foreign current pointer", func(t *testing.T) {
		store, source := manifestFixture(t)
		seedLocalItem(t, store, source, "a", "same text")
		foreignSource := source
		foreignSource.ExternalSourceID += "-foreign-manifest"
		var err error
		foreignSource.ID, err = mousa.NewSourceID(foreignSource.Namespace, foreignSource.ExternalSourceID)
		if err != nil {
			t.Fatal(err)
		}
		seedLocalItem(t, store, foreignSource, "a", "same text")
		foreign, err := store.GetLocalItem(t.Context(), foreignSource.ID, "a")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`UPDATE local_items SET representation_id=? WHERE source_id=?`, foreign.RepresentationID[:], source.ID[:]); err != nil {
			t.Fatal(err)
		}
		before := manifestDecisionCount(t, store)
		got, err := store.InspectSourceManifest(t.Context(), testEvaluationRequest(t, source.ID, "foreign-pointer"), nil)
		if !IsCode(err, CodeIntegrity) || got.Manifest != nil || got.Matches != nil {
			t.Fatalf("foreign pointer certified: %+v %v", got, err)
		}
		if manifestDecisionCount(t, store) != before {
			t.Fatal("foreign pointer committed authorization")
		}
	})
}

func TestSourceManifestExcludesAllowPolicyReplacement(t *testing.T) {
	store, source := manifestFixture(t)
	seedLocalItem(t, store, source, "a", "policy independent")
	before := inspectManifest(t, store, source, "before-policy", nil)
	definition := seedEvaluationFixture(t, store)
	definition.ExternalPolicyVersion = "2"
	var err error
	definition.ID, err = mousa.NewPolicyDefinitionID(definition.Namespace, definition.ExternalPolicyID, definition.ExternalPolicyVersion, definition.DefinitionMediaType, definition.DefinitionSchema, definition.DefinitionSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutPolicyDefinition(t.Context(), definition); err != nil {
		t.Fatal(err)
	}
	scope := mousa.NewDeploymentPolicyScope()
	bindingID, err := mousa.NewPolicyBindingID("example", "deployment", "2", scope, definition.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := mousa.PolicyBinding{Schema: mousa.PolicyBindingSchema, ID: bindingID, Namespace: "example", ExternalBindingID: "deployment", ExternalBindingVersion: "2", Scope: scope, PolicyDefinitionID: definition.ID}
	if err := store.PutPolicyBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	previous := testPolicyActivation(t, "example", "deployment", "activate", nil, nil)
	activation := testPolicyActivation(t, "example", "deployment", "manifest-replacement", &previous.ID, &binding.ID)
	if err := store.ApplyPolicyActivation(t.Context(), activation); err != nil {
		t.Fatal(err)
	}
	after := inspectManifest(t, store, source, "after-policy", &before.Manifest.ID)
	if !*after.Matches || after.Decision.ID == before.Decision.ID {
		t.Fatal("allow-policy replacement changed content identity or reused authorization")
	}
}

func TestSourceManifestExcludesAssociationsAndSupersession(t *testing.T) {
	store := openLexicalStore(t)
	defer store.Close()
	sourceID, from, to := seedAssociatedItems(t, store)
	source, err := store.GetSource(t.Context(), sourceID)
	if err != nil {
		t.Fatal(err)
	}
	before := inspectManifest(t, store, source, "before-metadata", nil)
	traced := associateQuery(t, store, sourceID, "manifest-association", "restart", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{drainDeclaration()})
	if traced.Trail.Schema != mousa.SourceTrailSchemaV3 || len(traced.Trail.Associated) == 0 {
		t.Fatal("fixture did not record an association")
	}
	predecessor, err := store.GetLocalItem(t.Context(), sourceID, from)
	if err != nil {
		t.Fatal(err)
	}
	successor, err := store.GetLocalItem(t.Context(), sourceID, to)
	if err != nil {
		t.Fatal(err)
	}
	declaration := supersessionTestDeclaration(t, sourceID, from, predecessor.RepresentationID, to, successor.RepresentationID, "maintainer", "replacement")
	if err := store.PutSupersessionDeclaration(t.Context(), declaration); err != nil {
		t.Fatal(err)
	}
	activation := activationTestTransition(t, sourceID, mousa.SupersessionActivationID{}, declaration.ID, "select replacement", activationBaseUsec)
	if err := store.ApplySupersessionActivation(t.Context(), activation); err != nil {
		t.Fatal(err)
	}
	after := inspectManifest(t, store, source, "after-metadata", &before.Manifest.ID)
	if !*after.Matches {
		t.Fatal("association or supersession metadata changed content identity")
	}
}

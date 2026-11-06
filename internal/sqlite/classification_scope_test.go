package sqlite

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/graydeon/mousa/internal/mousa"
)

func scopedClassification(t *testing.T, subject mousa.ClassificationSubject, basis ...mousa.ClassificationSubject) mousa.Classification {
	t.Helper()
	record := mousa.Classification{Schema: mousa.ClassificationSchema, Subject: subject, Taxonomy: "example.scope", TaxonomyVersion: "1", Label: "private", AsserterID: "test", AsserterVersion: "1", Basis: basis, AssertedAtUsec: 100, ConfidencePPM: 0}
	record.ID = classificationID(t, record)
	return record
}

func classificationCounts(t *testing.T, store *Store) map[string]int {
	t.Helper()
	counts := freshAuditCounts(t, store)
	for _, table := range []string{"sources", "observations", "artifacts", "representations", "segments", "classifications", "classification_bases"} {
		var count int
		if err := store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		counts[table] = count
	}
	return counts
}

func TestSourceClassificationAllKindsAndNeverIndexedAncestry(t *testing.T) {
	store, template := classificationStoreFixture(t)
	defer store.Close()
	graph, _ := classificationRecordGraph(t)
	seedEvaluationFixture(t, store)
	sequence := uint64(1)
	if err := store.ApplyIngest(t.Context(), testPushBatch(t, "classification-active-source", &sequence)); err != nil {
		t.Fatal(err)
	}
	for _, subject := range template.Basis {
		t.Run(string(subject.Kind()), func(t *testing.T) {
			record := scopedClassification(t, subject, template.Basis...)
			before := classificationCounts(t, store)
			for range 2 {
				got, err := store.PutSourceClassification(t.Context(), graph.source.ID, record)
				if err != nil || !reflect.DeepEqual(got, record) {
					t.Fatalf("put/retry = %#v, %v", got, err)
				}
			}
			after := classificationCounts(t, store)
			if after["classifications"] != before["classifications"]+1 || after["classification_bases"] != before["classification_bases"]+len(record.Basis) {
				t.Fatal("retry appended duplicate rows")
			}
			for _, table := range []string{"sources", "observations", "artifacts", "representations", "segments"} {
				if before[table] != after[table] {
					t.Fatalf("put created parent in %s", table)
				}
			}
			got, err := store.InspectClassification(t.Context(), testEvaluationRequest(t, graph.source.ID, string(subject.Kind())), record.ID)
			if err != nil || got.Decision.Outcome != mousa.PolicyOutcomeAllow || !reflect.DeepEqual(got.Classification, &record) {
				t.Fatalf("inspection = %#v, %v", got, err)
			}
			record.AsserterVersion = "2"
			record.ID = classificationID(t, record)
			if _, err := store.PutSourceClassification(t.Context(), graph.source.ID, record); err != nil {
				t.Fatal(err)
			}
			if classificationCounts(t, store)["classifications"] != before["classifications"]+2 {
				t.Fatal("different assertion did not append")
			}
		})
	}
}

func TestSourceClassificationForeignMixedAndDamagedParents(t *testing.T) {
	for _, mode := range []string{"foreign subject", "foreign basis", "damaged foreign basis", "mixed subject", "mixed basis", "missing parent", "missing source"} {
		t.Run(mode, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			foreign := addVerifiedLexicalDocument(t, f.store, "classification-foreign", "foreign evidence")
			mixed := putVerifiedRepresentation(t, f.store, []mousa.DerivationInput{mousa.NewRepresentationDerivationInput(f.predecessor.ID), mousa.NewRepresentationDerivationInput(foreign.representation.ID)}, "classification-mixed", []byte("mixed evidence"), true)
			local := mousa.NewRepresentationClassificationSubject(f.predecessor.ID)
			remote := mousa.NewRepresentationClassificationSubject(foreign.representation.ID)
			mixedSubject := mousa.NewRepresentationClassificationSubject(mixed.representation.ID)
			record := scopedClassification(t, local, local)
			sourceID := f.source.ID
			switch mode {
			case "foreign subject":
				record.Subject = remote
			case "foreign basis", "damaged foreign basis":
				record.Basis = []mousa.ClassificationSubject{local, remote}
			case "mixed subject":
				record.Subject = mixedSubject
			case "mixed basis":
				record.Basis = []mousa.ClassificationSubject{mixedSubject}
			case "missing parent":
				record.Subject = mousa.NewRepresentationClassificationSubject(mousa.RepresentationID(testDigest("absent")))
			case "missing source":
				sourceID = mousa.SourceID(testDigest("absent"))
			}
			record.ID = classificationID(t, record)
			if mode != "missing parent" {
				if err := f.store.PutClassification(t.Context(), record); err != nil {
					t.Fatalf("native cross-source append: %v", err)
				}
			}
			if mode == "damaged foreign basis" {
				if _, err := f.store.db.Exec(`UPDATE representations SET record_json = ? WHERE id = ?`, []byte("{}"), foreign.representation.ID[:]); err != nil {
					t.Fatal(err)
				}
			}
			before := classificationCounts(t, f.store)
			if _, err := f.store.PutSourceClassification(t.Context(), sourceID, record); !IsCode(err, CodeNotFound) || !strings.Contains(err.Error(), "classification unavailable") {
				t.Fatalf("scoped put leaked foreign details: %v", err)
			}
			got, err := f.store.InspectClassification(t.Context(), testEvaluationRequest(t, f.source.ID, mode), record.ID)
			if mode == "missing source" {
				if err != nil || got.Classification == nil {
					t.Fatalf("existing local record lost: %#v %v", got, err)
				}
				return
			}
			if !IsCode(err, CodeNotFound) || got.Classification != nil || !strings.Contains(err.Error(), "classification unavailable") {
				t.Fatalf("scoped get leaked foreign details: %#v, %v", got, err)
			}
			if !reflect.DeepEqual(before, classificationCounts(t, f.store)) {
				t.Fatal("refused calls mutated records or committed receipt")
			}
		})
	}
}

func TestInspectClassificationProjectionEligibility(t *testing.T) {
	for _, mode := range []string{"foreign canonical subject", "foreign canonical basis", "foreign projection", "foreign basis projection", "local contradiction", "missing trailing basis", "ordinal gap", "malformed bytes", "noncanonical bytes"} {
		t.Run(mode, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			foreign := addVerifiedLexicalDocument(t, f.store, "classification-hidden", "hidden evidence")
			local := mousa.NewRepresentationClassificationSubject(f.predecessor.ID)
			record := scopedClassification(t, local, local)
			if mode == "foreign canonical subject" {
				record.Subject = mousa.NewRepresentationClassificationSubject(foreign.representation.ID)
			}
			if mode == "foreign canonical basis" {
				record.Basis = []mousa.ClassificationSubject{mousa.NewRepresentationClassificationSubject(foreign.representation.ID)}
			}
			record.ID = classificationID(t, record)
			if err := f.store.PutClassification(t.Context(), record); err != nil {
				t.Fatal(err)
			}
			statement := ""
			var args []any
			want := CodeNotFound
			switch mode {
			case "foreign canonical subject":
				statement, args = `UPDATE classifications SET subject_representation_id = ?`, []any{f.predecessor.ID[:]}
			case "foreign canonical basis":
				statement, args = `UPDATE classification_bases SET basis_representation_id = ?`, []any{f.predecessor.ID[:]}
			case "foreign projection":
				statement, args = `UPDATE classifications SET subject_representation_id = ?`, []any{foreign.representation.ID[:]}
			case "foreign basis projection":
				statement, args = `UPDATE classification_bases SET basis_representation_id = ?`, []any{foreign.representation.ID[:]}
			case "local contradiction":
				statement, args = `UPDATE classifications SET subject_representation_id = ?`, []any{f.successor.ID[:]}
				want = CodeIntegrity
			case "missing trailing basis":
				statement = `DELETE FROM classification_bases`
				want = CodeIntegrity
			case "ordinal gap":
				statement = `UPDATE classification_bases SET ordinal = 9`
			case "malformed bytes":
				statement, args = `UPDATE classifications SET record_json = ?`, []any{[]byte("{}")}
			case "noncanonical bytes":
				statement, args = `UPDATE classifications SET record_json = ?`, []any{[]byte(" " + classificationJSON(t, record))}
				want = CodeIntegrity
			}
			if _, err := f.store.db.Exec(statement, args...); err != nil {
				t.Fatal(err)
			}
			before := classificationCounts(t, f.store)
			got, err := f.store.InspectClassification(t.Context(), testEvaluationRequest(t, f.source.ID, mode), record.ID)
			if !IsCode(err, want) || got.Classification != nil || !reflect.DeepEqual(before, classificationCounts(t, f.store)) {
				t.Fatalf("projection eligibility = %#v, %v; want %s and rollback", got, err, want)
			}
			request := testEvaluationRequest(t, f.source.ID, mode+"-denied")
			request.ExternalCallerID = "not-authorized"
			request.ID, err = mousa.NewPolicyEvaluationRequestID(request)
			if err != nil {
				t.Fatal(err)
			}
			got, err = f.store.InspectClassification(t.Context(), request, record.ID)
			if err != nil || got.Decision.Outcome != mousa.PolicyOutcomeDeny || got.Classification != nil {
				t.Fatalf("denial consulted hidden damage: %#v, %v", got, err)
			}
		})
	}
}

func TestSourceClassificationHistoricalWithdrawnAndRollback(t *testing.T) {
	f := newV4Fixture(t)
	defer f.store.Close()
	_, replacement, text := prepareLocalRevision(t, f.store, "predecessor", "new replacement")
	if _, err := f.store.ActivateLocalItem(t.Context(), f.source.ID, "predecessor", replacement.ID, text); err != nil {
		t.Fatal(err)
	}
	_, never, _ := prepareLocalRevision(t, f.store, "never-indexed", "never indexed")
	record := scopedClassification(t, mousa.NewRepresentationClassificationSubject(f.predecessor.ID), mousa.NewRepresentationClassificationSubject(never.ID))
	if _, err := f.store.PutSourceClassification(t.Context(), f.source.ID, record); err != nil {
		t.Fatal(err)
	}
	got, err := f.store.InspectClassification(t.Context(), testEvaluationRequest(t, f.source.ID, "historical"), record.ID)
	if err != nil || !reflect.DeepEqual(got.Classification, &record) {
		t.Fatalf("historical pins: %#v, %v", got, err)
	}
	withdrawVerifiedSource(t, f.store, f.source.ID, "withdraw-classification", 500)
	record.AsserterVersion = "withdrawn"
	record.ID = classificationID(t, record)
	if _, err := f.store.PutSourceClassification(t.Context(), f.source.ID, record); err != nil {
		t.Fatalf("withdrawn administration: %v", err)
	}
	got, err = f.store.InspectClassification(t.Context(), testEvaluationRequest(t, f.source.ID, "withdrawn"), record.ID)
	if err != nil || got.Decision.Outcome != mousa.PolicyOutcomeDeny || got.Classification != nil {
		t.Fatalf("withdrawn visibility: %#v, %v", got, err)
	}
	before := classificationCounts(t, f.store)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.store.PutSourceClassification(ctx, f.source.ID, record); err == nil {
		t.Fatal("cancelled put succeeded")
	}
	if _, err := f.store.InspectClassification(ctx, testEvaluationRequest(t, f.source.ID, "cancelled"), record.ID); err == nil {
		t.Fatal("cancelled inspection succeeded")
	}
	if _, err := f.store.db.Exec(`CREATE TRIGGER reject_classification_basis BEFORE INSERT ON classification_bases BEGIN SELECT RAISE(ABORT, 'injected basis failure'); END`); err != nil {
		t.Fatal(err)
	}
	record.AsserterVersion = "rollback"
	record.ID = classificationID(t, record)
	if _, err := f.store.PutSourceClassification(t.Context(), f.source.ID, record); err == nil {
		t.Fatal("basis insertion failure accepted")
	}
	if !reflect.DeepEqual(before, classificationCounts(t, f.store)) {
		t.Fatal("failed write left rows or receipts")
	}
}

func TestInspectClassificationOwnsWriterSnapshot(t *testing.T) {
	f := newV4Fixture(t)
	defer f.store.Close()
	local := mousa.NewRepresentationClassificationSubject(f.predecessor.ID)
	record := scopedClassification(t, local, local)
	if _, err := f.store.PutSourceClassification(t.Context(), f.source.ID, record); err != nil {
		t.Fatal(err)
	}
	other, err := Open(t.Context(), f.store.path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(t.Context(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := f.store.InspectClassification(ctx, testEvaluationRequest(t, f.source.ID, "blocked"), record.ID); err == nil {
		t.Fatal("inspection bypassed owning writer transaction")
	}
	if _, err := conn.ExecContext(t.Context(), "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	// Atomically exchange two valid classification views under the same lookup
	// ID. The alternate is intentionally ID-inconsistent: a reader must see
	// either the original record or integrity, never release an unverified mix.
	alternate := record
	alternate.Subject = mousa.NewRepresentationClassificationSubject(f.successor.ID)
	alternate.ID = classificationID(t, alternate)
	originalJSON := []byte(classificationJSON(t, record))
	alternateJSON := []byte(classificationJSON(t, alternate))
	round := make(chan struct{})
	finished := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 10 {
			if _, ok := <-round; !ok {
				return
			}
			data, subject := originalJSON, f.predecessor.ID
			if i%2 == 0 {
				data, subject = alternateJSON, f.successor.ID
			}
			_, err := other.db.ExecContext(t.Context(), `UPDATE classifications SET record_json = ?, subject_representation_id = ? WHERE id = ?`, data, subject[:], record.ID[:])
			finished <- err
		}
	})
	for i := range 10 {
		round <- struct{}{}
		got, err := f.store.InspectClassification(t.Context(), testEvaluationRequest(t, f.source.ID, fmt.Sprintf("competing-%d", i)), record.ID)
		if err == nil {
			if !reflect.DeepEqual(got.Classification, &record) {
				t.Errorf("released mixed snapshot: %#v", got)
			}
		} else if !IsCode(err, CodeIntegrity) || got.Classification != nil {
			t.Errorf("unexpected snapshot result: %#v, %v", got, err)
		}
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}
	close(round)
	wg.Wait()
}

func TestSourceClassificationBoundsAndCorruptRetry(t *testing.T) {
	store, template := classificationStoreFixture(t)
	defer store.Close()
	graph, _ := classificationRecordGraph(t)
	t.Run("basis boundary", func(t *testing.T) {
		for _, count := range []int{maxClassificationBases, maxClassificationBases + 1} {
			basis := make([]mousa.ClassificationSubject, count)
			for i := range basis {
				basis[i] = mousa.NewSourceClassificationSubject(mousa.SourceID(testDigest(fmt.Sprintf("missing-basis-%d", i))))
			}
			record := template
			record.Basis = basis
			if count <= maxClassificationBases {
				record.ID = classificationID(t, record)
			}
			before := classificationCounts(t, store)
			_, err := store.PutSourceClassification(t.Context(), graph.source.ID, record)
			want := CodeNotFound
			if count > maxClassificationBases {
				want = CodeInvalidRecord
			}
			if !IsCode(err, want) || !reflect.DeepEqual(before, classificationCounts(t, store)) {
				t.Fatalf("basis count %d: %v; want %s without placeholder parents", count, err, want)
			}
		}
	})
	t.Run("duplicate basis", func(t *testing.T) {
		record := template
		record.Basis = []mousa.ClassificationSubject{template.Subject, template.Subject}
		if _, err := store.PutSourceClassification(t.Context(), graph.source.ID, record); !IsCode(err, CodeInvalidRecord) {
			t.Fatalf("duplicate basis: %v", err)
		}
	})
	t.Run("record bytes", func(t *testing.T) {
		record := template
		record.Label = strings.Repeat("x", maxRecordBytes)
		record.ID = classificationID(t, record)
		if _, err := store.PutSourceClassification(t.Context(), graph.source.ID, record); !IsCode(err, CodeResourceLimit) {
			t.Fatalf("oversized record: %v", err)
		}
	})
	t.Run("damaged duplicate is not repaired", func(t *testing.T) {
		if _, err := store.PutSourceClassification(t.Context(), graph.source.ID, template); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`DELETE FROM classification_bases WHERE classification_id = ? AND ordinal = 4`, template.ID[:]); err != nil {
			t.Fatal(err)
		}
		before := classificationCounts(t, store)
		if _, err := store.PutSourceClassification(t.Context(), graph.source.ID, template); !IsCode(err, CodeConflict) {
			t.Fatalf("damaged retry: %v", err)
		}
		if !reflect.DeepEqual(before, classificationCounts(t, store)) {
			t.Fatal("retry repaired damaged projection")
		}
	})
}

func TestInspectClassificationMalformedTypedProjections(t *testing.T) {
	for _, damage := range []string{
		`UPDATE classifications SET subject_kind = 'future'`,
		`UPDATE classifications SET subject_source_id = (SELECT id FROM sources LIMIT 1)`,
		`UPDATE classification_bases SET basis_kind = 'future'`,
	} {
		t.Run(damage, func(t *testing.T) {
			f := newV4Fixture(t)
			defer f.store.Close()
			subject := mousa.NewRepresentationClassificationSubject(f.predecessor.ID)
			record := scopedClassification(t, subject, subject)
			if _, err := f.store.PutSourceClassification(t.Context(), f.source.ID, record); err != nil {
				t.Fatal(err)
			}
			conn, err := f.store.db.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := conn.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = ON`); err != nil {
				conn.Close()
				t.Fatal(err)
			}
			_, damageErr := conn.ExecContext(t.Context(), damage)
			_, resetErr := conn.ExecContext(t.Context(), `PRAGMA ignore_check_constraints = OFF`)
			conn.Close()
			if damageErr != nil || resetErr != nil {
				t.Fatalf("damage fixture: %v, %v", damageErr, resetErr)
			}
			before := classificationCounts(t, f.store)
			got, err := f.store.InspectClassification(t.Context(), testEvaluationRequest(t, f.source.ID, damage), record.ID)
			if !IsCode(err, CodeNotFound) || got.Classification != nil || !reflect.DeepEqual(before, classificationCounts(t, f.store)) {
				t.Fatalf("malformed projection disclosed integrity: %#v, %v", got, err)
			}
		})
	}
}

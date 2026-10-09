package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

// seedLocalItem ingests one additional local item of an existing source with passage segmentation,
// so the item carries addressable passages and declarable item identity.
func seedLocalItem(t *testing.T, store *Store, source mousa.Source, item, text string) {
	t.Helper()
	ctx := context.Background()
	digest := testDigest(text)
	external := fmt.Sprintf("item/%s@%x", item, digest)
	observationID, err := mousa.NewObservationID(source.ID, external)
	if err != nil {
		t.Fatal(err)
	}
	observation := mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: source.ID, ExternalObservationID: external}
	artifactID, err := mousa.NewArtifactID(observationID, "body")
	if err != nil {
		t.Fatal(err)
	}
	artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: "body",
		MediaType: mousa.UTF8TextMediaType, ContentSHA256: digest, ByteLength: uint64(len(text))}
	batch := mousa.IngestBatch{AdapterID: "test", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem,
		CapturedAtUsec: 2, Source: source, Observation: observation, Artifacts: []mousa.Artifact{artifact}}
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatalf("ApplyIngest: %v", err)
	}
	representation, normalized, err := mousa.NormalizeUTF8TextWithPolicy(artifact, []byte(text), mousa.TextSegmentPassageV1)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutRepresentation(ctx, representation); err != nil {
		t.Fatal(err)
	}
	segments, err := mousa.SegmentUTF8Text(representation, normalized)
	if err != nil {
		t.Fatal(err)
	}
	for _, segment := range segments {
		if err := store.PutSegment(ctx, segment); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.IndexTextRepresentation(ctx, representation.ID, normalized); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ActivateLocalItem(ctx, source.ID, item, representation.ID, normalized); err != nil {
		t.Fatal(err)
	}
}

// seedAssociatedItems ingests two independent items of one source: a procedure item and the
// separate item whose qualification the procedure requires. This fixture is deliberately not the
// documentation backup structure: the procedure is an operations runbook and the qualification is
// a release-scheduling caveat in another item.
func seedAssociatedItems(t *testing.T, store *Store) (mousa.SourceID, string, string) {
	t.Helper()
	ctx := context.Background()
	seedEvaluationFixture(t, store)
	sequence := uint64(1)
	batch := testPushBatch(t, "operations", &sequence)
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatalf("ApplyIngest: %v", err)
	}
	seedLocalItem(t, store, batch.Source, "rollback.md", "Rolling restart\n\nPerform the rolling restart one server at a time.\n\nVerify health after every server before continuing.\n")
	seedLocalItem(t, store, batch.Source, "drain.md", "Shutdown caveat\n\nStopping a server does not drain connections.\n\nDrain client connections before each server stops.\n")
	seedLocalItem(t, store, batch.Source, "unrelated.md", "Gardening notes\n\nCompost requires air and moisture.\n")
	return batch.Source.ID, "rollback.md", "drain.md"
}

func associateQuery(t *testing.T, store *Store, sourceID mousa.SourceID, externalRequestID, expression string, budget uint64, packingPolicy string, associations []mousa.AssociationDeclaration) TracedLexicalResult {
	t.Helper()
	request := testEvaluationRequest(t, sourceID, externalRequestID)
	traced, err := store.EvaluateAndTraceAssociatedLexical(context.Background(), request, expression, 10, budget, packingPolicy, associations)
	if err != nil {
		t.Fatalf("EvaluateAndTraceAssociatedLexical: %v", err)
	}
	return traced
}

func drainDeclaration() mousa.AssociationDeclaration {
	return mousa.AssociationDeclaration{
		FromItem: "rollback.md", ToItem: "drain.md",
		Basis:  "The rolling restart uses server shutdown, and the shutdown connection caveat is documented in the drain item.",
		Author: "operations-runbook maintainer",
	}
}

// The association must deliver the qualification without the caller naming it.
func TestAssociatedLexicalReleasesQualification(t *testing.T) {
	store := openLexicalStore(t)
	defer store.Close()
	sourceID, _, _ := seedAssociatedItems(t, store)

	// RED against the unassociated path: the query for the procedure cannot see the caveat.
	before := associateQuery(t, store, sourceID, "before-associations", "restart", 1<<20, mousa.PackingOriginal, nil)
	if len(before.Trail.Candidates) == 0 || before.Trail.Schema != mousa.SourceTrailSchema {
		t.Fatalf("baseline trace changed: %#v", before.Trail)
	}
	for _, candidate := range before.Trail.Candidates {
		if candidate.SegmentID.String() == "" {
			t.Fatal("baseline candidate without identity")
		}
	}

	traced := associateQuery(t, store, sourceID, "with-associations", "restart", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{drainDeclaration()})
	if traced.Trail.Schema != mousa.SourceTrailSchemaV3 {
		t.Fatalf("associated trail schema = %s", traced.Trail.Schema)
	}
	released := 0
	for index, row := range traced.Trail.Associated {
		if row.ToItem != "drain.md" || row.FromItem != "rollback.md" || row.Basis == "" || row.Author == "" {
			t.Fatalf("associated row lost its declaration: %#v", row)
		}
		if !row.Selected {
			continue
		}
		released++
		passage := traced.Associated[index]
		if passage.Segment.ID != row.SegmentID || passage.Segment.ContentSHA256 != row.ContentSHA256 {
			t.Fatalf("associated passage identity disagrees with its trail row")
		}
		if uint64(len(passage.Text)) != row.TextBytes {
			t.Fatalf("associated passage size disagrees with its trail row")
		}
		if passage.Item != "drain.md" {
			t.Fatalf("associated passage item = %q", passage.Item)
		}
	}
	if released == 0 {
		t.Fatalf("no associated passage was released: rows=%#v omissions=%#v", traced.Trail.Associated, traced.Trail.AssociationOmissions)
	}
	if traced.Trail.UsedBytes <= before.Trail.UsedBytes {
		t.Fatalf("associated bytes were not accounted: used %d after %d before", traced.Trail.UsedBytes, before.Trail.UsedBytes)
	}
	if err := traced.Trail.Validate(); err != nil {
		t.Fatalf("associated trail invalid: %v", err)
	}
}

// A declaration that never fires changes nothing, byte for byte.
func TestAssociatedLexicalWithoutFiringDeclarationIsUnchanged(t *testing.T) {
	store := openLexicalStore(t)
	defer store.Close()
	sourceID, _, _ := seedAssociatedItems(t, store)
	declaration := mousa.AssociationDeclaration{FromItem: "unrelated.md", ToItem: "drain.md",
		Basis: "declared but never the primary evidence", Author: "maintainer"}
	traced := associateQuery(t, store, sourceID, "unfired", "restart", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{declaration})
	if traced.Trail.Schema != mousa.SourceTrailSchema {
		t.Fatalf("unfired declaration changed the schema: %s", traced.Trail.Schema)
	}
	if len(traced.Associated) != 0 || len(traced.Trail.Associated) != 0 || len(traced.Trail.AssociationOmissions) != 0 {
		t.Fatalf("unfired declaration recorded association state: %#v", traced.Trail)
	}
}

// Unrelated queries keep their established behavior even with declarations supplied.
func TestAssociatedLexicalUnrelatedQueryUnchanged(t *testing.T) {
	store := openLexicalStore(t)
	defer store.Close()
	sourceID, _, _ := seedAssociatedItems(t, store)
	plain := associateQuery(t, store, sourceID, "unrelated-plain", "compost", 1<<20, mousa.PackingOriginal, nil)
	declared := associateQuery(t, store, sourceID, "unrelated-declared", "compost", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{drainDeclaration()})
	if plain.Trail.Schema != declared.Trail.Schema || plain.Trail.PacketID != declared.Trail.PacketID ||
		plain.Trail.UsedBytes != declared.Trail.UsedBytes || len(plain.Trail.Candidates) != len(declared.Trail.Candidates) {
		t.Fatalf("declared unrelated query changed the selection: %#v vs %#v", plain.Trail, declared.Trail)
	}
	if len(declared.Associated) != 0 {
		t.Fatalf("unrelated query released associated passages: %#v", declared.Associated)
	}
}

// A declared target that is missing or inactive is an omission, never a release.
func TestAssociatedLexicalStaleTargetIsOmitted(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	sourceID, _, _ := seedAssociatedItems(t, store)

	unknown := mousa.AssociationDeclaration{FromItem: "rollback.md", ToItem: "absent.md", Basis: "stale declaration", Author: "maintainer"}
	traced := associateQuery(t, store, sourceID, "unknown-target", "restart", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{unknown})
	if len(traced.Associated) != 0 || traced.Trail.Schema != mousa.SourceTrailSchemaV3 {
		t.Fatalf("unknown target should record only an omission: %#v", traced.Trail)
	}
	if len(traced.Trail.AssociationOmissions) != 1 || traced.Trail.AssociationOmissions[0].Reason != mousa.AssociationTargetUnknown {
		t.Fatalf("unknown target omission = %#v", traced.Trail.AssociationOmissions)
	}

	// Deactivate the target: the current revision is not released through the relationship.
	if _, err := store.DeleteLocalItem(ctx, sourceID, "drain.md"); err != nil {
		t.Fatal(err)
	}
	inactive := associateQuery(t, store, sourceID, "inactive-target", "restart", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{drainDeclaration()})
	if len(inactive.Associated) != 0 {
		t.Fatalf("inactive target released passages: %#v", inactive.Associated)
	}
	if len(inactive.Trail.AssociationOmissions) != 1 || inactive.Trail.AssociationOmissions[0].Reason != mousa.AssociationTargetInactive {
		t.Fatalf("inactive target omission = %#v", inactive.Trail.AssociationOmissions)
	}
}

// Fan-out stays bounded and visible.
func TestAssociatedLexicalFanOutBounded(t *testing.T) {
	store := openLexicalStore(t)
	defer store.Close()
	sourceID, _, _ := seedAssociatedItems(t, store)
	declarations := make([]mousa.AssociationDeclaration, 0, 7)
	targets := []string{"drain.md", "unrelated.md", "absent1.md", "absent2.md", "absent3.md", "absent4.md", "absent5.md"}
	for _, target := range targets {
		declarations = append(declarations, mousa.AssociationDeclaration{
			FromItem: "rollback.md", ToItem: target, Basis: "fan-out probe", Author: "maintainer",
		})
	}
	traced := associateQuery(t, store, sourceID, "fan-out", "restart", 1<<20, mousa.PackingOriginal, declarations)
	applied := map[string]bool{}
	for _, row := range traced.Trail.Associated {
		applied[row.ToItem] = true
	}
	bounded := 0
	for _, omission := range traced.Trail.AssociationOmissions {
		if omission.Reason != mousa.AssociationFanOut && omission.Reason != mousa.AssociationDuplicateTarget &&
			omission.Reason != mousa.AssociationTargetUnknown {
			t.Fatalf("declaration produced an unexpected omission: %#v", omission)
		}
		bounded++
	}
	if len(applied) > mousa.MaxAssociationTargets {
		t.Fatalf("applied %d targets", len(applied))
	}
	if len(applied)+bounded != len(targets) {
		t.Fatalf("applied %d targets with %d recorded omissions for %d declarations", len(applied), bounded, len(targets))
	}
	if err := traced.Trail.Validate(); err != nil {
		t.Fatalf("fan-out trail invalid: %v", err)
	}
}

// A small budget keeps primary priority and exposes the omission instead of displacing primary
// evidence or claiming completeness.
func TestAssociatedLexicalSmallBudgetOmitsWithContext(t *testing.T) {
	store := openLexicalStore(t)
	defer store.Close()
	sourceID, _, _ := seedAssociatedItems(t, store)
	traced := associateQuery(t, store, sourceID, "small-budget", "restart", 1, mousa.PackingOriginal, []mousa.AssociationDeclaration{drainDeclaration()})
	if traced.Trail.UsedBytes != 0 || len(traced.Trail.Associated) != 0 || traced.Trail.Schema != mousa.SourceTrailSchema {
		t.Fatalf("one-byte budget released or recorded association state: %#v", traced.Trail)
	}
}

// A declaration is not an access grant: a denied or withdrawn source releases nothing, including
// through declared relationships.
// A declaration is not an access grant: a denied source releases nothing, including through
// declared relationships. The deny reuses the deployment-scope policy fixture the other
// retrieval tests evaluate against.
func TestAssociatedLexicalDenyReleasesNothing(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	sourceID, _, _ := seedAssociatedItems(t, store)
	// Activate the deny binding so current evaluation denies retrieval for every caller.
	scope := mousa.NewDeploymentPolicyScope()
	data, err := mousa.EncodeSourceRetrievalPolicy(mousa.SourceRetrievalPolicy{
		Schema: mousa.SourceRetrievalPolicySchema, Action: mousa.SourceRetrievalAction,
		CallerNamespace: "example.caller", ExternalCallerID: "caller-1",
		PurposeNamespace: "example.purpose", ExternalPurposeID: "purpose-1",
		Effect: mousa.SourceRetrievalEffectDeny,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := testDigest(string(data))
	denyID, err := mousa.NewPolicyDefinitionID("example", "deny", "1", mousa.SourceRetrievalPolicyMediaType, mousa.SourceRetrievalPolicySchema, digest)
	if err != nil {
		t.Fatal(err)
	}
	deny := mousa.PolicyDefinition{
		Schema: mousa.PolicyDefinitionSchema, ID: denyID, Namespace: "example",
		ExternalPolicyID: "deny", ExternalPolicyVersion: "1",
		DefinitionMediaType: mousa.SourceRetrievalPolicyMediaType, DefinitionSchema: mousa.SourceRetrievalPolicySchema,
		DefinitionSHA256: digest, Definition: string(data),
	}
	if err := store.PutPolicyDefinition(ctx, deny); err != nil {
		t.Fatal(err)
	}
	bindingID, err := mousa.NewPolicyBindingID("example", "deployment-deny", "deny", scope, deny.ID)
	if err != nil {
		t.Fatal(err)
	}
	binding := mousa.PolicyBinding{
		Schema: mousa.PolicyBindingSchema, ID: bindingID, Namespace: "example",
		ExternalBindingID: "deployment-deny", ExternalBindingVersion: "deny",
		Scope: scope, PolicyDefinitionID: deny.ID,
	}
	if err := store.PutPolicyBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	activation := testPolicyActivation(t, "example", "deployment-deny", "activate-deny", nil, &binding.ID)
	if err := store.ApplyPolicyActivation(ctx, activation); err != nil {
		t.Fatal(err)
	}
	request := testEvaluationRequest(t, sourceID, "denied-associated")
	traced, err := store.EvaluateAndTraceAssociatedLexical(ctx, request, "restart", 10, 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{drainDeclaration()})
	if err != nil {
		t.Fatalf("traced retrieval under deny: %v", err)
	}
	if traced.Trail.Outcome != string(mousa.PolicyOutcomeDeny) || len(traced.Trail.Candidates) != 0 ||
		len(traced.Trail.Associated) != 0 || len(traced.Trail.AssociationOmissions) != 0 {
		t.Fatalf("deny released evidence or association state: %#v", traced.Trail)
	}
	if traced.Trail.Schema == mousa.SourceTrailSchemaV3 {
		t.Fatal("deny must not record an association stage")
	}
}

// seedRepeatedPrimaryItem seeds a procedure item that repeats one passage byte for byte, plus the
// declared target item whose qualification the procedure requires.
func seedRepeatedPrimaryItem(t *testing.T, store *Store) mousa.SourceID {
	t.Helper()
	ctx := context.Background()
	seedEvaluationFixture(t, store)
	sequence := uint64(1)
	batch := testPushBatch(t, "operations", &sequence)
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatalf("ApplyIngest: %v", err)
	}
	passage := "# Restart\n\nRestart one server at a time.\n\n"
	seedLocalItem(t, store, batch.Source, "rollback.md", passage+passage)
	seedLocalItem(t, store, batch.Source, "drain.md", "Shutdown caveat\n\nStopping a server does not drain connections.\n")
	return batch.Source.ID
}

// Repeated byte-identical primary passages are ordinary under the original packing policy, which
// keeps every accepted passage whose bytes fit, and an explicit duplicate omission under exact-v1.
// Recording an association stage must preserve both policies' primary selection and stay readable.
func TestAssociatedLexicalRepeatedPrimaryPackingPolicies(t *testing.T) {
	ctx := context.Background()
	for _, policy := range []string{mousa.PackingOriginal, mousa.PackingExactV1} {
		t.Run(policy, func(t *testing.T) {
			store := openLexicalStore(t)
			defer store.Close()
			sourceID := seedRepeatedPrimaryItem(t, store)
			plain := associateQuery(t, store, sourceID, "repeated-plain", "restart", 1<<20, policy, nil)
			selected, duplicates := 0, 0
			for _, candidate := range plain.Trail.Candidates {
				if candidate.Selected {
					selected++
				}
				if candidate.Omission == "duplicate" {
					duplicates++
				}
			}
			wantSelected, wantDuplicates := 2, 0
			if policy == mousa.PackingExactV1 {
				wantSelected, wantDuplicates = 1, 1
			}
			if selected != wantSelected {
				t.Fatalf("%s selected %d repeated primary passages, want %d: %#v", policy, selected, wantSelected, plain.Trail.Candidates)
			}
			if duplicates != wantDuplicates {
				t.Fatalf("%s recorded %d duplicate omissions, want %d", policy, duplicates, wantDuplicates)
			}
			traced := associateQuery(t, store, sourceID, "repeated-associated", "restart", 1<<20, policy, []mousa.AssociationDeclaration{drainDeclaration()})
			if traced.Trail.Schema != mousa.SourceTrailSchemaV3 || len(traced.Trail.Associated) == 0 {
				t.Fatalf("associated %s trail = %#v", policy, traced.Trail)
			}
			for index, candidate := range plain.Trail.Candidates {
				if candidate.Selected != traced.Trail.Candidates[index].Selected || candidate.Omission != traced.Trail.Candidates[index].Omission || candidate.DuplicateOf != traced.Trail.Candidates[index].DuplicateOf {
					t.Fatalf("association changed the %s primary selection: %#v", policy, traced.Trail.Candidates)
				}
			}
			stored, err := store.GetSourceTrail(ctx, traced.Trail.ID)
			if err != nil {
				t.Fatalf("associated %s readback: %v", policy, err)
			}
			want, err := mousa.EncodeSourceTrail(traced.Trail)
			if err != nil {
				t.Fatal(err)
			}
			got, err := mousa.EncodeSourceTrail(stored)
			if err != nil || string(want) != string(got) {
				t.Fatalf("associated %s readback disagrees with the written trail: %v", policy, err)
			}
		})
	}
}

// trailSegmentID and trailRepresentationID name the evidence behind the first primary or
// associated passage of one traced result.
func trailSegmentID(result TracedLexicalResult, associated bool) mousa.SegmentID {
	if associated {
		return result.Trail.Associated[0].SegmentID
	}
	return result.Trail.Candidates[0].SegmentID
}

func trailRepresentationID(result TracedLexicalResult, associated bool) mousa.RepresentationID {
	if associated {
		return result.Associated[0].Segment.RepresentationID
	}
	return result.Candidates[0].Segment.RepresentationID
}

// Historical readback compares associated rows with canonical segments, representation records and
// retained bytes, even when the trail's own digests and sizes are internally consistent.
func TestAssociatedTrailRechecksCanonicalContent(t *testing.T) {
	ctx := context.Background()
	damage := []struct {
		kind           string
		statement      string
		representation bool
	}{
		{"record", `UPDATE segments SET record_json = x'7b7d' WHERE id = ?`, false},
		{"text", `UPDATE segment_lexical_fts SET text = 'changed bytes' WHERE rowid = (SELECT rowid FROM segment_lexical_rows WHERE segment_id = ?)`, false},
		{"coordinates", `UPDATE segments SET selector_end = x'ffffffffffffffff' WHERE id = ?`, false},
		{"representation", `UPDATE representations SET record_json = x'7b7d' WHERE id = ?`, true},
		{"ancestry", `UPDATE representation_inputs SET ordinal = ordinal + 10 WHERE representation_id = ?`, true},
	}
	for _, origin := range []string{"primary", "associated"} {
		for _, entry := range damage {
			t.Run(origin+"-"+entry.kind, func(t *testing.T) {
				store := openLexicalStore(t)
				defer store.Close()
				sourceID, _, _ := seedAssociatedItems(t, store)
				traced := associateQuery(t, store, sourceID, "canonical-"+origin+"-"+entry.kind, "restart", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{drainDeclaration()})
				if len(traced.Trail.Associated) == 0 || !traced.Trail.Associated[0].Selected {
					t.Fatalf("fixture released no associated passage: %#v", traced.Trail)
				}
				associated := origin == "associated"
				segmentID := trailSegmentID(traced, associated)
				identifier := segmentID[:]
				if entry.representation {
					representationID := trailRepresentationID(traced, associated)
					identifier = representationID[:]
				}
				if _, err := store.db.ExecContext(ctx, entry.statement, identifier); err != nil {
					t.Fatal(err)
				}
				if _, err := store.GetSourceTrail(ctx, traced.Trail.ID); !IsCode(err, CodeIntegrity) {
					t.Fatalf("damaged %s canonical content accepted: %v", origin, err)
				}
			})
		}
	}
}

// seedLargeTargetItem seeds the procedure item plus a declared target carrying more passages than
// one query considers.
func seedLargeTargetItem(t *testing.T, store *Store, passages int) mousa.SourceID {
	t.Helper()
	ctx := context.Background()
	seedEvaluationFixture(t, store)
	sequence := uint64(1)
	batch := testPushBatch(t, "operations", &sequence)
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatalf("ApplyIngest: %v", err)
	}
	seedLocalItem(t, store, batch.Source, "rollback.md", "Rolling restart\n\nPerform the rolling restart one server at a time.\n")
	seedLocalItem(t, store, batch.Source, "drain.md", strings.Repeat("# a\n\n", passages))
	return batch.Source.ID
}

// A declared target with more passages than one query considers is read up to the bound in document
// order, and the passages that were not read are a visible omission instead of an unbounded read.
func TestAssociatedLexicalTargetConsiderationIsBounded(t *testing.T) {
	store := openLexicalStore(t)
	defer store.Close()
	sourceID := seedLargeTargetItem(t, store, mousa.MaxAssociationPassages+2)
	traced := associateQuery(t, store, sourceID, "consideration-bound", "restart", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{drainDeclaration()})
	if len(traced.Trail.Associated) != mousa.MaxAssociationPassages {
		t.Fatalf("considered %d associated passages, want %d", len(traced.Trail.Associated), mousa.MaxAssociationPassages)
	}
	if len(traced.Trail.AssociationOmissions) != 1 || traced.Trail.AssociationOmissions[0].Reason != mousa.AssociationTargetTruncated {
		t.Fatalf("truncation omission = %#v", traced.Trail.AssociationOmissions)
	}
	if err := traced.Trail.Validate(); err != nil {
		t.Fatalf("bounded trail invalid: %v", err)
	}
}

// The consideration bound has to hold for the largest declaration the schema accepts. Every field
// can hold escapable bytes that grow sixfold in JSON, so the recorded trail of a maximally declared
// large target still has to stay well inside the canonical record limit.
func TestAssociatedLexicalMaximalDeclarationStaysWithinRecordLimit(t *testing.T) {
	store := openLexicalStore(t)
	defer store.Close()
	ctx := context.Background()
	seedEvaluationFixture(t, store)
	sequence := uint64(1)
	batch := testPushBatch(t, "operations", &sequence)
	if err := store.ApplyIngest(ctx, batch); err != nil {
		t.Fatalf("ApplyIngest: %v", err)
	}
	fromItem := strings.Repeat("\x01", mousa.MaxAssociationItemBytes)
	toItem := strings.Repeat("\x02", mousa.MaxAssociationItemBytes)
	seedLocalItem(t, store, batch.Source, fromItem, "# Restart\n\nRestart one server at a time.\n\n")
	seedLocalItem(t, store, batch.Source, toItem, strings.Repeat("# a\n\n", mousa.MaxAssociationPassages+2))
	declaration := mousa.AssociationDeclaration{
		FromItem: fromItem, ToItem: toItem,
		Basis:  strings.Repeat("\x03", mousa.MaxAssociationBasisBytes),
		Author: strings.Repeat("\x04", mousa.MaxAssociationAuthorBytes),
	}
	traced := associateQuery(t, store, batch.Source.ID, "maximal-declaration", "restart", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{declaration})
	if len(traced.Trail.Associated) != mousa.MaxAssociationPassages {
		t.Fatalf("considered %d associated passages, want %d", len(traced.Trail.Associated), mousa.MaxAssociationPassages)
	}
	record, err := mousa.EncodeSourceTrail(traced.Trail)
	if err != nil {
		t.Fatal(err)
	}
	if len(record) > maxRecordBytes/4 {
		t.Fatalf("association stage recorded %d bytes, over a quarter of the %d-byte record limit", len(record), maxRecordBytes)
	}
}

// Membership and coordinates are checked for every released passage, not once per representation: a
// later passage of an already verified representation still has to fit inside it.
func TestTrailRepresentationCheckCoversLaterSegments(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	sourceID, _, _ := seedAssociatedItems(t, store)
	traced := associateQuery(t, store, sourceID, "later-segment", "restart", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{drainDeclaration()})
	if len(traced.Trail.Associated) == 0 || !traced.Trail.Associated[0].Selected {
		t.Fatalf("fixture released no associated passage: %#v", traced.Trail)
	}
	first := traced.Associated[0].Segment
	representation, err := store.GetRepresentation(ctx, first.RepresentationID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	representations := make(map[mousa.RepresentationID]mousa.Representation)
	paths := make(map[mousa.RepresentationID][]mousa.EvidencePath)
	if err := verifyTrailRepresentation(ctx, tx, first, sourceID, representations, paths); err != nil {
		t.Fatalf("released passage rejected: %v", err)
	}
	selector := mousa.NewTextByteRangeSelector(0, representation.ByteLength+1)
	id, err := mousa.NewSegmentID(first.RepresentationID, selector, first.ContentSHA256)
	if err != nil {
		t.Fatal(err)
	}
	beyond := first
	beyond.ID = id
	beyond.Selector = selector
	if err := verifyTrailRepresentation(ctx, tx, beyond, sourceID, representations, paths); !IsCode(err, CodeIntegrity) {
		t.Fatalf("passage beyond its representation accepted after the representation was verified: %v", err)
	}
}

// An association row is a claim about canonical ancestry, not just about a tuple: the segment, its
// representation membership, its recorded coordinates and the declared relationship must still
// agree with the store and with the primary evidence the declaration was recorded against.
func TestAssociatedTrailVerificationRechecksCanonicalRelationships(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	sourceID, _, _ := seedAssociatedItems(t, store)
	traced := associateQuery(t, store, sourceID, "association-verification", "restart", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{drainDeclaration()})
	if len(traced.Trail.Associated) == 0 {
		t.Fatalf("fixture released no associated passage: %#v", traced.Trail)
	}
	foreign := addVerifiedLexicalDocument(t, store, "other-source", "sharedterm other")
	tx, err := store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := verifyTrailContent(ctx, tx, traced.Trail, sourceID); err != nil {
		t.Fatalf("undamaged associated trail verification: %v", err)
	}
	for _, kind := range []string{"digest", "size", "source", "declaring_item", "target_item", "duplicate_reference"} {
		t.Run(kind, func(t *testing.T) {
			trail := traced.Trail
			trail.Associated = append([]mousa.TrailAssociated(nil), trail.Associated...)
			row := &trail.Associated[0]
			switch kind {
			case "digest":
				row.ContentSHA256 = mousa.SHA256{}
			case "size":
				row.TextBytes++
			case "source":
				span, ok := foreign.segments[0].Selector.TextByteRange()
				if !ok {
					t.Fatal("foreign fixture segment has no byte range")
				}
				row.SegmentID = foreign.segments[0].ID
				row.ContentSHA256 = foreign.segments[0].ContentSHA256
				row.TextBytes = span.End - span.Start
			case "declaring_item":
				row.FromItem = "unrelated.md"
			case "target_item":
				row.ToItem = "unrelated.md"
			case "duplicate_reference":
				row.Selected = false
				row.Omission = "duplicate"
				row.DuplicateOf = foreign.segments[0].ID.String()
			}
			if err := verifyTrailContent(ctx, tx, trail, sourceID); !IsCode(err, CodeIntegrity) {
				t.Fatalf("verification accepted changed %s: %v", kind, err)
			}
		})
	}
}

// Historical readback follows the immutable records a trail recorded, never the current item
// pointer: a trail that released a declared target stays readable after that item is re-synced with
// new content, and after it is deactivated. The same verification still finds damage behind the
// originally released segment afterwards.
func TestAssociatedTrailReadableAfterTargetRevision(t *testing.T) {
	ctx := context.Background()
	store := openLexicalStore(t)
	defer store.Close()
	sourceID, _, _ := seedAssociatedItems(t, store)
	traced := associateQuery(t, store, sourceID, "target-revision", "restart", 1<<20, mousa.PackingOriginal, []mousa.AssociationDeclaration{drainDeclaration()})
	if len(traced.Trail.Associated) == 0 || !traced.Trail.Associated[0].Selected {
		t.Fatalf("fixture released no associated passage: %#v", traced.Trail)
	}
	released := traced.Trail.Associated[0]

	// Re-sync the declared target with different content: the recorded revision is replaced.
	source, err := store.GetSource(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	seedLocalItem(t, store, source, "drain.md", "Shutdown caveat\n\nStopping a server now drains connections.\n")
	stored, err := store.GetSourceTrail(ctx, traced.Trail.ID)
	if err != nil {
		t.Fatalf("readback after target revision: %v", err)
	}
	want, err := mousa.EncodeSourceTrail(traced.Trail)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mousa.EncodeSourceTrail(stored)
	if err != nil || string(want) != string(got) {
		t.Fatalf("readback after target revision changed the trail: %v", err)
	}

	// Deactivating the target leaves the recorded revision readable and verifiable.
	if _, err := store.DeleteLocalItem(ctx, sourceID, "drain.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSourceTrail(ctx, traced.Trail.ID); err != nil {
		t.Fatalf("readback after target deactivation: %v", err)
	}

	// The damaged originally released segment is still rejected.
	if _, err := store.db.ExecContext(ctx, `UPDATE segments SET record_json = x'7b7d' WHERE id = ?`, released.SegmentID[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSourceTrail(ctx, traced.Trail.ID); !IsCode(err, CodeIntegrity) {
		t.Fatalf("damaged recorded revision accepted after the target moved on: %v", err)
	}
}

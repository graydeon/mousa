package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

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
	addLocalItem := func(item, text string) {
		t.Helper()
		digest := testDigest(text)
		external := fmt.Sprintf("item/%s@%x", item, digest)
		observationID, err := mousa.NewObservationID(batch.Source.ID, external)
		if err != nil {
			t.Fatal(err)
		}
		observation := mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: batch.Source.ID, ExternalObservationID: external}
		artifactID, err := mousa.NewArtifactID(observationID, "body")
		if err != nil {
			t.Fatal(err)
		}
		artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observationID, ArtifactKey: "body",
			MediaType: mousa.UTF8TextMediaType, ContentSHA256: digest, ByteLength: uint64(len(text))}
		batch := mousa.IngestBatch{AdapterID: "test", AdapterVersion: "1", Initiative: mousa.InitiativePush, Form: mousa.FormItem,
			CapturedAtUsec: 2, Source: batch.Source, Observation: observation, Artifacts: []mousa.Artifact{artifact}}
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
		if _, err := store.ActivateLocalItem(ctx, batch.Source.ID, item, representation.ID, normalized); err != nil {
			t.Fatal(err)
		}
	}
	addLocalItem("rollback.md", "Rolling restart\n\nPerform the rolling restart one server at a time.\n\nVerify health after every server before continuing.\n")
	addLocalItem("drain.md", "Shutdown caveat\n\nStopping a server does not drain connections.\n\nDrain client connections before each server stops.\n")
	addLocalItem("unrelated.md", "Gardening notes\n\nCompost requires air and moisture.\n")
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

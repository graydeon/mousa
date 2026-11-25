package sqlite

import (
	"context"
	"fmt"
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
	if _, err := store.ActivateLocalItem(ctx, source.ID, item, representation.ID, normalized, nil, ""); err != nil {
		t.Fatal(err)
	}
}

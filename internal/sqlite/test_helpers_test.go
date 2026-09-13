package sqlite

import (
	"crypto/sha256"
	"testing"

	"github.com/graydeon/mousa/internal/mousa"
)

func testSource(t testing.TB) mousa.Source {
	t.Helper()
	id, err := mousa.NewSourceID("test", "source-1")
	if err != nil {
		t.Fatalf("NewSourceID: %v", err)
	}
	return mousa.Source{
		Schema:           mousa.SourceSchema,
		ID:               id,
		Namespace:        "test",
		ExternalSourceID: "source-1",
	}
}

func testDigest(value string) mousa.SHA256 {
	return mousa.SHA256(sha256.Sum256([]byte(value)))
}

func testRecordGraph(t *testing.T) (mousa.Source, mousa.Observation, mousa.Artifact, mousa.Representation, mousa.Representation, mousa.Segment) {
	t.Helper()
	source := testSource(t)
	observationID, err := mousa.NewObservationID(source.ID, "observation-1")
	if err != nil {
		t.Fatalf("NewObservationID: %v", err)
	}
	observation := mousa.Observation{Schema: mousa.ObservationSchema, ID: observationID, SourceID: source.ID, ExternalObservationID: "observation-1"}
	artifactID, err := mousa.NewArtifactID(observation.ID, "body")
	if err != nil {
		t.Fatalf("NewArtifactID: %v", err)
	}
	artifact := mousa.Artifact{Schema: mousa.ArtifactSchema, ID: artifactID, ObservationID: observation.ID, ArtifactKey: "body", MediaType: mousa.UTF8TextMediaType, ContentSHA256: testDigest("artifact"), ByteLength: 8}
	baseInputs := []mousa.DerivationInput{mousa.NewArtifactDerivationInput(artifact.ID)}
	baseID, err := mousa.NewRepresentationID(baseInputs, "test.processor", "1", testDigest("params-1"), mousa.UTF8TextMediaType, testDigest("base"))
	if err != nil {
		t.Fatalf("NewRepresentationID(base): %v", err)
	}
	base := mousa.Representation{Schema: mousa.RepresentationSchema, ID: baseID, Inputs: baseInputs, ProcessorID: "test.processor", ProcessorVersion: "1", ParametersSHA256: testDigest("params-1"), MediaType: mousa.UTF8TextMediaType, ContentSHA256: testDigest("base"), ByteLength: 8}
	mixedInputs := []mousa.DerivationInput{mousa.NewRepresentationDerivationInput(base.ID), mousa.NewArtifactDerivationInput(artifact.ID)}
	mixedID, err := mousa.NewRepresentationID(mixedInputs, "test.processor", "2", testDigest("params-2"), mousa.UTF8TextMediaType, testDigest("mixed"))
	if err != nil {
		t.Fatalf("NewRepresentationID(mixed): %v", err)
	}
	mixed := mousa.Representation{Schema: mousa.RepresentationSchema, ID: mixedID, Inputs: mixedInputs, ProcessorID: "test.processor", ProcessorVersion: "2", ParametersSHA256: testDigest("params-2"), MediaType: mousa.UTF8TextMediaType, ContentSHA256: testDigest("mixed"), ByteLength: 8}
	selector := mousa.NewTextByteRangeSelector(1, 8)
	segmentID, err := mousa.NewSegmentID(mixed.ID, selector, testDigest("segment"))
	if err != nil {
		t.Fatalf("NewSegmentID: %v", err)
	}
	segment := mousa.Segment{Schema: mousa.SegmentSchema, ID: segmentID, RepresentationID: mixed.ID, Selector: selector, ContentSHA256: testDigest("segment")}
	return source, observation, artifact, base, mixed, segment
}

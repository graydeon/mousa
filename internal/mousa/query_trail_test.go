package mousa

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTrailCanonicalVectors(t *testing.T) {
	one, two, three, four := trailVectors()
	for i, tt := range []struct {
		record                           Trail
		bytesSHA, id, resultSHA, leafSHA string
	}{
		{one, "19d59f468e4fd1d1dd22ebaf08c5b7f8afde23f15ba58c3fd36d8fc8012adf11", "9edb135e2a6c7361ff0eaa16b710b675056b41b3052c0e7fa4f2bdb693b8c2f8", "5079564af0dea07a5e8471f0fa8c47cc25171f8541464e2f4fc9afa027069a34", "d5b9eda7ef63b54bb3f83ae51ac90c465d625d79e35c6c54650c6d00c035c240"},
		{two, "f16dca4450a3312befa6767bded34799187f016764c6cc7c5a3816dc12422816", "5bc8f5ee9b36707a0138714089a55d844616e6750c18d34452968545b71ebfc5", "fd1abb380d07b5ba3d41aac8899df64733023f462906e075a080128fef259f0c", "364274dbc7958b2677283ed317f03c6edce1c8449da567f2092d457ce59afe96"},
		{three, "9a24c0f03e60c1d020655ea3c495ca35e3ac8ccbafade075df01389a04f28628", "48afca41afe7410159ed98e8a675d83993cd08bfb2f60bd18e451f5825f6e818", "d40c5b366a8e3ba88685fb63f444fb6e71785b1967940074d058ff491fb00bfd", "f6027e62ff9e3dc62510d6500189cd3a1dd3002655bb73a4cc57e04b8d53a9c7"},
		{four, "c722797f422cedf89beffc9c7f43570e00a8cc7f59c67abe6de01f3e2bfa328f", "fdf247ada2cfa980dfb74ebcf70da8f0d0b6090765d7c13ac085d1b915dd2fe6", "fd1abb380d07b5ba3d41aac8899df64733023f462906e075a080128fef259f0c", "312995299615c8bc4fa93b73722551f5c802df409e13ca05cae668c2523fab28"},
	} {
		t.Run(fmt.Sprint(i+1), func(t *testing.T) {
			trail, err := NewTrail(tt.record)
			if err != nil {
				t.Fatal(err)
			}
			got, err := EncodeTrail(trail)
			if err != nil {
				t.Fatal(err)
			}
			if sha := fmt.Sprintf("%x", sha256.Sum256(got)); sha != tt.bytesSHA {
				t.Errorf("canonical bytes hash = %s, want %s\n%s", sha, tt.bytesSHA, got)
			}
			if trail.ID.String() != tt.id || trail.ResultSHA256.String() != tt.resultSHA {
				t.Errorf("ID = %s, result = %s; want %s and %s", trail.ID, trail.ResultSHA256, tt.id, tt.resultSHA)
			}
			if leaf := fmt.Sprintf("%x", LogLeafHash(got)); leaf != tt.leafSHA {
				t.Errorf("leaf hash = %s, want %s", leaf, tt.leafSHA)
			}
			decoded, err := DecodeTrail(got)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, trail) {
				t.Errorf("decoded %+v, want %+v", decoded, trail)
			}
		})
	}
}

func TestDecodeTrailRefusesNonCanonicalRecords(t *testing.T) {
	one, _, _, _ := trailVectors()
	one, err := NewTrail(one)
	if err != nil {
		t.Fatal(err)
	}
	record, err := EncodeTrail(one)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, old, replacement string }{
		{"unknown", `"as_of":3`, `"as_of":3,"future":false`},
		{"principal", `"as_of":3`, `"as_of":3,"principal":null`},
		{"overlays", `"as_of":3`, `"as_of":3,"overlays":[]`},
		{"duplicate key", `"as_of":3`, `"as_of":3,"as_of":3`},
		{"float", `"as_of":3`, `"as_of":3.0`},
		{"line separator", `"cli"`, `"cli\u2028"`},
		{"paragraph separator", `"cli"`, `"cli\u2029"`},
		{"whitespace", `"as_of":3`, `"as_of": 3`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			edited := bytes.Replace(record, []byte(tt.old), []byte(tt.replacement), 1)
			if bytes.Equal(edited, record) {
				t.Fatal("fixture not edited")
			}
			if _, err := DecodeTrail(edited); err == nil {
				t.Fatal("accepted noncanonical trail")
			}
		})
	}
}

func TestNewTrailRefusesCandidateFromDeniedSource(t *testing.T) {
	_, two, three, _ := trailVectors()
	deny := three.Sources[1]
	two.Request.Scope = three.Request.Scope
	two.Sources = append(two.Sources, deny)
	two.Candidates[0].SourceIDs = []SourceID{deny.SourceID}
	if _, err := NewTrail(two); err == nil || !strings.Contains(err.Error(), "allowed") {
		t.Fatalf("want a rejection for denied ancestry, got %v", err)
	}
}

func TestTrailReplaysFirstFit(t *testing.T) {
	_, two, _, _ := trailVectors()
	original := two.Candidates[0]
	duplicate := Candidate{
		Rank: 3, Range: ByteRange{Start: 1600, End: 2300},
		SegmentID: SegmentID(vectorDigest("53")), ContentSHA256: original.ContentSHA256,
		RepresentationID: original.RepresentationID, SourceIDs: original.SourceIDs,
		Scores: []TrailScore{{Branch: 0, Level: "block", Rank: 3, ValueBits: "bff0000000000000"}},
	}
	fill := duplicate
	fill.SegmentID = SegmentID(vectorDigest("54"))
	fill.ContentSHA256 = vectorDigest("c4")
	fill.Range = ByteRange{Start: 2300, End: 2600}
	fill.Scores = []TrailScore{{Branch: 0, Level: "block", Rank: 4, ValueBits: "bfe0000000000000"}}
	fill.Rank = 4
	two.Candidates = append(two.Candidates, duplicate, fill)
	two.Retrieval.Branches[0].Proposed = 4
	*two.Packing = ApplyFirstFit(two.Candidates, two.Request.Budget.Limit, nil)
	if c := two.Candidates[0]; c.Packed != "full" || c.Cost != 700 {
		t.Errorf("first candidate not packed: %+v", c)
	}
	if c := two.Candidates[1]; c.Packed != "none" || c.Omitted != "budget" || c.Cost != 0 {
		t.Errorf("oversize candidate not omitted: %+v", c)
	}
	if c := two.Candidates[2]; c.Packed != "none" || c.Omitted != "duplicate" || c.DuplicateOf != original.SegmentID || c.Cost != 0 {
		t.Errorf("duplicate candidate not omitted: %+v", c)
	}
	if c := two.Candidates[3]; c.Packed != "full" || c.Cost != 300 {
		t.Errorf("smaller candidate after omissions not packed: %+v", c)
	}
	if two.Packing.Used != 1000 {
		t.Errorf("used = %d, want 1000", two.Packing.Used)
	}
	if _, err := NewTrail(two); err != nil {
		t.Fatalf("invalid packing output: %v", err)
	}
	two.Candidates[2].DuplicateOf = two.Candidates[1].SegmentID
	if _, err := NewTrail(two); err == nil {
		t.Fatal("accepted a changed duplicate reference")
	}
}

func TestTrailSupersessionEffectValidation(t *testing.T) {
	one, two, _, _ := trailVectors()
	// Sixty-five sorted unique replacements stay valid on an eligible
	// candidate; the bound on outgoing edges does not gate inbound trails.
	replacements := make([]SHA256, 0, 65)
	for i := 1; i <= 65; i++ {
		replacements = append(replacements, vectorDigest(fmt.Sprintf("%02x", i)))
	}
	two.Request.IncludeSuperseded = true
	two.Supersession = []SupersessionEffect{{
		NoteID: vectorDigest("77"), SegmentID: two.Candidates[0].SegmentID, SupersededBy: replacements,
	}}
	if _, err := NewTrail(two); err != nil {
		t.Fatalf("valid effect with 65 replacements rejected: %v", err)
	}
	// An all-denied trail cannot carry a supersession effect at all.
	one.Supersession = []SupersessionEffect{{
		NoteID: vectorDigest("77"), SegmentID: SegmentID(vectorDigest("51")), SupersededBy: []SHA256{vectorDigest("78")},
	}}
	if _, err := NewTrail(one); err == nil {
		t.Fatal("all-denied trail accepted a supersession effect")
	}
}

func vectorDigest(hexByte string) SHA256 {
	value, err := ParseSHA256(strings.Repeat(hexByte, 32))
	if err != nil {
		panic(err)
	}
	return value
}

func trailVectors() (Trail, Trail, Trail, Trail) {
	a, b := SourceID(vectorDigest("11")), SourceID(vectorDigest("22"))
	one := Trail{
		AsOf: 3, StoreID: "000102030405060708090a0b0c0d0e0f",
		Request: TrailRequest{
			Budget: Budget{Limit: 1024, Unit: "bytes"}, Caller: "cli",
			Limit: 100, Mode: "lexical", RequestID: "req-1",
			RequestedAtUsec: 1763596800000000,
			Scope:           TrailScope{Kind: "source", Sources: []SourceID{a}},
			Texts:           []string{"caddy reverse proxy"},
		},
		Sources: []SourceDecision{{Access: "default", Outcome: "deny", Reasons: []string{"access_default"}, SourceID: a}},
	}
	two := one
	two.AsOf = 9
	allowID := AccessEventID(vectorDigest("ee"))
	two.Sources = []SourceDecision{{
		Access: "allow", AccessEventID: &allowID, Head: &SourceHead{LeafSHA256: vectorDigest("bb"), Seq: 8},
		Outcome: "allow", Reasons: []string{"allow"}, SourceID: a,
	}}
	two.Retrieval = &Retrieval{Branches: []Branch{{
		Kind: "lexical", Proposed: 2, Scoring: "fts5-bm25", TermPolicy: "original", Terms: []string{"caddy", "reverse", "proxy"},
	}}}
	two.Candidates = []Candidate{
		{
			ContentSHA256: vectorDigest("c1"), Cost: 700, Packed: "full",
			Range: ByteRange{End: 700}, Rank: 1, RepresentationID: RepresentationID(vectorDigest("d1")),
			Scores:    []TrailScore{{Branch: 0, Level: "block", Rank: 1, ValueBits: "c00a000000000000"}},
			SegmentID: SegmentID(vectorDigest("51")), SourceIDs: []SourceID{a},
		},
		{
			ContentSHA256: vectorDigest("c2"), Omitted: "budget", Packed: "none",
			Range: ByteRange{Start: 700, End: 1600}, Rank: 2, RepresentationID: RepresentationID(vectorDigest("d1")),
			Scores:    []TrailScore{{Branch: 0, Level: "block", Rank: 2, ValueBits: "bff8000000000000"}},
			SegmentID: SegmentID(vectorDigest("52")), SourceIDs: []SourceID{a},
		},
	}
	two.Packing = &Packing{Policy: "first-fit-v1", Used: 700}
	three := one
	three.Request.Purpose = "audit"
	three.Request.Scope = TrailScope{Kind: "list", Sources: []SourceID{a, b}}
	three.Request.Texts = []string{"say \"hi\"\\ok\ttab\x1f", "caf\u00e9 \U0001f980 <&>"}
	denyID := AccessEventID(vectorDigest("ef"))
	three.Sources = append(three.Sources, SourceDecision{Access: "deny", AccessEventID: &denyID, Outcome: "deny", Reasons: []string{"access_denied"}, SourceID: b})
	four := two
	four.AsOf = 10
	four.Request.RequestID = "req-2"
	four.Request.RequestedAtUsec++
	return one, two, three, four
}

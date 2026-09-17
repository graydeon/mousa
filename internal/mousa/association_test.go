package mousa

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func sha256Sum(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	return sum[:]
}

// associatedPassageFixture builds one internally consistent associated passage.
func associatedPassageFixture(t *testing.T) AssociatedPassage {
	t.Helper()
	text := "The qualification note."
	digest := SHA256(sha256.Sum256([]byte(text)))
	segmentID, err := NewSegmentID(RepresentationID(sha256.Sum256([]byte("rep"))), NewTextByteRangeSelector(0, uint64(len(text))), digest)
	if err != nil {
		t.Fatal(err)
	}
	return AssociatedPassage{
		Segment: Segment{Schema: SegmentSchema, ID: segmentID, RepresentationID: RepresentationID(sha256.Sum256([]byte("rep"))), Selector: NewTextByteRangeSelector(0, uint64(len(text))), ContentSHA256: digest},
		Item:    "caveat.md", Text: text,
		Declaration: AssociationDeclaration{FromItem: "proc.md", ToItem: "caveat.md", Basis: "required qualification", Author: "maintainer"},
	}
}

func TestDecodeAssociationDeclarations(t *testing.T) {
	valid := `{"schema":"mousa.association_declarations.v1","associations":[
		{"from_item":"proc.md","to_item":"caveat.md","basis":"the procedure requires the caveat","author":"maintainer"}]}`
	declarations, err := DecodeAssociationDeclarations([]byte(valid))
	if err != nil {
		t.Fatalf("valid declarations rejected: %v", err)
	}
	if len(declarations) != 1 || declarations[0].FromItem != "proc.md" || declarations[0].ToItem != "caveat.md" {
		t.Fatalf("decoded = %#v", declarations)
	}
	cases := map[string]string{
		"wrong schema":   `{"schema":"mousa.association_declarations.v2","associations":[{"from_item":"a","to_item":"b","basis":"c","author":"d"}]}`,
		"empty":          `{"schema":"mousa.association_declarations.v1","associations":[]}`,
		"self reference": `{"schema":"mousa.association_declarations.v1","associations":[{"from_item":"a","to_item":"a","basis":"c","author":"d"}]}`,
		"empty basis":    `{"schema":"mousa.association_declarations.v1","associations":[{"from_item":"a","to_item":"b","basis":"","author":"d"}]}`,
		"oversized item": `{"schema":"mousa.association_declarations.v1","associations":[{"from_item":"` + strings.Repeat("x", 2000) + `","to_item":"b","basis":"c","author":"d"}]}`,
		"unknown field":  `{"schema":"mousa.association_declarations.v1","extra":true,"associations":[{"from_item":"a","to_item":"b","basis":"c","author":"d"}]}`,
		"duplicate":      `{"schema":"mousa.association_declarations.v1","associations":[{"from_item":"a","to_item":"b","basis":"c","author":"d"},{"from_item":"a","to_item":"b","basis":"c","author":"d"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeAssociationDeclarations([]byte(raw)); err == nil {
				t.Fatalf("%s accepted", name)
			}
		})
	}
}

func TestDeclarationLimitIsBounded(t *testing.T) {
	var builder strings.Builder
	builder.WriteString(`{"schema":"mousa.association_declarations.v1","associations":[`)
	for index := 0; index < MaxAssociationDeclarations+1; index++ {
		if index > 0 {
			builder.WriteString(",")
		}
		builder.WriteString(`{"from_item":"a` + itoa(index) + `","to_item":"b","basis":"c","author":"d"}`)
	}
	builder.WriteString(`]}`)
	if _, err := DecodeAssociationDeclarations([]byte(builder.String())); err == nil {
		t.Fatal("declaration cap not enforced")
	}
}

func TestAppendAssociatedPassagesChangesIdentity(t *testing.T) {
	request := testTrailRequest(t)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	candidates := trailCandidates(t)
	trail, err := NewSourceTrail(request, decision, "alpha", candidates, 1<<20, PackingOriginal)
	if err != nil {
		t.Fatal(err)
	}
	if trail.Schema != SourceTrailSchema {
		t.Fatalf("original packing produced %s", trail.Schema)
	}
	passage := associatedPassageFixture(t)
	appended, err := AppendAssociatedPassages(trail, candidates, []AssociatedPassage{passage}, nil)
	if err != nil {
		t.Fatalf("AppendAssociatedPassages: %v", err)
	}
	if appended.Schema != SourceTrailSchemaV3 {
		t.Fatalf("schema = %s", appended.Schema)
	}
	if appended.PacketID == trail.PacketID || appended.ID.String() == trail.ID.String() {
		t.Fatal("identity did not change with the association stage")
	}
	if len(appended.Associated) != 1 || !appended.Associated[0].Selected || appended.Associated[0].TextBytes != uint64(len(passage.Text)) {
		t.Fatalf("associated row = %#v", appended.Associated)
	}
	if appended.UsedBytes != trail.UsedBytes+uint64(len(passage.Text)) {
		t.Fatalf("used bytes = %d, want %d", appended.UsedBytes, trail.UsedBytes+uint64(len(passage.Text)))
	}
	if err := appended.Validate(); err != nil {
		t.Fatalf("appended trail invalid: %v", err)
	}
	// Canonical round trip.
	encoded, err := EncodeSourceTrail(appended)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSourceTrail(encoded)
	if err != nil {
		t.Fatalf("v3 decode: %v", err)
	}
	reencoded, err := EncodeSourceTrail(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(reencoded) {
		t.Fatal("v3 round trip changed canonical bytes")
	}
}

func TestAppendAssociatedPassagesHonorsBudgetAndDuplicates(t *testing.T) {
	request := testTrailRequest(t)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	candidates := trailCandidates(t)
	// A v1 trail with no budget headroom omits the associated passage with a reason.
	tight, err := NewSourceTrail(request, decision, "alpha", candidates, 40, PackingOriginal)
	if err != nil {
		t.Fatal(err)
	}
	passage := associatedPassageFixture(t)
	appended, err := AppendAssociatedPassages(tight, candidates, []AssociatedPassage{passage}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(appended.Associated) != 1 || appended.Associated[0].Selected || appended.Associated[0].Omission != "budget" {
		t.Fatalf("budget row = %#v", appended.Associated)
	}
	if err := appended.Validate(); err != nil {
		t.Fatalf("omission trail invalid: %v", err)
	}
}

func TestTrailV3RejectsInvalidAssociationRows(t *testing.T) {
	request := testTrailRequest(t)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	candidates := trailCandidates(t)
	base, err := NewSourceTrail(request, decision, "alpha", candidates, 1<<20, PackingOriginal)
	if err != nil {
		t.Fatal(err)
	}
	passage := associatedPassageFixture(t)
	valid, err := AppendAssociatedPassages(base, candidates, []AssociatedPassage{passage}, []AssociationOmission{{FromItem: "proc.md", ToItem: "gone.md", Reason: AssociationTargetInactive}})
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*SourceTrail){
		"unknown omission reason": func(x *SourceTrail) { x.AssociationOmissions[0].Reason = "vibes" },
		"omitted without reason":  func(x *SourceTrail) { x.AssociationOmissions[0].Reason = "" },
		"selected with omission":  func(x *SourceTrail) { x.Associated[0].Omission = "budget" },
		"unselected without omission": func(x *SourceTrail) {
			x.Associated[0].Selected = false
		},
		"basis overflow":       func(x *SourceTrail) { x.Associated[0].Basis = strings.Repeat("x", MaxAssociationBasisBytes+1) },
		"deny with associated": func(x *SourceTrail) { x.Outcome = string(PolicyOutcomeDeny); x.Candidates = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := valid
			mutate(&changed)
			changed.ID, _ = NewSourceTrailID(changed)
			if changed.Validate() == nil {
				t.Fatal("invalid association trail accepted")
			}
			raw, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSourceTrail(raw); err == nil {
				t.Fatal("invalid encoded association trail accepted")
			}
		})
	}
}

func TestV1V2TrailsRejectAssociationFields(t *testing.T) {
	golden, err := os.ReadFile("testdata/trail-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(golden, &value); err != nil {
		t.Fatal(err)
	}
	value["associated"] = []any{map[string]any{"segment_id": strings.Repeat("a", 64), "content_sha256": strings.Repeat("b", 64), "text_bytes": 1, "selected": true, "from_item": "a", "to_item": "b", "basis": "c", "author": "d"}}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSourceTrail(raw); err == nil {
		t.Fatal("v1 trail accepted an association field")
	}
}

func TestAppendWithoutContentReturnsUnchangedTrail(t *testing.T) {
	request := testTrailRequest(t)
	decision := trailDecision(t, request, PolicyOutcomeAllow)
	candidates := trailCandidates(t)
	trail, err := NewSourceTrail(request, decision, "alpha", candidates, 1<<20, PackingOriginal)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := AppendAssociatedPassages(trail, candidates, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.ID.String() != trail.ID.String() || unchanged.Schema != trail.Schema {
		t.Fatalf("empty association stage changed the trail")
	}
}

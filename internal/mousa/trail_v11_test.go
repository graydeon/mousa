package mousa

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestV11FixedTrailAndPacket(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/trail-v11.json")
	if err != nil {
		t.Fatal(err)
	}
	trail, err := DecodeSourceTrail(data)
	if err != nil {
		t.Fatal(err)
	}
	// The fixed JSON/SHA-256 construction and ordinary counts were derived
	// independently with Python and official tiktoken 0.12.0, not this encoder.
	if trail.ID.String() != "059f3fca1299d8b4d4ed507fba284f0aaeb920e8118e262fbe3531d398418bab" || trail.PacketID != "d6a6e7671e99ea386b0d130b79f1c77d31ac04d23f68fa7c38c07016d3845f69" {
		t.Fatal("fixed identities changed")
	}
	encoded, err := EncodeSourceTrail(trail)
	if err != nil || !bytes.Equal(data, encoded) {
		t.Fatal("canonical golden changed", err)
	}
	for _, name := range []string{"alignment", "primary-alignment", "duplicate-id", "null-selected-count", "duplicate-count", "omitted-selected", "no-selected-primary", "zero-size", "profile-bytes", "truncation", "mode", "supersession", "legacy-schema"} {
		t.Run(name, func(t *testing.T) {
			var changed SourceTrail
			if err := json.Unmarshal(data, &changed); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "alignment":
				changed.Token.Associations.AssociatedIDs[0] = changed.Candidates[0].SegmentID
			case "primary-alignment":
				changed.Token.PrimaryIDs[0] = changed.Token.Associations.AssociatedIDs[0]
			case "duplicate-id":
				changed.Token.Associations.AssociatedIDs[1] = changed.Token.Associations.AssociatedIDs[0]
			case "null-selected-count":
				changed.Token.Associations.ProposedTokens[0] = nil
			case "duplicate-count":
				n := uint64(1)
				changed.Token.Associations.ProposedTokens[1] = &n
			case "omitted-selected":
				changed.Associated[2].Selected = true
			case "no-selected-primary":
				changed.Candidates[0].Selected = false
			case "truncation":
				changed.AssociationOmissions = []AssociationOmission{{FromItem: "procedure", ToItem: "other", Reason: AssociationTargetTruncated}}
			case "zero-size":
				changed.Associated[2].TextBytes = 0
			case "profile-bytes":
				changed.Associated[2].TextBytes = MaxVectorTextBytes
			case "mode":
				changed.Token.Mode = "vector"
			case "supersession":
				changed.Supersession = &SupersessionSelection{}
			case "legacy-schema":
				changed.Schema = SourceTrailSchemaV7
			}
			changed.PacketID, err = tokenPacketIdentity(changed)
			if err != nil {
				t.Fatal(err)
			}
			changed.ID, err = NewSourceTrailID(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err := changed.Validate(); err == nil {
				t.Fatal("rehashed invalid token association accepted")
			}
		})
	}
	for _, field := range []string{"mode", "backend", "rendering", "source_id", "limit", "budget", "measured_tokens", "rendered_bytes", "rendered_sha256", "proposed_tokens", "lexical", "associations", "primary_ids"} {
		for _, kind := range []string{"missing", "null", "alias", "extra"} {
			t.Run(field+"/"+kind, func(t *testing.T) {
				var object map[string]json.RawMessage
				if err := json.Unmarshal(data, &object); err != nil {
					t.Fatal(err)
				}
				var token map[string]json.RawMessage
				if err := json.Unmarshal(object["token"], &token); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "missing":
					delete(token, field)
				case "null":
					token[field] = json.RawMessage("null")
				case "alias":
					token[strings.ToUpper(field)] = token[field]
					delete(token, field)
				case "extra":
					token["unexpected"] = json.RawMessage("1")
				}
				object["token"], err = json.Marshal(token)
				if err != nil {
					t.Fatal(err)
				}
				damaged, err := json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeSourceTrail(damaged); err == nil {
					t.Fatal("invalid closed token object accepted")
				}
			})
		}
	}
	view := ReleasedTokenSelection(trail)
	view.Associations.AssociatedIDs[0] = SegmentID{}
	*view.Associations.ProposedTokens[0] = 1
	if trail.Token.Associations.AssociatedIDs[0] == (SegmentID{}) || *trail.Token.Associations.ProposedTokens[0] != 262 {
		t.Fatal("released view aliases producing facts")
	}
	legacy, err := os.ReadFile("testdata/trail-v8.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"associations", "primary_ids", "ASSOCIATIONS", "PRIMARY_IDS"} {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(legacy, &object); err != nil {
			t.Fatal(err)
		}
		var token map[string]json.RawMessage
		if err := json.Unmarshal(object["token"], &token); err != nil {
			t.Fatal(err)
		}
		token[field] = json.RawMessage("null")
		object["token"], err = json.Marshal(token)
		if err != nil {
			t.Fatal(err)
		}
		damaged, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeSourceTrail(damaged); err == nil || !strings.Contains(err.Error(), "token association members require v11") {
			t.Fatalf("legacy closed token boundary changed: %v", err)
		}
	}
}

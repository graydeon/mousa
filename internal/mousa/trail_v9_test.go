package mousa

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestV9FixedTrailAndPacket(t *testing.T) {
	// Derived independently with Python JSON/SHA-256 and tuple-encoded packet fields.
	for mode, want := range map[string]string{"vector": "a18d04dbb4c68e1a72fae10f12aa627e6a0eeddecfb4e1ea04a23e4a3010bff0", "hybrid": "91f5e522e8216ea7ed539fcbe1f109db367f1277a9a798f054370f7537d3a1fd"} {
		t.Run(mode, func(t *testing.T) {
			data, err := os.ReadFile("testdata/trail-v9-" + mode + ".json")
			if err != nil {
				t.Fatal(err)
			}
			trail, err := DecodeSourceTrail(data)
			if err != nil {
				t.Fatal(err)
			}
			if trail.ID.String() != want || trail.PacketID != "15e2d6356e81145e4e620aea2ffc69ddb68aeddf044c55f0b1f06c19a7f5b199" {
				t.Fatal("fixed identities changed")
			}
			encoded, err := EncodeSourceTrail(trail)
			if err != nil || !bytes.Equal(encoded, data) {
				t.Fatal("fixed canonical bytes changed", err)
			}
			for _, name := range []string{"consultation", "both-modes", "token", "consulted", "withheld-selected", "rank", "coverage", "order", "packing", "version"} {
				t.Run(name, func(t *testing.T) {
					var changed SourceTrail
					if err := json.Unmarshal(data, &changed); err != nil {
						t.Fatal(err)
					}
					switch name {
					case "consultation":
						changed.Supersession = nil
					case "both-modes":
						if mode == "vector" {
							changed.Hybrid = &HybridSelection{}
						} else {
							changed.Vector = &VectorSelection{}
						}
					case "token":
						changed.Token = &TokenSelection{}
					case "consulted":
						changed.Supersession.Consulted = false
					case "withheld-selected":
						changed.Candidates[0].Selected = true
					case "rank":
						changed.Candidates[1].FinalRank = 3
					case "coverage":
						if mode == "vector" {
							changed.Vector.Eligible = 2
						} else {
							changed.Hybrid.Indexed = 4
						}
					case "order":
						if mode == "vector" {
							changed.Vector.Candidates[0].Cosine = -1
						} else {
							changed.Hybrid.Candidates[0].RRF = 0
						}
					case "packing":
						changed.PackingPolicy = "unknown"
					case "version":
						if mode == "vector" {
							changed.Schema = SourceTrailSchemaV5
						} else {
							changed.Schema = SourceTrailSchemaV6
						}
					}
					changed.UsedBytes = 0
					for _, row := range changed.Candidates {
						if row.Selected {
							changed.UsedBytes += row.TextBytes
						}
					}
					packet, err := changed.packetIdentity()
					if err != nil {
						t.Fatal(err)
					}
					changed.PacketID = packet.String()
					changed.ID, err = NewSourceTrailID(changed)
					if err != nil {
						t.Fatal(err)
					}
					if err := changed.Validate(); err == nil {
						t.Fatal("rehashed invalid producing record accepted")
					}
				})
			}
			for _, field := range []string{"supersession", mode, "packing_policy", "candidates"} {
				var obj map[string]json.RawMessage
				if err := json.Unmarshal(data, &obj); err != nil {
					t.Fatal(err)
				}
				delete(obj, field)
				missing, err := json.Marshal(obj)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeSourceTrail(missing); err == nil {
					t.Fatalf("missing %s accepted", field)
				}
				obj[field] = json.RawMessage(`null`)
				null, err := json.Marshal(obj)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeSourceTrail(null); err == nil {
					t.Fatalf("null %s accepted", field)
				}
			}
		})
	}
}

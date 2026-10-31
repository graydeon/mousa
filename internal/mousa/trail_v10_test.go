package mousa

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestV10FixedTrailAndPacket(t *testing.T) {
	// Independently derived with Python JSON/SHA-256 and official ordinary encoding.
	for mode, want := range map[string][2]string{
		"vector": {"49e2f4df4a27a7b0eb6dff22d9c16882fd9a391e03b54a2910f0aeebd898f662", "86d9fd82a11fee22514ace13aea1fb0442b7cb3997d43c8e995d3285f09453d1"},
		"hybrid": {"4e0748aad343773bcaad28b7c546bebc7f476c3305e6c595eb2a79b6f418f1b5", "3d7d0d694876f0141eabf5abdc6e438a100da95bffcc8aa9c78c5122e12081d3"},
	} {
		t.Run(mode, func(t *testing.T) {
			data, err := os.ReadFile("testdata/trail-v10-" + mode + ".json")
			if err != nil {
				t.Fatal(err)
			}
			trail, err := DecodeSourceTrail(data)
			if err != nil {
				t.Fatal(err)
			}
			if trail.ID.String() != want[0] || trail.PacketID != want[1] {
				t.Fatal("fixed identities changed")
			}
			encoded, err := EncodeSourceTrail(trail)
			if err != nil || !bytes.Equal(encoded, data) {
				t.Fatal("canonical golden changed", err)
			}
			for _, name := range []string{"mode", "both-modes", "consultation", "withheld-count", "withheld-selected", "size", "rank", "version", "coverage"} {
				t.Run(name, func(t *testing.T) {
					var changed SourceTrail
					if err := json.Unmarshal(data, &changed); err != nil {
						t.Fatal(err)
					}
					switch name {
					case "mode":
						if mode == "vector" {
							changed.Token.Mode = "hybrid"
						} else {
							changed.Token.Mode = "vector"
						}
					case "both-modes":
						if mode == "vector" {
							changed.Hybrid = &HybridSelection{}
						} else {
							changed.Vector = &VectorSelection{}
						}
					case "consultation":
						changed.Supersession = nil
					case "withheld-count":
						n := uint64(1)
						changed.Token.ProposedTokens[0] = &n
					case "withheld-selected":
						changed.Candidates[0].Selected = true
					case "size":
						changed.Candidates[0].TextBytes = 0
					case "rank":
						changed.Candidates[1].FinalRank = 3
					case "version":
						changed.Schema = SourceTrailSchemaV7
					case "coverage":
						if mode == "vector" {
							changed.Vector.Indexed = 4
						} else {
							changed.Hybrid.Indexed = 4
						}
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
						t.Fatal("rehashed invalid producing selection accepted")
					}
				})
			}
			for _, field := range []string{"token", "supersession", mode, "packing_policy", "candidates"} {
				for _, null := range []bool{false, true} {
					var obj map[string]json.RawMessage
					if err := json.Unmarshal(data, &obj); err != nil {
						t.Fatal(err)
					}
					if null {
						obj[field] = json.RawMessage(`null`)
					} else {
						delete(obj, field)
					}
					damaged, err := json.Marshal(obj)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := DecodeSourceTrail(damaged); err == nil {
						t.Fatalf("missing/null %s accepted", field)
					}
				}
			}
		})
	}
}

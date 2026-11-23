package mousa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// SourceTrailID binds the complete trail explanation and parent snapshot.
type SourceTrailID [sha256.Size]byte

func (id SourceTrailID) String() string { return hex.EncodeToString(id[:]) }

func (id SourceTrailID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *SourceTrailID) UnmarshalJSON(data []byte) error {
	decoded, err := decodeLowerHexJSON("id", data)
	if err != nil {
		return err
	}
	*id = SourceTrailID(decoded)
	return nil
}

// ParseSourceTrailID parses one lowercase hexadecimal trail identity.
func ParseSourceTrailID(value string) (SourceTrailID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return SourceTrailID{}, err
	}
	return SourceTrailID(decoded), nil
}

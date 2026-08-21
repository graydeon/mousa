package mousa

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
)

type DocumentID [sha256.Size]byte

type ChunkID [sha256.Size]byte

type SHA256 [sha256.Size]byte

func NewDocumentID(sourceID string, normalizedContentSHA256 SHA256) DocumentID {
	digest := sha256.New()
	writeTuple(digest, []byte("mousa.document.v1"), []byte(sourceID), normalizedContentSHA256[:])
	return DocumentID(digest.Sum(nil))
}

func NewChunkID(documentID DocumentID, ordinal, startByte, endByte uint64, textSHA256 SHA256) (ChunkID, error) {
	if endByte < startByte {
		return ChunkID{}, newValidationError("end_byte", ValidationCodeInvalidRange, "must be greater than or equal to start_byte", nil)
	}
	digest := sha256.New()
	var ordinalBytes, startBytes, endBytes [8]byte
	binary.BigEndian.PutUint64(ordinalBytes[:], ordinal)
	binary.BigEndian.PutUint64(startBytes[:], startByte)
	binary.BigEndian.PutUint64(endBytes[:], endByte)
	writeTuple(
		digest,
		[]byte("mousa.chunk.v1"),
		documentID[:],
		ordinalBytes[:],
		startBytes[:],
		endBytes[:],
		textSHA256[:],
	)
	return ChunkID(digest.Sum(nil)), nil
}

func (id DocumentID) String() string {
	return hex.EncodeToString(id[:])
}

func (id ChunkID) String() string {
	return hex.EncodeToString(id[:])
}

func (digest SHA256) String() string {
	return hex.EncodeToString(digest[:])
}

func ParseDocumentID(value string) (DocumentID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return DocumentID{}, err
	}
	return DocumentID(decoded), nil
}

func ParseChunkID(value string) (ChunkID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return ChunkID{}, err
	}
	return ChunkID(decoded), nil
}

func (id DocumentID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *DocumentID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseDocumentID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id ChunkID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *ChunkID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseChunkID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func ParseSHA256(value string) (SHA256, error) {
	decoded, err := decodeLowerHex("sha256", ValidationCodeInvalidDigest, value)
	if err != nil {
		return SHA256{}, err
	}
	return SHA256(decoded), nil
}

func (digest SHA256) MarshalJSON() ([]byte, error) {
	return json.Marshal(digest.String())
}

func (digest *SHA256) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("sha256", ValidationCodeInvalidDigest, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseSHA256(value)
	if err != nil {
		return err
	}
	*digest = parsed
	return nil
}

func decodeLowerHex(field string, code ValidationCode, value string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	if len(value) != hex.EncodedLen(len(result)) {
		return result, newValidationError(field, code, "must contain 64 lowercase hexadecimal characters", nil)
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return result, newValidationError(field, code, "must contain 64 lowercase hexadecimal characters", nil)
		}
	}
	if _, err := hex.Decode(result[:], []byte(value)); err != nil {
		return result, newValidationError(field, code, "must contain 64 lowercase hexadecimal characters", fmt.Errorf("decode hexadecimal value: %w", err))
	}
	return result, nil
}

func writeTuple(digest hash.Hash, fields ...[]byte) {
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write(field)
	}
}

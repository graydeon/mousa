package mousa

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"unicode/utf8"
)

type SourceID [sha256.Size]byte

type ObservationID [sha256.Size]byte

type SHA256 [sha256.Size]byte

func NewSourceID(namespace, externalSourceID string) (SourceID, error) {
	if err := validateIdentityInput("namespace", namespace); err != nil {
		return SourceID{}, err
	}
	if err := validateIdentityInput("external_source_id", externalSourceID); err != nil {
		return SourceID{}, err
	}
	digest := sha256.New()
	writeTuple(digest, []byte("mousa.source.v1"), []byte(namespace), []byte(externalSourceID))
	return SourceID(digest.Sum(nil)), nil
}

func NewObservationID(sourceID SourceID, externalObservationID string) (ObservationID, error) {
	if err := validateIdentityInput("external_observation_id", externalObservationID); err != nil {
		return ObservationID{}, err
	}
	digest := sha256.New()
	writeTuple(digest, []byte("mousa.observation.v1"), sourceID[:], []byte(externalObservationID))
	return ObservationID(digest.Sum(nil)), nil
}

func (id SourceID) String() string {
	return hex.EncodeToString(id[:])
}

func (id ObservationID) String() string {
	return hex.EncodeToString(id[:])
}

func (digest SHA256) String() string {
	return hex.EncodeToString(digest[:])
}

func ParseSourceID(value string) (SourceID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return SourceID{}, err
	}
	return SourceID(decoded), nil
}

func ParseObservationID(value string) (ObservationID, error) {
	decoded, err := decodeLowerHex("id", ValidationCodeInvalidID, value)
	if err != nil {
		return ObservationID{}, err
	}
	return ObservationID(decoded), nil
}

func (id SourceID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *SourceID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseSourceID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

func (id ObservationID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

func (id *ObservationID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseObservationID(value)
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

func validateIdentityInput(field, value string) error {
	if value == "" {
		return newValidationError(field, ValidationCodeInvalidValue, "must not be empty", nil)
	}
	if !utf8.ValidString(value) {
		return newValidationError(field, ValidationCodeInvalidValue, "must be valid UTF-8", nil)
	}
	return nil
}

func writeTuple(digest hash.Hash, fields ...[]byte) {
	var length [8]byte
	for _, field := range fields {
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write(field)
	}
}

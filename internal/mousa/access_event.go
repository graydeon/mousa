package mousa

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
)

const (
	// AccessEventSchema names one immutable change of a source's access.
	AccessEventSchema = "mousa.access_event.v1"
	maxActorBytes     = 256
	// maxJSONSafeInteger is the largest integer every JSON reader holds exactly (2^53 - 1).
	maxJSONSafeInteger = 1<<53 - 1
)

// AccessEventID is the SHA-256 identity of one access event.
type AccessEventID [sha256.Size]byte

// AccessEvent records that an actor set a source's access at a time. Events are append-only;
// a source's current access is its latest event in log order, and a source with no event is
// denied.
type AccessEvent struct {
	Schema         string
	ID             AccessEventID
	SourceID       SourceID
	Access         SourceAccess
	Actor          string
	OccurredAtUsec int64
}

// NewAccessEventID hashes the length-prefixed tuple (schema, source ID, access, occurred_at_usec
// as 8 big-endian bytes, actor).
func NewAccessEventID(event AccessEvent) (AccessEventID, error) {
	if event.SourceID == (SourceID{}) {
		return AccessEventID{}, newValidationError("source_id", ValidationCodeInvalidID, "must not be zero", nil)
	}
	if err := event.Access.Validate(); err != nil {
		return AccessEventID{}, err
	}
	if err := validateActor(event.Actor); err != nil {
		return AccessEventID{}, err
	}
	if event.OccurredAtUsec <= 0 || event.OccurredAtUsec > maxJSONSafeInteger {
		return AccessEventID{}, newValidationError("occurred_at_usec", ValidationCodeInvalidRange, "must be positive and at most 2^53-1", nil)
	}
	var time [8]byte
	binary.BigEndian.PutUint64(time[:], uint64(event.OccurredAtUsec))
	digest := sha256.New()
	writeTuple(digest, []byte(AccessEventSchema), event.SourceID[:], []byte(event.Access), time[:], []byte(event.Actor))
	return AccessEventID(digest.Sum(nil)), nil
}

// NewAccessEvent returns a validated event with its identity.
func NewAccessEvent(sourceID SourceID, access SourceAccess, actor string, occurredAtUsec int64) (AccessEvent, error) {
	event := AccessEvent{Schema: AccessEventSchema, SourceID: sourceID, Access: access, Actor: actor, OccurredAtUsec: occurredAtUsec}
	id, err := NewAccessEventID(event)
	if err != nil {
		return AccessEvent{}, err
	}
	event.ID = id
	return event, nil
}

// validateActor keeps actors to printable ASCII, so the encoding below needs no escaping rule
// beyond quote and backslash and matches RFC 8785 byte for byte.
func validateActor(actor string) error {
	if actor == "" || len(actor) > maxActorBytes {
		return newValidationError("actor", ValidationCodeInvalidValue, "must be 1 to 256 bytes", nil)
	}
	for i := 0; i < len(actor); i++ {
		if actor[i] < 0x20 || actor[i] > 0x7e {
			return newValidationError("actor", ValidationCodeInvalidValue, "must be printable ASCII", nil)
		}
	}
	return nil
}

func (event AccessEvent) Validate() error {
	if event.Schema != AccessEventSchema {
		return newValidationError("schema", ValidationCodeInvalidSchema, "must be mousa.access_event.v1", nil)
	}
	want, err := NewAccessEventID(event)
	if err != nil {
		return err
	}
	if event.ID != want {
		return newValidationError("id", ValidationCodeInvalidID, "does not match access event identity", nil)
	}
	return nil
}

// accessEventWire declares fields in sorted key order and holds no floats, so encoding/json
// emits RFC 8785 canonical JSON for it.
type accessEventWire struct {
	Access         SourceAccess  `json:"access"`
	Actor          string        `json:"actor"`
	ID             AccessEventID `json:"id"`
	OccurredAtUsec int64         `json:"occurred_at_usec"`
	Schema         string        `json:"schema"`
	SourceID       SourceID      `json:"source_id"`
}

// EncodeAccessEvent returns the canonical event bytes: RFC 8785 JSON with no trailing newline.
func EncodeAccessEvent(event AccessEvent) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	data, err := encodeJSONContract(accessEventWire{event.Access, event.Actor, event.ID, event.OccurredAtUsec, event.Schema, event.SourceID}, "access event")
	if err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(data, []byte("\n")), nil
}

// DecodeAccessEvent accepts only the exact canonical bytes of a valid event.
func DecodeAccessEvent(data []byte) (AccessEvent, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return AccessEvent{}, err
	}
	var wire accessEventWire
	if err := decodeJSONContract(data, &wire); err != nil {
		return AccessEvent{}, err
	}
	event := AccessEvent{Schema: wire.Schema, ID: wire.ID, SourceID: wire.SourceID, Access: wire.Access, Actor: wire.Actor, OccurredAtUsec: wire.OccurredAtUsec}
	encoded, err := EncodeAccessEvent(event)
	if err != nil {
		return AccessEvent{}, err
	}
	if !bytes.Equal(encoded, data) {
		return AccessEvent{}, newValidationError("", ValidationCodeInvalidJSON, "access event is not canonical", nil)
	}
	return event, nil
}

func (id AccessEventID) String() string { return hex.EncodeToString(id[:]) }

func ParseAccessEventID(value string) (AccessEventID, error) {
	raw, err := decodeLowerHex("access_event_id", ValidationCodeInvalidID, value)
	if err != nil {
		return AccessEventID{}, err
	}
	return AccessEventID(raw), nil
}

func (id AccessEventID) MarshalJSON() ([]byte, error) { return json.Marshal(id.String()) }

func (id *AccessEventID) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return newValidationError("access_event_id", ValidationCodeInvalidID, "must be a lowercase SHA-256 value", err)
	}
	parsed, err := ParseAccessEventID(value)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}

// Log kinds name the record tables whose rows are log leaves.
const (
	LogKindIngestReceipt    = "ingest_receipt"
	LogKindAccessEvent      = "access_event"
	LogKindSourceWithdrawal = "source_withdrawal"
)

// LogLeafHash is the RFC 6962 leaf hash of one record: SHA-256 over 0x00 then the record's
// canonical bytes as stored.
func LogLeafHash(record []byte) [sha256.Size]byte {
	digest := sha256.New()
	digest.Write([]byte{0})
	digest.Write(record)
	return [sha256.Size]byte(digest.Sum(nil))
}

// StoreID is the random identity a store receives when it is created.
type StoreID [16]byte

func (id StoreID) String() string { return hex.EncodeToString(id[:]) }

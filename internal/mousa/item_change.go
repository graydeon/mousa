package mousa

import (
	"bytes"
	"crypto/sha256"
	"errors"
)

const ItemChangeSchema = "mousa.item_change.v1"
const LogKindItemChange = "item_change"

// ItemChange records an atomic change to the searchable revision of an item.
// The log index distinguishes two separate activations of the same revision.
type ItemChange struct {
	Current  *RepresentationID `json:"current,omitempty"`
	ID       SHA256            `json:"id,omitzero"`
	ItemID   string            `json:"item_id"`
	Previous *RepresentationID `json:"previous,omitempty"`
	Schema   string            `json:"schema"`
	Seq      uint64            `json:"seq"`
	SourceID SourceID          `json:"source_id"`
}

func NewItemChange(source SourceID, item string, seq uint64, previous, current *RepresentationID) (ItemChange, error) {
	change := ItemChange{SourceID: source, ItemID: item, Seq: seq, Schema: ItemChangeSchema, Previous: previous, Current: current}
	if err := change.validate(); err != nil {
		return ItemChange{}, err
	}
	data, err := canonicalTrailJSON(change)
	if err != nil {
		return ItemChange{}, err
	}
	change.ID = SHA256(sha256.Sum256(data))
	return change, nil
}

func (change ItemChange) validate() error {
	if err := validateIdentityInput("item_id", change.ItemID); err != nil {
		return err
	}
	previous, current := change.Previous, change.Current
	if change.Schema != ItemChangeSchema || change.SourceID == (SourceID{}) || change.Seq == 0 ||
		change.Seq > maxJSONSafeInteger || (current == nil && previous == nil) ||
		(current != nil && *current == (RepresentationID{})) ||
		(previous != nil && *previous == (RepresentationID{})) ||
		(previous != nil && current != nil && *previous == *current) {
		return errors.New("invalid item change")
	}
	return nil
}

func EncodeItemChange(change ItemChange) ([]byte, error) {
	if err := change.validate(); err != nil {
		return nil, err
	}
	id := change.ID
	change.ID = SHA256{}
	data, err := canonicalTrailJSON(change)
	if err != nil {
		return nil, err
	}
	if id != SHA256(sha256.Sum256(data)) {
		return nil, errors.New("item change ID disagrees with record")
	}
	change.ID = id
	return canonicalTrailJSON(change)
}

func DecodeItemChange(data []byte) (ItemChange, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return ItemChange{}, err
	}
	var change ItemChange
	if err := decodeJSONContract(data, &change); err != nil {
		return ItemChange{}, err
	}
	canonical, err := EncodeItemChange(change)
	if err != nil {
		return ItemChange{}, err
	}
	if !bytes.Equal(canonical, data) {
		return ItemChange{}, errors.New("item change bytes are not canonical")
	}
	return change, nil
}

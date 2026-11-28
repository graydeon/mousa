package mousa

import (
	"bytes"
	"crypto/sha256"
	"errors"
)

const ItemChangeSchemaV1 = "mousa.item_change.v1"
const ItemChangeSchemaV2 = "mousa.item_change.v2"
const ItemChangeSchema = "mousa.item_change.v3"
const LogKindItemChange = "item_change"

// ItemChange records an atomic change to the searchable revision of an item.
// The log index distinguishes two separate activations of the same revision.
type ItemChange struct {
	ActivatedAtUsec int64             `json:"activated_at_usec,omitzero"`
	CreationPath    string            `json:"creation_path,omitempty"`
	Current         *RepresentationID `json:"current,omitempty"`
	ID              SHA256            `json:"id,omitzero"`
	ItemID          string            `json:"item_id"`
	Previous        *RepresentationID `json:"previous,omitempty"`
	Schema          string            `json:"schema"`
	Seq             uint64            `json:"seq"`
	SourceID        SourceID          `json:"source_id"`
}

func NewItemChange(source SourceID, item string, seq uint64, previous, current *RepresentationID, activatedAtUsec int64, creationPath string) (ItemChange, error) {
	schema := ItemChangeSchemaV2
	if creationPath != "" {
		schema = ItemChangeSchema
	}
	change := ItemChange{SourceID: source, ItemID: item, Seq: seq, Schema: schema, Previous: previous, Current: current, ActivatedAtUsec: activatedAtUsec, CreationPath: creationPath}
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
	if change.SourceID == (SourceID{}) || change.Seq == 0 ||
		change.Seq > maxJSONSafeInteger || (current == nil && previous == nil) ||
		(current != nil && *current == (RepresentationID{})) ||
		(previous != nil && *previous == (RepresentationID{})) ||
		(previous != nil && current != nil && *previous == *current) {
		return errors.New("invalid item change")
	}
	switch change.Schema {
	case ItemChangeSchemaV1:
		if change.ActivatedAtUsec != 0 {
			return errors.New("legacy item change has an activation time")
		}
		if change.CreationPath != "" {
			return errors.New("legacy item change has a creation path")
		}
	case ItemChangeSchemaV2, ItemChangeSchema:
		if change.ActivatedAtUsec < 0 || change.ActivatedAtUsec > maxJSONSafeInteger || (change.ActivatedAtUsec != 0) != (current != nil) {
			return errors.New("invalid item activation time")
		}
		if change.Schema == ItemChangeSchemaV2 && change.CreationPath != "" {
			return errors.New("version 2 item change has a creation path")
		}
		if change.Schema == ItemChangeSchema && (previous != nil || current == nil || change.CreationPath == "" ||
			(!ValidPath(change.CreationPath) && change.CreationPath != change.ItemID)) {
			return errors.New("invalid item creation path")
		}
	default:
		return errors.New("unknown item change schema")
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

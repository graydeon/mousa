package mousa

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"regexp"
)

// DefaultNotePath is where a note lives until it is moved.
const DefaultNotePath = "unsorted"

// pathPattern is a slash-delimited path of lowercase segments. Document items
// created before paths existed keep their literal item ID instead; that
// exception is checked where the item ID is known, never by this pattern.
var pathPattern = regexp.MustCompile(`^[a-z0-9_][a-z0-9._-]*(/[a-z0-9_][a-z0-9._-]*)*$`)

const maxPathBytes = 256

// ValidPath reports whether path is a store path: lowercase ASCII segments of
// letters, digits, '.', '_' and '-' joined by single slashes.
func ValidPath(path string) bool {
	return len(path) <= maxPathBytes && pathPattern.MatchString(path)
}

// LogKindPathMove names the path-move record kind in the store log.
const LogKindPathMove = "path_move"

// PathMoveEntry records one item's path change inside a move.
type PathMoveEntry struct {
	ItemID  string `json:"item_id"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
}

// PathMove is the canonical record of one move operation. Its identity is the
// SHA-256 of its canonical bytes, which carry the global log sequence it was
// committed under, so the record names its own position.
type PathMove struct {
	Actor    string          `json:"actor"`
	Records  []PathMoveEntry `json:"records"`
	Seq      uint64          `json:"seq"`
	SourceID SourceID        `json:"source_id"`
}

// MoveSelector picks the items one move touches: exactly one of a note
// identity, a document item ID, or a literal path prefix rewritten to To.
type MoveSelector struct {
	NoteID SHA256
	ItemID string
	Prefix string
	To     string
}

// EncodePathMove returns the canonical record bytes and the move identity:
// RFC 8785 JSON with no trailing newline, hashed once.
func EncodePathMove(move PathMove) ([]byte, SHA256, error) {
	data, err := encodePathMove(move)
	if err != nil {
		return nil, SHA256{}, err
	}
	return data, SHA256(sha256.Sum256(data)), nil
}

func encodePathMove(move PathMove) ([]byte, error) {
	if err := move.validate(); err != nil {
		return nil, err
	}
	return canonicalTrailJSON(move)
}

// DecodePathMove accepts only the exact canonical bytes of a valid move.
func DecodePathMove(data []byte) (PathMove, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return PathMove{}, err
	}
	var move PathMove
	if err := decodeJSONContract(data, &move); err != nil {
		return PathMove{}, err
	}
	canonical, err := encodePathMove(move)
	if err != nil {
		return PathMove{}, err
	}
	if !bytes.Equal(canonical, data) {
		return PathMove{}, errors.New("path move bytes are not canonical")
	}
	return move, nil
}

func (move PathMove) validate() error {
	if move.SourceID == (SourceID{}) {
		return errors.New("path move has a zero source id")
	}
	if move.Seq == 0 || move.Seq > maxJSONSafeInteger {
		return errors.New("path move has an invalid sequence")
	}
	if err := validateActor(move.Actor); err != nil {
		return err
	}
	if len(move.Records) == 0 {
		return errors.New("path move has no records")
	}
	for i, entry := range move.Records {
		if entry.ItemID == "" {
			return errors.New("path move record has an empty item id")
		}
		if i > 0 && entry.ItemID <= move.Records[i-1].ItemID {
			return errors.New("path move records are not sorted unique item ids")
		}
		if !ValidPath(entry.NewPath) {
			return errors.New("path move record has an invalid destination path")
		}
		// An item's pre-path location is its literal item ID, which need not
		// be a valid path; every other old location is one.
		if !ValidPath(entry.OldPath) && entry.OldPath != entry.ItemID {
			return errors.New("path move record has an invalid old path")
		}
		if entry.OldPath == entry.NewPath {
			return errors.New("path move record names its own path as the destination")
		}
	}
	return nil
}

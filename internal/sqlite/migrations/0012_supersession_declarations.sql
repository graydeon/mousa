-- Immutable supersession declarations: source-local history pinned to exact item revisions.
-- The row projects the canonical declaration; there is no UPDATE or DELETE path, and the
-- RESTRICT foreign keys keep a declaration from outliving the source or representation it pins.
-- Item identities are bounded in UTF-8 bytes (length(CAST(… AS BLOB))), matching the JSONL item
-- limit, because length() on TEXT alone would count characters.
CREATE TABLE supersession_declarations (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    source_id BLOB NOT NULL CHECK (typeof(source_id) = 'blob' AND length(source_id) = 32 AND source_id != zeroblob(32)) REFERENCES sources(id) ON DELETE RESTRICT,
    predecessor_item_id TEXT NOT NULL CHECK (typeof(predecessor_item_id) = 'text' AND length(CAST(predecessor_item_id AS BLOB)) BETWEEN 1 AND 4096),
    predecessor_representation_id BLOB NOT NULL CHECK (typeof(predecessor_representation_id) = 'blob' AND length(predecessor_representation_id) = 32 AND predecessor_representation_id != zeroblob(32)) REFERENCES representations(id) ON DELETE RESTRICT,
    successor_item_id TEXT NOT NULL CHECK (typeof(successor_item_id) = 'text' AND length(CAST(successor_item_id AS BLOB)) BETWEEN 1 AND 4096),
    successor_representation_id BLOB NOT NULL CHECK (typeof(successor_representation_id) = 'blob' AND length(successor_representation_id) = 32 AND successor_representation_id != zeroblob(32)) REFERENCES representations(id) ON DELETE RESTRICT,
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) BETWEEN 1 AND 65536)
) STRICT;

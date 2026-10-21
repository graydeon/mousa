-- Caller-declared spaces and source/segment-bound immutable embeddings.
-- Current eligibility is always read from local_items, never from these records.
CREATE TABLE vector_spaces (
    id BLOB PRIMARY KEY NOT NULL CHECK (length(id) = 32 AND id != zeroblob(32)),
    record_json BLOB NOT NULL CHECK (length(record_json) BETWEEN 1 AND 4096)
) STRICT;
CREATE TABLE embeddings (
    id BLOB PRIMARY KEY NOT NULL CHECK (length(id) = 32 AND id != zeroblob(32)),
    space_id BLOB NOT NULL REFERENCES vector_spaces(id) ON DELETE RESTRICT,
    source_id BLOB NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
    segment_id BLOB NOT NULL REFERENCES segments(id) ON DELETE RESTRICT,
    record_json BLOB NOT NULL CHECK (length(record_json) BETWEEN 1 AND 131072),
    UNIQUE (space_id, source_id, segment_id)
) STRICT;
-- These indexes bound enumeration before decoding or scoring source passages.
CREATE INDEX local_items_vector_current_idx ON local_items(source_id, active, item_id);
CREATE INDEX segments_vector_order_idx ON segments(representation_id, selector_start, selector_end, id);

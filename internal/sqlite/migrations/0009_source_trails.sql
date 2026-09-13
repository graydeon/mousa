CREATE TABLE source_trails (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    decision_id BLOB NOT NULL CHECK (typeof(decision_id) = 'blob' AND length(decision_id) = 32 AND decision_id != zeroblob(32)) REFERENCES policy_decisions(id) ON DELETE RESTRICT,
    outcome TEXT NOT NULL COLLATE BINARY CHECK (outcome IN ('allow', 'deny')),
    packet_id BLOB NOT NULL CHECK (typeof(packet_id) = 'blob' AND length(packet_id) = 32 AND packet_id != zeroblob(32)),
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0)
) STRICT;

CREATE TABLE source_trail_candidates (
    trail_id BLOB NOT NULL CHECK (typeof(trail_id) = 'blob' AND length(trail_id) = 32 AND trail_id != zeroblob(32)) REFERENCES source_trails(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL CHECK (typeof(ordinal) = 'integer' AND ordinal >= 0),
    segment_id BLOB NOT NULL CHECK (typeof(segment_id) = 'blob' AND length(segment_id) = 32 AND segment_id != zeroblob(32)) REFERENCES segments(id) ON DELETE RESTRICT,
    final_rank INTEGER NOT NULL CHECK (typeof(final_rank) = 'integer' AND final_rank >= 0),
    disposition TEXT NOT NULL COLLATE BINARY CHECK (disposition IN ('accepted', 'rejected')),
    selected INTEGER NOT NULL CHECK (selected IN (0, 1)),
    PRIMARY KEY (trail_id, ordinal),
    CHECK (disposition = 'accepted' OR final_rank = 0),
    CHECK (selected = 0 OR (disposition = 'accepted' AND final_rank > 0))
) STRICT;

-- Opt-in revision text history. A source whose latest source_history leaf
-- says enabled archives each retiring segment's verified text, so a served
-- request or recheck can show the revision that was replaced. The archive
-- rows are payloads, not logged records; every payload is checked against its
-- canonical segment on read and at verify.

CREATE TABLE segment_history (
    segment_id BLOB PRIMARY KEY NOT NULL REFERENCES segments(id) ON DELETE RESTRICT,
    text TEXT NOT NULL
) STRICT;

CREATE TABLE source_history_events (
    seq INTEGER PRIMARY KEY NOT NULL CHECK (seq > 0),
    id BLOB UNIQUE NOT NULL CHECK (length(id) = 32 AND id != zeroblob(32)),
    source_id BLOB NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
    record_json BLOB NOT NULL CHECK (length(record_json) > 0)
) STRICT;

CREATE INDEX source_history_events_source_seq_idx ON source_history_events(source_id, seq);

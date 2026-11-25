-- The append-only store log. Each receipt, access event, withdrawal, note,
-- trail, path move, request, and source-history event is one leaf, written in
-- the same transaction as its record.
-- seq runs 1, 2, 3 ... with no gaps; leaf_hash is the RFC 6962 leaf hash
-- SHA-256(0x00 || record_json) of the record named by kind and record_id.
CREATE TABLE log (
    seq INTEGER PRIMARY KEY NOT NULL CHECK (seq > 0),
    kind TEXT NOT NULL COLLATE BINARY,
    record_id BLOB NOT NULL CHECK (typeof(record_id) = 'blob' AND length(record_id) = 32 AND record_id != zeroblob(32)),
    source_id BLOB CHECK (source_id IS NULL OR (length(source_id) = 32 AND source_id != zeroblob(32))) REFERENCES sources(id) ON DELETE RESTRICT,
    leaf_hash BLOB NOT NULL CHECK (typeof(leaf_hash) = 'blob' AND length(leaf_hash) = 32),
    UNIQUE (kind, record_id),
    CHECK (CASE WHEN source_id IS NULL
        THEN kind IN ('trail', 'request_admission', 'request_outcome')
        ELSE kind IN ('ingest_receipt', 'access_event', 'item_change', 'source_withdrawal', 'note', 'path_move', 'source_history')
    END)
) STRICT;

CREATE INDEX log_source_kind_seq_idx ON log(source_id, kind, seq);
CREATE INDEX log_source_seq_idx ON log(source_id, seq);

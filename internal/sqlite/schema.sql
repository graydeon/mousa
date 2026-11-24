-- The one Mousa store schema. Open sets application_id and user_version after
-- applying it; a store with any other identity or version is refused.

CREATE TABLE sources (
  id BLOB PRIMARY KEY NOT NULL
    CHECK (length(id) = 32 AND id <> zeroblob(32)),
  record_json BLOB NOT NULL CHECK (length(record_json) > 0)
) STRICT;

CREATE TABLE collections (
  name TEXT PRIMARY KEY NOT NULL
    CHECK (length(name) BETWEEN 1 AND 64 AND name GLOB '[a-z0-9]*'
      AND name NOT GLOB '*[^a-z0-9._-]*')
) STRICT;

CREATE TABLE collection_sources (
  collection TEXT NOT NULL REFERENCES collections(name) ON DELETE RESTRICT,
  source_id BLOB NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
  PRIMARY KEY (collection, source_id)
) STRICT;

CREATE TABLE observations (
  id BLOB PRIMARY KEY NOT NULL
    CHECK (length(id) = 32 AND id <> zeroblob(32)),
  source_id BLOB NOT NULL
    CHECK (length(source_id) = 32 AND source_id <> zeroblob(32))
    REFERENCES sources(id) ON DELETE RESTRICT,
  record_json BLOB NOT NULL CHECK (length(record_json) > 0)
) STRICT;

CREATE INDEX observations_source_id_idx ON observations(source_id);

CREATE TABLE artifacts (
  id BLOB PRIMARY KEY NOT NULL
    CHECK (length(id) = 32 AND id <> zeroblob(32)),
  observation_id BLOB NOT NULL
    CHECK (length(observation_id) = 32 AND observation_id <> zeroblob(32))
    REFERENCES observations(id) ON DELETE RESTRICT,
  record_json BLOB NOT NULL CHECK (length(record_json) > 0)
) STRICT;

CREATE INDEX artifacts_observation_id_idx ON artifacts(observation_id);

CREATE TABLE representations (
  id BLOB PRIMARY KEY NOT NULL
    CHECK (length(id) = 32 AND id <> zeroblob(32)),
  record_json BLOB NOT NULL CHECK (length(record_json) > 0)
) STRICT;

CREATE TABLE representation_inputs (
  representation_id BLOB NOT NULL
    CHECK (length(representation_id) = 32 AND representation_id <> zeroblob(32))
    REFERENCES representations(id) ON DELETE RESTRICT,
  ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
  input_kind TEXT NOT NULL CHECK (input_kind IN ('artifact', 'representation')),
  artifact_id BLOB
    CHECK (artifact_id IS NULL OR
      (length(artifact_id) = 32 AND artifact_id <> zeroblob(32)))
    REFERENCES artifacts(id) ON DELETE RESTRICT,
  input_representation_id BLOB
    CHECK (input_representation_id IS NULL OR
      (length(input_representation_id) = 32 AND input_representation_id <> zeroblob(32)))
    REFERENCES representations(id) ON DELETE RESTRICT,
  PRIMARY KEY (representation_id, ordinal),
  CHECK (
    (input_kind = 'artifact' AND artifact_id IS NOT NULL AND input_representation_id IS NULL)
    OR
    (input_kind = 'representation' AND artifact_id IS NULL AND input_representation_id IS NOT NULL)
  )
) STRICT;

CREATE INDEX representation_inputs_artifact_id_idx
  ON representation_inputs(artifact_id)
  WHERE artifact_id IS NOT NULL;

CREATE INDEX representation_inputs_representation_id_idx
  ON representation_inputs(input_representation_id)
  WHERE input_representation_id IS NOT NULL;

CREATE TABLE segments (
  id BLOB PRIMARY KEY NOT NULL
    CHECK (length(id) = 32 AND id <> zeroblob(32)),
  representation_id BLOB NOT NULL
    CHECK (length(representation_id) = 32 AND representation_id <> zeroblob(32))
    REFERENCES representations(id) ON DELETE RESTRICT,
  selector_start BLOB NOT NULL CHECK (length(selector_start) = 8),
  selector_end BLOB NOT NULL
    CHECK (length(selector_end) = 8 AND selector_start < selector_end),
  record_json BLOB NOT NULL CHECK (length(record_json) > 0)
) STRICT;

CREATE INDEX segments_representation_id_idx ON segments(representation_id);

CREATE TABLE ingest_receipts (
  observation_id BLOB PRIMARY KEY NOT NULL
    CHECK (length(observation_id) = 32 AND observation_id <> zeroblob(32))
    REFERENCES observations(id) ON DELETE RESTRICT,
  source_id BLOB NOT NULL
    CHECK (length(source_id) = 32 AND source_id <> zeroblob(32))
    REFERENCES sources(id) ON DELETE RESTRICT,
  initiative TEXT NOT NULL CHECK (initiative IN ('push', 'pull')),
  form TEXT NOT NULL CHECK (form IN ('item', 'snapshot', 'stream_window')),
  captured_at_usec INTEGER NOT NULL CHECK (captured_at_usec > 0),
  expected_checkpoint BLOB
    CHECK (expected_checkpoint IS NULL OR (length(expected_checkpoint) BETWEEN 1 AND 4096)),
  next_checkpoint BLOB
    CHECK (next_checkpoint IS NULL OR (length(next_checkpoint) BETWEEN 1 AND 4096)),
  sequence BLOB CHECK (sequence IS NULL OR length(sequence) = 8),
  coverage_start_usec INTEGER,
  coverage_end_usec INTEGER,
  record_json BLOB NOT NULL CHECK (length(record_json) > 0),
  CHECK ((initiative = 'pull' AND next_checkpoint IS NOT NULL AND sequence IS NULL)
      OR (initiative = 'push' AND expected_checkpoint IS NULL AND next_checkpoint IS NULL)),
  CHECK ((form = 'item' AND coverage_start_usec IS NULL AND coverage_end_usec IS NULL)
      OR (form IN ('snapshot', 'stream_window') AND coverage_start_usec > 0
          AND coverage_end_usec > coverage_start_usec))
) STRICT;

CREATE INDEX ingest_receipts_source_id_idx ON ingest_receipts(source_id);

CREATE TABLE ingest_gaps (
  observation_id BLOB NOT NULL
    REFERENCES ingest_receipts(observation_id) ON DELETE RESTRICT,
  ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
  gap_start BLOB NOT NULL CHECK (length(gap_start) = 8),
  gap_end BLOB NOT NULL CHECK (length(gap_end) = 8 AND gap_start < gap_end),
  PRIMARY KEY (observation_id, ordinal)
) STRICT;

CREATE TABLE source_withdrawals (
  id BLOB PRIMARY KEY NOT NULL CHECK (length(id) = 32 AND id <> zeroblob(32)),
  source_id BLOB NOT NULL
    CHECK (length(source_id) = 32 AND source_id <> zeroblob(32))
    REFERENCES sources(id) ON DELETE RESTRICT,
  occurred_at_usec INTEGER NOT NULL CHECK (occurred_at_usec > 0),
  record_json BLOB NOT NULL CHECK (length(record_json) > 0)
) STRICT;

CREATE INDEX source_withdrawals_source_id_idx ON source_withdrawals(source_id);

CREATE TABLE source_ingest_state (
  source_id BLOB PRIMARY KEY NOT NULL
    CHECK (length(source_id) = 32 AND source_id <> zeroblob(32))
    REFERENCES sources(id) ON DELETE RESTRICT,
  collection_state TEXT NOT NULL CHECK (collection_state IN ('active', 'withdrawn')),
  checkpoint BLOB CHECK (checkpoint IS NULL OR length(checkpoint) BETWEEN 1 AND 4096),
  high_sequence BLOB CHECK (high_sequence IS NULL OR length(high_sequence) = 8),
  last_observation_id BLOB
    CHECK (last_observation_id IS NULL OR (length(last_observation_id) = 32 AND last_observation_id <> zeroblob(32)))
    REFERENCES observations(id) ON DELETE RESTRICT,
  current_withdrawal_id BLOB
    CHECK (current_withdrawal_id IS NULL OR (length(current_withdrawal_id) = 32 AND current_withdrawal_id <> zeroblob(32)))
    REFERENCES source_withdrawals(id) ON DELETE RESTRICT,
  last_captured_at_usec INTEGER CHECK (last_captured_at_usec IS NULL OR last_captured_at_usec > 0),
  CHECK ((collection_state = 'active' AND current_withdrawal_id IS NULL)
      OR (collection_state = 'withdrawn' AND current_withdrawal_id IS NOT NULL))
) STRICT;

CREATE TABLE lexical_scopes (
  id INTEGER PRIMARY KEY CHECK (id > 0),
  source_ids BLOB NOT NULL UNIQUE
    CHECK (length(source_ids) BETWEEN 32 AND 4096 AND length(source_ids) % 32 = 0)
) STRICT;

CREATE INDEX lexical_scopes_first_source_idx ON lexical_scopes(substr(source_ids, 1, 32));

CREATE TABLE segment_lexical_rows (
  rowid INTEGER PRIMARY KEY,
  scope_id INTEGER NOT NULL REFERENCES lexical_scopes(id) ON DELETE RESTRICT,
  segment_id BLOB NOT NULL UNIQUE
    CHECK (length(segment_id) = 32 AND segment_id <> zeroblob(32))
    REFERENCES segments(id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX segment_lexical_rows_scope_idx ON segment_lexical_rows(scope_id);

CREATE VIRTUAL TABLE segment_lexical_fts USING fts5(
  text,
  content_sha256 UNINDEXED,
  tokenize='unicode61 remove_diacritics 2'
);

CREATE TABLE trails (
    id BLOB PRIMARY KEY NOT NULL CHECK (length(id) = 32 AND id != zeroblob(32)),
    requested_at_usec INTEGER NOT NULL CHECK (requested_at_usec > 0),
    record_json BLOB NOT NULL CHECK (length(record_json) > 0)
) STRICT;

-- A query can address a source that does not exist, so source_id has no FK here.
CREATE TABLE trail_sources (
    trail_id BLOB NOT NULL REFERENCES trails(id) ON DELETE RESTRICT,
    source_id BLOB NOT NULL CHECK (length(source_id) = 32 AND source_id != zeroblob(32)),
    outcome TEXT NOT NULL CHECK (outcome IN ('allow', 'deny')),
    PRIMARY KEY (trail_id, source_id)
) STRICT;

CREATE TABLE trail_segments (
    trail_id BLOB NOT NULL REFERENCES trails(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    segment_id BLOB NOT NULL REFERENCES segments(id) ON DELETE RESTRICT,
    PRIMARY KEY (trail_id, ordinal)
) STRICT;

CREATE TABLE local_items (
  source_id BLOB NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
  item_id TEXT NOT NULL CHECK (length(item_id) > 0),
  representation_id BLOB NOT NULL REFERENCES representations(id) ON DELETE RESTRICT,
  active INTEGER NOT NULL CHECK (active IN (0, 1)),
  PRIMARY KEY (source_id, item_id)
) STRICT;

CREATE INDEX local_items_active_representation_idx
ON local_items(source_id, representation_id) WHERE active = 1;

CREATE TABLE item_changes (
  id BLOB PRIMARY KEY NOT NULL CHECK (length(id) = 32 AND id != zeroblob(32)),
  source_id BLOB NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
  record_json BLOB NOT NULL CHECK (length(record_json) > 0)
) STRICT;

-- Immutable durable notes. A note owns exactly one local item whose
-- representation holds the exact note bytes; the canonical record carries the
-- author, content digest, and query-trail lineage.
CREATE TABLE notes (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    source_id BLOB NOT NULL CHECK (typeof(source_id) = 'blob' AND length(source_id) = 32 AND source_id != zeroblob(32)),
    item_id TEXT NOT NULL CHECK (typeof(item_id) = 'text' AND length(item_id) > 0),
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0),
    UNIQUE (source_id, item_id),
    FOREIGN KEY (source_id, item_id)
        REFERENCES local_items(source_id, item_id) ON DELETE RESTRICT
) STRICT;

-- Ordered citation lineage: one row per cited segment, projected from the
-- canonical record's derived_from array.
CREATE TABLE note_citations (
    note_id BLOB NOT NULL CHECK (typeof(note_id) = 'blob' AND length(note_id) = 32 AND note_id != zeroblob(32))
        REFERENCES notes(id) ON DELETE RESTRICT,
    citation_ordinal INTEGER NOT NULL CHECK (typeof(citation_ordinal) = 'integer' AND citation_ordinal >= 0),
    segment_ordinal INTEGER NOT NULL CHECK (typeof(segment_ordinal) = 'integer' AND segment_ordinal >= 0),
    trail_id BLOB NOT NULL CHECK (typeof(trail_id) = 'blob' AND length(trail_id) = 32 AND trail_id != zeroblob(32)),
    segment_id BLOB NOT NULL CHECK (typeof(segment_id) = 'blob' AND length(segment_id) = 32 AND segment_id != zeroblob(32)),
    PRIMARY KEY (note_id, citation_ordinal, segment_ordinal)
) STRICT;

-- Served-request admissions and their single terminal outcomes. Both record
-- ids are SHA-256 of the request id; an outcome references its admission.
CREATE TABLE request_admissions (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0)
) STRICT;

CREATE TABLE request_outcomes (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32))
        REFERENCES request_admissions(id) ON DELETE RESTRICT,
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0)
) STRICT;

-- The random identity given to this store when it was created. Exactly one row.
CREATE TABLE store_identity (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 16)
) STRICT;

-- Append-only access changes. A source's current access is its latest event in log order;
-- a source with no event is denied for every caller.
CREATE TABLE access_events (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    source_id BLOB NOT NULL CHECK (typeof(source_id) = 'blob' AND length(source_id) = 32 AND source_id != zeroblob(32)) REFERENCES sources(id) ON DELETE RESTRICT,
    access TEXT NOT NULL COLLATE BINARY CHECK (access IN ('allow', 'deny')),
    occurred_at_usec INTEGER NOT NULL CHECK (typeof(occurred_at_usec) = 'integer' AND occurred_at_usec > 0),
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0)
) STRICT;

CREATE INDEX access_events_source_id_idx ON access_events(source_id);

-- The append-only store log. Each receipt, access event, withdrawal, note and trail is one leaf,
-- written in the same transaction as its record. seq runs 1, 2, 3 ... with no gaps; leaf_hash
-- is the RFC 6962 leaf hash SHA-256(0x00 || record_json) of the record named by kind and
-- record_id.
CREATE TABLE log (
    seq INTEGER PRIMARY KEY NOT NULL CHECK (seq > 0),
    kind TEXT NOT NULL COLLATE BINARY,
    record_id BLOB NOT NULL CHECK (typeof(record_id) = 'blob' AND length(record_id) = 32 AND record_id != zeroblob(32)),
    source_id BLOB CHECK (source_id IS NULL OR (length(source_id) = 32 AND source_id != zeroblob(32))) REFERENCES sources(id) ON DELETE RESTRICT,
    leaf_hash BLOB NOT NULL CHECK (typeof(leaf_hash) = 'blob' AND length(leaf_hash) = 32),
    UNIQUE (kind, record_id),
    CHECK (CASE WHEN source_id IS NULL
        THEN kind IN ('trail', 'request_admission', 'request_outcome')
        ELSE kind IN ('ingest_receipt', 'access_event', 'item_change', 'source_withdrawal', 'note')
    END)
) STRICT;

CREATE INDEX log_source_kind_seq_idx ON log(source_id, kind, seq);
CREATE INDEX log_source_seq_idx ON log(source_id, seq);

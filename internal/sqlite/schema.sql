-- The one Mousa store schema. Open sets application_id and user_version after
-- applying it; a store with any other identity or version is refused.

CREATE TABLE sources (
  id BLOB PRIMARY KEY NOT NULL
    CHECK (length(id) = 32 AND id <> zeroblob(32)),
  record_json BLOB NOT NULL CHECK (length(record_json) > 0)
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

CREATE TABLE segment_lexical_rows (
  rowid INTEGER PRIMARY KEY,
  segment_id BLOB NOT NULL UNIQUE
    CHECK (length(segment_id) = 32 AND segment_id <> zeroblob(32))
    REFERENCES segments(id) ON DELETE RESTRICT
) STRICT;

CREATE VIRTUAL TABLE segment_lexical_fts USING fts5(
  text,
  content_sha256 UNINDEXED,
  tokenize='unicode61 remove_diacritics 2'
);

CREATE TABLE policy_decisions (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    request_id BLOB NOT NULL UNIQUE CHECK (typeof(request_id) = 'blob' AND length(request_id) = 32 AND request_id != zeroblob(32)),
    caller_namespace TEXT NOT NULL COLLATE BINARY CHECK (typeof(caller_namespace) = 'text' AND length(caller_namespace) > 0),
    external_caller_id TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_caller_id) = 'text' AND length(external_caller_id) > 0),
    external_request_id TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_request_id) = 'text' AND length(external_request_id) > 0),
    source_id BLOB NOT NULL CHECK (typeof(source_id) = 'blob' AND length(source_id) = 32 AND source_id != zeroblob(32)) REFERENCES sources(id) ON DELETE RESTRICT,
    outcome TEXT NOT NULL COLLATE BINARY CHECK (outcome IN ('allow', 'deny')),
    evaluated_at_usec INTEGER NOT NULL CHECK (typeof(evaluated_at_usec) = 'integer' AND evaluated_at_usec > 0),
    current_withdrawal_id BLOB CHECK (current_withdrawal_id IS NULL OR (typeof(current_withdrawal_id) = 'blob' AND length(current_withdrawal_id) = 32 AND current_withdrawal_id != zeroblob(32))) REFERENCES source_withdrawals(id) ON DELETE RESTRICT,
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0),
    UNIQUE (caller_namespace, external_caller_id, external_request_id)
) STRICT;

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

CREATE TABLE local_items (
  source_id BLOB NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
  item_id TEXT NOT NULL CHECK (length(item_id) > 0),
  representation_id BLOB NOT NULL REFERENCES representations(id) ON DELETE RESTRICT,
  active INTEGER NOT NULL CHECK (active IN (0, 1)),
  PRIMARY KEY (source_id, item_id)
) STRICT;

CREATE INDEX local_items_active_representation_idx
ON local_items(source_id, representation_id) WHERE active = 1;

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

-- One access decision per source. A source without a row is allowed.
CREATE TABLE source_access (
    source_id BLOB PRIMARY KEY NOT NULL CHECK (typeof(source_id) = 'blob' AND length(source_id) = 32 AND source_id != zeroblob(32)) REFERENCES sources(id) ON DELETE RESTRICT,
    access TEXT NOT NULL COLLATE BINARY CHECK (access IN ('allow', 'deny'))
) STRICT;

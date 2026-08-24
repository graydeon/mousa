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

PRAGMA application_id = 1297044819;

CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY CHECK (version > 0),
  name TEXT NOT NULL UNIQUE CHECK (length(name) > 0),
  sha256 BLOB NOT NULL UNIQUE CHECK (length(sha256) = 32)
) STRICT;

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

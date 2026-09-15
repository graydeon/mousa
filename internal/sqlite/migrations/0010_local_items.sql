CREATE TABLE local_items (
  source_id BLOB NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
  item_id TEXT NOT NULL CHECK (length(item_id) > 0),
  representation_id BLOB NOT NULL REFERENCES representations(id) ON DELETE RESTRICT,
  active INTEGER NOT NULL CHECK (active IN (0, 1)),
  PRIMARY KEY (source_id, item_id)
) STRICT;

CREATE TABLE local_recovery_sources (
  source_id BLOB PRIMARY KEY NOT NULL REFERENCES sources(id) ON DELETE RESTRICT
) STRICT;

-- Old directory stores did not record current activation. Preserve their
-- canonical history, but require source replay instead of guessing chronology.
INSERT INTO local_recovery_sources(source_id)
SELECT id FROM sources WHERE json_extract(CAST(record_json AS TEXT), '$.namespace') = 'mousa-local';

DELETE FROM segment_lexical_fts WHERE rowid IN (
  SELECT lr.rowid FROM segment_lexical_rows AS lr
  JOIN segments AS s ON s.id = lr.segment_id
  JOIN representation_inputs AS ri ON ri.representation_id = s.representation_id
  JOIN artifacts AS a ON a.id = ri.artifact_id
  JOIN observations AS o ON o.id = a.observation_id
  JOIN local_recovery_sources AS r ON r.source_id = o.source_id
);
DELETE FROM segment_lexical_rows WHERE rowid IN (
  SELECT lr.rowid FROM segment_lexical_rows AS lr
  JOIN segments AS s ON s.id = lr.segment_id
  JOIN representation_inputs AS ri ON ri.representation_id = s.representation_id
  JOIN artifacts AS a ON a.id = ri.artifact_id
  JOIN observations AS o ON o.id = a.observation_id
  JOIN local_recovery_sources AS r ON r.source_id = o.source_id
);

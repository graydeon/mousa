-- One logged path move: an immutable canonical record hashed as its log leaf.
-- The current location itself is local_items.path; this table is the history.
CREATE TABLE path_moves (
  id BLOB PRIMARY KEY NOT NULL CHECK (length(id) = 32 AND id != zeroblob(32)),
  source_id BLOB NOT NULL REFERENCES sources(id) ON DELETE RESTRICT,
  record_json BLOB NOT NULL CHECK (length(record_json) > 0)
) STRICT;

-- Immutable same-source replacement edges, projected from each note
-- record's supersedes list when the note is stored. The canonical note is
-- authoritative; these rows only accelerate lookups and are verified back
-- against it.
CREATE TABLE note_supersessions (
  target_id BLOB NOT NULL REFERENCES notes(id) ON DELETE RESTRICT,
  replacement_id BLOB NOT NULL REFERENCES notes(id) ON DELETE RESTRICT,
  PRIMARY KEY (target_id, replacement_id),
  CHECK (target_id != replacement_id)
) STRICT;
CREATE INDEX note_supersessions_replacement_idx ON note_supersessions(replacement_id, target_id);

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

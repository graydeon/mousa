-- One access decision per source. A source without a row is allowed.
CREATE TABLE source_access (
    source_id BLOB PRIMARY KEY NOT NULL CHECK (typeof(source_id) = 'blob' AND length(source_id) = 32 AND source_id != zeroblob(32)) REFERENCES sources(id) ON DELETE RESTRICT,
    access TEXT NOT NULL COLLATE BINARY CHECK (access IN ('allow', 'deny'))
) STRICT;

CREATE TABLE callers (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    namespace TEXT NOT NULL COLLATE BINARY CHECK (typeof(namespace) = 'text' AND length(namespace) > 0),
    external_caller_id TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_caller_id) = 'text' AND length(external_caller_id) > 0),
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0),
    UNIQUE (namespace, external_caller_id)
) STRICT;

CREATE TABLE purposes (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    namespace TEXT NOT NULL COLLATE BINARY CHECK (typeof(namespace) = 'text' AND length(namespace) > 0),
    external_purpose_id TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_purpose_id) = 'text' AND length(external_purpose_id) > 0),
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0),
    UNIQUE (namespace, external_purpose_id)
) STRICT;

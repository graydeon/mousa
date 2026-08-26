CREATE TABLE policy_definitions (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    namespace TEXT NOT NULL COLLATE BINARY CHECK (typeof(namespace) = 'text' AND length(namespace) > 0),
    external_policy_id TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_policy_id) = 'text' AND length(external_policy_id) > 0),
    external_policy_version TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_policy_version) = 'text' AND length(external_policy_version) > 0),
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0),
    UNIQUE (namespace, external_policy_id, external_policy_version)
) STRICT;

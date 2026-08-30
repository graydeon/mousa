CREATE TABLE policy_bindings (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    namespace TEXT NOT NULL COLLATE BINARY CHECK (typeof(namespace) = 'text' AND length(namespace) > 0),
    external_binding_id TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_binding_id) = 'text' AND length(external_binding_id) > 0),
    external_binding_version TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_binding_version) = 'text' AND length(external_binding_version) > 0),
    scope_kind TEXT NOT NULL COLLATE BINARY CHECK (scope_kind IN ('deployment', 'source', 'observation', 'artifact', 'representation', 'segment')),
    subject_id BLOB CHECK ((scope_kind = 'deployment' AND subject_id IS NULL) OR (scope_kind != 'deployment' AND typeof(subject_id) = 'blob' AND length(subject_id) = 32 AND subject_id != zeroblob(32))),
    policy_definition_id BLOB NOT NULL CHECK (typeof(policy_definition_id) = 'blob' AND length(policy_definition_id) = 32 AND policy_definition_id != zeroblob(32)) REFERENCES policy_definitions(id),
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0),
    UNIQUE (namespace, external_binding_id, external_binding_version),
    UNIQUE (id, namespace, external_binding_id)
) STRICT;

CREATE INDEX policy_bindings_definition_id_idx ON policy_bindings(policy_definition_id);
CREATE INDEX policy_bindings_scope_idx ON policy_bindings(scope_kind, subject_id);

CREATE TABLE policy_activations (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    namespace TEXT NOT NULL COLLATE BINARY CHECK (typeof(namespace) = 'text' AND length(namespace) > 0),
    external_binding_id TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_binding_id) = 'text' AND length(external_binding_id) > 0),
    external_activation_id TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_activation_id) = 'text' AND length(external_activation_id) > 0),
    expected_previous_activation_id BLOB CHECK (expected_previous_activation_id IS NULL OR (typeof(expected_previous_activation_id) = 'blob' AND length(expected_previous_activation_id) = 32 AND expected_previous_activation_id != zeroblob(32))),
    active_binding_id BLOB CHECK (active_binding_id IS NULL OR (typeof(active_binding_id) = 'blob' AND length(active_binding_id) = 32 AND active_binding_id != zeroblob(32))),
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0),
    UNIQUE (namespace, external_binding_id, external_activation_id),
    UNIQUE (id, namespace, external_binding_id),
    FOREIGN KEY (expected_previous_activation_id, namespace, external_binding_id) REFERENCES policy_activations(id, namespace, external_binding_id),
    FOREIGN KEY (active_binding_id, namespace, external_binding_id) REFERENCES policy_bindings(id, namespace, external_binding_id)
) STRICT;

CREATE UNIQUE INDEX policy_activations_root_idx ON policy_activations(namespace, external_binding_id) WHERE expected_previous_activation_id IS NULL;
CREATE UNIQUE INDEX policy_activations_predecessor_idx ON policy_activations(expected_previous_activation_id) WHERE expected_previous_activation_id IS NOT NULL;
CREATE INDEX policy_activations_series_idx ON policy_activations(namespace, external_binding_id);
CREATE INDEX policy_activations_active_binding_id_idx ON policy_activations(active_binding_id) WHERE active_binding_id IS NOT NULL;

CREATE TABLE policy_binding_state (
    namespace TEXT NOT NULL COLLATE BINARY CHECK (typeof(namespace) = 'text' AND length(namespace) > 0),
    external_binding_id TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_binding_id) = 'text' AND length(external_binding_id) > 0),
    current_activation_id BLOB NOT NULL CHECK (typeof(current_activation_id) = 'blob' AND length(current_activation_id) = 32 AND current_activation_id != zeroblob(32)),
    active_binding_id BLOB CHECK (active_binding_id IS NULL OR (typeof(active_binding_id) = 'blob' AND length(active_binding_id) = 32 AND active_binding_id != zeroblob(32))),
    PRIMARY KEY (namespace, external_binding_id),
    FOREIGN KEY (current_activation_id, namespace, external_binding_id) REFERENCES policy_activations(id, namespace, external_binding_id),
    FOREIGN KEY (active_binding_id, namespace, external_binding_id) REFERENCES policy_bindings(id, namespace, external_binding_id)
) STRICT;
CREATE INDEX policy_binding_state_active_binding_id_idx ON policy_binding_state(active_binding_id) WHERE active_binding_id IS NOT NULL;

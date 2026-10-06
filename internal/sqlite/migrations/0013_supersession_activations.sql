-- The declaration side of the same-source references below needs a composite parent key that the
-- declaration table does not carry yet.
CREATE UNIQUE INDEX supersession_declarations_id_source_idx ON supersession_declarations(id, source_id);

-- Supersession activation history with one root and no forks per source. The event rows project the
-- canonical transition and have no UPDATE or DELETE path. Composite foreign keys carry the source,
-- so a transition can only name a declaration and a predecessor event of the same source. Version 1
-- keeps at most one active declaration per source: bounded recorded administration state, not an
-- authorization decision, a relation set or a query filter.
CREATE TABLE supersession_activations (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    source_id BLOB NOT NULL CHECK (typeof(source_id) = 'blob' AND length(source_id) = 32 AND source_id != zeroblob(32)) REFERENCES sources(id) ON DELETE RESTRICT,
    expected_previous_activation_id BLOB CHECK (expected_previous_activation_id IS NULL OR (typeof(expected_previous_activation_id) = 'blob' AND length(expected_previous_activation_id) = 32 AND expected_previous_activation_id != zeroblob(32))),
    declaration_id BLOB CHECK (declaration_id IS NULL OR (typeof(declaration_id) = 'blob' AND length(declaration_id) = 32 AND declaration_id != zeroblob(32))),
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) BETWEEN 1 AND 65536),
    UNIQUE (id, source_id),
    -- A root transition selects a declaration; a source with no history cannot be deactivated.
    CHECK (expected_previous_activation_id IS NOT NULL OR declaration_id IS NOT NULL),
    FOREIGN KEY (expected_previous_activation_id, source_id) REFERENCES supersession_activations(id, source_id),
    FOREIGN KEY (declaration_id, source_id) REFERENCES supersession_declarations(id, source_id)
) STRICT;

-- A source has at most one root, and a predecessor has at most one successor.
CREATE UNIQUE INDEX supersession_activations_root_idx ON supersession_activations(source_id) WHERE expected_previous_activation_id IS NULL;
CREATE UNIQUE INDEX supersession_activations_predecessor_idx ON supersession_activations(expected_previous_activation_id) WHERE expected_previous_activation_id IS NOT NULL;

-- The current projection names the event in effect; a null declaration means the source has no
-- active declaration.
CREATE TABLE supersession_activation_state (
    source_id BLOB PRIMARY KEY NOT NULL CHECK (typeof(source_id) = 'blob' AND length(source_id) = 32 AND source_id != zeroblob(32)) REFERENCES sources(id) ON DELETE RESTRICT,
    current_activation_id BLOB NOT NULL CHECK (typeof(current_activation_id) = 'blob' AND length(current_activation_id) = 32 AND current_activation_id != zeroblob(32)),
    active_declaration_id BLOB CHECK (active_declaration_id IS NULL OR (typeof(active_declaration_id) = 'blob' AND length(active_declaration_id) = 32 AND active_declaration_id != zeroblob(32))),
    FOREIGN KEY (current_activation_id, source_id) REFERENCES supersession_activations(id, source_id),
    FOREIGN KEY (active_declaration_id, source_id) REFERENCES supersession_declarations(id, source_id)
) STRICT;

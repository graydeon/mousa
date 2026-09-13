CREATE TABLE policy_decisions (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    request_id BLOB NOT NULL UNIQUE CHECK (typeof(request_id) = 'blob' AND length(request_id) = 32 AND request_id != zeroblob(32)),
    caller_namespace TEXT NOT NULL COLLATE BINARY CHECK (typeof(caller_namespace) = 'text' AND length(caller_namespace) > 0),
    external_caller_id TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_caller_id) = 'text' AND length(external_caller_id) > 0),
    external_request_id TEXT NOT NULL COLLATE BINARY CHECK (typeof(external_request_id) = 'text' AND length(external_request_id) > 0),
    source_id BLOB NOT NULL CHECK (typeof(source_id) = 'blob' AND length(source_id) = 32 AND source_id != zeroblob(32)) REFERENCES sources(id) ON DELETE RESTRICT,
    outcome TEXT NOT NULL COLLATE BINARY CHECK (outcome IN ('allow', 'deny')),
    evaluated_at_usec INTEGER NOT NULL CHECK (typeof(evaluated_at_usec) = 'integer' AND evaluated_at_usec > 0),
    current_withdrawal_id BLOB CHECK (current_withdrawal_id IS NULL OR (typeof(current_withdrawal_id) = 'blob' AND length(current_withdrawal_id) = 32 AND current_withdrawal_id != zeroblob(32))) REFERENCES source_withdrawals(id) ON DELETE RESTRICT,
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0),
    UNIQUE (caller_namespace, external_caller_id, external_request_id)
) STRICT;

CREATE TABLE policy_decision_inputs (
    decision_id BLOB NOT NULL CHECK (typeof(decision_id) = 'blob' AND length(decision_id) = 32 AND decision_id != zeroblob(32)) REFERENCES policy_decisions(id) ON DELETE RESTRICT,
    ordinal INTEGER NOT NULL CHECK (typeof(ordinal) = 'integer' AND ordinal >= 0),
    layer TEXT NOT NULL COLLATE BINARY CHECK (layer IN ('deployment', 'source')),
    activation_id BLOB NOT NULL CHECK (typeof(activation_id) = 'blob' AND length(activation_id) = 32 AND activation_id != zeroblob(32)) REFERENCES policy_activations(id) ON DELETE RESTRICT,
    binding_id BLOB NOT NULL CHECK (typeof(binding_id) = 'blob' AND length(binding_id) = 32 AND binding_id != zeroblob(32)) REFERENCES policy_bindings(id) ON DELETE RESTRICT,
    definition_id BLOB NOT NULL CHECK (typeof(definition_id) = 'blob' AND length(definition_id) = 32 AND definition_id != zeroblob(32)) REFERENCES policy_definitions(id) ON DELETE RESTRICT,
    result TEXT NOT NULL COLLATE BINARY CHECK (result IN ('allow', 'deny', 'request_mismatch', 'unsupported_definition', 'malformed_definition')),
    PRIMARY KEY (decision_id, ordinal)
) STRICT;

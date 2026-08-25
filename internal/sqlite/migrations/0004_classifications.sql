CREATE TABLE classifications (
    id BLOB PRIMARY KEY NOT NULL CHECK (typeof(id) = 'blob' AND length(id) = 32 AND id != zeroblob(32)),
    subject_kind TEXT NOT NULL CHECK (subject_kind IN ('source', 'observation', 'artifact', 'representation', 'segment')),
    subject_source_id BLOB REFERENCES sources(id) ON DELETE RESTRICT CHECK (subject_source_id IS NULL OR (typeof(subject_source_id) = 'blob' AND length(subject_source_id) = 32 AND subject_source_id != zeroblob(32))),
    subject_observation_id BLOB REFERENCES observations(id) ON DELETE RESTRICT CHECK (subject_observation_id IS NULL OR (typeof(subject_observation_id) = 'blob' AND length(subject_observation_id) = 32 AND subject_observation_id != zeroblob(32))),
    subject_artifact_id BLOB REFERENCES artifacts(id) ON DELETE RESTRICT CHECK (subject_artifact_id IS NULL OR (typeof(subject_artifact_id) = 'blob' AND length(subject_artifact_id) = 32 AND subject_artifact_id != zeroblob(32))),
    subject_representation_id BLOB REFERENCES representations(id) ON DELETE RESTRICT CHECK (subject_representation_id IS NULL OR (typeof(subject_representation_id) = 'blob' AND length(subject_representation_id) = 32 AND subject_representation_id != zeroblob(32))),
    subject_segment_id BLOB REFERENCES segments(id) ON DELETE RESTRICT CHECK (subject_segment_id IS NULL OR (typeof(subject_segment_id) = 'blob' AND length(subject_segment_id) = 32 AND subject_segment_id != zeroblob(32))),
    record_json BLOB NOT NULL CHECK (typeof(record_json) = 'blob' AND length(record_json) > 0),
    CHECK (
        (subject_kind = 'source' AND subject_source_id IS NOT NULL AND subject_observation_id IS NULL AND subject_artifact_id IS NULL AND subject_representation_id IS NULL AND subject_segment_id IS NULL) OR
        (subject_kind = 'observation' AND subject_source_id IS NULL AND subject_observation_id IS NOT NULL AND subject_artifact_id IS NULL AND subject_representation_id IS NULL AND subject_segment_id IS NULL) OR
        (subject_kind = 'artifact' AND subject_source_id IS NULL AND subject_observation_id IS NULL AND subject_artifact_id IS NOT NULL AND subject_representation_id IS NULL AND subject_segment_id IS NULL) OR
        (subject_kind = 'representation' AND subject_source_id IS NULL AND subject_observation_id IS NULL AND subject_artifact_id IS NULL AND subject_representation_id IS NOT NULL AND subject_segment_id IS NULL) OR
        (subject_kind = 'segment' AND subject_source_id IS NULL AND subject_observation_id IS NULL AND subject_artifact_id IS NULL AND subject_representation_id IS NULL AND subject_segment_id IS NOT NULL)
    )
) STRICT;

CREATE INDEX classifications_subject_source_id_idx ON classifications(subject_source_id) WHERE subject_source_id IS NOT NULL;
CREATE INDEX classifications_subject_observation_id_idx ON classifications(subject_observation_id) WHERE subject_observation_id IS NOT NULL;
CREATE INDEX classifications_subject_artifact_id_idx ON classifications(subject_artifact_id) WHERE subject_artifact_id IS NOT NULL;
CREATE INDEX classifications_subject_representation_id_idx ON classifications(subject_representation_id) WHERE subject_representation_id IS NOT NULL;
CREATE INDEX classifications_subject_segment_id_idx ON classifications(subject_segment_id) WHERE subject_segment_id IS NOT NULL;

CREATE TABLE classification_bases (
    classification_id BLOB NOT NULL REFERENCES classifications(id) ON DELETE RESTRICT CHECK (typeof(classification_id) = 'blob' AND length(classification_id) = 32 AND classification_id != zeroblob(32)),
    ordinal INTEGER NOT NULL CHECK (ordinal >= 0),
    basis_kind TEXT NOT NULL CHECK (basis_kind IN ('source', 'observation', 'artifact', 'representation', 'segment')),
    basis_source_id BLOB REFERENCES sources(id) ON DELETE RESTRICT CHECK (basis_source_id IS NULL OR (typeof(basis_source_id) = 'blob' AND length(basis_source_id) = 32 AND basis_source_id != zeroblob(32))),
    basis_observation_id BLOB REFERENCES observations(id) ON DELETE RESTRICT CHECK (basis_observation_id IS NULL OR (typeof(basis_observation_id) = 'blob' AND length(basis_observation_id) = 32 AND basis_observation_id != zeroblob(32))),
    basis_artifact_id BLOB REFERENCES artifacts(id) ON DELETE RESTRICT CHECK (basis_artifact_id IS NULL OR (typeof(basis_artifact_id) = 'blob' AND length(basis_artifact_id) = 32 AND basis_artifact_id != zeroblob(32))),
    basis_representation_id BLOB REFERENCES representations(id) ON DELETE RESTRICT CHECK (basis_representation_id IS NULL OR (typeof(basis_representation_id) = 'blob' AND length(basis_representation_id) = 32 AND basis_representation_id != zeroblob(32))),
    basis_segment_id BLOB REFERENCES segments(id) ON DELETE RESTRICT CHECK (basis_segment_id IS NULL OR (typeof(basis_segment_id) = 'blob' AND length(basis_segment_id) = 32 AND basis_segment_id != zeroblob(32))),
    PRIMARY KEY (classification_id, ordinal),
    CHECK (
        (basis_kind = 'source' AND basis_source_id IS NOT NULL AND basis_observation_id IS NULL AND basis_artifact_id IS NULL AND basis_representation_id IS NULL AND basis_segment_id IS NULL) OR
        (basis_kind = 'observation' AND basis_source_id IS NULL AND basis_observation_id IS NOT NULL AND basis_artifact_id IS NULL AND basis_representation_id IS NULL AND basis_segment_id IS NULL) OR
        (basis_kind = 'artifact' AND basis_source_id IS NULL AND basis_observation_id IS NULL AND basis_artifact_id IS NOT NULL AND basis_representation_id IS NULL AND basis_segment_id IS NULL) OR
        (basis_kind = 'representation' AND basis_source_id IS NULL AND basis_observation_id IS NULL AND basis_artifact_id IS NULL AND basis_representation_id IS NOT NULL AND basis_segment_id IS NULL) OR
        (basis_kind = 'segment' AND basis_source_id IS NULL AND basis_observation_id IS NULL AND basis_artifact_id IS NULL AND basis_representation_id IS NULL AND basis_segment_id IS NOT NULL)
    )
) STRICT;

CREATE INDEX classification_bases_source_id_idx ON classification_bases(basis_source_id) WHERE basis_source_id IS NOT NULL;
CREATE INDEX classification_bases_observation_id_idx ON classification_bases(basis_observation_id) WHERE basis_observation_id IS NOT NULL;
CREATE INDEX classification_bases_artifact_id_idx ON classification_bases(basis_artifact_id) WHERE basis_artifact_id IS NOT NULL;
CREATE INDEX classification_bases_representation_id_idx ON classification_bases(basis_representation_id) WHERE basis_representation_id IS NOT NULL;
CREATE INDEX classification_bases_segment_id_idx ON classification_bases(basis_segment_id) WHERE basis_segment_id IS NOT NULL;

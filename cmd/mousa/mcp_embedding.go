package main

import (
	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
)

type mcpVectorInputsResult struct {
	Schema          string                       `json:"schema"`
	DecisionID      mousa.PolicyDecisionID       `json:"decision_id"`
	DecisionOutcome mousa.PolicyDecisionOutcome  `json:"decision_outcome"`
	DecisionReasons []mousa.PolicyDecisionReason `json:"decision_reasons"`
	Passages        []sqlite.VectorPassage       `json:"passages"`
}

type mcpVectorImportResult struct {
	Schema   string       `json:"schema"`
	SpaceID  mousa.SHA256 `json:"space_id"`
	Imported int          `json:"imported"`
}

func mcpWriteTool(name string) bool {
	return name == "mousa_sync" || name == "mousa_vector_import"
}

func mcpVectorImportSchema() map[string]any {
	hex := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$"}
	descriptor := map[string]any{"type": "string", "minLength": 1, "maxLength": 256}
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"schema", "space", "embeddings"},
		"properties": map[string]any{
			"schema": map[string]any{"const": mousa.VectorImportSchema},
			"space": map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"schema", "provider", "model", "version", "dimensions", "metric"},
				"properties": map[string]any{
					"schema": map[string]any{"const": mousa.VectorSpaceSchema}, "provider": descriptor, "model": descriptor, "version": descriptor,
					"dimensions": map[string]any{"type": "integer", "minimum": 1, "maximum": mousa.MaxVectorDimensions}, "metric": map[string]any{"const": "cosine"},
				},
			},
			"embeddings": map[string]any{
				"type": "array", "minItems": 1, "maxItems": mousa.MaxVectorPassages,
				"items": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"source_id", "segment_id", "representation_id", "representation_sha256", "content_sha256", "values"},
					"properties": map[string]any{
						"source_id": hex, "segment_id": hex, "representation_id": hex, "representation_sha256": hex, "content_sha256": hex,
						"values": map[string]any{"type": "array", "minItems": 1, "maxItems": mousa.MaxVectorDimensions, "items": map[string]any{"type": "number"}},
					},
				},
			},
		},
	}
}

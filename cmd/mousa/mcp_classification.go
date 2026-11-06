package main

import (
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/graydeon/mousa/internal/mousa"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpClassificationPutResult struct {
	Classification mousa.Classification `json:"classification"`
}

type mcpClassificationGetResult struct {
	DecisionID      mousa.PolicyDecisionID       `json:"decision_id"`
	DecisionOutcome mousa.PolicyDecisionOutcome  `json:"decision_outcome"`
	DecisionReasons []mousa.PolicyDecisionReason `json:"decision_reasons"`
	Classification  *mousa.Classification        `json:"classification,omitempty"`
}

func mcpClassificationSubjectSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: "object", AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}, Required: []string{"kind", "id"}, Properties: map[string]*jsonschema.Schema{
		"kind": {Type: "string", Enum: []any{"source", "observation", "artifact", "representation", "segment"}},
		"id":   {Type: "string", Pattern: "^[0-9a-f]{64}$", Not: &jsonschema.Schema{Enum: []any{"0000000000000000000000000000000000000000000000000000000000000000"}}},
	}}
}

func mcpClassificationToolDefinitions(source map[string]any) ([]*mcp.Tool, error) {
	id := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "not": map[string]any{"const": "0000000000000000000000000000000000000000000000000000000000000000"}}
	subject := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind", "id"}, "properties": map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{"source", "observation", "artifact", "representation", "segment"}}, "id": id,
	}}
	properties := map[string]any{
		"schema":           map[string]any{"type": "string", "const": mousa.ClassificationInputSchema},
		"subject":          subject,
		"basis":            map[string]any{"type": "array", "minItems": 1, "maxItems": 4096, "uniqueItems": true, "items": subject},
		"asserted_at_usec": map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9223372036854775807)},
		"confidence_ppm":   map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000},
	}
	for _, field := range []string{"taxonomy", "taxonomy_version", "label", "asserter_id", "asserter_version"} {
		properties[field] = map[string]any{"type": "string", "minLength": 1}
	}
	draft := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"schema", "subject", "taxonomy", "taxonomy_version", "label", "asserter_id", "asserter_version", "basis", "asserted_at_usec", "confidence_ppm"}, "properties": properties,
		"description": "Native classification_input.v1 draft, at most 1048576 encoded bytes including whitespace; canonical record byte bounds also apply. Every field is explicit and non-null, including confidence_ppm zero. Basis order is significant and typed entries cannot repeat. Subject and every basis entry must have all canonical ancestry roots in this source. Attribution and confidence are caller assertions, not authenticated identity, factual truth, calibration or an access grant."}
	put, err := mcpOutputSchema[mcpClassificationPutResult]("mousa_classification_put")
	if err != nil {
		return nil, err
	}
	get, err := mcpOutputSchema[mcpClassificationGetResult]("mousa_classification_get")
	if err != nil {
		return nil, err
	}
	input := func(field string, value map[string]any) map[string]any {
		return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"source", field}, "properties": map[string]any{"source": source, field: value}}
	}
	return []*mcp.Tool{
		{Name: "mousa_classification_put", Description: "Append an immutable caller-attributed classification for one configured JSONL source. Requires explicit --classification-admin-source and HTTP mousa:write, not ingestion permission or retrieval authorization. Returns the verified canonical record; exact retries add no row. Native cross-source assertions are outside this MCP slice. Labels never change retrieval, ranking, packing or authorization.", InputSchema: input("classification", draft), OutputSchema: put},
		{Name: "mousa_classification_get", Description: "Fetch one same-source canonical classification under fresh retrieval authorization in one snapshot, without administration permission. Denial omits classification entirely, including existence or corruption details. Foreign, mixed-source and unprovable scope return not_found. Authorization may append a decision receipt. No enumeration or filtering is provided.", InputSchema: input("classification_id", id), OutputSchema: get},
	}, nil
}

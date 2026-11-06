package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	mcpSupersessionDeclarationInputSchema = "mousa.supersession_declaration_input.v1"
	mcpSupersessionActivationInputSchema  = "mousa.supersession_activation_input.v1"
)

var mcpDeclarationDraftFields = []string{"schema", "predecessor_item_id", "predecessor_representation_id", "successor_item_id", "successor_representation_id", "author", "basis"}
var mcpActivationDraftFields = []string{"schema", "expected_previous_activation_id", "declaration_id", "actor_id", "actor_version", "occurred_at_usec", "reason"}

type mcpSupersessionDeclareResult struct {
	Declaration mousa.SupersessionDeclaration `json:"declaration"`
}

type mcpSupersessionActivateResult struct {
	Activation mousa.SupersessionActivation `json:"activation"`
}

type mcpSupersessionStatusResult struct {
	DecisionID      mousa.PolicyDecisionID       `json:"decision_id"`
	DecisionOutcome mousa.PolicyDecisionOutcome  `json:"decision_outcome"`
	DecisionReasons []mousa.PolicyDecisionReason `json:"decision_reasons"`
	Current         *sqlite.SupersessionCurrent  `json:"current,omitempty"`
}

func decodeMCPSupersessionDraft(raw []byte, fields, nullable []string, schema string, limit int, target any) error {
	if len(raw) > limit || !utf8.Valid(raw) {
		return errors.New("supersession draft exceeds its encoded byte limit or is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return errors.New("supersession draft must be an object")
	}
	seen := make(map[string]bool, len(fields))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || !slices.Contains(fields, key) || seen[key] {
			return errors.New("unknown or duplicate supersession draft field")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		if bytes.Equal(value, []byte("null")) {
			if !slices.Contains(nullable, key) {
				return fmt.Errorf("%s must not be null", key)
			}
			continue
		}
		if key == "occurred_at_usec" {
			var timestamp int64
			if err := json.Unmarshal(value, &timestamp); err != nil || timestamp <= 0 {
				return errors.New("occurred_at_usec must be a positive int64 integer")
			}
			continue
		}
		text, err := decodeItemString(value)
		if err != nil {
			return fmt.Errorf("%s must be a lossless JSON string", key)
		}
		if key == "schema" && text != schema {
			return errors.New("unsupported supersession draft schema")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("supersession draft contains trailing data")
	}
	for _, key := range fields {
		if !seen[key] {
			return fmt.Errorf("missing supersession draft field %s", key)
		}
	}
	return json.Unmarshal(raw, target)
}

func decodeMCPSupersessionDeclaration(raw []byte, source mousa.SourceID) (mousa.SupersessionDeclaration, error) {
	var declaration mousa.SupersessionDeclaration
	if err := decodeMCPSupersessionDraft(raw, mcpDeclarationDraftFields, nil, mcpSupersessionDeclarationInputSchema, mousa.MaxSupersessionDeclarationBytes, &declaration); err != nil {
		return declaration, err
	}
	declaration.Schema, declaration.SourceID = mousa.SupersessionDeclarationSchema, source
	id, err := mousa.NewSupersessionDeclarationID(source, declaration.PredecessorItemID, declaration.PredecessorRepresentationID, declaration.SuccessorItemID, declaration.SuccessorRepresentationID, declaration.Author, declaration.Basis)
	if err != nil {
		return declaration, err
	}
	declaration.ID = id
	return declaration, declaration.Validate()
}

func decodeMCPSupersessionActivation(raw []byte, source mousa.SourceID) (mousa.SupersessionActivation, error) {
	var activation mousa.SupersessionActivation
	if err := decodeMCPSupersessionDraft(raw, mcpActivationDraftFields, []string{"expected_previous_activation_id", "declaration_id", "reason"}, mcpSupersessionActivationInputSchema, mousa.MaxSupersessionActivationBytes, &activation); err != nil {
		return activation, err
	}
	activation.Schema, activation.SourceID = mousa.SupersessionActivationSchema, source
	id, err := mousa.NewSupersessionActivationID(source, activation.ExpectedPreviousActivationID, activation.DeclarationID)
	if err != nil {
		return activation, err
	}
	activation.ID = id
	return activation, activation.Validate()
}

func mcpSupersessionToolDefinitions(source map[string]any) ([]*mcp.Tool, error) {
	label := func(limit int) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": limit, "pattern": "^[^\x00]+$"}
	}
	id := map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "not": map[string]any{"const": "0000000000000000000000000000000000000000000000000000000000000000"}}
	nullable := func(value map[string]any) map[string]any {
		return map[string]any{"anyOf": []any{value, map[string]any{"type": "null"}}}
	}
	declaration := map[string]any{
		"type": "object", "additionalProperties": false, "required": mcpDeclarationDraftFields,
		"description": "Closed draft, at most 65536 encoded bytes. Label limits also apply in UTF-8 bytes; NUL is forbidden. Predecessor and successor items and representation pins must differ. Author and basis are caller assertions, not authenticated authorship or factual truth.",
		"properties": map[string]any{
			"schema":              map[string]any{"type": "string", "const": mcpSupersessionDeclarationInputSchema},
			"predecessor_item_id": label(mousa.MaxSupersessionItemBytes), "successor_item_id": label(mousa.MaxSupersessionItemBytes),
			"predecessor_representation_id": id, "successor_representation_id": id,
			"author": label(mousa.MaxSupersessionAuthorBytes), "basis": label(mousa.MaxSupersessionBasisBytes),
		},
	}
	activation := map[string]any{
		"type": "object", "additionalProperties": false, "required": mcpActivationDraftFields,
		"description": "Closed draft, at most 65536 encoded bytes. Label limits also apply in UTF-8 bytes; NUL is forbidden. Nullable fields must be explicit. An initial transition must select a declaration. Actor, timestamp and reason are caller assertions, not the authenticated principal; no timestamp or expected predecessor is inferred.",
		"properties": map[string]any{
			"schema":                          map[string]any{"type": "string", "const": mcpSupersessionActivationInputSchema},
			"expected_previous_activation_id": nullable(id), "declaration_id": nullable(id),
			"actor_id": label(mousa.MaxSupersessionActorBytes), "actor_version": label(mousa.MaxSupersessionActorBytes),
			"occurred_at_usec": map[string]any{"type": "integer", "minimum": 1, "maximum": int64(9223372036854775807)},
			"reason":           nullable(label(mousa.MaxSupersessionReasonBytes)),
		},
		"anyOf": []any{
			map[string]any{"properties": map[string]any{"expected_previous_activation_id": id}},
			map[string]any{"properties": map[string]any{"declaration_id": id}},
		},
	}
	declared, err := mcpOutputSchema[mcpSupersessionDeclareResult]("mousa_supersession_declare")
	if err != nil {
		return nil, err
	}
	activated, err := mcpOutputSchema[mcpSupersessionActivateResult]("mousa_supersession_activate")
	if err != nil {
		return nil, err
	}
	status, err := mcpOutputSchema[mcpSupersessionStatusResult]("mousa_supersession_status")
	if err != nil {
		return nil, err
	}
	// Inline this transport view so its required null members and states are explicit.
	current := &jsonschema.Schema{Type: "object", AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}, Required: []string{"state", "activation", "declaration"}, Properties: map[string]*jsonschema.Schema{
		"state":       {Type: "string", Enum: []any{"no_history", "active", "deactivated"}},
		"activation":  {AnyOf: []*jsonschema.Schema{activated.Properties["result"].Properties["activation"], {Type: "null"}}},
		"declaration": {AnyOf: []*jsonschema.Schema{declared.Properties["result"].Properties["declaration"], {Type: "null"}}},
	}}
	status.Properties["result"].Properties["current"] = current
	input := func(field string, draft map[string]any) map[string]any {
		properties := map[string]any{"source": source}
		required := []string{"source"}
		if field != "" {
			properties[field] = draft
			required = append(required, field)
		}
		return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": properties}
	}
	return []*mcp.Tool{
		{Name: "mousa_supersession_declare", Description: "Record an immutable exact-revision replacement declaration for a configured JSONL source. Requires its explicit supersession administration startup allowlist and HTTP mousa:write, not ingestion permission or retrieval authorization. Returns the accepted canonical record; exact retries add no row. Does not activate it or establish semantic truth.", InputSchema: input("declaration", declaration), OutputSchema: declared},
		{Name: "mousa_supersession_activate", Description: "Explicitly select a declaration or deactivate with declaration_id null using the exact expected current activation. Requires the source's supersession administration allowlist and HTTP mousa:write. Returns the accepted canonical event. Exact retry succeeds only while that event remains current; stale expectations and metadata changes conflict. No evidence text or unsupplied current state is released.", InputSchema: input("activation", activation), OutputSchema: activated},
		{Name: "mousa_supersession_status", Description: "Inspect verified current supersession state under fresh retrieval authorization in one snapshot. Requires neither administration allowlist nor write scope. Allowed current includes state and explicit nullable canonical activation/declaration records; denial omits current entirely. Authorization may append a decision receipt. Historical record-by-ID reads remain native-only.", InputSchema: input("", nil), OutputSchema: status},
	}, nil
}

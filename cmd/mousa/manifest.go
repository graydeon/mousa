package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/graydeon/mousa/internal/mousa"
	"github.com/graydeon/mousa/internal/sqlite"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sourceManifestResult struct {
	DecisionID      mousa.PolicyDecisionID       `json:"decision_id"`
	DecisionOutcome mousa.PolicyDecisionOutcome  `json:"decision_outcome"`
	DecisionReasons []mousa.PolicyDecisionReason `json:"decision_reasons"`
	Manifest        *mousa.SourceManifest        `json:"manifest,omitempty"`
	Matches         *bool                        `json:"matches,omitempty"`
}

func sourceManifestProjection(inspection sqlite.SourceManifestInspection) sourceManifestResult {
	result := sourceManifestResult{DecisionID: inspection.Decision.ID, DecisionOutcome: inspection.Decision.Outcome, DecisionReasons: inspection.Decision.ReasonCodes}
	if inspection.Decision.Outcome == mousa.PolicyOutcomeAllow {
		result.Manifest, result.Matches = inspection.Manifest, inspection.Matches
	}
	return result
}

func manifestCommand(ctx context.Context, storePath string, args []string) error {
	flags := newCommandFlags("manifest")
	external := flags.String("source", "", "external JSONL source instead of directory")
	var expected *mousa.SHA256
	flags.Func("expect", "expected nonzero lowercase manifest SHA-256 identity", func(value string) error {
		if expected != nil {
			return errors.New("expect must not be repeated")
		}
		id, err := mousa.ParseSourceManifestID(value)
		if err != nil {
			return err
		}
		expected = &id
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return usageError{err.Error()}
	}
	source, _, err := selectSource(*external, flags.Args(), "manifest")
	if err != nil {
		return err
	}
	request, err := newRetrievalRequest(source.ID)
	if err != nil {
		return err
	}
	store, err := sqlite.Open(ctx, storePath)
	if err != nil {
		return err
	}
	defer store.Close()
	inspection, err := store.InspectSourceManifest(ctx, request, expected)
	if err != nil {
		return err
	}
	result := sourceManifestProjection(inspection)
	if err := emit(result); err != nil {
		return err
	}
	if result.Manifest == nil {
		return errors.New("source manifest inspection denied")
	}
	if expected != nil && (result.Matches == nil || !*result.Matches) {
		return errors.New("source manifest does not match expectation")
	}
	return nil
}

func mcpSourceManifestTool(source map[string]any) (*mcp.Tool, error) {
	output, err := mcpOutputSchema[sourceManifestResult]("mousa_source_manifest")
	if err != nil {
		return nil, err
	}
	return &mcp.Tool{
		Name:        "mousa_source_manifest",
		Description: "Inspect the complete bounded current indexed source inventory under fresh retrieval authorization. Optional expected_manifest_id returns matches in the same snapshot; mismatch is a normal result. Denial omits all manifest and match facts. Does not scan live inputs, grant a lease or record action completion. Requires no ingestion or administration permission.",
		InputSchema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"source"},
			"properties": map[string]any{
				"source":               source,
				"expected_manifest_id": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$", "not": map[string]any{"const": "0000000000000000000000000000000000000000000000000000000000000000"}},
			},
		},
		OutputSchema: output,
	}, nil
}

const mcpManifestMaxRequestIDBytes = 1024

func mcpManifestRequestIDAllowed(message jsonrpc.Message) bool {
	request, ok := message.(*jsonrpc.Request)
	if !ok || request.Method != "tools/call" {
		return true
	}
	var params struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(request.Params, &params) != nil || params.Name != "mousa_source_manifest" {
		return true
	}
	if id, ok := request.ID.Raw().(string); ok && len(id) > mcpManifestMaxRequestIDBytes-2 {
		return false
	}
	encoded, err := json.Marshal(request.ID.Raw())
	return err == nil && len(encoded) <= mcpManifestMaxRequestIDBytes
}

func mcpManifestSuccess(source string, result any) (*mcp.CallToolResult, error) {
	response, err := mcpSuccess("mousa_source_manifest", source, result)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	if len(encoded) > mcpMaxFrameBytes-4096 {
		return mcpFailure("mousa_source_manifest", source, "resource_limit", errors.New("serialized manifest response exceeds transport limit"), 0, 0)
	}
	return response, nil
}

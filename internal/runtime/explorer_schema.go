package runtime

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/workingcontext"
)

// ExplorerOutputSchema declares the advisory explorer result's existing shape.
// Candidate binding and sorted paths are still checked by controller replay.
func ExplorerOutputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["candidate_id","summary","paths"],"properties":{"candidate_id":{"type":"string"},"summary":{"type":"string"},"paths":{"type":"array","items":{"type":"string"}}}}`)
}

// WorkingContextExplorerOutputSchema adds an explicit optional-capability
// replacement contract to candidate-bound exploration. Empty expectedID denotes
// an absent projection, not authority to replace another agent's context.
func WorkingContextExplorerOutputSchema(candidateID, expectedID, expectedHash string) (json.RawMessage, error) {
	if expectedID == "" && expectedHash != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		return nil, errors.New("absent working context requires empty content hash")
	}
	base, err := ExplorerOutputSchemaForCandidate(candidateID)
	if err != nil {
		return nil, err
	}
	if expectedID != "" {
		if _, err := ExplorerOutputSchemaForCandidate(expectedID); err != nil {
			return nil, err
		}
	}
	if _, err := ExplorerOutputSchemaForCandidate(expectedHash); err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(base, &schema); err != nil {
		return nil, err
	}
	schema["required"] = []string{"candidate_id", "summary", "paths", "working_context_update"}
	schema["properties"].(map[string]any)["working_context_update"] = map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"expected_id", "expected_content_hash", "content"},
		"properties": map[string]any{
			"expected_id":           map[string]any{"type": "string", "enum": []string{expectedID}},
			"expected_content_hash": map[string]any{"type": "string", "enum": []string{expectedHash}},
			"content":               map[string]any{"type": "string", "maxLength": workingcontext.MaxContentBytes},
		},
	}
	return canonical.Bytes(schema)
}

// ExplorerOutputSchemaForCandidate binds json-v2's candidate_id field to the
// exact candidate already selected by the invocation. The controller still
// validates the returned value independently during replay.
func ExplorerOutputSchemaForCandidate(candidateID string) (json.RawMessage, error) {
	if len(candidateID) != 64 {
		return nil, errors.New("candidate ID must be 64 lowercase hexadecimal characters")
	}
	for _, c := range candidateID {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return nil, errors.New("candidate ID must be 64 lowercase hexadecimal characters")
		}
	}
	raw, err := canonical.Bytes(map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"candidate_id", "summary", "paths"},
		"properties": map[string]any{
			"candidate_id": map[string]any{"type": "string", "enum": []string{candidateID}},
			"summary":      map[string]any{"type": "string"},
			"paths":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	})
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

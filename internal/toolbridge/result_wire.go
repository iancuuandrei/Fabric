package toolbridge

import (
	"encoding/json"
	"errors"
)

// EncodeToolResult encodes one successful JSON tool result in the exact MCP
// JSON-RPC response envelope used by Server. The request ID is preserved as
// supplied after validation; the result JSON must be one unambiguous object.
func EncodeToolResult(requestID json.RawMessage, result Result) ([]byte, error) {
	if _, _, valid := canonicalRequestID(requestID); !valid {
		return nil, errors.New("invalid tool result request id")
	}
	if result.Error != nil || len(result.JSON) == 0 || firstJSONByte(result.JSON) != '{' || !unambiguousJSON(result.JSON) {
		return nil, errors.New("invalid tool result")
	}
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(requestID),
		"result": map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": string(result.JSON)}},
			"structuredContent": json.RawMessage(result.JSON),
			"isError":           result.IsError,
		},
	})
}

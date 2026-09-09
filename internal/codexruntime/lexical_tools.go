package codexruntime

import (
	"context"
	"encoding/json"
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/ri"
)

func lexicalTool() any {
	cursor := map[string]any{"type": []string{"object", "null"}, "properties": map[string]any{
		"manifest_id": map[string]any{"type": "string"}, "query_id": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"},
		"range": map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 0}, "minItems": 2, "maxItems": 2},
	}, "required": []string{"manifest_id", "query_id", "path", "range"}, "additionalProperties": false}
	return map[string]any{"type": "function", "deferLoading": false, "name": "ri_search", "description": "Search exact bytes in the fixed lexical scope (candidate overlay when bound). Returns Git blob identities and half-open byte ranges. limit is 1 to 100. Use after null initially; pass next unchanged with the same query. Truncation is explicit. Lexical absence is not semantic absence. No indexing or writes occur.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{
		"pattern": map[string]any{"type": "string"}, "fixed": map[string]any{"type": "boolean"}, "case_insensitive": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}, "after": cursor,
		"path": map[string]any{"type": "string", "description": "Optional case-sensitive repository-relative exact path or subtree prefix; empty means all paths."},
		"type": map[string]any{"type": "string", "description": "Optional case-sensitive final filename extension without a leading dot; empty means all types."},
	}, "required": []string{"pattern", "fixed", "case_insensitive", "limit", "after"}, "additionalProperties": false}}
}

func lexicalRead(ctx context.Context, binding LexicalBinding, raw json.RawMessage) (any, error) {
	return lexicalReadWith(ctx, binding, raw, ri.Client{Executable: binding.Executable, ExecutableHash: binding.ExecutableSHA256})
}

type lexicalReader interface {
	SearchLexical(context.Context, ri.LexicalRef, ri.LexicalQuery, int, *ri.LexicalCursor) (ri.LexicalResult, error)
	SearchLexicalOverlay(context.Context, ri.LexicalRef, ri.LexicalOverlayRef, ri.LexicalQuery, int, *ri.LexicalCursor) (ri.LexicalResult, error)
}

func lexicalReadWith(ctx context.Context, binding LexicalBinding, raw json.RawMessage, client lexicalReader) (any, error) {
	var args struct {
		Pattern         string            `json:"pattern"`
		Fixed           bool              `json:"fixed"`
		CaseInsensitive bool              `json:"case_insensitive"`
		Limit           int               `json:"limit"`
		After           *ri.LexicalCursor `json:"after"`
		Path            string            `json:"path,omitempty"`
		Type            string            `json:"type,omitempty"`
	}
	if err := canonical.Decode(raw, &args); err != nil {
		return nil, err
	}
	if args.Limit < 1 || args.Limit > 100 {
		return nil, errors.New("lexical tool page bound exceeded")
	}
	query := ri.LexicalQuery{Pattern: args.Pattern, Fixed: args.Fixed, CaseInsensitive: args.CaseInsensitive, Path: args.Path, Type: args.Type}
	if binding.Overlay != nil {
		return client.SearchLexicalOverlay(ctx, binding.Base, *binding.Overlay, query, args.Limit, args.After)
	}
	return client.SearchLexical(ctx, binding.Base, query, args.Limit, args.After)
}

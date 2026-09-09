package codexruntime

import (
	"context"
	"encoding/json"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/ri"
)

func riTool(name string) bool {
	return name == "ri_status" || name == "ri_locate" || name == "ri_definition" || name == "ri_references"
}

func semanticTools() []any {
	var catalog []any
	for _, name := range []string{"ri_locate", "ri_definition", "ri_references"} {
		properties := map[string]any{
			"producer": map[string]any{"type": "string"},
			"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 128},
			"after":    map[string]any{"type": []string{"string", "null"}},
		}
		required := []string{"producer", "limit", "after"}
		description := "Read direct semantic occurrences for an exact symbol from the fixed RI snapshot. "
		if name == "ri_locate" {
			properties["path"] = map[string]any{"type": "string"}
			properties["offset"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 64 << 20}
			required = append(required, "path", "offset")
			description = "Locate observations at a source byte offset in the fixed RI snapshot. "
		} else {
			properties["symbol"] = map[string]any{"type": "string"}
			required = append(required, "symbol")
		}
		catalog = append(catalog, map[string]any{"type": "function", "deferLoading": false, "name": name, "description": description + "Copy an exact producer identity from ri_status.producers; use after null initially and follow next_after with unchanged arguments. Empty results do not prove absence. No indexer is executed.", "inputSchema": map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}})
	}
	return catalog
}

func semanticRead(ctx context.Context, binding RIBinding, name string, raw json.RawMessage) (any, error) {
	client := ri.Client{Executable: binding.Executable, ExecutableHash: binding.ExecutableSHA256}
	if name == "ri_locate" {
		var args struct {
			Path     string  `json:"path"`
			Offset   int     `json:"offset"`
			Producer string  `json:"producer"`
			Limit    int     `json:"limit"`
			After    *string `json:"after"`
		}
		if err := canonical.Decode(raw, &args); err != nil {
			return nil, err
		}
		return client.Locate(ctx, binding.Snapshot, ri.LocateQuery{Path: args.Path, Offset: args.Offset, Producer: args.Producer, Limit: args.Limit}, args.After)
	}
	var args struct {
		Symbol   string  `json:"symbol"`
		Producer string  `json:"producer"`
		Limit    int     `json:"limit"`
		After    *string `json:"after"`
	}
	if err := canonical.Decode(raw, &args); err != nil {
		return nil, err
	}
	return client.Occurrences(ctx, binding.Snapshot, ri.OccurrenceQuery{Symbol: args.Symbol, Producer: args.Producer, Limit: args.Limit, Definitions: name == "ri_definition"}, args.After)
}

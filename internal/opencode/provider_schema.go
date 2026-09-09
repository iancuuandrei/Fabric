package opencode

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"unicode/utf8"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/toolbridge"
)

const (
	// OpenCodeOpenAISchemaProjection identifies the exact provider schema
	// lowering implemented by the pinned OpenCode runtime.
	OpenCodeOpenAISchemaProjection = "opencode-openai-schema-v1"
	// OpenCodeOpenAIUpstreamCommit is the exact OpenCode v1.18.29 source pin.
	OpenCodeOpenAIUpstreamCommit = "16747470f976aca3d362ad730bcd3fe82ecc2c9a"
	// OpenCodeOpenAITransformSHA256 binds the complete upstream transform.ts.
	OpenCodeOpenAITransformSHA256 = "01b2442770a25f943fd7884ff3f3dac838344a568690e395a77b7bd17c8b0ab5"
)

var providerSchemaToolName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ProviderWireTool is the exact function-tool shape admitted at the provider
// request boundary. Parameters may be a lossy provider projection; controller
// execution must continue to validate against the original tool schema.
type ProviderWireTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ProviderSchemaProjectionReceipt binds one original MCP catalog to the exact
// projected provider catalog and the pinned transform that produced it.
type ProviderSchemaProjectionReceipt struct {
	Version                int                `json:"version"`
	Projection             string             `json:"projection"`
	UpstreamCommit         string             `json:"upstream_commit"`
	TransformSHA256        string             `json:"transform_sha256"`
	OriginalCatalogSHA256  string             `json:"original_catalog_sha256"`
	ProjectedCatalogSHA256 string             `json:"projected_catalog_sha256"`
	Tools                  []ProviderWireTool `json:"tools"`
}

// ProjectOpenCodeOpenAITools mirrors the schema lowering applied by OpenCode
// v1.18.29 before @ai-sdk/openai receives function tools. The implementation is
// adapted from packages/opencode/src/provider/transform.ts at the commit above,
// licensed under the MIT License, Copyright (c) 2025 opencode.
func ProjectOpenCodeOpenAITools(catalog []toolbridge.ToolDefinition) (ProviderSchemaProjectionReceipt, error) {
	var receipt ProviderSchemaProjectionReceipt
	original, err := snapshotProviderSchemaCatalog(catalog)
	if err != nil {
		return receipt, err
	}
	originalHash, err := providerSchemaCatalogHash(original)
	if err != nil {
		return receipt, err
	}
	projected := make([]ProviderWireTool, 0, len(original))
	for _, tool := range original {
		value, err := decodeProviderSchema(tool.InputSchema)
		if err != nil {
			return receipt, errors.New("invalid original provider tool schema")
		}
		value = projectOpenAISchema(value)
		parameters, err := canonical.Bytes(value)
		if err != nil || len(parameters) == 0 || len(parameters) > 32<<10 || parameters[0] != '{' {
			return receipt, errors.New("invalid projected provider tool schema")
		}
		var shape struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(parameters, &shape) != nil || shape.Type != "object" {
			return receipt, errors.New("projected provider tool schema is not an object")
		}
		projected = append(projected, ProviderWireTool{Name: tool.Name, Description: tool.Description, Parameters: parameters})
	}
	projectedHash, err := providerWireCatalogHash(projected)
	if err != nil {
		return receipt, err
	}
	return ProviderSchemaProjectionReceipt{
		Version: 1, Projection: OpenCodeOpenAISchemaProjection,
		UpstreamCommit: OpenCodeOpenAIUpstreamCommit, TransformSHA256: OpenCodeOpenAITransformSHA256,
		OriginalCatalogSHA256: originalHash, ProjectedCatalogSHA256: projectedHash, Tools: projected,
	}, nil
}

func snapshotProviderSchemaCatalog(catalog []toolbridge.ToolDefinition) ([]toolbridge.ToolDefinition, error) {
	if len(catalog) == 0 || len(catalog) > 128 {
		return nil, errors.New("invalid provider tool catalog size")
	}
	seen := make(map[string]bool, len(catalog))
	result := make([]toolbridge.ToolDefinition, 0, len(catalog))
	for _, tool := range catalog {
		if len(tool.Name) < 1 || len(tool.Name) > 128 || !providerSchemaToolName.MatchString(tool.Name) || seen[tool.Name] {
			return nil, errors.New("invalid provider tool catalog name")
		}
		if len(tool.Description) > 4096 || !utf8.ValidString(tool.Description) {
			return nil, errors.New("invalid provider tool description")
		}
		normalized, err := canonical.Normalize(tool.InputSchema)
		if err != nil || len(tool.InputSchema) == 0 || len(tool.InputSchema) > 32<<10 || len(normalized) == 0 || normalized[0] != '{' {
			return nil, errors.New("invalid original provider tool schema")
		}
		var shape struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(normalized, &shape) != nil || shape.Type != "object" {
			return nil, errors.New("original provider tool schema is not an object")
		}
		seen[tool.Name] = true
		result = append(result, toolbridge.ToolDefinition{Name: tool.Name, Description: tool.Description, InputSchema: append(json.RawMessage(nil), tool.InputSchema...)})
	}
	return result, nil
}

func decodeProviderSchema(raw json.RawMessage) (any, error) {
	normalized, err := canonical.Normalize(raw)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func projectOpenAISchema(value any) any {
	if _, ok := value.(bool); ok {
		return map[string]any{"type": "string"}
	}
	if list, ok := value.([]any); ok {
		result := make([]any, len(list))
		for index := range list {
			result[index] = projectOpenAISchema(list[index])
		}
		return result
	}
	object, ok := value.(map[string]any)
	if !ok {
		return value
	}
	result := map[string]any{}
	if ref, ok := object["$ref"].(string); ok {
		result["$ref"] = ref
	}
	if description, ok := object["description"].(string); ok {
		result["description"] = description
	}
	if constant, ok := object["const"]; ok {
		result["enum"] = []any{constant}
	} else if enumeration, ok := object["enum"].([]any); ok {
		result["enum"] = enumeration
	}
	if properties, ok := object["properties"].(map[string]any); ok {
		projected := make(map[string]any, len(properties))
		for name, property := range properties {
			projected[name] = projectOpenAISchema(property)
		}
		result["properties"] = projected
	}
	if required, ok := object["required"].([]any); ok {
		filtered := make([]any, 0, len(required))
		for _, item := range required {
			if _, ok := item.(string); ok {
				filtered = append(filtered, item)
			}
		}
		result["required"] = filtered
	}
	if items, ok := object["items"]; ok {
		result["items"] = projectOpenAISchema(items)
	}
	if additional, ok := object["additionalProperties"]; ok {
		if _, boolean := additional.(bool); boolean {
			result["additionalProperties"] = additional
		} else {
			result["additionalProperties"] = projectOpenAISchema(additional)
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if choices, ok := object[key].([]any); ok {
			projected := make([]any, len(choices))
			for index := range choices {
				projected[index] = projectOpenAISchema(choices[index])
			}
			result[key] = projected
		}
	}
	for _, key := range []string{"$defs", "definitions"} {
		if definitions, ok := object[key].(map[string]any); ok {
			projected := make(map[string]any, len(definitions))
			for name, definition := range definitions {
				projected[name] = projectOpenAISchema(definition)
			}
			result[key] = projected
		}
	}
	types := providerSchemaTypes(object["type"])
	if len(types) == 0 && hasProviderSchemaKey(result, "$ref", "anyOf", "oneOf", "allOf") {
		return result
	}
	if len(types) == 0 {
		switch {
		case hasProviderSchemaKey(object, "properties", "required", "additionalProperties"):
			types = []string{"object"}
		case hasProviderSchemaKey(object, "items", "prefixItems"):
			types = []string{"array"}
		case hasProviderSchemaKey(result, "enum") || hasProviderSchemaKey(object, "format"):
			types = []string{"string"}
		case hasProviderSchemaKey(object, "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf"):
			types = []string{"number"}
		}
	}
	if len(types) == 0 {
		return map[string]any{}
	}
	if len(types) == 1 {
		result["type"] = types[0]
	} else {
		result["type"] = types
	}
	if containsProviderSchemaType(types, "object") && result["properties"] == nil {
		result["properties"] = map[string]any{}
	}
	if containsProviderSchemaType(types, "array") && result["items"] == nil {
		result["items"] = map[string]any{"type": "string"}
	}
	return result
}

func providerSchemaTypes(value any) []string {
	allowed := map[string]bool{"string": true, "number": true, "boolean": true, "integer": true, "object": true, "array": true, "null": true}
	if single, ok := value.(string); ok {
		if allowed[single] {
			return []string{single}
		}
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(list))
	for _, item := range list {
		if kind, ok := item.(string); ok && allowed[kind] {
			result = append(result, kind)
		}
	}
	return result
}

func hasProviderSchemaKey(object map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := object[key]; ok {
			return true
		}
	}
	return false
}

func containsProviderSchemaType(types []string, expected string) bool {
	for _, kind := range types {
		if kind == expected {
			return true
		}
	}
	return false
}

func providerSchemaCatalogHash(tools []toolbridge.ToolDefinition) (string, error) {
	return providerSchemaHash(struct {
		Tools []toolbridge.ToolDefinition `json:"tools"`
	}{Tools: tools})
}

func providerWireCatalogHash(tools []ProviderWireTool) (string, error) {
	return providerSchemaHash(struct {
		Tools []ProviderWireTool `json:"tools"`
	}{Tools: tools})
}

func providerSchemaHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

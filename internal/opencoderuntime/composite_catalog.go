package opencoderuntime

import (
	"errors"

	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/toolbridge"
)

// ProviderRequestToolsForCatalog projects a finite MCP catalog into the exact
// namespaced provider tools used by the pinned OpenCode runtime. It performs
// no dispatch and grants no authority to any selected tool.
func ProviderRequestToolsForCatalog(catalog []toolbridge.ToolDefinition, names []string) ([]providergateway.RequestTool, error) {
	if _, err := toolbridge.CatalogIdentity(catalog); err != nil {
		return nil, err
	}
	byName := make(map[string]toolbridge.ToolDefinition, len(catalog))
	for _, definition := range catalog {
		byName[definition.Name] = definition
	}
	selected := make([]toolbridge.ToolDefinition, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		definition, ok := byName[name]
		if !ok || seen[name] {
			return nil, errors.New("unavailable or duplicate session tool")
		}
		seen[name] = true
		definition.InputSchema = append([]byte(nil), definition.InputSchema...)
		selected = append(selected, definition)
	}
	projection, err := opencode.ProjectOpenCodeOpenAITools(selected)
	if err != nil {
		return nil, err
	}
	result := make([]providergateway.RequestTool, len(projection.Tools))
	for index, tool := range projection.Tools {
		result[index] = providergateway.RequestTool{Name: opencode.ToolsMCPServerName + "_" + tool.Name, Description: tool.Description, Parameters: append([]byte(nil), tool.Parameters...)}
	}
	return result, nil
}

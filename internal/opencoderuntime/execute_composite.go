package opencoderuntime

import (
	"errors"

	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/toolbridge"
	"harness.local/engorch/internal/toolreceipts"
)

func executionProviderTools(cfg ExecuteConfig, plan SessionPlan) ([]toolbridge.ToolDefinition, error) {
	if cfg.Intent.Version != 3 {
		if cfg.Composite != nil || cfg.VerifyComposite != nil || cfg.Intent.ToolReceipts != nil || cfg.Paths.ToolReceipts != "" {
			return nil, errors.New("legacy runtime cannot expose composite tools")
		}
		return providerToolsForSession(cfg.Broker.Catalog(), plan.ToolNames)
	}
	if cfg.Composite == nil || cfg.VerifyComposite == nil || cfg.Intent.ToolReceipts == nil || cfg.Paths.Version != 2 || cfg.Composite.Path != cfg.Paths.ToolReceipts || cfg.Composite.InvocationID != cfg.Intent.Invocation.ID {
		return nil, errors.New("complete composite runtime binding required")
	}
	if err := cfg.Paths.validate(); err != nil {
		return nil, err
	}
	prepared, err := contextmcp.PrepareRecorderBindingWithQueue(cfg.Broker, cfg.Composite.InvocationID, cfg.Composite.CallerBindingSHA256, cfg.Composite.AgentProjection, cfg.Composite.MaxQueuedCalls)
	if err != nil || !equalCanonical(prepared, *cfg.Intent.ToolReceipts) || prepared.CatalogSHA256 != cfg.Composite.CatalogSHA256 || prepared.CatalogSHA256 != plan.CatalogSHA256 {
		return nil, errors.New("composite runtime catalog or caller differs")
	}
	contextProjection, err := contextmcp.Projection(cfg.Broker)
	if err != nil {
		return nil, err
	}
	projection, err := toolbridge.Compose(contextProjection, cfg.Composite.AgentProjection)
	if err != nil {
		return nil, err
	}
	catalog, err := projection.Catalog()
	if err != nil {
		return nil, err
	}
	tools, err := ProviderRequestToolsForCatalog(catalog, plan.ToolNames)
	if err != nil {
		return nil, err
	}
	result := make([]toolbridge.ToolDefinition, len(tools))
	for index, tool := range tools {
		result[index] = toolbridge.ToolDefinition{Name: tool.Name, Description: tool.Description, InputSchema: append([]byte(nil), tool.Parameters...)}
	}
	return result, nil
}

func newExecutionMCP(cfg ExecuteConfig, bearer string) (*contextmcp.OwnedServer, error) {
	if cfg.Intent.Version == 3 {
		if cfg.Composite == nil {
			return nil, errors.New("composite runtime configuration required")
		}
		server, err := contextmcp.NewRecorderOwned(cfg.Broker, bearer, *cfg.Composite, nil)
		if err != nil {
			return nil, err
		}
		state, err := toolreceipts.Inspect(cfg.Paths.ToolReceipts)
		if err != nil || len(state.Calls) != 0 {
			return nil, errors.Join(errors.New("unused composite transport journal required"), err)
		}
		return server, nil
	}
	if cfg.MCPQueueDepth < 0 || cfg.MCPQueueDepth > 32 {
		return nil, errors.New("invalid context MCP queue depth")
	}
	if cfg.MCPQueueDepth == 0 {
		return contextmcp.NewOwned(cfg.Broker, bearer, nil)
	}
	return contextmcp.NewOwnedWithQueue(cfg.Broker, bearer, nil, cfg.MCPQueueDepth)
}

func validateExecutionMCPOwner(cfg ExecuteConfig, running *contextmcp.OwnedRunning, bearer string) error {
	if cfg.Intent.Version == 3 {
		if cfg.Intent.ToolReceipts == nil {
			return errors.New("composite runtime receipt binding required")
		}
		return running.ValidateRecorderOwner(cfg.Broker, bearer, cfg.Paths.ToolReceipts, *cfg.Intent.ToolReceipts)
	}
	return running.ValidateOwner(cfg.Broker, bearer)
}

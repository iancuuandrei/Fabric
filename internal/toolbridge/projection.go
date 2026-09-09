package toolbridge

import (
	"context"
	"errors"
)

// Projection pairs one controller-owned catalog with its execution callback.
// It supplies no caller identity or authority of its own.
type Projection struct {
	Catalog CatalogFunc
	Call    CallFunc
}

// Compose freezes disjoint catalogs and routes calls to their exact owner.
// Input order is preserved. Construction grants no execution authority; each
// callback must retain its own admission and durable receipt checks.
func Compose(projections ...Projection) (Projection, error) {
	if len(projections) == 0 || len(projections) > 128 {
		return Projection{}, errors.New("invalid tool projection count")
	}
	var combined []ToolDefinition
	routes := make(map[string]CallFunc)
	for _, projection := range projections {
		if projection.Catalog == nil || projection.Call == nil {
			return Projection{}, errors.New("tool projection callbacks required")
		}
		catalog, err := projection.Catalog()
		if err != nil {
			return Projection{}, err
		}
		frozen, _, _, err := validateCatalog(catalog, maximumBodyBound)
		if err != nil {
			return Projection{}, err
		}
		for _, tool := range frozen {
			if _, exists := routes[tool.Name]; exists {
				return Projection{}, errors.New("tool projection name collision")
			}
			routes[tool.Name] = projection.Call
			combined = append(combined, tool)
		}
	}
	frozen, _, _, err := validateCatalog(combined, maximumBodyBound)
	if err != nil {
		return Projection{}, err
	}
	return Projection{
		Catalog: func() ([]ToolDefinition, error) {
			copy := make([]ToolDefinition, len(frozen))
			for i, tool := range frozen {
				copy[i] = tool
				copy[i].InputSchema = append([]byte(nil), tool.InputSchema...)
			}
			return copy, nil
		},
		Call: func(ctx context.Context, call Call) (Result, error) {
			if ctx == nil {
				return Result{}, errors.New("tool call context required")
			}
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			callback, exists := routes[call.Tool]
			if !exists {
				return Result{}, errors.New("tool absent from composed catalog")
			}
			return callback(ctx, call)
		},
	}, nil
}

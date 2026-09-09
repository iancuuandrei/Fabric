// Package sourcetools defines and executes the read-only tools for an
// immutable repository source. It contains no provider or runtime policy;
// callers retain responsibility for request admission and response recording.
package sourcetools

import (
	"context"
	"encoding/json"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
)

const (
	// ListName identifies bounded immutable-source directory listing.
	ListName = "source_list"
	// ReadName identifies bounded immutable-source file reading.
	ReadName = "source_read"
)

// Definition is a runtime-neutral function-tool definition.
type Definition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

// ListArgs are the bounded source_list arguments.
type ListArgs struct {
	After string `json:"after"`
	Limit int    `json:"limit"`
}

// ReadArgs are the bounded source_read arguments.
type ReadArgs struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Limit  int    `json:"limit"`
}

// Catalog returns the immutable-source tool definitions in stable order.
func Catalog() []Definition {
	return []Definition{
		{
			Name:        ListName,
			Description: "List paths from the fixed source commit. limit must be 1 to 128; use after empty string initially. Follow next_after until null; includes symlinks/submodules as explicit kinds. No relevance filtering.",
			InputSchema: object(
				map[string]any{
					"after": map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 128},
				},
				[]string{"after", "limit"},
			),
		},
		{
			Name:        ReadName,
			Description: "Read exact regular-file bytes from the fixed source commit. limit must be 1 to 32768 bytes. Returns content_utf8 when valid UTF-8 and always content_base64, with chunk hash. Follow next_offset until null. Working tree changes do not affect this source.",
			InputSchema: object(
				map[string]any{
					"path":   map[string]any{"type": "string"},
					"offset": map[string]any{"type": "integer", "minimum": 0},
					"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 32768},
				},
				[]string{"path", "offset", "limit"},
			),
		},
	}
}

func object(properties map[string]any, required []string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

// Execute strictly decodes and executes a supported immutable-source tool.
// The repository package enforces commit/tree identity, path-kind and IO bounds.
func Execute(ctx context.Context, source repository.Identity, name string, arguments json.RawMessage) (content any, handled bool, err error) {
	switch name {
	case ListName:
		var args ListArgs
		if err := canonical.Decode(arguments, &args); err != nil {
			return nil, true, err
		}
		content, err := repository.ListSource(ctx, source, args.After, args.Limit)
		return content, true, err
	case ReadName:
		var args ReadArgs
		if err := canonical.Decode(arguments, &args); err != nil {
			return nil, true, err
		}
		content, err := repository.ReadSource(ctx, source, args.Path, args.Offset, args.Limit)
		return content, true, err
	default:
		return nil, false, nil
	}
}

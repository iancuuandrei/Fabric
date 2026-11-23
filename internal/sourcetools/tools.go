// Package sourcetools defines and executes the read-only tools for an
// immutable repository source. It contains no provider or runtime policy;
// callers retain responsibility for request admission and response recording.
package sourcetools

import (
	"context"
	"encoding/json"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/toolcontent"
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

// ReadResult is the model-facing view of a committed source page. Text pages
// omit their redundant Base64 copy; binary pages retain Base64.
type ReadResult struct {
	RepositoryID  string  `json:"repository_id"`
	Commit        string  `json:"commit"`
	Path          string  `json:"path"`
	Blob          string  `json:"blob"`
	Offset        int64   `json:"offset"`
	TotalBytes    int64   `json:"total_bytes"`
	ContentBase64 string  `json:"content_base64,omitempty"`
	ContentUTF8   *string `json:"content_utf8"`
	ChunkHash     string  `json:"chunk_hash"`
	NextOffset    *int64  `json:"next_offset"`
}

func readResult(chunk repository.SourceChunk) (ReadResult, error) {
	encoded, text, err := toolcontent.UTF8First(chunk.ContentBase64, chunk.ContentUTF8)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{chunk.RepositoryID, chunk.Commit, chunk.Path, chunk.Blob, chunk.Offset, chunk.TotalBytes, encoded, text, chunk.ChunkHash, chunk.NextOffset}, nil
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
			Description: "Read exact regular-file bytes from the fixed source commit. limit must be 1 to 32768 bytes. For valid UTF-8 pages, content_utf8 contains the bytes and content_base64 is omitted; binary pages include content_base64 and null content_utf8. chunk_hash binds returned bytes. Follow next_offset until null. Working tree changes do not affect this source.",
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

// LegacyCatalog returns the byte-identical catalog used by v1 context bindings.
func LegacyCatalog() []Definition {
	definitions := Catalog()
	definitions[1].Description = "Read exact regular-file bytes from the fixed source commit. limit must be 1 to 32768 bytes. Returns content_utf8 when valid UTF-8 and always content_base64, with chunk hash. Follow next_offset until null. Working tree changes do not affect this source."
	return definitions
}

func object(properties map[string]any, required []string) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

// Execute strictly decodes and executes a supported immutable-source tool.
// The repository package enforces commit/tree identity, path-kind and IO bounds.
func Execute(ctx context.Context, source repository.Identity, name string, arguments json.RawMessage) (content any, handled bool, err error) {
	return execute(ctx, source, name, arguments, true)
}

// ExecuteLegacy retains the v1 source-read result shape for sessions whose
// model was given the v1 tool catalog.
func ExecuteLegacy(ctx context.Context, source repository.Identity, name string, arguments json.RawMessage) (content any, handled bool, err error) {
	return execute(ctx, source, name, arguments, false)
}

func execute(ctx context.Context, source repository.Identity, name string, arguments json.RawMessage, utf8First bool) (content any, handled bool, err error) {
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
		chunk, err := repository.ReadSource(ctx, source, args.Path, args.Offset, args.Limit)
		if err != nil {
			return nil, true, err
		}
		if !utf8First {
			return chunk, true, nil
		}
		result, err := readResult(chunk)
		if err != nil {
			return nil, true, err
		}
		return result, true, nil
	default:
		return nil, false, nil
	}
}

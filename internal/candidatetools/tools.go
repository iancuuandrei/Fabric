// Package candidatetools defines and executes read-only tools for one exact
// mutable candidate observation. Callers must hold the workspace lease for the
// complete runtime execution and retain responsibility for request admission.
package candidatetools

import (
	"context"
	"encoding/json"
	"errors"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/sourcetools"
	"harness.local/engorch/internal/worktree"
)

const (
	// ListName identifies listing against the admitted candidate observation.
	ListName = "candidate_list"
	// ReadName identifies reading against the admitted candidate observation.
	ReadName = "candidate_read"
)

// Binding pins mutable workspace reads to one exact admitted candidate.
type Binding struct {
	Workspace worktree.Binding   `json:"workspace"`
	Candidate worktree.Candidate `json:"candidate"`
}

// Validate checks structural and source identity. Execute additionally
// reobserves the live candidate before making any bytes available.
func (b Binding) Validate(source repository.Identity) error {
	id, err := b.Workspace.ID()
	if err != nil {
		return err
	}
	if err := b.Candidate.ValidateBinding(b.Workspace); err != nil {
		return err
	}
	if b.Workspace.Request.Source != source || b.Candidate.WorktreeID != id || b.Candidate.Head != source.Commit {
		return errors.New("runtime candidate/source binding mismatch")
	}
	return nil
}

// Page is a bounded, path-ordered view of the exact candidate manifest.
type Page struct {
	CandidateID string               `json:"candidate_id"`
	Files       []worktree.FileState `json:"files"`
	NextAfter   *string              `json:"next_after"`
}

// Catalog returns candidate tool definitions in stable order.
func Catalog() []sourcetools.Definition {
	return []sourcetools.Definition{
		{
			Name:        ListName,
			Description: "List the exact admitted candidate's current regular files with hashes and modes. Use after empty initially, follow next_after until null. Unlike source_list this includes admitted modifications. Drift fails the request.",
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
			Description: "Read a byte page from the admitted candidate, including modifications. Returns exact full-file hash and binary/UTF-8 views. Follow next_offset until null. Any candidate drift fails the request.",
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

// ListArgs are the bounded candidate_list arguments.
type ListArgs struct {
	After string `json:"after"`
	Limit int    `json:"limit"`
}

// ReadArgs are the bounded candidate_read arguments.
type ReadArgs struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Limit  int    `json:"limit"`
}

// Execute strictly decodes and executes one supported candidate tool. Unknown
// names are returned as unhandled without inspecting the binding or workspace.
func Execute(ctx context.Context, binding Binding, name string, arguments json.RawMessage) (content any, handled bool, err error) {
	switch name {
	case ListName:
		var args ListArgs
		if err := canonical.Decode(arguments, &args); err != nil {
			return nil, true, err
		}
		if args.Limit < 1 || args.Limit > 128 || len(args.After) > 4096 {
			return nil, true, errors.New("invalid candidate list bounds")
		}
		if err := binding.Validate(binding.Workspace.Request.Source); err != nil {
			return nil, true, err
		}
		content, err := list(ctx, binding, args.After, args.Limit)
		return content, true, err
	case ReadName:
		var args ReadArgs
		if err := canonical.Decode(arguments, &args); err != nil {
			return nil, true, err
		}
		if args.Offset < 0 || args.Offset > 64<<20 || args.Limit < 1 || args.Limit > 32768 {
			return nil, true, errors.New("invalid candidate read bounds")
		}
		if err := binding.Validate(binding.Workspace.Request.Source); err != nil {
			return nil, true, err
		}
		content, err := worktree.ReadSource(ctx, binding.Workspace, binding.Candidate, args.Path, args.Offset, args.Limit)
		return content, true, err
	default:
		return nil, false, nil
	}
}

func list(ctx context.Context, binding Binding, after string, limit int) (Page, error) {
	if limit < 1 || limit > 128 || len(after) > 4096 {
		return Page{}, errors.New("invalid candidate list bounds")
	}
	candidate, files, err := worktree.Capture(ctx, binding.Workspace)
	if err != nil {
		return Page{}, err
	}
	if candidate != binding.Candidate {
		return Page{}, errors.New("candidate changed before listing")
	}
	id, err := candidate.ID()
	if err != nil {
		return Page{}, err
	}
	page := Page{CandidateID: id, Files: []worktree.FileState{}}
	for _, file := range files {
		if file.Path <= after {
			continue
		}
		if len(page.Files) == limit {
			last := page.Files[len(page.Files)-1].Path
			page.NextAfter = &last
			break
		}
		page.Files = append(page.Files, file)
	}
	return page, nil
}

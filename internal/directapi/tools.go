package directapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	maxToolReadBytes    = 64 << 10
	maxToolListEntries  = 200
	maxToolSearchHits   = 50
	maxToolSearchBytes  = 512 << 10
	defaultMaxToolTurns = 8
)

// repoTools provides strictly read-only, root-confined filesystem inspection
// tools for direct-api models. It never mutates files or runs shell commands.
type repoTools struct {
	root string
}

func newRepoTools(workDir string) (*repoTools, error) {
	if strings.TrimSpace(workDir) == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs
	}
	return &repoTools{root: filepath.Clean(resolved)}, nil
}

func (rt *repoTools) resolveSafePath(rel string) (string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(rel))
	if cleaned == "" || cleaned == "." {
		return rt.root, nil
	}
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", errors.New("path must stay inside repository workspace")
	}
	parts := strings.Split(filepath.ToSlash(cleaned), "/")
	for _, p := range parts {
		if p == ".." || p == ".git" || p == ".harness" || p == ".fabric" {
			return "", fmt.Errorf("access denied to protected path %q", rel)
		}
	}
	full := filepath.Join(rt.root, cleaned)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("path not found: %s", rel)
		}
		return "", err
	}
	relCheck, err := filepath.Rel(rt.root, resolved)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", errors.New("symlink escapes repository workspace")
	}
	return resolved, nil
}

func (rt *repoTools) execute(name string, rawArgs json.RawMessage) string {
	switch name {
	case "read_file":
		var args struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line,omitempty"`
			EndLine   int    `json:"end_line,omitempty"`
		}
		if err := json.Unmarshal(rawArgs, &args); err != nil || args.Path == "" {
			return `{"error":"read_file requires path"}`
		}
		out, err := rt.readFile(args.Path, args.StartLine, args.EndLine)
		if err != nil {
			return encodeToolError(err)
		}
		return out

	case "list_files":
		var args struct {
			Dir string `json:"dir,omitempty"`
		}
		_ = json.Unmarshal(rawArgs, &args)
		out, err := rt.listFiles(args.Dir)
		if err != nil {
			return encodeToolError(err)
		}
		return out

	case "search_text":
		var args struct {
			Query string `json:"query"`
			Dir   string `json:"dir,omitempty"`
		}
		if err := json.Unmarshal(rawArgs, &args); err != nil || strings.TrimSpace(args.Query) == "" {
			return `{"error":"search_text requires non-empty query"}`
		}
		out, err := rt.searchText(args.Query, args.Dir)
		if err != nil {
			return encodeToolError(err)
		}
		return out

	default:
		return fmt.Sprintf(`{"error":"unknown read-only tool %q"}`, name)
	}
}

func (rt *repoTools) readFile(rel string, startLine, endLine int) (string, error) {
	full, err := rt.resolveSafePath(rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("target path is a directory; use list_files")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("binary or non-UTF-8 file cannot be read")
	}
	lines := strings.Split(string(data), "\n")
	if startLine < 1 {
		startLine = 1
	}
	if endLine <= 0 || endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > len(lines) {
		return "", fmt.Errorf("start_line %d exceeds file length %d", startLine, len(lines))
	}
	var b strings.Builder
	truncated := false
	for i := startLine; i <= endLine; i++ {
		line := fmt.Sprintf("%d: %s\n", i, lines[i-1])
		if b.Len()+len(line) > maxToolReadBytes {
			truncated = true
			break
		}
		b.WriteString(line)
	}
	payload, _ := json.Marshal(map[string]any{
		"path":        filepath.ToSlash(rel),
		"total_lines": len(lines),
		"truncated":   truncated,
		"content":     b.String(),
	})
	return string(payload), nil
}

func (rt *repoTools) listFiles(dir string) (string, error) {
	base, err := rt.resolveSafePath(dir)
	if err != nil {
		return "", err
	}
	files := make([]string, 0, 64)
	truncated := false
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == ".harness" || name == ".fabric" || name == "node_modules" || name == "target" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= maxToolListEntries {
			truncated = true
			return fs.SkipAll
		}
		rel, err := filepath.Rel(rt.root, p)
		if err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	payload, _ := json.Marshal(map[string]any{"files": files, "truncated": truncated})
	return string(payload), nil
}

func (rt *repoTools) searchText(query, dir string) (string, error) {
	base, err := rt.resolveSafePath(dir)
	if err != nil {
		return "", err
	}
	type hit struct {
		Path string `json:"path"`
		Line int    `json:"line"`
		Text string `json:"text"`
	}
	var hits []hit
	truncated := false
	_ = filepath.WalkDir(base, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == ".git" || name == ".harness" || name == ".fabric" || name == "node_modules" || name == "target" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxToolSearchBytes {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		rel, _ := filepath.Rel(rt.root, p)
		sc := bufio.NewScanner(f)
		lineNo := 0
		for sc.Scan() {
			lineNo++
			txt := sc.Text()
			if strings.Contains(txt, query) {
				if len(txt) > 240 {
					txt = txt[:240] + "..."
				}
				hits = append(hits, hit{Path: filepath.ToSlash(rel), Line: lineNo, Text: txt})
				if len(hits) >= maxToolSearchHits {
					truncated = true
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	payload, _ := json.Marshal(map[string]any{"query": query, "matches": hits, "truncated": truncated})
	return string(payload), nil
}

func encodeToolError(err error) string {
	b, _ := json.Marshal(map[string]string{"error": err.Error()})
	return string(b)
}

func anthropicToolDefs() []map[string]any {
	return []map[string]any{
		{
			"name":        "read_file",
			"description": "Read a UTF-8 file inside the repository workspace with line numbers (read-only).",
			"input_schema": map[string]any{
				"type":     "object",
				"required": []string{"path"},
				"properties": map[string]any{
					"path":       map[string]any{"type": "string", "description": "Repository-relative file path"},
					"start_line": map[string]any{"type": "integer", "description": "Optional 1-based start line"},
					"end_line":   map[string]any{"type": "integer", "description": "Optional 1-based inclusive end line"},
				},
			},
		},
		{
			"name":        "list_files",
			"description": "List repository-relative file paths under the workspace or a subdirectory (read-only).",
			"input_schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"dir": map[string]any{"type": "string", "description": "Optional repository-relative subdirectory"},
				},
			},
		},
		{
			"name":        "search_text",
			"description": "Search literal text across repository source files (read-only).",
			"input_schema": map[string]any{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Exact substring to search for"},
					"dir":   map[string]any{"type": "string", "description": "Optional repository-relative subdirectory"},
				},
			},
			// Enable Anthropic prompt caching across the static tool catalog.
			"cache_control": map[string]string{"type": "ephemeral"},
		},
	}
}

func openaiToolDefs() []map[string]any {
	defs := anthropicToolDefs()
	out := make([]map[string]any, 0, len(defs))
	for _, d := range defs {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        d["name"],
				"description": d["description"],
				"parameters":  d["input_schema"],
			},
		})
	}
	return out
}

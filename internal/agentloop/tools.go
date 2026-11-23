package agentloop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/iancuuandrei/fabric/internal/codegraph"
	"github.com/iancuuandrei/fabric/internal/diagnostics"
	"github.com/iancuuandrei/fabric/internal/gitexec"
	"github.com/iancuuandrei/fabric/internal/sandbox"
)

const (
	maxReadBytes       = 64 << 10
	maxWriteBytes      = 512 << 10
	maxListEntries     = 250
	maxSearchHits      = 80
	maxSearchFileBytes = 2 << 20 // 2 MiB per file
	maxSearchLineBytes = 2 << 20 // 2 MiB scanner buffer for minified lines
	binarySniffBytes   = 8192
)

// WorkspaceTools confines all file reads, anchored edits, code-graph queries,
// lazy skills, inline diagnostics, and argv commands strictly inside an
// isolated Git worktree with CapScope capability enforcement.
type WorkspaceTools struct {
	Root              string
	AllowedWrite      []string // optional repo-relative write allowlist; empty = all non-protected worktree files
	Sandbox           sandbox.Config
	AllowCommand      bool
	EnableDiagnostics bool
	Capabilities      Capabilities
	Skills            []SkillMetadata
	OnPreMutate       func(reason string) error

	graphMu sync.Mutex
	graph   *codegraph.Graph
}

// ToolDef declares one model-callable ACI tool.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ToolCall is a single structured tool invocation requested by the model.
type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolResult is the deterministic ACI response returned to the model.
type ToolResult struct {
	CallID   string `json:"call_id"`
	ToolName string `json:"tool_name"`
	Output   string `json:"output"`
	IsError  bool   `json:"is_error,omitempty"`
	Mutated  bool   `json:"mutated,omitempty"`
}

// NewWorkspaceTools creates a confined ACI toolset bound to workDir with
// explicit CapScope capabilities enforced outside the LLM context window.
func NewWorkspaceTools(workDir string, allowedWrite []string, sb sandbox.Config, allowCommand bool, skillDirs ...string) (*WorkspaceTools, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs
	}
	searchDirs := defaultSkillSearchDirs("", resolved, skillDirs)
	return &WorkspaceTools{
		Root:              filepath.Clean(resolved),
		AllowedWrite:      allowedWrite,
		Sandbox:           sb,
		AllowCommand:      allowCommand,
		EnableDiagnostics: true,
		Capabilities:      DefaultCapabilities(allowCommand, allowedWrite),
		Skills:            discoverSkills(searchDirs...),
	}, nil
}

// Definitions returns the model-facing tool catalog including graph navigation
func propDef(typ, desc string) map[string]any {
	return map[string]any{"type": typ, "description": desc}
}

func toolObjSchema(required []string, props map[string]any) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// and lazy skill loading.
func (w *WorkspaceTools) Definitions() []ToolDef {
	defs := []ToolDef{
		{
			Name:        "repo_map",
			Description: "Return a compact structural Code Knowledge Graph map (top hub files, PageRank centrality, module clusters, and symbols). Call first for orientation instead of blindly reading files.",
			Parameters:  toolObjSchema(nil, nil),
		},
		{
			Name:        "graph_impact",
			Description: "Query the local Code Knowledge Graph for multi-hop blast radius (upstream callers, downstream callees/imports, and linked docs) around a file path or symbol name.",
			Parameters: toolObjSchema([]string{"target"}, map[string]any{
				"target": propDef("string", "File path or symbol name to trace"),
				"depth":  propDef("integer", "Hop depth (1..4, default 2)"),
			}),
		},
		{
			Name:        "list_skills",
			Description: "List available specialized Fabric engineering skills (metadata only). Call load_skill to read full steps when relevant.",
			Parameters:  toolObjSchema(nil, nil),
		},
		{
			Name:        "load_skill",
			Description: "Load the full playbook for one Fabric engineering skill by name (e.g. anchored-editing, tdd, systematic-debugging, code-review).",
			Parameters: toolObjSchema([]string{"name"}, map[string]any{
				"name": propDef("string", "Exact skill name from list_skills"),
			}),
		},
		{
			Name:        "read_file",
			Description: "Read a UTF-8 file in the workspace with 1-based line numbers.",
			Parameters: toolObjSchema([]string{"path"}, map[string]any{
				"path":       propDef("string", "Workspace-relative file path"),
				"start_line": propDef("integer", "Optional 1-based start line"),
				"end_line":   propDef("integer", "Optional 1-based inclusive end line"),
			}),
		},
		{
			Name:        "edit_file",
			Description: "Apply an exact unique anchored search-and-replace edit to an existing file. Fails if old_string matches 0 or more than 1 times. Returns inline syntax/type diagnostics when detected.",
			Parameters: toolObjSchema([]string{"path", "old_string", "new_string"}, map[string]any{
				"path":       propDef("string", "Workspace-relative file path"),
				"old_string": propDef("string", "Exact unique snippet currently in the file"),
				"new_string": propDef("string", "Exact replacement text"),
			}),
		},
		{
			Name:        "write_file",
			Description: "Create a new UTF-8 file or replace a small file inside the allowed workspace scope. Returns inline syntax/type diagnostics when detected.",
			Parameters: toolObjSchema([]string{"path", "content"}, map[string]any{
				"path":    propDef("string", "Workspace-relative file path"),
				"content": propDef("string", "Complete UTF-8 file contents"),
			}),
		},
		{
			Name:        "delete_file",
			Description: "Remove an existing file inside the allowed workspace scope (never deletes directories or protected paths).",
			Parameters: toolObjSchema([]string{"path"}, map[string]any{
				"path": propDef("string", "Workspace-relative file path to remove"),
			}),
		},
		{
			Name:        "list_files",
			Description: "List workspace-relative source file paths (skipping .git, node_modules, target, vendor).",
			Parameters: toolObjSchema(nil, map[string]any{
				"dir": propDef("string", "Optional subdirectory"),
			}),
		},
		{
			Name:        "search_text",
			Description: "Search literal text across workspace source files (automatically skips binary files), returning file:line matches.",
			Parameters: toolObjSchema([]string{"query"}, map[string]any{
				"query": propDef("string", "Exact substring to find"),
				"dir":   propDef("string", "Optional subdirectory"),
			}),
		},
		{
			Name:        "git_diff",
			Description: "Inspect the current unified git diff of all workspace changes (including newly created files).",
			Parameters:  toolObjSchema(nil, nil),
		},
	}
	if w.AllowCommand && w.Capabilities.AllowCommand {
		defs = append(defs, ToolDef{
			Name:        "run_command",
			Description: "Execute a bounded build, linter, or test command inside the isolated workspace using an explicit argv array (no shell).",
			Parameters: toolObjSchema([]string{"argv"}, map[string]any{
				"argv":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Executable and arguments, e.g. [\"go\", \"test\", \"./...\"]"},
				"timeout_seconds": propDef("integer", "Optional timeout (1..300s)"),
			}),
		})
	}
	return defs
}

// Execute runs one validated tool call inside the confined workspace after
// verifying typed CapScope capabilities outside the LLM context window.
func (w *WorkspaceTools) Execute(ctx context.Context, call ToolCall) ToolResult {
	res := ToolResult{CallID: call.ID, ToolName: call.Name}
	if err := w.Capabilities.checkToolAllowed(call.Name); err != nil {
		return errResult(res, err)
	}
	switch call.Name {
	case "repo_map":
		g, err := w.getGraph()
		if err != nil {
			return errResult(res, err)
		}
		res.Output = g.RepoMap(6000)
		return res

	case "graph_impact":
		var a struct {
			Target string `json:"target"`
			Depth  int    `json:"depth"`
		}
		if err := json.Unmarshal(call.Arguments, &a); err != nil || strings.TrimSpace(a.Target) == "" {
			return errResult(res, errors.New("graph_impact requires non-empty target"))
		}
		g, err := w.getGraph()
		if err != nil {
			return errResult(res, err)
		}
		b, _ := json.Marshal(g.Impact(a.Target, a.Depth))
		res.Output = string(b)
		return res

	case "list_skills":
		type item struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		items := make([]item, 0, len(w.Skills))
		for _, s := range w.Skills {
			items = append(items, item{Name: s.Name, Description: s.Description})
		}
		b, _ := json.Marshal(map[string]any{"skills": items})
		res.Output = string(b)
		return res

	case "load_skill":
		var a struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(call.Arguments, &a); err != nil || strings.TrimSpace(a.Name) == "" {
			return errResult(res, errors.New("load_skill requires skill name"))
		}
		body, ok := loadSkillBody(w.Skills, a.Name)
		if !ok {
			return errResult(res, fmt.Errorf("skill %q not found; call list_skills to see valid names", a.Name))
		}
		res.Output = fenceUntrustedRepoGuidance("skill:"+a.Name, body)
		return res

	case "read_file":
		var a struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
		}
		if err := json.Unmarshal(call.Arguments, &a); err != nil || a.Path == "" {
			return errResult(res, errors.New("read_file requires path"))
		}
		out, err := w.readFile(a.Path, a.StartLine, a.EndLine)
		if err != nil {
			return errResult(res, err)
		}
		res.Output = out
		return res

	case "edit_file":
		var a struct {
			Path      string `json:"path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if err := json.Unmarshal(call.Arguments, &a); err != nil || a.Path == "" || a.OldString == "" {
			return errResult(res, errors.New("edit_file requires path, non-empty old_string, and new_string"))
		}
		out, err := w.editFile(ctx, a.Path, a.OldString, a.NewString)
		if err != nil {
			return errResult(res, err)
		}
		w.invalidateGraph()
		res.Output = out
		res.Mutated = true
		return res

	case "write_file":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(call.Arguments, &a); err != nil || a.Path == "" {
			return errResult(res, errors.New("write_file requires path and content"))
		}
		out, err := w.writeFile(ctx, a.Path, a.Content)
		if err != nil {
			return errResult(res, err)
		}
		w.invalidateGraph()
		res.Output = out
		res.Mutated = true
		return res

	case "delete_file":
		var a struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(call.Arguments, &a); err != nil || a.Path == "" {
			return errResult(res, errors.New("delete_file requires path"))
		}
		out, err := w.deleteFile(a.Path)
		if err != nil {
			return errResult(res, err)
		}
		w.invalidateGraph()
		res.Output = out
		res.Mutated = true
		return res

	case "list_files":
		var a struct {
			Dir string `json:"dir"`
		}
		_ = json.Unmarshal(call.Arguments, &a)
		out, err := w.listFiles(a.Dir)
		if err != nil {
			return errResult(res, err)
		}
		res.Output = out
		return res

	case "search_text":
		var a struct {
			Query string `json:"query"`
			Dir   string `json:"dir"`
		}
		if err := json.Unmarshal(call.Arguments, &a); err != nil || strings.TrimSpace(a.Query) == "" {
			return errResult(res, errors.New("search_text requires non-empty query"))
		}
		out, err := w.searchText(a.Query, a.Dir)
		if err != nil {
			return errResult(res, err)
		}
		res.Output = out
		return res

	case "git_diff":
		txt, err := worktreeUnifiedDiff(ctx, w.Root)
		if err != nil {
			return errResult(res, err)
		}
		if len(txt) > maxReadBytes {
			txt = txt[:maxReadBytes] + "\n... [diff truncated] ..."
		}
		if strings.TrimSpace(txt) == "" {
			txt = "(no uncommitted diff)"
		}
		res.Output = txt
		return res

	case "run_command":
		if !w.AllowCommand {
			return errResult(res, errors.New("run_command is disabled in read-only mode"))
		}
		var a struct {
			Argv           []string `json:"argv"`
			TimeoutSeconds int      `json:"timeout_seconds"`
		}
		if err := json.Unmarshal(call.Arguments, &a); err != nil || len(a.Argv) == 0 {
			return errResult(res, errors.New("run_command requires non-empty argv array"))
		}
		if reason, blocked := isBlockedCommand(a.Argv); blocked {
			return errResult(res, fmt.Errorf("command blocked by Fabric safety policy: %s", reason))
		}
		timeout := 90 * time.Second
		if a.TimeoutSeconds > 0 && a.TimeoutSeconds <= 300 {
			timeout = time.Duration(a.TimeoutSeconds) * time.Second
		}
		execRes, err := sandbox.Run(ctx, w.Sandbox, w.Root, a.Argv, timeout)
		if err != nil {
			return errResult(res, err)
		}
		b, _ := json.Marshal(execRes)
		res.Output = string(b)
		res.IsError = !execRes.Passed
		return res

	default:
		return errResult(res, fmt.Errorf("unknown tool %q", call.Name))
	}
}

func (w *WorkspaceTools) getGraph() (*codegraph.Graph, error) {
	w.graphMu.Lock()
	defer w.graphMu.Unlock()
	if w.graph != nil {
		return w.graph, nil
	}
	g, err := codegraph.Build(w.Root)
	if err != nil {
		return nil, err
	}
	w.graph = g
	return g, nil
}

func (w *WorkspaceTools) invalidateGraph() {
	w.graphMu.Lock()
	w.graph = nil
	w.graphMu.Unlock()
}

var blockedBinaries = map[string]bool{
	"rm": true, "del": true, "erase": true, "rmdir": true, "rd": true,
	"remove-item": true, "ri": true, "format": true, "mkfs": true, "dd": true,
	"diskpart": true, "shutdown": true, "reboot": true, "sudo": true, "su": true,
	"runas": true, "reg": true, "schtasks": true, "crontab": true,
	"curl": true, "wget": true, "iwr": true, "irm": true,
	"invoke-webrequest": true, "invoke-restmethod": true, "invoke-expression": true, "iex": true,
	"ssh": true, "scp": true, "sftp": true, "rsync": true,
	"nc": true, "ncat": true, "netcat": true, "socat": true, "telnet": true,
}

var blockedGitSubcmds = map[string]bool{
	"push": true, "remote": true, "config": true, "gc": true, "prune": true,
	"clean": true, "worktree": true, "filter-branch": true, "filter-repo": true,
}

func isBlockedCommand(argv []string) (string, bool) {
	if len(argv) == 0 {
		return "empty argv", true
	}
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(argv[0]), ".exe"))
	if blockedBinaries[base] {
		return "blocked executable " + base, true
	}
	if base == "sh" || base == "bash" || base == "zsh" || base == "cmd" || base == "powershell" || base == "pwsh" {
		for _, tok := range argv[1:] {
			low := strings.ToLower(tok)
			if low == "-c" || low == "/c" || low == "-command" || strings.HasPrefix(low, "-enc") || low == "-e" {
				return fmt.Sprintf("inline shell execution %s %s is not permitted; use direct argv", base, tok), true
			}
		}
	}
	if base == "git" && len(argv) >= 2 {
		subIdx := firstGitSubcommand(argv)
		if subIdx > 0 {
			sub := strings.ToLower(argv[subIdx])
			if blockedGitSubcmds[sub] {
				return "blocked git subcommand: git " + sub, true
			}
			rest := argv[subIdx+1:]
			if sub == "reset" && (slices.Contains(rest, "--hard") || slices.Contains(rest, "--merge")) {
				return "blocked destructive git reset", true
			}
			if sub == "checkout" && (slices.Contains(rest, "-f") || slices.Contains(rest, "--force") || slices.Contains(rest, ".")) {
				return "blocked destructive git checkout", true
			}
			if sub == "branch" && slices.Contains(rest, "-D") {
				return "blocked destructive git branch -D", true
			}
		}
	}
	return "", false
}

func firstGitSubcommand(argv []string) int {
	for i := 1; i < len(argv); i++ {
		arg := argv[i]
		if arg == "-C" || arg == "-c" || arg == "--git-dir" || arg == "--work-tree" {
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return i
	}
	return -1
}

// worktreeUnifiedDiff computes the full diff (tracked + staged + untracked files)
// without touching workDir/.git/index.
func worktreeUnifiedDiff(ctx context.Context, workDir string) (string, error) {
	if diffBytes, err := gitexec.DiffWorktreeWithTempIndex(ctx, workDir, "HEAD"); err == nil {
		return string(diffBytes), nil
	}
	b, err := gitexec.CommandContext(ctx, "-C", workDir, "diff").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}
	return string(b), nil
}

func (w *WorkspaceTools) resolvePath(rel string, mustExist bool) (string, string, error) {
	cleaned := filepath.Clean(strings.TrimSpace(rel))
	if cleaned == "" || cleaned == "." {
		return w.Root, ".", nil
	}
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "", "", errors.New("path must stay inside isolated workspace")
	}
	slash := filepath.ToSlash(cleaned)
	for _, seg := range strings.Split(slash, "/") {
		if seg == ".." || seg == ".git" || seg == ".harness" || seg == ".fabric" {
			return "", "", fmt.Errorf("protected path denied: %s", rel)
		}
	}
	full := filepath.Join(w.Root, cleaned)
	if mustExist {
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil {
			return "", "", fmt.Errorf("path unavailable: %w", err)
		}
		relCheck, err := filepath.Rel(w.Root, resolved)
		if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
			return "", "", errors.New("symlink escapes isolated workspace")
		}
		return resolved, slash, nil
	}
	parent := filepath.Dir(full)
	if _, err := os.Stat(parent); err == nil {
		if resolvedParent, err := filepath.EvalSymlinks(parent); err == nil {
			relCheck, err := filepath.Rel(w.Root, resolvedParent)
			if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
				return "", "", errors.New("parent symlink escapes isolated workspace")
			}
		}
	}
	return full, slash, nil
}

func (w *WorkspaceTools) checkWriteScope(slash string) error {
	roots := w.AllowedWrite
	if len(w.Capabilities.WriteRoots) > 0 {
		roots = w.Capabilities.WriteRoots
	}
	if len(roots) == 0 {
		return nil
	}
	for _, allowed := range roots {
		norm := filepath.ToSlash(filepath.Clean(allowed))
		if norm == slash || strings.HasPrefix(slash, strings.TrimSuffix(norm, "/")+"/") {
			return nil
		}
	}
	return fmt.Errorf("write denied: %q is outside declared write ownership %v", slash, roots)
}

func (w *WorkspaceTools) readFile(rel string, startLine, endLine int) (string, error) {
	full, slash, err := w.resolvePath(rel, true)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("path is a directory; use list_files")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("binary file cannot be read")
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if startLine < 1 {
		startLine = 1
	}
	if endLine <= 0 || endLine > len(lines) {
		endLine = len(lines)
	}
	if startLine > len(lines) {
		return "", fmt.Errorf("start_line %d exceeds total lines %d", startLine, len(lines))
	}
	var b strings.Builder
	truncated := false
	for i := startLine; i <= endLine; i++ {
		ln := fmt.Sprintf("%d: %s\n", i, lines[i-1])
		if b.Len()+len(ln) > maxReadBytes {
			truncated = true
			break
		}
		b.WriteString(ln)
	}
	out, _ := json.Marshal(map[string]any{
		"path": slash, "total_lines": len(lines), "truncated": truncated, "content": b.String(),
	})
	return string(out), nil
}

func (w *WorkspaceTools) editFile(ctx context.Context, rel, oldStr, newStr string) (string, error) {
	if oldStr == newStr {
		return "", errors.New("no-op edit rejected: old_string equals new_string")
	}
	full, slash, err := w.resolvePath(rel, true)
	if err != nil {
		return "", err
	}
	if err := w.checkWriteScope(slash); err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	src := string(data)
	hasCRLF := strings.Contains(src, "\r\n")
	count := strings.Count(src, oldStr)
	useNormalized := false
	normSrc := src
	normOld := oldStr
	normNew := newStr
	if count == 0 && hasCRLF {
		normSrc = strings.ReplaceAll(src, "\r\n", "\n")
		normOld = strings.ReplaceAll(oldStr, "\r\n", "\n")
		normNew = strings.ReplaceAll(newStr, "\r\n", "\n")
		count = strings.Count(normSrc, normOld)
		useNormalized = count == 1
	}
	if count == 0 {
		return "", fmt.Errorf("anchor not found in %s; re-read exact file bytes before editing", slash)
	}
	if count > 1 {
		return "", fmt.Errorf("anchor matched %d times in %s; include more surrounding context so it is unique", count, slash)
	}
	if w.OnPreMutate != nil {
		if err := w.OnPreMutate("pre-edit " + slash); err != nil {
			return "", err
		}
	}
	var updated string
	if useNormalized {
		updated = strings.Replace(normSrc, normOld, normNew, 1)
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	} else {
		updated = strings.Replace(src, oldStr, newStr, 1)
	}
	if len(updated) > maxWriteBytes {
		return "", errors.New("edited file exceeds size bound")
	}
	if err := os.WriteFile(full, []byte(updated), 0o600); err != nil {
		return "", err
	}
	payload := map[string]any{"status": "EDITED", "path": slash, "bytes": len(updated)}
	if w.EnableDiagnostics {
		if diags := diagnostics.CheckFile(ctx, w.Root, slash); len(diags) > 0 {
			payload["diagnostics"] = diags
		}
	}
	out, _ := json.Marshal(payload)
	return string(out), nil
}

func (w *WorkspaceTools) writeFile(ctx context.Context, rel, content string) (string, error) {
	if len(content) > maxWriteBytes || !utf8.ValidString(content) {
		return "", errors.New("invalid or oversized UTF-8 file content")
	}
	full, slash, err := w.resolvePath(rel, false)
	if err != nil {
		return "", err
	}
	if err := w.checkWriteScope(slash); err != nil {
		return "", err
	}
	if w.OnPreMutate != nil {
		if err := w.OnPreMutate("pre-write " + slash); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		return "", err
	}
	payload := map[string]any{"status": "WRITTEN", "path": slash, "bytes": len(content)}
	if w.EnableDiagnostics {
		if diags := diagnostics.CheckFile(ctx, w.Root, slash); len(diags) > 0 {
			payload["diagnostics"] = diags
		}
	}
	out, _ := json.Marshal(payload)
	return string(out), nil
}

func (w *WorkspaceTools) deleteFile(rel string) (string, error) {
	full, slash, err := w.resolvePath(rel, true)
	if err != nil {
		return "", err
	}
	if err := w.checkWriteScope(slash); err != nil {
		return "", err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("delete_file refuses directories")
	}
	if w.OnPreMutate != nil {
		if err := w.OnPreMutate("pre-delete " + slash); err != nil {
			return "", err
		}
	}
	if err := os.Remove(full); err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"status": "DELETED", "path": slash})
	return string(out), nil
}

func (w *WorkspaceTools) listFiles(dir string) (string, error) {
	base, _, err := w.resolvePath(dir, true)
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
			if name == ".git" || name == ".harness" || name == ".fabric" || name == "node_modules" || name == "target" || name == "vendor" || name == "dist" || name == ".next" || name == "__pycache__" || name == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}
		if len(files) >= maxListEntries {
			truncated = true
			return fs.SkipAll
		}
		if rel, err := filepath.Rel(w.Root, p); err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	out, _ := json.Marshal(map[string]any{"files": files, "truncated": truncated})
	return string(out), nil
}

func (w *WorkspaceTools) searchText(query, dir string) (string, error) {
	base, _, err := w.resolvePath(dir, true)
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
			if name == ".git" || name == ".harness" || name == ".fabric" || name == "node_modules" || name == "target" || name == "vendor" || name == "dist" || name == ".next" || name == "__pycache__" || name == ".venv" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxSearchFileBytes {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()

		var probe [binarySniffBytes]byte
		n, readErr := f.Read(probe[:])
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil
		}
		if bytes.IndexByte(probe[:n], 0) >= 0 {
			return nil
		}

		rel, _ := filepath.Rel(w.Root, p)
		sc := bufio.NewScanner(io.MultiReader(bytes.NewReader(probe[:n]), f))
		sc.Buffer(make([]byte, 0, 64<<10), maxSearchLineBytes)
		ln := 0
		for sc.Scan() {
			ln++
			txt := sc.Text()
			if strings.Contains(txt, query) {
				hits = append(hits, hit{Path: filepath.ToSlash(rel), Line: ln, Text: truncateUTF8(txt, 260)})
				if len(hits) >= maxSearchHits {
					truncated = true
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	out, _ := json.Marshal(map[string]any{"query": query, "matches": hits, "truncated": truncated})
	return string(out), nil
}

func truncateUTF8(s string, maxRunes int) string {
	if len(s) <= maxRunes {
		return s
	}
	count := 0
	for idx := range s {
		if count == maxRunes {
			return s[:idx] + "..."
		}
		count++
	}
	return s
}

func errResult(res ToolResult, err error) ToolResult {
	b, _ := json.Marshal(map[string]string{"error": err.Error()})
	res.Output = string(b)
	res.IsError = true
	return res
}

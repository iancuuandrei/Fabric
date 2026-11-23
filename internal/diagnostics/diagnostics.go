package diagnostics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultCheckTimeout = 8 * time.Second
	maxDiagOutputBytes  = 4096
)

// Diagnostic reports a single syntax, parse, or static check issue found
// immediately after editing a source file.
type Diagnostic struct {
	Path     string `json:"path"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"` // error | warning
	Message  string `json:"message"`
	Source   string `json:"source"` // go/parser | go-vet | json | py_compile | tsc
}

// CheckFile performs a fast, bounded, best-effort syntax/type diagnostic pass
// on relPath inside workDir. Missing external language tools never fail edits.
func CheckFile(ctx context.Context, workDir, relPath string) []Diagnostic {
	slash := filepath.ToSlash(filepath.Clean(relPath))
	full := filepath.Join(workDir, filepath.FromSlash(slash))
	ext := strings.ToLower(filepath.Ext(slash))

	runCtx, cancel := context.WithTimeout(ctx, defaultCheckTimeout)
	defer cancel()

	switch ext {
	case ".go":
		return checkGoFile(runCtx, workDir, slash, full)
	case ".json":
		return checkJSONFile(slash, full)
	case ".py":
		return checkPythonFile(runCtx, workDir, slash, full)
	case ".ts", ".tsx":
		return checkTypeScript(runCtx, workDir, slash)
	default:
		return nil
	}
}

func checkGoFile(ctx context.Context, workDir, slash, full string) []Diagnostic {
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil
	}
	fset := token.NewFileSet()
	_, parseErr := parser.ParseFile(fset, slash, raw, parser.DeclarationErrors)
	if parseErr != nil {
		var diags []Diagnostic
		var errList scanner.ErrorList
		if errors.As(parseErr, &errList) {
			for _, e := range errList {
				diags = append(diags, Diagnostic{
					Path:     slash,
					Line:     e.Pos.Line,
					Severity: "error",
					Message:  e.Msg,
					Source:   "go/parser",
				})
				if len(diags) >= 8 {
					break
				}
			}
			return diags
		}
		return []Diagnostic{{
			Path:     slash,
			Severity: "error",
			Message:  parseErr.Error(),
			Source:   "go/parser",
		}}
	}

	// Only run bounded `go vet` if `go` is resolvable and syntax is valid.
	goBin, err := exec.LookPath("go")
	if err != nil {
		return nil
	}
	if abs, absErr := filepath.Abs(goBin); absErr == nil {
		goBin = abs
	}
	// Verify go.mod exists before running package vet to avoid module resolution failures on scratch snippets.
	if _, err := os.Stat(filepath.Join(workDir, "go.mod")); err != nil {
		return nil
	}
	pkgRel := filepath.ToSlash(filepath.Dir(slash))
	target := "./"
	if pkgRel != "." && pkgRel != "" {
		target = "./" + pkgRel
	}
	cmd := exec.CommandContext(ctx, goBin, "vet", target)
	cmd.Dir = workDir
	out, vetErr := cmd.CombinedOutput()
	if vetErr == nil {
		return nil
	}
	txt := strings.TrimSpace(string(out))
	if len(txt) > maxDiagOutputBytes {
		txt = txt[:maxDiagOutputBytes] + "\n... [truncated]"
	}
	if txt == "" {
		return nil
	}
	return []Diagnostic{{
		Path:     slash,
		Severity: "warning",
		Message:  txt,
		Source:   "go-vet",
	}}
}

func checkJSONFile(slash, full string) []Diagnostic {
	raw, err := os.ReadFile(full)
	if err != nil {
		return nil
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || json.Valid(trimmed) {
		return nil
	}
	var dummy any
	err = json.Unmarshal(trimmed, &dummy)
	msg := "invalid JSON"
	if err != nil {
		msg = err.Error()
	}
	return []Diagnostic{{
		Path:     slash,
		Severity: "error",
		Message:  msg,
		Source:   "json",
	}}
}

func checkPythonFile(ctx context.Context, workDir, slash, full string) []Diagnostic {
	pyExe := ""
	for _, candidate := range []string{"python3", "python"} {
		if p, err := exec.LookPath(candidate); err == nil {
			pyExe = p
			break
		}
	}
	if pyExe == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, pyExe, "-m", "py_compile", full)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if len(msg) > maxDiagOutputBytes {
		msg = msg[:maxDiagOutputBytes] + "..."
	}
	if msg == "" {
		msg = fmt.Sprintf("py_compile failed: %v", err)
	}
	return []Diagnostic{{
		Path:     slash,
		Severity: "error",
		Message:  msg,
		Source:   "py_compile",
	}}
}

func checkTypeScript(ctx context.Context, workDir, slash string) []Diagnostic {
	if _, err := os.Stat(filepath.Join(workDir, "tsconfig.json")); err != nil {
		return nil
	}
	tscPath, err := exec.LookPath("tsc")
	if err != nil {
		localTSC := filepath.Join(workDir, "node_modules", ".bin", "tsc")
		if _, statErr := os.Stat(localTSC); statErr != nil {
			return nil
		}
		tscPath = localTSC
	}
	cmd := exec.CommandContext(ctx, tscPath, "--noEmit", "--pretty", "false")
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	txt := strings.TrimSpace(string(out))
	if len(txt) > maxDiagOutputBytes {
		txt = txt[:maxDiagOutputBytes] + "..."
	}
	if txt == "" {
		return nil
	}
	return []Diagnostic{{
		Path:     slash,
		Severity: "error",
		Message:  txt,
		Source:   "tsc",
	}}
}

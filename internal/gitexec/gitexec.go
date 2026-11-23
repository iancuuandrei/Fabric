// Package gitexec resolves Git through fixed, conventional installation paths
// or an explicit operator override. It never searches PATH.
package gitexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ExecutableEnv names the explicit operator override for a nonstandard Git
// installation. The override must be an absolute path to a regular file.
const ExecutableEnv = "FABRIC_GIT_EXECUTABLE"

// DefaultCandidates reports fixed conventional system installation paths for
// the given GOOS. PATH is never consulted.
func DefaultCandidates(goos string) []string {
	switch goos {
	case "windows":
		return []string{
			"C:/Program Files/Git/cmd/git.exe",
			"C:/Program Files/Git/bin/git.exe",
		}
	case "darwin":
		return []string{
			"/usr/bin/git",
			"/opt/homebrew/bin/git",
			"/usr/local/bin/git",
		}
	case "linux":
		return []string{
			"/usr/bin/git",
			"/bin/git",
		}
	default:
		return nil
	}
}

// ResolveExecutable resolves Git without searching PATH. An explicit override
// is validated before fallback and an invalid override fails closed. Both the
// override and fallback candidates must resolve to absolute regular files.
func ResolveExecutable(getenv func(string) string, stat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error), candidates []string) (string, error) {
	if getenv != nil {
		if override := getenv(ExecutableEnv); override != "" {
			if !filepath.IsAbs(override) {
				return "", fmt.Errorf("%s must be an absolute path: %q", ExecutableEnv, override)
			}
			info, err := stat(override)
			if err != nil {
				return "", fmt.Errorf("%s not accessible %q: %w", ExecutableEnv, override, err)
			}
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("%s is not a regular file: %q", ExecutableEnv, override)
			}
			resolved, err := evalSymlinks(override)
			if err != nil {
				return "", fmt.Errorf("%s could not be resolved %q: %w", ExecutableEnv, override, err)
			}
			if !filepath.IsAbs(resolved) {
				return "", fmt.Errorf("%s resolved path is not absolute: %q", ExecutableEnv, resolved)
			}
			if resolved != override {
				resolvedInfo, err := stat(resolved)
				if err != nil {
					return "", fmt.Errorf("%s target not accessible %q: %w", ExecutableEnv, resolved, err)
				}
				if !resolvedInfo.Mode().IsRegular() {
					return "", fmt.Errorf("%s target is not a regular file: %q", ExecutableEnv, resolved)
				}
			}
			return resolved, nil
		}
	}
	for _, candidate := range candidates {
		if !filepath.IsAbs(candidate) {
			continue
		}
		info, err := stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		resolved, err := evalSymlinks(candidate)
		if err != nil || !filepath.IsAbs(resolved) {
			continue
		}
		if resolved != candidate {
			resolvedInfo, err := stat(resolved)
			if err != nil || !resolvedInfo.Mode().IsRegular() {
				continue
			}
		}
		return resolved, nil
	}
	return "", errors.New("git executable not found at conventional system paths and no valid FABRIC_GIT_EXECUTABLE override is set")
}

var resolveOnce = sync.OnceValues(func() (string, error) {
	return ResolveExecutable(os.Getenv, os.Stat, filepath.EvalSymlinks, DefaultCandidates(runtime.GOOS))
})

// Resolve returns the production executable path, cached after the first
// resolution. It never invokes PATH lookup.
func Resolve() (string, error) { return resolveOnce() }

// CommandContext creates a Git command using the resolved absolute executable.
// When resolution fails, it returns a command whose Start/Run returns that
// error without starting a child process. This preserves normal call-site
// error handling while keeping resolution failure before execution.
func CommandContext(ctx context.Context, args ...string) *exec.Cmd {
	path, err := Resolve()
	if err != nil {
		return &exec.Cmd{Path: "git", Args: append([]string{"git"}, args...), Err: err}
	}
	command := exec.CommandContext(ctx, path, args...)
	// The prior `exec.CommandContext(ctx, "git", ...)` call sites passed the
	// conventional name as argv[0]. Keep that observable argv while pinning
	// execution itself to the resolved absolute path.
	command.Args[0] = "git"
	return command
}

// DiffWorktreeWithTempIndex computes the complete diff against baseRef (including
// untracked files) using an isolated temporary index so workDir/.git/index is
// never modified.
func DiffWorktreeWithTempIndex(ctx context.Context, workDir, baseRef string, extraDiffArgs ...string) ([]byte, error) {
	if baseRef == "" {
		baseRef = "HEAD"
	}
	tmpDir, err := os.MkdirTemp("", "fabric-git-idx-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	env := append(os.Environ(), "GIT_INDEX_FILE="+filepath.Join(tmpDir, "index"))
	runEnv := func(args ...string) ([]byte, error) {
		cmd := CommandContext(ctx, append([]string{"-C", workDir}, args...)...)
		cmd.Env = env
		return cmd.CombinedOutput()
	}
	if out, err := runEnv("read-tree", baseRef); err != nil {
		return nil, fmt.Errorf("read-tree %s: %w: %s", baseRef, err, strings.TrimSpace(string(out)))
	}
	if out, err := runEnv("add", "-A"); err != nil {
		return nil, fmt.Errorf("stage temp index: %w: %s", err, strings.TrimSpace(string(out)))
	}
	args := []string{"diff", "--cached"}
	args = append(args, extraDiffArgs...)
	args = append(args, baseRef, "--")
	out, err := runEnv(args...)
	if err != nil {
		return nil, fmt.Errorf("git diff temp index: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

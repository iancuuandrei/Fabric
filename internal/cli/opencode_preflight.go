package cli

import (
	"errors"
	"path/filepath"

	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

// openCodeStateRootError is a safe preflight diagnostic. Its message carries
// only the failed stage and the legal next action; the underlying cause is
// retained for errors.Is/As without printing paths, credentials, provider
// detail or prompts.
type openCodeStateRootError struct {
	cause error
}

// Error reports only the failed stage and the legal next action. The
// underlying cause is retained for errors.Is and errors.As without printing
// paths, credentials, provider detail or prompts.
func (e *openCodeStateRootError) Error() string {
	return "OpenCode state root preflight failed at opencode-state-root (missing_or_unsafe): create or configure a safe private directory before dispatch"
}

// Unwrap preserves the cause for errors.Is and errors.As without printing it.
func (e *openCodeStateRootError) Unwrap() error { return e.cause }

// openCodeRoutesSelected reports whether any configured role dispatches
// through the OpenCode runtime. An unselected optional OpenCode host section
// never blocks other routes.
func openCodeRoutesSelected(cfg config.Config) bool {
	if cfg.Planner.Runtime == "opencode-http" {
		return true
	}
	if cfg.Writer != nil && cfg.Writer.Runtime == "opencode-http" {
		return true
	}
	if cfg.Fixer != nil && cfg.Fixer.Runtime == "opencode-http" {
		return true
	}
	if cfg.Explorer != nil && cfg.Explorer.Runtime == "opencode-http" {
		return true
	}
	if cfg.Reviewer != nil && cfg.Reviewer.Runtime == "opencode-http" {
		return true
	}
	return false
}

// requireOpenCodeStateRoot validates the existing safe absolute clean private
// state root for selected OpenCode routes before run creation, provider
// admission, doctor or inspect-plan. It never creates directories and never
// settles existing UNKNOWN runs; the controller runtime guard stays
// authoritative at dispatch.
func requireOpenCodeStateRoot(cfg config.Config) error {
	if !openCodeRoutesSelected(cfg) {
		return nil
	}
	host := cfg.OpenCode
	if host == nil {
		return &openCodeStateRootError{cause: errors.New("pinned OpenCode host configuration required")}
	}
	root := host.StateRoot
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return &openCodeStateRootError{cause: errors.New("OpenCode state root must be an absolute clean path")}
	}
	if err := safepath.Directory(root); err != nil {
		return &openCodeStateRootError{cause: err}
	}
	return nil
}

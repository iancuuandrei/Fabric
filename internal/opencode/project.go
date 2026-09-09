package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	projectMaxPathBytes    = 32 << 10
	projectMaxTextBytes    = 256 << 10
	projectMaxSandboxes    = 256
	projectMaxExactInteger = int64(1<<53 - 1)
)

// ErrProjectGlobalIdentity reports that an exact Git project selector resolved
// to OpenCode's global project. Callers may use it for a bounded pre-effect
// readiness check; it never authorizes a global fallback.
var ErrProjectGlobalIdentity = errors.New("OpenCode Git project reported global identity")

// ProjectReceipt is a safe projection of one strictly admitted
// /project/current response. SHA256 binds the complete response snapshot while
// omitting mutable display fields and local sandbox paths from the receipt.
// Directory records the exact workspace selector used for the read.
type ProjectReceipt struct {
	SHA256    string      `json:"sha256"`
	ID        string      `json:"id"`
	Directory string      `json:"directory"`
	Mode      ProjectMode `json:"mode"`
	Worktree  string      `json:"worktree"`
	VCS       string      `json:"vcs,omitempty"`
}

// ProjectMode makes the expected global-versus-Git identity explicit. The
// reader never falls back from one mode to the other.
type ProjectMode string

const (
	// ProjectModeGlobal admits the pinned global project identity used for an
	// isolated non-Git host directory.
	ProjectModeGlobal ProjectMode = "global"
	// ProjectModeGit admits one exact Git worktree identity.
	ProjectModeGit ProjectMode = "git"
)

// ProjectExpectation selects the request context and its exact expected
// identity. Worktree must be empty for global mode and an absolute clean path
// for Git mode.
type ProjectExpectation struct {
	Directory string
	Mode      ProjectMode
	Worktree  string
}

// ReadCurrentProject reads the current project without creating or updating it.
// Directory is sent through the pinned route's explicit workspace selector;
// the returned identity must then match the requested mode and worktree.
func (c *Client) ReadCurrentProject(ctx context.Context, expected ProjectExpectation) (ProjectReceipt, error) {
	if err := expected.validate(); err != nil {
		return ProjectReceipt{}, err
	}
	query := url.Values{"directory": []string{expected.Directory}}
	raw, err := c.read(ctx, "/project/current?"+query.Encode())
	if err != nil {
		return ProjectReceipt{}, err
	}
	return decodeCurrentProject(raw, expected)
}

func (expected ProjectExpectation) validate() error {
	if validateProjectPath(expected.Directory) != nil {
		return errors.New("invalid expected OpenCode project directory")
	}
	switch expected.Mode {
	case ProjectModeGlobal:
		if expected.Worktree != "" {
			return errors.New("global OpenCode project cannot specify a worktree")
		}
	case ProjectModeGit:
		if validateProjectPath(expected.Worktree) != nil {
			return errors.New("invalid expected OpenCode Git worktree")
		}
	default:
		return errors.New("invalid expected OpenCode project mode")
	}
	return nil
}

func decodeCurrentProject(raw []byte, expected ProjectExpectation) (ProjectReceipt, error) {
	if err := expected.validate(); err != nil {
		return ProjectReceipt{}, err
	}
	project, err := wireObject(raw)
	if err != nil {
		return ProjectReceipt{}, err
	}
	if !projectKeys(project, []string{"id", "worktree", "time", "sandboxes"}, "vcs", "name", "icon", "commands") {
		return ProjectReceipt{}, errors.New("OpenCode project shape mismatch")
	}

	var id, worktree string
	if field(project, "id", &id) != nil || !locator(id) || field(project, "worktree", &worktree) != nil {
		return ProjectReceipt{}, errors.New("OpenCode project identity mismatch")
	}

	var vcs string
	if value, ok := project["vcs"]; ok {
		if json.Unmarshal(value, &vcs) != nil || vcs != "git" {
			return ProjectReceipt{}, errors.New("OpenCode project VCS mismatch")
		}
	}
	transientGlobal := expected.Mode == ProjectModeGit && id == "global"
	if transientGlobal && (worktree != "/" || vcs != "") {
		return ProjectReceipt{}, errors.New("OpenCode Git project global identity shape mismatch")
	}
	if value, ok := project["name"]; ok {
		var name string
		if json.Unmarshal(value, &name) != nil || len(name) > projectMaxTextBytes {
			return ProjectReceipt{}, errors.New("invalid OpenCode project name")
		}
	}
	if value, ok := project["icon"]; ok {
		if err := validateProjectIcon(value); err != nil {
			return ProjectReceipt{}, err
		}
	}
	if value, ok := project["commands"]; ok {
		if err := validateProjectCommands(value); err != nil {
			return ProjectReceipt{}, err
		}
	}
	if err := validateProjectTime(project["time"]); err != nil {
		return ProjectReceipt{}, err
	}
	sandboxExpectation := expected
	if transientGlobal {
		sandboxExpectation = ProjectExpectation{Directory: expected.Directory, Mode: ProjectModeGlobal}
	}
	if err := validateProjectSandboxes(project["sandboxes"], sandboxExpectation, worktree); err != nil {
		return ProjectReceipt{}, err
	}
	if expected.Mode == ProjectModeGlobal {
		if id != "global" || worktree != "/" || vcs != "" {
			return ProjectReceipt{}, errors.New("OpenCode global project identity mismatch")
		}
	} else if transientGlobal {
		return ProjectReceipt{}, ErrProjectGlobalIdentity
	} else if worktree != expected.Worktree {
		return ProjectReceipt{}, errors.New("OpenCode Git project worktree mismatch")
	} else if vcs != "git" {
		return ProjectReceipt{}, errors.New("OpenCode Git project VCS mismatch")
	}

	digest := sha256.Sum256(raw)
	return ProjectReceipt{
		SHA256: hex.EncodeToString(digest[:]), ID: id, Directory: expected.Directory,
		Mode: expected.Mode, Worktree: worktree, VCS: vcs,
	}, nil
}

func validateProjectPath(value string) error {
	// The pinned middleware applies URL decoding after query parsing, so a
	// literal percent would make the selected directory ambiguous.
	if value == "" || len(value) > projectMaxPathBytes || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00%") ||
		!filepath.IsAbs(value) || filepath.Clean(value) != value {
		return errors.New("absolute clean OpenCode project path required")
	}
	return nil
}

func validateProjectTime(raw json.RawMessage) error {
	value, err := wireObject(raw)
	if err != nil || !projectKeys(value, []string{"created", "updated"}, "initialized") {
		return errors.New("OpenCode project time shape mismatch")
	}
	for _, key := range []string{"created", "updated"} {
		if _, err := projectNonNegativeInteger(value[key]); err != nil {
			return errors.New("invalid OpenCode project time")
		}
	}
	if initialized, ok := value["initialized"]; ok {
		if _, err := projectNonNegativeInteger(initialized); err != nil {
			return errors.New("invalid OpenCode project initialization time")
		}
	}
	return nil
}

func projectNonNegativeInteger(raw json.RawMessage) (int64, error) {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, errors.New("integer required")
	}
	value, err := number.Int64()
	if err != nil || value < 0 || value > projectMaxExactInteger {
		return 0, errors.New("bounded non-negative integer required")
	}
	return value, nil
}

func validateProjectSandboxes(raw json.RawMessage, expected ProjectExpectation, worktree string) error {
	var sandboxes []string
	if json.Unmarshal(raw, &sandboxes) != nil || sandboxes == nil || len(sandboxes) > projectMaxSandboxes {
		return errors.New("invalid OpenCode project sandboxes")
	}
	seen := make(map[string]struct{}, len(sandboxes))
	for _, sandbox := range sandboxes {
		if validateProjectPath(sandbox) != nil || sandbox == worktree {
			return errors.New("invalid OpenCode project sandbox path")
		}
		if _, exists := seen[sandbox]; exists {
			return errors.New("duplicate OpenCode project sandbox path")
		}
		seen[sandbox] = struct{}{}
	}
	if expected.Mode == ProjectModeGlobal {
		if len(sandboxes) != 0 {
			return errors.New("global OpenCode project has sandbox paths")
		}
		return nil
	}
	if expected.Directory != worktree {
		if _, ok := seen[expected.Directory]; !ok {
			return errors.New("OpenCode project directory is not in expected worktree")
		}
	}
	return nil
}

func validateProjectIcon(raw json.RawMessage) error {
	icon, err := wireObject(raw)
	if err != nil || !projectKeys(icon, nil, "url", "override", "color") {
		return errors.New("OpenCode project icon shape mismatch")
	}
	for _, key := range []string{"url", "override", "color"} {
		if value, ok := icon[key]; ok {
			var text string
			if json.Unmarshal(value, &text) != nil || len(text) > projectMaxTextBytes {
				return errors.New("invalid OpenCode project icon")
			}
		}
	}
	return nil
}

func validateProjectCommands(raw json.RawMessage) error {
	commands, err := wireObject(raw)
	if err != nil || !projectKeys(commands, nil, "start") {
		return errors.New("OpenCode project commands shape mismatch")
	}
	if value, ok := commands["start"]; ok {
		var start string
		if json.Unmarshal(value, &start) != nil || len(start) > projectMaxTextBytes {
			return errors.New("invalid OpenCode project start command")
		}
	}
	return nil
}

func projectKeys(value map[string]json.RawMessage, required []string, optional ...string) bool {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, key := range required {
		if _, ok := value[key]; !ok {
			return false
		}
		allowed[key] = struct{}{}
	}
	for _, key := range optional {
		allowed[key] = struct{}{}
	}
	for key := range value {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}

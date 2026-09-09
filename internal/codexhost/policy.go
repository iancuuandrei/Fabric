package codexhost

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/safepath"
)

var disabled = []string{"apps", "plugins", "remote_plugin", "hooks", "multi_agent", "multi_agent_v2", "shell_tool", "code_mode", "code_mode_host", "code_mode_only", "browser_use", "browser_use_external", "browser_use_full_cdp_access", "computer_use", "in_app_browser", "image_generation", "memories", "skill_search", "skill_mcp_dependency_install", "workspace_dependencies", "recommended_plugins", "goals", "tool_suggest", "view_image", "sleep_tool", "unbounded_connection_retries"}

// Launch identifies an immutable executable/configuration pair for one private home.
type Launch struct {
	Version    int    `json:"version"`
	Root       string `json:"root"`
	Binary     string `json:"binary"`
	BinaryHash string `json:"binary_hash"`
	ConfigHash string `json:"config_hash"`
}

func configuration() []byte {
	var b strings.Builder
	b.WriteString("approval_policy = \"never\"\nsandbox_mode = \"read-only\"\nweb_search = \"disabled\"\n[features]\n")
	for _, name := range disabled {
		b.WriteString(name + " = false\n")
	}
	b.WriteString("skip_host_skill_discovery = true\n")
	return []byte(b.String())
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func executableHash(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("absolute executable path required")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 512<<20 {
		return "", errors.New("unsupported executable type or size")
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (512<<20)+1))
	if err != nil {
		return "", err
	}
	if n > 512<<20 {
		return "", errors.New("executable exceeds size bound")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Prepare writes only into an existing empty control directory. It copies no
// user configuration, authentication material, plugins or repository contents.
func Prepare(root, binary string) (Launch, error) {
	l := Launch{Version: 1, Root: root, Binary: binary}
	if err := safepath.Directory(root); err != nil {
		return l, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return l, err
	}
	if len(entries) != 0 {
		return l, errors.New("host control directory must be empty")
	}
	l.BinaryHash, err = executableHash(binary)
	if err != nil {
		return l, err
	}
	if err := os.Mkdir(filepath.Join(root, "home"), 0700); err != nil {
		return l, err
	}
	if err := os.Mkdir(filepath.Join(root, "workspace"), 0700); err != nil {
		return l, err
	}
	b := configuration()
	f, err := os.OpenFile(filepath.Join(root, "home", "config.toml"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return l, err
	}
	_, writeErr := f.Write(b)
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return l, err
	}
	l.ConfigHash = digest(b)
	return l, l.Validate()
}

// Validate rejects changed binaries, configuration and aliased control directories.
func (l Launch) Validate() error {
	if l.Version != 1 {
		return errors.New("unsupported host launch version")
	}
	for _, path := range []string{l.Root, filepath.Join(l.Root, "home"), filepath.Join(l.Root, "workspace")} {
		if err := safepath.Directory(path); err != nil {
			return err
		}
	}
	h, err := executableHash(l.Binary)
	if err != nil {
		return err
	}
	if h != l.BinaryHash {
		return errors.New("host executable drift")
	}
	root, err := os.OpenRoot(filepath.Join(l.Root, "home"))
	if err != nil {
		return err
	}
	defer root.Close()
	configHash, _, _, exists, err := safepath.ReadRegular(root, "config.toml", 64<<10)
	if err != nil {
		return err
	}
	if !exists || configHash != l.ConfigHash || l.ConfigHash != digest(configuration()) {
		return errors.New("host configuration drift")
	}
	return nil
}

// ID derives the launch identity after structural and filesystem validation.
func (l Launch) ID() (string, error) {
	if err := l.Validate(); err != nil {
		return "", err
	}
	return l.BindingID()
}

// Expected constructs the fixed host-policy identity without creating files.
func Expected(root, binary, binaryHash string) (Launch, error) {
	l := Launch{Version: 1, Root: root, Binary: binary, BinaryHash: binaryHash, ConfigHash: digest(configuration())}
	_, err := l.BindingID()
	return l, err
}

// BindingID validates semantic identity without inspecting mutable filesystem state.
// Replay uses this method; process admission additionally requires Validate.
func (l Launch) BindingID() (string, error) {
	if l.Version != 1 || !filepath.IsAbs(l.Root) || !filepath.IsAbs(l.Binary) || l.ConfigHash != digest(configuration()) {
		return "", errors.New("invalid host policy identity")
	}
	if err := safepath.RequireDigest(l.BinaryHash); err != nil {
		return "", err
	}
	return canonical.Hash("harness.codex-host.v1", l)
}

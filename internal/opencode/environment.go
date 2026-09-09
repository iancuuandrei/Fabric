package opencode

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
)

// HostEnvironment constructs a private-state bootstrap environment from a small
// OS allowlist. It excludes ambient provider credentials, proxies, OpenCode flags
// and telemetry settings. The caller must create and verify private directories,
// add only admitted credentials, and qualify the pinned executable before launch.
// These settings alone do not establish a sandbox or provider token enforcement.
func HostEnvironment(root string, inherited []string) ([]string, error) {
	return hostEnvironment(root, inherited, `{"share":"disabled","plugin":[],"permission":"deny"}`)
}

// HostEnvironmentWithTools constructs the same private environment with one
// validated controller-owned MCP server. It intentionally accepts no raw
// configuration or arbitrary environment entries.
func HostEnvironmentWithTools(root string, inherited []string, tools ToolsConfigurationSpec) ([]string, error) {
	content, err := BuildToolsConfiguration(tools)
	if err != nil {
		return nil, err
	}
	return hostEnvironment(root, inherited, content)
}

func hostEnvironment(root string, inherited []string, configContent string) ([]string, error) {
	if !filepath.IsAbs(root) || strings.ContainsRune(root, 0) {
		return nil, errors.New("absolute private host root required")
	}
	values := map[string]string{}
	for _, entry := range inherited {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		key = strings.ToUpper(key)
		switch key {
		case "SYSTEMROOT", "WINDIR", "PATH", "PATHEXT", "COMSPEC":
			if _, exists := values[key]; exists {
				return nil, errors.New("ambiguous inherited system environment")
			}
			values[key] = value
		}
	}
	for _, key := range []string{"HOME", "USERPROFILE", "OPENCODE_TEST_HOME"} {
		values[key] = filepath.Join(root, "home")
	}
	for key, dir := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_CACHE_HOME": "cache", "XDG_STATE_HOME": "state", "TMP": "tmp", "TEMP": "tmp", "TMPDIR": "tmp"} {
		values[key] = filepath.Join(root, dir)
	}
	for _, key := range []string{"OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_DISABLE_DEFAULT_PLUGINS", "OPENCODE_DISABLE_EXTERNAL_SKILLS", "OPENCODE_DISABLE_CLAUDE_CODE", "OPENCODE_DISABLE_LSP_DOWNLOAD", "OPENCODE_DISABLE_AUTOUPDATE", "OPENCODE_DISABLE_MODELS_FETCH", "OPENCODE_DISABLE_AUTOCOMPACT", "OPENCODE_DISABLE_PRUNE", "OPENCODE_EXPERIMENTAL_DISABLE_FILEWATCHER"} {
		values[key] = "true"
	}
	values["OPENCODE_AUTO_SHARE"] = "false"
	values["OPENCODE_EXPERIMENTAL"] = "false"
	values["OPENCODE_CONFIG_CONTENT"] = configContent
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out, nil
}

package opencode

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestHostEnvironmentExcludesAmbientAuthority(t *testing.T) {
	root := t.TempDir()
	env, err := HostEnvironment(root, []string{"PATH=toolchain", "OPENAI_API_KEY=secret", "HTTP_PROXY=http://proxy", "OPENCODE_AUTO_SHARE=true", "OPENCODE_CONFIG_DIR=foreign", "HOME=foreign", "OTEL_EXPORTER_OTLP_HEADERS=secret"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"secret", "foreign", "http://proxy", "OPENCODE_AUTO_SHARE=true"} {
		if strings.Contains(joined, forbidden) {
			t.Fatal("ambient authority survived")
		}
	}
	for _, required := range []string{"OPENCODE_DISABLE_PROJECT_CONFIG=true", "OPENCODE_TEST_HOME=" + filepath.Join(root, "home"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "PATH=toolchain", `"share":"disabled"`} {
		if !strings.Contains(joined, required) {
			t.Fatal("required bootstrap setting absent")
		}
	}
	if _, err := HostEnvironment(root, []string{"PATH=a", "Path=b"}); err == nil {
		t.Fatal("ambiguous PATH admitted")
	}
}

func TestHostEnvironmentWithToolsUsesOnlyValidatedConfiguration(t *testing.T) {
	root := t.TempDir()
	spec := ToolsConfigurationSpec{Endpoint: "http://127.0.0.1:43123/mcp", Bearer: strings.Repeat("a", 32), ToolNames: []string{"read", "search"}, TimeoutMillis: 5000}
	env, err := HostEnvironmentWithTools(root, []string{"PATH=toolchain", "OPENAI_API_KEY=ambient-secret", "OPENCODE_CONFIG_CONTENT=foreign"}, spec)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, required := range []string{spec.Endpoint, spec.Bearer, `"engorch"`, `"share":"disabled"`} {
		if !strings.Contains(joined, required) {
			t.Fatal("validated tools configuration missing")
		}
	}
	for _, forbidden := range []string{"ambient-secret", "OPENCODE_CONFIG_CONTENT=foreign"} {
		if strings.Contains(joined, forbidden) {
			t.Fatal("ambient configuration survived")
		}
	}
	defaults, err := HostEnvironment(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(defaults, "\n"), spec.Endpoint) || strings.Contains(strings.Join(defaults, "\n"), `"mcp"`) {
		t.Fatal("default deny-only environment gained MCP authority")
	}
	invalid := spec
	invalid.Endpoint = "https://example.com/mcp"
	if env, err := HostEnvironmentWithTools(root, nil, invalid); err == nil || env != nil {
		t.Fatal("invalid tools configuration was admitted")
	}
}

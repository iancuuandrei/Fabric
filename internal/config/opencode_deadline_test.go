package config

import (
	"bytes"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providergateway"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCodeDeadlinesAreBoundedAndIdentityBound(t *testing.T) {
	c := providerBackedConfig("opencode-http", "engorch-openai")
	role := c.Provider.Roles["planner"]
	role.RequiredCapabilities = &ProviderRequiredCapabilities{Tools: true, StructuredOutput: providergateway.StructuredOutputUnsupported}
	c.Provider.Roles["planner"] = role
	c.OpenCode = &OpenCodeHost{Version: 1, Executable: filepath.Join(t.TempDir(), "opencode.exe"), ExecutableHash: strings.Repeat("a", 64), StateRoot: t.TempDir()}
	before, err := c.ID()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Bytes(c.OpenCode)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("timeout_seconds")) {
		t.Fatal("defaults changed historical JSON")
	}
	c.OpenCode.InvocationTimeoutSeconds = 7200
	c.OpenCode.ReadbackTimeoutSeconds = 300
	after, err := c.ID()
	if err != nil || after == before {
		t.Fatal("explicit deadlines not identity bound", err)
	}
	for _, pair := range [][2]int{{-1, 30}, {7201, 30}, {900, -1}, {900, 301}} {
		c.OpenCode.InvocationTimeoutSeconds = pair[0]
		c.OpenCode.ReadbackTimeoutSeconds = pair[1]
		if c.Validate() == nil {
			t.Fatal("invalid deadline admitted", pair)
		}
	}
}

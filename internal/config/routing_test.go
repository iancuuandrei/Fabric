package config

import (
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/runtime"
)

func TestExplicitRoleRouting(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Route("writer"); err == nil {
		t.Fatal("planner silently borrowed for writer")
	}
	legacy, err := canonical.Bytes(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacy), `"writer"`) {
		t.Fatal("absent role changed legacy serialization")
	}
	var decoded Config
	if err := canonical.Decode(legacy, &decoded); err != nil {
		t.Fatal("legacy config no longer readable", err)
	}
	before, err := c.ID()
	if err != nil {
		t.Fatal(err)
	}
	c.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer-fixture", Effort: "high", Role: "writer"}
	configured, err := c.ID()
	if err != nil || configured == before {
		t.Fatal("writer routing not identity bound", err)
	}
	route, err := c.Route("writer")
	if err != nil || route != *c.Writer {
		t.Fatal("writer route mismatch", err)
	}
	route.Model = "changed-copy"
	if c.Writer.Model != "writer-fixture" {
		t.Fatal("route leaked mutable profile")
	}
	c.Writer.Effort = "low"
	changed, err := c.ID()
	if err != nil || changed == configured {
		t.Fatal("effort change not bound", err)
	}
	c.Writer.Role = "reviewer"
	if err := c.Validate(); err == nil {
		t.Fatal("role substitution admitted")
	}
}

func TestRoleTOMLAndHostRequirements(t *testing.T) {
	raw := Example + "\n[writer]\nruntime = \"fake\"\nprovider = \"deterministic\"\nmodel = \"writer-fixture\"\neffort = \"high\"\nrole = \"writer\"\n"
	c, err := Parse([]byte(raw))
	if err != nil || c.Writer == nil {
		t.Fatal("explicit TOML role rejected", err)
	}
	for _, bad := range []string{
		strings.Replace(raw, "model = \"writer-fixture\"", "model = \"\"", 1),
		strings.Replace(raw, "role = \"writer\"", "role = \"explorer\"", 1),
		strings.Replace(raw, "[writer]", "[fallback]", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatal("invalid role configuration admitted")
		}
	}
	c.Writer.Runtime = "codex-app-server"
	c.Writer.Provider = "openai"
	if err := c.Validate(); err == nil {
		t.Fatal("Codex writer admitted without pinned host")
	}
}

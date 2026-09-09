package config

import (
	"encoding/json"
	"strings"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/runtime"
)

func TestExplicitAccessConfiguration(t *testing.T) {
	raw := strings.Replace(Example, "version = 1", "version = 2", 1) + `
[access]
class = "PRIVATE"
[access.limits]
tokens = 1000
concurrency = 1
[access.roles]
planner = "fixture-session"
[access.invocations.planner]
tokens = 200
[[access.profiles]]
version = 1
name = "fixture-session"
kind = "subscription"
runtime = "fake"
provider = "deterministic"
auth_mode = "fixture"
repository_classes = ["PRIVATE"]
`
	c, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.AccessPolicy(strings.Repeat("a", 64))
	if err != nil || len(p.Routes) != 1 {
		t.Fatal(err)
	}
	if p.Limits.Tokens != 1000 || p.Routes[0].AccessID == "" {
		t.Fatal("access policy not derived")
	}
	limitedJSON, err := json.Marshal(c.Access)
	if err != nil || strings.Contains(string(limitedJSON), "unlimited_tokens") {
		t.Fatal("limited access identity acquired an unlimited marker", err, string(limitedJSON))
	}
	unlimitedRaw := strings.Replace(raw, "tokens = 1000", "tokens = 0\nunlimited_tokens = true", 1)
	unlimitedRaw = strings.Replace(unlimitedRaw, "tokens = 200", "tokens = 0\nunlimited_tokens = true", 1)
	unlimited, err := Parse([]byte(unlimitedRaw))
	if err != nil {
		t.Fatal("explicit unlimited access rejected", err)
	}
	unlimitedPolicy, err := unlimited.AccessPolicy(strings.Repeat("a", 64))
	if err != nil || !unlimitedPolicy.Limits.UnlimitedTokens || !unlimited.Access.Invocations["planner"].UnlimitedTokens {
		t.Fatal("unlimited access policy marker lost", err)
	}
	for _, bad := range []string{
		strings.Replace(raw, "repository_classes = [\"PRIVATE\"]", "repository_classes = [\"PUBLIC\"]", 1),
		strings.Replace(raw, "planner = \"fixture-session\"", "planner = \"missing\"", 1),
		strings.Replace(raw, "version = 2", "version = 1", 1),
		strings.Replace(raw, "concurrency = 1", "concurrency = 0", 1),
		strings.Replace(raw, "tokens = 200", "tokens = 1001", 1),
		strings.Replace(raw, "tokens = 200", "tokens = 0", 1),
		strings.Replace(raw, "tokens = 200", "tokens = 1\nunlimited_tokens = true", 1),
		strings.Replace(raw, "tokens = 1000", "tokens = 1\nunlimited_tokens = true", 1),
		strings.Replace(raw, "tokens = 200", "tokens = 0\nunlimited_tokens = true", 1),
		strings.Replace(raw, "[access.invocations.planner]", "[access.invocations.writer]", 1),
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Fatal("invalid access configuration admitted")
		}
	}
	legacy, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.AccessPolicy(strings.Repeat("a", 64)); err == nil {
		t.Fatal("legacy acquired implicit access")
	}
}

func TestIndependentFixerRoute(t *testing.T) {
	c, err := Parse([]byte(Example))
	if err != nil {
		t.Fatal(err)
	}
	c.Version = 2
	c.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "writer-model", Effort: "low", Role: "writer"}
	c.Fixer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "fixer-model", Effort: "high", Role: "fixer"}
	c.Access = &Access{Class: access.Public, Limits: access.Limits{Tokens: 1000, Concurrency: 1}, Roles: map[string]string{"planner": "fixture", "writer": "fixture", "fixer": "fixture"}, Invocations: map[string]InvocationLimit{"planner": {Tokens: 100}, "writer": {Tokens: 200}, "fixer": {Tokens: 300}}, Profiles: []access.Profile{{Version: 1, Name: "fixture", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture", RepositoryClasses: []access.Class{access.Public}}}}
	r, err := c.Route("fixer")
	if err != nil || r.Model != "fixer-model" {
		t.Fatal("fixer route not independent", err)
	}
	before, err := c.ID()
	if err != nil {
		t.Fatal(err)
	}
	c.Fixer.Model = "replacement"
	after, err := c.ID()
	if err != nil || before == after || c.Writer.Model != "writer-model" {
		t.Fatal("fixer replacement not isolated and bound", err)
	}
	p, err := c.AccessPolicy(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Routes) != 3 || p.Routes[2].Role != "fixer" || p.Routes[2].Permission != "workspace-write" {
		t.Fatal("fixer missing from admission policy")
	}
	c.Version = 1
	if c.Validate() == nil {
		t.Fatal("legacy silently acquired new routing semantics")
	}
}

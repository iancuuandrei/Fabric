package gitpush

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"harness.local/engorch/internal/effects"
)

func TestCredentialScopeAndGitEnvironment(t *testing.T) {
	destination := "https://github.com/fixture/project.git"
	c, err := NewCredential(destination, "fixture-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if strings.Contains(fmt.Sprintf(format, c), "fixture-secret") || strings.Contains(fmt.Sprintf(format, c), c.header) {
			t.Fatal("credential formatting leaked")
		}
	}
	raw, err := json.Marshal(c)
	if err != nil || string(raw) != "{}" {
		t.Fatal("credential serialized")
	}
	if err := c.validate(destination + "/other"); err == nil {
		t.Fatal("credential destination widened")
	}
	for _, target := range []string{destination, "https://github.com/fixture/other.git", "https://other.example/fixture/project.git", destination + "-other"} {
		cmd := exec.Command("git", "config", "--get-urlmatch", "http.extraHeader", target)
		dir := t.TempDir()
		cmd.Dir = dir
		cmd.Env = c.environment(isolatedEnvironment(dir))
		out, err := cmd.Output()
		if target == destination {
			if err != nil || strings.TrimSpace(string(out)) != c.header {
				t.Fatal("Git did not consume scoped environment credential")
			}
		} else if err == nil || len(out) != 0 {
			t.Fatal("Git credential matched unrelated endpoint")
		}
	}
}

func TestAuthenticatedEntryPointsRejectUnboundCredential(t *testing.T) {
	p := fixturePlan(t, "sha1")
	p.Destination = "https://github.com/fixture/project.git"
	intent, err := p.Intent(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := intent.ID()
	foreign, err := NewCredential("https://github.com/foreign/project.git", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for _, credential := range []*Credential{nil, {}, foreign} {
		if _, err := ObserveRemoteAuthenticated(context.Background(), p, credential); err == nil {
			t.Fatal("unbound read credential admitted")
		}
		if _, err := ExecuteAuthenticated(context.Background(), p, intent, effects.Authorization{IntentID: id, Actor: "fixture"}, credential); err == nil {
			t.Fatal("unbound push credential admitted")
		}
	}
}

func TestCredentialRejectsUnsafeBinding(t *testing.T) {
	for _, destination := range []string{"http://github.com/a/b", "https://github.com.evil/a/b", "https://user@github.com/a/b", "https://github.com/a/b?x", "https://github.com/a/b/extra", "https://github.com/../b"} {
		if _, err := NewCredential(destination, "fixture"); err == nil {
			t.Fatal("unsafe destination admitted")
		}
	}
	for _, token := range []string{"", "line\n", "space token", "é", strings.Repeat("a", 4097)} {
		if _, err := NewCredential("https://github.com/a/b", token); err == nil {
			t.Fatal("unsafe token admitted")
		}
	}
}

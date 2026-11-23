package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/writercontract"
	"github.com/pelletier/go-toml/v2"
)

func TestCodexInitAddsOnlyExplicitIndependentFixerRoute(t *testing.T) {
	for _, test := range []struct {
		name, fixerModel, fixerEffort, wantModel, wantEffort string
		flags                                                []string
	}{
		{name: "both overrides", fixerModel: "gpt-6-sol", fixerEffort: "high", wantModel: "gpt-6-sol", wantEffort: "high", flags: []string{"--fixer-model", "gpt-6-sol", "--fixer-effort", "high"}},
		{name: "model only", fixerModel: "separate-model", fixerEffort: "medium", wantModel: "separate-model", wantEffort: "medium", flags: []string{"--fixer-model", "separate-model"}},
		{name: "effort only", fixerModel: "base-model", fixerEffort: "low", wantModel: "base-model", wantEffort: "low", flags: []string{"--fixer-effort", "low"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixerInitFixture(t)
			args := append(fixture.args("--writer-model", "writer-model", "--writer-effort", "xhigh", "--access-config", fixture.accessConfig(t)), test.flags...)
			var output bytes.Buffer
			if err := Execute(context.Background(), args, fixture.root, &output); err != nil {
				t.Fatal(err)
			}
			cfg := fixture.readConfig(t)
			if cfg.Version != 2 || cfg.Fixer == nil || cfg.Fixer.Model != test.wantModel || cfg.Fixer.Effort != test.wantEffort {
				t.Fatalf("explicit fixer route mismatch: %#v", cfg.Fixer)
			}
			if cfg.Writer == nil || cfg.Writer.Model != "writer-model" || cfg.Writer.Effort != "xhigh" {
				t.Fatalf("fixer allocation changed writer route: %#v", cfg.Writer)
			}
			if cfg.Planner.Model != "base-model" || cfg.Planner.Effort != "medium" || cfg.Explorer.Model != "base-model" || cfg.Explorer.Effort != "medium" || cfg.Reviewer.Model != "base-model" || cfg.Reviewer.Effort != "medium" {
				t.Fatal("fixer allocation changed another role")
			}
			policy, err := cfg.AccessPolicy(strings.Repeat("0", 64))
			if err != nil {
				t.Fatal(err)
			}
			var fixerRoute, writerRoute *access.Route
			for i := range policy.Routes {
				switch policy.Routes[i].Role {
				case "fixer":
					fixerRoute = &policy.Routes[i]
				case "writer":
					writerRoute = &policy.Routes[i]
				}
			}
			if fixerRoute == nil || writerRoute == nil || fixerRoute.Model != test.wantModel || fixerRoute.Effort != test.wantEffort || fixerRoute.Model == writerRoute.Model || fixerRoute.Permission != "workspace-write" {
				t.Fatalf("fixer and writer routes were not independently admitted: fixer=%+v writer=%+v", fixerRoute, writerRoute)
			}
			if strings.Contains(output.String(), "private-auth-marker") || strings.Contains(output.String(), fixture.auth) {
				t.Fatal("init output exposed an auth marker or path")
			}
		})
	}
}

func TestCodexInitWithoutFixerFlagsRetainsLegacyConfigBytes(t *testing.T) {
	fixture := newFixerInitFixture(t)
	var output bytes.Buffer
	if err := Execute(context.Background(), fixture.args(), fixture.root, &output); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(fixture.root, "harness.toml"))
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(fixture.binary)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := filepath.Abs(fixture.binary)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := filepath.Abs(fixture.auth)
	if err != nil {
		t.Fatal(err)
	}
	state, err := filepath.Abs(fixture.state)
	if err != nil {
		t.Fatal(err)
	}
	expected := config.Config{
		Version: 1, Repository: filepath.Base(fixture.root), BaseBranch: "HEAD",
		WriterContract: writercontract.ContractAnchoredEditsV1, PlannerContract: "plan-v1", ExplorerContract: "json-v2",
		Planner:      runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "base-model", Effort: "medium", Role: "planner"},
		Writer:       &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "base-model", Effort: "medium", Role: "writer"},
		Explorer:     &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "base-model", Effort: "medium", Role: "explorer"},
		Reviewer:     &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "base-model", Effort: "medium", Role: "reviewer"},
		Codex:        &config.Codex{Executable: executable, ExecutableHash: hex.EncodeToString(sha256Sum(binary)), StateRoot: state, AuthSource: auth},
		Verification: []config.Check{{Name: "unit", Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 120}},
	}
	want, err := toml.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, want) {
		t.Fatalf("omitting fixer flags changed legacy init configuration bytes\n got: %s\nwant: %s", raw, want)
	}
}

func TestCodexInitRejectsInvalidFixerBeforeWritingConfigOrState(t *testing.T) {
	for _, flags := range [][]string{
		{"--fixer-model", "", "--access-config"},
		{"--fixer-model", strings.Repeat("m", 129), "--access-config"},
		{"--fixer-effort", " ", "--access-config"},
		{"--fixer-effort", strings.Repeat("e", 129), "--access-config"},
	} {
		fixture := newFixerInitFixture(t)
		args := fixture.args()
		for i := 0; i < len(flags); i++ {
			if flags[i] == "--access-config" {
				args = append(args, flags[i], fixture.accessConfig(t))
			} else {
				args = append(args, flags[i])
				if flags[i] == "--fixer-model" || flags[i] == "--fixer-effort" {
					i++
					args = append(args, flags[i])
				}
			}
		}
		var output bytes.Buffer
		err := Execute(context.Background(), args, fixture.root, &output)
		if err == nil {
			t.Fatalf("accepted invalid fixer flags %q", flags)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.root, "harness.toml")); !os.IsNotExist(statErr) {
			t.Fatalf("rejected fixer profile wrote config: %v", statErr)
		}
		if _, statErr := os.Stat(fixture.state); !os.IsNotExist(statErr) {
			t.Fatalf("rejected fixer profile created runtime state: %v", statErr)
		}
		if strings.Contains(err.Error()+output.String(), "private-auth-marker") || strings.Contains(err.Error()+output.String(), fixture.auth) {
			t.Fatal("rejected init exposed auth marker or path")
		}
	}
}

func TestCodexInitRequiresStrictUserAccessConfigForFixer(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags []string
	}{
		{name: "fixer without access", flags: []string{"--fixer-model", "fixer-model"}},
		{name: "access without fixer", flags: []string{"--access-config", "ACCESS"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixerInitFixture(t)
			flags := append([]string(nil), test.flags...)
			for i := range flags {
				if flags[i] == "ACCESS" {
					flags[i] = fixture.accessConfig(t)
				}
			}
			var output bytes.Buffer
			if err := Execute(context.Background(), append(fixture.args(), flags...), fixture.root, &output); err == nil {
				t.Fatal("mismatched fixer/access options were accepted")
			}
			fixture.assertNoWrites(t)
		})
	}
}

func TestCodexInitRejectsMalformedAccessConfigBeforeWriting(t *testing.T) {
	for _, test := range []struct {
		name string
		make func(t *testing.T, fixture fixerInitFixture) string
	}{
		{name: "missing", make: func(t *testing.T, fixture fixerInitFixture) string { return filepath.Join(t.TempDir(), "absent.json") }},
		{name: "directory", make: func(t *testing.T, fixture fixerInitFixture) string { return t.TempDir() }},
		{name: "malformed", make: func(t *testing.T, fixture fixerInitFixture) string {
			return fixture.writeAccessFile(t, []byte(`{"class":`))
		}},
		{name: "duplicate", make: func(t *testing.T, fixture fixerInitFixture) string {
			return fixture.writeAccessFile(t, []byte(`{"class":"PRIVATE","class":"PUBLIC"}`))
		}},
		{name: "unknown", make: func(t *testing.T, fixture fixerInitFixture) string {
			return fixture.writeAccessFile(t, []byte(`{"unknown":true}`))
		}},
		{name: "trailing", make: func(t *testing.T, fixture fixerInitFixture) string {
			return fixture.writeAccessFile(t, append(fixture.validAccessBytes(t), []byte(` {}`)...))
		}},
		{name: "oversized", make: func(t *testing.T, fixture fixerInitFixture) string {
			return fixture.writeAccessFile(t, bytes.Repeat([]byte(" "), initAccessConfigMaxBytes+1))
		}},
		{name: "incomplete roles", make: func(t *testing.T, fixture fixerInitFixture) string {
			policy := fixture.validAccess(t)
			delete(policy.Roles, "fixer")
			body, err := canonical.Bytes(policy)
			if err != nil {
				t.Fatal(err)
			}
			return fixture.writeAccessFile(t, body)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newFixerInitFixture(t)
			path := test.make(t, fixture)
			var output bytes.Buffer
			err := Execute(context.Background(), fixture.args("--fixer-model", "fixer-model", "--access-config", path), fixture.root, &output)
			if err == nil {
				t.Fatal("invalid access config was accepted")
			}
			fixture.assertNoWrites(t)
			if strings.Contains(err.Error()+output.String(), "private-auth-marker") || strings.Contains(err.Error()+output.String(), fixture.auth) {
				t.Fatal("access config failure exposed auth marker or path")
			}
		})
	}
}

type fixerInitFixture struct {
	root, binary, auth, state string
}

func newFixerInitFixture(t *testing.T) fixerInitFixture {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "repository")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base"}} {
		if output, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	secretDir := t.TempDir()
	auth := filepath.Join(secretDir, "auth.json")
	if err := os.WriteFile(auth, []byte("private-auth-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	return fixerInitFixture{root: root, binary: binary, auth: auth, state: filepath.Join(secretDir, "state")}
}

func (f fixerInitFixture) args(extra ...string) []string {
	args := []string{"init", "--codex", f.binary, "--model", "base-model", "--effort", "medium", "--auth-source", f.auth, "--state-root", f.state}
	return append(args, extra...)
}

func (f fixerInitFixture) validAccess(t *testing.T) config.Access {
	t.Helper()
	return config.Access{
		Class:       access.Private,
		Limits:      access.Limits{Tokens: 5000, Concurrency: 2},
		Profiles:    []access.Profile{{Version: 1, Name: "codex-session", Kind: "subscription", Runtime: "codex-app-server", Provider: "openai", AuthMode: "session", RepositoryClasses: []access.Class{access.Private}}},
		Roles:       map[string]string{"planner": "codex-session", "explorer": "codex-session", "writer": "codex-session", "fixer": "codex-session", "reviewer": "codex-session"},
		Invocations: map[string]config.InvocationLimit{"planner": {Tokens: 1000}, "explorer": {Tokens: 1000}, "writer": {Tokens: 1000}, "fixer": {Tokens: 1000}, "reviewer": {Tokens: 1000}},
	}
}

func (f fixerInitFixture) validAccessBytes(t *testing.T) []byte {
	t.Helper()
	body, err := canonical.Bytes(f.validAccess(t))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (f fixerInitFixture) accessConfig(t *testing.T) string {
	t.Helper()
	return f.writeAccessFile(t, f.validAccessBytes(t))
}

func (f fixerInitFixture) writeAccessFile(t *testing.T, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "access.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func (f fixerInitFixture) assertNoWrites(t *testing.T) {
	t.Helper()
	for _, path := range []string{filepath.Join(f.root, "harness.toml"), f.state} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rejected init wrote config or state at %s: %v", path, err)
		}
	}
}

func (f fixerInitFixture) readConfig(t *testing.T) config.Config {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.root, "harness.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func sha256Sum(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

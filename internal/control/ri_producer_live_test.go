package control

import (
	"context"
	"crypto/sha256"
	"fmt"
	"harness.local/engorch/internal/codexhost"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/runtime"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestActualControllerScipGoProducer(t *testing.T) {
	actualControllerScipGoProducer(t, false)
}

func TestLiveControllerScipGoRoles(t *testing.T) {
	actualControllerScipGoProducer(t, true)
}

func actualControllerScipGoProducer(t *testing.T, liveRoles bool) {
	t.Helper()
	executable := os.Getenv("ENGORCH_SCIP_GO_BINARY")
	if executable == "" {
		t.Skip("requires installed scip-go")
	}
	c := riGitCreation(t)
	tempDir := t.TempDir
	if liveRoles {
		binary, auth := os.Getenv("ENGORCH_CODEX_PROBE_BINARY"), os.Getenv("ENGORCH_CODEX_AUTH_SOURCE")
		if binary == "" || auth == "" || os.Getenv("ENGORCH_RI_BINARY") == "" {
			t.Skip("requires explicit authenticated Codex and Rust RI opt-in")
		}
		if evidence := os.Getenv("ENGORCH_CODEX_CLI_EVIDENCE"); evidence != "" {
			base, err := os.MkdirTemp(filepath.Dir(evidence), "codex-ri-roles-")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("retained RI roles attempt: %s", base)
			tempDir = func() string {
				p, err := os.MkdirTemp(base, "fixture-")
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			c.Repository.Root = tempDir()
			if out, err := exec.Command("git", "-C", c.Repository.Root, "init", "-q").CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
		}
		probe, err := codexhost.Prepare(tempDir(), binary)
		if err != nil {
			t.Fatal(err)
		}
		c.Config.Codex = &config.Codex{Executable: binary, ExecutableHash: probe.BinaryHash, StateRoot: tempDir(), AuthSource: auth}
		c.Config.Writer = &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-luna", Effort: "low", Role: "writer"}
		c.Config.Reviewer = &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-luna", Effort: "low", Role: "reviewer"}
		c.Config.Verification = []config.Check{{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
		c.Objective = "Create only generated.txt containing the string returned by Greeting in library.go followed by a newline. Both writer and reviewer must call ri_status, ri_locate for Greeting and ri_definition for the discovered symbol, inspecting coverage and source provenance. Use candidate_read for current files when needed. Writer returns a regular-file proposal only. Reviewer checks generated.txt against Greeting using current candidate bytes and RI base context. Do not modify library.go or claim git-version tests application behavior."
	}
	for name, text := range map[string]string{"go.mod": "module example.test/controllerfixture\n\ngo 1.25\n", "library.go": "package controllerfixture\n\nfunc Greeting() string { return \"salut\" }\n"} {
		if err := os.WriteFile(filepath.Join(c.Repository.Root, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "go.mod", "library.go"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "module"}} {
		if out, err := exec.Command("git", append([]string{"-C", c.Repository.Root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	var err error
	c.Repository, err = repository.Discover(context.Background(), c.Repository.Root, c.Repository.Name)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(c.Config.Planner, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&runtime.Fake{}).Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.approved", Approval{s.PlanID, "operator"}); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(tempDir(), "index.scip")
	s, err = StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ri.PrepareProducer(c.Repository, s.Workspace.Request.Path, config.Check{Name: "scip-go", Argv: []string{executable, "index", "--module-version=fixture-v1", "--output", output}, TimeoutSeconds: 60}, output)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	auth := effects.Authorization{IntentID: id, Actor: "operator"}
	s, err = ExecuteRIProducer(context.Background(), path, plan, auth)
	if err != nil || s.RIProducer == nil || s.RIProducer.Outcome != "CONFIRMED" {
		t.Fatal("controller producer failed", err)
	}
	artifact := s.RIProducer.Observation.Result.Artifact
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) || artifact.Bytes != int64(len(data)) {
		t.Fatal("producer artifact receipt mismatch")
	}
	replayed, err := Inspect(path)
	if err != nil || replayed.RIProducer.Observation.Result.Artifact.SHA256 != artifact.SHA256 {
		t.Fatal("producer replay mismatch", err)
	}
	if _, err := ExecuteRIProducer(context.Background(), path, plan, auth); err == nil {
		t.Fatal("confirmed producer rerun")
	}
	t.Run("BoundImport", func(t *testing.T) {
		executable := os.Getenv("ENGORCH_RI_BINARY")
		if executable == "" {
			t.Skip("requires Rust RI")
		}
		binary, err := os.ReadFile(executable)
		if err != nil {
			t.Fatal(err)
		}
		source, err := ri.FromRepository(c.Repository)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := repository.DigestSource(context.Background(), c.Repository, "library.go")
		if err != nil {
			t.Fatal(err)
		}
		uri := url.URL{Scheme: "file", Path: plan.Invocation.Directory}
		policyHash := fmt.Sprintf("%x", sha256.Sum256([]byte("engorch.scip-go.0.2.7.positions.v1:utf8-byte-columns")))
		proposal := ri.ImportPlan{Version: 1, Executable: executable, ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256(binary)), Repository: c.Repository, OutputPath: filepath.Join(tempDir(), "snapshot"), Request: ri.ImportRequest{Source: source, Producer: "scip-go", ProjectRoot: uri.String(), Policy: "scip_go027", Sources: map[string]string{"library.go": filepath.Join(plan.Invocation.Directory, "library.go")}, Manifest: ri.Manifest{Format: 1, Source: source, Producers: []ri.Producer{{ID: "scip-go", Name: "scip-go", Version: "0.2.7", Inputs: []ri.Input{{Name: "scip:position-policy", SHA256: policyHash}, {Name: "source:library.go", SHA256: digest.SHA256}}}}}}}
		bound, err := BindRIProducerImport(path, proposal)
		if err != nil {
			t.Fatal(err)
		}
		if proposal.Request.Manifest.Producers[0].ArtifactSHA256 != "" || len(proposal.Request.Manifest.Producers[0].Inputs) != 2 {
			t.Fatal("binding mutated caller proposal")
		}
		wrong := bound
		wrong.Request.IndexPath = filepath.Join(c.Repository.Root, "foreign.scip")
		if validateProducerImport(s, wrong) == nil {
			t.Fatal("foreign producer index admitted")
		}
		importIntent, err := bound.Intent(s.RunID, s.PlanID)
		if err != nil {
			t.Fatal(err)
		}
		importID, err := importIntent.ID()
		if err != nil {
			t.Fatal(err)
		}
		imported, err := ExecuteRIImport(context.Background(), path, bound, effects.Authorization{IntentID: importID, Actor: "operator"})
		if err != nil || imported.RIImport.Outcome != "CONFIRMED" {
			t.Fatal("producer-bound import failed", err)
		}
		if liveRoles {
			qualifyLiveRIRoles(t, path, tempDir())
		}
	})
	beforeJournal, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Workspace.Request.Path, "unexpected.go"), []byte("package controllerfixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteRIProducer(context.Background(), path, plan, auth); err == nil {
		t.Fatal("changed producer sources admitted")
	}
	afterJournal, err := os.ReadFile(path)
	if err != nil || string(beforeJournal) != string(afterJournal) {
		t.Fatal("source preflight failure changed journal", err)
	}
}

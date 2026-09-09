package control

import (
	"context"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/controllerstate"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/worktree"
)

func approvedRepository(t *testing.T, checks ...config.Check) (string, Snapshot) {
	t.Helper()
	c := creation(t)
	if len(checks) > 0 {
		c.Config.Verification = checks
	}
	return approvedRepositoryCreation(t, c)
}

func approvedRepositoryCreation(t *testing.T, c Creation) (string, Snapshot) {
	t.Helper()
	root := c.Repository.Root
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "file.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	var err error
	c.Repository, err = repository.Discover(context.Background(), root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "run.jsonl")
	if c.Config.ControllerStateRoot != "" {
		paths, err := controllerstate.Resolve(c.Config.ControllerStateRoot, c.Repository)
		if err != nil {
			t.Fatal(err)
		}
		if err := controllerstate.Initialize(paths, c.Repository); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(paths.Runs, 0700); err != nil {
			t.Fatal(err)
		}
		id, err := canonical.Hash("harness.run.v1", c)
		if err != nil {
			t.Fatal(err)
		}
		p, err = paths.Run(id)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := Append(p, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(p, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	inv, err := plannerInvocation(c.Config, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	f := &runtime.Fake{}
	result, err := f.Execute(context.Background(), inv)
	if err != nil {
		t.Fatal(err)
	}
	if err = Append(p, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	if err = Append(p, "plan.approved", Approval{s.PlanID, "operator"}); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, s
}

func TestUnknownCreationRequiresReconciliation(t *testing.T) {
	p, s := approvedRepository(t)
	r, err := worktree.Prepare(s.RunID, s.Creation.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = Append(p, "workspace.intent", r); err != nil {
		t.Fatal(err)
	}
	if _, err = StartWorkspace(context.Background(), p); err == nil {
		t.Fatal("retried pending creation")
	}
	if _, err = ReconcileWorkspace(context.Background(), p); err == nil {
		t.Fatal("absent workspace confirmed")
	}
	// Simulate effect completion followed by lost controller confirmation.
	if _, err = worktree.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(p)
	if err != nil || s.WorkspaceOutcome != "UNKNOWN" {
		t.Fatal(s.WorkspaceOutcome, err)
	}
	s, err = ReconcileWorkspace(context.Background(), p)
	if err != nil || s.WorkspaceOutcome != "CONFIRMED" {
		t.Fatal(s.WorkspaceOutcome, err)
	}
	if _, err = StartWorkspace(context.Background(), p); err != nil {
		t.Fatal("confirmed replay failed", err)
	}
}

func TestReconciliationRejectsModifiedWorkspace(t *testing.T) {
	p, s := approvedRepository(t)
	r, err := worktree.Prepare(s.RunID, s.Creation.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = Append(p, "workspace.intent", r); err != nil {
		t.Fatal(err)
	}
	if _, err = worktree.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(r.Path, "file.txt"), []byte("unexpected edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReconcileWorkspace(context.Background(), p); err == nil {
		t.Fatal("non-pristine creation reconciled")
	}
	after, err := Inspect(p)
	if err != nil || after.WorkspaceOutcome != "UNKNOWN" {
		t.Fatal(after, err)
	}
}

package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func TestWorkspaceDestinationPreflightBeforePlannerEffect(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("demonstrated Git for Windows working-directory limit")
	}
	c := graphCreation(t, 1)
	autonomousGitInit(t, c.Repository.Root)
	parent := t.TempDir()
	destination := filepath.Join(parent, strings.Repeat("d", 200-len(parent)-1))
	if err := os.Rename(c.Repository.Root, destination); err != nil {
		t.Fatal(err)
	}
	source, err := repository.Discover(context.Background(), destination, c.Repository.Name)
	if err != nil {
		t.Fatal(err)
	}
	c.Repository = source
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	before, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = RunAutonomous(context.Background(), path)
	if !errors.Is(err, worktree.ErrDestinationUnsupported) {
		t.Fatal("planner preflight lost destination cause", err)
	}
	after, err := Inspect(path)
	if err != nil || after.State != "OBJECTIVE" || after.ControllerHead != before.ControllerHead || after.WorkspaceIntent != nil || after.PlannerHost != nil {
		t.Fatal("unsupported destination admitted effects", after.State, err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	restarted, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = RunAutonomous(context.Background(), path)
	if !errors.Is(err, worktree.ErrDestinationUnsupported) {
		t.Fatal("planning restart bypassed path preflight", err)
	}
	final, err := Inspect(path)
	if err != nil || final.ControllerHead != restarted.ControllerHead {
		t.Fatal("restart path preflight changed journal", err)
	}
}

func TestWorkspaceDestinationDiagnosticCannotSettleUnknown(t *testing.T) {
	outcome := ClassifyAutonomousFailure(Snapshot{}, worktree.ErrDestinationUnsupported)
	if outcome.PublicReason() != "workspace_path_unsupported" || outcome.NextAction() != "use_shorter_repository_checkout_before_new_run" || outcome.Status() != "NEEDS_ATTENTION" {
		t.Fatal("missing actionable workspace diagnostic", outcome)
	}
	outcome = ClassifyAutonomousFailure(Snapshot{WorkspaceOutcome: "UNKNOWN"}, worktree.ErrDestinationUnsupported)
	if outcome.Status() != "UNKNOWN" || outcome.NextAction() != "reconcile_existing_effect_without_resend" {
		t.Fatal("path diagnosis settled unknown workspace", outcome)
	}
}

func TestWorkspaceDestinationStopsApprovedIntentBeforeRegistration(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Git for Windows working-directory limit")
	}
	c := creation(t)
	parent := t.TempDir()
	c.Repository.Root = filepath.Join(parent, strings.Repeat("d", 200-len(parent)-1))
	if err := os.MkdirAll(c.Repository.Root, 0700); err != nil {
		t.Fatal(err)
	}
	path, before := approvedRepositoryCreation(t, c)
	_, err := StartWorkspace(context.Background(), path)
	if !errors.Is(err, worktree.ErrDestinationUnsupported) {
		t.Fatal("approved dispatch ignored workspace destination", err)
	}
	after, err := Inspect(path)
	if err != nil || after.WorkspaceIntent != nil || after.ControllerHead != before.ControllerHead {
		t.Fatal("rejected destination recorded a workspace effect", err)
	}
}

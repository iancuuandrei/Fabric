package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"harness.local/engorch/internal/worktree"
)

func TestExternalControllerWorkspaceBindsLeaseNamespace(t *testing.T) {
	c := creation(t)
	c.Config.ControllerStateRoot = t.TempDir()
	p, s := approvedRepositoryCreation(t, c)
	if _, err := os.Stat(filepath.Join(s.Creation.Repository.Root, ".harness")); !os.IsNotExist(err) {
		t.Fatal("external planning wrote state into source", err)
	}
	wrong, err := worktree.Prepare(s.RunID, s.Creation.Repository)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(p, "workspace.intent", wrong); err == nil {
		t.Fatal("external creation admitted a legacy lease namespace")
	}
	s, err = StartWorkspace(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	want, err := controllerNamespace(s.Creation)
	if err != nil || s.Workspace == nil || s.Workspace.Request.ControllerStateRoot != want {
		t.Fatal("workspace did not bind exact external namespace", err)
	}
	if _, err := os.Stat(filepath.Join(s.Creation.Repository.Root, ".harness", "leases")); !os.IsNotExist(err) {
		t.Fatal("lease state leaked into source", err)
	}
	if _, err := os.Stat(filepath.Join(want, "leases")); err != nil {
		t.Fatal("external lease namespace missing", err)
	}
	if _, err := Inspect(p); err != nil {
		t.Fatal("external workspace history no longer replays", err)
	}
}

func TestExternalControllerCreationRejectsWrongJournalBeforeWrite(t *testing.T) {
	c := creation(t)
	c.Config.ControllerStateRoot = t.TempDir()
	_, s := approvedRepositoryCreation(t, c)
	wrong := filepath.Join(s.Creation.Repository.Root, ".harness", "runs", s.RunID+".jsonl")
	if err := Append(wrong, "run.created", s.Creation); err == nil {
		t.Fatal("external config allowed a source-local controller journal")
	}
	if _, err := os.Stat(filepath.Join(s.Creation.Repository.Root, ".harness")); !os.IsNotExist(err) {
		t.Fatal("rejection created mutable source state", err)
	}
}

func TestExternalControllerCopiedJournalCannotAuthorizeDispatch(t *testing.T) {
	c := creation(t)
	c.Config.ControllerStateRoot = t.TempDir()
	p, _ := approvedRepositoryCreation(t, c)
	if _, err := RequestPause(p, "fixture", "external-pause"); err != nil {
		t.Fatal(err)
	}
	if _, err := SettleLifecycle(p, "fixture", "no active workload", true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(t.TempDir(), "copied.jsonl")
	if err := os.WriteFile(copied, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(copied); err == nil {
		t.Fatal("copied external journal admitted by Inspect")
	}
	if _, _, err := InspectWithHead(copied); err == nil {
		t.Fatal("copied external journal admitted for scheduled dispatch")
	}
	if _, err := MeasureRunUsage(copied); err == nil {
		t.Fatal("copied external journal admitted for usage reconciliation")
	}
	if _, err := SettleLifecycle(copied, "fixture", "no active workload", true); err == nil {
		t.Fatal("copied settled journal returned idempotent settlement success")
	}
	if _, _, err := InspectWithHead(p); err != nil {
		t.Fatal("bound external journal rejected", err)
	}
}

package worktree

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/repository"
)

func externalLeaseRequest(t *testing.T) Request {
	t.Helper()
	_, request := fixture(t)
	request.ControllerStateRoot = t.TempDir()
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	return request
}

func TestExternalControllerStateOwnsLeaseAndGuard(t *testing.T) {
	request := externalLeaseRequest(t)
	wantLease := filepath.Join(request.ControllerStateRoot, "leases", request.RunID+".lock")
	wantGuard := filepath.Join(request.ControllerStateRoot, "leases", request.RunID+".guard")
	if LeasePath(request) != wantLease || LeaseGuardPath(request) != wantGuard {
		t.Fatal("external lease paths changed", LeasePath(request), LeaseGuardPath(request))
	}
	lease, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wantLease); err != nil {
		t.Fatal("external writer token missing", err)
	}
	if _, err := os.Stat(wantGuard); err != nil {
		t.Fatal("external guard missing", err)
	}
	if _, err := os.Stat(filepath.Join(request.Source.Root, ".harness", "leases")); !os.IsNotExist(err) {
		t.Fatal("external request wrote the historical source lease namespace", err)
	}
	if err := lease.WithOwnership(request, func(identity LeaseIdentity) error {
		_, err := identity.ID()
		return err
	}); err != nil {
		t.Fatal("external lease ownership unavailable", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wantLease); !os.IsNotExist(err) {
		t.Fatal("external writer token retained after close", err)
	}
	if info, err := os.Stat(wantGuard); err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		t.Fatal("stable external guard was not preserved", err)
	}
	reader, err := AcquireRead(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExternalControllerStateLeaseRecoveryPreservesNamespace(t *testing.T) {
	request := externalLeaseRequest(t)
	owner, err := Acquire(request)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PrepareLeaseRecovery(request, "external-recovery", "worker exit observed by fixture", true)
	if err != nil {
		t.Fatal(err)
	}
	id, err := plan.ID()
	if err != nil {
		t.Fatal(err)
	}
	repositoryID, err := request.Source.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := effects.Intent{Version: 1, RunID: request.RunID, PlanID: strings.Repeat("b", 64), RepositoryID: repositoryID, Kind: "lease_recovery", InputHash: id}
	intentID, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owner.guard.Close(); err != nil {
		t.Fatal(err)
	}
	owner.file = nil
	owner.guard = nil
	adopted, err := AdoptLease(plan, intent, effects.Authorization{IntentID: intentID, Actor: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if adopted.path != LeasePath(request) || adopted.guardPath != LeaseGuardPath(request) {
		t.Fatal("recovery moved external lease ownership")
	}
	if err := adopted.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(request.Source.Root, ".harness", "leases")); !os.IsNotExist(err) {
		t.Fatal("recovery wrote source lease state", err)
	}
}

func TestControllerStateRootRejectsOverlapAndTraversal(t *testing.T) {
	_, request := fixture(t)
	for name, root := range map[string]string{
		"source":        request.Source.Root,
		"source child":  filepath.Join(request.Source.Root, "state"),
		"source parent": filepath.Dir(request.Source.Root),
		"git directory": filepath.Join(request.Source.Root, ".git"),
		"common dir":    request.Source.CommonDir,
		"worktree":      request.Path,
		"unclean":       t.TempDir() + string(filepath.Separator) + "state" + string(filepath.Separator) + ".." + string(filepath.Separator) + "other",
		"relative":      "controller-state",
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			changed.ControllerStateRoot = root
			if err := changed.Validate(); err == nil {
				t.Fatal("unsafe controller state root accepted", root)
			}
		})
	}

	target := t.TempDir()
	alias := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(target, alias); err != nil {
		if runtime.GOOS != "windows" {
			t.Skip("directory symlinks unavailable", err)
		}
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `New-Item -ItemType Junction -Path $env:HARNESS_JUNCTION -Target $env:HARNESS_TARGET -ErrorAction Stop | Out-Null`)
		cmd.Env = append(os.Environ(), "HARNESS_JUNCTION="+alias, "HARNESS_TARGET="+target)
		if output, junctionErr := cmd.CombinedOutput(); junctionErr != nil {
			t.Skip("directory aliases unavailable", junctionErr, string(output))
		}
	}
	request.ControllerStateRoot = alias
	if err := request.Validate(); err != nil {
		t.Fatal("pure request validation unexpectedly observed filesystem aliases", err)
	}
	if _, err := Acquire(request); err == nil {
		t.Fatal("linked controller state root admitted for lease IO")
	}
}

func TestHistoricalRequestBytesAndLeaseIdentityRemainStable(t *testing.T) {
	_, request := fixture(t)
	type historicalRequest struct {
		Version           int                 `json:"version"`
		RunID             string              `json:"run_id"`
		Source            repository.Identity `json:"source"`
		Path              string              `json:"path"`
		Branch            string              `json:"branch"`
		CandidateIdentity string              `json:"candidate_identity,omitempty"`
	}
	historical := historicalRequest{
		Version: request.Version, RunID: request.RunID, Source: request.Source,
		Path: request.Path, Branch: request.Branch, CandidateIdentity: request.CandidateIdentity,
	}
	currentBytes, err := canonical.Bytes(request)
	if err != nil {
		t.Fatal(err)
	}
	historicalBytes, err := canonical.Bytes(historical)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(currentBytes, historicalBytes) {
		t.Fatal("empty controller state root changed historical request bytes")
	}
	type historicalLeaseIdentity struct {
		Version int               `json:"version"`
		Request historicalRequest `json:"request"`
	}
	want, err := canonical.Hash("harness.worktree-lease-identity.v1", historicalLeaseIdentity{Version: 1, Request: historical})
	if err != nil {
		t.Fatal(err)
	}
	got, err := (LeaseIdentity{Version: 1, Request: request}).ID()
	if err != nil || got != want {
		t.Fatal("historical lease identity changed", got, want, err)
	}
}

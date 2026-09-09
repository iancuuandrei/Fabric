package control

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/worktree"
)

func TestDraftCrashWorker(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-3] != "engorch-draft-crash" {
		return
	}
	path, stage := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if stage == "acquired" {
		lease, err := worktree.Acquire(s.Draft.Intent.Prepared.Plan.Push.Workspace.Request)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
	} else {
		p, err := PrepareDraftLeaseRecovery(path, "previous fixture worker exited; no HTTP child exists", true)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := p.Intent.ID()
		auth := effects.Authorization{IntentID: id, Actor: "fixture"}
		if err := Append(path, "draft.lease-intent", DraftLeaseIntent{Prepared: p, Authorization: auth}); err != nil {
			t.Fatal(err)
		}
		if stage == "observed" {
			lease, err := worktree.AdoptLease(p.Plan, p.Intent, auth)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			s, err = Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			host := &fixtureDraftHost{t: t, path: path, available: true}
			if _, err := observeDraftLocked(context.Background(), path, s, host); err != nil {
				t.Fatal(err)
			}
		}
	}
	os.Exit(23) // Deliberately skip deferred release at the named boundary.
}

func runDraftCrash(t *testing.T, path, stage string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestDraftCrashWorker$", "--", "engorch-draft-crash", path, stage)
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 23 {
		t.Fatal("worker failed before controlled exit", err, string(output))
	}
}

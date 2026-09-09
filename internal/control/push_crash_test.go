package control

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitpush"
	"harness.local/engorch/internal/worktree"
)

func TestPushCrashWorker(t *testing.T) {
	if len(os.Args) < 5 || os.Args[len(os.Args)-4] != "engorch-push-crash" {
		return
	}
	path, preview, stage := os.Args[len(os.Args)-3], os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	data, err := os.ReadFile(preview)
	if err != nil {
		t.Fatal(err)
	}
	var intent PushIntent
	if err := json.Unmarshal(data, &intent); err != nil {
		t.Fatal(err)
	}
	if stage == "crash-recovery-intent" || stage == "crash-recovery-observed" {
		prepared, err := PreparePushLeaseRecovery(path, "previous worker exited after its Git command completed", true)
		if err != nil {
			t.Fatal(err)
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			t.Fatal(err)
		}
		if err := Append(path, "push.lease-intent", PushLeaseIntent{Prepared: prepared, Authorization: effects.Authorization{IntentID: id, Actor: "fixture"}}); err != nil {
			t.Fatal(err)
		}
		if stage == "crash-recovery-observed" {
			lease, err := worktree.AdoptLease(prepared.Plan, prepared.Intent, effects.Authorization{IntentID: id, Actor: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			s, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := observePushLocked(context.Background(), path, s); err != nil {
				t.Fatal(err)
			}
		}
		os.Exit(23)
	}
	lease, err := worktree.Acquire(intent.Prepared.Plan.Workspace.Request)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := Append(path, "push.intent", intent); err != nil {
		t.Fatal(err)
	}
	if stage == "crash-effect" {
		if _, err := gitpush.Execute(context.Background(), intent.Prepared.Plan, intent.Prepared.Intent, intent.Authorization); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(23) // Deliberately bypass deferred lease release after the boundary.
}

func qualifyPushCrash(t *testing.T, path string, prepared PreparedPush, auth effects.Authorization, stage string) {
	t.Helper()
	preview := filepath.Join(t.TempDir(), "push.json")
	data, err := json.Marshal(PushIntent{prepared, auth})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preview, data, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestPushCrashWorker$", "--", "engorch-push-crash", path, preview, stage)
	output, runErr := command.CombinedOutput()
	if exit, ok := runErr.(*exec.ExitError); !ok || exit.ExitCode() != 23 {
		t.Fatal("worker did not reach controlled exit", runErr, string(output))
	}
	s, err := Inspect(path)
	if err != nil || s.Push == nil || s.Push.Outcome != "UNKNOWN" {
		t.Fatal("crash lost durable uncertainty", err)
	}
	if _, err := ReconcilePush(context.Background(), path); err == nil {
		t.Fatal("reconciliation bypassed retained lease")
	}
	previousRecovery := ""
	if stage == "crash-effect" {
		for _, boundary := range []string{"crash-recovery-intent", "crash-recovery-observed"} {
			command := exec.CommandContext(ctx, executable, "-test.run=^TestPushCrashWorker$", "--", "engorch-push-crash", path, preview, boundary)
			output, runErr := command.CombinedOutput()
			if exit, ok := runErr.(*exec.ExitError); !ok || exit.ExitCode() != 23 {
				t.Fatal("recovery worker did not reach exit boundary", runErr, string(output))
			}
			s, err = Inspect(path)
			if err != nil || s.Push.LeaseRecovery == nil || s.Push.LeaseRecovery.Outcome != "UNKNOWN" {
				t.Fatal("interrupted recovery lost uncertainty", err)
			}
			prior := s.Push.LeaseRecovery.Intent
			previousRecovery, err = prior.Prepared.Intent.ID()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := RecoverPushLease(context.Background(), path, prior.Prepared, prior.Authorization); err == nil {
				t.Fatal("interrupted recovery reused original approval")
			}
		}
	}
	if _, err := PreparePushLeaseRecovery(path, "worker exited and completed its Git child", false); err == nil {
		t.Fatal("missing quiescence admitted")
	}
	recovery, err := PreparePushLeaseRecovery(path, "worker exit 23 observed; Git child completed before exit boundary", true)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Plan.PreviousIntentID != previousRecovery {
		t.Fatal("recovery successor omitted predecessor")
	}
	id, err := recovery.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverPushLease(context.Background(), path, recovery, auth); err == nil {
		t.Fatal("push approval reused for lease recovery")
	}
	s, err = RecoverPushLease(context.Background(), path, recovery, effects.Authorization{IntentID: id, Actor: "fixture"})
	if s.Push == nil || s.Push.LeaseRecovery == nil || s.Push.LeaseRecovery.Outcome != "CONFIRMED" {
		t.Fatal("lease release not confirmed", err)
	}
	if stage == "crash-effect" {
		if err != nil || s.State != "PUSHED" || s.Push.Outcome != "CONFIRMED" {
			t.Fatal("completed push not reconciled", err)
		}
	} else if err == nil || s.Push.Outcome != "UNKNOWN" || s.State != "PUSHING" {
		t.Fatal("pre-effect crash falsely classified", err)
	}
	if _, err := RecoverPushLease(context.Background(), path, recovery, effects.Authorization{IntentID: id, Actor: "fixture"}); err == nil {
		t.Fatal("lease recovery reused")
	}
	if _, err := ExecutePush(context.Background(), path, prepared, auth); err == nil {
		t.Fatal("crashed push automatically retried")
	}
	lock := filepath.Join(prepared.Plan.Workspace.Request.Source.Root, ".harness", "leases", prepared.Intent.RunID+".lock")
	if _, err := os.Lstat(lock); !os.IsNotExist(err) {
		t.Fatal("recovered lease remains", err)
	}
}

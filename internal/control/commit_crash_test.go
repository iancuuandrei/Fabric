package control

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/worktree"
)

// This helper exits the process without running deferred lease cleanup, after
// a completed Git subprocess. It does not simulate a killed in-flight Git child.
func TestCommitCrashWorker(t *testing.T) {
	args := os.Args
	if len(args) < 4 || args[len(args)-3] != "engorch-commit-crash-worker" {
		return
	}
	path, stage := args[len(args)-2], args[len(args)-1]
	s, err := Inspect(path)
	if err != nil || s.Commit == nil {
		t.Fatal("worker missing intent", err)
	}
	intent := s.Commit.Intent
	if stage == "commit-recovery-intent" {
		prepared, err := PrepareCommitRecovery(context.Background(), path, "parent fixture observed previous child exit", true)
		if err != nil {
			t.Fatal(err)
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			t.Fatal(err)
		}
		lease, err := worktree.Acquire(intent.Prepared.Plan.Workspace.Request)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
		if err := Append(path, "commit.recovery-intent", CommitRecoveryIntent{Prepared: prepared, Authorization: effects.Authorization{IntentID: id, Actor: "fixture-child"}}); err != nil {
			t.Fatal(err)
		}
		os.Exit(23)
	}
	if stage == "lease-intent" || stage == "lease-observed" {
		prepared, err := PrepareCommitLeaseRecovery(path, "parent fixture observed prior worker exit and completed Git calls", true)
		if err != nil {
			t.Fatal(err)
		}
		id, err := prepared.Intent.ID()
		if err != nil {
			t.Fatal(err)
		}
		if err := Append(path, "commit.lease-intent", CommitLeaseIntent{Prepared: prepared, Authorization: effects.Authorization{IntentID: id, Actor: "fixture-child"}}); err != nil {
			t.Fatal(err)
		}
		if stage == "lease-observed" {
			lease, err := worktree.AdoptLease(prepared.Plan, prepared.Intent, effects.Authorization{IntentID: id, Actor: "fixture-child"})
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			current, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := observeCommitLocked(context.Background(), path, current); err != nil {
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
	objects, err := gitlocal.ReadCandidateObjects(context.Background(), intent.Prepared.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if stage != "intent" {
		if err := gitlocal.StoreObjects(context.Background(), intent.Prepared.Plan, objects, intent.Prepared.Intent, intent.Authorization); err != nil {
			t.Fatal(err)
		}
	}
	if stage == "ref" || stage == "index" {
		if err := gitlocal.AdvanceRef(context.Background(), intent.Prepared.Plan, objects, intent.Prepared.Intent, intent.Authorization); err != nil {
			t.Fatal(err)
		}
	}
	if stage == "index" {
		if _, err := gitlocal.FinalizeIndex(context.Background(), intent.Prepared.Plan, objects, intent.Prepared.Intent, intent.Authorization); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(23)
}

func TestCommitCrashRetainsUnknownAndLease(t *testing.T) {
	for _, stage := range []string{"intent", "objects", "ref", "index"} {
		t.Run(stage, func(t *testing.T) {
			t.Setenv("GIT_DEFAULT_HASH", "sha1")
			ctx := context.Background()
			path, _ := approvedRepository(t, config.Check{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10})
			if _, err := StartWorkspace(ctx, path); err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(ctx, path); err != nil {
				t.Fatal(err)
			}
			identity := gitlocal.Identity{Name: "Fixture", Email: "fixture@example.invalid", UnixSeconds: 1788739200}
			p, err := PrepareCommit(ctx, path, identity, identity, "crash fixture "+stage+"\n")
			if err != nil {
				t.Fatal(err)
			}
			id, err := p.Intent.ID()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := RecordCommitIntent(ctx, path, p, effects.Authorization{IntentID: id, Actor: "fixture"}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			workerCtx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			out, err := exec.CommandContext(workerCtx, executable, "-test.run=^TestCommitCrashWorker$", "--", "engorch-commit-crash-worker", path, stage).CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 23 {
				t.Fatalf("worker did not reach crash boundary: %v %s", err, out)
			}
			s, err := Inspect(path)
			if err != nil || s.Commit.Outcome != "UNKNOWN" || s.State != "COMMITTING" {
				t.Fatal("crash state falsely resolved", err)
			}
			lock := filepath.Join(p.Plan.Workspace.Request.Source.Root, ".harness", "leases", p.Plan.Workspace.Request.RunID+".lock")
			if data, err := os.ReadFile(lock); err != nil || len(data) != 64 {
				t.Fatal("crashed owner lease missing", err)
			}
			if _, err := ReconcileCommit(ctx, path); err == nil {
				t.Fatal("crashed lease silently bypassed")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed reconciliation changed journal", err)
			}
			actual, err := exec.Command("git", "-C", p.Plan.Workspace.Request.Path, "rev-parse", "HEAD").Output()
			if err != nil {
				t.Fatal(err)
			}
			expected := p.Plan.Candidate.Head
			if stage == "ref" || stage == "index" {
				expected = s.Commit.Intent.CommitID
			}
			if string(bytes.TrimSpace(actual)) != expected {
				t.Fatal("unexpected ref at crash boundary " + strconv.Quote(stage))
			}
			var interrupted *CommitLeaseIntent
			if stage == "index" {
				for _, recoveryStage := range []string{"lease-intent", "lease-observed"} {
					out, err := exec.CommandContext(workerCtx, executable, "-test.run=^TestCommitCrashWorker$", "--", "engorch-commit-crash-worker", path, recoveryStage).CombinedOutput()
					exit, ok := err.(*exec.ExitError)
					if !ok || exit.ExitCode() != 23 {
						t.Fatalf("recovery worker did not exit at intent: %v %s", err, out)
					}
					s, err := Inspect(path)
					if err != nil || s.Commit.LeaseRecovery == nil || s.Commit.LeaseRecovery.Outcome != "UNKNOWN" {
						t.Fatal("interrupted lease recovery missing", err)
					}
					interrupted = &s.Commit.LeaseRecovery.Intent
					if _, err := RecoverCommitLease(ctx, path, interrupted.Prepared, interrupted.Authorization); err == nil {
						t.Fatal("interrupted recovery approval reused")
					}
				}
			}
			recovery, err := PrepareCommitLeaseRecovery(path, "fixture worker exited with status 23; all Git calls completed", true)
			if err != nil {
				t.Fatal(err)
			}
			if interrupted != nil {
				priorID, err := interrupted.Prepared.Intent.ID()
				if err != nil || recovery.Plan.PreviousIntentID != priorID {
					t.Fatal("recovery chain not bound", err)
				}
			}
			recoveryID, err := recovery.Intent.ID()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := RecoverCommitLease(ctx, path, recovery, effects.Authorization{}); err == nil {
				t.Fatal("unapproved recovery accepted")
			}
			recovered, recoveryErr := RecoverCommitLease(ctx, path, recovery, effects.Authorization{IntentID: recoveryID, Actor: "fixture"})
			if recovered.Commit == nil || recovered.Commit.LeaseRecovery == nil || recovered.Commit.LeaseRecovery.Outcome != "CONFIRMED" {
				t.Fatal("lease recovery not confirmed", recoveryErr)
			}
			if stage == "ref" || stage == "index" {
				// This fixture commits the unchanged tree, so its original index is
				// already correct even when exiting immediately after ref advancement.
				if recoveryErr != nil || recovered.Commit.Outcome != "CONFIRMED" {
					t.Fatal("final commit not recovered", recoveryErr)
				}
			} else if recoveryErr == nil || recovered.Commit.Outcome != "UNKNOWN" {
				t.Fatal("partial commit falsely confirmed", recoveryErr)
			}
			if _, err := os.Lstat(lock); !os.IsNotExist(err) {
				t.Fatal("recovered lease not released", err)
			}
			if _, err := RecoverCommitLease(ctx, path, recovery, effects.Authorization{IntentID: recoveryID, Actor: "fixture"}); err == nil {
				t.Fatal("recovery repeated")
			}
			if stage == "intent" || stage == "objects" {
				previousRecoveryID := ""
				if stage == "objects" {
					out, err := exec.CommandContext(workerCtx, executable, "-test.run=^TestCommitCrashWorker$", "--", "engorch-commit-crash-worker", path, "commit-recovery-intent").CombinedOutput()
					exit, ok := err.(*exec.ExitError)
					if !ok || exit.ExitCode() != 23 {
						t.Fatalf("commit recovery child did not exit: %v %s", err, out)
					}
					interrupted, err := Inspect(path)
					if err != nil || interrupted.Commit.Recovery == nil || interrupted.Commit.Recovery.Outcome != "UNKNOWN" {
						t.Fatal("interrupted commit recovery missing", err)
					}
					old := interrupted.Commit.Recovery.Intent
					previousRecoveryID, err = old.Prepared.Intent.ID()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := RecoverCommit(ctx, path, old.Prepared, old.Authorization); err == nil {
						t.Fatal("interrupted commit recovery approval reused")
					}
					leasePlan, err := PrepareCommitLeaseRecovery(path, "second worker exited after commit recovery intent", true)
					if err != nil {
						t.Fatal(err)
					}
					leaseID, err := leasePlan.Intent.ID()
					if err != nil {
						t.Fatal(err)
					}
					observed, err := RecoverCommitLease(ctx, path, leasePlan, effects.Authorization{IntentID: leaseID, Actor: "fixture"})
					if err == nil || observed.Commit.LeaseRecovery.Outcome != "CONFIRMED" || observed.Commit.Outcome != "UNKNOWN" {
						t.Fatal("second lease recovery classification wrong", err)
					}
				}
				completion, err := PrepareCommitRecovery(ctx, path, "fixture child exited; lease recovered; prior Git calls complete", true)
				if err != nil {
					t.Fatal(err)
				}
				if completion.Plan.PreviousIntentID != previousRecoveryID {
					t.Fatal("commit recovery successor not bound")
				}
				completionID, err := completion.Intent.ID()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := RecoverCommit(ctx, path, completion, effects.Authorization{IntentID: id, Actor: "fixture"}); err == nil {
					t.Fatal("original approval reused for recovery")
				}
				finished, err := RecoverCommit(ctx, path, completion, effects.Authorization{IntentID: completionID, Actor: "fixture"})
				if err != nil || finished.State != "COMMITTED" || finished.Commit.Recovery == nil || finished.Commit.Recovery.Outcome != "CONFIRMED" {
					t.Fatal("controller recovery failed", err)
				}
				if finished.Commit.Recovery.Receipt.IntentID != completionID || finished.Commit.Recovery.Receipt.ObservationHash != finished.Commit.Observation.Receipt.ObservationHash {
					t.Fatal("recovery receipt not bound to final evidence")
				}
				if _, err := RecoverCommit(ctx, path, completion, effects.Authorization{IntentID: completionID, Actor: "fixture"}); err == nil {
					t.Fatal("controller recovery repeated")
				}
			}
		})
	}
}

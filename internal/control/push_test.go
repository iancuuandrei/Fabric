package control

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/gitpush"
	"harness.local/engorch/internal/worktree"
)

func TestControllerPushAndReadOnlyReconciliation(t *testing.T) {
	for _, scenario := range []string{"sha1/execute", "sha256/execute", "sha1/lost-receipt", "sha256/lost-receipt", "sha1/absent", "sha1/changed-remote", "sha1/crash-intent", "sha1/crash-effect"} {
		t.Run(scenario, func(t *testing.T) {
			format, mode, _ := strings.Cut(scenario, "/")
			t.Setenv("GIT_DEFAULT_HASH", format)
			ctx := context.Background()
			path, _ := approvedRepository(t, config.Check{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10})
			if _, err := StartWorkspace(ctx, path); err != nil {
				t.Fatal(err)
			}
			remote := t.TempDir()
			if out, err := exec.Command("git", "-C", remote, "init", "--bare", "-q", "--object-format="+format).CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			if _, err := PreparePush(ctx, path, remote, "refs/heads/fixture"); err == nil {
				t.Fatal("uncommitted candidate admitted to push")
			}
			if _, err := Verify(ctx, path); err != nil {
				t.Fatal(err)
			}
			identity := gitlocal.Identity{Name: "Fixture", Email: "fixture@example.invalid", UnixSeconds: 1788739200}
			commit, err := PrepareCommit(ctx, path, identity, identity, "push fixture\n")
			if err != nil {
				t.Fatal(err)
			}
			commitID, err := commit.Intent.ID()
			if err != nil {
				t.Fatal(err)
			}
			s, err := ExecuteCommit(ctx, path, commit, effects.Authorization{IntentID: commitID, Actor: "fixture"})
			if err != nil || s.State != "COMMITTED" {
				t.Fatal("commit setup failed", err)
			}
			before := *s.Candidate
			prepared, err := PreparePush(ctx, path, remote, "refs/heads/fixture")
			if err != nil || prepared.Plan.ExpectedOld != nil {
				t.Fatal("push expectation incorrect", err)
			}
			id, err := prepared.Intent.ID()
			if err != nil {
				t.Fatal(err)
			}
			auth := effects.Authorization{IntentID: id, Actor: "fixture"}
			if mode == "crash-intent" || mode == "crash-effect" {
				qualifyPushCrash(t, path, prepared, auth, mode)
				return
			}
			if _, err := ExecutePush(ctx, path, prepared, effects.Authorization{}); err == nil {
				t.Fatal("push missing approval admitted")
			}
			if mode == "changed-remote" {
				// Another actor creates the branch after the absent-ref preview.
				old := s.Creation.Repository.Commit
				if out, pushErr := exec.Command("git", "-C", s.Creation.Repository.Root, "push", "--", remote, old+":"+prepared.Plan.TargetRef).CombinedOutput(); pushErr != nil {
					t.Fatal(pushErr, string(out))
				}
				s, err = ExecutePush(ctx, path, prepared, auth)
				if err == nil || s.Push == nil || s.Push.Outcome != "UNKNOWN" || s.State != "PUSHING" {
					t.Fatal("changed remote did not retain uncertainty", err)
				}
				observed, readErr := gitpush.ObserveRemote(ctx, prepared.Plan)
				if readErr != nil || observed.Commit == nil || *observed.Commit != old {
					t.Fatal("changed remote was overwritten", readErr)
				}
				if _, retryErr := ExecutePush(ctx, path, prepared, auth); retryErr == nil {
					t.Fatal("changed-remote push automatically retried")
				}
				return
			}
			if mode == "execute" {
				s, err = ExecutePush(ctx, path, prepared, auth)
			} else {
				lease, acquireErr := worktree.Acquire(prepared.Plan.Workspace.Request)
				if acquireErr != nil {
					t.Fatal(acquireErr)
				}
				if appendErr := Append(path, "push.intent", PushIntent{prepared, auth}); appendErr != nil {
					lease.Close()
					t.Fatal(appendErr)
				}
				if mode == "lost-receipt" {
					if _, pushErr := gitpush.Execute(ctx, prepared.Plan, prepared.Intent, auth); pushErr != nil {
						lease.Close()
						t.Fatal(pushErr)
					}
				}
				if closeErr := lease.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
				if _, retryErr := ExecutePush(ctx, path, prepared, auth); retryErr == nil {
					t.Fatal("pending push automatically retried")
				}
				s, err = ReconcilePush(ctx, path)
			}
			if mode == "absent" {
				if err == nil || s.State != "PUSHING" || s.Push.Outcome != "UNKNOWN" {
					t.Fatal("absence classified as non-execution", err)
				}
				observed, readErr := gitpush.ObserveRemote(ctx, prepared.Plan)
				if readErr != nil || observed.Commit != nil {
					t.Fatal("reconciliation performed a push", readErr)
				}
				if appendErr := Append(path, "push.intent", PushIntent{prepared, auth}); appendErr == nil {
					t.Fatal("UNKNOWN allowed another effect")
				}
				bad := *s.Push.Observation
				bad.Receipt.Outcome = "NOT_APPLIED"
				if appendErr := Append(path, "push.observed", bad); appendErr == nil {
					t.Fatal("absent branch fabricated non-execution")
				}
				bad = *s.Push.Observation
				foreign := *bad.Observation.Remote
				foreign.TargetRef = "refs/heads/other"
				bad.Observation.Remote = &foreign
				bad.Receipt.ObservationHash, err = canonical.Hash("harness.push-observation.v1", bad.Observation)
				if err != nil {
					t.Fatal(err)
				}
				if appendErr := Append(path, "push.observed", bad); appendErr == nil {
					t.Fatal("foreign branch observation admitted")
				}
			} else if err != nil || s.State != "PUSHED" || s.Push == nil || s.Push.Outcome != "CONFIRMED" || s.Push.Observation == nil || *s.Candidate != before {
				t.Fatal("push confirmation missing or source changed", err)
			}
			if _, retryErr := ExecutePush(ctx, path, prepared, auth); retryErr == nil {
				t.Fatal("push intent reused")
			}
		})
	}
}

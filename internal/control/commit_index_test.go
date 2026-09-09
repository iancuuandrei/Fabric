package control

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/worktree"
)

func TestCommitIndexFinalizationWithNewFile(t *testing.T) {
	for _, scenario := range []string{"sha1", "sha256", "sha1/controller", "sha256/controller", "sha1/recover-before", "sha256/recover-before", "sha1/recover-after", "sha256/recover-after"} {
		t.Run(scenario, func(t *testing.T) {
			format, mode, _ := strings.Cut(scenario, "/")
			t.Setenv("GIT_DEFAULT_HASH", format)
			ctx := context.Background()
			path, _ := approvedRepository(t, config.Check{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10})
			if _, err := StartWorkspace(ctx, path); err != nil {
				t.Fatal(err)
			}
			content := base64.StdEncoding.EncodeToString([]byte("candidate\x00bytes\n"))
			files, err := PrepareFiles(ctx, path, []fileeffects.Change{{Path: "nested/new.bin", ContentBase64: &content}})
			if err != nil {
				t.Fatal(err)
			}
			fileID, err := files.Intent.ID()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ApplyFiles(ctx, path, files, effects.Authorization{IntentID: fileID, Actor: "fixture"}); err != nil {
				t.Fatal(err)
			}
			if s, err := Verify(ctx, path); err != nil || s.State != "READY" {
				t.Fatal("verification", err)
			}
			identity := gitlocal.Identity{Name: "Fixture", Email: "fixture@example.invalid", UnixSeconds: 1788739200}
			p, err := PrepareCommit(ctx, path, identity, identity, "new file fixture\n")
			if err != nil {
				t.Fatal(err)
			}
			intentID, err := p.Intent.ID()
			if err != nil {
				t.Fatal(err)
			}
			auth := effects.Authorization{IntentID: intentID, Actor: "fixture"}
			if mode == "controller" {
				if _, err := ExecuteCommit(ctx, path, p, effects.Authorization{}); err == nil {
					t.Fatal("controller accepted missing authorization")
				}
				completed, err := ExecuteCommit(ctx, path, p, auth)
				if err != nil || completed.State != "COMMITTED" || completed.Commit == nil || completed.Commit.Outcome != "CONFIRMED" {
					t.Fatal("controller commit failed", err)
				}
				if _, err := ExecuteCommit(ctx, path, p, auth); err == nil {
					t.Fatal("controller repeated commit")
				}
				out, err := exec.Command("git", "-C", p.Plan.Workspace.Request.Path, "status", "--porcelain").Output()
				if err != nil || len(out) != 0 {
					t.Fatalf("controller left dirty workspace: %v %s", err, out)
				}
				if _, err := os.Stat(filepath.Join(p.Plan.Workspace.Request.Source.Root, "nested", "new.bin")); !os.IsNotExist(err) {
					t.Fatal("controller changed canonical checkout", err)
				}
				return
			}
			if _, err := RecordCommitIntent(ctx, path, p, auth); err != nil {
				t.Fatal(err)
			}
			pending, err := ReconcileCommit(ctx, path)
			if err == nil || pending.Commit == nil || pending.Commit.Outcome != "UNKNOWN" {
				t.Fatal("unexecuted intent falsely resolved", err)
			}
			lease, err := worktree.Acquire(p.Plan.Workspace.Request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := lease.Close(); err != nil {
					t.Error(err)
				}
			}()
			objects, err := gitlocal.ReadCandidateObjects(ctx, p.Plan)
			if err != nil {
				t.Fatal(err)
			}
			treeID := objects.Trees[len(objects.Trees)-1].ObjectID
			assertStage := func(want string) {
				t.Helper()
				state, err := gitlocal.InspectRecovery(ctx, p.Plan, treeID, objects.CommitID)
				if err != nil || state.Stage != want {
					t.Fatalf("recovery stage got %s want %s: %v", state.Stage, want, err)
				}
				assertRecoveryApprovalBinding(t, p, treeID, objects.CommitID, state)
			}
			assertStage("BEFORE_REF")
			if mode == "recover-before" {
				assertRecoveryExecutes(t, ctx, p, treeID, objects.CommitID)
				return
			}
			driftPath := filepath.Join(p.Plan.Workspace.Request.Path, "unexpected-drift")
			if err := os.WriteFile(driftPath, []byte("drift"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := gitlocal.InspectRecovery(ctx, p.Plan, treeID, objects.CommitID); err == nil {
				t.Fatal("drifted recovery state accepted")
			}
			if err := os.Remove(driftPath); err != nil {
				t.Fatal(err)
			}
			if _, err := gitlocal.FinalizeIndex(ctx, p.Plan, objects, p.Intent, auth); err == nil {
				t.Fatal("index finalized before ref advanced")
			}
			if err := gitlocal.StoreObjects(ctx, p.Plan, objects, p.Intent, auth); err != nil {
				t.Fatal(err)
			}
			assertStage("BEFORE_REF")
			if err := gitlocal.AdvanceRef(ctx, p.Plan, objects, p.Intent, auth); err != nil {
				t.Fatal(err)
			}
			assertStage("AFTER_REF")
			if mode == "recover-after" {
				assertRecoveryExecutes(t, ctx, p, treeID, objects.CommitID)
				return
			}
			if _, err := gitlocal.ObserveFinalized(ctx, p.Plan, treeID, objects.CommitID); err == nil {
				t.Fatal("unfinalized index confirmed")
			}
			if _, err := gitlocal.FinalizeIndex(ctx, p.Plan, objects, p.Intent, effects.Authorization{}); err == nil {
				t.Fatal("index finalized without approval")
			}
			after, err := gitlocal.FinalizeIndex(ctx, p.Plan, objects, p.Intent, auth)
			if err != nil {
				t.Fatal(err)
			}
			if after.FilesHash != p.Plan.Candidate.FilesHash || after.IndexHash == p.Plan.Candidate.IndexHash {
				t.Fatal("file/index finalization mismatch")
			}
			assertStage("FINALIZED")
			observed, err := gitlocal.ObserveFinalized(ctx, p.Plan, treeID, objects.CommitID)
			if err != nil || observed != after {
				t.Fatal("finalized observation differs", err)
			}
			if _, err := gitlocal.FinalizeIndex(ctx, p.Plan, objects, p.Intent, auth); err == nil {
				t.Fatal("changed index accepted for repeat finalization")
			}
			out, err := exec.Command("git", "-C", p.Plan.Workspace.Request.Path, "status", "--porcelain").Output()
			if err != nil || len(out) != 0 {
				t.Fatalf("finalized workspace not clean: %v %s", err, out)
			}
			data, err := os.ReadFile(filepath.Join(p.Plan.Workspace.Request.Path, "nested", "new.bin"))
			if err != nil || string(data) != "candidate\x00bytes\n" {
				t.Fatal("working file changed", err)
			}
			if _, err := os.Stat(filepath.Join(p.Plan.Workspace.Request.Source.Root, "nested", "new.bin")); !os.IsNotExist(err) {
				t.Fatal("canonical source gained candidate file", err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			confirmed, err := ReconcileCommit(ctx, path)
			if err != nil || confirmed.State != "COMMITTED" || confirmed.Commit.Outcome != "CONFIRMED" || confirmed.Candidate.Head != after.Head || confirmed.Candidate.FilesHash != after.FilesHash {
				t.Fatal("commit reconciliation failed", err)
			}
			if _, err := ReconcileCommit(ctx, path); err == nil {
				t.Fatal("resolved commit accepted duplicate observation")
			}
		})
	}
}

func assertRecoveryApprovalBinding(t *testing.T, prepared PreparedCommit, treeID, commitID string, state gitlocal.RecoveryState) {
	t.Helper()
	plan := gitlocal.RecoveryPlan{Version: 1, Commit: prepared.Plan, OriginalIntent: prepared.Intent, TreeID: treeID, CommitID: commitID, Before: state, Nonce: "fixture-recovery", Evidence: "fixture owns lease; all prior calls completed", WorkloadsStopped: true}
	id, err := plan.ID()
	if state.Stage == "FINALIZED" {
		if err == nil {
			t.Fatal("finalized state accepted for recovery mutation")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	intent, err := plan.Intent()
	if err != nil || intent.Kind != "commit_recovery" || intent.InputHash != id {
		t.Fatal("recovery effect binding", err)
	}
	originalID, err := prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := (effects.Authorization{IntentID: originalID, Actor: "fixture"}).Validate(intent); err == nil {
		t.Fatal("original approval authorized recovery")
	}
	changed := plan
	changed.Nonce += "-new"
	other, err := changed.ID()
	if err != nil || other == id {
		t.Fatal("recovery nonce not bound", err)
	}
	changed = plan
	changed.Evidence += " more evidence"
	other, err = changed.ID()
	if err != nil || other == id {
		t.Fatal("recovery evidence not bound", err)
	}
	changed = plan
	changed.WorkloadsStopped = false
	if _, err := changed.ID(); err == nil {
		t.Fatal("missing recovery quiescence accepted")
	}
	changed = plan
	changed.Before.Candidate.IndexHash = strings.Repeat("0", 64)
	if _, err := changed.ID(); err == nil {
		t.Fatal("foreign recovery index accepted")
	}
	changed = plan
	changed.OriginalIntent.InputHash = strings.Repeat("0", 64)
	if _, err := changed.ID(); err == nil {
		t.Fatal("foreign original intent accepted")
	}
}

func assertRecoveryExecutes(t *testing.T, ctx context.Context, prepared PreparedCommit, treeID, commitID string) {
	t.Helper()
	state, err := gitlocal.InspectRecovery(ctx, prepared.Plan, treeID, commitID)
	if err != nil {
		t.Fatal(err)
	}
	plan := gitlocal.RecoveryPlan{Version: 1, Commit: prepared.Plan, OriginalIntent: prepared.Intent, TreeID: treeID, CommitID: commitID, Before: state, Nonce: "execution-fixture", Evidence: "fixture holds lease; prior Git processes completed", WorkloadsStopped: true}
	intent, err := plan.Intent()
	if err != nil {
		t.Fatal(err)
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	auth := effects.Authorization{IntentID: id, Actor: "fixture"}
	if _, err := gitlocal.Recover(ctx, plan, intent, effects.Authorization{}); err == nil {
		t.Fatal("unapproved recovery executed")
	}
	data, err := json.Marshal(struct {
		Plan          gitlocal.RecoveryPlan
		Intent        effects.Intent
		Authorization effects.Authorization
	}{plan, intent, auth})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "recovery-intent.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	after, err := gitlocal.Recover(ctx, plan, intent, auth)
	if err != nil || after.Head != commitID || after.FilesHash != prepared.Plan.Candidate.FilesHash {
		t.Fatal("recovery execution failed", err)
	}
	if _, err := gitlocal.Recover(ctx, plan, intent, auth); err == nil {
		t.Fatal("stale recovery repeated")
	}
	out, err := exec.Command("git", "-C", prepared.Plan.Workspace.Request.Path, "status", "--porcelain").Output()
	if err != nil || len(out) != 0 {
		t.Fatalf("recovered workspace not clean: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(prepared.Plan.Workspace.Request.Source.Root, "nested", "new.bin")); !os.IsNotExist(err) {
		t.Fatal("recovery changed canonical source", err)
	}
}

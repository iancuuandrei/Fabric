package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
)

func TestLocalPlanApprovalAndResume(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	run := func(args ...string) []byte {
		t.Helper()
		var b bytes.Buffer
		if err := Execute(context.Background(), args, root, &b); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	run("init")
	var ignored bytes.Buffer
	if err := Execute(context.Background(), []string{"init"}, root, &ignored); err == nil {
		t.Fatal("overwrote configuration")
	}
	configurationPath := filepath.Join(root, "harness.toml")
	configurationBytes, err := os.ReadFile(configurationPath)
	if err != nil {
		t.Fatal(err)
	}
	configurationText := strings.Replace(string(configurationBytes), `["go", "test", "./..."]`, `["git", "--version"]`, 1)
	if err := os.WriteFile(configurationPath, []byte(configurationText), 0600); err != nil {
		t.Fatal(err)
	}
	run("doctor")
	strictHost := configurationText + "\n[host_policy]\nversion = 1\nallowed_hosts = [\"native\", \"codex\"]\nrequire_verified_sandbox = true\nallowed_sandbox_modes = []\n"
	if err := os.WriteFile(configurationPath, []byte(strictHost), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"doctor"}, {"plan", "Must not dispatch"}} {
		var denied bytes.Buffer
		err := Execute(context.Background(), command, root, &denied)
		if err == nil || !strings.Contains(err.Error(), "requires verified inherited sandbox evidence") || denied.Len() != 0 {
			t.Fatal("strict host policy did not reject before output", err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".harness")); !os.IsNotExist(err) {
		t.Fatal("host denial created run state", err)
	}
	if err := os.WriteFile(configurationPath, []byte(configurationText), 0600); err != nil {
		t.Fatal(err)
	}
	goalPath := filepath.Join(t.TempDir(), "goal.md")
	if err := os.WriteFile(goalPath, []byte("Improve the fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	result := run("plan", "--file", goalPath)
	var s control.Snapshot
	if err := json.Unmarshal(result, &s); err != nil {
		t.Fatal(err)
	}
	if s.Creation.Objective != "Improve the fixture" {
		t.Fatal("goal file was not bound to run creation")
	}
	if s.Creation.HostAdmission == nil {
		t.Fatal("new run omitted host admission evidence")
	}
	checkScheduleCLI(t, root, s, run)
	if s.State != "AWAITING_APPROVAL" {
		t.Fatal(s.State)
	}
	if string(run("resume", s.RunID)) != string(result) {
		t.Fatal("resume changed completed plan")
	}
	exported := run("inspect", s.RunID, "--export-jsonl")
	events, err := journal.Replay(exported)
	if err != nil {
		t.Fatal("export is not a valid canonical journal:", err)
	}
	exportedState, err := control.Replay(events)
	if err != nil || exportedState.RunID != s.RunID || exportedState.State != s.State {
		t.Fatal("export changed run semantics", exportedState, err)
	}
	if !bytes.Equal(exported, run("inspect", s.RunID, "--export-jsonl")) {
		t.Fatal("read-only export changed history")
	}
	var paused control.Snapshot
	if err := json.Unmarshal(run("pause", s.RunID, "fixture-human", "pause-1"), &paused); err != nil || paused.Lifecycle.Status != control.LifecyclePauseRequested {
		t.Fatal("pause request missing", err)
	}
	if err := Execute(context.Background(), []string{"resume", s.RunID, "fixture-human", "too-soon"}, root, &ignored); err == nil {
		t.Fatal("unsettled pause resumed")
	}
	if err := json.Unmarshal(run("settle-lifecycle", s.RunID, "fixture-human", "fake planner completed; no active workload", "workloads-stopped"), &paused); err != nil || paused.Lifecycle.Status != control.LifecyclePaused {
		t.Fatal("pause settlement missing", err)
	}
	if err := json.Unmarshal(run("resume", s.RunID, "fixture-human", "resume-1"), &paused); err != nil || paused.Lifecycle.Status != control.LifecycleActive {
		t.Fatal("dispatch resume missing", err)
	}
	var cancelled control.Snapshot
	if err := json.Unmarshal(run("plan", "cancel lifecycle fixture"), &cancelled); err != nil {
		t.Fatal(err)
	}
	run("cancel", cancelled.RunID, "fixture-human", "cancel-1")
	if err := json.Unmarshal(run("settle-lifecycle", cancelled.RunID, "fixture-human", "fake planner completed; no active workload", "workloads-stopped"), &cancelled); err != nil || cancelled.Lifecycle.Status != control.LifecycleCancelled {
		t.Fatal("cancellation settlement missing", err)
	}
	if err := Execute(context.Background(), []string{"resume", cancelled.RunID, "fixture-human", "invalid-revival"}, root, &ignored); err == nil {
		t.Fatal("cancelled run revived")
	}
	run("approve", s.RunID, s.PlanID, "fixture-human")
	var approved control.Snapshot
	if err := json.Unmarshal(run("inspect", s.RunID), &approved); err != nil {
		t.Fatal(err)
	}
	if approved.State != "IMPLEMENTING" {
		t.Fatal(approved.State)
	}
	run("run", s.RunID)
	var workspace control.Snapshot
	if err := json.Unmarshal(run("inspect", s.RunID), &workspace); err != nil {
		t.Fatal(err)
	}
	if workspace.WorkspaceOutcome != "CONFIRMED" || workspace.Workspace == nil {
		t.Fatal("workspace not admitted")
	}
	changesPath := filepath.Join(root, "changes.json")
	if err := os.WriteFile(changesPath, []byte(`[{"path":"added.txt","before_hash":null,"content_base64":"aGVsbG8K","executable":false}]`), 0600); err != nil {
		t.Fatal(err)
	}
	previewBytes := run("prepare-files", s.RunID, changesPath)
	var preview filePreview
	if err := json.Unmarshal(previewBytes, &preview); err != nil {
		t.Fatal(err)
	}
	previewPath := filepath.Join(root, "preview.json")
	if err := os.WriteFile(previewPath, previewBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), []string{"apply-files", s.RunID, previewPath, "wrong-id", "human"}, root, &ignored); err == nil {
		t.Fatal("wrong explicit effect approval admitted")
	}
	run("apply-files", s.RunID, previewPath, preview.IntentID, "fixture-human")
	added, err := os.ReadFile(filepath.Join(workspace.Workspace.Request.Path, "added.txt"))
	if err != nil || string(added) != "hello\n" {
		t.Fatal("CLI file effect missing", err)
	}
	run("verify", s.RunID)
	metadataPath := filepath.Join(root, "commit-metadata.json")
	metadata := `{"author":{"name":"Fixture","email":"fixture@example.invalid","unix_seconds":1788739200},"committer":{"name":"Fixture","email":"fixture@example.invalid","unix_seconds":1788739200},"message":"CLI fixture commit\n"}`
	if err := os.WriteFile(metadataPath, []byte(metadata), 0600); err != nil {
		t.Fatal(err)
	}
	commitBytes := run("prepare-commit", s.RunID, metadataPath)
	var commitPlan commitPreview
	if err := json.Unmarshal(commitBytes, &commitPlan); err != nil {
		t.Fatal(err)
	}
	commitPath := filepath.Join(root, "commit-preview.json")
	if err := os.WriteFile(commitPath, commitBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), []string{"commit", s.RunID, commitPath, "wrong-id", "fixture"}, root, &ignored); err == nil {
		t.Fatal("wrong commit approval accepted")
	}
	var committed control.Snapshot
	if err := json.Unmarshal(run("commit", s.RunID, commitPath, commitPlan.IntentID, "fixture"), &committed); err != nil {
		t.Fatal(err)
	}
	if committed.State != "COMMITTED" || committed.Commit == nil || committed.Commit.Outcome != "CONFIRMED" {
		t.Fatal("CLI commit not confirmed")
	}
	if err := Execute(context.Background(), []string{"commit", s.RunID, commitPath, commitPlan.IntentID, "fixture"}, root, &ignored); err == nil {
		t.Fatal("CLI repeated commit")
	}
	var pending control.Snapshot
	remote := t.TempDir()
	if out, err := exec.Command("git", "-C", remote, "init", "--bare", "-q", "--object-format="+committed.Creation.Repository.ObjectFormat).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	pushBytes := run("prepare-push", s.RunID, remote, "refs/heads/fixture")
	var publication pushPreview
	if err := json.Unmarshal(pushBytes, &publication); err != nil {
		t.Fatal(err)
	}
	pushPath := filepath.Join(root, "push-preview.json")
	if err := os.WriteFile(pushPath, pushBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), []string{"push", s.RunID, pushPath, "wrong-id", "fixture"}, root, &ignored); err == nil {
		t.Fatal("wrong push approval accepted")
	}
	var pushed control.Snapshot
	if err := json.Unmarshal(run("push", s.RunID, pushPath, publication.IntentID, "fixture"), &pushed); err != nil {
		t.Fatal(err)
	}
	if pushed.State != "PUSHED" || pushed.Push == nil || pushed.Push.Outcome != "CONFIRMED" {
		t.Fatal("CLI push not confirmed")
	}
	if err := Execute(context.Background(), []string{"push", s.RunID, pushPath, publication.IntentID, "fixture"}, root, &ignored); err == nil {
		t.Fatal("CLI push repeated")
	}
	if err := json.Unmarshal(run("plan", "Lease recovery fixture"), &pending); err != nil {
		t.Fatal(err)
	}
	run("approve", pending.RunID, pending.PlanID, "fixture")
	run("run", pending.RunID)
	run("verify", pending.RunID)
	var pendingPlan commitPreview
	if err := json.Unmarshal(run("prepare-commit", pending.RunID, metadataPath), &pendingPlan); err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(root, ".harness", "runs", pending.RunID+".jsonl")
	if _, err := control.RecordCommitIntent(context.Background(), pendingPath, pendingPlan.Prepared, effects.Authorization{IntentID: pendingPlan.IntentID, Actor: "fixture"}); err != nil {
		t.Fatal(err)
	}
	// CLI routing fixture: an ownerless persistent token; actual process exits
	// are covered independently by the controller crash tests.
	lockPath := filepath.Join(root, ".harness", "leases", pending.RunID+".lock")
	if err := os.WriteFile(lockPath, []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), []string{"prepare-commit-lease", pending.RunID, "fixture has no running owner", "not-stopped"}, root, &ignored); err == nil {
		t.Fatal("missing quiescence attestation accepted")
	}
	leaseBytes := run("prepare-commit-lease", pending.RunID, "fixture has no running owner", "workloads-stopped")
	var leasePreview commitLeasePreview
	if err := json.Unmarshal(leaseBytes, &leasePreview); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(root, "lease-preview.json")
	if err := os.WriteFile(leasePath, leaseBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), []string{"recover-commit-lease", pending.RunID, leasePath, "wrong-id", "fixture"}, root, &ignored); err == nil {
		t.Fatal("wrong lease approval accepted")
	}
	var recoveredOutput bytes.Buffer
	if err := Execute(context.Background(), []string{"recover-commit-lease", pending.RunID, leasePath, leasePreview.IntentID, "fixture"}, root, &recoveredOutput); err == nil {
		t.Fatal("incomplete commit incorrectly reported success")
	}
	var recovered control.Snapshot
	if err := json.Unmarshal(recoveredOutput.Bytes(), &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Commit == nil || recovered.Commit.Outcome != "UNKNOWN" || recovered.Commit.LeaseRecovery == nil || recovered.Commit.LeaseRecovery.Outcome != "CONFIRMED" {
		t.Fatalf("CLI recovery state mismatch: %+v", recovered.Commit)
	}
	if _, err := os.Lstat(lockPath); !os.IsNotExist(err) {
		t.Fatal("CLI lease not released", err)
	}
	if err := Execute(context.Background(), []string{"prepare-commit-recovery", pending.RunID, "fixture idle", "not-stopped"}, root, &ignored); err == nil {
		t.Fatal("commit recovery missing attestation accepted")
	}
	completionBytes := run("prepare-commit-recovery", pending.RunID, "fixture idle; lease recovery completed", "workloads-stopped")
	var completion commitRecoveryPreview
	if err := json.Unmarshal(completionBytes, &completion); err != nil {
		t.Fatal(err)
	}
	completionPath := filepath.Join(root, "commit-recovery-preview.json")
	if err := os.WriteFile(completionPath, completionBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), []string{"recover-commit", pending.RunID, completionPath, pendingPlan.IntentID, "fixture"}, root, &ignored); err == nil {
		t.Fatal("original commit approval reused by CLI")
	}
	var completed control.Snapshot
	if err := json.Unmarshal(run("recover-commit", pending.RunID, completionPath, completion.IntentID, "fixture"), &completed); err != nil {
		t.Fatal(err)
	}
	if completed.State != "COMMITTED" || completed.Commit.Recovery == nil || completed.Commit.Recovery.Outcome != "CONFIRMED" {
		t.Fatal("CLI recovery did not confirm commit")
	}
	if err := Execute(context.Background(), []string{"recover-commit", pending.RunID, completionPath, completion.IntentID, "fixture"}, root, &ignored); err == nil {
		t.Fatal("CLI recovery repeated")
	}
	var pendingPush pushPreview
	if err := json.Unmarshal(run("prepare-push", pending.RunID, remote, "refs/heads/pending"), &pendingPush); err != nil {
		t.Fatal(err)
	}
	if err := control.Append(pendingPath, "push.intent", control.PushIntent{Prepared: pendingPush.Prepared, Authorization: effects.Authorization{IntentID: pendingPush.IntentID, Actor: "fixture"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte(strings.Repeat("b", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	pushLeaseBytes := run("prepare-push-lease", pending.RunID, "fixture owner stopped; no Git child started", "workloads-stopped")
	var pushLease pushLeasePreview
	if err := json.Unmarshal(pushLeaseBytes, &pushLease); err != nil {
		t.Fatal(err)
	}
	pushLeasePath := filepath.Join(root, "push-lease-preview.json")
	if err := os.WriteFile(pushLeasePath, pushLeaseBytes, 0600); err != nil {
		t.Fatal(err)
	}
	var pushRecoveryOutput bytes.Buffer
	if err := Execute(context.Background(), []string{"recover-push-lease", pending.RunID, pushLeasePath, pushLease.IntentID, "fixture"}, root, &pushRecoveryOutput); err == nil {
		t.Fatal("unexecuted push falsely confirmed")
	}
	var pushRecovered control.Snapshot
	if err := json.Unmarshal(pushRecoveryOutput.Bytes(), &pushRecovered); err != nil {
		t.Fatal(err)
	}
	if pushRecovered.Push == nil || pushRecovered.Push.Outcome != "UNKNOWN" || pushRecovered.Push.LeaseRecovery == nil || pushRecovered.Push.LeaseRecovery.Outcome != "CONFIRMED" {
		t.Fatal("CLI push lease recovery classification mismatch")
	}
	statusBytes := run("status")
	var statuses []runStatus
	if err := json.Unmarshal(statusBytes, &statuses); err != nil || len(statuses) == 0 {
		t.Fatal("run status unavailable", err)
	}
	for _, forbidden := range []string{`"creation"`, `"objective"`, `"output"`, "Improve the fixture"} {
		if bytes.Contains(statusBytes, []byte(forbidden)) {
			t.Fatal("incidental status exposed run body", forbidden)
		}
	}
	for _, entry := range statuses {
		if entry.RunID == cancelled.RunID && entry.Lifecycle != control.LifecycleCancelled {
			t.Fatal("status lost cancellation")
		}
	}
	// A copied journal under another run name must be rejected before approval.
	original := filepath.Join(root, ".harness", "runs", s.RunID+".jsonl")
	data, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	otherID := strings.Repeat("f", 64)
	other := filepath.Join(root, ".harness", "runs", otherID+".jsonl")
	if err := os.WriteFile(other, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), []string{"approve", otherID, s.PlanID, "actor"}, root, &ignored); err == nil {
		t.Fatal("mismatched file accepted")
	}
	after, err := os.ReadFile(other)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("mismatched journal mutated", err)
	}
	if err := Execute(context.Background(), []string{"inspect", "../escape"}, root, &ignored); err == nil {
		t.Fatal("path traversal accepted")
	}
	content, err := os.ReadFile(filepath.Join(root, "source.txt"))
	if err != nil || string(content) != "fixture" {
		t.Fatal("planning mutated source", err)
	}
}

func TestReferenceIsCurrent(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "cli.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != Reference() {
		t.Fatal("regenerate CLI reference with harness reference")
	}
}

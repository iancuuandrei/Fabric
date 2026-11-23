package control

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/effects"
	"github.com/iancuuandrei/fabric/internal/fileeffects"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/verification"
)

func autonomousGitCmd(t *testing.T, root string, args []string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	return cmd.CombinedOutput()
}

func autonomousCreation(t *testing.T, maxRepairs int) Creation {
	t.Helper()
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
	c.Config.Verification = []config.Check{{Name: "unit", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
	c.Execution = &ExecutionPolicy{Mode: "autonomous-v1", MaxRepairs: maxRepairs}
	return c
}

func approvedRepositoryCreationNoApproval(t *testing.T, c Creation) (string, Snapshot) {
	t.Helper()
	root := c.Repository.Root
	autonomousGitInit(t, root)
	var err error
	c.Repository, err = repository.Discover(context.Background(), root, c.Config.Repository)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "run.jsonl")
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
	if s.State != "AWAITING_APPROVAL" {
		t.Fatalf("expected AWAITING_APPROVAL, got %s", s.State)
	}
	return p, s
}

func autonomousGitInit(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "file.txt"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base"}} {
		if out, err := autonomousGitCmd(t, root, args); err != nil {
			t.Fatal(err, string(out))
		}
	}
}

func autonomousAwaitingApproval(t *testing.T, c Creation) (string, Snapshot) {
	t.Helper()
	p, s := approvedRepositoryCreationNoApproval(t, c)
	return p, s
}

func verificationPlanFor(t *testing.T, path, candidateID string) (verification.Plan, error) {
	t.Helper()
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	return verification.PreparePlan(s.RunID, "autonomous-fixture", candidateID, s.Workspace.Request.Path, s.Creation.Config.Verification)
}

func countJournalKind(t *testing.T, path, kind string) int {
	t.Helper()
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func machinePolicyID(t *testing.T, s Snapshot) string {
	t.Helper()
	id, err := canonical.Hash("harness.execution-policy.v1", *s.Creation.Execution)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func recordAutonomousProposal(t *testing.T, path, after string) WriterRecord {
	t.Helper()
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	tryBodies := []string{"base\n", "writer output\n", "repaired\n", after}
	var lastErr error
	for _, before := range tryBodies {
		out, err := canonical.Bytes(WriterProposal{candidateID, []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText(before), ContentBase64: content64(after)}}})
		if err != nil {
			t.Fatal(err)
		}
		model := inv.Profile.Model
		r := runtime.Result{Version: 1, InvocationID: inv.ID, Requested: inv.Profile, ObservedModel: &model, Output: string(out)}
		rec, err := RecordWriterProposal(context.Background(), path, inv, r)
		if err == nil {
			return rec
		}
		lastErr = err
	}
	t.Fatalf("cannot record writer proposal: %v", lastErr)
	return WriterRecord{}
}

// failingAutonomousCreation returns an autonomous creation whose verification
// always fails so repair and budget boundaries are exercised deterministically.
func failingAutonomousCreation(t *testing.T, maxRepairs int) Creation {
	t.Helper()
	c := autonomousCreation(t, maxRepairs)
	c.Config.Verification = []config.Check{{Name: "unit", Argv: []string{"git", "--no-such-flag-xyz"}, TimeoutSeconds: 10}}
	return c
}

// machineAuthorizePlan records the exact machine plan approval for the
// awaiting-approval snapshot. It returns the bound policy identity.
func machineAuthorizePlan(t *testing.T, path string, s Snapshot) string {
	t.Helper()
	policyID := machinePolicyID(t, s)
	if err := Append(path, "plan.autonomous-authorized", MachineApproval{PlanID: s.PlanID, PolicyID: policyID, Actor: "fabric:autonomous"}); err != nil {
		t.Fatal(err)
	}
	return policyID
}

// applyWriterOutput starts the isolated workspace, records a writer proposal
// that writes after bytes, authorizes the file effect and confirms it. It
// returns the confirmed snapshot; callers keep their own outcome assertions.
func applyWriterOutput(t *testing.T, path, after string) Snapshot {
	t.Helper()
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	rec := recordAutonomousProposal(t, path, after)
	live, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := autonomousFileAuthorization(live, rec.Prepared.Intent)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := ApplyFiles(context.Background(), path, rec.Prepared, a)
	if err != nil || confirmed.FileOutcome != "CONFIRMED" {
		t.Fatal(err, confirmed.FileOutcome)
	}
	return confirmed
}

// repairingAutonomousFixture stages one autonomous run up to REPAIRING using
// the always-failing check: plan, machine authorize, initial writer output
// and failed verification. It preserves the exact IMPLEMENTING to REPAIRING
// transition each repair test asserts independently afterwards.
func repairingAutonomousFixture(t *testing.T, maxRepairs int) (string, Snapshot) {
	t.Helper()
	c := failingAutonomousCreation(t, maxRepairs)
	path, s := autonomousAwaitingApproval(t, c)
	machineAuthorizePlan(t, path, s)
	applyWriterOutput(t, path, "writer output\n")
	verifying, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if verifying.State != "REPAIRING" {
		t.Fatalf("failing check did not enter REPAIRING: %s", verifying.State)
	}
	return path, verifying
}

// confirmedAutonomousFixture stages one autonomous run up to a confirmed
// initial file effect with the default passing fixture check.
func confirmedAutonomousFixture(t *testing.T, maxRepairs int) (string, Snapshot) {
	t.Helper()
	c := autonomousCreation(t, maxRepairs)
	path, s := autonomousAwaitingApproval(t, c)
	machineAuthorizePlan(t, path, s)
	confirmed := applyWriterOutput(t, path, "writer output\n")
	return path, confirmed
}

func TestAutonomousPolicyPersistedAndLegacyIdentity(t *testing.T) {
	c := autonomousCreation(t, 2)
	p, s := approvedRepositoryCreation(t, c)
	if s.Creation.Execution == nil || s.Creation.Execution.Mode != "autonomous-v1" || s.Creation.Execution.MaxRepairs != 2 {
		t.Fatalf("execution policy not persisted: %#v", s.Creation.Execution)
	}
	policyID, err := autonomousPolicyID(s)
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical.Hash("harness.execution-policy.v1", *c.Execution)
	if err != nil || want != policyID {
		t.Fatal("policy identity mismatch", want, policyID, err)
	}
	raw, err := canonical.Bytes(s.Creation)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "autonomous-v1") {
		t.Fatal("persisted creation lost policy")
	}
	_ = p
	legacy := creation(t)
	legacyRaw, err := canonical.Bytes(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyRaw), "execution") {
		t.Fatal("legacy nil execution changed canonical identity")
	}
	legacyID, err := canonical.Hash("harness.run.v1", legacy)
	if err != nil {
		t.Fatal(err)
	}
	// Decoding old JSON without the new field must yield the same identity.
	oldJSON := append([]byte(nil), legacyRaw...)
	var decoded Creation
	if err := canonical.Decode(oldJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	decodedID, err := canonical.Hash("harness.run.v1", decoded)
	if err != nil || decodedID != legacyID {
		t.Fatal("legacy run identity not preserved", legacyID, decodedID)
	}
	if _, err := autonomousPolicyID(Snapshot{Creation: legacy}); err == nil {
		t.Fatal("legacy run admitted autonomous policy")
	}
}

func TestMachinePlanAuthorizationRequiresExactBinding(t *testing.T) {
	c := autonomousCreation(t, 2)
	path, s := autonomousAwaitingApproval(t, c)
	if s.State != "AWAITING_APPROVAL" {
		t.Fatalf("expected approval gate, got %s", s.State)
	}
	policyID := machinePolicyID(t, s)
	if err := Append(path, "plan.autonomous-authorized", MachineApproval{PlanID: "stale", PolicyID: policyID, Actor: "fabric:autonomous"}); err == nil {
		t.Fatal("stale plan authorized")
	}
	if err := Append(path, "plan.autonomous-authorized", MachineApproval{PlanID: s.PlanID, PolicyID: strings.Repeat("0", 64), Actor: "fabric:autonomous"}); err == nil {
		t.Fatal("foreign policy authorized")
	}
	if err := Append(path, "plan.autonomous-authorized", MachineApproval{PlanID: s.PlanID, PolicyID: policyID, Actor: "human"}); err == nil {
		t.Fatal("human actor used machine path")
	}
	eventsBefore, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	machineBefore := countJournalKind(t, path, "plan.autonomous-authorized")
	if err := Append(path, "plan.autonomous-authorized", MachineApproval{PlanID: s.PlanID, PolicyID: policyID, Actor: "fabric:autonomous"}); err != nil {
		t.Fatal(err)
	}
	s, err = Inspect(path)
	if err != nil || s.State != "IMPLEMENTING" || s.ApprovedBy != "fabric:autonomous" || s.MachineApproval == nil || s.MachineApproval.PolicyID != policyID {
		t.Fatalf("machine approval transition failed: %+v %v", s, err)
	}
	eventsAfter, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if countJournalKind(t, path, "plan.autonomous-authorized") != machineBefore+1 {
		t.Fatal("machine approval left no durable evidence")
	}
	if len(eventsAfter) != len(eventsBefore)+1 {
		t.Fatal("machine approval left no durable evidence")
	}
	last := eventsAfter[len(eventsAfter)-1]
	if last.Kind != "plan.autonomous-authorized" || last.Hash == "" || last.Hash == eventsBefore[len(eventsBefore)-1].Hash {
		t.Fatal("machine approval left no durable evidence")
	}
	legacy := creation(t)
	lpath, _ := autonomousAwaitingApproval(t, legacy)
	ls, err := Inspect(lpath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(lpath, "plan.autonomous-authorized", MachineApproval{PlanID: ls.PlanID, PolicyID: strings.Repeat("0", 64), Actor: "fabric:autonomous"}); err == nil {
		t.Fatal("legacy run admitted machine approval")
	}
}

func TestMachineFileAuthorizationIsFilesystemOnly(t *testing.T) {
	c := autonomousCreation(t, 2)
	path, _ := approvedRepositoryCreation(t, c)
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.MachineApproval != nil {
		t.Fatal("human-approved fixture should lack machine approval")
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	rec := recordAutonomousProposal(t, path, "writer output\n")
	// Without machine approval the autonomous file authorization must be rejected.
	{
		a, err := autonomousFileAuthorization(s, rec.Prepared.Intent)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ApplyFiles(context.Background(), path, rec.Prepared, a); err == nil {
			t.Fatal("machine file effect admitted without machine plan approval (stale snapshot)")
		}
	}
	live, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := autonomousFileAuthorization(live, rec.Prepared.Intent)
	if err != nil {
		t.Fatal(err)
	}
	if a.Actor != "fabric:autonomous" || a.Authority != "autonomous-v1" || a.PolicyID == "" {
		t.Fatalf("machine file authorization malformed: %+v", a)
	}
	if _, err := ApplyFiles(context.Background(), path, rec.Prepared, a); err == nil {
		t.Fatal("machine file effect admitted without machine plan approval")
	}
	// Machine-approve a fresh run and confirm the filesystem effect is admitted.
	c2 := autonomousCreation(t, 2)
	p2, s2 := autonomousAwaitingApproval(t, c2)
	policyID := machinePolicyID(t, s2)
	if err := Append(p2, "plan.autonomous-authorized", MachineApproval{PlanID: s2.PlanID, PolicyID: policyID, Actor: "fabric:autonomous"}); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), p2); err != nil {
		t.Fatal(err)
	}
	rec2 := recordAutonomousProposal(t, p2, "writer output\n")
	live2, err := Inspect(p2)
	if err != nil {
		t.Fatal(err)
	}
	a2, err := autonomousFileAuthorization(live2, rec2.Prepared.Intent)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := ApplyFiles(context.Background(), p2, rec2.Prepared, a2)
	if err != nil || confirmed.FileOutcome != "CONFIRMED" {
		t.Fatalf("machine filesystem effect rejected: %v %+v", err, confirmed)
	}
	// Non-filesystem kinds must never carry machine authority.
	h := strings.Repeat("a", 64)
	other := effects.Intent{Version: 1, RunID: h, PlanID: h, RepositoryID: h, Kind: "ri_producer", InputHash: h}
	otherID, err := other.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := (effects.Authorization{IntentID: otherID, Actor: "fabric:autonomous", Authority: "autonomous-v1", PolicyID: policyID}.Validate(other)); err == nil {
		t.Fatal("machine authority admitted non-filesystem effect")
	}
}

func TestExactInitialRestartBoundaryGoesDirectlyToVerify(t *testing.T) {
	path, confirmed := confirmedAutonomousFixture(t, 2)
	if !autonomousFilesApplied(confirmed) {
		t.Fatal("exact confirmed initial effect not recognized")
	}
	writersBefore := countJournalKind(t, path, "writer.proposed")
	verifiesBefore := countJournalKind(t, path, "verification.planned")
	final, err := RunAutonomous(context.Background(), path)
	if err != nil {
		t.Fatalf("autonomous restart failed: %v %+v", err, final)
	}
	if final.State != "READY" {
		t.Fatalf("initial repair did not verify to READY: %s", final.State)
	}
	if countJournalKind(t, path, "writer.proposed") != writersBefore {
		t.Fatal("confirmed initial effect sent another writer request instead of verifying")
	}
	if countJournalKind(t, path, "verification.planned") != verifiesBefore+1 {
		t.Fatal("confirmed initial effect did not verify exactly once")
	}
}

func TestExactRepairRestartBoundaryVerifiesBeforeNewSlot(t *testing.T) {
	path, verifying := repairingAutonomousFixture(t, 1)
	if verifying.State != "REPAIRING" {
		t.Fatalf("failing check did not enter REPAIRING: %s", verifying.State)
	}
	candidateB, _ := verifying.Candidate.ID()
	if err := Append(path, "autonomous.repair-started", AutonomousRepair{Attempt: 1, CandidateID: candidateB}); err != nil {
		t.Fatal(err)
	}
	rec2 := recordAutonomousProposal(t, path, "repaired\n")
	live2, _ := Inspect(path)
	a2, _ := autonomousFileAuthorization(live2, rec2.Prepared.Intent)
	repaired, err := ApplyFiles(context.Background(), path, rec2.Prepared, a2)
	if err != nil || repaired.FileOutcome != "CONFIRMED" {
		t.Fatal(err, repaired.FileOutcome)
	}
	if !autonomousRepairApplied(repaired) {
		t.Fatal("exact confirmed repair not recognized")
	}
	candidateC, _ := repaired.Candidate.ID()
	if candidateC == candidateB {
		t.Fatal("repair made no candidate change in fixture")
	}
	if autonomousVerificationCovered(repaired, candidateC) {
		t.Fatal("repaired candidate should not yet be verified")
	}
	verifiesBefore := countJournalKind(t, path, "verification.planned")
	repairsBefore := countJournalKind(t, path, "autonomous.repair-started")
	_, err = RunAutonomous(context.Background(), path)
	if !errors.Is(err, ErrAutonomousRepairBudget) {
		t.Fatalf("expected exhausted repair bound after verifying repair, got %v", err)
	}
	if countJournalKind(t, path, "verification.planned") != verifiesBefore+1 {
		t.Fatal("confirmed repair did not verify before exhausting the repair slot")
	}
	if countJournalKind(t, path, "autonomous.repair-started") != repairsBefore {
		t.Fatal("repair slot consumed before verifying the exact repaired candidate")
	}
}

func TestExhaustedRepairBudgetReturnsBoundWithoutNewSlot(t *testing.T) {
	path, verifying := repairingAutonomousFixture(t, 0)
	if verifying.State != "REPAIRING" {
		t.Fatalf("expected REPAIRING, got %+v", verifying)
	}
	before := countJournalKind(t, path, "autonomous.repair-started")
	_, err := RunAutonomous(context.Background(), path)
	if !errors.Is(err, ErrAutonomousRepairBudget) {
		t.Fatalf("expected repair bound, got %v", err)
	}
	if countJournalKind(t, path, "autonomous.repair-started") != before {
		t.Fatal("exhausted budget consumed another repair slot")
	}
}

func TestRepairNoProgressIsRejected(t *testing.T) {
	path, verifying := repairingAutonomousFixture(t, 2)
	if verifying.State != "REPAIRING" {
		t.Fatal(verifying.State)
	}
	snap := verifying
	candidateID, _ := snap.Candidate.ID()
	if err := Append(path, "autonomous.repair-started", AutonomousRepair{Attempt: 1, CandidateID: candidateID}); err != nil {
		t.Fatal(err)
	}
	// Admit a repair file intent but leave the workspace unchanged, so
	// reconciliation reports NOT_APPLIED with no candidate progress.
	prepared, err := PrepareFiles(context.Background(), path, []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText("writer output\n"), ContentBase64: content64("repaired\n")}})
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "files.intent", FileIntent{prepared, effects.Authorization{IntentID: intentID, Actor: "operator"}}); err != nil {
		t.Fatal(err)
	}
	reconciled, err := ReconcileFiles(context.Background(), path)
	if err != nil {
		// Reconcile returns an error for NOT_APPLIED? Inspect to confirm.
		reconciled, _ = Inspect(path)
	}
	if reconciled.FileOutcome != "NOT_APPLIED" {
		t.Fatalf("expected NOT_APPLIED repair attempt, got %s", reconciled.FileOutcome)
	}
	before := countJournalKind(t, path, "autonomous.repair-started")
	_, err = RunAutonomous(context.Background(), path)
	if !errors.Is(err, ErrAutonomousNoProgress) {
		t.Fatalf("expected no-progress rejection, got %v", err)
	}
	if countJournalKind(t, path, "autonomous.repair-started") != before {
		t.Fatal("no-progress repair consumed another slot")
	}
}

func TestRepairCounterPersistsAndIsBounded(t *testing.T) {
	path, verifying := repairingAutonomousFixture(t, 1)
	snap := verifying
	if snap.State != "REPAIRING" {
		t.Fatal(snap.State)
	}
	candidateID, _ := snap.Candidate.ID()
	if err := Append(path, "autonomous.repair-started", AutonomousRepair{Attempt: 1, CandidateID: candidateID}); err != nil {
		t.Fatal(err)
	}
	snap, err := Inspect(path)
	if err != nil || snap.RepairAttempts != 1 || snap.RepairCandidateID != candidateID {
		t.Fatalf("repair counter not persisted: %+v %v", snap, err)
	}
	if err := Append(path, "autonomous.repair-started", AutonomousRepair{Attempt: 1, CandidateID: candidateID}); err == nil {
		t.Fatal("duplicate repair attempt admitted")
	}
	if err := Append(path, "autonomous.repair-started", AutonomousRepair{Attempt: 2, CandidateID: candidateID}); err == nil {
		t.Fatal("repair budget overrun admitted")
	}
	if err := Append(path, "autonomous.repair-started", AutonomousRepair{Attempt: 2, CandidateID: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("foreign repair candidate admitted")
	}
}

func TestRepairDuplicateSlotRejectedWithBudgetRemaining(t *testing.T) {
	path, verifying := repairingAutonomousFixture(t, 2)
	snap, err := Inspect(path)
	if err != nil || snap.State != "REPAIRING" {
		t.Fatal(snap.State, err)
	}
	_ = verifying
	candidateID, _ := snap.Candidate.ID()
	if err := Append(path, "autonomous.repair-started", AutonomousRepair{Attempt: 1, CandidateID: candidateID}); err != nil {
		t.Fatal(err)
	}
	// Budget remains (1 < MaxRepairs 2), so this rejection is the duplicate-slot
	// guard, not max-budget overflow: the same candidate must not consume a
	// second slot without candidate progress.
	if err := Append(path, "autonomous.repair-started", AutonomousRepair{Attempt: 2, CandidateID: candidateID}); err == nil {
		t.Fatal("same candidate consumed a second repair slot with budget remaining")
	}
	snap, err = Inspect(path)
	if err != nil || snap.RepairAttempts != 1 || snap.RepairCandidateID != candidateID {
		t.Fatalf("duplicate repair attempt changed persisted counter: %+v %v", snap, err)
	}
	if countJournalKind(t, path, "autonomous.repair-started") != 1 {
		t.Fatal("duplicate repair slot left durable evidence")
	}
}

func TestReadyReturnsImmediately(t *testing.T) {
	path, _ := confirmedAutonomousFixture(t, 2)
	ready, err := Verify(context.Background(), path)
	if err != nil || ready.State != "READY" {
		t.Fatal(ready.State, err)
	}
	eventsBefore, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	headBefore := eventsBefore[len(eventsBefore)-1].Hash
	final, err := RunAutonomous(context.Background(), path)
	if err != nil || final.State != "READY" {
		t.Fatalf("READY did not return: %v %+v", err, final)
	}
	eventsAfter, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfter) != len(eventsBefore) || eventsAfter[len(eventsAfter)-1].Hash != headBefore {
		t.Fatal("READY run changed the journal")
	}
}

func TestPendingVerificationAndUnknownGuards(t *testing.T) {
	path, _ := confirmedAutonomousFixture(t, 2)
	// Pending verification must block automatic execution.
	snap, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	candidateID, _ := snap.Candidate.ID()
	plan, err := verificationPlanFor(t, path, candidateID)
	if err != nil {
		t.Fatal(err)
	}
	planID, _ := plan.ID()
	if err := Append(path, "verification.planned", plan); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "verification.started", VerificationStart{planID, 0}); err != nil {
		t.Fatal(err)
	}
	observedBefore := countJournalKind(t, path, "verification.observed")
	if _, err := RunAutonomous(context.Background(), path); err == nil || !errors.Is(err, ErrAutonomousReconciliation) {
		t.Fatalf("pending verification resent: %v", err)
	}
	if countJournalKind(t, path, "verification.observed") != observedBefore {
		t.Fatal("pending verification advanced without observation")
	}
}

func TestUnknownFileEffectIsReconciledNotResent(t *testing.T) {
	c := autonomousCreation(t, 2)
	path, s := autonomousAwaitingApproval(t, c)
	policyID := machinePolicyID(t, s)
	if err := Append(path, "plan.autonomous-authorized", MachineApproval{PlanID: s.PlanID, PolicyID: policyID, Actor: "fabric:autonomous"}); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareFiles(context.Background(), path, []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText("base\n"), ContentBase64: content64("after\n")}})
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "files.intent", FileIntent{prepared, effects.Authorization{IntentID: intentID, Actor: "operator"}}); err != nil {
		t.Fatal(err)
	}
	intentsBefore := countJournalKind(t, path, "files.intent")
	snap, err := Inspect(path)
	if err != nil || snap.FileOutcome != "UNKNOWN" {
		t.Fatal(snap.FileOutcome, err)
	}
	after, err := RunAutonomous(context.Background(), path)
	if err != nil {
		// Reconciliation without external application yields NOT_APPLIED and
		// stays IMPLEMENTING; the key guard is no second intent.
		t.Logf("reconcile returned: %v", err)
		after, _ = Inspect(path)
	}
	if countJournalKind(t, path, "files.intent") != intentsBefore {
		t.Fatal("UNKNOWN file effect resent instead of reconciled")
	}
	_ = after
}

func TestContextCancellationStopsPromptly(t *testing.T) {
	c := autonomousCreation(t, 2)
	path, s := autonomousAwaitingApproval(t, c)
	policyID := machinePolicyID(t, s)
	if err := Append(path, "plan.autonomous-authorized", MachineApproval{PlanID: s.PlanID, PolicyID: policyID, Actor: "fabric:autonomous"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	eventsBefore, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	headBefore := eventsBefore[len(eventsBefore)-1].Hash
	if _, err := RunAutonomous(ctx, path); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context not honored: %v", err)
	}
	eventsAfter, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsAfter) != len(eventsBefore) || eventsAfter[len(eventsAfter)-1].Hash != headBefore {
		t.Fatal("cancelled run changed the journal")
	}
}

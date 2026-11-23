package control

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/memoryadmission"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

func TestGraphMemoryAdmissionWaitsForConcurrentDuplicateTask(t *testing.T) {
	c := isolatedThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := graphReadyTasks(current)
	if err != nil {
		t.Fatal(err)
	}
	var implementations []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			implementations = append(implementations, task)
		}
	}
	err = runIsolatedGraphWriterBatch(context.Background(), path, current, implementations)
	if err != nil {
		t.Fatal(err)
	}
	latest, inspectErr := Inspect(path)
	if inspectErr != nil || latest.GraphMemoryAdmission == nil || latest.GraphMemoryAdmission.Sequence < len(implementations) || len(latest.GraphMemoryAdmission.ActiveTaskIDs) != 0 || len(latest.GraphWriterResults) != len(implementations) {
		t.Fatalf("isolated memory reservations did not settle one result per task: inspect=%v admissions=%+v results=%d", inspectErr, latest.GraphMemoryAdmission, len(latest.GraphWriterResults))
	}
}

func TestUnknownSchedulerClaimRetainsMemoryAdmissionCeiling(t *testing.T) {
	c := isolatedThreeWriterCreation(t)
	path, s := isolatedGraphAwaitingApprovalWithGraph(t, c, isolatedThreeWriterGraphFixture())
	machineAuthorizePlan(t, path, s)
	if _, err := ensureGraphRecorded(path, s); err != nil {
		t.Fatal(err)
	}
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareIsolatedGraphWriterCohort(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	current, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	ready, err := graphReadyTasks(current)
	if err != nil {
		t.Fatal(err)
	}
	var implementations []engineeringplan.Task
	for _, task := range ready {
		if task.Kind == engineeringplan.Implementation {
			implementations = append(implementations, task)
		}
	}
	specs, err := buildGraphWriterBatchSpecs(path, current, implementations)
	if err != nil || len(specs) < 2 {
		t.Fatalf("writer cohort specs unavailable: count=%d err=%v", len(specs), err)
	}
	gate := newGraphMemoryAdmissionGate(path, func() memoryadmission.Observation {
		return memoryadmission.Observation{Status: memoryadmission.ObservationObserved, Source: memoryadmission.SourceWindowsGlobal, AvailableMiB: 1024, TotalMiB: 4096}
	})
	finish, err := gate.BeforeClaim(context.Background(), specs[0])
	if err != nil || finish == nil {
		t.Fatalf("initial memory reservation failed: callback=%v err=%v", finish != nil, err)
	}
	completion, err := finish(taskscheduler.AdmissionClaimUnknown)
	if err != nil {
		t.Fatal(err)
	}
	if completion != nil {
		completion()
	}
	before, err := Inspect(path)
	if err != nil || before.GraphMemoryAdmission == nil || before.GraphMemoryAdmission.Decision.EffectiveWorkers != 1 || len(before.GraphMemoryAdmission.ActiveTaskIDs) != 1 {
		t.Fatalf("ambiguous scheduler claim did not retain serial admission: state=%+v err=%v", before.GraphMemoryAdmission, err)
	}
	if _, ok := before.GraphMemoryAdmission.ActiveTaskIDs[specs[0].ID]; !ok {
		t.Fatalf("ambiguous task reservation was refunded: %+v", before.GraphMemoryAdmission.ActiveTaskIDs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := gate.BeforeClaim(ctx, specs[1]); err == nil {
		t.Fatal("future claim bypassed the active UNKNOWN reservation ceiling")
	}
	after, err := Inspect(path)
	if err != nil || after.GraphMemoryAdmission == nil || len(after.GraphMemoryAdmission.ActiveTaskIDs) != 1 || after.GraphMemoryAdmission.Sequence != before.GraphMemoryAdmission.Sequence {
		t.Fatalf("blocked claim changed ambiguous admission: state=%+v err=%v", after.GraphMemoryAdmission, err)
	}
}

func TestGraphMemoryCorrectionReleaseRequiresExactSettledPredecessor(t *testing.T) {
	priorInvocation := runtime.Invocation{ID: "prior-invocation", Profile: runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: "writer"}}
	correctionInvocation := runtime.Invocation{ID: "correction-invocation", Profile: runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: "writer"}}
	claim := taskscheduler.Claim{Version: 1, ScheduleID: graphMemoryTestDigest("schedule"), Task: taskscheduler.TaskSpec{ID: "prior-task", RunID: graphMemoryTestDigest("run"), ControllerPath: "C:\\controller.jsonl", Operation: taskscheduler.OperationWriter, InvocationID: priorInvocation.ID}, Generation: 1, ControllerHead: graphMemoryTestDigest("controller-head")}
	claimID, err := claim.ID()
	if err != nil {
		t.Fatal(err)
	}
	result := runtime.Result{Output: "malformed writer result"}
	domain, err := runtimeResultDomain("writer")
	if err != nil {
		t.Fatal(err)
	}
	resultHash, err := canonical.Hash(domain, result)
	if err != nil {
		t.Fatal(err)
	}
	correction := RoleSemanticCorrection{
		Version: 1, Attempt: 1, CandidateID: graphMemoryTestDigest("candidate"), BaseInvocationID: priorInvocation.ID,
		TaskID: "graph-task", PriorTaskID: claim.Task.ID, ScheduledTaskID: "correction-task",
		PriorScheduleID: claim.ScheduleID, PriorScheduleDefinitionHash: graphMemoryTestDigest("schedule-definition"),
		PriorClaim: &claim, PriorEvidence: &taskscheduler.Evidence{RunID: claim.Task.RunID, InvocationID: claim.Task.InvocationID, ControllerHead: graphMemoryTestDigest("evidence-head"), AdmissionID: graphMemoryTestDigest("claim-admission"), Status: taskscheduler.StatusFailed},
		Original: priorInvocation, Predecessor: result, ReceiptHead: graphMemoryTestDigest("runtime-receipt"), Rejection: "invalid output", Invocation: correctionInvocation,
	}
	active := GraphMemoryAdmissionActive{AdmissionID: graphMemoryTestDigest("memory-admission"), InvocationID: priorInvocation.ID}
	release := GraphMemoryAdmissionRelease{
		Version: 1, TaskID: correction.PriorTaskID, AdmissionID: active.AdmissionID, Reason: "semantic_correction",
		CorrectionTaskID: correction.ScheduledTaskID, PriorClaimID: claimID, PriorScheduleID: correction.PriorScheduleID,
		PriorScheduleDefinitionHash: correction.PriorScheduleDefinitionHash, InvocationID: priorInvocation.ID,
		ReceiptHead: correction.ReceiptHead, ResultHash: resultHash,
	}
	s := Snapshot{RoleCorrections: []RoleSemanticCorrection{correction}}
	if err := validateGraphMemoryCorrectionRelease(s, release, active); err != nil {
		t.Fatalf("exact completed rejection did not release its old reservation: %v", err)
	}
	for name, mutate := range map[string]func(*GraphMemoryAdmissionRelease){
		"different predecessor task":  func(r *GraphMemoryAdmissionRelease) { r.TaskID = "other-task" },
		"different claim":             func(r *GraphMemoryAdmissionRelease) { r.PriorClaimID = graphMemoryTestDigest("other-claim") },
		"different schedule":          func(r *GraphMemoryAdmissionRelease) { r.PriorScheduleID = graphMemoryTestDigest("other-schedule") },
		"different receipt":           func(r *GraphMemoryAdmissionRelease) { r.ReceiptHead = graphMemoryTestDigest("other-receipt") },
		"different result":            func(r *GraphMemoryAdmissionRelease) { r.ResultHash = graphMemoryTestDigest("other-result") },
		"different active invocation": func(r *GraphMemoryAdmissionRelease) { r.InvocationID = "other-invocation" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := release
			mutate(&bad)
			if err := validateGraphMemoryCorrectionRelease(s, bad, active); err == nil {
				t.Fatal("mismatched correction evidence released a memory reservation")
			}
		})
	}
}

func TestGraphMemoryScopeReleaseRequiresDurableFailedMember(t *testing.T) {
	invocation := runtime.Invocation{ID: "scope-result-invocation", Profile: runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "fixture", Effort: "high", Role: "writer"}}
	claim := taskscheduler.Claim{Version: 1, ScheduleID: graphMemoryTestDigest("scope-schedule"), Task: taskscheduler.TaskSpec{ID: "scope-task", RunID: graphMemoryTestDigest("scope-run"), ControllerPath: "C:\\controller.jsonl", Operation: taskscheduler.OperationWriter, InvocationID: invocation.ID}, Generation: 1, ControllerHead: graphMemoryTestDigest("scope-controller-head")}
	claimID, err := claim.ID()
	if err != nil {
		t.Fatal(err)
	}
	receiptHead := graphMemoryTestDigest("scope-runtime-head")
	resultHash := graphMemoryTestDigest("scope-result")
	member := SettledGraphWriterMember{
		TaskID: "scope-task", Claim: claim, ClaimID: claimID,
		Evidence:   taskscheduler.Evidence{RunID: claim.Task.RunID, InvocationID: invocation.ID, ControllerHead: graphMemoryTestDigest("scope-failed-head"), AdmissionID: graphMemoryTestDigest("scope-failed-admission"), Status: taskscheduler.StatusFailed},
		Invocation: invocation, RuntimeReceipt: WriterRuntimeReceipt{InvocationID: invocation.ID, JournalHead: receiptHead, ResultHash: resultHash},
		ResultHash: resultHash, ScopeViolation: true, RejectedResult: &runtime.Result{Output: "scope-rejected"},
	}
	request := ScopeReplanRequest{Version: 1, RequestID: graphMemoryTestDigest("scope-request"), Cohort: SettledGraphWriterCohort{Members: []SettledGraphWriterMember{member}}}
	active := GraphMemoryAdmissionActive{AdmissionID: graphMemoryTestDigest("scope-grant"), InvocationID: invocation.ID}
	release, found, err := scopeReplanMemoryRelease(request, member, map[string]GraphMemoryAdmissionActive{"scope-task": active})
	if err != nil || !found {
		t.Fatalf("exact settled scope violation did not release: found=%v err=%v", found, err)
	}
	snapshot := Snapshot{ScopeReplanRequests: []ScopeReplanRequest{request}}
	if err := validateGraphMemoryScopeReplanRelease(snapshot, release, active); err != nil {
		t.Fatalf("exact durable request did not validate release: %v", err)
	}
	if _, found, err := scopeReplanMemoryRelease(request, member, map[string]GraphMemoryAdmissionActive{}); err != nil || found {
		t.Fatalf("already-settled or previously released grant was not an idempotent no-op: found=%v err=%v", found, err)
	}

	for name, mutate := range map[string]func(*ScopeReplanRequest, *SettledGraphWriterMember, *GraphMemoryAdmissionRelease, *GraphMemoryAdmissionActive){
		"not a scope violation": func(_ *ScopeReplanRequest, m *SettledGraphWriterMember, _ *GraphMemoryAdmissionRelease, _ *GraphMemoryAdmissionActive) {
			m.ScopeViolation = false
		},
		"unknown member": func(_ *ScopeReplanRequest, m *SettledGraphWriterMember, _ *GraphMemoryAdmissionRelease, _ *GraphMemoryAdmissionActive) {
			m.Evidence.Status = taskscheduler.StatusUnknown
		},
		"wrong request": func(_ *ScopeReplanRequest, _ *SettledGraphWriterMember, rel *GraphMemoryAdmissionRelease, _ *GraphMemoryAdmissionActive) {
			rel.ScopeRequestID = graphMemoryTestDigest("other-request")
		},
		"wrong invocation": func(_ *ScopeReplanRequest, _ *SettledGraphWriterMember, rel *GraphMemoryAdmissionRelease, a *GraphMemoryAdmissionActive) {
			rel.InvocationID = "other-invocation"
			a.InvocationID = "other-invocation"
		},
		"wrong receipt": func(_ *ScopeReplanRequest, _ *SettledGraphWriterMember, rel *GraphMemoryAdmissionRelease, _ *GraphMemoryAdmissionActive) {
			rel.ReceiptHead = graphMemoryTestDigest("other-head")
		},
		"wrong result": func(_ *ScopeReplanRequest, _ *SettledGraphWriterMember, rel *GraphMemoryAdmissionRelease, _ *GraphMemoryAdmissionActive) {
			rel.ResultHash = graphMemoryTestDigest("other-result")
		},
		"missing rejected result": func(_ *ScopeReplanRequest, m *SettledGraphWriterMember, _ *GraphMemoryAdmissionRelease, _ *GraphMemoryAdmissionActive) {
			m.RejectedResult = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			badRequest := request
			badMember := member
			badRelease := release
			badActive := active
			badRequest.Cohort.Members = []SettledGraphWriterMember{badMember}
			mutate(&badRequest, &badRequest.Cohort.Members[0], &badRelease, &badActive)
			badSnapshot := Snapshot{ScopeReplanRequests: []ScopeReplanRequest{badRequest}}
			if err := validateGraphMemoryScopeReplanRelease(badSnapshot, badRelease, badActive); err == nil {
				t.Fatal("mismatched or unsettled scope evidence released a grant")
			}
		})
	}
}

func graphMemoryTestDigest(value string) string {
	digest, _ := canonical.Hash("harness.test.graph-memory", value)
	return digest
}

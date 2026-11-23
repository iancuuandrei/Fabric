package control

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
)

func settledCorrectionProjection(t *testing.T) (Snapshot, SettledGraphWriterMember) {
	t.Helper()
	s := scopeReplanFixture(t)
	s.Creation.Execution.ScopeReplanVersion = 2
	s.Creation.Execution.ScopeReplanDesignVersion = 2
	ready, err := graphReadyTasks(s)
	if err != nil {
		t.Fatal(err)
	}
	var task engineeringplan.Task
	for _, candidate := range ready {
		if candidate.Kind == engineeringplan.Implementation {
			task = candidate
			break
		}
	}
	if task.ID == "" {
		t.Fatal("implementation task unavailable")
	}
	controllerPath := filepath.Join(t.TempDir(), "controller.jsonl")
	scheduleID := strings.Repeat("9", 64)
	path := controllerPath + ".graph-writers-" + strings.Repeat("8", 16) + ".jsonl"
	staticInvocation, err := writerInvocationForTask(s, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	base, err := writerInvocationForTaskBase(s, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	staticModel := staticInvocation.Profile.Model
	staticResult := runtime.Result{Version: 1, InvocationID: staticInvocation.ID, Requested: staticInvocation.Profile, ObservedModel: &staticModel, Output: "malformed"}
	staticHash, err := canonical.Hash("harness.writer-result.v1", staticResult)
	if err != nil {
		t.Fatal(err)
	}
	staticHead := strings.Repeat("a", 64)
	claimHead := strings.Repeat("b", 64)
	staticTaskSpec := taskscheduler.TaskSpec{ID: task.ID, RunID: s.RunID, ControllerPath: controllerPath, Operation: taskscheduler.OperationWriter, InvocationID: staticInvocation.ID}
	staticClaim := taskscheduler.Claim{Version: 1, ScheduleID: scheduleID, Task: staticTaskSpec, Generation: 1, ControllerHead: claimHead}
	staticEvidence := taskscheduler.Evidence{RunID: s.RunID, InvocationID: staticInvocation.ID, ControllerHead: claimHead, AdmissionID: strings.Repeat("c", 64), Status: taskscheduler.StatusFailed}
	candidateID, err := s.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	correctionInvocation, err := runtime.NewInvocation(base.Profile, "bounded correction")
	if err != nil {
		t.Fatal(err)
	}
	prior := scheduledRoleCorrectionBinding{SchedulePath: path, ScheduleID: scheduleID, ScheduleDefinitionHash: scheduleID, PriorTaskID: task.ID, Claim: staticClaim, Evidence: staticEvidence}
	correctionID := scheduledCorrectionTaskID(s.RunID, task.ID, prior, correctionInvocation, 1)
	turnInvocation, err := scheduledTurnInvocation(correctionInvocation, taskscheduler.OperationWriter, correctionID)
	if err != nil {
		t.Fatal(err)
	}
	dynamic := taskscheduler.DynamicTask{Task: taskscheduler.TaskSpec{ID: correctionID, RunID: s.RunID, ControllerPath: controllerPath, Operation: taskscheduler.OperationWriter, InvocationID: turnInvocation.ID}, ParentAgentID: strings.Repeat("d", 64), AgentID: strings.Repeat("e", 64), TurnID: correctionID, TurnSequence: 1}
	turn := taskscheduler.AgentTurnBinding{ParentAgentID: dynamic.ParentAgentID, AgentID: dynamic.AgentID, TurnID: dynamic.TurnID, TurnSequence: dynamic.TurnSequence}
	claim := taskscheduler.Claim{Version: 1, ScheduleID: scheduleID, Task: dynamic.Task, Generation: 1, ControllerHead: strings.Repeat("f", 64), AgentTurn: &turn}
	claimID, err := claim.ID()
	if err != nil {
		t.Fatal(err)
	}
	evidence := taskscheduler.Evidence{RunID: s.RunID, InvocationID: turnInvocation.ID, ControllerHead: strings.Repeat("1", 64), AdmissionID: strings.Repeat("2", 64), Status: taskscheduler.StatusFailed}
	finalModel := turnInvocation.Profile.Model
	finalResult := runtime.Result{Version: 1, InvocationID: turnInvocation.ID, Requested: turnInvocation.Profile, ObservedModel: &finalModel, Output: "valid proposal"}
	finalHash, err := canonical.Hash("harness.writer-result.v1", finalResult)
	if err != nil {
		t.Fatal(err)
	}
	finalHead := strings.Repeat("3", 64)
	correction := RoleSemanticCorrection{Version: 1, Attempt: 1, CandidateID: candidateID, BaseInvocationID: base.ID, TaskID: task.ID, PriorTaskID: task.ID, ScheduledTaskID: correctionID, PriorSchedulePath: path, PriorScheduleID: scheduleID, PriorScheduleDefinitionHash: scheduleID, PriorClaim: &staticClaim, PriorEvidence: &staticEvidence, Original: staticInvocation, Predecessor: staticResult, ReceiptHead: staticHead, Rejection: "invalid proposal", Invocation: correctionInvocation}
	correctionHash, err := canonical.Hash("harness.role-semantic-correction.v1", correction)
	if err != nil {
		t.Fatal(err)
	}
	s.RoleCorrections = []RoleSemanticCorrection{correction}
	s.GraphWriterHosts = map[string]WriterHostState{
		task.ID:      {Intent: WriterHostIntent{Invocation: staticInvocation}, RuntimeReceipt: &WriterRuntimeReceipt{InvocationID: staticInvocation.ID, ThreadID: "thread-static", TurnID: "turn-static", JournalHead: staticHead, ResultHash: staticHash}},
		correctionID: {Intent: WriterHostIntent{Invocation: turnInvocation}, RuntimeReceipt: &WriterRuntimeReceipt{InvocationID: turnInvocation.ID, ThreadID: "thread-correction", TurnID: correctionID, JournalHead: finalHead, ResultHash: finalHash}},
	}
	member := SettledGraphWriterMember{
		TaskID: task.ID, Claim: staticClaim, ClaimID: mustClaimID(t, staticClaim), Evidence: staticEvidence, Invocation: staticInvocation,
		ResultInvocation: &turnInvocation, ResultEvidence: &evidence, RuntimeReceipt: *s.GraphWriterHosts[correctionID].RuntimeReceipt, ResultHash: finalHash,
		Corrections: []SettledGraphWriterCorrection{{CorrectionHash: correctionHash, Dynamic: dynamic, Claim: claim, ClaimID: claimID, Evidence: evidence, Invocation: turnInvocation, ResultHash: finalHash, ReceiptHead: finalHead}},
	}
	return s, member
}

func mustClaimID(t *testing.T, claim taskscheduler.Claim) string {
	t.Helper()
	id, err := claim.ID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSettledWriterCorrectionMemberRequiresExactTerminalLineage(t *testing.T) {
	s, member := settledCorrectionProjection(t)
	if !validateSettledWriterCorrectionMember(s, member.Corrections[0].Claim.ScheduleID, s.RoleCorrections[0].PriorSchedulePath, member) {
		t.Fatal("exact completed correction lineage was not accepted")
	}
	mutations := map[string]func(*Snapshot, *SettledGraphWriterMember){
		"unbound correction": func(_ *Snapshot, m *SettledGraphWriterMember) {
			m.Corrections[0].CorrectionHash = strings.Repeat("4", 64)
		},
		"substituted turn": func(_ *Snapshot, m *SettledGraphWriterMember) {
			m.Corrections[0].Dynamic.TurnID = strings.Repeat("5", 64)
		},
		"unresolved task": func(_ *Snapshot, m *SettledGraphWriterMember) {
			m.Corrections[0].Evidence.Status = taskscheduler.StatusUnknown
			m.ResultEvidence = &m.Corrections[0].Evidence
		},
		"wrong predecessor": func(s *Snapshot, _ *SettledGraphWriterMember) { s.RoleCorrections[0].PriorTaskID = "foreign-task" },
		"result identity":   func(_ *Snapshot, m *SettledGraphWriterMember) { m.ResultInvocation.ID = strings.Repeat("6", 64) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			copySnapshot, copyMember := s, member
			copySnapshot.RoleCorrections = append([]RoleSemanticCorrection(nil), s.RoleCorrections...)
			copyMember.Corrections = append([]SettledGraphWriterCorrection(nil), member.Corrections...)
			mutate(&copySnapshot, &copyMember)
			if validateSettledWriterCorrectionMember(copySnapshot, member.Corrections[0].Claim.ScheduleID, copySnapshot.RoleCorrections[0].PriorSchedulePath, copyMember) {
				t.Fatal("substituted or unresolved correction lineage was accepted")
			}
		})
	}
}

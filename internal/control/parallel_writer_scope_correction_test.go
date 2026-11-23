package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/engineeringplan"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

func TestParallelSemanticCorrectionThenScopeReplanUsesSettledCorrection(t *testing.T) {
	ctx := context.Background()
	c := parallelWriterCreation(t, 2)
	c.Objective = "parallel semantic correction scope fixture"
	c.Execution.SemanticCorrectionVersion = 1
	c.Execution.ScopeReplanVersion = 2
	c.Execution.ScopeReplanDesignVersion = 2
	c.Execution.MaxScopeReplans = 1
	if err := c.Execution.Validate(); err != nil {
		t.Fatal(err)
	}
	graph := parallelWriterGraphFixture()
	graph.Tasks[0].WritePaths = []string{"owned-alpha.txt"}
	path, _ := graphAwaitingApprovalWithGraph(t, c, graph)
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	machineAuthorizePlan(t, path, s)
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.Config.Codex.StateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.Config.Codex.StateRoot, "scheduled-scope-correction-mode"), []byte("enabled"), 0600); err != nil {
		t.Fatal(err)
	}

	ready, err := RunAutonomous(ctx, path)
	if err != nil {
		for cause := errors.Unwrap(err); cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("scope correction failure cause: %v", cause)
		}
		latest, inspectErr := Inspect(path)
		if inspectErr == nil {
			if len(latest.ScopeReplanRequests) > 0 && latest.Graph != nil {
				request := latest.ScopeReplanRequests[len(latest.ScopeReplanRequests)-1]
				readyTasks, readyErr := graphReadyTasks(latest)
				var implementations []engineeringplan.Task
				for _, task := range readyTasks {
					if task.Kind == engineeringplan.Implementation {
						implementations = append(implementations, task)
					}
				}
				specs, specsErr := buildGraphWriterBatchSpecs(path, latest, implementations)
				var computedID string
				var computedCohortID string
				if specsErr == nil {
					cohortID, cohortErr := graphCohortID(latest.Graph.Digest, latest.Graph.Revision, specs)
					if cohortErr == nil {
						computedCohortID = cohortID
						definition := taskscheduler.Definition{Version: 1, Nonce: "graph-writers-" + cohortID, Tasks: specs}
						computedID, _ = definition.ID()
					}
				}
				invocationBindings := make([]string, 0, len(specs))
				for _, spec := range specs {
					invocationBindings = append(invocationBindings, spec.ID+":"+spec.InvocationID)
				}
				memberBindings := make([]string, 0, len(request.Cohort.Members))
				for _, member := range request.Cohort.Members {
					memberBindings = append(memberBindings, member.TaskID+":"+member.Invocation.ID)
				}
				t.Logf("scope request schedule recorded=%s recomputed=%s graph=%s/%d cohort=%s/%d cohortID=%s recomputedCohortID=%s invocationBindings=%v memberBindings=%v readyErr=%v specsErr=%v", request.Cohort.ScheduleID, computedID, latest.Graph.Digest, latest.Graph.Revision, request.Cohort.GraphDigest, request.Cohort.Revision, request.Cohort.ScheduleID, computedCohortID, invocationBindings, memberBindings, readyErr, specsErr)
				t.Logf("scope request task IDs members=%v current=%v", func() []string {
					ids := make([]string, 0, len(request.Cohort.Members))
					for _, member := range request.Cohort.Members {
						ids = append(ids, member.TaskID)
					}
					return ids
				}(), func() []string {
					ids := make([]string, 0, len(implementations))
					for _, task := range implementations {
						ids = append(ids, task.ID)
					}
					return ids
				}())
			}
			patterns := []string{path + ".graph-writers-*.jsonl", path + ".isolated-graph-writers-*.jsonl"}
			for _, pattern := range patterns {
				matches, _ := filepath.Glob(pattern)
				sort.Strings(matches)
				for _, schedulePath := range matches {
					schedule, scheduleErr := taskscheduler.Inspect(schedulePath)
					if scheduleErr != nil {
						t.Logf("schedule inspect: %v", scheduleErr)
						continue
					}
					for taskID, state := range schedule.Tasks {
						t.Logf("schedule task %s status=%s claim=%v evidence=%v", taskID, state.Status, state.Claim != nil, state.Evidence != nil)
						if state.Claim != nil {
							probe, probeErr := (ScheduledDispatchAdapter{}).Probe(ctx, taskscheduler.ProbeRequest{Task: state.Claim.Task, AgentTurn: state.Claim.AgentTurn})
							t.Logf("task %s probe status=%s admission=%t err=%v", taskID, probe.Status, probe.AdmissionID != "", probeErr)
							ss, _, inv, invErr := scheduledInvocation(state.Claim.Task, state.Claim.AgentTurn)
							if invErr == nil {
								if taskID == "impl-alpha" && state.Evidence != nil && state.Claim != nil {
									definitionHash, hashErr := schedule.Definition.ID()
									binding := scheduledRoleCorrectionBinding{SchedulePath: schedulePath, ScheduleID: schedule.ScheduleID, ScheduleDefinitionHash: definitionHash, PriorTaskID: taskID, Claim: *state.Claim, Evidence: *state.Evidence}
									claimDigest, claimIDErr := state.Claim.ID()
									t.Logf("pure correction binding validation err=%v deferr=%v claimID=%s claimIDerr=%v evidenceHeadMatchesClaim=%t taskRunMatches=%t taskInvocationMatches=%t claimScheduleMatches=%t pathValid=%v", validateScheduledRoleCorrectionBinding(ss, taskID, inv, binding), hashErr, claimDigest, claimIDErr, state.Evidence.ControllerHead == state.Claim.ControllerHead, state.Claim.Task.RunID == ss.RunID, state.Claim.Task.InvocationID == inv.ID, state.Claim.ScheduleID == schedule.ScheduleID, validateControllerJournalPath(state.Claim.Task.ControllerPath, ss.Creation, ss.RunID))
									if result, resultErr := readCompletedScheduledRoleResult(ss, inv, state.Claim.Task, state.Claim.AgentTurn); resultErr == nil {
										var proof *AnchoredSemanticRejection
										if writercontract.IsAnchoredEdits(ss.Creation.Config.WriterContract) {
											proof, resultErr = captureAnchoredSemanticRejection(ctx, path, ss, taskID, result)
										}
										if resultErr == nil {
											_, resultErr = expectedRoleSemanticCorrectionForTask(ss, taskID, inv, result, &binding, proof)
										}
										t.Logf("derive correction err=%v", resultErr)
									}
								}
								runtimePath, pathErr := scheduledRuntimeJournal(ss, inv, state.Claim.Task, state.Claim.AgentTurn)
								if pathErr == nil {
									runtimeState, runtimeHead, runtimeErr := codexruntime.InspectWithHead(runtimePath)
									t.Logf("task %s runtime intent=%t result=%t turnStatus=%q headPresent=%t err=%v", taskID, runtimeState.Intent != nil, runtimeState.Result != nil, runtimeState.TurnStatus, runtimeHead != "", runtimeErr)
									if runtimeErr == nil {
										_, found, completedErr := maybeReadCompletedScheduledRoleResult(ss, inv, state.Claim.Task, state.Claim.AgentTurn)
										t.Logf("task %s completed result found=%t err=%v hostKeys=%d modelAccess=%d", taskID, found, completedErr, len(ss.GraphWriterHosts), len(ss.ModelAccess))
									}
								} else {
									t.Logf("task %s runtime path error: %v", taskID, pathErr)
								}
							}
						}
					}
				}
			}
		}
		t.Fatalf("run stopped before READY: %v (inspect=%v state=%s corrections=%d scopeRequests=%d graphBatch=%v)", err, inspectErr, latest.State, len(latest.RoleCorrections), len(latest.ScopeReplanRequests), latest.GraphWriterBatch != nil)
	}
	if ready.State != "READY" || ready.GraphWriterBatch == nil || ready.GraphWriterBatch.Version != 3 {
		t.Fatalf("settled correction cohort did not reach READY through a v3 aggregate: state=%s batch=%+v", ready.State, ready.GraphWriterBatch)
	}
	if len(ready.RoleCorrections) != 1 || ready.RoleCorrections[0].TaskID != "impl-alpha" || ready.RoleCorrections[0].ScheduledTaskID == "" {
		t.Fatalf("malformed static proposal was not corrected under a distinct scheduled task: %+v", ready.RoleCorrections)
	}
	if len(ready.ScopeReplanRequests) != 1 || len(ready.ScopeReplans) != 1 || ready.ScopeReplanRequests[0].Cohort.Members[0].ResultInvocation == nil {
		t.Fatalf("scope replan did not preserve the settled correction chain: requests=%d revisions=%d", len(ready.ScopeReplanRequests), len(ready.ScopeReplans))
	}
	var alpha, beta bool
	for _, change := range ready.GraphWriterBatch.Prepared.Proposal.Changes {
		alpha = alpha || change.Path == "alpha.txt"
		beta = beta || change.Path == "beta.txt"
	}
	if !alpha || !beta {
		t.Fatalf("fresh parent aggregate omitted corrected or successful sibling output: alpha=%v beta=%v", alpha, beta)
	}
	if _, err := os.Stat(filepath.Join(ready.Workspace.Request.Path, "alpha.txt")); err != nil {
		t.Fatalf("corrected alpha output was not applied by the parent aggregate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ready.Workspace.Request.Path, "beta.txt")); err != nil {
		t.Fatalf("successful beta sibling output was not preserved: %v", err)
	}
	if _, ok := ready.Graph.Graph.Task("impl-alpha"); ok {
		t.Fatal("old task ID remained after explicit scope refinement")
	}
	for _, task := range ready.Graph.Graph.Tasks {
		if task.Kind == engineeringplan.Implementation && task.Completed && task.ID != "impl-beta" {
			if task.WritePaths[0] == "owned-alpha.txt" {
				t.Fatal("scope replan did not admit the exact corrected path")
			}
		}
	}
}

package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/agenttree"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

type scheduledRoleCorrectionBinding struct {
	SchedulePath           string
	ScheduleID             string
	ScheduleDefinitionHash string
	PriorTaskID            string
	Claim                  taskscheduler.Claim
	Evidence               taskscheduler.Evidence
}

type scheduledRoleCorrectionContextKey struct{}

func withScheduledRoleCorrection(ctx context.Context, correction RoleSemanticCorrection) context.Context {
	return context.WithValue(ctx, scheduledRoleCorrectionContextKey{}, correction)
}

func scheduledRoleCorrectionFromContext(ctx context.Context) (RoleSemanticCorrection, bool) {
	if ctx == nil {
		return RoleSemanticCorrection{}, false
	}
	correction, ok := ctx.Value(scheduledRoleCorrectionContextKey{}).(RoleSemanticCorrection)
	return correction, ok && correction.ScheduledTaskID != ""
}

func scheduledCorrectionTaskID(runID, taskID string, prior scheduledRoleCorrectionBinding, invocation runtime.Invocation, attempt int) string {
	claimID, _ := prior.Claim.ID()
	id, _ := canonical.Hash("harness.scheduled-role-correction-turn.v1", struct {
		RunID        string `json:"run_id"`
		TaskID       string `json:"task_id"`
		PriorTaskID  string `json:"prior_task_id"`
		ScheduleID   string `json:"schedule_id"`
		PriorClaimID string `json:"prior_claim_id"`
		InvocationID string `json:"invocation_id"`
		Attempt      int    `json:"attempt"`
	}{runID, taskID, prior.PriorTaskID, prior.ScheduleID, claimID, invocation.ID, attempt})
	return id
}

func validateScheduledRoleCorrectionBinding(s Snapshot, taskID string, original runtime.Invocation, binding scheduledRoleCorrectionBinding) error {
	if binding.SchedulePath == "" || !filepath.IsAbs(binding.SchedulePath) || filepath.Clean(binding.SchedulePath) != binding.SchedulePath || safepath.RequireDigest(binding.ScheduleID) != nil || safepath.RequireDigest(binding.ScheduleDefinitionHash) != nil {
		return ErrAutonomousUnsafe
	}
	claimID, err := binding.Claim.ID()
	if err != nil || safepath.RequireDigest(claimID) != nil || binding.PriorTaskID == "" || binding.Claim.Task.ID != binding.PriorTaskID || binding.Claim.Task.RunID != s.RunID || binding.Claim.Task.ControllerPath == "" || binding.Claim.Task.Operation != taskscheduler.OperationWriter && binding.Claim.Task.Operation != taskscheduler.OperationReviewer || binding.Claim.Task.InvocationID != original.ID || binding.Claim.ScheduleID != binding.ScheduleID {
		return errors.Join(ErrAutonomousUnsafe, err)
	}
	if binding.Evidence.Status != taskscheduler.StatusFailed || binding.Evidence.RunID != s.RunID || binding.Evidence.InvocationID != original.ID || binding.Evidence.AdmissionID == "" || safepath.RequireDigest(binding.Evidence.ControllerHead) != nil {
		return ErrAutonomousUnsafe
	}
	if err := validateControllerJournalPath(binding.Claim.Task.ControllerPath, s.Creation, s.RunID); err != nil {
		return errors.Join(ErrAutonomousUnsafe, err)
	}
	observedInvocation, invocationErr := scheduledInvocationFromSnapshot(s, binding.Claim.Task, binding.Claim.AgentTurn)
	if invocationErr != nil || observedInvocation != original {
		return errors.Join(ErrAutonomousUnsafe, invocationErr)
	}
	if turn := binding.Claim.AgentTurn; turn != nil {
		if safepath.RequireDigest(turn.ParentAgentID) != nil || safepath.RequireDigest(turn.AgentID) != nil || safepath.RequireDigest(turn.TurnID) != nil || turn.TurnID != binding.Claim.Task.ID || turn.TurnSequence < 1 {
			return ErrAutonomousUnsafe
		}
		if admission, ok := s.AgentDispatch[original.ID]; ok {
			if admission.Admission.AgentTurn == nil || *admission.Admission.AgentTurn != *turn || admission.Admission.Invocation != original {
				return ErrAutonomousUnsafe
			}
		} else if correction, ok := scheduledCorrectionForTask(s, binding.Claim.Task.ID); !ok || correction.ScheduledTaskID != binding.Claim.Task.ID {
			// Semantic-correction turns are durably linked by the preceding
			// correction event. Other dynamic turns need their controller-side
			// AgentDispatch admission as an immutable replay binding.
			return ErrAutonomousUnsafe
		}
	}
	return nil
}

// validateLiveScheduledRoleCorrectionBinding checks the mutable scheduler
// journal at the live admission boundary. Controller Replay uses the pure
// validator above and never reads scheduler or agent-tree files.
func validateLiveScheduledRoleCorrectionBinding(s Snapshot, original runtime.Invocation, binding scheduledRoleCorrectionBinding) error {
	if err := validateScheduledRoleCorrectionBinding(s, binding.PriorTaskID, original, binding); err != nil {
		return err
	}
	schedule, err := taskscheduler.Inspect(binding.SchedulePath)
	if err != nil || schedule.Definition == nil || schedule.ScheduleID != binding.ScheduleID {
		return errors.Join(ErrAutonomousUnsafe, err)
	}
	definitionHash, err := schedule.Definition.ID()
	if err != nil || definitionHash != binding.ScheduleDefinitionHash {
		return errors.Join(ErrAutonomousUnsafe, err)
	}
	state, ok := schedule.Tasks[binding.PriorTaskID]
	if !ok || state.Status != taskscheduler.StatusFailed || state.Claim == nil || state.Evidence == nil || !sameCanonical(*state.Claim, binding.Claim) || !sameCanonical(*state.Evidence, binding.Evidence) {
		return ErrAutonomousUnsafe
	}
	return nil
}

func roleReceiptTaskID(s Snapshot, original runtime.Invocation, taskID string) string {
	for _, correction := range s.RoleCorrections {
		if correction.TaskID != taskID || correction.ScheduledTaskID == "" {
			continue
		}
		operation, err := scheduledOperationForRole(correction.Invocation.Profile.Role)
		if err != nil {
			continue
		}
		invocation, err := scheduledTurnInvocation(correction.Invocation, operation, correction.ScheduledTaskID)
		if err == nil && invocation == original {
			return correction.ScheduledTaskID
		}
	}
	return taskID
}

func scheduledCorrectionForTask(s Snapshot, taskID string) (RoleSemanticCorrection, bool) {
	if taskID == "" {
		return RoleSemanticCorrection{}, false
	}
	for _, correction := range s.RoleCorrections {
		if correction.ScheduledTaskID == taskID {
			return correction, true
		}
	}
	return RoleSemanticCorrection{}, false
}

func ensureScheduledRoleCorrectionTask(controllerPath, schedulePath string, correction RoleSemanticCorrection) (taskscheduler.DynamicTask, error) {
	if correction.ScheduledTaskID == "" || correction.Invocation.ID == "" || correction.TaskID == "" && correction.Invocation.Profile.Role != "reviewer" {
		return taskscheduler.DynamicTask{}, ErrAutonomousUnsafe
	}
	operation, err := scheduledOperationForRole(correction.Invocation.Profile.Role)
	if err != nil || operation != taskscheduler.OperationWriter && operation != taskscheduler.OperationReviewer {
		return taskscheduler.DynamicTask{}, errors.Join(ErrAutonomousUnsafe, err)
	}
	turnInvocation, err := scheduledTurnInvocation(correction.Invocation, operation, correction.ScheduledTaskID)
	if err != nil {
		return taskscheduler.DynamicTask{}, err
	}
	contextHash, err := access.InputID(turnInvocation.Input)
	if err != nil {
		return taskscheduler.DynamicTask{}, err
	}
	snapshot, err := Inspect(controllerPath)
	if err != nil {
		return taskscheduler.DynamicTask{}, err
	}
	treePath := controllerPath + ".agent-tree"
	tree, err := agenttree.Inspect(treePath)
	if err != nil {
		return taskscheduler.DynamicTask{}, err
	}
	if tree.TreeID == "" {
		tree, err = bootstrapAgentTreeRoot(treePath, snapshot)
	}
	if err != nil || tree.TreeID != snapshot.RunID {
		return taskscheduler.DynamicTask{}, errors.Join(ErrAutonomousUnsafe, err)
	}
	var parent *agenttree.Node
	for i := range tree.Nodes {
		if tree.Nodes[i].ParentAgentID == "" {
			copy := tree.Nodes[i]
			parent = &copy
			break
		}
	}
	if parent == nil {
		return taskscheduler.DynamicTask{}, ErrAutonomousUnsafe
	}
	role := turnInvocation.Profile.Role
	authority, err := agentAuthority(role)
	if err != nil {
		return taskscheduler.DynamicTask{}, err
	}
	name := "semantic-" + role + "-" + correction.ScheduledTaskID[:8] + "-" + string(rune('0'+correction.Attempt))
	spec := agenttree.NodeSpec{ParentAgentID: parent.AgentID, Name: name, Role: role, Authority: authority, InvocationID: turnInvocation.ID, ContextSHA256: contextHash}
	wantedPath, err := agenttree.ChildPath(parent.Path, name)
	if err != nil {
		return taskscheduler.DynamicTask{}, err
	}
	var node agenttree.Node
	found := false
	for _, existing := range tree.Nodes {
		if existing.Path == wantedPath {
			if !exactNodeFields(existing, parent.AgentID, wantedPath, spec) {
				return taskscheduler.DynamicTask{}, ErrAutonomousUnsafe
			}
			node, found = existing, true
			break
		}
	}
	if !found {
		reservation, reserveErr := agenttree.ReserveChild(treePath, snapshot.RunID, spec)
		if reserveErr != nil {
			return taskscheduler.DynamicTask{}, reserveErr
		}
		node, err = agenttree.CommitChild(treePath, reservation.ReservationID)
		if err != nil {
			return taskscheduler.DynamicTask{}, err
		}
	}
	task := taskscheduler.TaskSpec{ID: correction.ScheduledTaskID, RunID: snapshot.RunID, ControllerPath: controllerPath, Operation: operation, InvocationID: turnInvocation.ID}
	dynamic := taskscheduler.DynamicTask{Task: task, ParentAgentID: parent.AgentID, AgentID: node.AgentID, TurnID: task.ID}
	_, err = taskscheduler.AddTask(schedulePath, dynamic)
	return dynamic, err
}

// AfterSettledScheduledClaim admits one new scheduled role-correction turn only
// after the prior schedule journal proves a terminal, failed claim. Repeated
// calls are idempotent across the controller-event and task.added boundary.
func AfterSettledScheduledClaim(ctx context.Context, controllerPath, schedulePath, taskID string) (bool, error) {
	if ctx == nil || ctx.Err() != nil || !filepath.IsAbs(controllerPath) || filepath.Clean(controllerPath) != controllerPath || !filepath.IsAbs(schedulePath) || filepath.Clean(schedulePath) != schedulePath || taskID == "" {
		return false, errors.New("invalid settled scheduled role correction request")
	}
	schedule, err := taskscheduler.Inspect(schedulePath)
	if err != nil || schedule.Definition == nil {
		return false, errors.Join(errors.New("scheduled role correction requires a bound schedule"), err)
	}
	state, ok := schedule.Tasks[taskID]
	if !ok || state.Status != taskscheduler.StatusFailed || state.Claim == nil || state.Evidence == nil {
		return false, nil
	}
	claim := *state.Claim
	evidence := *state.Evidence
	if claim.Task.ControllerPath != controllerPath || claim.Task.ID != taskID || evidence.Status != taskscheduler.StatusFailed {
		return false, ErrAutonomousUnsafe
	}
	if !settledStaticRoleCohort(schedule, controllerPath, claim.Task.Operation) {
		return false, nil
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		return false, err
	}
	if s.Creation.Execution == nil || s.Creation.Execution.SemanticCorrectionVersion != 1 || s.Candidate == nil || claim.Task.Operation != taskscheduler.OperationWriter && claim.Task.Operation != taskscheduler.OperationReviewer {
		return false, nil
	}
	if existing, found := scheduledCorrectionForFailedClaim(s, schedule.ScheduleID, claim); found {
		if err := releaseGraphMemoryAdmissionForCorrection(controllerPath, existing); err != nil {
			return false, err
		}
		_, err := ensureScheduledRoleCorrectionTask(controllerPath, schedulePath, existing)
		return err == nil, err
	}
	if safepath.RequireDigest(evidence.ControllerHead) != nil {
		return false, errors.Join(ErrAutonomousUnsafe, err)
	}
	// A later settled event may have advanced the controller journal. The claim
	// head is retained for audit, while candidate identity is independently
	// checked under a read lease immediately before event append.
	_, _, invocation, err := scheduledInvocation(claim.Task, claim.AgentTurn)
	if err != nil || invocation.ID != claim.Task.InvocationID {
		return false, errors.Join(ErrAutonomousUnsafe, err)
	}
	result, err := readCompletedScheduledRoleResult(s, invocation, claim.Task, claim.AgentTurn)
	if err != nil {
		return false, err
	}
	proofTaskID := ""
	if priorCorrection, ok := scheduledCorrectionForTask(s, taskID); ok && claim.Task.Operation == taskscheduler.OperationWriter {
		proofTaskID = priorCorrection.TaskID
	} else if claim.Task.Operation == taskscheduler.OperationWriter && s.Graph != nil {
		if graphTask, ok := s.Graph.Graph.Task(taskID); ok && graphTask.Kind == "implementation" {
			proofTaskID = taskID
		}
	}
	semanticRejection := isScheduledRoleSemanticRejection(s, invocation, result)
	var anchoredProof *AnchoredSemanticRejection
	if claim.Task.Operation == taskscheduler.OperationWriter && writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
		anchoredProof, err = captureAnchoredSemanticRejection(ctx, controllerPath, s, proofTaskID, result)
		if err != nil {
			return false, err
		}
		if anchoredProof != nil {
			proposal, decodeErr := decodeAnchoredProposal(result.Output)
			if decodeErr != nil {
				return false, ErrAutonomousUnsafe
			}
			rejection, proofErr := validateAnchoredSemanticRejection(s, proofTaskID, proposal, anchoredProof)
			if proofErr != nil {
				return false, proofErr
			}
			semanticRejection = semanticRejection || rejection != nil
		}
	}
	if !semanticRejection {
		return false, nil
	}
	if s.Workspace == nil {
		return false, ErrAutonomousUnsafe
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return false, err
	}
	defer lease.Close()
	latest, err := Inspect(controllerPath)
	if err != nil || latest.Candidate == nil || s.Candidate == nil || !sameCanonical(*latest.Candidate, *s.Candidate) || latest.Workspace == nil || !sameCanonical(*latest.Workspace, *s.Workspace) {
		return false, errors.Join(ErrAutonomousUnsafe, err)
	}
	observed, err := observeCandidate(ctx, controllerPath, latest)
	if err != nil || observed != *latest.Candidate {
		return false, errors.Join(ErrAutonomousUnsafe, err)
	}
	graphTaskID := ""
	if priorCorrection, ok := scheduledCorrectionForTask(latest, taskID); ok && claim.Task.Operation == taskscheduler.OperationWriter {
		graphTaskID = priorCorrection.TaskID
	} else if claim.Task.Operation == taskscheduler.OperationWriter && latest.Graph != nil {
		if task, ok := latest.Graph.Graph.Task(taskID); ok && task.Kind == "implementation" {
			graphTaskID = taskID
		}
	}
	definitionHash, err := schedule.Definition.ID()
	if err != nil {
		return false, err
	}
	binding := scheduledRoleCorrectionBinding{SchedulePath: schedulePath, ScheduleID: schedule.ScheduleID, ScheduleDefinitionHash: definitionHash, PriorTaskID: taskID, Claim: claim, Evidence: evidence}
	if err := validateLiveScheduledRoleCorrectionBinding(latest, invocation, binding); err != nil {
		return false, err
	}
	correction, err := expectedRoleSemanticCorrectionForTask(latest, graphTaskID, invocation, result, &binding, anchoredProof)
	if err != nil {
		return false, err
	}
	if err := Append(controllerPath, "role.semantic-correction", correction); err != nil {
		latest, inspectErr := Inspect(controllerPath)
		if inspectErr != nil {
			return false, errors.Join(err, inspectErr)
		}
		if existing, found := scheduledCorrectionForFailedClaim(latest, schedule.ScheduleID, claim); found {
			_, retryErr := ensureScheduledRoleCorrectionTask(controllerPath, schedulePath, existing)
			return retryErr == nil, errors.Join(err, retryErr)
		}
		return false, err
	}
	if err := releaseGraphMemoryAdmissionForCorrection(controllerPath, correction); err != nil {
		return false, err
	}
	_, err = ensureScheduledRoleCorrectionTask(controllerPath, schedulePath, correction)
	return err == nil, err
}

func settledStaticRoleCohort(schedule taskscheduler.Snapshot, controllerPath string, operation taskscheduler.Operation) bool {
	if schedule.Definition == nil {
		return false
	}
	for _, task := range schedule.Definition.Tasks {
		if task.ControllerPath != controllerPath || task.Operation != operation {
			continue
		}
		state := schedule.Tasks[task.ID]
		if state.Status != taskscheduler.StatusSucceeded && state.Status != taskscheduler.StatusFailed {
			return false
		}
	}
	return true
}

func scheduledCorrectionForFailedClaim(s Snapshot, scheduleID string, claim taskscheduler.Claim) (RoleSemanticCorrection, bool) {
	claimID, err := claim.ID()
	if err != nil {
		return RoleSemanticCorrection{}, false
	}
	for _, correction := range s.RoleCorrections {
		if correction.PriorClaim == nil || correction.PriorScheduleID != scheduleID {
			continue
		}
		id, idErr := correction.PriorClaim.ID()
		if idErr == nil && id == claimID {
			return correction, true
		}
	}
	return RoleSemanticCorrection{}, false
}

func readCompletedScheduledRoleResult(s Snapshot, invocation runtime.Invocation, task taskscheduler.TaskSpec, turn *taskscheduler.AgentTurnBinding) (runtime.Result, error) {
	result, found, err := maybeReadCompletedScheduledRoleResult(s, invocation, task, turn)
	if err != nil {
		return runtime.Result{}, err
	}
	if !found {
		return runtime.Result{}, errors.New("settled role correction lacks a completed runtime result")
	}
	return result, nil
}

func maybeReadCompletedScheduledRoleResult(s Snapshot, invocation runtime.Invocation, task taskscheduler.TaskSpec, turn *taskscheduler.AgentTurnBinding) (runtime.Result, bool, error) {
	if invocation.Profile.Runtime == "opencode-http" {
		receipt, ok := s.ProviderRuntime[invocation.ID]
		if !ok {
			return runtime.Result{}, false, nil
		}
		if receipt.InvocationID != invocation.ID || receipt.Role != invocation.Profile.Role || requireOpenCodeRoleReceipt(s, invocation, receipt.Result) != nil {
			return runtime.Result{}, false, ErrAutonomousUnsafe
		}
		return receipt.Result, true, nil
	}
	if invocation.Profile.Runtime != "codex-app-server" {
		return runtime.Result{}, false, nil
	}
	path, err := scheduledRuntimeJournal(s, invocation, task, turn)
	if err != nil {
		return runtime.Result{}, false, err
	}
	state, head, err := codexruntime.InspectWithHead(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return runtime.Result{}, false, nil
		}
		return runtime.Result{}, false, err
	}
	if state.Intent == nil || state.Intent.Invocation != invocation || state.Result == nil || state.TurnStatus != "completed" {
		return runtime.Result{}, false, nil
	}
	domain, err := runtimeResultDomain(invocation.Profile.Role)
	if err != nil {
		return runtime.Result{}, false, err
	}
	hash, err := canonical.Hash(domain, *state.Result)
	if err != nil {
		return runtime.Result{}, false, err
	}
	var receiptInvocationID, receiptHead, receiptResultHash string
	if invocation.Profile.Role == "reviewer" {
		if host := scheduledReviewHostForInvocation(s, invocation); host != nil {
			if receipt := host.RuntimeReceipt; receipt != nil {
				receiptInvocationID, receiptHead, receiptResultHash = receipt.InvocationID, receipt.JournalHead, receipt.ResultHash
			}
		}
	} else if task.Operation == taskscheduler.OperationWriter && task.ID != "" {
		if host, ok := s.GraphWriterHosts[roleReceiptTaskID(s, invocation, task.ID)]; ok {
			if receipt := host.RuntimeReceipt; receipt != nil {
				receiptInvocationID, receiptHead, receiptResultHash = receipt.InvocationID, receipt.JournalHead, receipt.ResultHash
			}
		}
	} else if s.WriterHost != nil {
		if receipt := s.WriterHost.RuntimeReceipt; receipt != nil {
			receiptInvocationID, receiptHead, receiptResultHash = receipt.InvocationID, receipt.JournalHead, receipt.ResultHash
		}
	}
	if receiptInvocationID != invocation.ID || receiptHead != head || receiptResultHash != hash {
		return runtime.Result{}, false, ErrAutonomousUnsafe
	}
	return *state.Result, true, nil
}

func isScheduledRoleSemanticRejection(s Snapshot, invocation runtime.Invocation, result runtime.Result) bool {
	if runtime.ValidateResult(invocation, result, true) != nil {
		return false
	}
	if invocation.Profile.Role == "reviewer" {
		payload, err := canonical.Bytes(ReviewRecord{invocation, result})
		if err != nil {
			return false
		}
		copy := s
		return semanticOutputFailureOnly(replayReview(&copy, journal.Event{Payload: payload}))
	}
	var rejection error
	if writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
		_, rejection = decodeAnchoredProposal(result.Output)
	} else {
		_, rejection = decodeWriterProposal(s.Creation.Config.WriterContract, result.Output)
	}
	return rejection != nil && semanticOutputFailureOnly(rejectedSemanticOutput(rejection))
}

func settledScheduledSemanticFailureEvidence(ctx context.Context, controllerPath string, s Snapshot, evidence taskscheduler.Evidence, invocation runtime.Invocation, task taskscheduler.TaskSpec, turn *taskscheduler.AgentTurnBinding) (taskscheduler.Evidence, error) {
	if task.Operation != taskscheduler.OperationWriter && task.Operation != taskscheduler.OperationReviewer || evidence.Status == taskscheduler.StatusSucceeded || evidence.Status == taskscheduler.StatusFailed || evidence.Status == taskscheduler.StatusCancelled {
		return evidence, nil
	}
	result, found, err := maybeReadCompletedScheduledRoleResult(s, invocation, task, turn)
	if err != nil || !found {
		return evidence, err
	}
	knownRejection := isScheduledRoleSemanticRejection(s, invocation, result)
	writerPolicy := s.Creation.Execution != nil && s.Creation.Execution.SemanticCorrectionVersion == 1 && writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract)
	if !knownRejection && task.Operation == taskscheduler.OperationWriter && (cohortScopeReplanningEnabled(s) || writerPolicy) && controllerPath != "" && s.Graph != nil {
		graphTaskID := task.ID
		if correction, ok := scheduledCorrectionForTask(s, task.ID); ok && correction.TaskID != "" {
			graphTaskID = correction.TaskID
		}
		if graphTask, ok := s.Graph.Graph.Task(graphTaskID); ok {
			// Re-derive the exact candidate-bound proposal under the existing
			// read-only preparation path. A typed content rejection is settled
			// semantic evidence; a valid proposal becomes a settled scope
			// rejection only when it is outside WritePaths but inside ScopePaths.
			// No proposal is recorded or applied here.
			var prepared PreparedFiles
			var prepareErr error
			if isolatedImplementationEnabled(s) {
				_, prepareErr = prepareIsolatedGraphWriterProposal(ctx, controllerPath, graphTaskID, invocation, result)
			} else {
				prepared, _, prepareErr = prepareWriterFilesForTask(ctx, controllerPath, graphTaskID, invocation, result)
			}
			if semanticOutputFailureOnly(prepareErr) {
				knownRejection = true
			} else if cohortScopeReplanningEnabled(s) && isolatedImplementationEnabled(s) {
				var violation *graphWriterScopeViolationError
				if errors.As(prepareErr, &violation) && graphWriterChangesWithinScope(graphTask, violation.Changes) {
					// The isolated preparation routine reaches this error only after
					// exact child candidate, manifest and source checks have succeeded.
					knownRejection = true
				}
			} else if !isolatedImplementationEnabled(s) && cohortScopeReplanningEnabled(s) && prepareErr == nil && !graphWriterChangesWithinTask(graphTask, prepared.Proposal.Changes) && graphWriterChangesWithinScope(graphTask, prepared.Proposal.Changes) {
				knownRejection = true
			}
		}
	}
	if !knownRejection {
		return evidence, nil
	}
	if evidence.AdmissionID == "" {
		evidence.AdmissionID = scheduledAdmissionID(s, invocation.ID)
	}
	if evidence.AdmissionID == "" && task.Operation == taskscheduler.OperationWriter && s.Creation.Config.Version == 1 && graphWriterCohortEnabled(s) {
		evidence.AdmissionID = invocation.ID
	}
	if safepath.RequireDigest(evidence.AdmissionID) != nil {
		return taskscheduler.Evidence{}, ErrAutonomousUnsafe
	}
	evidence.Status = taskscheduler.StatusFailed
	return evidence, nil
}

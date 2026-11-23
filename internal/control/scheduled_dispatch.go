package control

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/iancuuandrei/fabric/internal/access"
	"github.com/iancuuandrei/fabric/internal/agenttree"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/providerruntime"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskpool"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// ScheduledDispatchAdapter exposes only the controller's finite role operations
// to the durable scheduler. It grants no additional model or effect authority.
type ScheduledDispatchAdapter struct {
	// JournalPath selects the exact schedule journal whose current claim may be
	// exposed to controller-bound runtime projections. Empty preserves legacy
	// context-only dispatch.
	JournalPath string
}

type scheduledDispatchJournalContextKey struct{}

type scheduledDispatchJournalBinding struct {
	path       string
	scheduleID string
	claimID    string
}

func scheduledDispatchJournalPath(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	binding, ok := ctx.Value(scheduledDispatchJournalContextKey{}).(scheduledDispatchJournalBinding)
	return binding.path, ok && binding.path != "" && binding.scheduleID != "" && binding.claimID != ""
}

func (a ScheduledDispatchAdapter) bindJournalContext(ctx context.Context, claim taskscheduler.Claim) (context.Context, error) {
	if a.JournalPath == "" {
		return ctx, nil
	}
	if ctx == nil || !filepath.IsAbs(a.JournalPath) || filepath.Clean(a.JournalPath) != a.JournalPath {
		return nil, errors.New("invalid scheduled dispatch journal path")
	}
	claimID, err := claim.ID()
	if err != nil {
		return nil, err
	}
	snapshot, err := taskscheduler.Inspect(a.JournalPath)
	if err != nil || snapshot.Definition == nil || snapshot.ScheduleID != claim.ScheduleID {
		return nil, errors.Join(errors.New("scheduled dispatch journal identity differs"), err)
	}
	state, ok := snapshot.Tasks[claim.Task.ID]
	if !ok || state.Claim == nil {
		return nil, errors.New("scheduled dispatch claim absent from journal")
	}
	storedID, err := state.Claim.ID()
	if err != nil || storedID != claimID || !reflect.DeepEqual(*state.Claim, claim) {
		return nil, errors.Join(errors.New("scheduled dispatch claim differs from journal"), err)
	}
	binding := scheduledDispatchJournalBinding{path: a.JournalPath, scheduleID: claim.ScheduleID, claimID: claimID}
	return context.WithValue(ctx, scheduledDispatchJournalContextKey{}, binding), nil
}

// Probe reads exact controller evidence for the task without launching its runtime.
func (ScheduledDispatchAdapter) Probe(ctx context.Context, request taskscheduler.ProbeRequest) (taskscheduler.Evidence, error) {
	if ctx == nil {
		return taskscheduler.Evidence{}, errors.New("scheduled dispatch context required")
	}
	task := request.Task
	s, head, invocation, err := scheduledInvocation(task, request.AgentTurn)
	if err != nil {
		return taskscheduler.Evidence{}, err
	}
	if task.RunID != s.RunID || task.InvocationID != invocation.ID {
		return taskscheduler.Evidence{}, errors.New("scheduled invocation identity changed")
	}
	if err := validateScheduledAgentTurn(task, s, invocation, request.AgentTurn); err != nil {
		return taskscheduler.Evidence{}, err
	}
	return scheduledEvidenceWithAcceptedResult(ctx, task.ControllerPath, s, head, invocation, task, request.AgentTurn)
}

// Dispatch executes or reconciles the claim through its bound controller operation.
func (a ScheduledDispatchAdapter) Dispatch(ctx context.Context, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	ctx, err := a.bindJournalContext(ctx, claim)
	if err != nil {
		return taskscheduler.Evidence{}, err
	}
	return ExecuteScheduledClaim(ctx, claim.Task.ControllerPath, claim)
}

// Reconcile consumes only an already journaled runtime attempt for an UNKNOWN
// claim. It refuses to create a missing runtime journal or a fresh provider turn.
func (a ScheduledDispatchAdapter) Reconcile(ctx context.Context, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	ctx, err := a.bindJournalContext(ctx, claim)
	if err != nil {
		return taskscheduler.Evidence{}, err
	}
	return ReconcileScheduledClaim(ctx, claim.Task.ControllerPath, claim)
}

// PrepareScheduledTask binds one scheduler node to the invocation currently
// derivable from the controller. Dependencies and the caller-selected task ID
// are added by the schedule definition builder.
func PrepareScheduledTask(controllerPath string, operation taskscheduler.Operation, input string) (taskscheduler.TaskSpec, error) {
	task := taskscheduler.TaskSpec{ControllerPath: controllerPath, Operation: operation, Input: input}
	s, _, invocation, err := scheduledInvocation(task, nil)
	if err != nil {
		return taskscheduler.TaskSpec{}, err
	}
	task.RunID = s.RunID
	task.InvocationID = invocation.ID
	return task, nil
}

// PrepareDynamicScheduledTask binds a scheduler-created turn identity into the
// provider invocation, so identical follow-up bodies remain distinct turns.
func PrepareDynamicScheduledTask(controllerPath string, operation taskscheduler.Operation, input, turnID string) (taskscheduler.TaskSpec, error) {
	if safepath.RequireDigest(turnID) != nil {
		return taskscheduler.TaskSpec{}, errors.New("invalid scheduled turn identity")
	}
	task, err := PrepareScheduledTask(controllerPath, operation, input)
	if err != nil {
		return taskscheduler.TaskSpec{}, err
	}
	task.ID = turnID
	_, _, base, err := scheduledInvocation(task, nil)
	if err != nil {
		return taskscheduler.TaskSpec{}, err
	}
	scoped, err := scheduledTurnInvocation(base, operation, turnID)
	if err != nil {
		return taskscheduler.TaskSpec{}, err
	}
	task.InvocationID = scoped.ID
	return task, nil
}

// ExecuteScheduledClaim validates the exact claim and delegates to one existing
// controller role entry point. The duplicated path must equal the path bound in
// the immutable task so callers cannot redirect work during dispatch.
func ExecuteScheduledClaim(ctx context.Context, controllerPath string, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	if ctx == nil || controllerPath != claim.Task.ControllerPath {
		return taskscheduler.Evidence{}, errors.New("scheduled controller path changed")
	}
	if _, err := claim.ID(); err != nil {
		return taskscheduler.Evidence{}, err
	}
	current, currentHead, currentErr := InspectWithHead(controllerPath)
	if currentErr != nil {
		return taskscheduler.Evidence{}, currentErr
	}
	if claim.ControllerHead != currentHead && scheduledAdmissionID(current, claim.Task.InvocationID) == "" {
		// Narrow exception: only static graph-bound explorers in the same
		// frozen batch may tolerate sibling read-only appends. All other
		// operations retain the legacy exclusive head gate.
		if !(isStaticGraphExplorerCohort(current, claim) && verifyGraphCohortDelta(controllerPath, claim) == nil) &&
			!(isStaticGraphWriterCohort(current, claim) && verifyGraphWriterCohortDelta(controllerPath, claim) == nil) {
			return taskscheduler.Evidence{}, errors.Join(&taskscheduler.ParkError{Reason: taskscheduler.ParkNoEffect}, errors.New("scheduled controller head changed before admission"))
		}
	}
	s, head, invocation, err := scheduledInvocation(claim.Task, claim.AgentTurn)
	if err != nil {
		return taskscheduler.Evidence{}, err
	}
	if claim.Task.RunID != s.RunID || claim.Task.InvocationID != invocation.ID {
		return taskscheduler.Evidence{}, errors.New("scheduled invocation identity changed")
	}
	if err := validateScheduledAgentTurn(claim.Task, s, invocation, claim.AgentTurn); err != nil {
		return taskscheduler.Evidence{}, err
	}
	before, err := scheduledEvidenceWithAcceptedResult(ctx, controllerPath, s, head, invocation, claim.Task, claim.AgentTurn)
	if err != nil {
		return taskscheduler.Evidence{}, err
	}
	if claim.Task.Operation == taskscheduler.OperationExplorer && claim.AgentTurn != nil && before.Status == taskscheduler.StatusUnknown {
		indexed, indexErr := ensureAcceptedExplorerResult(ctx, controllerPath, claim, invocation, nil)
		if indexErr != nil {
			return taskscheduler.Evidence{}, indexErr
		}
		if indexed {
			return (ScheduledDispatchAdapter{}).Probe(ctx, taskscheduler.ProbeRequest{Task: claim.Task, AgentTurn: claim.AgentTurn})
		}
	}
	if claim.ControllerHead != head && before.AdmissionID == "" {
		if !(isStaticGraphExplorerCohort(s, claim) && verifyGraphCohortDelta(controllerPath, claim) == nil) &&
			!(isStaticGraphWriterCohort(s, claim) && verifyGraphWriterCohortDelta(controllerPath, claim) == nil) {
			return taskscheduler.Evidence{}, errors.New("scheduled controller head changed")
		}
	}
	if before.Status == taskscheduler.StatusSucceeded || before.Status == taskscheduler.StatusFailed || before.Status == taskscheduler.StatusCancelled {
		return before, nil
	}
	status := lifecycleStatus(s)
	if status == LifecyclePauseRequested || status == LifecyclePaused || status == LifecycleCancelRequested || status == LifecycleCancelled {
		return taskscheduler.Evidence{}, &taskscheduler.ParkError{Reason: taskscheduler.ParkLifecycle}
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return taskscheduler.Evidence{}, errors.Join(&taskscheduler.ParkError{Reason: taskscheduler.ParkNoEffect}, err)
	}
	if claim.Task.Operation != taskscheduler.OperationPlanner {
		if err := requireExecutableRoleRuntime(invocation.Profile); err != nil {
			return taskscheduler.Evidence{}, errors.Join(&taskscheduler.ParkError{Reason: taskscheduler.ParkNoEffect}, err)
		}
	}
	correction, isCorrection := scheduledCorrectionForTask(s, claim.Task.ID)
	managedExplorer := claim.AgentTurn != nil && claim.Task.Operation == taskscheduler.OperationExplorer && managedExplorerDispatchEnabled(s) && (invocation.Profile.Runtime == "codex-app-server" || invocation.Profile.Runtime == "fake")
	if claim.AgentTurn != nil && invocation.Profile.Runtime != "opencode-http" && invocation.Profile.Runtime != "provider-api" && !managedExplorer && !(isCorrection && invocation.Profile.Runtime == "codex-app-server" && (claim.Task.Operation == taskscheduler.OperationWriter || claim.Task.Operation == taskscheduler.OperationReviewer)) {
		return taskscheduler.Evidence{}, errors.Join(&taskscheduler.ParkError{Reason: taskscheduler.ParkNoEffect}, errors.New("scheduled dynamic agent turns require a provider runtime"))
	}
	observationCtx := ctx
	ctx = withScheduledAgentTurn(ctx, claim.AgentTurn)
	if managedExplorer {
		ctx = context.WithValue(ctx, workingContextInvocationKey{}, invocation.ID)
	}
	if isCorrection {
		ctx = withScheduledRoleCorrection(ctx, correction)
	}
	var interruptWatcher *scheduledInterruptWatcher
	if claim.AgentTurn != nil && claim.Task.Operation == taskscheduler.OperationExplorer && invocation.Profile.Runtime == "opencode-http" {
		ctx, interruptWatcher, err = watchScheduledAgentInterrupt(ctx, claim)
		if err != nil {
			return taskscheduler.Evidence{}, errors.Join(&taskscheduler.ParkError{Reason: taskscheduler.ParkNoEffect}, err)
		}
	}
	switch claim.Task.Operation {
	case taskscheduler.OperationPlanner:
		_, err = ResumePlanning(ctx, controllerPath)
	case taskscheduler.OperationExplorer:
		var record ExplorerRecord
		record, err = RunExplorer(ctx, controllerPath, claim.Task.Input)
		if err == nil && claim.AgentTurn != nil {
			_, err = ensureAcceptedExplorerResult(ctx, controllerPath, claim, invocation, &record)
		}
	case taskscheduler.OperationWriter:
		if isCorrection && correction.TaskID != "" {
			ctx = withGraphWriterTask(ctx, correction.TaskID)
		} else if graphWriterCohortEnabled(s) && !isCorrection {
			ctx = withGraphWriterTask(ctx, claim.Task.ID)
		}
		_, err = RunWriter(ctx, controllerPath)
	case taskscheduler.OperationReviewer:
		_, err = RunReview(ctx, controllerPath)
	default:
		return taskscheduler.Evidence{}, errors.New("unsupported scheduled operation")
	}
	if interruptWatcher != nil {
		err = errors.Join(err, interruptWatcher.finish())
	}
	if err != nil {
		if errors.Is(err, taskpool.ErrCapacity) {
			return taskscheduler.Evidence{}, &taskscheduler.ParkError{Reason: taskscheduler.ParkCapacity}
		}
		after, probeErr := (ScheduledDispatchAdapter{}).Probe(observationCtx, taskscheduler.ProbeRequest{Task: claim.Task, AgentTurn: claim.AgentTurn})
		if errors.Is(err, ErrUsageQualification) && probeErr == nil {
			current, _, inspectErr := InspectWithHead(controllerPath)
			if inspectErr == nil && current.RunID == claim.Task.RunID && scheduledUsageQualificationParkable(current, invocation, after) {
				return taskscheduler.Evidence{}, errors.Join(&taskscheduler.ParkError{Reason: taskscheduler.ParkNoEffect}, err)
			}
		}
		return classifyScheduledDispatchError(err, after, probeErr)
	}
	return (ScheduledDispatchAdapter{}).Probe(observationCtx, taskscheduler.ProbeRequest{Task: claim.Task, AgentTurn: claim.AgentTurn})
}

func scheduledUsageQualificationParkable(s Snapshot, invocation runtime.Invocation, evidence taskscheduler.Evidence) bool {
	return evidence.Status == taskscheduler.StatusReady && evidence.AdmissionID == "" && !scheduledInvocationHasEffect(s, invocation)
}

// scheduledInvocationHasEffect is a narrow pre-dispatch guard for usage-policy
// refusal. READY evidence alone cannot establish that no intent was persisted.
func scheduledInvocationHasEffect(s Snapshot, invocation runtime.Invocation) bool {
	if _, ok := s.AgentDispatch[invocation.ID]; ok {
		return true
	}
	for _, access := range s.ModelAccess {
		if access.RuntimeInvocationID == invocation.ID || access.Intent.Reservation.InvocationID == invocation.ID {
			return true
		}
	}
	if receipt, ok := s.ProviderRuntime[invocation.ID]; ok && receipt.InvocationID == invocation.ID {
		return true
	}
	if s.WriterHost != nil && s.WriterHost.Intent.Invocation.ID == invocation.ID {
		return true
	}
	if s.ReviewHost != nil && s.ReviewHost.Intent.Invocation.ID == invocation.ID {
		return true
	}
	for _, host := range s.ScheduledReviewHosts {
		if host.Intent.Invocation.ID == invocation.ID {
			return true
		}
	}
	if s.PlannerAccess != nil && s.PlannerAccess.Reservation.InvocationID == invocation.ID {
		return true
	}
	if s.PlannerHost != nil {
		if planner, err := plannerInvocationForSnapshot(s); err == nil && planner.ID == invocation.ID {
			return true
		}
	}
	if s.ExplorerHost != nil && s.ExplorerHost.Intent.Invocation.ID == invocation.ID {
		return true
	}
	for _, host := range s.ExplorerRuns {
		if host.Intent.Invocation.ID == invocation.ID {
			return true
		}
	}
	for _, host := range s.GraphWriterHosts {
		if host.Intent.Invocation.ID == invocation.ID {
			return true
		}
	}
	for _, exploration := range s.Explorations {
		if exploration.Invocation.ID == invocation.ID {
			return true
		}
	}
	if s.WriterProposal != nil && s.WriterProposal.Invocation.ID == invocation.ID || s.Review != nil && s.Review.Invocation.ID == invocation.ID || s.Plan != nil && s.Plan.InvocationID == invocation.ID {
		return true
	}
	return false
}

func classifyScheduledDispatchError(dispatchErr error, after taskscheduler.Evidence, probeErr error) (taskscheduler.Evidence, error) {
	if probeErr != nil {
		return taskscheduler.Evidence{}, errors.Join(dispatchErr, probeErr)
	}
	if errors.Is(dispatchErr, worktree.ErrLeaseContention) && after.Status == taskscheduler.StatusReady && after.AdmissionID == "" {
		return taskscheduler.Evidence{}, errors.Join(&taskscheduler.ParkError{Reason: taskscheduler.ParkNoEffect}, dispatchErr)
	}
	switch after.Status {
	case taskscheduler.StatusSucceeded, taskscheduler.StatusFailed, taskscheduler.StatusCancelled:
		return after, nil
	case taskscheduler.StatusRunning, taskscheduler.StatusUnknown:
		return after, dispatchErr
	default:
		return taskscheduler.Evidence{}, dispatchErr
	}
}

func validateScheduledAgentTurn(task taskscheduler.TaskSpec, s Snapshot, invocation runtime.Invocation, turn *taskscheduler.AgentTurnBinding) error {
	admitted, hasAdmission := s.AgentDispatch[invocation.ID]
	if turn == nil {
		if hasAdmission && admitted.Admission.AgentTurn != nil {
			return errors.New("scheduled static task conflicts with dynamic agent admission")
		}
		return nil
	}
	if safepath.RequireDigest(turn.ParentAgentID) != nil || safepath.RequireDigest(turn.AgentID) != nil || safepath.RequireDigest(turn.TurnID) != nil || turn.TurnID != task.ID || turn.TurnSequence < 1 {
		return errors.New("invalid scheduled agent turn")
	}
	if task.Operation == taskscheduler.OperationPlanner || turn.TurnSequence > 1 && task.Operation != taskscheduler.OperationExplorer {
		return errors.New("scheduled operation does not support this dynamic agent turn")
	}
	state, err := agenttree.Inspect(task.ControllerPath + ".agent-tree")
	if err != nil || state.TreeID != s.RunID {
		return errors.Join(errors.New("scheduled agent tree binding unavailable"), err)
	}
	node, ok := agentNodeByID(state, turn.AgentID)
	contextHash, contextErr := access.InputID(invocation.Input)
	authority, authorityErr := agentAuthority(invocation.Profile.Role)
	if !ok || contextErr != nil || authorityErr != nil || node.ParentAgentID != turn.ParentAgentID || node.Role != invocation.Profile.Role || node.Authority != authority || turn.TurnSequence == 1 && (node.InvocationID != invocation.ID || node.ContextSHA256 != contextHash) {
		return errors.Join(errors.New("scheduled agent turn binding differs"), contextErr, authorityErr)
	}
	if hasAdmission {
		bound := admitted.Admission.AgentTurn
		if bound == nil || *bound != *turn {
			return errors.New("scheduled agent admission turn differs")
		}
	}
	return nil
}

// ReconcileScheduledClaim first repairs a missing body-free child-result index
// from an already accepted controller record. Any remaining role recovery is
// admitted only after proving the exact invocation has durable runtime history.
func ReconcileScheduledClaim(ctx context.Context, controllerPath string, claim taskscheduler.Claim) (taskscheduler.Evidence, error) {
	if ctx == nil || controllerPath != claim.Task.ControllerPath {
		return taskscheduler.Evidence{}, errors.New("scheduled reconciliation path changed")
	}
	if _, err := claim.ID(); err != nil {
		return taskscheduler.Evidence{}, err
	}
	s, _, invocation, err := scheduledInvocation(claim.Task, claim.AgentTurn)
	if err != nil {
		return taskscheduler.Evidence{}, err
	}
	if s.RunID != claim.Task.RunID || invocation.ID != claim.Task.InvocationID {
		return taskscheduler.Evidence{}, errors.New("scheduled reconciliation invocation changed")
	}
	evidence, err := (ScheduledDispatchAdapter{}).Probe(ctx, taskscheduler.ProbeRequest{Task: claim.Task, AgentTurn: claim.AgentTurn})
	if err != nil {
		return taskscheduler.Evidence{}, err
	}
	if evidence.Status == taskscheduler.StatusSucceeded || evidence.Status == taskscheduler.StatusFailed || evidence.Status == taskscheduler.StatusCancelled {
		return evidence, nil
	}
	if evidence.AdmissionID == "" {
		return taskscheduler.Evidence{}, errors.New("scheduled reconciliation lacks controller admission")
	}
	if claim.Task.Operation == taskscheduler.OperationExplorer && claim.AgentTurn != nil {
		indexed, indexErr := ensureAcceptedExplorerResult(ctx, controllerPath, claim, invocation, nil)
		if indexErr != nil {
			return taskscheduler.Evidence{}, indexErr
		}
		if indexed {
			return (ScheduledDispatchAdapter{}).Probe(ctx, taskscheduler.ProbeRequest{Task: claim.Task, AgentTurn: claim.AgentTurn})
		}
	}
	runtimePath, err := scheduledRuntimeJournal(s, invocation, claim.Task, claim.AgentTurn)
	if err != nil {
		return taskscheduler.Evidence{}, err
	}
	if err := requireScheduledOfflineRuntime(invocation.Profile.Runtime, runtimePath); err != nil {
		return taskscheduler.Evidence{}, err
	}
	return ExecuteScheduledClaim(ctx, controllerPath, claim)
}

func requireScheduledOfflineRuntime(runtimeName, runtimePath string) error {
	switch runtimeName {
	case "provider-api":
		state, err := providerruntime.Inspect(runtimePath)
		if err != nil || state.Binding == nil || state.Result == nil {
			return errors.Join(errors.New("scheduled direct-provider reconciliation lacks a durable result"), err)
		}
		return nil
	case "opencode-http":
		events, err := journal.Read(runtimePath)
		if err != nil || len(events) == 0 {
			return errors.Join(errors.New("scheduled OpenCode reconciliation lacks durable runtime history"), err)
		}
		// OpenCode Execute treats every existing runtime intent as recovery-only:
		// it completes sealed subordinate receipts or returns ErrRecoveryRequired.
		return nil
	case "codex-app-server":
		state, err := codexruntime.Inspect(runtimePath)
		if err != nil || state.Intent == nil {
			return errors.Join(errors.New("scheduled Codex reconciliation lacks durable runtime history"), err)
		}
		if state.Result != nil && state.TurnStatus == "completed" || state.Result == nil && (state.TurnStatus == "failed" || state.TurnStatus == "interrupted") {
			return nil
		}
		return errors.New("scheduled Codex runtime is nonterminal; provider resume denied")
	default:
		return errors.New("scheduled runtime has no offline reconciliation contract")
	}
}

func scheduledRuntimeJournal(s Snapshot, invocation runtime.Invocation, task taskscheduler.TaskSpec, turn *taskscheduler.AgentTurnBinding) (string, error) {
	stem := providerRoleJournalStem(invocation.Profile.Role, turn)
	switch invocation.Profile.Runtime {
	case "provider-api":
		return task.ControllerPath + "." + stem + ".provider-runtime.jsonl", nil
	case "opencode-http":
		var err error
		stem, err = providerInvocationJournalStemForSnapshot(task.ControllerPath, s, invocation, turn)
		if err != nil {
			return "", err
		}
		return task.ControllerPath + "." + stem + ".opencode-runtime.jsonl", nil
	case "codex-app-server":
		switch task.Operation {
		case taskscheduler.OperationPlanner:
			if s.PlannerHost != nil {
				return filepath.Join(s.PlannerHost.Root, "planner.jsonl"), nil
			}
		case taskscheduler.OperationExplorer:
			if s.ExplorerHost != nil && s.ExplorerHost.Intent.Invocation.ID == invocation.ID {
				return filepath.Join(s.ExplorerHost.Intent.Launch.Root, "explorer.jsonl"), nil
			}
			if run, ok := explorerRunForInvocation(s, invocation.ID); ok {
				return filepath.Join(run.Intent.Launch.Root, "explorer.jsonl"), nil
			}
		case taskscheduler.OperationWriter:
			if s.WriterHost != nil && s.WriterHost.Intent.Invocation.ID == invocation.ID {
				return filepath.Join(s.WriterHost.Intent.Launch.Root, "writer.jsonl"), nil
			}
			for _, host := range s.GraphWriterHosts {
				if host.Intent.Invocation.ID == invocation.ID {
					return filepath.Join(host.Intent.Launch.Root, "writer.jsonl"), nil
				}
			}
			if correction, ok := scheduledCorrectionForTask(s, task.ID); ok && correction.Invocation.Profile.Role != "reviewer" {
				expected, err := expectedWriterHostForTask(s, task.ID)
				if err != nil || expected.Invocation != invocation {
					return "", errors.Join(errors.New("scheduled writer host binding changed"), err)
				}
				return filepath.Join(expected.Launch.Root, "writer.jsonl"), nil
			}
			// A scheduler probes a static graph-writer claim before dispatch. At
			// that point the exact Codex intent has not been journaled yet, so
			// derive only its expected path from the already validated invocation.
			// This is a read-only lookup: it does not create the intent or runtime
			// journal, and the claim must still be an admitted static graph task.
			if turn == nil && isStaticGraphWriterCohort(s, taskscheduler.Claim{Task: task}) {
				expected, err := expectedWriterHostForTask(s, task.ID)
				if err != nil || expected.Invocation != invocation {
					return "", errors.Join(errors.New("static graph writer host binding changed"), err)
				}
				return filepath.Join(expected.Launch.Root, "writer.jsonl"), nil
			}
		case taskscheduler.OperationReviewer:
			if s.ReviewHost != nil && s.ReviewHost.Intent.Invocation.ID == invocation.ID {
				return filepath.Join(s.ReviewHost.Intent.Launch.Root, "review.jsonl"), nil
			}
			if task.ID != "" {
				if host, ok := s.ScheduledReviewHosts[task.ID]; ok && host.Intent.Invocation.ID == invocation.ID {
					return filepath.Join(host.Intent.Launch.Root, "review.jsonl"), nil
				}
			}
			if correction, ok := scheduledCorrectionForTask(s, task.ID); ok && correction.Invocation.Profile.Role == "reviewer" {
				expected, err := expectedScheduledReviewHost(s, task.ID)
				if err != nil || expected.Intent.Invocation != invocation {
					return "", errors.Join(errors.New("scheduled review host binding changed"), err)
				}
				return filepath.Join(expected.Intent.Launch.Root, "review.jsonl"), nil
			}
			if turn == nil {
				expected, err := expectedReviewHost(s)
				if err == nil && expected.Invocation == invocation {
					return filepath.Join(expected.Launch.Root, "review.jsonl"), nil
				}
			}
		}
		return "", errors.New("scheduled Codex reconciliation lacks exact host intent")
	default:
		return "", errors.New("scheduled runtime has no offline reconciliation contract")
	}
}

func scheduledInvocation(task taskscheduler.TaskSpec, turn *taskscheduler.AgentTurnBinding) (Snapshot, string, runtime.Invocation, error) {
	if !filepath.IsAbs(task.ControllerPath) || filepath.Clean(task.ControllerPath) != task.ControllerPath {
		return Snapshot{}, "", runtime.Invocation{}, errors.New("invalid scheduled controller path")
	}
	switch task.Operation {
	case taskscheduler.OperationExplorer:
		if strings.TrimSpace(task.Input) == "" || strings.TrimSpace(task.Input) != task.Input || len(task.Input) > 4096 {
			return Snapshot{}, "", runtime.Invocation{}, errors.New("invalid scheduled explorer input")
		}
	case taskscheduler.OperationPlanner, taskscheduler.OperationWriter, taskscheduler.OperationReviewer:
		if task.Input != "" {
			return Snapshot{}, "", runtime.Invocation{}, errors.New("scheduled operation does not accept input")
		}
	default:
		return Snapshot{}, "", runtime.Invocation{}, errors.New("unsupported scheduled operation")
	}
	if task.RunID != "" && safepath.RequireDigest(task.RunID) != nil || task.InvocationID != "" && safepath.RequireDigest(task.InvocationID) != nil {
		return Snapshot{}, "", runtime.Invocation{}, errors.New("invalid scheduled identity")
	}
	s, head, err := InspectWithHead(task.ControllerPath)
	if err != nil {
		return s, "", runtime.Invocation{}, err
	}
	if s.Creation.Config.Version != 2 {
		// V1 is permitted only for static graph explorers or explicitly opted-in
		// initial implementation writers, plus exact policy-bound correction
		// successors. V1 TaskPool remains forbidden; PumpOptions.Workers bounds
		// cohort concurrency instead.
		v1GraphExplorer := task.Operation == taskscheduler.OperationExplorer && v1StaticGraphExplorerStateAllowed(s) && isStaticGraphExplorerCohort(s, taskscheduler.Claim{Task: task})
		v1GraphWriter := task.Operation == taskscheduler.OperationWriter && s.State == "IMPLEMENTING" && graphWriterCohortEnabled(s) && isStaticGraphWriterCohort(s, taskscheduler.Claim{Task: task})
		v1CorrectionTurn := scheduledV1CorrectionTurnAllowed(s, task, turn)
		if !(s.Creation.Config.Version == 1 && s.Creation.Execution.GraphEnabled() && s.Creation.Execution.Context == taskContextBoundedV1 &&
			s.Workspace != nil && s.Candidate != nil && s.Plan != nil && s.Graph != nil && s.Creation.Config.TaskPool == nil && ((turn == nil && (v1GraphExplorer || v1GraphWriter)) || v1CorrectionTurn)) {
			return s, "", runtime.Invocation{}, errors.New("scheduled dispatch requires configuration v2")
		}
	}
	var invocation runtime.Invocation
	recordedTurnInvocation := false
	if correction, ok := scheduledCorrectionForTask(s, task.ID); ok {
		candidateID, candidateErr := s.Candidate.ID()
		if candidateErr != nil || correction.CandidateID != candidateID || correction.Invocation.Profile.Role != "writer" && correction.Invocation.Profile.Role != "fixer" && correction.Invocation.Profile.Role != "reviewer" {
			return s, "", runtime.Invocation{}, errors.Join(ErrAutonomousUnsafe, candidateErr)
		}
		if correction.Invocation.Profile.Role == "reviewer" && task.Operation != taskscheduler.OperationReviewer || correction.Invocation.Profile.Role != "reviewer" && task.Operation != taskscheduler.OperationWriter {
			return s, "", runtime.Invocation{}, ErrAutonomousUnsafe
		}
		invocation = correction.Invocation
	} else {
		switch task.Operation {
		case taskscheduler.OperationPlanner:
			invocation, err = plannerInvocationForSnapshot(s)
		case taskscheduler.OperationExplorer:
			if recorded, ok := acceptedScheduledExplorerInvocation(s, task, turn); ok {
				invocation, recordedTurnInvocation = recorded, true
			} else {
				invocation, err = explorerInvocation(s, task.Input)
			}
		case taskscheduler.OperationWriter:
			if graphWriterCohortEnabled(s) {
				if task.ID == "" {
					err = errors.New("graph writer task ID required")
				} else {
					invocation, err = writerInvocationForTask(s, task.ID)
				}
			} else {
				invocation, err = writerInvocation(s)
			}
		case taskscheduler.OperationReviewer:
			if s.Review != nil {
				invocation = s.Review.Invocation
				recordedTurnInvocation = turn != nil
				if exactErr := invocation.Validate(); exactErr != nil {
					err = errors.New("recorded review invocation identity mismatch")
				}
			} else {
				invocation, err = reviewInvocation(s)
			}
		default:
			err = errors.New("unsupported scheduled operation")
		}
	}
	if err == nil && turn != nil && !recordedTurnInvocation {
		invocation, err = scopedScheduledInvocation(s, invocation, task.Operation, turn, task.InvocationID)
	}
	return s, head, invocation, err
}

func scheduledV1CorrectionTurnAllowed(s Snapshot, task taskscheduler.TaskSpec, turn *taskscheduler.AgentTurnBinding) bool {
	if turn == nil || s.Creation.Config.Version != 1 || s.Creation.Execution == nil || s.Creation.Execution.SemanticCorrectionVersion != 1 || task.ID == "" || turn.TurnID != task.ID || turn.TurnSequence != 1 {
		return false
	}
	correction, ok := scheduledCorrectionForTask(s, task.ID)
	if !ok || correction.ScheduledTaskID != task.ID || correction.TaskID == "" && correction.Invocation.Profile.Role != "reviewer" {
		return false
	}
	switch correction.Invocation.Profile.Role {
	case "writer", "fixer":
		return task.Operation == taskscheduler.OperationWriter
	case "reviewer":
		return task.Operation == taskscheduler.OperationReviewer
	default:
		return false
	}
}

// scheduledInvocationFromSnapshot derives the same deterministic invocation as
// scheduledInvocation without reading the controller journal. Replay validators
// use this form because re-reading a journal while Replay is validating an event
// would recursively replay that same event.
func scheduledInvocationFromSnapshot(s Snapshot, task taskscheduler.TaskSpec, turn *taskscheduler.AgentTurnBinding) (runtime.Invocation, error) {
	if task.RunID != s.RunID || task.ID == "" && turn != nil {
		return runtime.Invocation{}, errors.New("scheduled snapshot task identity changed")
	}
	if s.Creation.Config.Version != 2 {
		v1GraphExplorer := task.Operation == taskscheduler.OperationExplorer && v1StaticGraphExplorerStateAllowed(s) && isStaticGraphExplorerCohort(s, taskscheduler.Claim{Task: task})
		v1GraphWriter := task.Operation == taskscheduler.OperationWriter && s.State == "IMPLEMENTING" && graphWriterCohortEnabled(s) && isStaticGraphWriterCohort(s, taskscheduler.Claim{Task: task})
		v1CorrectionTurn := scheduledV1CorrectionTurnAllowed(s, task, turn)
		if !(s.Creation.Config.Version == 1 && s.Creation.Execution.GraphEnabled() && s.Creation.Execution.Context == taskContextBoundedV1 &&
			s.Workspace != nil && s.Candidate != nil && s.Plan != nil && s.Graph != nil && s.Creation.Config.TaskPool == nil && ((turn == nil && (v1GraphExplorer || v1GraphWriter)) || v1CorrectionTurn)) {
			return runtime.Invocation{}, errors.New("scheduled dispatch requires configuration v2")
		}
	}
	var invocation runtime.Invocation
	recordedTurnInvocation := false
	if correction, ok := scheduledCorrectionForTask(s, task.ID); ok {
		candidateID, candidateErr := s.Candidate.ID()
		if candidateErr != nil || correction.CandidateID != candidateID || correction.Invocation.Profile.Role != "writer" && correction.Invocation.Profile.Role != "fixer" && correction.Invocation.Profile.Role != "reviewer" {
			return runtime.Invocation{}, errors.Join(ErrAutonomousUnsafe, candidateErr)
		}
		if correction.Invocation.Profile.Role == "reviewer" && task.Operation != taskscheduler.OperationReviewer || correction.Invocation.Profile.Role != "reviewer" && task.Operation != taskscheduler.OperationWriter {
			return runtime.Invocation{}, ErrAutonomousUnsafe
		}
		invocation = correction.Invocation
	} else {
		var err error
		switch task.Operation {
		case taskscheduler.OperationPlanner:
			invocation, err = plannerInvocationForSnapshot(s)
		case taskscheduler.OperationExplorer:
			if recorded, ok := acceptedScheduledExplorerInvocation(s, task, turn); ok {
				invocation, recordedTurnInvocation = recorded, true
			} else {
				invocation, err = explorerInvocation(s, task.Input)
			}
		case taskscheduler.OperationWriter:
			if graphWriterCohortEnabled(s) {
				if task.ID == "" {
					err = errors.New("graph writer task ID required")
				} else {
					invocation, err = writerInvocationForTask(s, task.ID)
				}
			} else {
				invocation, err = writerInvocation(s)
			}
		case taskscheduler.OperationReviewer:
			if s.Review != nil {
				invocation = s.Review.Invocation
				recordedTurnInvocation = turn != nil
				if exactErr := invocation.Validate(); exactErr != nil {
					err = errors.New("recorded review invocation identity mismatch")
				}
			} else {
				invocation, err = reviewInvocation(s)
			}
		default:
			err = errors.New("unsupported scheduled operation")
		}
		if err != nil {
			return runtime.Invocation{}, err
		}
	}
	if turn != nil && !recordedTurnInvocation {
		var err error
		invocation, err = scopedScheduledInvocation(s, invocation, task.Operation, turn, task.InvocationID)
		if err != nil {
			return runtime.Invocation{}, err
		}
	}
	if invocation.ID == "" || task.InvocationID != invocation.ID {
		return runtime.Invocation{}, errors.New("scheduled snapshot invocation identity changed")
	}
	return invocation, nil
}

func scheduledTurnInvocation(base runtime.Invocation, operation taskscheduler.Operation, turnID string) (runtime.Invocation, error) {
	if safepath.RequireDigest(turnID) != nil {
		return runtime.Invocation{}, errors.New("invalid scheduled turn identity")
	}
	input, err := canonical.Bytes(struct {
		Version   int                     `json:"version"`
		Operation taskscheduler.Operation `json:"operation"`
		TurnID    string                  `json:"turn_id"`
		BaseInput string                  `json:"base_input"`
	}{1, operation, turnID, base.Input})
	if err != nil {
		return runtime.Invocation{}, err
	}
	return runtime.NewInvocationWithCodexAutoCompact(base.Profile, string(input), base.CodexAutoCompactOption())
}

// An admitted observation is bound to its exact original invocation. Later
// observations can change source-guidance selection for new work; they cannot
// rewrite this completed turn's input when probing or reconciling its receipt.
func acceptedScheduledExplorerInvocation(s Snapshot, task taskscheduler.TaskSpec, turn *taskscheduler.AgentTurnBinding) (runtime.Invocation, bool) {
	if turn == nil || task.Operation != taskscheduler.OperationExplorer || turn.TurnID != task.ID {
		return runtime.Invocation{}, false
	}
	dispatch, ok := s.AgentDispatch[task.InvocationID]
	if !ok || dispatch.Admission.AgentTurn == nil || *dispatch.Admission.AgentTurn != *turn {
		return runtime.Invocation{}, false
	}
	for _, record := range s.Explorations {
		if record.Question == task.Input && record.Invocation.ID == task.InvocationID && record.Invocation == dispatch.Admission.Invocation && record.Invocation.Validate() == nil {
			return record.Invocation, true
		}
	}
	return runtime.Invocation{}, false
}

func scheduledInvocationFromContext(ctx context.Context, operation taskscheduler.Operation, base runtime.Invocation) (runtime.Invocation, error) {
	turn := scheduledAgentTurn(ctx)
	if turn == nil {
		return base, nil
	}
	return scheduledTurnInvocation(base, operation, turn.TurnID)
}

// v1StaticGraphExplorerStateAllowed keeps the configuration-v1 scheduler
// exception limited to initial graph work, plus the explicitly opted-in
// bounded repair-design wave. Other REPAIRING work remains outside this
// exception.
func v1StaticGraphExplorerStateAllowed(s Snapshot) bool {
	return s.State == "IMPLEMENTING" || s.State == "REPAIRING" && repairPlanningEnabled(s)
}

func resolveScheduledRecordedInvocation(s Snapshot, base, observed runtime.Invocation) (runtime.Invocation, error) {
	if observed == base {
		return base, nil
	}
	for _, correction := range s.RoleCorrections {
		if correction.ScheduledTaskID == "" || correction.BaseInvocationID != base.ID || correction.Invocation.Profile.Role != base.Profile.Role {
			continue
		}
		operation, err := scheduledOperationForRole(correction.Invocation.Profile.Role)
		if err != nil {
			continue
		}
		scoped, err := scheduledTurnInvocation(correction.Invocation, operation, correction.ScheduledTaskID)
		if err == nil && scoped == observed {
			return scoped, nil
		}
	}
	resolved, err := resolveScheduledInvocationID(s, base, observed.ID)
	if err != nil || resolved != observed {
		return runtime.Invocation{}, errors.Join(errors.New("scheduled role invocation substituted"), err)
	}
	return resolved, nil
}

func resolveScheduledInvocationID(s Snapshot, base runtime.Invocation, invocationID string) (runtime.Invocation, error) {
	if invocationID == base.ID {
		return base, nil
	}
	dispatch, ok := s.AgentDispatch[invocationID]
	if !ok || dispatch.Admission.AgentTurn == nil || dispatch.Admission.Invocation.ID != invocationID {
		return runtime.Invocation{}, errors.New("scheduled role admission unavailable")
	}
	operation, err := scheduledOperationForRole(base.Profile.Role)
	if err != nil {
		return runtime.Invocation{}, err
	}
	scoped, err := scopedScheduledInvocation(s, base, operation, dispatch.Admission.AgentTurn, invocationID)
	if err != nil || scoped != dispatch.Admission.Invocation {
		return runtime.Invocation{}, errors.Join(errors.New("scheduled role admission invocation differs"), err)
	}
	return scoped, nil
}

func scheduledOperationForRole(role string) (taskscheduler.Operation, error) {
	switch role {
	case "planner":
		return taskscheduler.OperationPlanner, nil
	case "explorer":
		return taskscheduler.OperationExplorer, nil
	case "writer", "fixer":
		return taskscheduler.OperationWriter, nil
	case "reviewer":
		return taskscheduler.OperationReviewer, nil
	default:
		return "", errors.New("unsupported scheduled role")
	}
}

func controllerJournalHead(path string) (string, error) {
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 {
		return "", errors.Join(errors.New("controller journal is empty"), err)
	}
	return events[len(events)-1].Hash, nil
}

func scheduledEvidence(s Snapshot, head string, invocation runtime.Invocation, task taskscheduler.TaskSpec) taskscheduler.Evidence {
	evidence := taskscheduler.Evidence{RunID: s.RunID, InvocationID: invocation.ID, ControllerHead: head, Status: taskscheduler.StatusReady}
	if dispatch, ok := s.AgentDispatch[invocation.ID]; ok {
		evidence.AdmissionID, _ = dispatch.Admission.ID()
		if dispatch.Observation == nil {
			evidence.Status = taskscheduler.StatusRunning
		} else if dispatch.Observation.Status == agenttree.StatusUnknown {
			evidence.Status = taskscheduler.StatusUnknown
		} else if dispatch.Observation.Status == agenttree.StatusSucceeded {
			evidence.Status = taskscheduler.StatusRunning
		}
	} else {
		for _, admitted := range s.ModelAccess {
			if admitted.RuntimeInvocationID == invocation.ID {
				evidence.AdmissionID = admitted.Intent.Reservation.InvocationID
				evidence.Status = taskscheduler.StatusRunning
				if admitted.Terminal != nil && admitted.Terminal.Receipt.Status != "completed" {
					evidence.Status = taskscheduler.StatusFailed
				}
			}
		}
		if task.Operation == taskscheduler.OperationPlanner && s.PlannerAccess != nil {
			evidence.AdmissionID = s.PlannerAccess.Reservation.InvocationID
			evidence.Status = taskscheduler.StatusRunning
		}
	}
	complete := task.Operation == taskscheduler.OperationPlanner && s.Plan != nil && s.Plan.InvocationID == invocation.ID
	if task.Operation == taskscheduler.OperationExplorer {
		for _, record := range s.Explorations {
			complete = complete || record.Invocation.ID == invocation.ID
		}
	}
	complete = complete || task.Operation == taskscheduler.OperationWriter && s.WriterProposal != nil && s.WriterProposal.Invocation.ID == invocation.ID
	if task.Operation == taskscheduler.OperationWriter && graphWriterCohortEnabled(s) {
		if record, ok := s.GraphWriterResults[task.ID]; ok && graphWriterRecordInvocation(record).ID == invocation.ID {
			complete = true
		} else if correction, ok := scheduledCorrectionForTask(s, task.ID); ok &&
			(correction.Invocation.Profile.Role == "writer" || correction.Invocation.Profile.Role == "fixer") &&
			correction.TaskID != "" && correction.ScheduledTaskID == task.ID {
			// Dynamic correction turns are keyed by their scheduled turn ID
			// while the integrated proposal remains keyed by the original
			// graph task. Resolve only through the recorded correction and
			// require the exact scoped invocation plus the exact recorded
			// proposal invocation. No scan, no completion-only success.
			if scoped, err := scheduledTurnInvocation(correction.Invocation, taskscheduler.OperationWriter, correction.ScheduledTaskID); err == nil && scoped == invocation {
				if record, ok := s.GraphWriterResults[correction.TaskID]; ok && graphWriterRecordInvocation(record).ID == invocation.ID {
					complete = true
				}
			}
		}
	}
	complete = complete || task.Operation == taskscheduler.OperationReviewer && s.Review != nil && s.Review.Invocation.ID == invocation.ID
	// V1 static graph-bound explorers have no separate ModelAccess/AgentDispatch
	// admission; the exact recorded ExplorerRecord plus frozen task_context is
	// the terminal receipt. Bind AdmissionID to the exact invocation so the
	// scheduler can observe success without broadening V1 writer/reviewer/planner
	// or dynamic turns. Every lookup still binds the exact invocation.
	if complete && evidence.AdmissionID == "" && task.Operation == taskscheduler.OperationExplorer &&
		s.Creation.Config.Version == 1 && s.Creation.Execution.GraphEnabled() {
		evidence.AdmissionID = invocation.ID
	}
	if complete && evidence.AdmissionID == "" && task.Operation == taskscheduler.OperationWriter && graphWriterCohortEnabled(s) && s.Creation.Config.Version == 1 {
		evidence.AdmissionID = invocation.ID
	}
	if complete && evidence.AdmissionID != "" {
		evidence.Status = taskscheduler.StatusSucceeded
	}
	if lifecycleStatus(s) == LifecyclePaused && evidence.AdmissionID == "" {
		evidence.Status = taskscheduler.StatusPaused
	}
	if lifecycleStatus(s) == LifecycleCancelled && evidence.AdmissionID != "" && !complete {
		evidence.Status = taskscheduler.StatusCancelled
	}
	return evidence
}

func scheduledAdmissionID(s Snapshot, invocationID string) string {
	if dispatch, ok := s.AgentDispatch[invocationID]; ok {
		id, _ := dispatch.Admission.ID()
		return id
	}
	for _, admitted := range s.ModelAccess {
		if admitted.RuntimeInvocationID == invocationID {
			return admitted.Intent.Reservation.InvocationID
		}
	}
	if s.PlannerAccess != nil && s.Creation.Config.Planner.Runtime == "fake" {
		planner, err := plannerInvocationForSnapshot(s)
		if err == nil && planner.ID == invocationID {
			return s.PlannerAccess.Reservation.InvocationID
		}
	}
	return ""
}

var _ taskscheduler.Adapter = ScheduledDispatchAdapter{}

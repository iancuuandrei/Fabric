package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/iancuuandrei/fabric/internal/candidatetools"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

// RunWriter runs or resumes a configured Codex proposal invocation, then records
// the validated proposal without applying files. Candidate tools include admitted
// modifications and reject drift from the invocation-bound state.
func RunWriter(ctx context.Context, path string) (WriterRecord, error) {
	s, err := Inspect(path)
	if err != nil {
		return WriterRecord{}, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return WriterRecord{}, err
	}
	isolatedInitial, err := isolatedInitialWriterForTask(s, graphWriterTaskFromContext(ctx))
	if err != nil {
		return WriterRecord{}, err
	}
	taskID := graphWriterTaskFromContext(ctx)
	if taskID != "" && !parallelImplementationEnabled(s) && !isolatedImplementationEnabled(s) {
		return WriterRecord{}, errors.New("graph task-bound writer requires an implementation policy")
	}
	if taskID != "" {
		deactivate := markGraphWriterActive(path, taskID)
		defer deactivate()
	}
	if taskContextEnabled(s) {
		var blocked error
		if taskID != "" {
			blocked = autonomousGraphWriterDispatchBlocked(s, path, taskID)
		} else {
			blocked = autonomousDispatchBlocked(s)
		}
		if blocked != nil {
			return WriterRecord{}, blocked
		}
		question := s.Creation.Objective
		if taskID != "" {
			question, err = graphWriterTaskQuestion(s, taskID)
			if err != nil {
				return WriterRecord{}, err
			}
		}
		if isolatedInitial {
			if err := maybeAdmitIsolatedWriterTaskContext(ctx, path, taskID, question); err != nil {
				return WriterRecord{}, err
			}
		} else if err := maybeAdmitTaskContext(ctx, path, writerTaskRole(s), question); err != nil {
			return WriterRecord{}, err
		}
		s, err = Inspect(path)
		if err != nil {
			return WriterRecord{}, err
		}
		if err := requireCurrentHostAdmission(ctx, s); err != nil {
			return WriterRecord{}, err
		}
		isolatedInitial, err = isolatedInitialWriterForTask(s, taskID)
		if err != nil {
			return WriterRecord{}, err
		}
	}
	var invocation runtime.Invocation
	if correction, ok := scheduledRoleCorrectionFromContext(ctx); ok && correction.TaskID == taskID {
		invocation = correction.Invocation
	} else {
		invocation, err = writerInvocationForTask(s, taskID)
	}
	if err != nil {
		return WriterRecord{}, err
	}
	invocation, err = scheduledInvocationFromContext(ctx, taskscheduler.OperationWriter, invocation)
	if err != nil {
		return WriterRecord{}, err
	}
	if err := requireExecutableRoleRuntime(invocation.Profile); err != nil {
		return WriterRecord{}, err
	}
	if err := requireModelAccessAdmissionAvailable(s, invocation); err != nil {
		return WriterRecord{}, err
	}
	if taskID == "" && s.WriterProposal != nil && s.WriterProposal.Invocation == invocation {
		return WriterRecord{}, errors.New("writer proposal already recorded; inspect its provenance rather than dispatch again")
	}
	if taskID != "" {
		if _, exists := s.GraphWriterResults[taskID]; exists {
			return WriterRecord{}, errors.New("graph writer proposal already recorded; inspect its provenance rather than dispatch again")
		}
		if isolatedInitial {
			if invocation.Profile.Runtime != "codex-app-server" && invocation.Profile.Runtime != "opencode-http" {
				return WriterRecord{}, errors.New("isolated graph writers require the Codex app-server or OpenCode runtime")
			}
		} else if invocation.Profile.Runtime != "codex-app-server" {
			return WriterRecord{}, errors.New("parallel graph writers currently require the Codex app-server runtime")
		}
	}
	if isExternalRuntime(invocation.Profile.Runtime) {
		result, err := executeExternalRole(ctx, path, s, invocation)
		if err != nil {
			return WriterRecord{}, err
		}
		return RecordWriterProposal(ctx, path, invocation, result)
	}
	if invocation.Profile.Runtime == "provider-api" {
		result, err := executeDirectProviderRole(ctx, path, s, invocation, "")
		if err != nil {
			return WriterRecord{}, err
		}
		return RecordWriterProposal(ctx, path, invocation, result)
	}
	if invocation.Profile.Runtime == "opencode-http" {
		if taskID == "" {
			result, err := executeOpenCodeRole(ctx, path, s, invocation, "")
			if err != nil {
				return WriterRecord{}, err
			}
			return RecordWriterProposal(ctx, path, invocation, result)
		}
		if !isolatedInitial {
			return WriterRecord{}, errors.New("parallel graph writers currently require the Codex app-server runtime")
		}
		question, err := graphWriterTaskQuestion(s, taskID)
		if err != nil {
			return WriterRecord{}, err
		}
		binding, err := isolatedWriterBindingForSnapshot(s, taskID)
		if err != nil {
			return WriterRecord{}, err
		}
		startedAt := time.Now().UTC()
		result, err := executeIsolatedOpenCodeWriter(ctx, path, taskID, invocation, binding, question)
		if err != nil {
			return WriterRecord{}, err
		}
		endedAt := time.Now().UTC()
		proposal, err := prepareIsolatedGraphWriterProposal(ctx, path, taskID, invocation, result)
		if err != nil {
			return WriterRecord{}, err
		}
		if err := recordIsolatedGraphWriterProposal(path, proposal, observedGraphWriterDispatchTiming(startedAt, endedAt)); err != nil {
			return WriterRecord{}, err
		}
		return WriterRecord{Invocation: invocation, Result: result}, nil
	}
	hostTaskID := taskID
	if correction, ok := scheduledRoleCorrectionFromContext(ctx); ok && correction.TaskID == taskID && correction.ScheduledTaskID != "" {
		hostTaskID = correction.ScheduledTaskID
	}
	expected, err := expectedWriterHostForTask(s, hostTaskID)
	if err != nil {
		return WriterRecord{}, err
	}
	// This bounds controller wrapper execution only. It says nothing about
	// provider request count, and is persisted only after a successful proposal
	// so UNKNOWN dispatch behavior remains unchanged.
	var startedAt time.Time
	if taskID != "" {
		startedAt = time.Now().UTC()
	}
	result, err := executeWriterForTask(ctx, path, s, expected, taskID)
	if err != nil {
		return WriterRecord{}, err
	}
	if taskID == "" {
		return RecordWriterProposal(ctx, path, expected.Invocation, result)
	}
	endedAt := time.Now().UTC()
	if isolatedInitial {
		proposal, err := prepareIsolatedGraphWriterProposal(ctx, path, taskID, expected.Invocation, result)
		if err != nil {
			return WriterRecord{}, err
		}
		if err := recordIsolatedGraphWriterProposal(path, proposal, observedGraphWriterDispatchTiming(startedAt, endedAt)); err != nil {
			return WriterRecord{}, err
		}
		return WriterRecord{Invocation: expected.Invocation, Result: result}, nil
	}
	record, err := prepareGraphWriterFiles(ctx, path, taskID, expected.Invocation, result)
	if err != nil {
		return WriterRecord{}, err
	}
	if err := recordGraphWriterProposal(path, taskID, record, observedGraphWriterDispatchTiming(startedAt, endedAt)); err != nil {
		return WriterRecord{}, err
	}
	return record, nil
}

func executeWriter(ctx context.Context, path string, s Snapshot, expected WriterHostIntent) (result runtime.Result, err error) {
	return executeWriterForTask(ctx, path, s, expected, "")
}

func executeWriterForTask(ctx context.Context, path string, s Snapshot, expected WriterHostIntent, taskID string) (result runtime.Result, err error) {
	hostTaskID := taskID
	if correction, ok := scheduledRoleCorrectionFromContext(ctx); ok && correction.TaskID == taskID && correction.ScheduledTaskID != "" {
		hostTaskID = correction.ScheduledTaskID
	}
	var isolated *isolatedWriterBinding
	workspace := s.Workspace
	useIsolated, err := isolatedInitialWriterForTask(s, taskID)
	if err != nil {
		return result, err
	}
	if useIsolated {
		binding, bindingErr := isolatedWriterBindingForSnapshot(s, taskID)
		if bindingErr != nil {
			return result, bindingErr
		}
		isolated = &binding
		workspace = &binding.Workspace
	}
	if workspace == nil {
		return result, errors.New("writer workspace unavailable")
	}
	var lease controllerCandidateLease
	if taskID == "" {
		lease, err = worktree.Acquire(workspace.Request)
	} else {
		// Opt-in graph writer turns operate against a frozen read-only candidate.
		// Multiple independent proposals may therefore share candidate reads;
		// their single aggregate file effect is applied only after all turns end.
		lease, err = worktree.AcquireRead(workspace.Request)
	}
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return result, err
	}
	s = graphWriterProjectedSnapshotWithHost(s, taskID, hostTaskID)
	currentIsolated, modeErr := isolatedInitialWriterForTask(s, taskID)
	if modeErr != nil || currentIsolated != (isolated != nil) {
		return result, errors.Join(errors.New("writer isolation mode changed before dispatch"), modeErr)
	}
	if isolated != nil {
		currentBinding, bindingErr := isolatedWriterBindingForSnapshot(s, taskID)
		if bindingErr != nil || !sameCanonical(currentBinding, *isolated) {
			return result, errors.Join(errors.New("isolated writer binding changed before dispatch"), bindingErr)
		}
	}
	current, err := expectedWriterHostForTask(s, hostTaskID)
	if err != nil {
		return result, err
	}
	if current != expected {
		return result, errors.New("writer state changed before dispatch")
	}
	if s.Creation.Config.Version == 2 {
		intent, err := deriveModelAccessIntent(s, expected.Invocation, 1)
		if err != nil {
			return result, err
		}
		if err := validateCodexSubscriptionAccess(s, intent); err != nil {
			return result, err
		}
	}
	var runtimeRI *codexruntime.RIBinding
	var runtimeLexical *codexruntime.LexicalBinding
	lexical, err := writerLexicalContext(s, isolated != nil)
	if err != nil {
		return result, err
	}
	if lexical != nil {
		binding, err := selectRuntimeLexicalLeased(ctx, path, lexical.Scope == "candidate", *workspace)
		if err != nil {
			return result, err
		}
		buildID, err := binding.Base.Build.ID()
		if err != nil {
			return result, err
		}
		if buildID != lexical.BuildID {
			return result, errors.New("writer lexical selection changed")
		}
		if binding.Overlay != nil {
			id, err := binding.Overlay.ID(binding.Base.Manifest)
			if err != nil || id != lexical.OverlayID {
				return result, errors.New("writer lexical overlay changed")
			}
		}
		runtimeLexical = &binding
	}
	intelligence, err := roleRI(s)
	if err != nil {
		return result, err
	}
	if intelligence != nil {
		binding, err := SelectRuntimeRI(ctx, path)
		if err != nil {
			return result, err
		}
		if binding != intelligence.Binding {
			return result, errors.New("writer RI selection changed")
		}
		runtimeRI = &binding
	}
	var before worktree.Candidate
	if isolated != nil {
		before, _, err = worktree.Capture(ctx, *workspace)
		if err != nil || before != isolated.Candidate {
			return result, errors.Join(errors.New("isolated writer child candidate mismatch"), err)
		}
	} else {
		before, err = observeCandidate(ctx, path, s)
		if err != nil {
			return result, err
		}
		if s.Candidate == nil || before != *s.Candidate {
			return result, errors.New("writer base candidate mismatch")
		}
	}
	if s.WriterHost == nil || s.WriterHost.Intent != expected {
		if _, _, _, _, policyErr := codexRuntimeUsagePolicy(s, expected.Invocation.Profile.Role); policyErr != nil {
			return result, policyErr
		}
		if err := appendWriterHostTransition(path, hostTaskID, "writer.host-intent", expected); err != nil {
			return result, err
		}
		s, err = Inspect(path)
		if err != nil {
			return result, err
		}
		s = graphWriterProjectedSnapshotWithHost(s, taskID, hostTaskID)
	}
	l := expected.Launch
	if !s.WriterHost.Ready {
		c := s.Creation.Config.Codex
		if err := safepath.Directory(c.StateRoot); err != nil {
			return result, err
		}
		if _, statErr := os.Stat(l.Root); os.IsNotExist(statErr) {
			if err := safepath.EnsureDirectory(c.StateRoot, s.RunID+"/writer-"+expected.Invocation.ID); err != nil {
				return result, err
			}
			prepared, err := codexhost.Prepare(l.Root, l.Binary)
			if err != nil {
				return result, err
			}
			if prepared != l {
				return result, errors.New("writer prepared host substitution")
			}
		} else if statErr != nil {
			return result, statErr
		}
		if err := l.Validate(); err != nil {
			return result, err
		}
		if err := appendWriterHostTransition(path, hostTaskID, "writer.host-ready", expected); err != nil {
			return result, err
		}
	}
	runtimePath := filepath.Join(l.Root, "writer.jsonl")
	state, readErr := codexruntime.Inspect(runtimePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return result, readErr
	}
	semanticUsagePending := false
	if readErr == nil && state.SemanticResult != nil && state.Result == nil {
		if s.Creation.Config.Version != 2 {
			return result, errors.New("retained semantic output lacks required usage accounting support")
		}
		_, result, _, err = observeSemanticUsagePending(ctx, path, runtimePath, expected.Invocation, "harness.writer-result.v1")
		if err != nil {
			return runtime.Result{}, errors.Join(codexruntime.ErrUsageReconciliation, err)
		}
		semanticUsagePending = true
	}
	if readErr == nil && s.Creation.Config.Version == 2 && state.Result == nil && (state.TurnStatus == "failed" || state.TurnStatus == "interrupted") {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_, _, terminalErr := completeModelAccess(cleanupCtx, path, runtimePath, expected.Invocation, "harness.writer-result.v1")
		cancel()
		return result, errors.Join(errors.New("writer runtime is terminal without a result"), terminalErr)
	}
	if readErr != nil || state.Result == nil && !semanticUsagePending {
		usageBudget, requireLiveUsage, usageQualified, unlimitedTokens, policyErr := codexRuntimeUsagePolicy(s, expected.Invocation.Profile.Role)
		if policyErr != nil {
			return result, policyErr
		}
		candidateBinding := &codexruntime.CandidateBinding{Workspace: *workspace, Candidate: before}
		if (s.Creation.Config.WriterContract == writercontract.ContractAnchoredEditsV2 || s.Creation.Config.WriterContract == writercontract.ContractAnchoredEditsV3) && (expected.Invocation.Profile.Role == "writer" || expected.Invocation.Profile.Role == "fixer") {
			candidateBinding.AnchorValidationVersion = candidatetools.AnchorValidationVersion
		}
		directory := filepath.Join(l.Root, "workspace")
		threadDirectory := ""
		if isolated != nil {
			directory = workspace.Request.Path
			threadDirectory = directory
		}
		a := &codexruntime.Adapter{JournalPath: runtimePath, Directory: directory, Source: &s.Creation.Repository, Candidate: candidateBinding, RI: runtimeRI, Lexical: runtimeLexical, UsageBudget: usageBudget, RequireLiveUsage: requireLiveUsage, UsageQualified: usageQualified, UnlimitedTokens: unlimitedTokens}
		tools := codexruntime.NewToolSession(ctx, a)
		defer tools.Close()
		h, err := startCodexRoleHostAtThreadDirectory(ctx, l, s.Creation.Config.Codex, expected.Invocation.Profile, a.SourceToolsForInvocation(expected.Invocation), tools.HandleTool, threadDirectory)
		if err != nil {
			return result, err
		}
		defer func() { err = errors.Join(err, h.Close()) }()
		if err := appendWriterHostTransition(path, hostTaskID, "writer.host-observed", h.Receipt); err != nil {
			return result, err
		}
		if err := h.LoginChatGPT(ctx, s.Creation.Config.Codex.AuthSource); err != nil {
			return result, err
		}
		a.Client = h.Client
		if s.Creation.Config.Version == 2 {
			var admission ModelAccessState
			s, admission, err = ensureModelAccessIntent(path, s, expected.Invocation)
			if err != nil {
				return result, err
			}
			if err := requireModelAccessActive(path, s, admission); err != nil {
				return result, err
			}
		}
		if state.Intent == nil {
			_, err = a.Execute(ctx, expected.Invocation)
		} else {
			_, err = a.Resume(ctx, expected.Invocation.ID)
		}
		if err != nil {
			if errors.Is(err, codexruntime.ErrUsageReconciliation) && s.Creation.Config.Version == 2 {
				_, retained, _, pendingErr := observeSemanticUsagePending(ctx, path, runtimePath, expected.Invocation, "harness.writer-result.v1")
				if pendingErr == nil {
					result = retained
					semanticUsagePending = true
					err = nil
				} else {
					return runtime.Result{}, errors.Join(err, pendingErr)
				}
			}
		}
		if err != nil {
			if s.Creation.Config.Version == 2 {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				_, _, terminalErr := completeModelAccess(cleanupCtx, path, runtimePath, expected.Invocation, "harness.writer-result.v1")
				cancel()
				return result, errors.Join(err, terminalErr)
			}
			return result, err
		}
	}
	state, head, err := codexruntime.InspectWithHead(runtimePath)
	if err != nil {
		return result, err
	}
	latest, err := Inspect(path)
	if err != nil {
		return result, err
	}
	latest = graphWriterProjectedSnapshotWithHost(latest, taskID, hostTaskID)
	if latest.WriterHost == nil || latest.WriterHost.Intent != expected || !latest.WriterHost.Ready || latest.WriterHost.Receipt == nil {
		return result, errors.New("writer host observation missing")
	}
	directory := filepath.Join(l.Root, "workspace")
	if isolated != nil {
		directory = workspace.Request.Path
	}
	if state.Intent == nil || state.Intent.Invocation != expected.Invocation || state.Intent.Directory != directory || (state.Result == nil && !semanticUsagePending) || state.Thread == nil || state.TurnStatus != "completed" || state.Source == nil || *state.Source != s.Creation.Repository {
		return result, errors.New("writer runtime evidence incomplete or substituted")
	}
	if (state.RI == nil) != (runtimeRI == nil) || (runtimeRI != nil && *state.RI != *runtimeRI) {
		return result, errors.New("writer runtime RI binding mismatch")
	}
	if state.Candidate == nil || state.Candidate.Workspace != *workspace || state.Candidate.Candidate != before {
		return result, errors.New("writer runtime candidate mismatch")
	}
	candidateID, err := before.ID()
	if err != nil {
		return result, err
	}
	if err := validateRoleLexicalState(s, runtimeLexical, state, candidateID); err != nil {
		return result, err
	}
	if err := state.Thread.Validate(expected.Invocation.Profile, directory); err != nil {
		return result, err
	}
	resultValue := state.Result
	if semanticUsagePending {
		resultValue = &result
		if state.SemanticResult == nil || *state.SemanticResult != result {
			return runtime.Result{}, errors.New("retained writer semantic result changed")
		}
	}
	if resultValue == nil {
		return runtime.Result{}, errors.New("writer runtime result missing")
	}
	if err := runtime.ValidateResult(expected.Invocation, *resultValue, true); err != nil {
		return result, err
	}
	var after worktree.Candidate
	if isolated != nil {
		after, _, err = worktree.Capture(ctx, *workspace)
		if err != nil || after != before {
			return result, errors.Join(errors.New("isolated writer child changed during execution"), err)
		} else {
			latestBinding, bindingErr := isolatedWriterBindingForSnapshot(latest, taskID)
			if bindingErr != nil || !sameCanonical(latestBinding, *isolated) {
				return result, errors.Join(errors.New("isolated writer receipt changed during execution"), bindingErr)
			}
		}
	} else {
		after, err = observeCandidate(ctx, path, s)
		if err != nil {
			return result, err
		}
		if after != before {
			return result, errors.New("writer source changed during execution")
		}
	}
	hash, err := canonical.Hash("harness.writer-result.v1", *resultValue)
	if err != nil {
		return result, err
	}
	if s.Creation.Config.Version == 2 && !semanticUsagePending {
		var terminal ModelAccessTerminal
		latest, terminal, err = completeModelAccess(ctx, path, runtimePath, expected.Invocation, "harness.writer-result.v1")
		if err != nil {
			return result, err
		}
		latest = graphWriterProjectedSnapshotWithHost(latest, taskID, hostTaskID)
		if terminal.Receipt.Status != "completed" || terminal.RuntimeJournalHead != head || terminal.Receipt.OutputHash != hash {
			return result, errors.New("writer result differs from terminal model access receipt")
		}
	}
	receipt := WriterRuntimeReceipt{InvocationID: expected.Invocation.ID, ThreadID: state.Thread.ThreadID, TurnID: state.TurnID, JournalHead: head, ResultHash: hash, UsagePending: semanticUsagePending}
	if latest.WriterHost.RuntimeReceipt == nil {
		if err := appendWriterHostTransition(path, hostTaskID, "writer.runtime-observed", receipt); err != nil {
			return result, err
		}
	} else if *latest.WriterHost.RuntimeReceipt != receipt {
		return result, errors.New("writer runtime journal no longer matches recorded receipt")
	}
	return *resultValue, nil
}

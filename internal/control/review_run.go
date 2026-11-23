package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// RunReview runs or resumes a configured Codex proposal invocation, then records
// the validated proposal without applying files. Candidate tools include admitted
// modifications and reject drift from the invocation-bound state.
func RunReview(ctx context.Context, path string) (ReviewRecord, error) {
	s, err := Inspect(path)
	if err != nil {
		return ReviewRecord{}, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return ReviewRecord{}, err
	}
	if taskContextEnabled(s) {
		if err := autonomousDispatchBlocked(s); err != nil {
			return ReviewRecord{}, err
		}
		if err := maybeAdmitTaskContext(ctx, path, "reviewer", s.Creation.Objective); err != nil {
			return ReviewRecord{}, err
		}
		s, err = Inspect(path)
		if err != nil {
			return ReviewRecord{}, err
		}
		if err := requireCurrentHostAdmission(ctx, s); err != nil {
			return ReviewRecord{}, err
		}
	}
	if reviewImpactContextEnabled(s) {
		if err := maybeAdmitReviewImpactContext(ctx, path); err != nil {
			return ReviewRecord{}, err
		}
		s, err = Inspect(path)
		if err != nil {
			return ReviewRecord{}, err
		}
		if err := requireCurrentHostAdmission(ctx, s); err != nil {
			return ReviewRecord{}, err
		}
	}
	var invocation runtime.Invocation
	if correction, ok := scheduledRoleCorrectionFromContext(ctx); ok && correction.TaskID == "" {
		invocation = correction.Invocation
	} else {
		invocation, err = reviewInvocation(s)
	}
	if err != nil {
		return ReviewRecord{}, err
	}
	invocation, err = scheduledInvocationFromContext(ctx, taskscheduler.OperationReviewer, invocation)
	if err != nil {
		return ReviewRecord{}, err
	}
	if err := requireExecutableRoleRuntime(invocation.Profile); err != nil {
		return ReviewRecord{}, err
	}
	if err := requireModelAccessAdmissionAvailable(s, invocation); err != nil {
		return ReviewRecord{}, err
	}
	if s.Review != nil && s.Review.Invocation == invocation {
		return ReviewRecord{}, errors.New("review proposal already recorded; inspect its provenance rather than dispatch again")
	}
	if isExternalRuntime(invocation.Profile.Runtime) {
		result, err := executeExternalRole(ctx, path, s, invocation)
		if err != nil {
			return ReviewRecord{}, err
		}
		record := ReviewRecord{invocation, result}
		_, err = RecordReview(path, record)
		return finishReviewSemanticResult(ctx, path, record, err)
	}
	if invocation.Profile.Runtime == "provider-api" {
		result, err := executeDirectProviderRole(ctx, path, s, invocation, "")
		if err != nil {
			return ReviewRecord{}, err
		}
		record := ReviewRecord{invocation, result}
		_, err = RecordReview(path, record)
		return finishReviewSemanticResult(ctx, path, record, err)
	}
	if invocation.Profile.Runtime == "opencode-http" {
		result, err := executeOpenCodeRole(ctx, path, s, invocation, "")
		if err != nil {
			return ReviewRecord{}, err
		}
		record := ReviewRecord{invocation, result}
		_, err = RecordReview(path, record)
		return finishReviewSemanticResult(ctx, path, record, err)
	}
	expected, err := expectedReviewHostForInvocation(s, invocation)
	if err != nil {
		return ReviewRecord{}, err
	}
	result, err := executeReview(ctx, path, s, expected)
	if err != nil {
		return ReviewRecord{}, err
	}
	record := ReviewRecord{expected.Invocation, result}
	_, err = RecordReview(path, record)
	return finishReviewSemanticResult(ctx, path, record, err)
}

func finishReviewSemanticResult(ctx context.Context, path string, record ReviewRecord, err error) (ReviewRecord, error) {
	if !semanticOutputFailureOnly(err) {
		return record, err
	}
	s, inspectErr := Inspect(path)
	if inspectErr != nil {
		return record, inspectErr
	}
	if s.Creation.Execution == nil || s.Creation.Execution.SemanticCorrectionVersion != 1 {
		return record, err
	}
	if correctionErr := startRoleSemanticCorrection(ctx, path, record.Invocation, record.Result); correctionErr != nil {
		return record, correctionErr
	}
	return RunReview(ctx, path)
}

func executeReview(ctx context.Context, path string, s Snapshot, expected ReviewHostIntent) (result runtime.Result, err error) {
	hostTaskID := ""
	if correction, ok := scheduledRoleCorrectionFromContext(ctx); ok && correction.TaskID == "" && correction.ScheduledTaskID != "" {
		hostTaskID = correction.ScheduledTaskID
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return result, err
	}
	current, err := expectedReviewHostForInvocation(s, expected.Invocation)
	if err != nil {
		return result, err
	}
	if current != expected {
		return result, errors.New("review state changed before dispatch")
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
	runtimeLexical, err := selectRoleRuntimeLexical(ctx, path, s)
	if err != nil {
		return result, err
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
			return result, errors.New("review RI selection changed")
		}
		runtimeRI = &binding
	}
	before, err := observeCandidate(ctx, path, s)
	if err != nil {
		return result, err
	}
	if s.Candidate == nil || before != *s.Candidate {
		return result, errors.New("review base candidate mismatch")
	}
	currentHost := scheduledReviewHostState(s, hostTaskID)
	if currentHost == nil || currentHost.Intent != expected {
		if _, _, _, _, policyErr := codexRuntimeUsagePolicy(s, expected.Invocation.Profile.Role); policyErr != nil {
			return result, policyErr
		}
		if err := appendReviewHostTransition(path, hostTaskID, "review.host-intent", expected); err != nil {
			return result, err
		}
		s, err = Inspect(path)
		if err != nil {
			return result, err
		}
		currentHost = scheduledReviewHostState(s, hostTaskID)
	}
	l := expected.Launch
	if currentHost == nil {
		return result, errors.New("review host intent missing after admission")
	}
	if !currentHost.Ready {
		c := s.Creation.Config.Codex
		if err := safepath.Directory(c.StateRoot); err != nil {
			return result, err
		}
		if _, statErr := os.Stat(l.Root); os.IsNotExist(statErr) {
			if err := safepath.EnsureDirectory(c.StateRoot, s.RunID+"/review-"+expected.Invocation.ID); err != nil {
				return result, err
			}
			prepared, err := codexhost.Prepare(l.Root, l.Binary)
			if err != nil {
				return result, err
			}
			if prepared != l {
				return result, errors.New("review prepared host substitution")
			}
		} else if statErr != nil {
			return result, statErr
		}
		if err := l.Validate(); err != nil {
			return result, err
		}
		if err := appendReviewHostTransition(path, hostTaskID, "review.host-ready", expected); err != nil {
			return result, err
		}
	}
	runtimePath := filepath.Join(l.Root, "review.jsonl")
	state, readErr := codexruntime.Inspect(runtimePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return result, readErr
	}
	semanticUsagePending := false
	if readErr == nil && state.SemanticResult != nil && state.Result == nil {
		if s.Creation.Config.Version != 2 {
			return result, errors.New("retained semantic output lacks required usage accounting support")
		}
		_, result, _, err = observeSemanticUsagePending(ctx, path, runtimePath, expected.Invocation, "harness.review-result.v1")
		if err != nil {
			return runtime.Result{}, errors.Join(codexruntime.ErrUsageReconciliation, err)
		}
		semanticUsagePending = true
	}
	if readErr == nil && s.Creation.Config.Version == 2 && state.Result == nil && (state.TurnStatus == "failed" || state.TurnStatus == "interrupted") {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_, _, terminalErr := completeModelAccess(cleanupCtx, path, runtimePath, expected.Invocation, "harness.review-result.v1")
		cancel()
		return result, errors.Join(errors.New("review runtime is terminal without a result"), terminalErr)
	}
	if readErr != nil || state.Result == nil && !semanticUsagePending {
		usageBudget, requireLiveUsage, usageQualified, unlimitedTokens, policyErr := codexRuntimeUsagePolicy(s, expected.Invocation.Profile.Role)
		if policyErr != nil {
			return result, policyErr
		}
		a := &codexruntime.Adapter{JournalPath: runtimePath, Directory: filepath.Join(l.Root, "workspace"), Source: &s.Creation.Repository, Candidate: &codexruntime.CandidateBinding{Workspace: *s.Workspace, Candidate: before}, RI: runtimeRI, Lexical: runtimeLexical, UsageBudget: usageBudget, RequireLiveUsage: requireLiveUsage, UsageQualified: usageQualified, UnlimitedTokens: unlimitedTokens}
		tools := codexruntime.NewToolSession(ctx, a)
		defer tools.Close()
		h, err := startCodexRoleHost(ctx, l, s.Creation.Config.Codex, expected.Invocation.Profile, a.SourceTools(), tools.HandleTool)
		if err != nil {
			return result, err
		}
		defer func() { err = errors.Join(err, h.Close()) }()
		if err := appendReviewHostTransition(path, hostTaskID, "review.host-observed", h.Receipt); err != nil {
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
				_, retained, _, pendingErr := observeSemanticUsagePending(ctx, path, runtimePath, expected.Invocation, "harness.review-result.v1")
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
				_, _, terminalErr := completeModelAccess(cleanupCtx, path, runtimePath, expected.Invocation, "harness.review-result.v1")
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
	latestHost := scheduledReviewHostState(latest, hostTaskID)
	if latestHost == nil || latestHost.Intent != expected || !latestHost.Ready || latestHost.Receipt == nil {
		return result, errors.New("review host observation missing")
	}
	if state.Intent == nil || state.Intent.Invocation != expected.Invocation || state.Intent.Directory != filepath.Join(l.Root, "workspace") || (state.Result == nil && !semanticUsagePending) || state.Thread == nil || state.TurnStatus != "completed" || state.Source == nil || *state.Source != s.Creation.Repository {
		return result, errors.New("review runtime evidence incomplete or substituted")
	}
	if (state.RI == nil) != (runtimeRI == nil) || (runtimeRI != nil && *state.RI != *runtimeRI) {
		return result, errors.New("review runtime RI binding mismatch")
	}
	if state.Candidate == nil || state.Candidate.Workspace != *s.Workspace || state.Candidate.Candidate != before {
		return result, errors.New("review runtime candidate mismatch")
	}
	candidateID, err := before.ID()
	if err != nil {
		return result, err
	}
	if err := validateRoleLexicalState(s, runtimeLexical, state, candidateID); err != nil {
		return result, err
	}
	if err := state.Thread.Validate(expected.Invocation.Profile, filepath.Join(l.Root, "workspace")); err != nil {
		return result, err
	}
	resultValue := state.Result
	if semanticUsagePending {
		resultValue = &result
		if state.SemanticResult == nil || *state.SemanticResult != result {
			return runtime.Result{}, errors.New("retained review semantic result changed")
		}
	}
	if resultValue == nil {
		return runtime.Result{}, errors.New("review runtime result missing")
	}
	if err := runtime.ValidateResult(expected.Invocation, *resultValue, true); err != nil {
		return result, err
	}
	after, err := observeCandidate(ctx, path, s)
	if err != nil {
		return result, err
	}
	if after != before {
		return result, errors.New("review source changed during execution")
	}
	hash, err := canonical.Hash("harness.review-result.v1", *resultValue)
	if err != nil {
		return result, err
	}
	if s.Creation.Config.Version == 2 && !semanticUsagePending {
		var terminal ModelAccessTerminal
		latest, terminal, err = completeModelAccess(ctx, path, runtimePath, expected.Invocation, "harness.review-result.v1")
		if err != nil {
			return result, err
		}
		if terminal.Receipt.Status != "completed" || terminal.RuntimeJournalHead != head || terminal.Receipt.OutputHash != hash {
			return result, errors.New("review result differs from terminal model access receipt")
		}
	}
	receipt := ReviewRuntimeReceipt{InvocationID: expected.Invocation.ID, ThreadID: state.Thread.ThreadID, TurnID: state.TurnID, JournalHead: head, ResultHash: hash, UsagePending: semanticUsagePending}
	latestHost = scheduledReviewHostState(latest, hostTaskID)
	if latestHost == nil {
		return result, errors.New("review host receipt state unavailable")
	}
	if latestHost.RuntimeReceipt == nil {
		if err := appendReviewHostTransition(path, hostTaskID, "review.runtime-observed", receipt); err != nil {
			return result, err
		}
	} else if *latestHost.RuntimeReceipt != receipt {
		return result, errors.New("review runtime journal no longer matches recorded receipt")
	}
	return *resultValue, nil
}

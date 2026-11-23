package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/agenttree"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/taskscheduler"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

// RunExplorer executes or resumes a private read-only host for the exact question.
// Admission requires its runtime receipt and a still-current candidate.
func RunExplorer(ctx context.Context, path, question string) (_ ExplorerRecord, err error) {
	s, err := Inspect(path)
	if err != nil {
		return ExplorerRecord{}, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return ExplorerRecord{}, err
	}
	if taskContextEnabled(s) {
		if err := autonomousDispatchBlocked(s); err != nil {
			return ExplorerRecord{}, err
		}
		if err := maybeAdmitTaskContext(ctx, path, "explorer", question); err != nil {
			return ExplorerRecord{}, err
		}
		s, err = Inspect(path)
		if err != nil {
			return ExplorerRecord{}, err
		}
		if err := requireCurrentHostAdmission(ctx, s); err != nil {
			return ExplorerRecord{}, err
		}
	}
	invocation, err := explorerInvocation(s, question)
	if err != nil {
		return ExplorerRecord{}, err
	}
	invocation, err = scheduledExplorerInvocationFromContext(ctx, s, invocation)
	if err != nil {
		return ExplorerRecord{}, err
	}
	if err := requireModelAccessAdmissionAvailable(s, invocation); err != nil {
		return ExplorerRecord{}, err
	}
	if err := requireExecutableRoleRuntime(invocation.Profile); err != nil {
		return ExplorerRecord{}, err
	}
	if len(s.Explorations) >= s.Creation.Config.MaxExplorationRecords() {
		return ExplorerRecord{}, errors.New("exploration record bound reached")
	}
	for _, prior := range s.Explorations {
		if prior.Invocation == invocation {
			return ExplorerRecord{}, errors.New("exploration already recorded")
		}
	}
	var managed *agentDispatchBinding
	settled := false
	if turn := scheduledAgentTurn(ctx); turn != nil && managedExplorerDispatchEnabled(s) && (invocation.Profile.Runtime == "codex-app-server" || invocation.Profile.Runtime == "fake") {
		prior, hasPrior := s.AgentDispatch[invocation.ID]
		completed := hasPrior && prior.Observation != nil && prior.Observation.Status == agenttree.StatusSucceeded
		settled = completed
		if completed && invocation.Profile.Runtime == "codex-app-server" {
			runtimePath, pathErr := scheduledRuntimeJournal(s, invocation, taskscheduler.TaskSpec{Operation: taskscheduler.OperationExplorer}, turn)
			if pathErr != nil {
				return ExplorerRecord{}, pathErr
			}
			if err := requireScheduledOfflineRuntime(invocation.Profile.Runtime, runtimePath); err != nil {
				return ExplorerRecord{}, err
			}
		}
		var binding agentDispatchBinding
		var bindingErr error
		if completed {
			admissionID, idErr := prior.Admission.ID()
			if idErr != nil || prior.Admission.Invocation != invocation || prior.Admission.AgentTurn == nil || *prior.Admission.AgentTurn != *turn {
				return ExplorerRecord{}, errors.Join(errors.New("completed explorer admission differs"), idErr)
			}
			binding = agentDispatchBinding{JournalPath: path + ".agent-tree", ControllerPath: path, AdmissionID: admissionID, Node: prior.Admission.Node, InvocationID: invocation.ID, AgentTurn: turn}
		} else {
			binding, bindingErr = beginAgentDispatchForTurn(path, s, invocation, turn)
		}
		if bindingErr != nil {
			return ExplorerRecord{}, bindingErr
		}
		managed = &binding
		defer func() {
			if err != nil && !settled {
				err = errors.Join(err, finishAgentDispatchUnknown(binding))
			}
		}()
		if !completed {
			if err := markAgentDispatchRunning(managed); err != nil {
				return ExplorerRecord{}, err
			}
		}
		s, err = Inspect(path)
		if err != nil {
			return ExplorerRecord{}, err
		}
	}
	complete := func(result runtime.Result) (ExplorerRecord, error) {
		if managed != nil {
			hash, hashErr := canonical.Hash("harness.explorer-result.v1", result)
			if hashErr != nil {
				return ExplorerRecord{}, hashErr
			}
			if err := finishAgentDispatch(*managed, hash); err != nil {
				return ExplorerRecord{}, err
			}
			settled = true
		}
		record := ExplorerRecord{question, invocation, result}
		_, err := RecordExploration(path, record)
		return record, err
	}
	if invocation.Profile.Runtime == "fake" && s.Creation.Execution != nil && s.Creation.Execution.GraphEnabled() {
		result, err := runFakeExplorer(ctx, path, s, invocation, question)
		if err != nil {
			return ExplorerRecord{}, err
		}
		return complete(result)
	}
	if isExternalRuntime(invocation.Profile.Runtime) {
		result, err := executeExternalRole(ctx, path, s, invocation)
		if err != nil {
			return ExplorerRecord{}, err
		}
		return complete(result)
	}
	if invocation.Profile.Runtime == "provider-api" {
		result, err := executeDirectProviderRole(ctx, path, s, invocation, question)
		if err != nil {
			return ExplorerRecord{}, err
		}
		return complete(result)
	}
	if invocation.Profile.Runtime == "opencode-http" {
		result, err := executeOpenCodeRole(ctx, path, s, invocation, question)
		if err != nil {
			return ExplorerRecord{}, err
		}
		return complete(result)
	}
	expected, err := expectedExplorerHostForInvocation(s, question, invocation)
	if err != nil {
		return ExplorerRecord{}, err
	}
	result, err := executeExplorer(ctx, path, s, expected)
	if err != nil {
		return ExplorerRecord{}, err
	}
	return complete(result)
}

// runFakeExplorer synthesizes a deterministic bounded explorer result for the
// fixture fake runtime. It holds only a shared read lease, rechecks the exact
// candidate before/after, freezes RI/lexical selection, and returns a valid
// advisory Exploration (question as summary, no paths) bound to the exact
// invocation. No host is used, so parallel fake explorers overlap with
// separate receipt identities and an unchanged candidate.
func runFakeExplorer(ctx context.Context, path string, s Snapshot, invocation runtime.Invocation, question string) (result runtime.Result, err error) {
	if invocation.Profile.Runtime != "fake" {
		return result, errors.New("fake explorer invocation required")
	}
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	fresh, err := Inspect(path)
	if err != nil {
		return result, err
	}
	current, err := explorerInvocation(fresh, question)
	if err != nil {
		return result, err
	}
	// Resolve scheduled turn scoping if present (static graph claims have none).
	scoped, err := scheduledExplorerInvocationFromContext(ctx, fresh, current)
	if err != nil {
		return result, err
	}
	if scoped.ID != invocation.ID || scoped != invocation {
		return result, errors.New("explorer state changed before dispatch")
	}
	// Freeze/confirm RI/lexical selection without extra dispatch.
	if intelligence, err := roleRI(fresh); err != nil {
		return result, err
	} else if intelligence != nil {
		binding, err := SelectRuntimeRI(ctx, path)
		if err != nil {
			return result, err
		}
		if binding != intelligence.Binding {
			return result, errors.New("explorer RI selection changed")
		}
	}
	if lex, err := roleLexical(fresh); err != nil {
		return result, err
	} else if lex != nil {
		if _, err := selectRoleRuntimeLexical(ctx, path, fresh); err != nil {
			return result, err
		}
	}
	before, err := worktree.Fingerprint(ctx, *fresh.Workspace)
	if err != nil {
		return result, err
	}
	if fresh.Candidate == nil || before != *fresh.Candidate {
		return result, errors.New("explorer base candidate mismatch")
	}
	candidateID, err := before.ID()
	if err != nil {
		return result, err
	}
	summary := strings.TrimSpace(question)
	if summary == "" {
		return result, errors.New("exploration requires a bounded question")
	}
	if len(summary) > 8192 {
		summary = summary[:8192]
	}
	observation := Exploration{CandidateID: candidateID, Summary: summary, Paths: []string{}}
	var contextEnvelope workingContextTurnEnvelope
	if workingContextEnabled(fresh) && canonical.Decode([]byte(invocation.Input), &contextEnvelope) == nil && contextEnvelope.ContextVersion == 1 {
		observation.WorkingContextUpdate, err = canonical.Bytes(struct {
			ExpectedID   string `json:"expected_id"`
			ExpectedHash string `json:"expected_content_hash"`
			Content      string `json:"content"`
		}{contextEnvelope.ExpectedID, contextEnvelope.ExpectedHash, summary})
		if err != nil {
			return result, err
		}
	}
	body, err := canonical.Bytes(observation)
	if err != nil {
		return result, err
	}
	model := invocation.Profile.Model
	result = runtime.Result{Version: 1, InvocationID: invocation.ID, Requested: invocation.Profile, ObservedModel: &model, Output: string(body)}
	if err := runtime.ValidateResult(invocation, result, true); err != nil {
		return result, err
	}
	after, err := worktree.Fingerprint(ctx, *fresh.Workspace)
	if err != nil {
		return result, err
	}
	if after != before {
		return result, errors.New("explorer source changed during execution")
	}
	return result, nil
}

func executeExplorer(ctx context.Context, path string, s Snapshot, expected ExplorerHostIntent) (result runtime.Result, err error) {
	lease, err := worktree.AcquireRead(s.Workspace.Request)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return result, err
	}
	current, err := expectedExplorerHostForInvocation(s, expected.Question, expected.Invocation)
	if err != nil {
		return result, err
	}
	if current != expected {
		return result, errors.New("explorer state changed before dispatch")
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
			return result, errors.New("explorer RI selection changed")
		}
		runtimeRI = &binding
	}
	before, err := worktree.Fingerprint(ctx, *s.Workspace)
	if err != nil {
		return result, err
	}
	if s.Candidate == nil || before != *s.Candidate {
		return result, errors.New("explorer base candidate mismatch")
	}
	// Per-invocation state: every map lookup binds the exact invocation, no
	// sibling host is substituted. Legacy singleton is mirrored in the map.
	if run, ok := explorerRunForInvocation(s, expected.Invocation.ID); !ok || run.Intent != expected {
		if _, _, _, _, policyErr := codexRuntimeUsagePolicy(s, expected.Invocation.Profile.Role); policyErr != nil {
			return result, policyErr
		}
		if err := Append(path, "explorer.host-intent", expected); err != nil {
			return result, err
		}
		s, err = Inspect(path)
		if err != nil {
			return result, err
		}
		if run, ok := explorerRunForInvocation(s, expected.Invocation.ID); !ok || run.Intent != expected {
			return result, errors.New("explorer host intent missing after append")
		}
	}
	s, err = Inspect(path)
	if err != nil {
		return result, err
	}
	currentRun, ok := explorerRunForInvocation(s, expected.Invocation.ID)
	if !ok {
		return result, errors.New("explorer host intent missing")
	}
	l := expected.Launch
	if !currentRun.Ready {
		c := s.Creation.Config.Codex
		if err := safepath.Directory(c.StateRoot); err != nil {
			return result, err
		}
		if _, statErr := os.Stat(l.Root); os.IsNotExist(statErr) {
			if err := safepath.EnsureDirectory(c.StateRoot, s.RunID+"/explorer-"+expected.Invocation.ID); err != nil {
				return result, err
			}
			prepared, err := codexhost.Prepare(l.Root, l.Binary)
			if err != nil {
				return result, err
			}
			if prepared != l {
				return result, errors.New("explorer prepared host substitution")
			}
		} else if statErr != nil {
			return result, statErr
		}
		if err := l.Validate(); err != nil {
			return result, err
		}
		if err := Append(path, "explorer.host-ready", expected); err != nil {
			return result, err
		}
	}
	runtimePath := filepath.Join(l.Root, "explorer.jsonl")
	state, readErr := codexruntime.Inspect(runtimePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return result, readErr
	}
	if readErr == nil && s.Creation.Config.Version == 2 && state.Result == nil && (state.TurnStatus == "failed" || state.TurnStatus == "interrupted") {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_, _, terminalErr := completeModelAccess(cleanupCtx, path, runtimePath, expected.Invocation, "harness.explorer-result.v1")
		cancel()
		return result, errors.Join(errors.New("explorer runtime is terminal without a result"), terminalErr)
	}
	var pendingResult *runtime.Result
	usagePending := false
	if readErr == nil && s.Creation.Config.Version == 2 && state.SemanticResult != nil && state.Result == nil &&
		state.UsagePolicy != nil && state.UsagePolicy.Required && (state.UsageReceipt == nil || state.UsageReceipt.Coverage != "OBSERVED") {
		_, semantic, _, pendingErr := observeSemanticUsagePending(ctx, path, runtimePath, expected.Invocation, "harness.explorer-result.v1")
		if pendingErr != nil {
			return result, pendingErr
		}
		pendingResult = &semantic
		usagePending = true
	}
	if readErr != nil || state.Result == nil && !usagePending {
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
		if err := Append(path, "explorer.host-observed", h.Receipt); err != nil {
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
		if errors.Is(err, codexruntime.ErrUsageReconciliation) && s.Creation.Config.Version == 2 {
			_, semantic, _, pendingErr := observeSemanticUsagePending(ctx, path, runtimePath, expected.Invocation, "harness.explorer-result.v1")
			if pendingErr == nil {
				pendingResult = &semantic
				usagePending = true
				err = nil
			} else {
				return result, errors.Join(err, pendingErr)
			}
		}
		if err != nil {
			if s.Creation.Config.Version == 2 {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				_, _, terminalErr := completeModelAccess(cleanupCtx, path, runtimePath, expected.Invocation, "harness.explorer-result.v1")
				cancel()
				return result, errors.Join(err, terminalErr)
			}
			_, settleErr := reconcileExplorerCapacityFailures(path, s)
			return result, errors.Join(err, settleErr)
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
	if run, ok := explorerRunForInvocation(latest, expected.Invocation.ID); !ok || run.Intent != expected || !run.Ready || run.Receipt == nil {
		return result, errors.New("explorer host observation missing")
	}
	semanticResult := state.Result
	if usagePending {
		if state.SemanticResult == nil || pendingResult == nil || !sameCanonical(*state.SemanticResult, *pendingResult) {
			return result, errors.New("explorer semantic usage pending result is missing or changed")
		}
		semanticResult = state.SemanticResult
	}
	if state.Intent == nil || state.Intent.Invocation != expected.Invocation || state.Intent.Directory != filepath.Join(l.Root, "workspace") || semanticResult == nil || state.Thread == nil || state.TurnStatus != "completed" || state.Source == nil || *state.Source != s.Creation.Repository {
		return result, errors.New("explorer runtime evidence incomplete or substituted")
	}
	if (state.RI == nil) != (runtimeRI == nil) || (runtimeRI != nil && *state.RI != *runtimeRI) {
		return result, errors.New("explorer runtime RI binding mismatch")
	}
	if state.Candidate == nil || state.Candidate.Workspace != *s.Workspace || state.Candidate.Candidate != before {
		return result, errors.New("explorer runtime candidate mismatch")
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
	if err := runtime.ValidateResult(expected.Invocation, *semanticResult, true); err != nil {
		return result, err
	}
	after, err := worktree.Fingerprint(ctx, *s.Workspace)
	if err != nil {
		return result, err
	}
	if after != before {
		return result, errors.New("explorer source changed during execution")
	}
	hash, err := canonical.Hash("harness.explorer-result.v1", *semanticResult)
	if err != nil {
		return result, err
	}
	if s.Creation.Config.Version == 2 && usagePending {
		if err := requireModelAccessResult(latest, expected.Invocation, head, hash, state.Thread.ThreadID, state.TurnID, true); err != nil {
			return result, err
		}
	} else if s.Creation.Config.Version == 2 {
		var terminal ModelAccessTerminal
		latest, terminal, err = completeModelAccess(ctx, path, runtimePath, expected.Invocation, "harness.explorer-result.v1")
		if err != nil {
			return result, err
		}
		if terminal.Receipt.Status != "completed" || terminal.RuntimeJournalHead != head || terminal.Receipt.OutputHash != hash {
			return result, errors.New("explorer result differs from terminal model access receipt")
		}
	}
	receipt := ExplorerRuntimeReceipt{InvocationID: expected.Invocation.ID, ThreadID: state.Thread.ThreadID, TurnID: state.TurnID, JournalHead: head, ResultHash: hash, UsagePending: usagePending}
	if run, ok := explorerRunForInvocation(latest, expected.Invocation.ID); !ok || run.RuntimeReceipt == nil {
		if err := Append(path, "explorer.runtime-observed", receipt); err != nil {
			return result, err
		}
	} else if *run.RuntimeReceipt != receipt {
		return result, errors.New("explorer runtime journal no longer matches recorded receipt")
	}
	return *semanticResult, nil
}

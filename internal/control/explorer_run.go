package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexhost"
	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
	"harness.local/engorch/internal/taskscheduler"
	"harness.local/engorch/internal/worktree"
)

// RunExplorer executes or resumes a private read-only host for the exact question.
// Admission requires its runtime receipt and a still-current candidate.
func RunExplorer(ctx context.Context, path, question string) (ExplorerRecord, error) {
	s, err := Inspect(path)
	if err != nil {
		return ExplorerRecord{}, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return ExplorerRecord{}, err
	}
	invocation, err := explorerInvocation(s, question)
	if err != nil {
		return ExplorerRecord{}, err
	}
	invocation, err = scheduledInvocationFromContext(ctx, taskscheduler.OperationExplorer, invocation)
	if err != nil {
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
	if invocation.Profile.Runtime == "opencode-http" {
		result, err := executeOpenCodeRole(ctx, path, s, invocation, question)
		if err != nil {
			return ExplorerRecord{}, err
		}
		record := ExplorerRecord{question, invocation, result}
		_, err = RecordExploration(path, record)
		return record, err
	}
	expected, err := expectedExplorerHost(s, question)
	if err != nil {
		return ExplorerRecord{}, err
	}
	result, err := executeExplorer(ctx, path, s, expected)
	if err != nil {
		return ExplorerRecord{}, err
	}
	record := ExplorerRecord{question, expected.Invocation, result}
	_, err = RecordExploration(path, record)
	return record, err
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
	current, err := expectedExplorerHost(s, expected.Question)
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
	if s.ExplorerHost == nil || s.ExplorerHost.Intent != expected {
		if err := Append(path, "explorer.host-intent", expected); err != nil {
			return result, err
		}
		s, err = Inspect(path)
		if err != nil {
			return result, err
		}
	}
	l := expected.Launch
	if !s.ExplorerHost.Ready {
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
		_, _, terminalErr := completeModelAccess(context.Background(), path, runtimePath, expected.Invocation, "harness.explorer-result.v1")
		return result, errors.Join(errors.New("explorer runtime is terminal without a result"), terminalErr)
	}
	if readErr != nil || state.Result == nil {
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
		if err != nil {
			if s.Creation.Config.Version == 2 {
				_, _, terminalErr := completeModelAccess(context.Background(), path, runtimePath, expected.Invocation, "harness.explorer-result.v1")
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
	if latest.ExplorerHost == nil || latest.ExplorerHost.Intent != expected || !latest.ExplorerHost.Ready || latest.ExplorerHost.Receipt == nil {
		return result, errors.New("explorer host observation missing")
	}
	if state.Intent == nil || state.Intent.Invocation != expected.Invocation || state.Intent.Directory != filepath.Join(l.Root, "workspace") || state.Result == nil || state.Thread == nil || state.TurnStatus != "completed" || state.Source == nil || *state.Source != s.Creation.Repository {
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
	if err := runtime.ValidateResult(expected.Invocation, *state.Result, true); err != nil {
		return result, err
	}
	after, err := worktree.Fingerprint(ctx, *s.Workspace)
	if err != nil {
		return result, err
	}
	if after != before {
		return result, errors.New("explorer source changed during execution")
	}
	hash, err := canonical.Hash("harness.explorer-result.v1", *state.Result)
	if err != nil {
		return result, err
	}
	if s.Creation.Config.Version == 2 {
		var terminal ModelAccessTerminal
		latest, terminal, err = completeModelAccess(ctx, path, runtimePath, expected.Invocation, "harness.explorer-result.v1")
		if err != nil {
			return result, err
		}
		if terminal.Receipt.Status != "completed" || terminal.RuntimeJournalHead != head || terminal.Receipt.OutputHash != hash {
			return result, errors.New("explorer result differs from terminal model access receipt")
		}
	}
	receipt := ExplorerRuntimeReceipt{expected.Invocation.ID, state.Thread.ThreadID, state.TurnID, head, hash}
	if latest.ExplorerHost.RuntimeReceipt == nil {
		if err := Append(path, "explorer.runtime-observed", receipt); err != nil {
			return result, err
		}
	} else if *latest.ExplorerHost.RuntimeReceipt != receipt {
		return result, errors.New("explorer runtime journal no longer matches recorded receipt")
	}
	return *state.Result, nil
}

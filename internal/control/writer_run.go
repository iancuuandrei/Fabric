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
	invocation, err := writerInvocation(s)
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
	if s.WriterProposal != nil && s.WriterProposal.Invocation == invocation {
		return WriterRecord{}, errors.New("writer proposal already recorded; inspect its provenance rather than dispatch again")
	}
	if invocation.Profile.Runtime == "opencode-http" {
		result, err := executeOpenCodeRole(ctx, path, s, invocation, "")
		if err != nil {
			return WriterRecord{}, err
		}
		return RecordWriterProposal(ctx, path, invocation, result)
	}
	expected, err := expectedWriterHost(s)
	if err != nil {
		return WriterRecord{}, err
	}
	result, err := executeWriter(ctx, path, s, expected)
	if err != nil {
		return WriterRecord{}, err
	}
	return RecordWriterProposal(ctx, path, expected.Invocation, result)
}

func executeWriter(ctx context.Context, path string, s Snapshot, expected WriterHostIntent) (result runtime.Result, err error) {
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	s, err = Inspect(path)
	if err != nil {
		return result, err
	}
	current, err := expectedWriterHost(s)
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
	lexical, err := roleLexical(s)
	if err != nil {
		return result, err
	}
	if lexical != nil {
		binding, err := selectRuntimeLexicalLeased(ctx, path, lexical.Scope == "candidate", *s.Workspace)
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
	before, err := observeCandidate(ctx, path, s)
	if err != nil {
		return result, err
	}
	if s.Candidate == nil || before != *s.Candidate {
		return result, errors.New("writer base candidate mismatch")
	}
	if s.WriterHost == nil || s.WriterHost.Intent != expected {
		if err := Append(path, "writer.host-intent", expected); err != nil {
			return result, err
		}
		s, err = Inspect(path)
		if err != nil {
			return result, err
		}
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
		if err := Append(path, "writer.host-ready", expected); err != nil {
			return result, err
		}
	}
	runtimePath := filepath.Join(l.Root, "writer.jsonl")
	state, readErr := codexruntime.Inspect(runtimePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return result, readErr
	}
	if readErr == nil && s.Creation.Config.Version == 2 && state.Result == nil && (state.TurnStatus == "failed" || state.TurnStatus == "interrupted") {
		_, _, terminalErr := completeModelAccess(context.Background(), path, runtimePath, expected.Invocation, "harness.writer-result.v1")
		return result, errors.Join(errors.New("writer runtime is terminal without a result"), terminalErr)
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
		if err := Append(path, "writer.host-observed", h.Receipt); err != nil {
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
				_, _, terminalErr := completeModelAccess(context.Background(), path, runtimePath, expected.Invocation, "harness.writer-result.v1")
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
	if latest.WriterHost == nil || latest.WriterHost.Intent != expected || !latest.WriterHost.Ready || latest.WriterHost.Receipt == nil {
		return result, errors.New("writer host observation missing")
	}
	if state.Intent == nil || state.Intent.Invocation != expected.Invocation || state.Intent.Directory != filepath.Join(l.Root, "workspace") || state.Result == nil || state.Thread == nil || state.TurnStatus != "completed" || state.Source == nil || *state.Source != s.Creation.Repository {
		return result, errors.New("writer runtime evidence incomplete or substituted")
	}
	if (state.RI == nil) != (runtimeRI == nil) || (runtimeRI != nil && *state.RI != *runtimeRI) {
		return result, errors.New("writer runtime RI binding mismatch")
	}
	if state.Candidate == nil || state.Candidate.Workspace != *s.Workspace || state.Candidate.Candidate != before {
		return result, errors.New("writer runtime candidate mismatch")
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
	after, err := observeCandidate(ctx, path, s)
	if err != nil {
		return result, err
	}
	if after != before {
		return result, errors.New("writer source changed during execution")
	}
	hash, err := canonical.Hash("harness.writer-result.v1", *state.Result)
	if err != nil {
		return result, err
	}
	if s.Creation.Config.Version == 2 {
		var terminal ModelAccessTerminal
		latest, terminal, err = completeModelAccess(ctx, path, runtimePath, expected.Invocation, "harness.writer-result.v1")
		if err != nil {
			return result, err
		}
		if terminal.Receipt.Status != "completed" || terminal.RuntimeJournalHead != head || terminal.Receipt.OutputHash != hash {
			return result, errors.New("writer result differs from terminal model access receipt")
		}
	}
	receipt := WriterRuntimeReceipt{expected.Invocation.ID, state.Thread.ThreadID, state.TurnID, head, hash}
	if latest.WriterHost.RuntimeReceipt == nil {
		if err := Append(path, "writer.runtime-observed", receipt); err != nil {
			return result, err
		}
	} else if *latest.WriterHost.RuntimeReceipt != receipt {
		return result, errors.New("writer runtime journal no longer matches recorded receipt")
	}
	return *state.Result, nil
}

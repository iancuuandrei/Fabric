package control

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
)

// PlannerReceipt binds admitted planning output or a classified terminal
// capacity refusal to its exact runtime journal.
type PlannerReceipt struct {
	FailureCode         string `json:"failure_code,omitempty"`
	FailureResponseHash string `json:"failure_response_hash,omitempty"`
	InvocationID        string `json:"invocation_id"`
	ThreadID            string `json:"thread_id"`
	TurnID              string `json:"turn_id"`
	JournalHead         string `json:"journal_head"`
	ResultHash          string `json:"result_hash"`
	UsagePending        bool   `json:"usage_pending,omitempty"`
}

func expectedPlannerHost(s Snapshot) (codexhost.Launch, error) {
	c := s.Creation.Config.Codex
	if c == nil || s.Creation.Config.Planner.Runtime != "codex-app-server" {
		return codexhost.Launch{}, errors.New("Codex planner configuration required")
	}
	rel, err := filepath.Rel(s.Creation.Repository.Root, c.StateRoot)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return codexhost.Launch{}, errors.New("Codex state root must be outside the source repository")
	}
	root := filepath.Join(c.StateRoot, s.RunID)
	if len(s.PlannerCorrections) != 0 {
		root = filepath.Join(root, fmt.Sprintf("planner-correction-%d", len(s.PlannerCorrections)))
	}
	return codexhost.Expected(root, c.Executable, c.ExecutableHash)
}

func replayPlanner(s *Snapshot, e journal.Event) error {
	if s.State != "PLANNING" {
		return errors.New("planner host event outside planning")
	}
	expected, err := expectedPlannerHost(*s)
	if err != nil {
		return err
	}
	switch e.Kind {
	case "planning.host-intent":
		if s.PlannerHost != nil {
			return errors.New("duplicate planner host intent")
		}
		var l codexhost.Launch
		if err := canonical.Decode(e.Payload, &l); err != nil {
			return err
		}
		if l != expected {
			return errors.New("planner host intent substitution")
		}
		s.PlannerHost = &l
	case "planning.host-ready":
		if s.PlannerHost == nil || s.PlannerHostReady {
			return errors.New("host preparation transition rejected")
		}
		var l codexhost.Launch
		if err := canonical.Decode(e.Payload, &l); err != nil {
			return err
		}
		if l != expected {
			return errors.New("prepared host substitution")
		}
		s.PlannerHostReady = true
	case "planning.host-observed":
		if !s.PlannerHostReady || s.PlannerReceipt != nil {
			return errors.New("host observation transition rejected")
		}
		var receipt codexhost.Receipt
		if err := canonical.Decode(e.Payload, &receipt); err != nil {
			return err
		}
		if err := validateCodexHostReceipt(receipt, expected, s.Creation.Config.Codex); err != nil {
			return err
		}
		s.PlannerHostReceipt = &receipt
	case "planning.runtime-observed":
		if s.PlannerHostReceipt == nil || s.PlannerReceipt != nil {
			return errors.New("planner runtime receipt transition rejected")
		}
		var receipt PlannerReceipt
		if err := canonical.Decode(e.Payload, &receipt); err != nil {
			return err
		}
		i, err := plannerInvocationForSnapshot(*s)
		if err != nil {
			return err
		}
		if receipt.InvocationID != i.ID || receipt.ThreadID == "" || len(receipt.ThreadID) > 256 || receipt.TurnID == "" || len(receipt.TurnID) > 256 {
			return errors.New("planner runtime identity mismatch")
		}
		for _, h := range []string{receipt.JournalHead, receipt.ResultHash} {
			if err := safepath.RequireDigest(h); err != nil {
				return err
			}
		}
		if receipt.FailureCode != "" {
			if s.Creation.Config.Version != 1 || s.Creation.Config.Planner.Runtime != "codex-app-server" || len(s.PlannerCorrections) != 0 || s.Plan != nil || receipt.UsagePending || receipt.FailureCode != "serverOverloaded" {
				return errors.New("invalid planner capacity failure receipt")
			}
			if err := safepath.RequireDigest(receipt.FailureResponseHash); err != nil {
				return err
			}
			want, err := plannerCapacityReceiptHash(receipt)
			if err != nil || want != receipt.ResultHash {
				return errors.New("planner capacity failure proof hash mismatch")
			}
		} else if receipt.FailureResponseHash != "" {
			return errors.New("unexpected planner failure response hash")
		}
		if err := requireModelAccessResult(*s, i, receipt.JournalHead, receipt.ResultHash, receipt.ThreadID, receipt.TurnID, receipt.UsagePending); err != nil {
			return err
		}
		s.PlannerReceipt = &receipt
	}
	return nil
}

// ResumePlanning continues the selected runtime under an exclusive planning marker.
// A Codex runtime journal is observed before dispatch; unknown turns are never resent.
func ResumePlanning(ctx context.Context, path string) (snapshot Snapshot, err error) {
	s, err := Inspect(path)
	if err != nil {
		return s, err
	}
	if s.State != "OBJECTIVE" && s.State != "PLANNING" {
		return s, nil
	}
	if s.PlannerReceipt != nil && s.PlannerReceipt.FailureCode == "serverOverloaded" {
		if err := validatePlannerCapacityFailureRuntime(s, path); err != nil {
			return s, err
		}
		return s, ErrPlannerCapacityRefused
	}
	if err := preflightInitialVerification(s); err != nil {
		return s, err
	}
	if err := requireCurrentHostAdmission(ctx, s); err != nil {
		return s, err
	}
	lock, err := os.OpenFile(path+".planning", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return s, err
	}
	if err := lock.Close(); err != nil {
		return s, err
	}
	defer func() { err = errors.Join(err, os.Remove(path+".planning")) }()
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if s.State == "OBJECTIVE" && plannerContextEnabled(s) && s.PlannerContext == nil {
		if _, err := AdmitPlannerContext(ctx, path); err != nil {
			return s, err
		}
		s, err = Inspect(path)
		if err != nil {
			return s, err
		}
	}
	if s.State == "OBJECTIVE" && plannerGoContextEnabled(s) && s.PlannerGoContext == nil {
		if _, err := AdmitPlannerGoContext(ctx, path); err != nil {
			return s, err
		}
		s, err = Inspect(path)
		if err != nil {
			return s, err
		}
	}
	if s.State == "OBJECTIVE" {
		if err := Append(path, "planning.started", struct{}{}); err != nil {
			return s, err
		}
		s, err = Inspect(path)
		if err != nil {
			return s, err
		}
	}
	if s.State != "PLANNING" {
		return s, nil
	}
	i, err := plannerInvocationForSnapshot(s)
	if err != nil {
		return s, err
	}
	if err := requireModelAccessAdmissionAvailable(s, i); err != nil {
		return s, err
	}
	if i.Profile.Runtime == "fake" {
		if s.Creation.Config.Version == 2 {
			if s.PlannerAccess != nil {
				return s, errors.New("planner invocation unresolved; automatic retry denied")
			}
			intent, err := expectedPlanningAccess(s)
			if err != nil {
				return s, err
			}
			if err := Append(path, "planning.access-intent", intent); err != nil {
				return s, err
			}
		}
		f := &runtime.Fake{}
		defer f.Close()
		r, err := f.Execute(ctx, i)
		if err != nil {
			return s, err
		}
		if err := Append(path, "plan.recorded", r); err != nil {
			return s, err
		}
		return Inspect(path)
	}
	if isExternalRuntime(i.Profile.Runtime) {
		r, err := executeExternalRole(ctx, path, s, i)
		if err != nil {
			return s, err
		}
		if err := Append(path, "plan.recorded", r); err != nil {
			return s, err
		}
		return Inspect(path)
	}
	if i.Profile.Runtime == "provider-api" {
		if s.PlannerProvider == nil {
			_, receipt, err := executeDirectProvider(ctx, path, s.Creation.Config, s.RunID, i)
			if err != nil {
				return s, err
			}
			if err := Append(path, "planning.provider-observed", receipt); err != nil {
				return s, err
			}
			s, err = Inspect(path)
			if err != nil {
				return s, err
			}
		}
		if s.PlannerProvider == nil {
			return s, errors.New("direct planner receipt unavailable")
		}
		if err := settleProviderDispatchTaskPool(path, s.Creation.Config, s.RunID, i, *s.PlannerProvider); err != nil {
			return s, err
		}
		if err := Append(path, "plan.recorded", s.PlannerProvider.Result); err != nil {
			return s, err
		}
		return Inspect(path)
	}
	if i.Profile.Runtime == "opencode-http" {
		if s.PlannerProvider == nil {
			_, receipt, err := executeOpenCodeProvider(ctx, path, s, i, nil)
			if err != nil {
				return s, err
			}
			if err := Append(path, "planning.provider-observed", receipt); err != nil {
				return s, err
			}
			s, err = Inspect(path)
			if err != nil {
				return s, err
			}
		}
		if s.PlannerProvider == nil {
			return s, errors.New("OpenCode planner receipt unavailable")
		}
		if err := settleProviderDispatchTaskPool(path, s.Creation.Config, s.RunID, i, *s.PlannerProvider); err != nil {
			return s, err
		}
		if err := Append(path, "plan.recorded", s.PlannerProvider.Result); err != nil {
			return s, err
		}
		return Inspect(path)
	}
	if s.Creation.Config.Version == 2 {
		intent, err := deriveModelAccessIntent(s, i, 1)
		if err != nil {
			return s, err
		}
		if err := validateCodexSubscriptionAccess(s, intent); err != nil {
			return s, err
		}
	}
	l, err := expectedPlannerHost(s)
	if err != nil {
		return s, err
	}
	if s.PlannerHost == nil {
		if _, _, _, _, policyErr := codexRuntimeUsagePolicy(s, i.Profile.Role); policyErr != nil {
			return s, policyErr
		}
		if err := Append(path, "planning.host-intent", l); err != nil {
			return s, err
		}
	}
	if !s.PlannerHostReady {
		if err := safepath.Directory(s.Creation.Config.Codex.StateRoot); err != nil {
			return s, err
		}
		if _, statErr := os.Stat(l.Root); os.IsNotExist(statErr) {
			if err := safepath.EnsureDirectory(s.Creation.Config.Codex.StateRoot, s.RunID); err != nil {
				return s, err
			}
			prepared, err := codexhost.Prepare(l.Root, l.Binary)
			if err != nil {
				return s, err
			}
			if prepared != l {
				return s, errors.New("prepared executable differs from pinned configuration")
			}
		} else if statErr != nil {
			return s, statErr
		}
		if err := l.Validate(); err != nil {
			return s, err
		}
		if err := Append(path, "planning.host-ready", l); err != nil {
			return s, err
		}
	}
	runtimePath := filepath.Join(l.Root, "planner.jsonl")
	state, readErr := codexruntime.Inspect(runtimePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return s, readErr
	}
	if readErr == nil && state.TurnStatus == "failed" {
		observed, ok, observeErr := observePlannerCapacityFailure(path, s, l, i, runtimePath)
		if observeErr != nil {
			return s, observeErr
		}
		if ok {
			return observed, ErrPlannerCapacityRefused
		}
	}
	if readErr == nil && s.Creation.Config.Version == 2 && state.Result == nil && (state.TurnStatus == "failed" || state.TurnStatus == "interrupted") {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		_, _, terminalErr := completeModelAccess(cleanupCtx, path, runtimePath, i, "harness.planner-result.v1")
		cancel()
		return s, errors.Join(errors.New("planner runtime is terminal without a result"), terminalErr)
	}
	var pendingResult *runtime.Result
	usagePending := false
	if readErr == nil && s.Creation.Config.Version == 2 && state.SemanticResult != nil && state.Result == nil &&
		state.UsagePolicy != nil && state.UsagePolicy.Required && (state.UsageReceipt == nil || state.UsageReceipt.Coverage != "OBSERVED") {
		_, semantic, _, pendingErr := observeSemanticUsagePending(ctx, path, runtimePath, i, "harness.planner-result.v1")
		if pendingErr != nil {
			return s, pendingErr
		}
		pendingResult = &semantic
		usagePending = true
	}
	if readErr != nil || state.Result == nil && !usagePending {
		usageBudget, requireLiveUsage, usageQualified, unlimitedTokens, policyErr := codexRuntimeUsagePolicy(s, i.Profile.Role)
		if policyErr != nil {
			return s, policyErr
		}
		a := &codexruntime.Adapter{JournalPath: runtimePath, Directory: filepath.Join(l.Root, "workspace"), Source: &s.Creation.Repository, UsageBudget: usageBudget, RequireLiveUsage: requireLiveUsage, UsageQualified: usageQualified, UnlimitedTokens: unlimitedTokens}
		tools := codexruntime.NewToolSession(ctx, a)
		defer tools.Close()
		h, err := startCodexRoleHost(ctx, l, s.Creation.Config.Codex, i.Profile, a.SourceTools(), tools.HandleTool)
		if err != nil {
			return s, err
		}
		defer h.Close()
		if err := Append(path, "planning.host-observed", h.Receipt); err != nil {
			return s, err
		}
		if err := h.LoginChatGPT(ctx, s.Creation.Config.Codex.AuthSource); err != nil {
			return s, err
		}
		a.Client = h.Client
		if s.Creation.Config.Version == 2 {
			var admission ModelAccessState
			s, admission, err = ensureModelAccessIntent(path, s, i)
			if err != nil {
				return s, err
			}
			if err := requireModelAccessActive(path, s, admission); err != nil {
				return s, err
			}
		}
		if state.Intent == nil {
			_, err = a.Execute(ctx, i)
		} else {
			_, err = a.Resume(ctx, i.ID)
		}
		if errors.Is(err, codexruntime.ErrUsageReconciliation) && s.Creation.Config.Version == 2 {
			_, semantic, _, pendingErr := observeSemanticUsagePending(ctx, path, runtimePath, i, "harness.planner-result.v1")
			if pendingErr == nil {
				pendingResult = &semantic
				usagePending = true
				err = nil
			} else {
				return s, errors.Join(err, pendingErr)
			}
		}
		if err != nil {
			observed, ok, capacityErr := observePlannerCapacityFailure(path, s, l, i, runtimePath)
			if capacityErr != nil {
				return s, errors.Join(err, capacityErr)
			}
			if ok {
				return observed, ErrPlannerCapacityRefused
			}
			if s.Creation.Config.Version == 2 {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				_, _, terminalErr := completeModelAccess(cleanupCtx, path, runtimePath, i, "harness.planner-result.v1")
				cancel()
				return s, errors.Join(err, terminalErr)
			}
			return s, err
		}
	}
	state, head, err := codexruntime.InspectWithHead(runtimePath)
	if err != nil {
		return s, err
	}
	result := state.Result
	if usagePending {
		if state.SemanticResult == nil || pendingResult == nil || !sameCanonical(*state.SemanticResult, *pendingResult) {
			return s, errors.New("planner semantic usage pending result is missing or changed")
		}
		result = state.SemanticResult
	}
	if state.Intent == nil || state.Intent.Invocation != i || state.Intent.Directory != filepath.Join(l.Root, "workspace") || result == nil || state.Thread == nil || state.TurnStatus != "completed" || state.Source == nil || *state.Source != s.Creation.Repository {
		return s, errors.New("planner runtime evidence incomplete")
	}
	if err := state.Thread.Validate(i.Profile, filepath.Join(l.Root, "workspace")); err != nil {
		return s, err
	}
	if head == "" {
		return s, errors.New("planner runtime journal head unavailable")
	}
	hash, err := canonical.Hash("harness.planner-result.v1", *result)
	if err != nil {
		return s, err
	}
	receipt := PlannerReceipt{InvocationID: i.ID, ThreadID: state.Thread.ThreadID, TurnID: state.TurnID, JournalHead: head, ResultHash: hash, UsagePending: usagePending}
	if s.Creation.Config.Version == 2 {
		if usagePending {
			err = requireModelAccessResult(s, i, head, hash, state.Thread.ThreadID, state.TurnID, true)
		} else {
			var terminal ModelAccessTerminal
			s, terminal, err = completeModelAccess(ctx, path, runtimePath, i, "harness.planner-result.v1")
			if err == nil && (terminal.Receipt.Status != "completed" || terminal.RuntimeJournalHead != receipt.JournalHead || terminal.Receipt.OutputHash != receipt.ResultHash) {
				err = errors.New("planner result differs from terminal model access receipt")
			}
		}
		if err != nil {
			return s, err
		}
	}
	s, err = Inspect(path)
	if err != nil {
		return s, err
	}
	if s.PlannerReceipt == nil {
		if err := Append(path, "planning.runtime-observed", receipt); err != nil {
			return s, err
		}
	} else if *s.PlannerReceipt != receipt {
		return s, errors.New("planner receipt no longer matches runtime journal")
	}
	if err := Append(path, "plan.recorded", *result); err != nil {
		return s, err
	}
	return Inspect(path)
}

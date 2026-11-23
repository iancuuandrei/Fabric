package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/iancuuandrei/fabric/internal/candidatetools"
	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/codexusage"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/writercontract"
)

// ErrSemanticUsagePending reports a known completed provider turn whose
// required usage accounting is unresolved. It blocks later provider admission
// without claiming the completed semantic effect is UNKNOWN.
var ErrSemanticUsagePending = errors.New("completed semantic output has unresolved required usage")

// ModelAccessSemanticPending binds a completed semantic result to its exact
// runtime journal while leaving the token reservation unsettled.
type ModelAccessSemanticPending struct {
	RuntimeInvocationID string                         `json:"runtime_invocation_id"`
	RuntimeJournalHead  string                         `json:"runtime_journal_head"`
	ResultDomain        string                         `json:"result_domain"`
	ResultHash          string                         `json:"result_hash"`
	ThreadID            string                         `json:"thread_id"`
	TurnID              string                         `json:"turn_id"`
	Source              repository.Identity            `json:"source"`
	CandidateBinding    *codexruntime.CandidateBinding `json:"candidate_binding,omitempty"`
	UsageFailure        string                         `json:"usage_failure"`
}

// observeSemanticUsagePending validates and persists the exact semantic result
// retained by the runtime when required usage accounting is missing. It does
// not settle or complete the access reservation.
func observeSemanticUsagePending(ctx context.Context, controllerPath, runtimePath string, invocation runtime.Invocation, resultDomain string) (Snapshot, runtime.Result, ModelAccessSemanticPending, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, runtime.Result{}, ModelAccessSemanticPending{}, err
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		return s, runtime.Result{}, ModelAccessSemanticPending{}, err
	}
	if s.Creation.Config.Version != 2 || invocation.Profile.Runtime != "codex-app-server" {
		return s, runtime.Result{}, ModelAccessSemanticPending{}, errors.New("semantic usage pending requires Codex access policy v2")
	}
	if err := invocation.Validate(); err != nil {
		return s, runtime.Result{}, ModelAccessSemanticPending{}, err
	}
	if expected, currentErr := currentModelInvocation(s, invocation.ID); currentErr != nil || expected != invocation {
		return s, runtime.Result{}, ModelAccessSemanticPending{}, errors.Join(errors.New("semantic usage pending invocation is not current controller work"), currentErr)
	}
	domain, err := semanticResultDomain(invocation.Profile.Role)
	if err != nil || domain != resultDomain {
		return s, runtime.Result{}, ModelAccessSemanticPending{}, errors.Join(errors.New("semantic usage pending result domain mismatch"), err)
	}
	index := modelAccessIndex(s, invocation.ID)
	if index < 0 || s.ModelAccess[index].Terminal != nil {
		return s, runtime.Result{}, ModelAccessSemanticPending{}, errors.New("semantic usage pending lacks active exact model access intent")
	}
	state, head, err := codexruntime.InspectWithHead(runtimePath)
	if err != nil {
		return s, runtime.Result{}, ModelAccessSemanticPending{}, err
	}
	pending, result, err := semanticUsagePendingFromRuntime(s, state, head, invocation, resultDomain)
	if err != nil {
		return s, runtime.Result{}, ModelAccessSemanticPending{}, err
	}
	if existing := s.ModelAccess[index].SemanticPending; existing != nil {
		if !sameCanonical(*existing, pending) {
			return s, runtime.Result{}, ModelAccessSemanticPending{}, errors.New("recorded semantic usage pending proof changed")
		}
		return s, result, pending, nil
	}
	if err := appendModelAccessSemanticPending(ctx, controllerPath, runtimePath, invocation, resultDomain, pending); err != nil {
		return s, runtime.Result{}, ModelAccessSemanticPending{}, err
	}
	s, err = Inspect(controllerPath)
	if err != nil {
		return s, runtime.Result{}, ModelAccessSemanticPending{}, err
	}
	index = modelAccessIndex(s, invocation.ID)
	if index < 0 || s.ModelAccess[index].SemanticPending == nil || !sameCanonical(*s.ModelAccess[index].SemanticPending, pending) {
		return s, runtime.Result{}, ModelAccessSemanticPending{}, errors.New("semantic usage pending proof missing after append")
	}
	return s, result, pending, nil
}

// reconcileUnrecordedSemanticUsagePending materializes only a completed
// semantic result already present in the exact runtime journal for an
// unresolved controller access invocation. It performs no host/RPC action.
func reconcileUnrecordedSemanticUsagePending(ctx context.Context, controllerPath string, snapshot Snapshot) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return snapshot, nil
	}
	if snapshot.Creation.Config.Version != 2 {
		return snapshot, nil
	}
	for _, admission := range snapshot.ModelAccess {
		if admission.Terminal != nil || admission.SemanticPending != nil || admission.Intent.Route.Runtime != "codex-app-server" {
			continue
		}
		invocation, err := currentModelInvocation(snapshot, admission.RuntimeInvocationID)
		if err != nil || invocation.Profile.Runtime != "codex-app-server" {
			continue
		}
		budget, requireLive, qualified, _, policyErr := codexRuntimeUsagePolicy(snapshot, invocation.Profile.Role)
		if policyErr != nil || !(requireLive || budget > 0) || !qualified {
			continue
		}
		runtimePath, ok := runtimeJournalForSemanticInvocation(snapshot, invocation)
		if !ok {
			continue
		}
		state, _, inspectErr := codexruntime.InspectWithHead(runtimePath)
		if inspectErr != nil {
			if os.IsNotExist(inspectErr) {
				continue
			}
			return snapshot, errors.Join(ErrAutonomousReconciliation, inspectErr)
		}
		if state.SemanticResult == nil || state.Result != nil || state.TurnStatus != "completed" || state.UsagePolicy == nil || !state.UsagePolicy.Required || state.UsageReceipt != nil && state.UsageReceipt.Coverage == "OBSERVED" {
			continue
		}
		domain, err := semanticResultDomain(invocation.Profile.Role)
		if err != nil {
			return snapshot, err
		}
		updated, _, _, err := observeSemanticUsagePending(ctx, controllerPath, runtimePath, invocation, domain)
		if err != nil {
			return snapshot, errors.Join(ErrAutonomousReconciliation, err)
		}
		snapshot = updated
	}
	return snapshot, nil
}

func runtimeJournalForSemanticInvocation(s Snapshot, invocation runtime.Invocation) (string, bool) {
	root := ""
	switch invocation.Profile.Role {
	case "planner":
		if s.PlannerHost != nil && s.PlannerHostReady {
			root = s.PlannerHost.Root
			return filepath.Join(root, "planner.jsonl"), filepath.IsAbs(root)
		}
	case "explorer":
		if s.ExplorerHost != nil && s.ExplorerHost.Intent.Invocation.ID == invocation.ID {
			root = s.ExplorerHost.Intent.Launch.Root
			return filepath.Join(root, "explorer.jsonl"), filepath.IsAbs(root)
		}
		if run, ok := explorerRunForInvocation(s, invocation.ID); ok {
			root = run.Intent.Launch.Root
			return filepath.Join(root, "explorer.jsonl"), filepath.IsAbs(root)
		}
	case "writer", "fixer":
		if s.WriterHost != nil && s.WriterHost.Intent.Invocation.ID == invocation.ID {
			root = s.WriterHost.Intent.Launch.Root
			return filepath.Join(root, "writer.jsonl"), filepath.IsAbs(root)
		}
		for _, host := range s.GraphWriterHosts {
			if host.Intent.Invocation.ID == invocation.ID {
				root = host.Intent.Launch.Root
				return filepath.Join(root, "writer.jsonl"), filepath.IsAbs(root)
			}
		}
	case "reviewer":
		if s.ReviewHost != nil && s.ReviewHost.Intent.Invocation.ID == invocation.ID {
			root = s.ReviewHost.Intent.Launch.Root
			return filepath.Join(root, "review.jsonl"), filepath.IsAbs(root)
		}
		for _, host := range s.ScheduledReviewHosts {
			if host.Intent.Invocation.ID == invocation.ID {
				root = host.Intent.Launch.Root
				return filepath.Join(root, "review.jsonl"), filepath.IsAbs(root)
			}
		}
	}
	return "", false
}

func semanticUsagePendingFromRuntime(s Snapshot, state codexruntime.State, head string, invocation runtime.Invocation, resultDomain string) (ModelAccessSemanticPending, runtime.Result, error) {
	if safepath.RequireDigest(head) != nil || state.Intent == nil || state.Intent.Invocation != invocation ||
		state.TurnStatus != "completed" || state.SemanticResult == nil || state.Result != nil ||
		state.Thread == nil || state.TurnID == "" || state.SemanticResult.InvocationID != invocation.ID ||
		state.Source == nil || *state.Source != s.Creation.Repository {
		return ModelAccessSemanticPending{}, runtime.Result{}, errors.New("semantic usage pending runtime identity is incomplete or substituted")
	}
	if err := state.Thread.Validate(invocation.Profile, state.Intent.Directory); err != nil {
		return ModelAccessSemanticPending{}, runtime.Result{}, err
	}
	if state.UsagePolicy == nil || !state.UsagePolicy.Required || state.UsagePolicy.ThreadID != state.Thread.ThreadID ||
		state.UsageReceipt != nil && state.UsageReceipt.Coverage == "OBSERVED" || state.UsageFailure == "BUDGET_EXHAUSTED" {
		return ModelAccessSemanticPending{}, runtime.Result{}, errors.New("runtime does not show unresolved required usage")
	}
	switch state.UsageFailure {
	case "", "USAGE_MISSING", "USAGE_HYDRATION_MISSING":
	default:
		return ModelAccessSemanticPending{}, runtime.Result{}, errors.New("runtime usage failure is not an eligible missing-usage state")
	}
	budget, requireLive, qualified, unlimited, err := codexRuntimeUsagePolicy(s, invocation.Profile.Role)
	if err != nil {
		return ModelAccessSemanticPending{}, runtime.Result{}, err
	}
	zero := int64(0)
	expectedPolicy := codexruntime.UsagePolicy{Version: 1, UnlimitedTokens: unlimited, ThreadID: state.Thread.ThreadID,
		Baseline: codexusage.TokenUsage{CacheWriteInputTokens: &zero}, Budget: budget,
		Required: requireLive || budget > 0, Qualified: qualified, Origin: "FRESH_THREAD"}
	if !sameCanonical(*state.UsagePolicy, expectedPolicy) {
		return ModelAccessSemanticPending{}, runtime.Result{}, errors.New("runtime usage policy differs from controller reservation")
	}
	result := *state.SemanticResult
	if err := runtime.ValidateResult(invocation, result, true); err != nil {
		return ModelAccessSemanticPending{}, runtime.Result{}, err
	}
	resultHash, err := canonical.Hash(resultDomain, result)
	if err != nil {
		return ModelAccessSemanticPending{}, runtime.Result{}, err
	}
	expectedCandidate, err := expectedSemanticCandidateBinding(s, invocation)
	if err != nil || !sameOptionalCandidateBinding(state.Candidate, expectedCandidate) {
		return ModelAccessSemanticPending{}, runtime.Result{}, errors.Join(errors.New("semantic usage pending candidate binding substituted"), err)
	}
	return ModelAccessSemanticPending{
		RuntimeInvocationID: invocation.ID,
		RuntimeJournalHead:  head,
		ResultDomain:        resultDomain,
		ResultHash:          resultHash,
		ThreadID:            state.Thread.ThreadID,
		TurnID:              state.TurnID,
		Source:              s.Creation.Repository,
		CandidateBinding:    expectedCandidate,
		UsageFailure:        state.UsageFailure,
	}, result, nil
}

func sameOptionalCandidateBinding(left, right *codexruntime.CandidateBinding) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return sameCanonical(*left, *right)
}

func expectedSemanticCandidateBinding(s Snapshot, invocation runtime.Invocation) (*codexruntime.CandidateBinding, error) {
	if invocation.Profile.Role == "planner" {
		return nil, nil
	}
	if invocation.Profile.Role == "writer" || invocation.Profile.Role == "fixer" {
		for taskID, host := range s.GraphWriterHosts {
			if host.Intent.Invocation.ID != invocation.ID {
				continue
			}
			if isolated, ok := s.GraphIsolations[taskID]; ok {
				if isolated.Binding == nil || isolated.Candidate == nil {
					return nil, errors.New("semantic usage pending isolated writer binding is incomplete")
				}
				return &codexruntime.CandidateBinding{Workspace: *isolated.Binding, Candidate: *isolated.Candidate, AnchorValidationVersion: expectedAnchorValidationVersion(s, invocation)}, nil
			}
		}
	}
	if s.Workspace == nil || s.Candidate == nil {
		return nil, errors.New("semantic usage pending requires current candidate and workspace")
	}
	return &codexruntime.CandidateBinding{Workspace: *s.Workspace, Candidate: *s.Candidate, AnchorValidationVersion: expectedAnchorValidationVersion(s, invocation)}, nil
}

func expectedAnchorValidationVersion(s Snapshot, invocation runtime.Invocation) int {
	if (invocation.Profile.Role == "writer" || invocation.Profile.Role == "fixer") && writercontract.IsAnchoredEdits(s.Creation.Config.WriterContract) {
		return candidatetools.AnchorValidationVersion
	}
	return 0
}

func semanticResultDomain(role string) (string, error) {
	switch role {
	case "planner":
		return "harness.planner-result.v1", nil
	case "explorer":
		return "harness.explorer-result.v1", nil
	case "writer", "fixer":
		return "harness.writer-result.v1", nil
	case "reviewer":
		return "harness.review-result.v1", nil
	default:
		return "", errors.New("unsupported semantic usage role")
	}
}

func validateModelAccessSemanticPending(s Snapshot, invocation runtime.Invocation, pending ModelAccessSemanticPending) error {
	index := modelAccessIndex(s, invocation.ID)
	if invocation.Profile.Runtime != "codex-app-server" || index < 0 || s.ModelAccess[index].Terminal != nil || pending.RuntimeInvocationID != invocation.ID ||
		pending.Source != s.Creation.Repository || pending.ThreadID == "" || len(pending.ThreadID) > 256 ||
		pending.TurnID == "" || len(pending.TurnID) > 256 || safepath.RequireDigest(pending.RuntimeJournalHead) != nil ||
		safepath.RequireDigest(pending.ResultHash) != nil {
		return errors.New("invalid semantic usage pending identity")
	}
	domain, err := semanticResultDomain(invocation.Profile.Role)
	if err != nil || pending.ResultDomain != domain {
		return errors.Join(errors.New("semantic usage pending domain mismatch"), err)
	}
	if pending.UsageFailure != "" && pending.UsageFailure != "USAGE_MISSING" && pending.UsageFailure != "USAGE_HYDRATION_MISSING" {
		return errors.New("invalid semantic usage pending reason")
	}
	expectedCandidate, err := expectedSemanticCandidateBinding(s, invocation)
	if err != nil || !sameOptionalCandidateBinding(pending.CandidateBinding, expectedCandidate) {
		return errors.Join(errors.New("semantic usage pending candidate binding does not match controller state"), err)
	}
	budget, requireLive, qualified, _, err := codexRuntimeUsagePolicy(s, invocation.Profile.Role)
	if err != nil {
		return err
	}
	if !(requireLive || budget > 0) || !qualified {
		return errors.New("semantic usage pending is inconsistent with controller budget policy")
	}
	return nil
}

func appendModelAccessSemanticPending(ctx context.Context, controllerPath, runtimePath string, invocation runtime.Invocation, resultDomain string, expected ModelAccessSemanticPending) error {
	_, err := journal.Append(controllerPath, "model.access-semantic-pending", expected, func(events []journal.Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(events) == 0 || events[len(events)-1].Kind != "model.access-semantic-pending" {
			return errors.New("semantic usage pending append candidate missing")
		}
		var proposed ModelAccessSemanticPending
		if err := canonical.Decode(events[len(events)-1].Payload, &proposed); err != nil || !sameCanonical(proposed, expected) {
			return errors.Join(errors.New("semantic usage pending append candidate changed"), err)
		}
		s, err := Replay(events[:len(events)-1])
		if err != nil {
			return err
		}
		if err := validateControllerJournalPath(controllerPath, s.Creation, s.RunID); err != nil {
			return err
		}
		if s.Creation.Config.Version != 2 || invocation.Profile.Runtime != "codex-app-server" {
			return errors.New("semantic usage pending requires Codex access policy v2")
		}
		current, currentErr := currentModelInvocation(s, invocation.ID)
		if currentErr != nil || current != invocation {
			return errors.Join(errors.New("semantic usage pending invocation changed before append"), currentErr)
		}
		index := modelAccessIndex(s, invocation.ID)
		if index < 0 || s.ModelAccess[index].Terminal != nil || s.ModelAccess[index].SemanticPending != nil {
			return errors.New("semantic usage pending transition changed before append")
		}
		state, head, err := codexruntime.InspectWithHead(runtimePath)
		if err != nil {
			return err
		}
		actual, _, err := semanticUsagePendingFromRuntime(s, state, head, invocation, resultDomain)
		if err != nil {
			return err
		}
		if !sameCanonical(actual, expected) {
			return errors.New("semantic usage pending runtime evidence changed before append")
		}
		return nil
	})
	return err
}

func hasSemanticUsagePending(s Snapshot) bool {
	for _, state := range s.ModelAccess {
		if state.Terminal == nil && state.SemanticPending != nil {
			return true
		}
	}
	return false
}

// requireModelAccessAdmissionAvailable blocks a new provider invocation while
// a prior exact completed semantic result still lacks required usage. The
// exact same invocation may proceed only to read back its already-retained
// result; ensureModelAccessIntent still denies dispatch for that invocation.

func requireModelAccessAdmissionAvailable(s Snapshot, invocation runtime.Invocation) error {
	if invocation.Profile.Runtime == "fake" {
		return nil
	}
	for _, state := range s.ModelAccess {
		if state.Terminal != nil || state.SemanticPending == nil {
			continue
		}
		if state.RuntimeInvocationID != invocation.ID {
			return ErrSemanticUsagePending
		}
		current, err := currentModelInvocation(s, invocation.ID)
		if err != nil || current != invocation {
			return errors.Join(ErrSemanticUsagePending, err)
		}
	}
	return nil
}

func semanticUsagePendingFor(s Snapshot, invocationID string) *ModelAccessSemanticPending {
	index := modelAccessIndex(s, invocationID)
	if index < 0 || s.ModelAccess[index].SemanticPending == nil {
		return nil
	}
	copy := *s.ModelAccess[index].SemanticPending
	return &copy
}

// requireModelAccessResult accepts either a normal settled access receipt or
// exact semantic output whose required usage is still pending.
func requireModelAccessResult(s Snapshot, invocation runtime.Invocation, runtimeHead, outputHash, threadID, turnID string, usagePending bool) error {
	if !usagePending {
		return requireCompletedModelAccess(s, invocation, runtimeHead, outputHash)
	}
	pending := semanticUsagePendingFor(s, invocation.ID)
	if pending == nil || pending.RuntimeJournalHead != runtimeHead || pending.ResultHash != outputHash ||
		pending.ThreadID != threadID || pending.TurnID != turnID {
		return ErrSemanticUsagePending
	}
	if err := validateModelAccessSemanticPending(s, invocation, *pending); err != nil {
		return err
	}
	return nil
}

// replaySemanticUsagePending is called only from the strict controller journal
// replay path; runtime bytes are reattested by observeSemanticUsagePending.
func replaySemanticUsagePending(s *Snapshot, event journal.Event) error {
	if s.Creation.Config.Version != 2 {
		return errors.New("semantic usage pending requires configuration v2")
	}
	var pending ModelAccessSemanticPending
	if err := canonical.Decode(event.Payload, &pending); err != nil {
		return err
	}
	invocation, err := currentModelInvocation(*s, pending.RuntimeInvocationID)
	if err != nil {
		return err
	}
	index := modelAccessIndex(*s, invocation.ID)
	if index < 0 || s.ModelAccess[index].SemanticPending != nil || s.ModelAccess[index].Terminal != nil {
		return errors.New("semantic usage pending transition rejected")
	}
	if err := validateModelAccessSemanticPending(*s, invocation, pending); err != nil {
		return err
	}
	copy := pending
	s.ModelAccess[index].SemanticPending = &copy
	return validateModelAccessLedger(*s)
}

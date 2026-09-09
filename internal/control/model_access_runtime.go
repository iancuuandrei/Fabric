package control

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexruntime"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/safepath"
)

// ModelAccessState mirrors the access-only journal inside controller replay.
// Reservations are admission ceilings, not proof of provider-side enforcement.
type ModelAccessState struct {
	RuntimeInvocationID string               `json:"runtime_invocation_id"`
	Intent              access.Intent        `json:"intent"`
	Terminal            *ModelAccessTerminal `json:"terminal,omitempty"`
}

// ModelAccessTerminal binds an access receipt to exact runtime journal evidence.
type ModelAccessTerminal struct {
	RuntimeInvocationID string         `json:"runtime_invocation_id"`
	RuntimeJournalHead  string         `json:"runtime_journal_head"`
	Receipt             access.Receipt `json:"receipt"`
}

type modelAccessIntentEvent struct {
	RuntimeInvocationID string        `json:"runtime_invocation_id"`
	Intent              access.Intent `json:"intent"`
}

func replayModelAccess(s *Snapshot, event journal.Event) error {
	if s.Creation.Config.Version != 2 {
		return errors.New("model access events require configuration v2")
	}
	switch event.Kind {
	case "model.access-intent":
		var proposed modelAccessIntentEvent
		if err := canonical.Decode(event.Payload, &proposed); err != nil {
			return err
		}
		invocation, err := currentModelInvocation(*s, proposed.RuntimeInvocationID)
		if err != nil {
			return err
		}
		expected, err := deriveModelAccessIntent(*s, invocation, 1)
		if err != nil {
			return err
		}
		if err := validateCodexSubscriptionAccess(*s, expected); err != nil {
			return err
		}
		if proposed.RuntimeInvocationID != invocation.ID || !sameCanonical(proposed.Intent, expected) {
			return errors.New("model access intent differs from exact runtime invocation")
		}
		for _, existing := range s.ModelAccess {
			if existing.RuntimeInvocationID == proposed.RuntimeInvocationID || existing.Intent.Reservation.InvocationID == proposed.Intent.Reservation.InvocationID {
				return errors.New("model access invocation already recorded")
			}
		}
		s.ModelAccess = append(s.ModelAccess, ModelAccessState{RuntimeInvocationID: proposed.RuntimeInvocationID, Intent: proposed.Intent})
	case "model.access-receipt":
		var terminal ModelAccessTerminal
		if err := canonical.Decode(event.Payload, &terminal); err != nil {
			return err
		}
		if safepath.RequireDigest(terminal.RuntimeJournalHead) != nil {
			return errors.New("model access terminal lacks runtime journal identity")
		}
		index := modelAccessIndex(*s, terminal.RuntimeInvocationID)
		if index < 0 || s.ModelAccess[index].Terminal != nil {
			return errors.New("model access receipt transition rejected")
		}
		if err := terminal.Receipt.Validate(s.ModelAccess[index].Intent); err != nil {
			return err
		}
		copy := terminal
		s.ModelAccess[index].Terminal = &copy
	default:
		return errors.New("unknown model access event")
	}
	return validateModelAccessLedger(*s)
}

func validateModelAccessLedger(s Snapshot) error {
	policy, err := s.Creation.Config.AccessPolicy(s.RunID)
	if err != nil {
		return err
	}
	ledger, err := access.NewLedger(policy.Limits)
	if err != nil {
		return err
	}
	if s.PlannerAccess != nil {
		if err := ledger.Reserve(s.PlannerAccess.Reservation); err != nil {
			return err
		}
		if s.Plan != nil {
			if err := ledger.Complete(s.PlannerAccess.Reservation.InvocationID); err != nil {
				return err
			}
		}
	}
	for _, state := range s.ModelAccess {
		if err := ledger.Reserve(state.Intent.Reservation); err != nil {
			return err
		}
		if state.Terminal != nil {
			if err := ledger.Complete(state.Intent.Reservation.InvocationID); err != nil {
				return err
			}
		}
	}
	return nil
}

func currentModelInvocation(s Snapshot, id string) (runtime.Invocation, error) {
	candidates := []runtime.Invocation{}
	if s.State == "PLANNING" && s.Creation.Config.Planner.Runtime == "codex-app-server" {
		invocation, err := plannerInvocation(s.Creation.Config, s.Creation.Objective)
		if err != nil {
			return runtime.Invocation{}, err
		}
		candidates = append(candidates, invocation)
	}
	if s.ExplorerHost != nil {
		candidates = append(candidates, s.ExplorerHost.Intent.Invocation)
	}
	if s.WriterHost != nil {
		candidates = append(candidates, s.WriterHost.Intent.Invocation)
	}
	if s.ReviewHost != nil {
		candidates = append(candidates, s.ReviewHost.Intent.Invocation)
	}
	for _, invocation := range candidates {
		if invocation.ID == id {
			exact, err := runtime.NewInvocation(invocation.Profile, invocation.Input)
			if err != nil || exact != invocation {
				return runtime.Invocation{}, errors.New("recorded runtime invocation identity mismatch")
			}
			return invocation, nil
		}
	}
	return runtime.Invocation{}, errors.New("model access invocation is not current controller work")
}

func validateCodexSubscriptionAccess(s Snapshot, intent access.Intent) error {
	if intent.Route.Runtime != "codex-app-server" || intent.Route.Provider != "openai" || s.Creation.Config.Codex == nil || !filepath.IsAbs(s.Creation.Config.Codex.AuthSource) {
		return errors.New("Codex ChatGPT subscription route required")
	}
	policy, err := s.Creation.Config.AccessPolicy(s.RunID)
	if err != nil {
		return err
	}
	for _, profile := range policy.Profiles {
		id, err := profile.ID()
		if err != nil {
			return err
		}
		if id == intent.Route.AccessID {
			if profile.Kind != "subscription" || profile.Runtime != "codex-app-server" || profile.Provider != "openai" || profile.AuthMode != "chatgpt-session" || profile.CredentialRef != "" || intent.Reservation.BillingMode != "subscription" || intent.Reservation.CostMicroUSD != nil {
				return errors.New("Codex access profile is not the declared ChatGPT subscription mode")
			}
			return nil
		}
	}
	return errors.New("Codex route access profile missing")
}

func ensureModelAccessIntent(path string, s Snapshot, invocation runtime.Invocation) (Snapshot, ModelAccessState, error) {
	expected, err := deriveModelAccessIntent(s, invocation, 1)
	if err != nil {
		return s, ModelAccessState{}, err
	}
	if err := validateCodexSubscriptionAccess(s, expected); err != nil {
		return s, ModelAccessState{}, err
	}
	if err := ensureLegacyPlannerAccessMirror(path, s); err != nil {
		return s, ModelAccessState{}, err
	}
	index := modelAccessIndex(s, invocation.ID)
	if index >= 0 {
		if !sameCanonical(s.ModelAccess[index].Intent, expected) {
			return s, ModelAccessState{}, errors.New("recorded model access intent changed")
		}
	} else {
		if err := Append(path, "model.access-intent", modelAccessIntentEvent{invocation.ID, expected}); err != nil {
			return s, ModelAccessState{}, err
		}
		s, err = Inspect(path)
		if err != nil {
			return s, ModelAccessState{}, err
		}
		index = modelAccessIndex(s, invocation.ID)
		if index < 0 {
			return s, ModelAccessState{}, errors.New("model access intent missing after append")
		}
	}
	if _, err := ensureTaskPool(s.Creation.Config, s.RunID, expected); err != nil {
		return s, ModelAccessState{}, err
	}
	policy, err := s.Creation.Config.AccessPolicy(s.RunID)
	if err != nil {
		return s, ModelAccessState{}, err
	}
	accessPath := modelAccessJournal(path)
	if err := access.ReserveDurable(accessPath, policy, expected); err != nil {
		if activeErr := access.RequireActive(accessPath, policy, expected); activeErr != nil {
			return s, ModelAccessState{}, errors.Join(err, activeErr)
		}
	}
	return s, s.ModelAccess[index], nil
}

func ensureLegacyPlannerAccessMirror(path string, s Snapshot) error {
	if s.Creation.Config.Version != 2 || s.Creation.Config.Planner.Runtime != "fake" {
		return nil
	}
	if s.PlannerAccess == nil || s.Plan == nil {
		return errors.New("legacy fake planner admission is not terminal")
	}
	expected, err := expectedPlanningAccess(s)
	if err != nil {
		return err
	}
	if !sameCanonical(*s.PlannerAccess, expected) {
		return errors.New("legacy fake planner admission changed")
	}
	receipt, err := planningAccessReceipt(s, *s.Plan)
	if err != nil {
		return err
	}
	policy, err := s.Creation.Config.AccessPolicy(s.RunID)
	if err != nil {
		return err
	}
	accessPath := modelAccessJournal(path)
	if reserveErr := access.ReserveDurable(accessPath, policy, expected); reserveErr != nil {
		if terminalErr := access.RequireTerminal(accessPath, policy, expected, receipt); terminalErr == nil {
			return nil
		}
		if activeErr := access.RequireActive(accessPath, policy, expected); activeErr != nil {
			return errors.Join(reserveErr, activeErr)
		}
	}
	if terminalErr := access.RecordTerminal(accessPath, policy, receipt); terminalErr != nil {
		if exactErr := access.RequireTerminal(accessPath, policy, expected, receipt); exactErr != nil {
			return errors.Join(terminalErr, exactErr)
		}
	}
	return nil
}

func requireModelAccessActive(path string, s Snapshot, state ModelAccessState) error {
	if state.Terminal != nil {
		return errors.New("model invocation access is terminal")
	}
	policy, err := s.Creation.Config.AccessPolicy(s.RunID)
	if err != nil {
		return err
	}
	return access.RequireActive(modelAccessJournal(path), policy, state.Intent)
}

func completeModelAccess(ctx context.Context, path, runtimePath string, invocation runtime.Invocation, resultDomain string) (Snapshot, ModelAccessTerminal, error) {
	s, err := Inspect(path)
	if err != nil {
		return s, ModelAccessTerminal{}, err
	}
	index := modelAccessIndex(s, invocation.ID)
	if index < 0 {
		return s, ModelAccessTerminal{}, errors.New("runtime result lacks durable model access intent")
	}
	expected, err := observedModelAccessTerminal(runtimePath, invocation, s.ModelAccess[index].Intent, s.Creation.Repository, resultDomain)
	if err != nil {
		return s, ModelAccessTerminal{}, err
	}
	if recorded := s.ModelAccess[index].Terminal; recorded != nil {
		if !sameCanonical(*recorded, expected) {
			return s, ModelAccessTerminal{}, errors.New("recorded model access terminal changed")
		}
		if err := settleModelTaskPool(path, s.Creation.Config, s.RunID, s.ModelAccess[index].Intent, *recorded); err != nil {
			return s, ModelAccessTerminal{}, err
		}
		return s, *recorded, nil
	}
	policy, err := s.Creation.Config.AccessPolicy(s.RunID)
	if err != nil {
		return s, ModelAccessTerminal{}, err
	}
	accessPath := modelAccessJournal(path)
	reconcileErr := access.Reconcile(ctx, accessPath, policy, s.ModelAccess[index].Intent, func(context.Context, access.Intent) (access.Receipt, error) {
		return expected.Receipt, nil
	})
	if reconcileErr != nil {
		if terminalErr := access.RequireTerminal(accessPath, policy, s.ModelAccess[index].Intent, expected.Receipt); terminalErr != nil {
			return s, ModelAccessTerminal{}, errors.Join(reconcileErr, terminalErr)
		}
	}
	if err := appendModelAccessReceipt(path, expected); err != nil {
		return s, ModelAccessTerminal{}, err
	}
	s, err = Inspect(path)
	if err != nil {
		return s, ModelAccessTerminal{}, err
	}
	if err := settleModelTaskPool(path, s.Creation.Config, s.RunID, s.ModelAccess[index].Intent, expected); err != nil {
		return s, ModelAccessTerminal{}, err
	}
	return s, expected, nil
}

func appendModelAccessReceipt(path string, terminal ModelAccessTerminal) error {
	_, err := journal.Append(path, "model.access-receipt", terminal, func(events []journal.Event) error {
		s, replayErr := Replay(events)
		if replayErr != nil {
			return replayErr
		}
		return validateControllerJournalPath(path, s.Creation, s.RunID)
	})
	return err
}

func observedModelAccessTerminal(runtimePath string, invocation runtime.Invocation, intent access.Intent, expectedSource repository.Identity, resultDomain string) (ModelAccessTerminal, error) {
	state, head, err := codexruntime.InspectWithHead(runtimePath)
	if err != nil {
		return ModelAccessTerminal{}, err
	}
	if state.Intent == nil || state.Intent.Invocation != invocation || head == "" || safepath.RequireDigest(head) != nil {
		return ModelAccessTerminal{}, errors.New("runtime terminal identity unavailable")
	}
	if state.Source == nil || *state.Source != expectedSource {
		return ModelAccessTerminal{}, errors.New("runtime terminal source identity missing or substituted")
	}
	if state.Thread == nil || state.TurnID == "" {
		return ModelAccessTerminal{}, errors.New("runtime terminal lacks exact thread and turn")
	}
	if err := state.Thread.Validate(invocation.Profile, state.Intent.Directory); err != nil {
		return ModelAccessTerminal{}, err
	}
	routeID, err := intent.Route.ID()
	if err != nil {
		return ModelAccessTerminal{}, err
	}
	receipt := access.Receipt{InvocationID: intent.Reservation.InvocationID, RouteID: routeID}
	if state.Result != nil && state.TurnStatus == "completed" {
		if err := runtime.ValidateResult(invocation, *state.Result, true); err != nil {
			return ModelAccessTerminal{}, err
		}
		receipt.Status = "completed"
		receipt.OutputHash, err = canonical.Hash(resultDomain, *state.Result)
		if err != nil {
			return ModelAccessTerminal{}, err
		}
		receipt.ObservedModel = state.Result.ObservedModel
		receipt.ObservedProvider = state.Result.ObservedProvider
		receipt.InputTokens = state.Result.Usage.InputTokens
		receipt.OutputTokens = state.Result.Usage.OutputTokens
	} else if state.Result == nil && (state.TurnStatus == "failed" || state.TurnStatus == "interrupted") {
		receipt.Status = "failed"
		if state.TurnStatus == "interrupted" {
			receipt.Status = "cancelled"
		}
		model, provider := state.Thread.Model, state.Thread.Provider
		receipt.ObservedModel = &model
		receipt.ObservedProvider = &provider
	} else {
		return ModelAccessTerminal{}, errors.New("runtime invocation remains unresolved")
	}
	if err := receipt.Validate(intent); err != nil {
		return ModelAccessTerminal{}, err
	}
	return ModelAccessTerminal{RuntimeInvocationID: invocation.ID, RuntimeJournalHead: head, Receipt: receipt}, nil
}

func requireCompletedModelAccess(s Snapshot, invocation runtime.Invocation, runtimeHead, outputHash string) error {
	if s.Creation.Config.Version != 2 || invocation.Profile.Runtime != "codex-app-server" {
		return nil
	}
	index := modelAccessIndex(s, invocation.ID)
	if index < 0 || s.ModelAccess[index].Terminal == nil {
		return errors.New("completed runtime lacks terminal model access receipt")
	}
	terminal := s.ModelAccess[index].Terminal
	if terminal.RuntimeJournalHead != runtimeHead || terminal.Receipt.Status != "completed" || terminal.Receipt.OutputHash != outputHash {
		return errors.New("runtime receipt differs from terminal model access evidence")
	}
	return nil
}

func modelAccessIndex(s Snapshot, runtimeInvocationID string) int {
	for index := range s.ModelAccess {
		if s.ModelAccess[index].RuntimeInvocationID == runtimeInvocationID {
			return index
		}
	}
	return -1
}

func modelAccessJournal(path string) string { return path + ".model-access.jsonl" }

func sameCanonical(left, right any) bool {
	a, err := canonical.Bytes(left)
	if err != nil {
		return false
	}
	b, err := canonical.Bytes(right)
	return err == nil && bytes.Equal(a, b)
}

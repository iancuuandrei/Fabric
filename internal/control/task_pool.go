package control

import (
	"errors"
	"reflect"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/providerruntime"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskpool"
)

type taskPoolAttemptIdentity struct {
	Version            int    `json:"version"`
	PoolBindingID      string `json:"pool_binding_id"`
	AccessInvocationID string `json:"access_invocation_id"`
}

func taskPoolRequest(c config.Config, runID string, intent access.Intent) (taskpool.Request, bool, error) {
	if c.TaskPool == nil {
		return taskpool.Request{}, false, nil
	}
	poolID, err := canonical.Hash("harness.taskpool-binding.v1", *c.TaskPool)
	if err != nil {
		return taskpool.Request{}, false, err
	}
	id, err := canonical.Hash("harness.controller-taskpool-attempt.v1", taskPoolAttemptIdentity{Version: 1, PoolBindingID: poolID, AccessInvocationID: intent.Reservation.InvocationID})
	if err != nil {
		return taskpool.Request{}, false, err
	}
	return taskpool.Request{ID: id, RunID: runID, AccessProfileID: intent.Route.AccessID, Provider: intent.Route.Provider, Model: intent.Route.Model}, true, nil
}

func ensureTaskPool(c config.Config, runID string, intent access.Intent) (taskpool.Request, error) {
	request, configured, err := taskPoolRequest(c, runID, intent)
	if err != nil || !configured {
		return request, err
	}
	if err := taskpool.Bind(c.TaskPool.Path, c.TaskPool.Limits); err != nil {
		return request, err
	}
	state, err := taskpool.Inspect(c.TaskPool.Path)
	if err != nil {
		return request, err
	}
	if active, ok := state.Active[request.ID]; ok {
		if !reflect.DeepEqual(active, request) {
			return request, errors.New("task pool active attempt identity changed")
		}
		return request, nil
	}
	if _, settled := state.Settled[request.ID]; settled {
		return request, errors.New("task pool attempt is already settled")
	}
	if err := taskpool.Acquire(c.TaskPool.Path, request); err != nil {
		state, inspectErr := taskpool.Inspect(c.TaskPool.Path)
		if inspectErr == nil && reflect.DeepEqual(state.Active[request.ID], request) {
			return request, nil
		}
		return request, errors.Join(err, inspectErr)
	}
	return request, nil
}

func settleProviderTaskPool(controllerPath string, c config.Config, runID string, intent access.Intent, receipt providerDispatchReceipt) error {
	request, configured, err := taskPoolRequest(c, runID, intent)
	if err != nil || !configured {
		return err
	}
	s, err := Inspect(controllerPath)
	if err != nil {
		return err
	}
	observed := false
	if receipt.Role == "planner" {
		observed = s.PlannerProvider != nil && reflect.DeepEqual(*s.PlannerProvider, receipt)
	} else {
		observed = reflect.DeepEqual(s.ProviderRuntime[receipt.InvocationID], receipt)
	}
	if !observed {
		return errors.New("task pool settlement lacks replayed controller provider receipt")
	}
	routeID, err := intent.Route.ID()
	if err != nil {
		return err
	}
	model, provider := intent.Route.Model, intent.Route.Provider
	terminal := access.Receipt{InvocationID: intent.Reservation.InvocationID, RouteID: routeID, Status: "completed", OutputHash: receipt.ResultHash, ObservedModel: &model, ObservedProvider: &provider, InputTokens: receipt.Result.Usage.InputTokens, OutputTokens: receipt.Result.Usage.OutputTokens}
	policy, err := c.AccessPolicy(runID)
	if err != nil {
		return err
	}
	if err := access.RequireTerminal(modelAccessJournal(controllerPath), policy, intent, terminal); err != nil {
		return err
	}
	receiptHash, err := canonical.Hash("harness.provider-dispatch-receipt.v1", receipt)
	if err != nil {
		return err
	}
	release := taskpool.Release{ID: request.ID, ReceiptSHA256: receiptHash}
	state, err := taskpool.Inspect(c.TaskPool.Path)
	if err != nil {
		return err
	}
	if settled, ok := state.Settled[request.ID]; ok {
		if reflect.DeepEqual(settled, release) {
			return nil
		}
		return errors.New("task pool settlement receipt changed")
	}
	if !reflect.DeepEqual(state.Active[request.ID], request) {
		return errors.New("task pool settlement lacks exact active attempt")
	}
	return taskpool.Settle(c.TaskPool.Path, release)
}

func settleProviderDispatchTaskPool(controllerPath string, c config.Config, runID string, invocation runtime.Invocation, receipt providerDispatchReceipt) error {
	if c.TaskPool == nil {
		return nil
	}
	var inputHash string
	var err error
	if invocation.Profile.Runtime == "provider-api" {
		_, expectation, resolveErr := ConfiguredProviderExpectation(c, invocation.Profile.Role)
		if resolveErr != nil {
			return resolveErr
		}
		direct := providerruntime.Invocation{Version: 1, System: "Return exactly one JSON value for the controller role request. Do not claim tools or repository access.", Prompt: invocation.Input, Output: providerruntime.OutputContract{Kind: "json"}}
		inputHash, err = direct.InputHash(expectation)
	} else {
		inputHash, err = access.InputID(invocation.Input)
	}
	if err != nil {
		return err
	}
	selected, err := ResolveProviderRouting(c, runID, invocation.Profile.Role, inputHash, 1)
	if err != nil || selected.Intent.Reservation.InvocationID != receipt.AccessInvocationID {
		return errors.Join(errors.New("task pool settlement route changed"), err)
	}
	return settleProviderTaskPool(controllerPath, c, runID, selected.Intent, receipt)
}

func settleModelTaskPool(controllerPath string, c config.Config, runID string, intent access.Intent, terminal ModelAccessTerminal) error {
	if c.TaskPool == nil {
		return nil
	}
	policy, err := c.AccessPolicy(runID)
	if err != nil {
		return err
	}
	if err := access.RequireTerminal(modelAccessJournal(controllerPath), policy, intent, terminal.Receipt); err != nil {
		return err
	}
	receiptHash, err := canonical.Hash("harness.model-access-terminal.v1", terminal)
	if err != nil {
		return err
	}
	return settleTaskPool(c, runID, intent, receiptHash)
}

func settleTaskPool(c config.Config, runID string, intent access.Intent, receiptHash string) error {
	request, configured, err := taskPoolRequest(c, runID, intent)
	if err != nil || !configured {
		return err
	}
	release := taskpool.Release{ID: request.ID, ReceiptSHA256: receiptHash}
	state, err := taskpool.Inspect(c.TaskPool.Path)
	if err != nil {
		return err
	}
	if settled, ok := state.Settled[request.ID]; ok {
		if reflect.DeepEqual(settled, release) {
			return nil
		}
		return errors.New("task pool settlement receipt changed")
	}
	if !reflect.DeepEqual(state.Active[request.ID], request) {
		return errors.New("task pool settlement lacks exact active attempt")
	}
	return taskpool.Settle(c.TaskPool.Path, release)
}

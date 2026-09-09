package control

import (
	"errors"
	"path"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/safepath"
)

const providerStateRootBoundEvent = "provider.opencode-state-root-bound"

type providerStateRootBinding struct {
	Version      int    `json:"version"`
	RunID        string `json:"run_id"`
	Role         string `json:"role"`
	InvocationID string `json:"invocation_id"`
	Segment      string `json:"segment"`
}

func bindProviderOpenCodeStateRoot(runtimePath string, runtimeExists bool, runID, role, invocationID string) (string, error) {
	want, err := newProviderStateRootBinding(runID, role, invocationID)
	if err != nil {
		return "", err
	}
	bindingPath := runtimePath + ".state-root.jsonl"
	events, err := journal.Read(bindingPath)
	if err != nil {
		return "", err
	}
	stored, err := replayProviderStateRoot(events)
	if err != nil {
		return "", err
	}
	if stored != nil {
		if *stored != want {
			return "", errors.New("OpenCode state root binding differs")
		}
		return stored.Segment, nil
	}
	if runtimeExists {
		return path.Join(runID, role+"-"+invocationID), nil
	}
	if _, err := journal.Append(bindingPath, providerStateRootBoundEvent, want, validateProviderStateRootEvents); err != nil {
		events, readErr := journal.Read(bindingPath)
		stored, replayErr := replayProviderStateRoot(events)
		if readErr == nil && replayErr == nil && stored != nil && *stored == want {
			return stored.Segment, nil
		}
		return "", errors.Join(err, readErr, replayErr)
	}
	return want.Segment, nil
}

func newProviderStateRootBinding(runID, role, invocationID string) (providerStateRootBinding, error) {
	var zero providerStateRootBinding
	if safepath.RequireDigest(runID) != nil || safepath.RequireDigest(invocationID) != nil || role == "" || len(role) > 128 {
		return zero, errors.New("invalid OpenCode state root identity")
	}
	binding := providerStateRootBinding{Version: 1, RunID: runID, Role: role, InvocationID: invocationID}
	segment, err := canonical.Hash("harness.control-opencode-state-root.v1", binding)
	if err != nil {
		return zero, err
	}
	binding.Segment = segment
	return binding, nil
}

func replayProviderStateRoot(events []journal.Event) (*providerStateRootBinding, error) {
	if len(events) == 0 {
		return nil, nil
	}
	if len(events) != 1 || events[0].Kind != providerStateRootBoundEvent {
		return nil, errors.New("invalid OpenCode state root binding history")
	}
	var binding providerStateRootBinding
	if err := canonical.Decode(events[0].Payload, &binding); err != nil {
		return nil, err
	}
	want, err := newProviderStateRootBinding(binding.RunID, binding.Role, binding.InvocationID)
	if err != nil || binding.Version != 1 || binding != want {
		return nil, errors.New("invalid OpenCode state root binding")
	}
	return &binding, nil
}

func validateProviderStateRootEvents(events []journal.Event) error {
	_, err := replayProviderStateRoot(events)
	return err
}

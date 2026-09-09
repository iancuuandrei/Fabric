package control

import (
	"context"
	"errors"
	"fmt"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/hostenvironment"
)

// HostAdmission binds the historical host observation and explicit controller
// policy used when a run was created. The observation is durable report data;
// dispatch evaluates the policy again against a fresh local observation.
type HostAdmission struct {
	Version     int                         `json:"version"`
	Observation hostenvironment.Observation `json:"observation"`
	Policy      hostenvironment.Policy      `json:"policy"`
	BindingHash string                      `json:"binding_hash"`
}

type hostAdmissionPayload struct {
	Version     int                         `json:"version"`
	Observation hostenvironment.Observation `json:"observation"`
	Policy      hostenvironment.Policy      `json:"policy"`
}

// DefaultHostPolicy permits standalone and Codex-host-hinted execution without
// claiming or requiring sandbox enforcement.
func DefaultHostPolicy() hostenvironment.Policy {
	return hostenvironment.Policy{
		Version:             1,
		AllowedHosts:        []hostenvironment.HostKind{hostenvironment.HostNative, hostenvironment.HostCodex},
		AllowedSandboxModes: []hostenvironment.SandboxMode{},
	}
}

// BindHostAdmission validates fresh local evidence against declared policy and
// returns a creation value with an immutable host admission payload.
func BindHostAdmission(creation Creation, observation hostenvironment.Observation, policy hostenvironment.Policy) (Creation, error) {
	if creation.HostAdmission != nil {
		return Creation{}, errors.New("creation already has host admission")
	}
	if creation.Config.HostPolicy != nil && !sameCanonical(*creation.Config.HostPolicy, policy) {
		return Creation{}, errors.New("host admission differs from configured policy")
	}
	if err := hostenvironment.Admit(policy, observation); err != nil {
		return Creation{}, fmt.Errorf("admit creation host: %w", err)
	}
	observation.HostEvidence.Signals = append([]string{}, observation.HostEvidence.Signals...)
	policy.AllowedHosts = append([]hostenvironment.HostKind{}, policy.AllowedHosts...)
	policy.AllowedSandboxModes = append([]hostenvironment.SandboxMode{}, policy.AllowedSandboxModes...)
	admission := &HostAdmission{Version: 1, Observation: observation, Policy: policy}
	hash, err := hostAdmissionHash(admission)
	if err != nil {
		return Creation{}, err
	}
	admission.BindingHash = hash
	creation.HostAdmission = admission
	return creation, nil
}

// RequireHostAdmission re-evaluates a new run's immutable declared policy
// against fresh local host evidence. Legacy runs without an admission remain
// readable and retain their historical behavior.
func RequireHostAdmission(snapshot Snapshot, current hostenvironment.Observation) error {
	if err := validateCreationHostAdmission(snapshot.Creation); err != nil {
		return err
	}
	if snapshot.Creation.HostAdmission == nil {
		return nil
	}
	if err := hostenvironment.Admit(snapshot.Creation.HostAdmission.Policy, current); err != nil {
		return fmt.Errorf("current host admission: %w", err)
	}
	return nil
}

func validateCreationHostAdmission(creation Creation) error {
	if creation.Config.HostPolicy != nil {
		if creation.HostAdmission == nil || !sameCanonical(*creation.Config.HostPolicy, creation.HostAdmission.Policy) {
			return errors.New("host admission differs from configured policy")
		}
	}
	return validateRecordedHostAdmission(creation.HostAdmission)
}

func requireCurrentHostAdmission(ctx context.Context, snapshot Snapshot) error {
	if err := validateCreationHostAdmission(snapshot.Creation); err != nil {
		return err
	}
	if snapshot.Creation.HostAdmission == nil {
		return nil
	}
	current, err := hostenvironment.ObserveDefault(ctx)
	if err != nil {
		return err
	}
	return RequireHostAdmission(snapshot, current)
}

func validateRecordedHostAdmission(admission *HostAdmission) error {
	if admission == nil {
		return nil
	}
	if admission.Version != 1 {
		return errors.New("unsupported host admission version")
	}
	hash, err := hostAdmissionHash(admission)
	if err != nil {
		return err
	}
	if admission.BindingHash != hash {
		return errors.New("host admission binding mismatch")
	}
	if err := hostenvironment.ValidatePolicy(admission.Policy); err != nil {
		return err
	}
	if err := hostenvironment.ValidateReport(admission.Observation); err != nil {
		return err
	}
	if !containsHostKind(admission.Policy.AllowedHosts, admission.Observation.Host) {
		return errors.New("recorded host did not satisfy declared policy")
	}
	if admission.Policy.RequireVerifiedSandbox && admission.Observation.Sandbox.Status != hostenvironment.SandboxVerified {
		return errors.New("recorded admission lacks required verified sandbox report")
	}
	if len(admission.Policy.AllowedSandboxModes) != 0 && !containsSandboxMode(admission.Policy.AllowedSandboxModes, admission.Observation.Sandbox.Mode) {
		return errors.New("recorded sandbox mode did not satisfy declared policy")
	}
	return nil
}

func hostAdmissionHash(admission *HostAdmission) (string, error) {
	return canonical.Hash("harness.host-admission.v1", hostAdmissionPayload{
		Version: admission.Version, Observation: admission.Observation, Policy: admission.Policy,
	})
}

func containsHostKind(values []hostenvironment.HostKind, want hostenvironment.HostKind) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsSandboxMode(values []hostenvironment.SandboxMode, want hostenvironment.SandboxMode) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

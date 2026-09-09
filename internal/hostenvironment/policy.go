package hostenvironment

import (
	"errors"
	"fmt"
	"strings"
)

// Policy is explicit controller authority. It is never inferred from an
// Observation, process environment, agent runtime, or model transport.
type Policy struct {
	Version                int           `toml:"version" json:"version"`
	AllowedHosts           []HostKind    `toml:"allowed_hosts" json:"allowed_hosts"`
	RequireVerifiedSandbox bool          `toml:"require_verified_sandbox" json:"require_verified_sandbox"`
	AllowedSandboxModes    []SandboxMode `toml:"allowed_sandbox_modes" json:"allowed_sandbox_modes"`
}

// Admit checks an observation against an explicit declared host policy.
func Admit(policy Policy, observation Observation) error {
	if err := ValidatePolicy(policy); err != nil {
		return err
	}
	if err := ValidateReport(observation); err != nil {
		return err
	}
	verified := observation.Sandbox.Status == SandboxVerified &&
		observation.Sandbox.Inherited && observation.verifiedHere &&
		observation.Sandbox.Mode == observation.verifiedMode &&
		observation.Sandbox.Source == observation.verifiedSource
	if observation.Sandbox.Status == SandboxVerified && !verified {
		return errors.New("sandbox report is historical data without fresh verifier authority")
	}
	if len(policy.AllowedHosts) != 0 && !containsHost(policy.AllowedHosts, observation.Host) {
		return fmt.Errorf("host %q is not allowed by declared policy", observation.Host)
	}
	if policy.RequireVerifiedSandbox && !verified {
		return errors.New("declared policy requires verified inherited sandbox evidence")
	}
	if len(policy.AllowedSandboxModes) != 0 {
		if !verified {
			return errors.New("declared sandbox modes require verified inherited sandbox evidence")
		}
		if !containsMode(policy.AllowedSandboxModes, observation.Sandbox.Mode) {
			return fmt.Errorf("sandbox mode %q is not allowed by declared policy", observation.Sandbox.Mode)
		}
	}
	return nil
}

// ValidatePolicy validates finite declared controller authority without using
// any host observation to infer or broaden it.
func ValidatePolicy(policy Policy) error {
	if policy.Version != 1 || policy.AllowedHosts == nil || policy.AllowedSandboxModes == nil || len(policy.AllowedHosts) == 0 || len(policy.AllowedHosts) > 2 {
		return errors.New("invalid host policy")
	}
	hosts := map[HostKind]bool{}
	for _, host := range policy.AllowedHosts {
		if (host != HostNative && host != HostCodex) || hosts[host] {
			return errors.New("invalid or duplicate allowed host")
		}
		hosts[host] = true
	}
	if len(policy.AllowedSandboxModes) > 3 || (len(policy.AllowedSandboxModes) != 0 && !policy.RequireVerifiedSandbox) {
		return errors.New("allowed sandbox modes require verified sandbox policy")
	}
	modes := map[SandboxMode]bool{}
	for _, mode := range policy.AllowedSandboxModes {
		if (mode != SandboxReadOnly && mode != SandboxWorkspaceWrite && mode != SandboxRestricted) || modes[mode] {
			return errors.New("invalid or duplicate allowed sandbox mode")
		}
		modes[mode] = true
	}
	return nil
}

// ValidateReport validates a serialized observation as historical report data.
// It does not restore verifier authority or qualify a current sandbox.
func ValidateReport(observation Observation) error {
	if observation.Version != 1 {
		return errors.New("unsupported host observation version")
	}
	if !identifier(observation.Platform, 32) {
		return errors.New("invalid host observation platform")
	}
	switch observation.HostEvidence.Level {
	case EvidenceDefault:
		if observation.Host != HostNative || len(observation.HostEvidence.Signals) != 0 {
			return errors.New("invalid native host evidence")
		}
	case EvidenceHint:
		if observation.Host != HostCodex || len(observation.HostEvidence.Signals) == 0 {
			return errors.New("invalid Codex host evidence")
		}
		for index, signal := range observation.HostEvidence.Signals {
			name, isEnvironment := strings.CutPrefix(signal, "env:")
			_, knownEnvironment := codexEnvironmentHints[name]
			if signal != "process-name" && (!isEnvironment || !knownEnvironment) {
				return errors.New("invalid Codex host signal")
			}
			if index > 0 && signal <= observation.HostEvidence.Signals[index-1] {
				return errors.New("host signals must be unique and sorted")
			}
		}
	default:
		return errors.New("invalid host evidence level")
	}
	switch observation.Sandbox.Status {
	case SandboxNotVerified:
		if observation.Sandbox.Mode != "" || observation.Sandbox.Source != "" || observation.Sandbox.Inherited {
			return errors.New("unverified sandbox observation contains evidence")
		}
	case SandboxVerified:
		if err := validateVerifiedSandbox(VerifiedSandbox{Mode: observation.Sandbox.Mode, Source: observation.Sandbox.Source, Inherited: observation.Sandbox.Inherited}); err != nil {
			return err
		}
	default:
		return errors.New("invalid sandbox observation status")
	}
	return nil
}

func containsHost(values []HostKind, want HostKind) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsMode(values []SandboxMode, want SandboxMode) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

package control

import (
	"context"
	"errors"

	"harness.local/engorch/internal/codexhost"
	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/runtime"
)

// startCodexRoleHost centralizes the controller's Codex admission order for
// every role. Strict capability confinement is decided after configuration
// observation but before login, model-access intent, thread creation or resume.
func startCodexRoleHost(ctx context.Context, launch codexhost.Launch, hostConfig *config.Codex, profile runtime.Profile, dynamicTools []any, handler codexrpc.ToolHandler) (*codexhost.Host, error) {
	if hostConfig == nil {
		return nil, errors.New("Codex host configuration required")
	}
	if hostConfig.CapabilityAttestationPath != "" || hostConfig.CapabilityAttestationSHA256 != "" {
		attestation := codexhost.CapabilityConfinementAttestation{
			ManifestPath:   hostConfig.CapabilityAttestationPath,
			ExpectedSHA256: hostConfig.CapabilityAttestationSHA256,
			Profile:        profile,
			DynamicTools:   dynamicTools,
		}
		host, _, err := codexhost.StartWithCapabilityConfinementAttested(ctx, launch, hostConfig.CapabilityConfinement, attestation, handler)
		return host, err
	}
	host, _, err := codexhost.StartWithCapabilityConfinement(ctx, launch, hostConfig.CapabilityConfinement, handler)
	return host, err
}

// validateCodexHostReceipt binds a replayed host observation to the immutable
// capability-attestation selection recorded in the run's creation config.
func validateCodexHostReceipt(receipt codexhost.Receipt, launch codexhost.Launch, hostConfig *config.Codex) error {
	if hostConfig == nil {
		return errors.New("Codex host configuration required")
	}
	if err := receipt.Validate(launch); err != nil {
		return err
	}
	pinned := hostConfig.CapabilityAttestationPath != "" || hostConfig.CapabilityAttestationSHA256 != ""
	if pinned {
		if hostConfig.CapabilityConfinement != codexhost.CapabilityConfinementRequired || hostConfig.CapabilityAttestationPath == "" || hostConfig.CapabilityAttestationSHA256 == "" {
			return errors.New("Codex capability attestation configuration incomplete")
		}
		if receipt.CapabilityAttestationID == "" || receipt.CapabilityAttestationSHA256 != hostConfig.CapabilityAttestationSHA256 {
			return errors.New("Codex host receipt differs from configured capability attestation")
		}
		return nil
	}
	if hostConfig.CapabilityConfinement == codexhost.CapabilityConfinementRequired {
		return errors.New("strict Codex host receipt lacks configured capability attestation")
	}
	if receipt.CapabilityAttestationID != "" || receipt.CapabilityAttestationSHA256 != "" {
		return errors.New("legacy Codex configuration cannot acquire a capability attestation")
	}
	return nil
}

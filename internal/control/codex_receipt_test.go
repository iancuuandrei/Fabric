package control

import (
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/codexhost"
	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/config"
)

var codexReceiptDisabledFeatures = []string{"apps", "plugins", "remote_plugin", "hooks", "multi_agent", "multi_agent_v2", "shell_tool", "code_mode", "code_mode_host", "code_mode_only", "browser_use", "browser_use_external", "browser_use_full_cdp_access", "computer_use", "in_app_browser", "image_generation", "memories", "skill_search", "skill_mcp_dependency_install", "workspace_dependencies", "recommended_plugins", "goals", "tool_suggest", "view_image", "sleep_tool", "unbounded_connection_retries"}

func codexReceiptReplayFixture(t *testing.T) (codexhost.Launch, codexhost.Receipt) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "host")
	launch, err := codexhost.Expected(root, filepath.Join(root, "codex.exe"), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	launchID, err := launch.BindingID()
	if err != nil {
		t.Fatal(err)
	}
	return launch, codexhost.Receipt{
		LaunchID: launchID,
		Server: codexrpc.Server{
			CodexHome:      filepath.Join(root, "home"),
			UserAgent:      "engorch/replay-test",
			PlatformFamily: "windows",
			PlatformOS:     "windows",
		},
		DisabledFeatures: append([]string{}, codexReceiptDisabledFeatures...),
		PID:              1,
	}
}

func TestCodexHostReceiptReplayBindsConfiguredCapabilityAttestation(t *testing.T) {
	launch, legacy := codexReceiptReplayFixture(t)
	legacyConfig := &config.Codex{}
	if err := validateCodexHostReceipt(legacy, launch, legacyConfig); err != nil {
		t.Fatal("legacy receipt rejected", err)
	}

	manifestSHA := strings.Repeat("b", 64)
	attested := legacy
	attested.CapabilityAttestationSHA256 = manifestSHA
	attested.CapabilityAttestationID, _ = codexhost.AttestationBindingID(manifestSHA, legacy.LaunchID)
	strictConfig := &config.Codex{
		CapabilityConfinement:       codexhost.CapabilityConfinementRequired,
		CapabilityAttestationPath:   filepath.Join(t.TempDir(), "attestation.json"),
		CapabilityAttestationSHA256: manifestSHA,
	}
	if err := validateCodexHostReceipt(attested, launch, strictConfig); err != nil {
		t.Fatal("matching attested receipt rejected", err)
	}

	tests := []struct {
		name    string
		receipt codexhost.Receipt
		config  *config.Codex
	}{
		{"strict missing pin", legacy, &config.Codex{CapabilityConfinement: codexhost.CapabilityConfinementRequired}},
		{"strict missing receipt attestation", legacy, strictConfig},
		{"legacy acquired attestation", attested, legacyConfig},
		{"missing configuration", legacy, nil},
	}
	other := attested
	other.CapabilityAttestationSHA256 = strings.Repeat("c", 64)
	other.CapabilityAttestationID, _ = codexhost.AttestationBindingID(other.CapabilityAttestationSHA256, other.LaunchID)
	tests = append(tests, struct {
		name    string
		receipt codexhost.Receipt
		config  *config.Codex
	}{"different valid attestation", other, strictConfig})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateCodexHostReceipt(test.receipt, launch, test.config); err == nil {
				t.Fatal("receipt substitution admitted")
			}
		})
	}
}

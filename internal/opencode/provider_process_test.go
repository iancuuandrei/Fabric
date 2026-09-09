package opencode

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func providerProcessFixture(t *testing.T) (string, string, string, int) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	root := t.TempDir()
	for _, name := range []string{"home", "config", "data", "cache", "state", "tmp"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	_ = listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return binary, hex.EncodeToString(digest[:]), root, port
}

func TestStartReadyLimitedProviderToolsProcessBindsLaunchAndReadback(t *testing.T) {
	binary, digest, root, port := providerProcessFixture(t)
	provider, tools := providerConfigurationFixture(ProviderProtocolOpenAIResponses), providerToolsFixture()
	temperature, topP := json.Number("0.25"), json.Number("0.875")
	provider.RuntimeAgent = "build"
	provider.Variant = ProviderVariantSpec{Name: "none", Responses: &OpenAIResponsesVariantOptions{TextVerbosity: "high", Temperature: &temperature, TopP: &topP}}
	content, err := BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	var expanded map[string]any
	if err := json.Unmarshal([]byte(content), &expanded); err != nil {
		t.Fatal(err)
	}
	expanded["autoupdate"] = false
	readback, err := json.Marshal(expanded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".helper-config"), readback, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	process, receipt, evidence, err := StartReadyLimitedProviderToolsProcess(
		ctx, binary, digest, root, port, "user", "password", io.Discard,
		2, 750*time.Millisecond, false, provider, tools,
	)
	if err != nil {
		t.Fatalf("provider process startup failed: %v; evidence: %+v", err, evidence)
	}
	defer func() { _ = process.Close() }()
	if len(evidence) != 2 || evidence[0].Outcome != "timeout" || !evidence[0].Reaped ||
		evidence[1].Outcome != "ready" || evidence[1].Reaped || evidence[1].Configuration.SHA256 != receipt.SHA256 {
		t.Fatalf("unexpected provider startup evidence: %+v", evidence)
	}
	if evidence[0].ReadinessElapsedMillis < 1 || evidence[0].ReadinessPolls < 1 || evidence[0].ReadinessHTTPObserved || evidence[0].FinalReadinessErrorClass == "" ||
		!evidence[1].ReadinessHTTPObserved || evidence[1].ReadinessPolls < 1 || evidence[1].FinalReadinessErrorClass != "" || evidence[1].FirstHTTPResponseMillis < 0 {
		t.Fatalf("readiness diagnostics were not bounded: %+v", evidence)
	}
	configurationEntries := 0
	for _, entry := range process.command.Env {
		if strings.HasPrefix(entry, "OPENCODE_CONFIG_CONTENT=") {
			configurationEntries++
			if entry != "OPENCODE_CONFIG_CONTENT="+content {
				t.Fatal("provider configuration changed between build and launch")
			}
		}
	}
	identity, ok := process.identity()
	if !ok || identity.Provider == nil || identity.AdmittedProvider == nil ||
		identity.Provider.ProviderID != provider.ProviderID || identity.Provider.ModelID != provider.ModelID ||
		identity.Provider.MaxOutputTokens != provider.MaxOutputTokens || identity.Provider.CapabilitySHA256 == "" ||
		identity.AdmittedProvider.SHA256 != receipt.SHA256 || identity.MaxOutputTokens != provider.MaxOutputTokens {
		t.Fatalf("provider process identity was not bound: %+v", identity)
	}
	if configurationEntries != 1 {
		t.Fatal("provider configuration missing or duplicated")
	}
	if strings.Contains(identity.Provider.CapabilitySHA256, provider.GatewayCapability) {
		t.Fatal("provider identity exposed scoped capability")
	}
	publicIdentity, err := process.ProviderIdentity()
	if err != nil || publicIdentity.Configuration.SHA256 != receipt.SHA256 ||
		publicIdentity.CapabilitySHA256 != identity.Provider.CapabilitySHA256 ||
		publicIdentity.RuntimeAgent != "build" || publicIdentity.TextVerbosity != "high" || publicIdentity.Temperature != "0.25" || publicIdentity.TopP != "0.875" ||
		ValidateProviderProcessIdentity(publicIdentity) != nil {
		t.Fatalf("public provider process identity invalid: %+v: %v", publicIdentity, err)
	}
	if publicIdentity.ConfigurationSHA256 == publicIdentity.Configuration.SHA256 {
		t.Fatal("launch input and expanded readback hashes unexpectedly collapsed")
	}
	publicIdentity.Configuration.ToolsConfiguration.ToolIDs[0] = "mutated"
	secondIdentity, err := process.ProviderIdentity()
	if err != nil || secondIdentity.Configuration.ToolsConfiguration.ToolIDs[0] == "mutated" {
		t.Fatal("public provider process identity did not deep-copy tools")
	}
	tampered := secondIdentity
	tampered.ModelID = "substituted-model"
	if ValidateProviderProcessIdentity(tampered) == nil {
		t.Fatal("tampered public provider process identity was accepted")
	}
}

func TestStartReadyLimitedProviderToolsProcessSeparatesStateAndWorkingDirectory(t *testing.T) {
	binary, digest, stateRoot, port := providerProcessFixture(t)
	workingDirectory := t.TempDir()
	provider, tools := providerConfigurationFixture(ProviderProtocolOpenAIResponses), providerToolsFixture()
	content, err := BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingDirectory, ".first-startup-attempt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingDirectory, ".helper-config"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	process, _, evidence, err := StartReadyLimitedProviderToolsProcessInDirectory(
		ctx, binary, digest, stateRoot, workingDirectory, port, "user", "password", io.Discard,
		1, time.Second, false, provider, tools,
	)
	if err != nil {
		t.Fatalf("provider process startup failed: %v; evidence: %+v", err, evidence)
	}
	defer func() { _ = process.Close() }()
	identity, ok := process.identity()
	if !ok || identity.Root != filepath.Clean(stateRoot) || identity.WorkingDirectory != filepath.Clean(workingDirectory) || process.command.Dir != filepath.Clean(workingDirectory) {
		t.Fatalf("state/workspace identity mismatch: %+v; dir=%q", identity, process.command.Dir)
	}
	home := ""
	for _, entry := range process.command.Env {
		if strings.HasPrefix(entry, "HOME=") {
			home = strings.TrimPrefix(entry, "HOME=")
		}
	}
	if home != filepath.Join(stateRoot, "home") {
		t.Fatalf("HOME = %q, want private state root", home)
	}
	if _, err := os.Stat(filepath.Join(stateRoot, ".first-startup-attempt")); !os.IsNotExist(err) {
		t.Fatal("helper used private state root as working directory")
	}
}

func TestProviderProjectRejectionRetainsAdmittedConfigurationEvidence(t *testing.T) {
	binary, digest, root, port := providerProcessFixture(t)
	workingDirectory := t.TempDir()
	provider, tools := providerConfigurationFixture(ProviderProtocolOpenAIResponses), providerToolsFixture()
	content, err := BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".first-startup-attempt", ".helper-project-global"} {
		if err := os.WriteFile(filepath.Join(workingDirectory, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workingDirectory, ".helper-config"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	process, receipt, evidence, err := StartReadyLimitedProviderToolsProjectProcessInDirectory(
		ctx, binary, digest, root, workingDirectory, port, "user", "password", io.Discard,
		1, 7*time.Second, false, provider, tools,
		ProjectExpectation{Directory: workingDirectory, Mode: ProjectModeGit, Worktree: workingDirectory},
	)
	if err == nil || process != nil || receipt.SHA256 != "" || len(evidence) != 1 {
		t.Fatalf("project rejection returned a successful process identity: process=%v receipt=%+v evidence=%+v err=%v", process, receipt, evidence, err)
	}
	attempt := evidence[0]
	if attempt.Outcome != "project_rejected" || !attempt.Reaped || attempt.Configuration.SHA256 == "" || attempt.Configuration.ProviderID != provider.ProviderID || attempt.Configuration.ToolsConfiguration.SHA256 == "" || attempt.ProjectReadinessPolls < 1 {
		t.Fatalf("project rejection lost admitted configuration evidence: %+v", attempt)
	}
}

func TestStartReadyLimitedProviderToolsProcessRejectsBeforeLaunch(t *testing.T) {
	binary, digest, root, port := providerProcessFixture(t)
	provider, tools := providerConfigurationFixture(ProviderProtocolOpenAIResponses), providerToolsFixture()
	provider.MaxOutputTokens = 0
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	process, _, evidence, err := StartReadyLimitedProviderToolsProcess(
		ctx, binary, digest, root, port, "user", "password", io.Discard,
		1, 250*time.Millisecond, false, provider, tools,
	)
	if err == nil || process != nil || evidence != nil {
		t.Fatal("invalid provider configuration reached process launch")
	}
	if _, err := os.Stat(filepath.Join(root, ".first-startup-attempt")); !os.IsNotExist(err) {
		t.Fatal("prelaunch rejection started the helper process")
	}
}

func TestProviderProcessIdentityRejectsLegacyAndUnsafeCapability(t *testing.T) {
	if _, err := (&Process{}).ProviderIdentity(); err == nil {
		t.Fatal("unadmitted legacy process returned provider identity")
	}
	if _, err := ProviderCapabilitySHA256("short"); err == nil {
		t.Fatal("unsafe provider capability was hashed")
	}
	provider := providerConfigurationFixture(ProviderProtocolOpenAIResponses)
	digest, err := ProviderCapabilitySHA256(provider.GatewayCapability)
	if err != nil || len(digest) != 64 || strings.Contains(digest, provider.GatewayCapability) {
		t.Fatal("valid provider capability digest unavailable")
	}
}

func TestStartReadyLimitedProviderToolsProcessBindsAnthropicBudget(t *testing.T) {
	binary, digest, root, port := providerProcessFixture(t)
	provider, tools := providerConfigurationFixture(ProviderProtocolAnthropic), providerToolsFixture()
	budget := int64(256)
	provider.Reasoning = true
	provider.Variant = ProviderVariantSpec{Name: "high", Anthropic: &AnthropicVariantOptions{
		ThinkingMode: "enabled", ThinkingBudgetTokens: &budget, Effort: "high",
	}}
	content, err := BuildProviderToolsConfiguration(provider, tools)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".first-startup-attempt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".helper-config"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	process, receipt, evidence, err := StartReadyLimitedProviderToolsProcess(
		ctx, binary, digest, root, port, "user", "password", io.Discard,
		1, time.Second, false, provider, tools,
	)
	if err != nil {
		t.Fatalf("Anthropic provider process startup failed: %v; evidence: %+v", err, evidence)
	}
	defer func() { _ = process.Close() }()
	identity, err := process.ProviderIdentity()
	if err != nil || receipt.MaxOutputTokens != 1024 || receipt.RuntimeOutputTokens != 768 ||
		identity.MaxOutputTokens != 1024 || identity.RuntimeOutputTokens != 768 ||
		identity.ThinkingMode != "enabled" || identity.ThinkingBudgetTokens == nil || *identity.ThinkingBudgetTokens != budget ||
		identity.AnthropicEffort != "high" || identity.SDKVersion != "3.0.111" {
		t.Fatalf("Anthropic process identity mismatch: %+v: %v", identity, err)
	}
	outputLimitEntries := 0
	for _, entry := range process.command.Env {
		if strings.HasPrefix(entry, "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX=") {
			outputLimitEntries++
			if entry != "OPENCODE_EXPERIMENTAL_OUTPUT_TOKEN_MAX=768" {
				t.Fatal("Anthropic runtime output did not subtract thinking budget")
			}
		}
	}
	if outputLimitEntries != 1 {
		t.Fatal("Anthropic derived runtime output limit missing or duplicated")
	}
}

func TestStartReadyLimitedProviderToolsProcessRejectsMismatchAndReaps(t *testing.T) {
	binary, digest, root, port := providerProcessFixture(t)
	provider, tools := providerConfigurationFixture(ProviderProtocolOpenAIResponses), providerToolsFixture()
	if err := os.WriteFile(filepath.Join(root, ".first-startup-attempt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	substitute := provider
	substitute.ModelID = "substituted-model"
	content, err := BuildProviderToolsConfiguration(substitute, tools)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".helper-config"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	process, receipt, evidence, err := StartReadyLimitedProviderToolsProcess(
		ctx, binary, digest, root, port, "user", "password", io.Discard,
		1, time.Second, false, provider, tools,
	)
	if err == nil || process != nil || receipt.SHA256 != "" || len(evidence) != 1 ||
		evidence[0].Outcome != "configuration_rejected" || !evidence[0].Reaped ||
		evidence[0].Configuration.SHA256 != "" {
		t.Fatalf("provider substitution did not fail closed and reap: %v; evidence: %+v", err, evidence)
	}
}

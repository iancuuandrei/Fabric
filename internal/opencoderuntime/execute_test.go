package opencoderuntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
	"harness.local/engorch/internal/providercredential"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
	"harness.local/engorch/internal/runtime"
)

func TestPrepareExecutionRejectsNonSSEProviderFramingBeforeResources(t *testing.T) {
	cfg := ExecuteConfig{
		RequestExpectation: providergateway.AdapterRequestExpectation{ResponseFraming: providergateway.ResponseFramingJSON},
		ProviderRole:       config.ResolvedProviderRole{ResponseFraming: providergateway.ResponseFramingJSON},
	}
	if _, err := prepareExecution(cfg); err == nil || err.Error() != "OpenCode runtime requires SSE provider framing" {
		t.Fatal("OpenCode did not reject finite JSON framing at its boundary", err)
	}
}

func TestExecuteRecoversOnlyFromExistingRuntimeHistory(t *testing.T) {
	fixture := newRuntimeFixture(t)
	beforeSession, err := os.ReadFile(fixture.paths.Session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Execute(context.Background(), ExecuteConfig{RuntimePath: fixture.path, Intent: fixture.intent}); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("unsealed history did not require offline recovery", err)
	}
	afterSession, err := os.ReadFile(fixture.paths.Session)
	if err != nil || string(afterSession) != string(beforeSession) {
		t.Fatal("recovery-only execution mutated the existing session", err)
	}

	writeTerminalToolTurn(t, &fixture)
	beforeDispatch, err := os.ReadFile(fixture.paths.Dispatch)
	if err != nil {
		t.Fatal(err)
	}
	record, err := Execute(context.Background(), ExecuteConfig{RuntimePath: fixture.path, Intent: fixture.intent})
	if err != nil || record.Result.Output != "tool result accepted" {
		t.Fatal("sealed execution did not complete journal-only", record, err)
	}
	afterDispatch, err := os.ReadFile(fixture.paths.Dispatch)
	if err != nil || string(afterDispatch) != string(beforeDispatch) {
		t.Fatal("completed recovery mutated dispatch evidence", err)
	}
}

func TestExecuteRejectsCanceledFreshAttemptBeforeRecordingIntent(t *testing.T) {
	fixture := newRuntimeFixture(t)
	path := filepath.Join(t.TempDir(), "fresh-runtime.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Execute(ctx, ExecuteConfig{RuntimePath: path, Intent: fixture.intent}); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled fresh attempt was not rejected", err)
	}
	events, err := journal.Read(path)
	if err != nil || len(events) != 0 {
		t.Fatal("canceled attempt recorded runtime intent", events, err)
	}
}

func TestPrepareExecutionBindsProviderToolsToSelectedContextCatalog(t *testing.T) {
	fixture := newRuntimeFixture(t)
	catalogSHA256, err := ContextCatalogSHA256(fixture.broker)
	again, secondErr := ContextCatalogSHA256(fixture.broker)
	if err != nil || secondErr != nil || len(catalogSHA256) != 64 || catalogSHA256 != again {
		t.Fatal("controller catalog identity is not exact and deterministic", catalogSHA256, again, err, secondErr)
	}
	projected, err := providerToolsForSession(fixture.broker.Catalog(), fixture.intent.Session.ToolNames)
	if err != nil || len(projected) != 1 || projected[0].Name != "engorch_source_list" {
		t.Fatal("exact pinned provider tool projection unavailable", projected, err)
	}
	requestTools, err := ProviderRequestToolsForSession(fixture.broker.Catalog(), fixture.intent.Session.ToolNames)
	if err != nil || len(requestTools) != 1 || requestTools[0].Name != projected[0].Name || !bytes.Equal(requestTools[0].Parameters, projected[0].InputSchema) {
		t.Fatal("provider request tool projection differs", requestTools, err)
	}
	requestTools[0].Parameters[0] ^= 1
	againTools, err := ProviderRequestToolsForSession(fixture.broker.Catalog(), fixture.intent.Session.ToolNames)
	if err != nil || bytes.Equal(requestTools[0].Parameters, againTools[0].Parameters) {
		t.Fatal("provider request tool schema was not copied", err)
	}
	paths := fixture.paths
	paths.Gateway = filepath.Join(t.TempDir(), "gateway.jsonl")
	cfg := ExecuteConfig{
		RuntimePath:        filepath.Join(t.TempDir(), "runtime.jsonl"),
		StateRoot:          t.TempDir(),
		Paths:              paths,
		Intent:             fixture.intent,
		Credential:         &providercredential.Lease{},
		Transport:          &providertransport.Client{},
		Broker:             fixture.broker,
		Port:               43191,
		ParentMessageID:    "msg_user",
		ProviderTimeout:    time.Second,
		ToolTimeout:        time.Second,
		ReadinessTimeout:   time.Second,
		SealTimeout:        time.Second,
		RequestExpectation: providergateway.AdapterRequestExpectation{Tools: []providergateway.RequestTool{{Name: projected[0].Name, Description: "substituted", Parameters: projected[0].InputSchema}}},
	}
	if _, err := prepareExecution(cfg); err == nil || !strings.Contains(err.Error(), "provider tools differ") {
		t.Fatal("substituted provider tool schema admitted", err)
	}
	events, err := journal.Read(cfg.RuntimePath)
	if err != nil || len(events) != 0 {
		t.Fatal("configuration rejection recorded a runtime effect", events, err)
	}
}

func TestProviderConfigurationSeparatesWireCapFromRuntimeThinkingBudget(t *testing.T) {
	budget := int64(256)
	tests := []struct {
		name         string
		adapter      string
		capabilities providergateway.ModelCapabilities
		variant      config.ProviderVariant
		wantRuntime  int64
	}{
		{
			name:         "anthropic enabled thinking",
			adapter:      providergateway.AnthropicMessagesAdapter,
			capabilities: providergateway.ModelCapabilities{Tools: true, Reasoning: true},
			variant:      config.ProviderVariant{Effort: "high", ThinkingMode: "enabled", ThinkingBudgetTokens: &budget},
			wantRuntime:  768,
		},
		{
			name:         "responses reasoning",
			adapter:      providergateway.OpenAIResponsesAdapter,
			capabilities: providergateway.ModelCapabilities{Tools: true, Reasoning: true},
			variant:      config.ProviderVariant{Effort: "high", ReasoningSummary: "auto"},
			wantRuntime:  1024,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocation := runtimeInvocationFixture(t, test.variant.Effort)
			controls := json.RawMessage(nil)
			if test.adapter == providergateway.OpenAIResponsesAdapter {
				var err error
				controls, err = canonical.Bytes(providergateway.ResponsesRequestExpectation{MaxOutputTokens: 1024, StateMode: "full-input-stateless", SystemRole: "developer", ReasoningEffort: "high", ReasoningSummary: "auto", Include: []string{"reasoning.encrypted_content"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			cfg := ExecuteConfig{
				Intent: Intent{Version: 2, Invocation: invocation, Directory: t.TempDir(), SessionPlan: &SessionPlan{
					Agent: "build", Provider: invocation.Profile.Provider, Model: invocation.Profile.Model, Variant: invocation.Profile.Effort,
					ToolNames: []string{"source_list"}, CatalogSHA256: strings.Repeat("a", 64),
				}},
				Gateway:            providergateway.Binding{Model: providergateway.ModelContract{AdapterID: test.adapter, ContextWindowTokens: 8192, Capabilities: &test.capabilities}},
				ProviderRole:       config.ResolvedProviderRole{Variant: test.variant},
				RequestExpectation: providergateway.AdapterRequestExpectation{MaxOutputTokens: 1024, Controls: controls},
				ProviderTimeout:    time.Second,
				ToolTimeout:        time.Second,
			}
			spec, err := providerConfiguration(cfg, "http://127.0.0.1:43192/v1", strings.Repeat("a", 32))
			if err != nil {
				t.Fatal(err)
			}
			content, err := opencode.BuildProviderToolsConfiguration(spec, opencode.ToolsConfigurationSpec{Endpoint: "http://127.0.0.1:43193/mcp", Bearer: strings.Repeat("b", 32), ToolNames: []string{"source_list"}, TimeoutMillis: 1000})
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Provider map[string]struct {
					Models map[string]struct {
						Limit struct {
							Output int64 `json:"output"`
						} `json:"limit"`
					} `json:"models"`
				} `json:"provider"`
			}
			if err := json.Unmarshal([]byte(content), &decoded); err != nil {
				t.Fatal(err)
			}
			model := decoded.Provider[spec.ProviderID].Models[spec.ModelID]
			if spec.MaxOutputTokens != 1024 || model.Limit.Output != test.wantRuntime {
				t.Fatalf("wire/runtime output caps collapsed: wire=%d runtime=%d", spec.MaxOutputTokens, model.Limit.Output)
			}
		})
	}
}

func TestProviderConfigurationOmitsEmptyResponsesOptionsForNonreasoning(t *testing.T) {
	cfg := responsesRuntimeConfiguration(t, providergateway.ResponsesRequestExpectation{
		MaxOutputTokens: 1024,
		StateMode:       "full-input-stateless",
		SystemRole:      "developer",
	}, config.ProviderVariant{Effort: "none"})
	spec, err := providerConfiguration(cfg, "http://127.0.0.1:43192/v1", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal("default nonreasoning Responses configuration was rejected", err)
	}
	if spec.Reasoning || spec.Variant.Name != "none" || spec.Variant.Responses != nil {
		t.Fatalf("default nonreasoning Responses configuration retained empty options: %+v", spec.Variant)
	}
	if _, err := opencode.BuildProviderToolsConfiguration(spec, opencode.ToolsConfigurationSpec{
		Endpoint: "http://127.0.0.1:43193/mcp", Bearer: strings.Repeat("b", 32), ToolNames: []string{"source_list"}, TimeoutMillis: 1000,
	}); err != nil {
		t.Fatal("full OpenCode provider configuration rejected default nonreasoning Responses", err)
	}
}

func TestProviderConfigurationPreservesResponsesOptionsAndRejectsInvalidReasoning(t *testing.T) {
	// This full-configuration fixture uses canonical integer-domain sampling
	// values. Fractional json.Number controls are outside this envelope and are
	// covered separately by the provider configuration codec.
	temperature, topP := json.Number("1"), json.Number("1")
	controls := providergateway.ResponsesRequestExpectation{
		MaxOutputTokens: 1024,
		StateMode:       "full-input-stateless",
		SystemRole:      "developer",
		TextVerbosity:   "high",
		Temperature:     &temperature,
		TopP:            &topP,
	}
	cfg := responsesRuntimeConfiguration(t, controls, config.ProviderVariant{Effort: "none", TextVerbosity: "high"})
	spec, err := providerConfiguration(cfg, "http://127.0.0.1:43192/v1", strings.Repeat("a", 32))
	if err != nil {
		t.Fatal("explicit nonreasoning Responses controls were rejected", err)
	}
	options := spec.Variant.Responses
	if options == nil || options.TextVerbosity != "high" || options.Temperature == nil || options.Temperature.String() != "1" || options.TopP == nil || options.TopP.String() != "1" {
		t.Fatalf("explicit Responses controls changed: %+v", options)
	}
	if _, err := opencode.BuildProviderToolsConfiguration(spec, opencode.ToolsConfigurationSpec{
		Endpoint: "http://127.0.0.1:43193/mcp", Bearer: strings.Repeat("b", 32), ToolNames: []string{"source_list"}, TimeoutMillis: 1000,
	}); err != nil {
		t.Fatal("full OpenCode provider configuration rejected explicit Responses controls", err)
	}

	invalid := responsesRuntimeConfiguration(t, providergateway.ResponsesRequestExpectation{
		MaxOutputTokens: 1024,
		StateMode:       "full-input-stateless",
		SystemRole:      "developer",
		ReasoningEffort: "high",
	}, config.ProviderVariant{Effort: "high"})
	if _, err := providerConfiguration(invalid, "http://127.0.0.1:43192/v1", strings.Repeat("a", 32)); err == nil || !strings.Contains(err.Error(), "invalid OpenAI Responses reasoning variant") {
		t.Fatal("invalid Responses reasoning controls did not fail through full configuration admission", err)
	}
}

func responsesRuntimeConfiguration(t *testing.T, controls providergateway.ResponsesRequestExpectation, variant config.ProviderVariant) ExecuteConfig {
	t.Helper()
	invocation := runtimeInvocationFixture(t, variant.Effort)
	if controls.Include == nil {
		controls.Include = []string{}
	}
	raw, err := canonical.Bytes(controls)
	if err != nil {
		t.Fatal(err)
	}
	return ExecuteConfig{
		Intent: Intent{Version: 2, Invocation: invocation, Directory: t.TempDir(), SessionPlan: &SessionPlan{
			Agent: "build", Provider: invocation.Profile.Provider, Model: invocation.Profile.Model, Variant: invocation.Profile.Effort,
			ToolNames: []string{"source_list"}, CatalogSHA256: strings.Repeat("a", 64),
		}},
		Gateway: providergateway.Binding{Model: providergateway.ModelContract{
			AdapterID: providergateway.OpenAIResponsesAdapter, ContextWindowTokens: 8192,
			Capabilities: &providergateway.ModelCapabilities{Tools: true, Reasoning: controls.ReasoningEffort != ""},
		}},
		ProviderRole:       config.ResolvedProviderRole{Variant: variant},
		RequestExpectation: providergateway.AdapterRequestExpectation{MaxOutputTokens: 1024, Controls: raw},
		ProviderTimeout:    time.Second,
		ToolTimeout:        time.Second,
	}
}

func runtimeInvocationFixture(t *testing.T, effort string) runtime.Invocation {
	t.Helper()
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "engorch-fixture", Model: "wire-model", Effort: effort, Role: "explorer"}, "inspect the repository")
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

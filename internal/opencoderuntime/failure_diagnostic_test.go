package opencoderuntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/opencode"
	"github.com/iancuuandrei/fabric/internal/providergateway"
)

func diagnosticCause(t *testing.T) (error, opencode.DispatchDiagnostic) {
	t.Helper()
	client, err := opencode.NewClient("http://127.0.0.1:1", "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cause := client.SubmitSynchronousToolTurn(ctx, "", "", opencode.SynchronousToolDispatchIntent{})
	diagnostic := opencode.DispatchDiagnosticFromError(cause)
	if diagnostic == nil {
		t.Fatal("missing diagnostic", cause)
	}
	return errors.Join(cause, errors.New("TOKEN=private")), *diagnostic
}

func readFailureSidecar(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private") {
		t.Fatal("failure artifact leaked provider text")
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

func exhaustedGatewayConfig(t *testing.T, runtimePath string) (ExecuteConfig, opencode.DispatchDiagnostic) {
	t.Helper()
	path, bound := writeGatewayTranscript(t, true)
	if bound.Gateway == nil {
		t.Fatal("exhausted gateway fixture lacks a binding")
	}
	state, err := providergateway.Inspect(path)
	if err != nil || state.Pending != nil || !state.Exhausted || state.Finished {
		t.Fatal("gateway fixture is not pending-free exhausted unfinished", state, err)
	}
	cause, original := diagnosticCause(t)
	cfg := ExecuteConfig{RuntimePath: runtimePath}
	cfg.Intent.Invocation.ID = strings.Repeat("a", 64)
	cfg.Intent.ProviderGatewayBindingID = bound.Gateway.BindingID
	cfg.Gateway = bound.Gateway.Binding
	cfg.Paths.Gateway = path
	_ = cause
	return cfg, original
}

func TestDispatchFailureSidecarReportsExhaustedGatewayBudget(t *testing.T) {
	runtimePath := filepath.Join(t.TempDir(), "runtime.jsonl")
	cfg, original := exhaustedGatewayConfig(t, runtimePath)
	cause, _ := diagnosticCause(t)
	if err := preserveDispatchDiagnostic(cfg, cause); err != nil {
		t.Fatal(err)
	}
	path := runtimePath + ".failure.json"
	record := readFailureSidecar(t, path)
	if record["status"] != "UNKNOWN" || record["invocation_id"] != cfg.Intent.Invocation.ID {
		t.Fatal("sidecar lost UNKNOWN status or invocation identity", record)
	}
	diagnostic, ok := record["diagnostic"].(map[string]any)
	if !ok || diagnostic["stage"] != original.Stage || diagnostic["code"] != "provider_call_budget_exhausted" {
		t.Fatal("exhausted call budget unexplained or stage changed", record)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := preserveDispatchDiagnostic(cfg, cause); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("first exhausted diagnostic overwritten")
	}
	beforeGateway, err := os.ReadFile(cfg.Paths.Gateway)
	if err != nil {
		t.Fatal(err)
	}
	state, err := providergateway.Inspect(cfg.Paths.Gateway)
	if err != nil || !state.Exhausted {
		t.Fatal("diagnostic mutated the gateway journal", state, err)
	}
	afterGateway, _ := os.ReadFile(cfg.Paths.Gateway)
	if string(beforeGateway) != string(afterGateway) {
		t.Fatal("diagnostic wrote to the gateway journal")
	}
	if _, err := os.Stat(runtimePath); !os.IsNotExist(err) {
		t.Fatal("diagnostic fabricated a runtime journal")
	}
}

func TestDispatchFailureSidecarRetainsDiagnosticWithoutValidatedExhaustion(t *testing.T) {
	cause, original := diagnosticCause(t)
	exhaustedPath, exhausted := writeGatewayTranscript(t, true)
	if exhausted.Gateway == nil {
		t.Fatal("exhausted gateway fixture lacks a binding")
	}
	finishedPath, finished := writeGatewayTranscript(t, false)
	if finished.Gateway == nil {
		t.Fatal("finished gateway fixture lacks a binding")
	}
	corruptPath := filepath.Join(t.TempDir(), "corrupt-gateway.jsonl")
	if err := os.WriteFile(corruptPath, []byte("not a journal\n"), 0600); err != nil {
		t.Fatal(err)
	}
	pendingPath := filepath.Join(t.TempDir(), "pending-gateway.jsonl")
	appendUnchecked(t, pendingPath, "provider.bound", exhausted.Gateway.Binding)
	pendingIntent := providergateway.CallIntent{Version: 1, Sequence: 1, BindingID: exhausted.Gateway.BindingID, InvocationID: exhausted.Gateway.Binding.AccessInvocationID, RouteID: exhausted.Gateway.Binding.RouteID, EndpointID: exhausted.Gateway.Binding.EndpointID, ModelID: exhausted.Gateway.Binding.ModelID, RequestSHA256: strings.Repeat("d", 64), RequestBytes: 128, MaxOutputTokens: 16, RequestExpectationID: strings.Repeat("e", 64)}
	var err error
	pendingIntent.CallID, err = pendingIntent.ID()
	if err != nil {
		t.Fatal(err)
	}
	appendUnchecked(t, pendingPath, "provider.call-intent", pendingIntent)
	if state, err := providergateway.Inspect(pendingPath); err != nil || state.Pending == nil || state.Exhausted || state.Finished {
		t.Fatal("pending gateway fixture mismatch", state, err)
	}

	cases := []struct {
		name   string
		mutate func(*ExecuteConfig)
	}{
		{"missing gateway path", func(cfg *ExecuteConfig) { cfg.Paths.Gateway = "" }},
		{"corrupt gateway journal", func(cfg *ExecuteConfig) { cfg.Paths.Gateway = corruptPath }},
		{"foreign binding digest", func(cfg *ExecuteConfig) { cfg.Intent.ProviderGatewayBindingID = strings.Repeat("c", 64) }},
		{"foreign gateway binding", func(cfg *ExecuteConfig) {
			cfg.Gateway = finished.Gateway.Binding
			cfg.Intent.ProviderGatewayBindingID = finished.Gateway.BindingID
			cfg.Paths.Gateway = exhaustedPath
		}},
		{"finished gateway", func(cfg *ExecuteConfig) {
			cfg.Gateway = finished.Gateway.Binding
			cfg.Intent.ProviderGatewayBindingID = finished.Gateway.BindingID
			cfg.Paths.Gateway = finishedPath
		}},
		{"pending gateway", func(cfg *ExecuteConfig) { cfg.Paths.Gateway = pendingPath }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			cfg := ExecuteConfig{RuntimePath: filepath.Join(t.TempDir(), "runtime.jsonl")}
			cfg.Intent.Invocation.ID = strings.Repeat("a", 64)
			cfg.Intent.ProviderGatewayBindingID = exhausted.Gateway.BindingID
			cfg.Gateway = exhausted.Gateway.Binding
			cfg.Paths.Gateway = exhaustedPath
			test.mutate(&cfg)
			if err := preserveDispatchDiagnostic(cfg, cause); err != nil {
				t.Fatal(err)
			}
			record := readFailureSidecar(t, cfg.RuntimePath+".failure.json")
			diagnostic, ok := record["diagnostic"].(map[string]any)
			if !ok || diagnostic["stage"] != original.Stage || diagnostic["code"] != original.Code {
				t.Fatal("nearby gateway state changed the existing diagnostic", record)
			}
			if record["status"] != "UNKNOWN" {
				t.Fatal("sidecar lost UNKNOWN status", record)
			}
		})
	}
}

func TestDispatchFailureSidecarPreservesFirstDiagnosticOnly(t *testing.T) {
	client, err := opencode.NewClient("http://127.0.0.1:1", "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cause := client.SubmitSynchronousToolTurn(ctx, "", "", opencode.SynchronousToolDispatchIntent{})
	if opencode.DispatchDiagnosticFromError(cause) == nil {
		t.Fatal("missing diagnostic", cause)
	}
	cfg := ExecuteConfig{RuntimePath: filepath.Join(t.TempDir(), "runtime.jsonl")}
	cfg.Intent.Invocation.ID = strings.Repeat("a", 64)
	cause = errors.Join(cause, errors.New("TOKEN=private"))
	if err := preserveDispatchDiagnostic(cfg, cause); err != nil {
		t.Fatal(err)
	}
	path := cfg.RuntimePath + ".failure.json"
	first, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(first), "private") || !strings.Contains(string(first), `"status":"UNKNOWN"`) || !strings.Contains(string(first), cfg.Intent.Invocation.ID) {
		t.Fatal("unsafe failure artifact", err, string(first))
	}
	if err := preserveDispatchDiagnostic(cfg, errors.New("plain failure")); err != nil {
		t.Fatal(err)
	}
	if err := preserveDispatchDiagnostic(cfg, cause); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(path)
	if string(first) != string(again) {
		t.Fatal("first diagnostic overwritten")
	}
	if _, err := os.Stat(cfg.RuntimePath); !os.IsNotExist(err) {
		t.Fatal("diagnostic fabricated a runtime journal")
	}
}

package opencoderuntime

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/iancuuandrei/fabric/internal/opencode"
	"github.com/iancuuandrei/fabric/internal/providergateway"
)

// A sidecar preserves explanatory labels after the process exits. It is never
// read by replay, receipt admission, recovery, or provider dispatch.
func preserveDispatchDiagnostic(cfg ExecuteConfig, cause error) error {
	diagnostic := opencode.DispatchDiagnosticFromError(cause)
	if diagnostic == nil {
		return nil
	}
	if refined := exhaustedGatewayDiagnostic(cfg, *diagnostic); refined != nil {
		diagnostic = refined
	}
	record := struct {
		Version      int                         `json:"version"`
		InvocationID string                      `json:"invocation_id"`
		Status       string                      `json:"status"`
		Diagnostic   opencode.DispatchDiagnostic `json:"diagnostic"`
	}{1, cfg.Intent.Invocation.ID, "UNKNOWN", *diagnostic}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	// Exclusive creation preserves the first failure and refuses existing links.
	f, err := os.OpenFile(cfg.RuntimePath+".failure.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	return errors.Join(writeErr, f.Sync(), f.Close())
}

// exhaustedGatewayDiagnostic explains a validated provider call-budget limit
// without granting retry, resume, or reconciliation authority. It returns nil
// unless the inspected gateway journal validates, its binding canonically
// equals the configured gateway, the derived binding ID exactly matches the
// intent binding, and the terminal state is pending-free, exhausted, and
// unfinished. Every other state retains the existing diagnostic. No HTTP
// status, error text, or receipt count is inferred.
func exhaustedGatewayDiagnostic(cfg ExecuteConfig, current opencode.DispatchDiagnostic) *opencode.DispatchDiagnostic {
	if cfg.Paths.Gateway == "" {
		return nil
	}
	bindingID, err := cfg.Gateway.ID()
	if err != nil || bindingID == "" || bindingID != cfg.Intent.ProviderGatewayBindingID {
		return nil
	}
	state, err := providergateway.Inspect(cfg.Paths.Gateway)
	if err != nil || state.Binding == nil {
		return nil
	}
	if !equalCanonical(*state.Binding, cfg.Gateway) {
		return nil
	}
	if state.Pending != nil || !state.Exhausted || state.Finished {
		return nil
	}
	refined := current
	refined.Code = "provider_call_budget_exhausted"
	return &refined
}

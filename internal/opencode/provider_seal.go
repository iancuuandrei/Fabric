package opencode

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/contextmcp"
	"harness.local/engorch/internal/safepath"
)

// ProviderProxyIdentity is the secret-free identity of the exact owned local
// provider capability used by an admitted OpenCode process.
type ProviderProxyIdentity struct {
	Version            int    `json:"version"`
	SHA256             string `json:"sha256"`
	BindingSHA256      string `json:"binding_sha256"`
	CapabilitySHA256   string `json:"capability_sha256"`
	URL                string `json:"url"`
	BaseURL            string `json:"base_url"`
	AdapterID          string `json:"adapter_id"`
	AccessInvocationID string `json:"access_invocation_id"`
	GatewayBindingID   string `json:"gateway_binding_id"`
}

// ValidateProviderProxyIdentity verifies a copied provider proxy identity.
func ValidateProviderProxyIdentity(identity ProviderProxyIdentity) error {
	for _, digest := range []string{identity.SHA256, identity.BindingSHA256, identity.CapabilitySHA256, identity.AccessInvocationID, identity.GatewayBindingID} {
		if safepath.RequireDigest(digest) != nil {
			return errors.New("invalid provider proxy identity")
		}
	}
	if identity.Version != 1 || identity.AdapterID == "" || !validExactProviderProxyURL(identity.URL) || !validExactProviderProxyBaseURL(identity.BaseURL) || !strings.HasPrefix(identity.URL, identity.BaseURL+"/") {
		return errors.New("invalid provider proxy identity")
	}
	want := identity.SHA256
	identity.SHA256 = ""
	digest, err := canonical.Hash("harness.opencode-provider-proxy-identity.v1", identity)
	if err != nil || digest != want {
		return errors.New("invalid provider proxy identity digest")
	}
	return nil
}

// ProviderProxyLifecycle is implemented by the exact owned proxy listener.
// Identity validates the scoped capability without exposing it in receipts.
type ProviderProxyLifecycle interface {
	ProviderSealIdentity(string) (ProviderProxyIdentity, error)
	Close(context.Context) error
	Wait() error
}

type providerSealLive struct {
	proxy      ProviderProxyLifecycle
	capability string
}

// SealProviderSynchronousToolTurn seals a provider-backed tool turn while also
// stopping and binding the exact owned provider proxy before process reap.
func SealProviderSynchronousToolTurn(ctx context.Context, sealPath, dispatchPath, brokerPath string, expected SynchronousToolTurnSealExpected, toolsSpec ToolsConfigurationSpec, client *Client, process *Process, running *contextmcp.OwnedRunning, broker *contextbroker.Broker, proxy ProviderProxyLifecycle, capability string) (ToolTurnTerminalReceipt, error) {
	if expected.Provider == nil || proxy == nil {
		return ToolTurnTerminalReceipt{}, errors.New("provider seal expectation and proxy required")
	}
	return sealSynchronousToolTurn(ctx, sealPath, dispatchPath, brokerPath, expected, toolsSpec, client, process, running, broker, &providerSealLive{proxy: proxy, capability: capability})
}

// ProviderToolTurnSealExpected extends the ordinary tool-turn seal with exact
// admitted provider-process and local-proxy identities.
type ProviderToolTurnSealExpected struct {
	Process ProviderProcessIdentity `json:"process"`
	Proxy   ProviderProxyIdentity   `json:"proxy"`
}

func validateProviderToolTurnSealExpected(expected ProviderToolTurnSealExpected, base SynchronousToolTurnSealExpected) error {
	if ValidateProviderProcessIdentity(expected.Process) != nil || ValidateProviderProxyIdentity(expected.Proxy) != nil || expected.Process.CapabilitySHA256 != expected.Proxy.CapabilitySHA256 || expected.Process.GatewayBaseURL != expected.Proxy.BaseURL || string(expected.Process.Protocol) != expected.Proxy.AdapterID || expected.Process.ProviderID != base.Session.Session.Provider || expected.Process.ModelID != base.Session.Session.Model || expected.Process.MaxOutputTokens != base.MaxOutputTokens || expected.Process.RuntimeOutputTokens != base.RuntimeOutputTokens || !equalCanonical(expected.Process.Configuration.ToolsConfiguration, base.Tools) {
		return errors.New("provider seal identity differs from tool turn")
	}
	return nil
}

func snapshotProviderToolTurnSealExpected(expected *ProviderToolTurnSealExpected) *ProviderToolTurnSealExpected {
	if expected == nil {
		return nil
	}
	copy := *expected
	copy.Process.Configuration.ToolsConfiguration.ToolIDs = append([]string(nil), expected.Process.Configuration.ToolsConfiguration.ToolIDs...)
	if expected.Process.ThinkingBudgetTokens != nil {
		budget := *expected.Process.ThinkingBudgetTokens
		copy.Process.ThinkingBudgetTokens = &budget
	}
	if expected.Process.Configuration.ThinkingBudgetTokens != nil {
		budget := *expected.Process.Configuration.ThinkingBudgetTokens
		copy.Process.Configuration.ThinkingBudgetTokens = &budget
	}
	return &copy
}

func validateProviderSealLive(process *Process, proxy ProviderProxyLifecycle, capability string, expected ProviderToolTurnSealExpected, base SynchronousToolTurnSealExpected) error {
	if process == nil || proxy == nil || validateProviderToolTurnSealExpected(expected, base) != nil {
		return errors.New("incomplete provider seal ownership")
	}
	processIdentity, err := process.ProviderIdentity()
	if err != nil || !equalCanonical(processIdentity, expected.Process) {
		return errors.New("provider process identity changed before seal")
	}
	proxyIdentity, err := proxy.ProviderSealIdentity(capability)
	if err != nil || !equalCanonical(proxyIdentity, expected.Proxy) {
		return errors.New("provider proxy identity changed before seal")
	}
	return nil
}

func validExactProviderProxyURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawPath != "" || parsed.Hostname() != "127.0.0.1" || parsed.Path == "" || parsed.Path == "/" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && port > 0 && port <= 65535 && net.ParseIP(parsed.Hostname()) != nil && parsed.Host == "127.0.0.1:"+strconv.Itoa(port) && parsed.String() == value
}

func validExactProviderProxyBaseURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Path != "/v1" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.User != nil || parsed.Opaque != "" || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	return err == nil && port > 0 && port <= 65535 && parsed.Host == "127.0.0.1:"+strconv.Itoa(port) && parsed.String() == value
}

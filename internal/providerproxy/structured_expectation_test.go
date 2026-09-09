package providerproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/providergateway"
	"harness.local/engorch/internal/providertransport"
)

func TestFreezeConfigDetachesTerminalSchemaAndRequiredCapabilities(t *testing.T) {
	f := newProxyFixture(t)
	expected := f.expectation
	expected.TerminalStructuredOutput = &providergateway.TerminalStructuredOutputExpectation{
		Version: 1, Name: providergateway.StructuredOutputToolName,
		Schema: json.RawMessage(`{"type":"object"}`), SchemaSHA256: proxyDigest(`{"type":"object"}`),
	}
	expected.RequiredCapabilities = &providergateway.RequiredCapabilities{
		Tools: true, Schema: json.RawMessage(`{"type":"object"}`),
	}
	_, _, _, frozen, err := freezeConfig(Config{Policy: f.policy, Intent: f.intent, Gateway: f.gateway, Expectation: expected})
	if err != nil {
		t.Fatal(err)
	}
	before, err := canonical.Bytes(frozen)
	if err != nil {
		t.Fatal(err)
	}
	expected.TerminalStructuredOutput.Name = "substituted"
	expected.TerminalStructuredOutput.Schema[0] = '['
	expected.RequiredCapabilities.Tools = false
	expected.RequiredCapabilities.Schema[0] = '['
	after, err := canonical.Bytes(frozen)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("caller mutation changed frozen provider expectation", err)
	}
}

func TestProxyRejectsChangedExpectationBeforeProviderDispatch(t *testing.T) {
	f := newProxyFixture(t)
	var calls atomic.Int32
	f.server.execute = func(context.Context, providertransport.Request) (providertransport.Result, error) {
		calls.Add(1)
		return providertransport.Result{}, providertransport.ErrRejected
	}
	// Simulate internal substitution after binding; external mutation is
	// separately prevented by the detached configuration test above.
	f.server.expectation.MaxOutputTokens++
	running, err := f.server.Listen()
	if err != nil {
		t.Fatal(err)
	}
	defer closeProxy(t, running)
	status, _ := proxyPost(t, running.URL(), proxyBearer, f.body, nil)
	state, inspectErr := providergateway.Inspect(f.gatewayPath)
	if status != http.StatusConflict || calls.Load() != 0 || inspectErr != nil || state.Pending != nil || len(state.Calls) != 0 {
		t.Fatal("changed expectation reached provider admission", status, calls.Load(), inspectErr)
	}
}

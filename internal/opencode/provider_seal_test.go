package opencode

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"harness.local/engorch/internal/contextbroker"
)

type failingSealProxy struct {
	closed atomic.Int32
	waited atomic.Int32
}

func (*failingSealProxy) ProviderSealIdentity(string) (ProviderProxyIdentity, error) {
	return ProviderProxyIdentity{}, errors.New("not used")
}
func (p *failingSealProxy) Close(context.Context) error {
	p.closed.Add(1)
	return errors.New("uncertain proxy close")
}
func (p *failingSealProxy) Wait() error {
	p.waited.Add(1)
	return nil
}

func TestSnapshotProviderToolTurnSealExpectedCopiesBothThinkingBudgets(t *testing.T) {
	processBudget := int64(257)
	configurationBudget := int64(258)
	expected := ProviderToolTurnSealExpected{Process: ProviderProcessIdentity{
		ThinkingBudgetTokens: &processBudget,
		Configuration: ProviderConfigurationReceipt{
			ThinkingBudgetTokens: &configurationBudget,
			ToolsConfiguration:   ToolsConfigurationReceipt{ToolIDs: []string{"engorch_source_read"}},
		},
	}}

	snapshot := snapshotProviderToolTurnSealExpected(&expected)
	processBudget = 1
	configurationBudget = 2
	expected.Process.Configuration.ToolsConfiguration.ToolIDs[0] = "changed"
	if snapshot == nil || snapshot.Process.ThinkingBudgetTokens == expected.Process.ThinkingBudgetTokens || snapshot.Process.Configuration.ThinkingBudgetTokens == expected.Process.Configuration.ThinkingBudgetTokens ||
		*snapshot.Process.ThinkingBudgetTokens != 257 || *snapshot.Process.Configuration.ThinkingBudgetTokens != 258 || snapshot.Process.Configuration.ToolsConfiguration.ToolIDs[0] != "engorch_source_read" {
		t.Fatal("provider seal expectation retained mutable thinking or tool identity")
	}
}

func TestBestEffortUnsealedCleanupReapsAndDoesNotCloseBroker(t *testing.T) {
	fixture := newSealFixture(t)
	proxy := &failingSealProxy{}
	bestEffortUnsealedCleanup(fixture.client, fixture.process, fixture.running, &providerSealLive{proxy: proxy})
	select {
	case <-fixture.process.Done():
	default:
		t.Fatal("uncertain provider shutdown did not reap owned OpenCode root")
	}
	state, err := contextbroker.Inspect(fixture.brokerPath)
	if err != nil || state.Closed {
		t.Fatal("uncertain cleanup claimed terminal broker closure", state, err)
	}
	if proxy.closed.Load() != 1 || proxy.waited.Load() != 1 {
		t.Fatal("uncertain provider shutdown did not settle proxy handlers", proxy.closed.Load(), proxy.waited.Load())
	}
}

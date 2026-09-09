package opencoderuntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"harness.local/engorch/internal/agentcontrol"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/taskscheduler"
)

func TestInterruptShutdownAcceptsAdmittedBrokerCompletionAfterOpenPrefix(t *testing.T) {
	fixture := newRuntimeFixture(t)
	resources := interruptShutdownResourcesFixture(t, fixture.intent, fixture.paths.Broker)
	open, err := contextbroker.Inspect(fixture.paths.Broker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.broker.Call(context.Background(), "interrupt-settled-call", "source_list", json.RawMessage(`{"after":"","limit":1}`)); err != nil {
		t.Fatal("complete admitted broker call", err)
	}
	settled, settledID, settledHead, err := settledInterruptBroker(fixture.paths.Broker, open, resources.BrokerOpenHead)
	if err != nil || settled.Pending != nil || settled.Calls != open.Calls+1 || settledID == resources.BrokerOpenStateID || settledHead == resources.BrokerOpenHead {
		t.Fatal("completed admitted call did not settle from bound prefix", settled, settledID, settledHead, err)
	}
}

func TestInterruptShutdownInspectorRequiresExactRequestAndVerifiedReceipt(t *testing.T) {
	fixture := newRuntimeFixture(t)
	expected := interruptShutdownExpectedFixture(t, fixture.intent)
	resources := interruptShutdownResourcesFixture(t, fixture.intent, fixture.paths.Broker)

	if _, err := InspectInterruptShutdown(fixture.path, expected); err == nil || !strings.Contains(err.Error(), "not attempted") {
		t.Fatal("missing shutdown was not distinguished", err)
	}
	intent, err := recordInterruptShutdownIntent(fixture.path, expected, resources)
	if err != nil {
		t.Fatal("record shutdown intent", err)
	}
	if _, err := InspectInterruptShutdown(fixture.path, expected); err == nil || !strings.Contains(err.Error(), "remains unconfirmed") {
		t.Fatal("intent-only shutdown was accepted", err)
	}

	runtimeEvents, err := journal.Read(fixture.path)
	if err != nil || len(runtimeEvents) == 0 {
		t.Fatal("read runtime journal", err)
	}
	if err := fixture.broker.Close(); err != nil {
		t.Fatal("close fixture broker", err)
	}
	closed, err := contextbroker.Inspect(fixture.paths.Broker)
	if err != nil {
		t.Fatal("inspect closed fixture broker", err)
	}
	closedID, err := interruptBrokerStateID(closed)
	if err != nil {
		t.Fatal(err)
	}
	closedHead, err := journalHead(fixture.paths.Broker)
	if err != nil {
		t.Fatal(err)
	}
	receipt := InterruptShutdownReceipt{
		Version: 1, IntentID: intent.ID, RuntimeHead: runtimeEvents[len(runtimeEvents)-1].Hash,
		ProcessID: resources.ProcessID, ProviderProcessSHA256: resources.ProviderProcessSHA256,
		ProviderProxySHA256: resources.ProviderProxySHA256, MCPServerSHA256: resources.MCPServerSHA256,
		BrokerBindingID: resources.BrokerBindingID, BrokerOpenStateID: resources.BrokerOpenStateID,
		BrokerOpenHead: resources.BrokerOpenHead, BrokerSettledStateID: resources.BrokerOpenStateID,
		BrokerSettledHead: resources.BrokerOpenHead, BrokerClosedStateID: closedID,
		BrokerClosedHead: closedHead, MCPHandlersStopped: true,
		ProviderProxyStopped: true, ProviderHandlersSettled: true, RootProcessReaped: true, BrokerClosed: true,
	}
	if _, err := recordInterruptShutdownReceipt(fixture.path, intent, verifiedInterruptShutdown{receipt: receipt}); err == nil {
		t.Fatal("receipt was writable without the private verification capability")
	}
	stored, err := recordInterruptShutdownReceipt(fixture.path, intent, verifiedInterruptShutdown{seal: &interruptShutdownVerificationSeal{}, receipt: receipt})
	if err != nil {
		t.Fatal("record verified shutdown receipt", err)
	}
	inspected, err := InspectInterruptShutdown(fixture.path, expected)
	if err != nil || !equalCanonical(inspected, stored) {
		t.Fatal("exact shutdown receipt did not replay", inspected, err)
	}
	byRequest, err := InspectInterruptShutdownRequest(fixture.path, expected.RequestID, expected.Request)
	if err != nil || !equalCanonical(byRequest, stored) {
		t.Fatal("exact request did not resolve its shutdown receipt", byRequest, err)
	}

	changed := expected
	changed.Request.Nonce = "different"
	changed.RequestID, _ = changed.Request.ID()
	if _, err := InspectInterruptShutdown(fixture.path, changed); err == nil || !strings.Contains(err.Error(), "expectation mismatch") {
		t.Fatal("substituted interrupt request was accepted", err)
	}
	changed = expected
	changed.RequestID = strings.Repeat("9", 64)
	if _, err := InspectInterruptShutdown(fixture.path, changed); err == nil || !strings.Contains(err.Error(), "invalid interrupt shutdown expectation") {
		t.Fatal("request ID detached from exact request was accepted", err)
	}
}

func TestInterruptShutdownRejectsUnprovedOrSubstitutedResourceResults(t *testing.T) {
	fixture := newRuntimeFixture(t)
	expected := interruptShutdownExpectedFixture(t, fixture.intent)
	resources := interruptShutdownResourcesFixture(t, fixture.intent, fixture.paths.Broker)
	intent, err := recordInterruptShutdownIntent(fixture.path, expected, resources)
	if err != nil {
		t.Fatal(err)
	}
	runtimeEvents, _ := journal.Read(fixture.path)
	valid := InterruptShutdownReceipt{
		Version: 1, IntentID: intent.ID, RuntimeHead: runtimeEvents[len(runtimeEvents)-1].Hash,
		ProcessID: resources.ProcessID, ProviderProcessSHA256: resources.ProviderProcessSHA256,
		ProviderProxySHA256: resources.ProviderProxySHA256, MCPServerSHA256: resources.MCPServerSHA256,
		BrokerBindingID: resources.BrokerBindingID, BrokerOpenStateID: resources.BrokerOpenStateID,
		BrokerOpenHead: resources.BrokerOpenHead, BrokerSettledStateID: strings.Repeat("1", 64),
		BrokerSettledHead: resources.BrokerOpenHead, BrokerClosedStateID: strings.Repeat("7", 64),
		BrokerClosedHead: strings.Repeat("8", 64), MCPHandlersStopped: true,
		ProviderProxyStopped: true, ProviderHandlersSettled: true, RootProcessReaped: true, BrokerClosed: true,
	}
	cases := []struct {
		name   string
		mutate func(*InterruptShutdownReceipt)
	}{
		{"root not reaped", func(r *InterruptShutdownReceipt) { r.RootProcessReaped = false }},
		{"provider handlers unresolved", func(r *InterruptShutdownReceipt) { r.ProviderHandlersSettled = false }},
		{"broker not closed", func(r *InterruptShutdownReceipt) { r.BrokerClosed = false }},
		{"process substituted", func(r *InterruptShutdownReceipt) { r.ProcessID++ }},
		{"proxy substituted", func(r *InterruptShutdownReceipt) { r.ProviderProxySHA256 = strings.Repeat("9", 64) }},
		{"runtime prefix missing", func(r *InterruptShutdownReceipt) { r.RuntimeHead = strings.Repeat("9", 64) }},
		{"broker state unchanged", func(r *InterruptShutdownReceipt) { r.BrokerClosedStateID = r.BrokerSettledStateID }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			receipt := valid
			tc.mutate(&receipt)
			if _, err := recordInterruptShutdownReceipt(fixture.path, intent, verifiedInterruptShutdown{seal: &interruptShutdownVerificationSeal{}, receipt: receipt}); err == nil {
				t.Fatal("invalid shutdown proof was accepted")
			}
		})
	}
}

func TestInterruptShutdownSidecarRejectsForgedReceipt(t *testing.T) {
	fixture := newRuntimeFixture(t)
	expected := interruptShutdownExpectedFixture(t, fixture.intent)
	resources := interruptShutdownResourcesFixture(t, fixture.intent, fixture.paths.Broker)
	intent, err := recordInterruptShutdownIntent(fixture.path, expected, resources)
	if err != nil {
		t.Fatal(err)
	}
	runtimeEvents, _ := journal.Read(fixture.path)
	forged := InterruptShutdownReceipt{
		Version: 1, IntentID: intent.ID, RuntimeHead: runtimeEvents[len(runtimeEvents)-1].Hash,
		ProcessID: resources.ProcessID, ProviderProcessSHA256: resources.ProviderProcessSHA256,
		ProviderProxySHA256: resources.ProviderProxySHA256, MCPServerSHA256: resources.MCPServerSHA256,
		BrokerBindingID: resources.BrokerBindingID, BrokerOpenStateID: resources.BrokerOpenStateID,
		BrokerOpenHead: resources.BrokerOpenHead, BrokerSettledStateID: strings.Repeat("1", 64),
		BrokerSettledHead: resources.BrokerOpenHead, BrokerClosedStateID: strings.Repeat("7", 64),
		BrokerClosedHead: strings.Repeat("8", 64), MCPHandlersStopped: true,
		ProviderProxyStopped: true, ProviderHandlersSettled: true, RootProcessReaped: false, BrokerClosed: true,
	}
	if _, err := journal.Append(interruptShutdownPath(fixture.path), interruptShutdownReceiptEvent, forged, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal("write forged fixture", err)
	}
	if _, err := InspectInterruptShutdown(fixture.path, expected); err == nil {
		t.Fatal("forged all-caller-asserted shutdown receipt was accepted")
	}
}

func interruptShutdownExpectedFixture(t *testing.T, runtimeIntent Intent) InterruptShutdownExpected {
	t.Helper()
	request := agentcontrol.InterruptRequest{
		Version: 1, TreeID: strings.Repeat("1", 64), ScheduleID: strings.Repeat("2", 64),
		AgentTurn:    taskscheduler.AgentTurnBinding{ParentAgentID: strings.Repeat("3", 64), AgentID: strings.Repeat("4", 64), TurnID: strings.Repeat("5", 64), TurnSequence: 2},
		InvocationID: runtimeIntent.Invocation.ID, ClaimID: strings.Repeat("6", 64), ControllerHead: strings.Repeat("a", 64),
		Actor: "fixture-operator", Nonce: "fixture-request-1",
	}
	requestID, err := request.ID()
	if err != nil {
		t.Fatal(err)
	}
	return InterruptShutdownExpected{Version: 1, Runtime: runtimeIntent, RequestID: requestID, Request: request}
}

func interruptShutdownResourcesFixture(t *testing.T, runtimeIntent Intent, brokerPath string) InterruptShutdownResources {
	t.Helper()
	bindingID, err := runtimeIntent.Context.ID()
	if err != nil {
		t.Fatal(err)
	}
	open, err := contextbroker.Inspect(brokerPath)
	if err != nil {
		t.Fatal(err)
	}
	openID, err := interruptBrokerStateID(open)
	if err != nil {
		t.Fatal(err)
	}
	openHead, err := journalHead(brokerPath)
	if err != nil {
		t.Fatal(err)
	}
	return InterruptShutdownResources{
		Version: 1, ProcessID: 1234, ProviderProcessSHA256: strings.Repeat("b", 64),
		ProviderProxySHA256: strings.Repeat("c", 64), MCPServerSHA256: strings.Repeat("d", 64),
		BrokerPath: brokerPath, BrokerBindingID: bindingID, BrokerOpenStateID: openID, BrokerOpenHead: openHead,
	}
}

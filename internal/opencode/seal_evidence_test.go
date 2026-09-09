package opencode

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/journal"
)

func TestRecoverSynchronousToolTurnSealEvidenceReturnsExactObservationAndReceipt(t *testing.T) {
	fixture := newSealFixture(t)
	before, err := fixture.client.RecoverSynchronousToolTurn(context.Background(), fixture.dispatchPath, fixture.brokerPath, fixture.expected.Dispatch)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sealed, err := SealSynchronousToolTurn(ctx, fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected, fixture.spec, fixture.client, fixture.process, fixture.running, fixture.broker)
	if err != nil {
		t.Fatal(err)
	}
	observation, receipt, err := RecoverSynchronousToolTurnSealEvidence(fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected)
	if err != nil || !reflect.DeepEqual(observation, before) || !reflect.DeepEqual(receipt, sealed) {
		t.Fatal("recovered seal evidence differs from exact stored evidence", observation, receipt, err)
	}
	receiptOnly, err := RecoverSynchronousToolTurnSeal(fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected)
	if err != nil || !reflect.DeepEqual(receiptOnly, receipt) {
		t.Fatal("receipt-only seal recovery did not delegate exactly", receiptOnly, err)
	}
}

func TestRecoverSynchronousToolTurnSealEvidenceRejectsUnsealedAndMismatchedHistory(t *testing.T) {
	fixture := newSealFixture(t)
	if _, _, err := RecoverSynchronousToolTurnSealEvidence(fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected); err == nil || !strings.Contains(err.Error(), "intent mismatch") {
		t.Fatal("empty seal history produced evidence", err)
	}
	intent := SynchronousToolTurnSealIntent{Version: 1, Expected: fixture.expected}
	intent.ObservationSHA256 = strings.Repeat("a", 64)
	intent.TranscriptSHA256 = strings.Repeat("b", 64)
	intent.OpenBrokerStateID = strings.Repeat("c", 64)
	intent.ToolsConfigurationSHA256 = fixture.expected.Tools.SHA256
	intent.MCPStatusSHA256 = strings.Repeat("d", 64)
	intent.ProcessID = 1
	intent.OpenCodeEndpoint = "http://127.0.0.1:1"
	intent.MCPEndpoint = fixture.expected.Tools.Endpoint
	intent.ID, _ = synchronousToolTurnSealIntentID(intent)
	if err := appendSynchronousToolTurnSeal(fixture.sealPath, "opencode.tool-turn-seal-intent", intent); err != nil {
		t.Fatal(err)
	}
	if _, _, err := RecoverSynchronousToolTurnSealEvidence(fixture.sealPath, fixture.dispatchPath, fixture.brokerPath, fixture.expected); err == nil || !strings.Contains(err.Error(), "remains unsealed") {
		t.Fatal("intent-only seal produced evidence", err)
	}
	events, err := journal.Read(fixture.sealPath)
	if err != nil || len(events) != 1 {
		t.Fatal("evidence recovery changed seal journal", events, err)
	}
}

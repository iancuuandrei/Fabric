package codexhost

import (
	"encoding/json"
	"strings"
	"testing"

	"harness.local/engorch/internal/codexrpc"
)

func receiptFixture(t *testing.T) (Launch, Receipt) {
	t.Helper()
	launch, err := Expected(`C:\host`, `C:\codex.exe`, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	id, err := launch.BindingID()
	if err != nil {
		t.Fatal(err)
	}
	receipt := Receipt{LaunchID: id, Server: codexrpc.Server{CodexHome: `C:\host\home`, UserAgent: "engorch/0.153.4", PlatformFamily: "windows", PlatformOS: "windows"}, DisabledFeatures: append([]string{}, disabled...), PID: 1}
	return launch, receipt
}

func TestLegacyReceiptEncodingAndValidationOmitAttestation(t *testing.T) {
	launch, receipt := receiptFixture(t)
	if err := receipt.Validate(launch); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "attestation") {
		t.Fatal("legacy receipt encoding acquired attestation fields")
	}
}

func TestAttestedReceiptRequiresBoundPair(t *testing.T) {
	launch, receipt := receiptFixture(t)
	manifestSHA := strings.Repeat("b", 64)
	id, err := AttestationBindingID(manifestSHA, receipt.LaunchID)
	if err != nil {
		t.Fatal(err)
	}
	receipt.CapabilityAttestationID = id
	receipt.CapabilityAttestationSHA256 = manifestSHA
	decision := CapabilityConfinementDecision{Version: 1, Requested: CapabilityConfinementRequired, Observed: CapabilityConfinementR17, Admitted: true, AttestationID: id}
	if err := receipt.ValidateAttested(launch, decision); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Receipt){
		func(value *Receipt) { value.CapabilityAttestationID = "" },
		func(value *Receipt) { value.CapabilityAttestationSHA256 = "" },
		func(value *Receipt) { value.CapabilityAttestationSHA256 = strings.Repeat("c", 64) },
	} {
		changed := receipt
		mutate(&changed)
		if err := changed.Validate(launch); err == nil {
			t.Fatal("tampered attestation receipt accepted")
		}
	}
}

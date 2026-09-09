package codexhost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestCapabilityConfinementRequiredFailsClosedWithoutEffectiveSurfaceEvidence(t *testing.T) {
	decision, err := DecideCapabilityConfinement(CapabilityConfinementRequired)
	if !errors.Is(err, ErrCapabilityConfinementUnverified) {
		t.Fatal("strict confinement was admitted without authoritative effective-surface evidence", err)
	}
	want := CapabilityConfinementDecision{Version: 1, Requested: CapabilityConfinementRequired, Observed: CapabilityConfinementUnverified, Admitted: false, Reason: "CAPABILITY_CONFINEMENT_UNVERIFIED"}
	if !reflect.DeepEqual(decision, want) {
		t.Fatal("unexpected strict confinement decision", decision)
	}
	var typed *CapabilityConfinementError
	if !errors.As(err, &typed) || !reflect.DeepEqual(typed.Decision, want) {
		t.Fatal("typed confinement evidence unavailable", err)
	}
}

func TestInstalledHostStrictCapabilityConfinementProbe(t *testing.T) {
	binary := os.Getenv("ENGORCH_CODEX_CONFINEMENT_PROBE_BINARY")
	if binary == "" {
		t.Skip("opt-in installed Codex confinement probe")
	}
	launch, err := Prepare(t.TempDir(), binary)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	host, decision, err := StartWithCapabilityConfinement(ctx, launch, CapabilityConfinementRequired, nil)
	if host != nil || !errors.Is(err, ErrCapabilityConfinementUnverified) {
		if host != nil {
			_ = host.Close()
		}
		t.Fatal("installed host passed strict confinement without an authoritative producer", err)
	}
	var typed *CapabilityConfinementError
	if !errors.As(err, &typed) || typed.Launch != launch || typed.Receipt.LaunchID == "" {
		t.Fatal("installed host denial lost launch or observed host receipt", err)
	}
	evidence, marshalErr := json.Marshal(struct {
		Decision CapabilityConfinementDecision `json:"decision"`
		Launch   Launch                        `json:"launch"`
		Receipt  Receipt                       `json:"host_receipt"`
	}{decision, typed.Launch, typed.Receipt})
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	t.Logf("installed host strict confinement decision=%s", evidence)
}

func TestCapabilityConfinementLegacyModePreservesAdmission(t *testing.T) {
	decision, err := DecideCapabilityConfinement("")
	if err != nil || decision != (CapabilityConfinementDecision{}) {
		t.Fatal("legacy admission semantics changed", decision, err)
	}
}

func TestAttestedCapabilityConfinementRequiresExplicitStrictMode(t *testing.T) {
	host, decision, err := StartWithCapabilityConfinementAttested(context.Background(), Launch{}, "", CapabilityConfinementAttestation{}, nil)
	if host != nil || !errors.Is(err, ErrCapabilityConfinementUnverified) {
		t.Fatal("attested entrypoint silently admitted legacy mode", host, decision, err)
	}
	if decision.Requested != "" || decision.Observed != CapabilityConfinementUnverified || decision.Admitted {
		t.Fatal("unexpected attested-entrypoint denial", decision)
	}
}

package hostenvironment

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type verifierFunc func(context.Context) (VerifiedSandbox, error)

func (f verifierFunc) VerifyInherited(ctx context.Context) (VerifiedSandbox, error) {
	return f(ctx)
}

func TestPassiveCodexSignalsAreHintsNotSandboxEvidence(t *testing.T) {
	observation, err := Observe(context.Background(), Input{
		GOOS:        "windows",
		ProcessName: `C:\tools\codex.exe`,
		Environ: []string{
			"CODEX_THREAD_ID=secret-thread",
			"CODEX_HOME=C:\\private",
			"UNRELATED=value",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Host != HostCodex || observation.HostEvidence.Level != EvidenceHint {
		t.Fatal("Codex host hint was not represented", observation)
	}
	wantSignals := []string{"env:CODEX_HOME", "env:CODEX_THREAD_ID", "process-name"}
	if !reflect.DeepEqual(observation.HostEvidence.Signals, wantSignals) {
		t.Fatalf("signals = %#v, want %#v", observation.HostEvidence.Signals, wantSignals)
	}
	if observation.Sandbox.Status != SandboxNotVerified || observation.Sandbox.Inherited {
		t.Fatal("passive signals were promoted to sandbox evidence", observation.Sandbox)
	}
	b, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret-thread") || strings.Contains(string(b), "private") {
		t.Fatal("host observation disclosed environment values", string(b))
	}
}

func TestNativeObservationMakesNoSandboxClaim(t *testing.T) {
	observation, err := Observe(context.Background(), Input{GOOS: "linux", Environ: []string{"PATH=/bin"}, ProcessName: "harness"})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Host != HostNative || observation.HostEvidence.Level != EvidenceDefault || len(observation.HostEvidence.Signals) != 0 {
		t.Fatal("unexpected native host observation", observation)
	}
	if observation.Sandbox.Status != SandboxNotVerified {
		t.Fatal("native execution claimed a sandbox", observation.Sandbox)
	}
}

func TestTrustedVerifierCanEstablishInheritedBoundary(t *testing.T) {
	observation, err := Observe(context.Background(), Input{
		GOOS: "windows",
		Verifier: verifierFunc(func(context.Context) (VerifiedSandbox, error) {
			return VerifiedSandbox{Mode: SandboxWorkspaceWrite, Source: "codex-host-api", Inherited: true}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Sandbox.Status != SandboxVerified || observation.Sandbox.Mode != SandboxWorkspaceWrite || !observation.Sandbox.Inherited {
		t.Fatal("trusted verification was not retained", observation.Sandbox)
	}
	policy := Policy{Version: 1, AllowedHosts: []HostKind{HostNative}, RequireVerifiedSandbox: true, AllowedSandboxModes: []SandboxMode{SandboxWorkspaceWrite}}
	if err := Admit(policy, observation); err != nil {
		t.Fatal("verified observation rejected", err)
	}

	// A serialized observation is report data, not reusable verifier authority.
	b, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Observation
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if err := Admit(policy, decoded); err == nil {
		t.Fatal("serialized observation was accepted as sandbox authority", err)
	}
	mutated := observation
	mutated.Sandbox.Mode = SandboxReadOnly
	if err := Admit(Policy{Version: 1, AllowedHosts: []HostKind{HostNative}, RequireVerifiedSandbox: true, AllowedSandboxModes: []SandboxMode{SandboxReadOnly}}, mutated); err == nil {
		t.Fatal("mutated report fields retained verifier authority")
	}
}

func TestVerifierFailureAndInvalidEvidenceFailClosed(t *testing.T) {
	tests := []struct {
		name     string
		verifier BoundaryVerifier
	}{
		{"error", verifierFunc(func(context.Context) (VerifiedSandbox, error) { return VerifiedSandbox{}, errors.New("unavailable") })},
		{"not inherited", verifierFunc(func(context.Context) (VerifiedSandbox, error) {
			return VerifiedSandbox{Mode: SandboxReadOnly, Source: "os-probe"}, nil
		})},
		{"unknown mode", verifierFunc(func(context.Context) (VerifiedSandbox, error) {
			return VerifiedSandbox{Mode: "full-access", Source: "os-probe", Inherited: true}, nil
		})},
		{"unbounded source", verifierFunc(func(context.Context) (VerifiedSandbox, error) {
			return VerifiedSandbox{Mode: SandboxReadOnly, Source: "User supplied text", Inherited: true}, nil
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Observe(context.Background(), Input{GOOS: "windows", Verifier: test.verifier}); err == nil {
				t.Fatal("invalid verifier result was accepted")
			}
		})
	}
}

func TestDeclaredPolicyIsIndependentAndFailClosed(t *testing.T) {
	observation, err := Observe(context.Background(), Input{GOOS: "windows", Environ: []string{"CODEX_CI=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Admit(Policy{Version: 1, AllowedHosts: []HostKind{HostNative}, AllowedSandboxModes: []SandboxMode{}}, observation); err == nil {
		t.Fatal("declared host policy did not reject Codex hint")
	}
	if err := Admit(Policy{Version: 1, AllowedHosts: []HostKind{HostCodex}, RequireVerifiedSandbox: true, AllowedSandboxModes: []SandboxMode{}}, observation); err == nil {
		t.Fatal("host hint satisfied a verified-sandbox policy")
	}
	if err := Admit(Policy{Version: 1, AllowedHosts: []HostKind{HostCodex}, AllowedSandboxModes: []SandboxMode{}}, observation); err != nil {
		t.Fatal("informational Codex host policy unexpectedly rejected observation", err)
	}
}

func TestDefaultObservationCannotClaimVerifiedSandbox(t *testing.T) {
	observation, err := ObserveDefault(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if observation.Sandbox.Status != SandboxNotVerified {
		t.Fatal("default observation claimed verified sandbox evidence", observation.Sandbox)
	}
}

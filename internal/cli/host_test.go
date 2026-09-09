package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/hostenvironment"
	"harness.local/engorch/internal/repository"
)

func TestCreationUsesConfiguredHostPolicy(t *testing.T) {
	policy := control.DefaultHostPolicy()
	policy.RequireVerifiedSandbox = true
	creation := control.Creation{Config: config.Config{HostPolicy: &policy}}
	if _, err := bindCurrentHost(context.Background(), creation); err == nil {
		t.Fatal("configured sandbox requirement replaced by default policy")
	}
	policy.RequireVerifiedSandbox = false
	bound, err := bindCurrentHost(context.Background(), creation)
	if err != nil || bound.HostAdmission == nil || bound.HostAdmission.Policy.RequireVerifiedSandbox {
		t.Fatal("declared host policy not bound", err)
	}
}

func TestConfiguredHostPolicyCreationRoundTrips(t *testing.T) {
	cfg, err := config.Parse([]byte(config.Example + "\n[host_policy]\nversion = 1\nallowed_hosts = [\"native\", \"codex\"]\nrequire_verified_sandbox = false\nallowed_sandbox_modes = []\n"))
	if err != nil {
		t.Fatal(err)
	}
	bound, err := bindCurrentHost(context.Background(), control.Creation{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	body, err := canonical.Bytes(bound)
	if err != nil {
		t.Fatal(err)
	}
	var replayed control.Creation
	if err := canonical.Decode(body, &replayed); err != nil {
		t.Fatal("accepted policy cannot be replayed", err)
	}
}

func TestDoctorReportsHostObservationWithoutDispatch(t *testing.T) {
	identity := repository.Identity{Version: 1, Name: "fixture"}
	observe := func(context.Context) (hostenvironment.Observation, error) {
		return hostenvironment.Observation{
			Version:  1,
			Host:     hostenvironment.HostCodex,
			Platform: "windows",
			HostEvidence: hostenvironment.HostEvidence{
				Level:   hostenvironment.EvidenceHint,
				Signals: []string{"env:CODEX_THREAD_ID"},
			},
			Sandbox: hostenvironment.SandboxObservation{Status: hostenvironment.SandboxNotVerified},
		}, nil
	}
	var out bytes.Buffer
	if err := writeDoctorWithObserver(context.Background(), &out, identity, observe, nil); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Status          string                      `json:"status"`
		HostEnvironment hostenvironment.Observation `json:"host_environment"`
		RuntimeDispatch string                      `json:"runtime_dispatch"`
		Verification    string                      `json:"verification"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "PASS" || report.RuntimeDispatch != "NOT_RUN" || report.Verification != "NOT_RUN" {
		t.Fatal("doctor changed its validation/dispatch claims", report)
	}
	if report.HostEnvironment.Host != hostenvironment.HostCodex || report.HostEnvironment.Sandbox.Status != hostenvironment.SandboxNotVerified {
		t.Fatal("doctor omitted or upgraded host evidence", report.HostEnvironment)
	}
	policy := control.DefaultHostPolicy()
	policy.RequireVerifiedSandbox = true
	out.Reset()
	if err := writeDoctorWithObserver(context.Background(), &out, identity, observe, &policy); err == nil || out.Len() != 0 {
		t.Fatal("doctor reported success despite unsatisfied host policy")
	}
}

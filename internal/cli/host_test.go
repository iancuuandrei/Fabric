package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/hostenvironment"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
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

func validOpenCodeConfig(t *testing.T) *config.Config {
	t.Helper()
	state := t.TempDir()
	exeBytes := []byte("#!/bin/sh\nexit 0\n")
	exe := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(exe, exeBytes, 0755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(exeBytes)
	return &config.Config{
		Planner: runtime.Profile{Runtime: "opencode-http", Provider: "synthetic", Model: "synthetic-model"},
		OpenCode: &config.OpenCodeHost{
			Executable:     exe,
			ExecutableHash: hex.EncodeToString(sum[:]),
			StateRoot:      state,
		},
	}
}

func TestOpenCodeRouteReadinessReady(t *testing.T) {
	cfg := validOpenCodeConfig(t)
	got := openCodeRouteReadiness(cfg)
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Status string              `json:"status"`
		Reason string              `json:"reason"`
		Roles  []map[string]string `json:"roles"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "READY" {
		t.Fatalf("status = %q", decoded.Status)
	}
	if decoded.Reason != "opencode local prerequisites ready" {
		t.Fatalf("reason = %q", decoded.Reason)
	}
	if len(decoded.Roles) != 1 {
		t.Fatalf("roles = %v", decoded.Roles)
	}
	role := decoded.Roles[0]
	if len(role) != 4 || role["role"] != "planner" || role["provider"] != "synthetic" || role["model"] != "synthetic-model" || role["runtime"] != "opencode-http" {
		t.Fatalf("role = %v", role)
	}
}

func TestOpenCodeRouteReadinessConfiguredHostTruthTable(t *testing.T) {
	valid := validOpenCodeConfig(t)
	hostOnly := validOpenCodeConfig(t)
	hostOnly.Planner = runtime.Profile{}
	rolesOnly := &config.Config{
		Planner: runtime.Profile{Runtime: "opencode-http", Provider: "synthetic", Model: "synthetic-model"},
	}
	rows := []struct {
		name   string
		cfg    *config.Config
		status string
	}{
		{"neither", &config.Config{}, "NOT_CHECKED"},
		{"host-only", hostOnly, "NOT_READY"},
		{"roles-only", rolesOnly, "NOT_READY"},
		{"both", valid, "READY"},
	}
	for _, row := range rows {
		got := openCodeRouteReadiness(row.cfg)
		status, _ := got["status"].(string)
		if status != row.status {
			t.Fatalf("%s: status = %q", row.name, status)
		}
		if row.name == "neither" && status != "NOT_CHECKED" {
			t.Fatalf("neither must be NOT_CHECKED")
		}
		if row.name != "neither" && status == "NOT_CHECKED" {
			t.Fatalf("%s must not be NOT_CHECKED", row.name)
		}
	}
}

package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	stdruntime "runtime"
	"testing"
	"time"

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

func TestOpenCodeLinkedParentFixture(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	child := filepath.Join(target, "child")
	alias := filepath.Join(root, "alias")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "sentinel"), []byte("linked"), 0644); err != nil {
		t.Fatal(err)
	}
	if stdruntime.GOOS == "windows" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "New-Item -ItemType Junction -Path $env:EO_ALIAS -Target $env:EO_TARGET | Out-Null")
		cmd.Env = append(os.Environ(), "EO_ALIAS="+alias, "EO_TARGET="+target)
		if err := cmd.Run(); err != nil {
			t.Fatal(err)
		}
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
	}
	defer os.Remove(alias)
	fi, err := os.Lstat(alias)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
		t.Fatalf("alias is ordinary directory: %v", fi.Mode())
	}
	aliasChild := filepath.Join(alias, "child")
	st, err := os.Stat(aliasChild)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsDir() {
		t.Fatalf("alias child is not a directory")
	}
	li, err := os.Lstat(aliasChild)
	if err != nil {
		t.Fatal(err)
	}
	if li.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		t.Fatalf("alias child is a link: %v", li.Mode())
	}
	entries, err := os.ReadDir(aliasChild)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %v", entries)
	}
	data, err := os.ReadFile(filepath.Join(aliasChild, "sentinel"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "linked" {
		t.Fatalf("sentinel = %q", data)
	}
	exeBytes := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(filepath.Join(child, "opencode"), exeBytes, 0755); err != nil {
		t.Fatal(err)
	}
	writtenHash := sha256.Sum256(exeBytes)
	aliasExe := filepath.Join(aliasChild, "opencode")
	fiExe, err := os.Lstat(aliasExe)
	if err != nil {
		t.Fatal(err)
	}
	if !fiExe.Mode().IsRegular() || fiExe.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		t.Fatalf("alias exe not ordinary regular: %v", fiExe.Mode())
	}
	readback, err := os.ReadFile(aliasExe)
	if err != nil {
		t.Fatal(err)
	}
	readHash := sha256.Sum256(readback)
	if readHash != writtenHash {
		t.Fatalf("executable readback hash mismatch")
	}
	stateRoot := t.TempDir()
	cfg := &config.Config{Planner: runtime.Profile{Runtime: "opencode-http", Provider: "synthetic", Model: "synthetic-model"}, OpenCode: &config.OpenCodeHost{Executable: aliasExe, ExecutableHash: hex.EncodeToString(writtenHash[:]), StateRoot: stateRoot}}
	got := openCodeRouteReadiness(cfg)
	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) == 0 || len(body) >= 8192 {
		t.Fatalf("body len = %d", len(body))
	}
	var decoded struct {
		Status string              `json:"status"`
		Reason string              `json:"reason"`
		Roles  []map[string]string `json:"roles"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status != "NOT_READY" {
		t.Fatalf("status = %q", decoded.Status)
	}
	if decoded.Reason != "opencode executable unavailable" {
		t.Fatalf("reason = %q", decoded.Reason)
	}
	if len(decoded.Roles) != 1 {
		t.Fatalf("roles = %v", decoded.Roles)
	}
	role := decoded.Roles[0]
	if len(role) != 4 || role["role"] != "planner" || role["provider"] != "synthetic" || role["model"] != "synthetic-model" || role["runtime"] != "opencode-http" {
		t.Fatalf("role = %v", role)
	}
	lowerBody := bytes.ToLower(body)
	needles := []string{root, target, alias, aliasChild, aliasExe, stateRoot, "link", "syscall", "privilege", "1314", "junction", "powershell"}
	for _, n := range needles {
		if len(n) == 0 {
			t.Fatalf("empty privacy needle")
		}
		if bytes.Contains(lowerBody, bytes.ToLower([]byte(n))) {
			t.Fatalf("public JSON leaks %q", n)
		}
	}
	if _, err := os.Lstat(aliasChild); err != nil {
		t.Fatal(err)
	}
	plainDir := t.TempDir()
	plainExe := filepath.Join(plainDir, "opencode")
	plainBytes := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(plainExe, plainBytes, 0755); err != nil {
		t.Fatal(err)
	}
	fiPlain, err := os.Lstat(plainExe)
	if err != nil {
		t.Fatal(err)
	}
	if !fiPlain.Mode().IsRegular() || fiPlain.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		t.Fatalf("plain exe not ordinary regular: %v", fiPlain.Mode())
	}
	readPlain, err := os.ReadFile(plainExe)
	if err != nil {
		t.Fatal(err)
	}
	writtenPlain := sha256.Sum256(plainBytes)
	readPlainHash := sha256.Sum256(readPlain)
	if readPlainHash != writtenPlain {
		t.Fatalf("plain executable readback hash mismatch")
	}
	cfgState := &config.Config{Planner: runtime.Profile{Runtime: "opencode-http", Provider: "synthetic", Model: "synthetic-model"}, OpenCode: &config.OpenCodeHost{Executable: plainExe, ExecutableHash: hex.EncodeToString(writtenPlain[:]), StateRoot: aliasChild}}
	gotState := openCodeRouteReadiness(cfgState)
	bodyState, err := json.Marshal(gotState)
	if err != nil {
		t.Fatal(err)
	}
	if len(bodyState) == 0 || len(bodyState) >= 8192 {
		t.Fatalf("body len = %d", len(bodyState))
	}
	var decodedState struct {
		Status string              `json:"status"`
		Reason string              `json:"reason"`
		Roles  []map[string]string `json:"roles"`
	}
	if err := json.Unmarshal(bodyState, &decodedState); err != nil {
		t.Fatal(err)
	}
	if decodedState.Status != "NOT_READY" {
		t.Fatalf("status = %q", decodedState.Status)
	}
	if decodedState.Reason != "opencode state unavailable" {
		t.Fatalf("reason = %q", decodedState.Reason)
	}
	if len(decodedState.Roles) != 1 {
		t.Fatalf("roles = %v", decodedState.Roles)
	}
	roleState := decodedState.Roles[0]
	if len(roleState) != 4 || roleState["role"] != "planner" || roleState["provider"] != "synthetic" || roleState["model"] != "synthetic-model" || roleState["runtime"] != "opencode-http" {
		t.Fatalf("role = %v", roleState)
	}
	lowerState := bytes.ToLower(bodyState)
	needlesState := []string{root, target, alias, aliasChild, plainExe, plainDir, "link", "syscall", "privilege", "1314", "junction", "powershell"}
	for _, n := range needlesState {
		if len(n) == 0 {
			t.Fatalf("empty privacy needle")
		}
		if bytes.Contains(lowerState, bytes.ToLower([]byte(n))) {
			t.Fatalf("public JSON leaks %q", n)
		}
	}
}

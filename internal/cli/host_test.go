package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/hostenvironment"
	"github.com/iancuuandrei/fabric/internal/memoryadmission"
	"github.com/iancuuandrei/fabric/internal/repository"
)

func TestDoctorMemoryObservationDoesNotGrantAdmission(t *testing.T) {
	for _, observation := range []memoryadmission.Observation{
		{Status: memoryadmission.ObservationUnavailable, Reason: "unavailable"},
		{Status: memoryadmission.ObservationObserved, Source: memoryadmission.SourceWindowsGlobal, AvailableMiB: 4096, TotalMiB: 8192},
	} {
		resources := doctorMemoryResources(observation)
		if resources["live_memory_pressure"] != observation.Status || resources["memory_admission"] != "NOT_ADMITTED" || resources["worker_memory_usage"] != "NOT_MEASURED" {
			t.Fatal("doctor upgraded memory observation to admission or worker measurement", resources)
		}
	}
}

func TestInspectPlanPreservesStateAndRejectsParserSubstitution(t *testing.T) {
	root := autonomousCLIFixture(t)
	raw := mustExecuteCLI(t, root, "run", "--autonomous", "--inspect-plan")
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil || report["runtime_dispatch"] != "NOT_RUN" {
		t.Fatalf("invalid read-only report: %s %v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".harness")); !os.IsNotExist(err) {
		t.Fatalf("inspection created run state: %v", err)
	}
	parser := filepath.Join(t.TempDir(), "parser.exe")
	if err := os.WriteFile(parser, []byte("not-the-pinned-parser"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := Execute(context.Background(), []string{"run", "--autonomous", "--inspect-plan", "--planner-context", "go-contract-context-v3", "--planner-context-ri-executable", parser, "--planner-context-ri-executable-sha256", strings.Repeat("a", 64)}, root, &out)
	if err == nil || out.Len() != 0 {
		t.Fatal("inspection accepted substituted parser", err)
	}
}

func TestDoctorExecutionPlanShowsUnavailableChecksWithoutEffects(t *testing.T) {
	root := t.TempDir()
	identity := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: root, ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	cfg, err := config.Parse([]byte(config.Example))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Verification = []config.Check{{Name: "missing", Argv: []string{"fabric-doctor-missing-check-93752"}, TimeoutSeconds: 10}, {Name: "available", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
	plan, err := doctorExecutionPlan(identity, cfg)
	if err != nil {
		t.Fatal(err)
	}
	checks := plan["verification_checks"].([]map[string]string)
	if checks[0]["readiness"] != "UNAVAILABLE" || checks[1]["readiness"] != "AVAILABLE_NOT_RUN" || plan["provider_qualification"] != "NOT_RUN" {
		t.Fatalf("doctor upgraded readiness to executed evidence: %+v", plan)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("doctor created state: %v %v", entries, err)
	}
}

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

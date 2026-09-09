package control

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/codexhost"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
)

func TestStrictCodexRoleHostStopsBeforeAuthenticationAccessOrInference(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hash, err := fixtureFileHash(executable)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := codexhost.Prepare(t.TempDir(), executable)
	if err != nil {
		t.Fatal(err)
	}
	hostConfig := &config.Codex{CapabilityConfinement: codexhost.CapabilityConfinementRequired}
	host, err := startCodexRoleHost(context.Background(), launch, hostConfig, runtime.Profile{}, nil, nil)
	if host != nil || !errors.Is(err, codexhost.ErrCapabilityConfinementUnverified) {
		t.Fatal("strict role host did not fail closed", host, err)
	}
	if launch.BinaryHash != hash {
		t.Fatal("fixture executable identity changed")
	}
	if _, err := os.Stat(filepath.Join(launch.Root, "planner.jsonl")); !os.IsNotExist(err) {
		t.Fatal("strict host created a runtime journal before confinement admission", err)
	}
	if _, err := os.Stat(filepath.Join(launch.Root, "home", "auth.json")); !os.IsNotExist(err) {
		t.Fatal("strict host persisted authentication material", err)
	}
	methods, err := os.ReadFile(filepath.Join(launch.Root, "home", "fixture-methods.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"account/login/start", "thread/start", "thread/resume", "turn/start", "turn/read"} {
		if strings.Contains(string(methods), forbidden+"\n") {
			t.Fatal("strict host crossed the pre-access gate", forbidden, string(methods))
		}
	}
}

func TestConfiguredCodexAttestationCannotFallBackToLegacyAdmission(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launch, err := codexhost.Prepare(t.TempDir(), executable)
	if err != nil {
		t.Fatal(err)
	}
	profile := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "gpt-5.6-sol", Effort: "medium", Role: "planner"}
	hostConfig := &config.Codex{
		CapabilityConfinement:       codexhost.CapabilityConfinementRequired,
		CapabilityAttestationPath:   filepath.Join(t.TempDir(), "missing-attestation.json"),
		CapabilityAttestationSHA256: strings.Repeat("a", 64),
	}
	host, err := startCodexRoleHost(context.Background(), launch, hostConfig, profile, []any{}, nil)
	if err == nil || host != nil {
		t.Fatal("unavailable attestation admitted a host", host, err)
	}
	for _, name := range []string{"auth.json", "fixture-methods.log"} {
		if _, err := os.Stat(filepath.Join(launch.Root, "home", name)); !os.IsNotExist(err) {
			t.Fatal("invalid attestation reached process or authentication", name, err)
		}
	}
}

func TestCodexCapabilityConfinementConfigurationIsV2OnlyAndIdentityBound(t *testing.T) {
	spec := codexAccessCreation(t)
	legacyWire, err := canonical.Bytes(*spec.Config.Codex)
	if err != nil {
		t.Fatal(err)
	}
	oldShapeWire, err := canonical.Bytes(struct {
		Executable       string `json:"executable"`
		ExecutableHash   string `json:"executable_hash"`
		StateRoot        string `json:"state_root"`
		AuthSource       string `json:"auth_source"`
		RequireLiveUsage bool   `json:"require_live_usage,omitempty"`
		UsageQualified   bool   `json:"usage_qualified,omitempty"`
	}{spec.Config.Codex.Executable, spec.Config.Codex.ExecutableHash, spec.Config.Codex.StateRoot, spec.Config.Codex.AuthSource, spec.Config.Codex.RequireLiveUsage, spec.Config.Codex.UsageQualified})
	if err != nil || !bytes.Equal(legacyWire, oldShapeWire) {
		t.Fatal("empty capability confinement changed historical Codex JSON", string(legacyWire), string(oldShapeWire), err)
	}
	legacyID, err := spec.Config.ID()
	if err != nil {
		t.Fatal(err)
	}
	spec.Config.Codex.CapabilityConfinement = "required"
	strictID, err := spec.Config.ID()
	if err != nil || strictID == legacyID {
		t.Fatal("strict capability confinement was not configuration-identity-bound", err)
	}
	for _, invalid := range []string{"true", "optional", "unverified", "attested"} {
		candidate := spec.Config
		copy := *spec.Config.Codex
		copy.CapabilityConfinement = invalid
		candidate.Codex = &copy
		if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "capability confinement") {
			t.Fatal("unsupported capability confinement was admitted", invalid, err)
		}
	}
	v1 := creation(t).Config
	v1.Planner = spec.Config.Planner
	copy := *spec.Config.Codex
	v1.Codex = &copy
	if err := v1.Validate(); err == nil || !strings.Contains(err.Error(), "capability confinement") {
		t.Fatal("configuration v1 admitted strict capability confinement", err)
	}
}

func TestResumePlanningStrictConfinementCreatesNoAccessOrRuntimeIntent(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executableHash, err := fixtureFileHash(executable)
	if err != nil {
		t.Fatal(err)
	}
	spec := codexAccessCreation(t)
	stateRoot := t.TempDir()
	spec.Config.Codex.Executable = executable
	spec.Config.Codex.ExecutableHash = executableHash
	spec.Config.Codex.StateRoot = stateRoot
	spec.Config.Codex.AuthSource = filepath.Join(t.TempDir(), "unused-auth.json")
	spec.Config.Codex.CapabilityConfinement = codexhost.CapabilityConfinementRequired
	if err := spec.Config.Validate(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", spec); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResumePlanning(context.Background(), path); !errors.Is(err, codexhost.ErrCapabilityConfinementUnverified) {
		t.Fatal("strict planning did not fail at capability confinement", err)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "model.access-intent" || event.Kind == "planning.host-observed" || event.Kind == "planning.runtime-observed" {
			t.Fatal("strict planning crossed capability confinement", event.Kind)
		}
	}
	if _, err := os.Stat(modelAccessJournal(path)); !os.IsNotExist(err) {
		t.Fatal("strict planning created model access journal", err)
	}
	hostRoot := filepath.Join(stateRoot, snapshot.RunID)
	if _, err := os.Stat(filepath.Join(hostRoot, "planner.jsonl")); !os.IsNotExist(err) {
		t.Fatal("strict planning created runtime intent", err)
	}
	methods, err := os.ReadFile(filepath.Join(hostRoot, "home", "fixture-methods.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"account/login/start", "thread/start", "thread/resume", "turn/start", "turn/read"} {
		if strings.Contains(string(methods), forbidden+"\n") {
			t.Fatal("strict planning crossed the pre-access gate", forbidden, string(methods))
		}
	}
}

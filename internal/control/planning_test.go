package control

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/runtime"
)

func codexPlanningJournal(t *testing.T) (string, Snapshot) {
	t.Helper()
	c := creation(t)
	c.Config.Planner = runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "explicit", Effort: "low", Role: "planner"}
	c.Config.Codex = &config.Codex{Executable: filepath.Join(t.TempDir(), "missing.exe"), ExecutableHash: strings.Repeat("a", 64), StateRoot: t.TempDir(), AuthSource: filepath.Join(t.TempDir(), "auth.json")}
	p := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(p, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(p, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, s
}

func TestCodexPlanRequiresLinkedRuntimeEvidence(t *testing.T) {
	p, s := codexPlanningJournal(t)
	i, err := runtime.NewInvocation(s.Creation.Config.Planner, s.Creation.Objective)
	if err != nil {
		t.Fatal(err)
	}
	model, provider, effort := i.Profile.Model, i.Profile.Provider, i.Profile.Effort
	r := runtime.Result{Version: 1, InvocationID: i.ID, Requested: i.Profile, ObservedModel: &model, ObservedProvider: &provider, ObservedEffort: &effort, Output: "unbacked plan"}
	if err := Append(p, "plan.recorded", r); err == nil {
		t.Fatal("unbacked real-runtime plan admitted")
	}
	if err := Append(p, "planning.runtime-observed", PlannerReceipt{i.ID, "thread", "turn", strings.Repeat("a", 64), strings.Repeat("b", 64)}); err == nil {
		t.Fatal("receipt without host admission accepted")
	}
	l, err := expectedPlannerHost(s)
	if err != nil {
		t.Fatal(err)
	}
	bad := l
	bad.BinaryHash = strings.Repeat("c", 64)
	if err := Append(p, "planning.host-intent", bad); err == nil {
		t.Fatal("binary substitution accepted")
	}
	if err := Append(p, "planning.host-ready", l); err == nil {
		t.Fatal("preparation without intent")
	}
	if err := Append(p, "planning.host-intent", l); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumePlanning(context.Background(), p); err == nil {
		t.Fatal("missing pinned executable admitted")
	}
	after, err := Inspect(p)
	if err != nil || after.PlannerHostReady || after.Plan != nil {
		t.Fatal("failed preparation changed plan authority", err)
	}
}

func TestPlannerHostCannotInheritSourceControlDirectory(t *testing.T) {
	_, s := codexPlanningJournal(t)
	s.Creation.Config.Codex.StateRoot = filepath.Join(s.Creation.Repository.Root, ".harness", "runtime")
	if _, err := expectedPlannerHost(s); err == nil {
		t.Fatal("runtime home under source repository accepted")
	}
}

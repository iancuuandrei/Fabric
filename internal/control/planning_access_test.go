package control

import (
	"context"
	"path/filepath"
	"testing"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
)

func accessCreation(t *testing.T) Creation {
	c := creation(t)
	c.Config.Version = 2
	c.Config.Access = &config.Access{Class: access.Private, Limits: access.Limits{Tokens: 1000, Concurrency: 1}, Roles: map[string]string{"planner": "fixture"}, Profiles: []access.Profile{{Version: 1, Name: "fixture", Kind: "subscription", Runtime: "fake", Provider: "deterministic", AuthMode: "fixture", RepositoryClasses: []access.Class{access.Private}}}}
	c.Config.Access.Invocations = map[string]config.InvocationLimit{"planner": {Tokens: 200}}
	return c
}

func TestV2PlannerDurableAdmission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	c := accessCreation(t)
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	s, err := ResumePlanning(context.Background(), path)
	if err != nil || s.State != "AWAITING_APPROVAL" || s.PlannerAccess == nil {
		t.Fatal("gated planning failed", err)
	}
	if s.PlannerAccess.Reservation.Tokens != 200 {
		t.Fatal("planner reserved total run budget")
	}
	usage, err := MeasureRunUsage(path)
	if err != nil || len(usage.Admissions) != 1 {
		t.Fatal("admission accounting missing", err)
	}
	entry := usage.Admissions[0]
	if entry.Receipt == nil || entry.Receipt.CostMicroUSD != nil || entry.Receipt.InputTokens != nil || entry.Class != access.Private || entry.Intent.Reservation.Tokens != 200 || entry.EvidenceScope != "deterministic_fake_planner" {
		t.Fatal("reservation confused with measured usage")
	}
	events, err := journal.Read(path)
	if err != nil || len(events) != 4 || events[2].Kind != "planning.access-intent" || events[3].Kind != "plan.recorded" {
		t.Fatal("wrong admission ordering", err)
	}
	if _, err := ResumePlanning(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	after, _ := journal.Read(path)
	if len(after) != len(events) {
		t.Fatal("terminal plan repeated")
	}
}

func TestV2PlannerMissingIntentAndUnknownRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.jsonl")
	c := accessCreation(t)
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	i, _ := runtime.NewInvocation(c.Config.Planner, c.Objective)
	f := &runtime.Fake{}
	defer f.Close()
	r, err := f.Execute(context.Background(), i)
	if err != nil {
		t.Fatal(err)
	}
	if Append(path, "plan.recorded", r) == nil {
		t.Fatal("ungated output admitted")
	}
	s, _ := Inspect(path)
	intent, err := expectedPlanningAccess(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.access-intent", intent); err != nil {
		t.Fatal(err)
	}
	if _, err := ResumePlanning(context.Background(), path); err == nil {
		t.Fatal("unknown invocation retried")
	}
	usage, err := MeasureRunUsage(path)
	if err != nil || len(usage.Admissions) != 1 || usage.Admissions[0].Receipt != nil {
		t.Fatal("unresolved admission reported terminal", err)
	}
	s, err = Inspect(path)
	if err != nil || s.Plan != nil {
		t.Fatal("unknown state incorrectly completed", err)
	}
}

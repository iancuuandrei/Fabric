package control

import (
	"context"
	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/runtime"
	"path/filepath"
	"strings"
	"testing"
)

func TestProducerFailureIsJournaledWithoutRetry(t *testing.T) {
	c := riGitCreation(t)
	path := filepath.Join(t.TempDir(), "run.jsonl")
	if err := Append(path, "run.created", c); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "planning.started", struct{}{}); err != nil {
		t.Fatal(err)
	}
	invocation, err := runtime.NewInvocation(c.Config.Planner, c.Objective)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&runtime.Fake{}).Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.recorded", result); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "plan.approved", Approval{s.PlanID, "operator"}); err != nil {
		t.Fatal(err)
	}
	s, err = StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := ri.PrepareProducer(c.Repository, s.Workspace.Request.Path, config.Check{Name: "missing-indexer", Argv: []string{filepath.Join(c.Repository.Root, "missing.exe")}, TimeoutSeconds: 10}, filepath.Join(c.Repository.Root, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	intent, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	authorization := effects.Authorization{IntentID: id, Actor: "operator"}
	s, err = ExecuteRIProducer(context.Background(), path, plan, authorization)
	if err == nil || s.RIProducer == nil || s.RIProducer.Outcome != "NOT_APPLIED" {
		t.Fatal("not-started producer was not classified as not applied", err)
	}
	if s.RIProducer.Observation.Result == nil || s.RIProducer.Observation.Result.Process.Started {
		t.Fatal("lost not-run process evidence")
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-2].Kind != "ri.producer-intent" || events[len(events)-1].Kind != "ri.producer-observed" {
		t.Fatal("producer event ordering mismatch")
	}
	if _, err := ExecuteRIProducer(context.Background(), path, plan, authorization); err == nil {
		t.Fatal("unknown producer retried")
	}
	if err := Append(path, "ri.producer-observed", RIProducerObservation{IntentID: "foreign"}); err == nil {
		t.Fatal("foreign observation admitted")
	}
	plan.Invocation.Check.TimeoutSeconds++
	fresh, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	freshID, err := fresh.ID()
	if err != nil || freshID == id {
		t.Fatal("fresh plan reused effect identity", err)
	}
	s, err = ExecuteRIProducer(context.Background(), path, plan, effects.Authorization{IntentID: freshID, Actor: "operator"})
	if err == nil || s.RIProducer.Outcome != "NOT_APPLIED" {
		t.Fatal("fresh authorized attempt blocked by not-applied result", err)
	}
	plan.Invocation.Check.TimeoutSeconds++
	interrupted, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	interruptedID, err := interrupted.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "ri.producer-intent", RIProducerIntent{Plan: plan, Intent: interrupted, Authorization: effects.Authorization{IntentID: interruptedID, Actor: "operator"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := CloseRIProducer(context.Background(), path, interruptedID, "operator", "fixture never started a workload", false); err == nil {
		t.Fatal("closure accepted without quiescence attestation")
	}
	s, err = CloseRIProducer(context.Background(), path, interruptedID, "operator", "fixture never started a workload", true)
	if err != nil || s.RIProducer.Outcome != "UNKNOWN" || s.RIProducer.Closure == nil {
		t.Fatal("closure lost uncertainty", err)
	}
	if err := Append(path, "ri.producer-observed", RIProducerObservation{IntentID: interruptedID}); err == nil {
		t.Fatal("late observation changed closed producer")
	}
	plan.Invocation.Check.TimeoutSeconds++
	next, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	nextID, err := next.ID()
	if err != nil {
		t.Fatal(err)
	}
	s, err = ExecuteRIProducer(context.Background(), path, plan, effects.Authorization{IntentID: nextID, Actor: "operator"})
	if err == nil || s.RIProducer.Outcome != "NOT_APPLIED" {
		t.Fatal("closed uncertainty blocked new authorized attempt", err)
	}
	bad := *s.RIProducer.Observation
	foreign := *bad.Before
	foreign.WorktreeID = strings.Repeat("f", 64)
	bad.Before = &foreign
	bad.After = &foreign
	payload, err := canonical.Bytes(bad)
	if err != nil {
		t.Fatal(err)
	}
	probe := s
	producerState := *s.RIProducer
	probe.RIProducer = &producerState
	probe.RIProducer.Outcome = "UNKNOWN"
	if err := replayRIProducer(&probe, journal.Event{Kind: "ri.producer-observed", Payload: payload}, map[string]bool{}); err == nil {
		t.Fatal("foreign candidate accepted")
	}
	if probe.RIProducer.Outcome != "UNKNOWN" {
		t.Fatal("rejected source observation changed producer outcome")
	}
}

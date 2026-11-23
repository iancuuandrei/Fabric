package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/codexhost"
	"github.com/iancuuandrei/fabric/internal/codexrpc"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func plannerCapacityFixture(t *testing.T) (string, Snapshot, codexhost.Launch, runtime.Invocation, string) {
	t.Helper()
	p, s := codexPlanningJournal(t)
	l, err := expectedPlannerHost(s)
	if err != nil {
		t.Fatal(err)
	}
	i, err := plannerInvocationForSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind    string
		payload any
	}{{"planning.host-intent", l}, {"planning.host-ready", l}} {
		if err := Append(p, event.kind, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	_, hostReceipt := codexReceiptReplayFixture(t)
	hostReceipt.LaunchID, _ = l.BindingID()
	hostReceipt.Server.CodexHome = filepath.Join(l.Root, "home")
	if err := Append(p, "planning.host-observed", hostReceipt); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(l.Root, "workspace")
	thread := codexrpc.ThreadSettings{ThreadID: "thread-capacity", Model: i.Profile.Model, Provider: i.Profile.Provider, Effort: &i.Profile.Effort, Directory: directory, Approval: "never", Sandbox: "readOnly"}
	runtimePath := filepath.Join(l.Root, "planner.jsonl")
	writePlannerCapacityRuntime(t, runtimePath, s.Creation.Repository, i, thread, "serverOverloaded")
	s, err = Inspect(p)
	if err != nil {
		t.Fatal(err)
	}
	return p, s, l, i, runtimePath
}

func writePlannerCapacityRuntime(t *testing.T, runtimePath string, source repository.Identity, i runtime.Invocation, thread codexrpc.ThreadSettings, failureCode string) {
	t.Helper()
	stateIntent := codexruntime.Intent{Invocation: i, Directory: thread.Directory}
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0700); err != nil {
		t.Fatal(err)
	}
	errorJSON, _ := json.Marshal(map[string]string{"codexErrorInfo": failureCode})
	raw, _ := json.Marshal(map[string]any{"threadId": thread.ThreadID, "turn": map[string]any{"id": "turn-capacity", "status": "failed", "items": []any{}, "error": map[string]string{"codexErrorInfo": failureCode}}})
	telemetry := codexruntime.TerminalResponseTelemetry{Method: "turn/completed", RawParams: string(raw), ParamsBytes: len(raw), ThreadID: thread.ThreadID, TurnID: "turn-capacity", TurnStatus: "failed", FinishReason: "UNKNOWN", TurnError: errorJSON}
	for _, event := range []struct {
		kind    string
		payload any
	}{
		{"runtime.intent", stateIntent}, {"runtime.source", source}, {"runtime.thread", thread},
		{"runtime.turn-intent", map[string]string{"invocation_id": i.ID}}, {"runtime.turn", map[string]string{"id": "turn-capacity"}},
		{"runtime.terminal-response", telemetry}, {"runtime.stream-terminal", codexruntime.TurnStatus{ID: "turn-capacity", Status: "failed"}}, {"runtime.turn-status", codexruntime.TurnStatus{ID: "turn-capacity", Status: "failed"}},
	} {
		if _, err := journal.Append(runtimePath, event.kind, event.payload, func([]journal.Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlannerCapacityFailureIsSealedAndNeverBecomesPlanOrCompletedUsage(t *testing.T) {
	path, s, launch, invocation, runtimePath := plannerCapacityFixture(t)
	state, head, err := codexruntime.InspectWithHead(runtimePath)
	if err != nil || !plannerCapacityFailureEligible(s, launch, invocation, state) || head == "" {
		t.Fatalf("valid sealed terminal capacity evidence not recognized (status=%q, complete=%t, response=%t): %v", state.TurnStatus, state.NotificationStreamComplete, state.TerminalResponse != nil, err)
	}
	settled, ok, err := observePlannerCapacityFailure(path, s, launch, invocation, runtimePath)
	if err != nil || !ok || settled.PlannerReceipt == nil || settled.PlannerReceipt.FailureCode != "serverOverloaded" || settled.Plan != nil {
		t.Fatal("capacity refusal was not durably settled", err)
	}
	if got := ClassifyAutonomousFailure(settled, ErrPlannerCapacityRefused); got.Status() != "NEEDS_ATTENTION" || got.PublicReason() != "planner_capacity_refused" {
		t.Fatal("capacity refusal was not reported as an actionable terminal outcome", got)
	}
	if _, err := ResumePlanning(context.Background(), path); !errors.Is(err, ErrPlannerCapacityRefused) {
		t.Fatal("settled capacity refusal attempted another planner turn", err)
	}
	stateAfterResume, afterResumeHead, err := codexruntime.InspectWithHead(runtimePath)
	if err != nil || stateAfterResume.TurnID != "turn-capacity" || afterResumeHead != head {
		t.Fatal("settled capacity refusal changed or redispatched runtime", err)
	}
	if err := Append(path, "plan.recorded", runtime.Result{}); err == nil {
		t.Fatal("capacity failure receipt authorized a plan")
	}
	usage, err := MeasureRunUsage(path)
	if err != nil || len(usage.Invocations) != 1 || !usage.Invocations[0].ReceiptMatched || usage.Invocations[0].Usage == nil || usage.Invocations[0].Usage.Completed {
		t.Fatal("capacity failure was counted as completed usage", err)
	}
	if _, err := Inspect(path); err != nil {
		t.Fatal("controller replay rejected exact failure proof", err)
	}
	if _, ok, err := observePlannerCapacityFailure(path, settled, launch, invocation, runtimePath); err != nil || ok {
		t.Fatal("settlement was not idempotent", err)
	}
	if err := validatePlannerCapacityFailureRuntime(settled, path); err != nil {
		t.Fatal("exact sealed runtime did not revalidate", err)
	}
	forged := settled
	forgedReceipt := *settled.PlannerReceipt
	forgedReceipt.JournalHead = strings.Repeat("f", 64)
	forged.PlannerReceipt = &forgedReceipt
	if err := validatePlannerCapacityFailureRuntime(forged, path); err == nil {
		t.Fatal("mutated controller receipt head was accepted")
	}
	if err := os.Remove(runtimePath); err != nil {
		t.Fatal(err)
	}
	thread := codexrpc.ThreadSettings{ThreadID: "thread-capacity", Model: invocation.Profile.Model, Provider: invocation.Profile.Provider, Effort: &invocation.Profile.Effort, Directory: filepath.Join(launch.Root, "workspace"), Approval: "never", Sandbox: "readOnly"}
	writePlannerCapacityRuntime(t, runtimePath, settled.Creation.Repository, invocation, thread, "differentFailure")
	if err := validatePlannerCapacityFailureRuntime(settled, path); err == nil {
		t.Fatal("changed terminal runtime still matched failure receipt")
	}
}

func TestPlannerCapacityFailureRequiresExactSealedRuntime(t *testing.T) {
	_, s, launch, invocation, _ := plannerCapacityFixture(t)
	state := codexruntime.State{
		Intent: &codexruntime.Intent{Invocation: invocation, Directory: filepath.Join(launch.Root, "workspace")},
		Source: &s.Creation.Repository,
		Thread: &codexrpc.ThreadSettings{ThreadID: "thread-capacity", Model: invocation.Profile.Model, Provider: invocation.Profile.Provider, Effort: &invocation.Profile.Effort, Directory: filepath.Join(launch.Root, "workspace"), Approval: "never", Sandbox: "readOnly"},
		TurnID: "turn-capacity", TurnStatus: "failed", ExecutionOutcome: "failed", NotificationStreamComplete: true,
		TerminalResponse: &codexruntime.TerminalResponseTelemetry{Method: "turn/completed", ThreadID: "thread-capacity", TurnID: "turn-capacity", TurnStatus: "failed", TurnError: json.RawMessage(`{"codexErrorInfo":"serverOverloaded"}`)},
	}
	tests := []struct {
		name   string
		change func(*codexruntime.State)
	}{
		{"incomplete notification stream", func(s *codexruntime.State) { s.NotificationStreamComplete = false }},
		{"pending tool", func(s *codexruntime.State) { s.PendingTool = &codexruntime.ToolRequest{} }},
		{"completed result", func(s *codexruntime.State) { s.Result = &runtime.Result{} }},
		{"retained semantic result", func(s *codexruntime.State) { s.SemanticResult = &runtime.Result{} }},
		{"changed source", func(s *codexruntime.State) {
			source := *s.Source
			source.Commit = strings.Repeat("b", 40)
			s.Source = &source
		}},
		{"wrong invocation", func(s *codexruntime.State) { s.Intent.Invocation.ID = strings.Repeat("c", 64) }},
		{"wrong terminal error", func(s *codexruntime.State) {
			s.TerminalResponse.TurnError = json.RawMessage(`{"codexErrorInfo":"other"}`)
		}},
		{"unknown execution", func(s *codexruntime.State) { s.ExecutionOutcome = "UNKNOWN" }},
		{"incomplete turn status", func(s *codexruntime.State) { s.TurnStatus = "inProgress" }},
		{"wrong response method", func(s *codexruntime.State) { s.TerminalResponse.Method = "other/method" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := state
			test.change(&changed)
			if plannerCapacityFailureEligible(s, launch, invocation, changed) {
				t.Fatal("unsealed or substituted failure admitted")
			}
		})
	}
	wrongConfig := s
	wrongConfig.Creation.Config.Version = 2
	if plannerCapacityFailureEligible(wrongConfig, launch, invocation, state) {
		t.Fatal("version 2 planner accounting failure was reclassified as capacity")
	}
}

func TestPlannerCapacityReceiptBindsFailureFields(t *testing.T) {
	receipt := PlannerReceipt{FailureCode: "serverOverloaded", FailureResponseHash: strings.Repeat("a", 64), InvocationID: strings.Repeat("b", 64), ThreadID: "thread", TurnID: "turn", JournalHead: strings.Repeat("c", 64)}
	receipt.ResultHash, _ = plannerCapacityReceiptHash(receipt)
	mutated := receipt
	mutated.TurnID = "other-turn"
	if hash, _ := plannerCapacityReceiptHash(mutated); hash == receipt.ResultHash {
		t.Fatal("failure receipt hash omitted exact turn identity")
	}
	mutated = receipt
	mutated.FailureResponseHash = strings.Repeat("d", 64)
	if hash, _ := plannerCapacityReceiptHash(mutated); hash == receipt.ResultHash {
		t.Fatal("failure receipt hash omitted terminal response")
	}
}

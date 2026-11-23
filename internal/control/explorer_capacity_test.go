package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/codexrpc"
	"github.com/iancuuandrei/fabric/internal/codexruntime"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestExplorerCapacityFailureRequiresExactSealedEvidence(t *testing.T) {
	c := creation(t)
	c.Config.Explorer = &runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "explicit-explorer", Effort: "high", Role: "explorer"}
	c.Config.Codex = &config.Codex{Executable: filepath.Join(t.TempDir(), "missing.exe"), ExecutableHash: strings.Repeat("a", 64), StateRoot: t.TempDir(), AuthSource: filepath.Join(t.TempDir(), "auth.json")}
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := expectedExplorerHost(s, "inspect")
	if err != nil {
		t.Fatal(err)
	}
	host := ExplorerHostState{Intent: intent}
	directory := filepath.Join(intent.Launch.Root, "workspace")
	state := codexruntime.State{Intent: &codexruntime.Intent{Invocation: intent.Invocation, Directory: directory}, Source: &s.Creation.Repository, Candidate: &codexruntime.CandidateBinding{Workspace: *s.Workspace, Candidate: *s.Candidate}, Thread: &codexrpc.ThreadSettings{ThreadID: "thread", Model: intent.Invocation.Profile.Model, Provider: "openai", Effort: &intent.Invocation.Profile.Effort, Directory: directory, Approval: "never", Sandbox: "readOnly"}, TurnID: "turn", TurnStatus: "failed", NotificationStreamComplete: true, TerminalResponse: &codexruntime.TerminalResponseTelemetry{ThreadID: "thread", TurnID: "turn", TurnStatus: "failed", TurnError: json.RawMessage(`{"codexErrorInfo":"serverOverloaded"}`)}}
	if !explorerCapacityFailure(state, host, s) {
		t.Fatal("sealed overload not recognized")
	}
	for name, mutate := range map[string]func(*codexruntime.State){
		"unknown":           func(s *codexruntime.State) { s.TurnStatus = "" },
		"incomplete stream": func(s *codexruntime.State) { s.NotificationStreamComplete = false },
		"pending tool":      func(s *codexruntime.State) { s.PendingTool = &codexruntime.ToolRequest{} },
		"successful result": func(s *codexruntime.State) { s.Result = &runtime.Result{} },
		"semantic output":   func(s *codexruntime.State) { s.SemanticResult = &runtime.Result{} },
		"wrong turn":        func(s *codexruntime.State) { s.TurnID = "other" },
		"missing binding":   func(s *codexruntime.State) { s.Candidate = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := state
			mutate(&changed)
			if explorerCapacityFailure(changed, host, s) {
				t.Fatal("unsafe retry admitted")
			}
		})
	}
	// Exercise controller replay and usage using a real sealed SQLite journal.
	for _, event := range []struct {
		kind    string
		payload any
	}{
		{"explorer.host-intent", intent}, {"explorer.host-ready", intent},
	} {
		if err := Append(path, event.kind, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	_, hostReceipt := codexReceiptReplayFixture(t)
	hostReceipt.LaunchID, _ = intent.Launch.BindingID()
	hostReceipt.Server.CodexHome = filepath.Join(intent.Launch.Root, "home")
	if err := Append(path, "explorer.host-observed", hostReceipt); err != nil {
		t.Fatal(err)
	}
	raw := `{"threadId":"thread","turn":{"id":"turn","status":"failed","items":[],"error":{"codexErrorInfo":"serverOverloaded"}}}`
	telemetry := *state.TerminalResponse
	telemetry.Method, telemetry.RawParams, telemetry.ParamsBytes, telemetry.FinishReason = "turn/completed", raw, len(raw), "UNKNOWN"
	runtimePath := filepath.Join(intent.Launch.Root, "explorer.jsonl")
	if err := os.MkdirAll(intent.Launch.Root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, event := range []struct {
		kind    string
		payload any
	}{
		{"runtime.intent", *state.Intent}, {"runtime.source", *state.Source}, {"runtime.candidate", *state.Candidate}, {"runtime.thread", *state.Thread},
		{"runtime.turn-intent", map[string]string{"invocation_id": intent.Invocation.ID}}, {"runtime.turn", map[string]string{"id": "turn"}},
		{"runtime.terminal-response", telemetry}, {"runtime.stream-terminal", codexruntime.TurnStatus{ID: "turn", Status: "failed"}}, {"runtime.turn-status", codexruntime.TurnStatus{ID: "turn", Status: "failed"}},
	} {
		if _, err := journal.Append(runtimePath, event.kind, event.payload, func([]journal.Event) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	s, err = reconcileExplorerCapacityFailures(path, s)
	if err != nil {
		t.Fatal(err)
	}
	if s.ExplorerHost.RuntimeReceipt == nil || s.ExplorerHost.RuntimeReceipt.FailureCode != "serverOverloaded" || len(s.Explorations) != 0 {
		t.Fatal("failed receipt was not settled honestly")
	}
	if err := autonomousDispatchBlocked(s); err != nil {
		t.Fatal("known failure remained UNKNOWN", err)
	}
	if _, err := reconcileExplorerCapacityFailures(path, s); err != nil {
		t.Fatal("idempotent settlement failed", err)
	}
	usage, err := MeasureRunUsage(path)
	if err != nil || len(usage.Invocations) != 1 || !usage.Invocations[0].ReceiptMatched || usage.Invocations[0].Usage.Completed {
		t.Fatal("failed invocation usage counted as success", err)
	}
}

func TestExplorerCapacityRetryBoundAndReplay(t *testing.T) {
	question := "inspect task"
	s := Snapshot{ExplorerRuns: map[string]ExplorerHostState{"first": {Intent: ExplorerHostIntent{Question: question}}}}
	got, err := explorerCapacityRetryQuestion(s, question)
	if err != nil || got != question {
		t.Fatal("pending invocation retried")
	}
	host := s.ExplorerRuns["first"]
	host.RuntimeReceipt = &ExplorerRuntimeReceipt{FailureCode: "serverOverloaded"}
	s.ExplorerRuns["first"] = host
	retry := question + explorerCapacityRetrySuffix
	for range 3 {
		got, err = explorerCapacityRetryQuestion(s, question)
		if err != nil || got != retry {
			t.Fatal("retry identity changed", got, err)
		}
	}
	raw, _ := json.Marshal(s)
	var replay Snapshot
	if err := json.Unmarshal(raw, &replay); err != nil {
		t.Fatal(err)
	}
	got, err = explorerCapacityRetryQuestion(replay, question)
	if err != nil || got != retry {
		t.Fatal("restart changed retry identity")
	}
	s.ExplorerRuns["retry"] = ExplorerHostState{Intent: ExplorerHostIntent{Question: retry}, RuntimeReceipt: &ExplorerRuntimeReceipt{FailureCode: "serverOverloaded"}}
	if _, err := explorerCapacityRetryQuestion(s, question); err == nil {
		t.Fatal("second retry admitted")
	}
	if len(s.Explorations) != 0 {
		t.Fatal("failure counted as exploration")
	}
}

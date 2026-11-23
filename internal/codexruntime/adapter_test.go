package codexruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/codexrpc"
	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
)

func TestRetainedSemanticResultRejectsSubstitutionAndPreservesLegacyReplay(t *testing.T) {
	root := t.TempDir()
	client, done := peer(t, func(m codexrpc.Message) (any, bool) {
		if m.Method == "thread/start" {
			return threadResponse(root, "explicit-model"), false
		}
		return map[string]any{"turn": completedTurn()}, false
	})
	a := &Adapter{Client: client, Directory: root, JournalPath: filepath.Join(root, "source.db")}
	result, err := a.Execute(context.Background(), invocation(t))
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	<-done
	events, err := journal.Read(a.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "copy.db")
		for _, event := range events {
			if event.Kind == "runtime.result" || legacy && event.Kind == "runtime.semantic-result" {
				continue
			}
			if event.Kind == "runtime.semantic-result" {
				claimed := result
				cost := int64(1)
				claimed.Usage.CostMinorUnits = &cost
				if err := appendEvent(path, event.Kind, TurnResult{TurnID: "turn-1", Result: claimed}); err == nil {
					t.Fatal("retained output accepted unobserved cost")
				}
			}
			if err := appendEvent(path, event.Kind, event.Payload); err != nil {
				t.Fatal(err)
			}
		}
		if !legacy {
			changed := result
			changed.Output = "substituted output"
			if err := appendEvent(path, "runtime.result", TurnResult{TurnID: "turn-1", Result: changed}); err == nil {
				t.Fatal("accepted result substituted retained output")
			}
		}
		if err := appendEvent(path, "runtime.result", TurnResult{TurnID: "turn-1", Result: result}); err != nil {
			t.Fatalf("valid legacy=%v result rejected: %v", legacy, err)
		}
		state, err := Inspect(path)
		if err != nil || state.Result == nil || state.Result.Output != result.Output {
			t.Fatalf("valid replay lost output: %+v %v", state, err)
		}
	}
}

func TestInvalidAutoCompactionInvocationFailsBeforeRuntimeIntent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	profile := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "model", Effort: "high", Role: "writer"}
	i := runtime.Invocation{Version: 1, ID: "tampered", Profile: profile, Input: "write", CodexAutoCompactVersion: runtime.CodexAutoCompactVersion, CodexAutoCompactTokenLimit: 1000}
	a := &Adapter{JournalPath: path}
	if _, err := a.Execute(context.Background(), i); err == nil {
		t.Fatal("tampered invocation dispatched")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid invocation wrote runtime intent: stat err=%v", err)
	}
}

func TestRuntimeIntentReplayBindsAutoCompactionOption(t *testing.T) {
	profile := runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "model", Effort: "high", Role: "planner"}
	invocation, err := runtime.NewInvocationWithCodexAutoCompact(profile, "objective", &runtime.CodexAutoCompactOptions{Version: runtime.CodexAutoCompactVersion, TokenLimit: 50000})
	if err != nil {
		t.Fatal(err)
	}
	rawInvocation, err := json.Marshal(invocation)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip runtime.Invocation
	if err := json.Unmarshal(rawInvocation, &roundTrip); err != nil || roundTrip.Validate() != nil {
		t.Fatalf("option-bound invocation JSON failed validation: %s, %v", rawInvocation, err)
	}
	validPath := filepath.Join(t.TempDir(), "valid.jsonl")
	if err := appendEvent(validPath, "runtime.intent", Intent{Invocation: invocation, Directory: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if state, err := Inspect(validPath); err != nil || state.Intent == nil || state.Intent.Invocation.CodexAutoCompactOption() == nil {
		t.Fatalf("valid option-bound intent did not replay: %+v, %v", state.Intent, err)
	}
	forgedPath := filepath.Join(t.TempDir(), "forged.jsonl")
	forged := invocation
	forged.CodexAutoCompactTokenLimit = 50001
	if err := appendEvent(forgedPath, "runtime.intent", Intent{Invocation: forged, Directory: t.TempDir()}); err == nil {
		t.Fatal("runtime journal accepted an option changed after invocation identity was bound")
	}
}

func invocation(t *testing.T) runtime.Invocation {
	t.Helper()
	i, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "explicit-model", Effort: "high", Role: "planner"}, "inspect this fixture")
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func peer(t *testing.T, handler func(codexrpc.Message) (any, bool)) (*codexrpc.Client, <-chan error) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		r := bufio.NewReader(server)
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				done <- nil
				return
			}
			m, err := codexrpc.Decode(line)
			if err != nil {
				done <- err
				return
			}
			result, stop := handler(m)
			if stop {
				done <- nil
				return
			}
			b, err := json.Marshal(result)
			if err != nil {
				done <- err
				return
			}
			if _, err := fmt.Fprintf(server, "{\"id\":%s,\"result\":%s}\n", m.ID, b); err != nil {
				done <- nil
				return
			}
		}
	}()
	return codexrpc.New(client), done
}

func threadResponse(directory, model string) any {
	return map[string]any{"thread": map[string]any{"id": "thread-1"}, "model": model, "modelProvider": "openai", "reasoningEffort": "high", "cwd": directory, "approvalPolicy": "never", "sandbox": map[string]any{"type": "readOnly", "networkAccess": false}}
}

func completedTurn() any {
	return map[string]any{"id": "turn-1", "status": "completed", "items": []any{
		map[string]any{"id": "comment", "type": "agentMessage", "text": "working", "phase": "commentary"},
		map[string]any{"id": "answer", "type": "agentMessage", "text": "fixture final answer", "phase": "final_answer"},
	}}
}

func TestAdapterRecordsIntentBeforeDispatchAndAdmitsFinalOutput(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime.jsonl")
	violations := make(chan string, 4)
	client, done := peer(t, func(m codexrpc.Message) (any, bool) {
		s, err := Inspect(path)
		if err != nil {
			violations <- err.Error()
			return nil, true
		}
		switch m.Method {
		case "thread/start":
			var params map[string]json.RawMessage
			_ = json.Unmarshal(m.Params, &params)
			if string(params["environments"]) != "[]" || string(params["allowProviderModelFallback"]) != "false" {
				violations <- "missing environment or fallback restriction"
			}
			if s.Intent == nil || s.Thread != nil {
				violations <- "missing prior thread intent"
			}
			return threadResponse(root, "explicit-model"), false
		case "turn/start":
			if !s.TurnPending || s.Thread == nil {
				violations <- "missing prior turn intent"
			}
			var params map[string]any
			_ = json.Unmarshal(m.Params, &params)
			if params["effort"] != "high" || params["model"] != "explicit-model" || params["clientUserMessageId"] != s.Intent.Invocation.ID {
				violations <- "turn routing substitution"
			}
			if envs, ok := params["environments"].([]any); !ok || len(envs) != 0 {
				violations <- "turn has native environment access"
			}
			return map[string]any{"turn": completedTurn()}, false
		default:
			violations <- "unexpected dispatch"
			return nil, true
		}
	})
	a := &Adapter{Client: client, JournalPath: path, Directory: root}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	i := invocation(t)
	r, err := a.Execute(ctx, i)
	if err != nil {
		t.Fatal(err)
	}
	state, inspectErr := Inspect(path)
	if inspectErr != nil || state.RouteEvidence == nil || state.RouteEvidence.NotificationCoverage != "THROUGH_TERMINAL" || state.ExecutionOutcome != "completed" {
		t.Fatal("missing live terminal evidence", state, inspectErr)
	}
	if r.Output != "fixture final answer" || r.ObservedEffort == nil || *r.ObservedEffort != "high" || r.Usage.InputTokens != nil {
		t.Fatal("invalid admitted evidence", r)
	}
	if _, err := a.Execute(ctx, i); err == nil {
		t.Fatal("repeated invocation dispatched")
	}
	if _, err := a.Resume(ctx, "different"); err == nil {
		t.Fatal("wrong invocation resumed")
	}
	if _, err := a.Resume(ctx, i.ID); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(violations)
	for v := range violations {
		t.Error(v)
	}
}

func TestModelSubstitutionStopsBeforeTurn(t *testing.T) {
	root := t.TempDir()
	client, done := peer(t, func(m codexrpc.Message) (any, bool) { return threadResponse(root, "substituted-model"), false })
	a := &Adapter{Client: client, JournalPath: filepath.Join(root, "runtime.jsonl"), Directory: root}
	if _, err := a.Execute(context.Background(), invocation(t)); err == nil {
		t.Fatal("accepted substitution")
	}
	s, err := Inspect(a.JournalPath)
	if err != nil || s.Intent == nil || s.Thread != nil || s.TurnPending {
		t.Fatal("invalid substituted state", s, err)
	}
	_ = a.Close()
	<-done
}

func TestLostTurnResponseCannotBeResubmitted(t *testing.T) {
	root := t.TempDir()
	client, done := peer(t, func(m codexrpc.Message) (any, bool) {
		if m.Method == "thread/start" {
			return threadResponse(root, "explicit-model"), false
		}
		return nil, true
	})
	a := &Adapter{Client: client, JournalPath: filepath.Join(root, "runtime.jsonl"), Directory: root}
	i := invocation(t)
	if _, err := a.Execute(context.Background(), i); err == nil {
		t.Fatal("lost response succeeded")
	}
	s, err := Inspect(a.JournalPath)
	if err != nil || !s.TurnPending || s.TurnID != "" {
		t.Fatal("lost-response state", s, err)
	}
	if _, err := a.Resume(context.Background(), i.ID); err == nil {
		t.Fatal("resubmitted unknown turn")
	}
	_ = a.Close()
	<-done
}

func TestResumeOnlyReadsRecordedTurn(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime.jsonl")
	i := invocation(t)
	settings := codexrpc.ThreadSettings{ThreadID: "thread-1", Model: i.Profile.Model, Provider: i.Profile.Provider, Effort: &i.Profile.Effort, Directory: root, Approval: "never", Sandbox: "readOnly"}
	for _, event := range []struct {
		kind    string
		payload any
	}{
		{"runtime.intent", Intent{Invocation: i, Directory: root}}, {"runtime.thread", settings},
		{"runtime.turn-intent", struct {
			InvocationID string `json:"invocation_id"`
		}{i.ID}},
		{"runtime.turn", struct {
			ID string `json:"id"`
		}{"turn-1"}},
	} {
		if err := appendEvent(path, event.kind, event.payload); err != nil {
			t.Fatal(err)
		}
	}
	method := make(chan string, 2)
	client, done := peer(t, func(m codexrpc.Message) (any, bool) {
		method <- m.Method
		if m.Method == "thread/resume" {
			return threadResponse(root, "explicit-model"), false
		}
		return map[string]any{"thread": map[string]any{"id": "thread-1", "model": nil, "modelProvider": "openai", "reasoningEffort": nil, "cwd": root, "turns": []any{completedTurn()}}}, false
	})
	a := &Adapter{Client: client, JournalPath: path, Directory: root}
	r, err := a.Resume(context.Background(), i.ID)
	if err != nil || r.Output != "fixture final answer" {
		t.Fatal(r, err)
	}
	if got := <-method; got != "thread/resume" {
		t.Fatal("route not reattested", got)
	}
	if got := <-method; got != "thread/read" {
		t.Fatal("resumed by dispatching", got)
	}
	state, inspectErr := Inspect(path)
	if inspectErr != nil || state.RouteObserved == nil || state.RouteResumePending || state.RouteResumes != 1 {
		t.Fatal("route observation not durable", inspectErr)
	}
	_ = a.Close()
	<-done
}

func TestFailedTurnRetainsTerminalEvidenceWithoutResult(t *testing.T) {
	root := t.TempDir()
	client, done := peer(t, func(m codexrpc.Message) (any, bool) {
		if m.Method == "thread/start" {
			return threadResponse(root, "explicit-model"), false
		}
		return map[string]any{"turn": map[string]any{"id": "turn-1", "status": "failed", "items": []any{}, "error": map[string]any{"message": "fixture failure"}}}, false
	})
	a := &Adapter{Client: client, JournalPath: filepath.Join(root, "runtime.jsonl"), Directory: root}
	if _, err := a.Execute(context.Background(), invocation(t)); err == nil {
		t.Fatal("failed turn accepted")
	}
	s, err := Inspect(a.JournalPath)
	if err != nil || s.TurnStatus != "failed" || s.Result != nil {
		t.Fatal("lost terminal evidence", s, err)
	}
	_ = a.Close()
	<-done
}

func TestSummaryCompletionReadsFullTurnWithoutAnotherDispatch(t *testing.T) {
	root := t.TempDir()
	methods := make(chan string, 4)
	client, done := peer(t, func(m codexrpc.Message) (any, bool) {
		methods <- m.Method
		switch m.Method {
		case "thread/start":
			return threadResponse(root, "explicit-model"), false
		case "turn/start":
			return map[string]any{"turn": map[string]any{"id": "turn-1", "status": "completed", "itemsView": "summary", "items": []any{}}}, false
		case "thread/read":
			return map[string]any{"thread": map[string]any{"id": "thread-1", "model": "explicit-model", "modelProvider": "openai", "reasoningEffort": "high", "cwd": root, "turns": []any{completedTurn()}}}, false
		default:
			return nil, true
		}
	})
	a := &Adapter{Client: client, JournalPath: filepath.Join(root, "runtime.jsonl"), Directory: root}
	r, err := a.Execute(context.Background(), invocation(t))
	if err != nil || r.Output != "fixture final answer" {
		t.Fatal(r, err)
	}
	_ = a.Close()
	<-done
	close(methods)
	want := []string{"thread/start", "turn/start", "thread/read"}
	n := 0
	for method := range methods {
		if n >= len(want) || method != want[n] {
			t.Fatal("unexpected method sequence", method, n)
		}
		n++
	}
	if n != len(want) {
		t.Fatal("missing full-turn read")
	}
}

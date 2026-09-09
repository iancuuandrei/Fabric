package codexrpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"testing"
	"time"

	"harness.local/engorch/internal/runtime"
)

func TestTurnOutputRequiresCompletedFinalAnswer(t *testing.T) {
	i, err := runtime.NewInvocation(runtime.Profile{Runtime: "codex-app-server", Provider: "openai", Model: "exact", Effort: "high", Role: "planner"}, "objective")
	if err != nil {
		t.Fatal(err)
	}
	s := ThreadSettings{ThreadID: "thread", Model: "exact", Provider: "openai", Directory: t.TempDir(), Approval: "never", Sandbox: "readOnly"}
	turn := Turn{ID: "turn", Status: "completed", Items: []json.RawMessage{json.RawMessage(`{"id":"a","type":"agentMessage","text":"answer","phase":"final_answer"}`)}}
	r, err := turn.Result(i, s)
	if err != nil || r.ObservedEffort != nil {
		t.Fatal("missing effort invented", r, err)
	}
	for _, status := range []string{"inProgress", "failed", "interrupted", "unknown"} {
		bad := turn
		bad.Status = status
		if _, err := bad.Result(i, s); err == nil {
			t.Fatal("non-success admitted", status)
		}
	}
	bad := turn
	bad.ItemsView = "partial"
	if _, err := bad.Result(i, s); err == nil {
		t.Fatal("partial output admitted")
	}
	bad = turn
	bad.Items = append(bad.Items, bad.Items[0])
	if _, err := bad.Result(i, s); err == nil {
		t.Fatal("duplicate output item admitted")
	}
	bad = turn
	bad.Items = []json.RawMessage{json.RawMessage(`{"id":"tool","type":"commandExecution","text":"fake final answer"}`)}
	if _, err := bad.Result(i, s); err == nil {
		t.Fatal("tool text admitted as assistant answer")
	}
	rerouteParams := json.RawMessage(`{"fromModel":"exact","toModel":"other","reason":"highRiskCyberActivity","threadId":"thread","turnId":"turn"}`)
	if _, err := Completion(Message{Method: "model/rerouted", Params: rerouteParams}, "thread", "turn"); !errors.Is(err, ErrRouteIdentityContradiction) {
		t.Fatal("model rerouting ignored")
	} else {
		var reroute *RerouteError
		if !errors.As(err, &reroute) || string(reroute.Event.Params) != string(rerouteParams) {
			t.Fatal("reroute evidence not preserved", reroute, err)
		}
	}
	if _, err := Completion(Message{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"other","turn":{"id":"turn","status":"completed","items":[]}}`)}, "thread", "turn"); err == nil {
		t.Fatal("other thread completion admitted")
	}
}

func scriptedResponse(t *testing.T, notification string, result any, call func(context.Context, *Client) error) Message {
	t.Helper()
	clientStream, server := net.Pipe()
	c := New(clientStream)
	defer c.Close()
	requestCh := make(chan Message, 1)
	errCh := make(chan error, 1)
	go func() {
		defer server.Close()
		line, err := bufio.NewReader(server).ReadBytes('\n')
		if err != nil {
			errCh <- err
			return
		}
		request, err := Decode(line[:len(line)-1])
		if err != nil {
			errCh <- err
			return
		}
		requestCh <- request
		if notification != "" {
			if _, err = fmt.Fprintln(server, notification); err != nil {
				errCh <- err
				return
			}
		}
		body, err := json.Marshal(result)
		if err == nil {
			_, err = fmt.Fprintf(server, "{\"id\":%s,\"result\":%s}\n", request.ID, body)
		}
		errCh <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	callErr := call(ctx, c)
	request := <-requestCh
	if serverErr := <-errCh; serverErr != nil {
		t.Fatal(serverErr)
	}
	if callErr != nil {
		t.Log("scripted call returned", callErr)
	}
	return request
}

func resumeResult(directory, model string, effort any) map[string]any {
	return map[string]any{
		"thread":          map[string]any{"id": "thread"},
		"model":           model,
		"modelProvider":   "openai",
		"reasoningEffort": effort,
		"serviceTier":     "priority",
		"cwd":             directory,
		"approvalPolicy":  "never",
		"sandbox":         map[string]any{"type": "readOnly", "networkAccess": false},
	}
}

func TestResumeThreadObservesExactRouteWithoutOverrides(t *testing.T) {
	directory := filepath.Clean(t.TempDir())
	effort := "high"
	recorded := ThreadSettings{ThreadID: "thread", Model: "exact", Provider: "openai", Effort: &effort, Directory: directory, Approval: "never", Sandbox: "readOnly"}
	var observed ThreadSettings
	var callErr error
	request := scriptedResponse(t, "", resumeResult(directory, "exact", "high"), func(ctx context.Context, c *Client) error {
		observed, callErr = c.ResumeThread(ctx, recorded)
		return callErr
	})
	if callErr != nil || observed.Effort == nil || *observed.Effort != effort || observed.ServiceTier == nil || *observed.ServiceTier != "priority" {
		t.Fatal(observed, callErr)
	}
	if request.Method != "thread/resume" || string(request.Params) != `{"threadId":"thread"}` {
		t.Fatalf("resume supplied an override or started work: %s %s", request.Method, request.Params)
	}
}

func TestResumeThreadRejectsUnknownAndContradictoryRoutes(t *testing.T) {
	directory := filepath.Clean(t.TempDir())
	effort := "high"
	recorded := ThreadSettings{ThreadID: "thread", Model: "exact", Provider: "openai", Effort: &effort, Directory: directory, Approval: "never", Sandbox: "readOnly"}
	for _, tc := range []struct {
		name   string
		result map[string]any
		want   error
	}{
		{name: "different model", result: resumeResult(directory, "other", "high"), want: ErrContinuationRouteMismatch},
		{name: "different model precedes absent effort", result: resumeResult(directory, "other", nil), want: ErrContinuationRouteMismatch},
		{name: "absent effort", result: resumeResult(directory, "exact", nil), want: ErrRouteIdentityUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var observed ThreadSettings
			var callErr error
			scriptedResponse(t, "", tc.result, func(ctx context.Context, c *Client) error {
				observed, callErr = c.ResumeThread(ctx, recorded)
				return callErr
			})
			if !errors.Is(callErr, tc.want) || observed.ThreadID != "thread" {
				t.Fatal(observed, callErr)
			}
		})
	}
}

func TestResumeAndReadRejectReroutingAndReadAllowsNullableRouteMetadata(t *testing.T) {
	directory := filepath.Clean(t.TempDir())
	effort := "high"
	settings := ThreadSettings{ThreadID: "thread", Model: "exact", Provider: "openai", Effort: &effort, Directory: directory}
	rerouted := `{"method":"model/rerouted","params":{"fromModel":"exact","toModel":"other","reason":"highRiskCyberActivity","threadId":"thread","turnId":"turn"}}`
	var resumeErr error
	scriptedResponse(t, rerouted, resumeResult(directory, "exact", "high"), func(ctx context.Context, c *Client) error {
		_, resumeErr = c.ResumeThread(ctx, settings)
		return resumeErr
	})
	if !errors.Is(resumeErr, ErrContinuationRouteMismatch) {
		t.Fatal(resumeErr)
	}
	var resumeReroute *RerouteError
	if !errors.As(resumeErr, &resumeReroute) || string(resumeReroute.Event.Params) != `{"fromModel":"exact","toModel":"other","reason":"highRiskCyberActivity","threadId":"thread","turnId":"turn"}` {
		t.Fatal("resume reroute evidence not preserved", resumeReroute, resumeErr)
	}

	readResult := map[string]any{"thread": map[string]any{
		"id": "thread", "cwd": directory, "model": nil, "modelProvider": "openai", "reasoningEffort": nil,
		"turns": []any{map[string]any{"id": "turn", "status": "completed", "items": []any{}}},
	}}
	var turn Turn
	var readErr error
	request := scriptedResponse(t, "", readResult, func(ctx context.Context, c *Client) error {
		turn, readErr = c.ReadTurn(ctx, settings, "turn")
		return readErr
	})
	if readErr != nil || turn.ID != "turn" || request.Method != "thread/read" {
		t.Fatal(turn, request.Method, readErr)
	}

	scriptedResponse(t, rerouted, readResult, func(ctx context.Context, c *Client) error {
		_, readErr = c.ReadTurn(ctx, settings, "turn")
		return readErr
	})
	if !errors.Is(readErr, ErrRouteIdentityContradiction) {
		t.Fatal(readErr)
	}
}

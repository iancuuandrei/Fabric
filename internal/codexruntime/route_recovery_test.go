package codexruntime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/runtime"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func seedRecovery(t *testing.T) (string, string, runtime.Invocation, codexrpc.ThreadSettings) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "runtime.db")
	i := invocation(t)
	s := codexrpc.ThreadSettings{ThreadID: "thread-1", Model: i.Profile.Model, Provider: i.Profile.Provider, Effort: &i.Profile.Effort, Directory: root, Approval: "never", Sandbox: "readOnly"}
	for _, e := range []struct {
		k string
		p any
	}{
		{"runtime.intent", Intent{i, root}}, {"runtime.thread", s},
		{"runtime.turn-intent", map[string]string{"invocation_id": i.ID}},
		{"runtime.turn", map[string]string{"id": "turn-1"}},
	} {
		if err := appendEvent(path, e.k, e.p); err != nil {
			t.Fatal(err)
		}
	}
	return root, path, i, s
}

func TestResumeRouteFailureIsDurableAndNeverDispatches(t *testing.T) {
	for _, test := range []struct {
		name    string
		model   string
		missing bool
		want    error
		failure string
	}{
		{"mismatch", "other", false, codexrpc.ErrContinuationRouteMismatch, "CONTINUATION_ROUTE_MISMATCH"},
		{"missing_effort", "explicit-model", true, codexrpc.ErrRouteIdentityUnknown, "UNKNOWN"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, path, i, _ := seedRecovery(t)
			calls := 0
			client, done := peer(t, func(m codexrpc.Message) (any, bool) {
				calls++
				if m.Method != "thread/resume" {
					return nil, true
				}
				r := threadResponse(root, test.model).(map[string]any)
				if test.missing {
					r["reasoningEffort"] = nil
				}
				return r, false
			})
			a := &Adapter{Client: client, JournalPath: path, Directory: root}
			if _, err := a.Resume(context.Background(), i.ID); !errors.Is(err, test.want) {
				t.Fatalf("wrong error: %v", err)
			}
			if test.failure == "CONTINUATION_ROUTE_MISMATCH" {
				if _, err := a.Resume(context.Background(), i.ID); !errors.Is(err, test.want) {
					t.Fatal(err)
				}
			}
			_ = a.Close()
			<-done
			state, err := Inspect(path)
			if err != nil || state.RouteFailure != test.failure || state.Result != nil || state.RouteResumePending || calls != 1 {
				t.Fatalf("invalid recovery evidence: %+v calls=%d err=%v", state, calls, err)
			}
		})
	}
}

func TestReplayCannotEraseRouteContradiction(t *testing.T) {
	for _, kind := range []string{"runtime.route-resume-intent", "runtime.route-resumed", "runtime.result"} {
		t.Run(kind, func(t *testing.T) {
			_, path, _, s := seedRecovery(t)
			if kind == "runtime.route-resumed" {
				if err := appendEvent(path, "runtime.route-resume-intent", s); err != nil {
					t.Fatal(err)
				}
			}
			if err := appendEvent(path, "runtime.route-contradiction", RouteFailureEvidence{Reason: "ROUTE_IDENTITY_CONTRADICTION"}); err != nil {
				t.Fatal(err)
			}
			if err := appendEvent(path, kind, s); err == nil {
				t.Fatal("contradiction erased by", kind)
			}
		})
	}
}

func TestRouteObservationClearedForNextAttempt(t *testing.T) {
	_, path, _, s := seedRecovery(t)
	for _, e := range []struct {
		k string
		p any
	}{
		{"runtime.route-resume-intent", s}, {"runtime.route-resumed", s}, {"runtime.route-resume-intent", s},
		{"runtime.route-resume-failed", RouteFailureEvidence{Reason: "UNKNOWN"}},
	} {
		if err := appendEvent(path, e.k, e.p); err != nil {
			t.Fatal(err)
		}
	}
	state, err := Inspect(path)
	if err != nil || state.RouteObserved != nil || state.RouteFailure != "UNKNOWN" || state.RouteResumes != 2 {
		t.Fatalf("stale route: %+v %v", state, err)
	}
}

func TestContradictionBeforeThreadIsDurable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime.db")
	i := invocation(t)
	if err := appendEvent(path, "runtime.intent", Intent{i, root}); err != nil {
		t.Fatal(err)
	}
	if err := appendEvent(path, "runtime.route-contradiction", RouteFailureEvidence{Reason: "ROUTE_IDENTITY_CONTRADICTION"}); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(path)
	if err != nil || state.Thread != nil || state.RouteFailure != "ROUTE_IDENTITY_CONTRADICTION" {
		t.Fatal(state, err)
	}
}

func TestExecutePersistsEarlyRerouteNotification(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime.db")
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		line, err := bufio.NewReader(server).ReadBytes('\n')
		if err != nil {
			return
		}
		request, err := codexrpc.Decode(line)
		if err != nil {
			return
		}
		fmt.Fprintf(server, "%s\n", `{"method":"model/rerouted","params":{"fromModel":"explicit-model","toModel":"other","threadId":"thread-1","turnId":"turn-1","reason":"fixture"}}`)
		fmt.Fprintf(server, `{"id":%s,"result":{}}`+"\n", request.ID)
	}()
	a := &Adapter{Client: codexrpc.New(client), JournalPath: path, Directory: root}
	_, err := a.Execute(context.Background(), invocation(t))
	_ = a.Close()
	<-done
	if !errors.Is(err, codexrpc.ErrRouteIdentityContradiction) {
		t.Fatal(err)
	}
	state, err := Inspect(path)
	if err != nil || state.RouteFailure != "ROUTE_IDENTITY_CONTRADICTION" || state.Thread != nil {
		t.Fatal(state, err)
	}
	events, err := journal.Read(path)
	if err != nil || len(events) != 2 || events[1].Kind != "runtime.route-contradiction" {
		t.Fatal("missing route event", err)
	}
}

func TestContinuationMismatchPreservesHistoricalRouteAndUnknownOutcome(t *testing.T) {
	for _, reason := range []string{"CONTINUATION_ROUTE_MISMATCH", "ROUTE_IDENTITY_CONTRADICTION"} {
		t.Run(reason, func(t *testing.T) {
			_, path, _, dispatch := seedRecovery(t)
			if err := appendEvent(path, "runtime.route-resume-intent", dispatch); err != nil {
				t.Fatal(err)
			}
			observed := dispatch
			observed.Model = "different"
			observed.Effort = nil
			if err := appendEvent(path, "runtime.route-resume-failed", RouteFailureEvidence{Reason: reason, Observed: &observed}); err != nil {
				t.Fatal(err)
			}
			state, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			e := state.RouteEvidence
			if e == nil || e.DispatchObserved.Model != dispatch.Model || e.DispatchObserved.Effort == nil || *e.DispatchObserved.Effort != *dispatch.Effort || e.ContinuationConfiguration.Model != "different" || e.ContinuationConfiguration.Effort != nil || e.ContinuationStatus != "CONTINUATION_ROUTE_MISMATCH" || state.ExecutionOutcome != "UNKNOWN" {
				t.Fatalf("history rewritten: %+v", state)
			}
			events, err := journal.Read(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(events[len(events)-1].Payload), reason) {
				t.Fatal("legacy event rewritten")
			}
			if err := appendEvent(path, "runtime.route-resume-intent", dispatch); err == nil {
				t.Fatal("blocked continuation retried")
			}
		})
	}
}

package control

import (
	"context"
	"strings"
	"testing"

	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/taskscheduler"
)

func staticQueueTestContext(withBinding bool) context.Context {
	if !withBinding {
		return context.Background()
	}
	return context.WithValue(context.Background(), scheduledDispatchJournalContextKey{}, scheduledDispatchJournalBinding{
		path:       "/tmp/schedule.jsonl",
		scheduleID: strings.Repeat("a", 64),
		claimID:    strings.Repeat("b", 64),
	})
}

func staticQueueTestTurn() *taskscheduler.AgentTurnBinding {
	return &taskscheduler.AgentTurnBinding{
		ParentAgentID: strings.Repeat("c", 64),
		AgentID:       strings.Repeat("d", 64),
		TurnID:        strings.Repeat("e", 64),
		TurnSequence:  1,
	}
}

func TestStaticExplorerQueueDepthGrantsQueueOnlyToStaticScheduledExplorers(t *testing.T) {
	bound := staticQueueTestContext(true)
	turnCtx := withScheduledAgentTurn(bound, staticQueueTestTurn())
	invocation := func(role string) runtime.Invocation {
		return runtime.Invocation{Version: 1, ID: strings.Repeat("f", 64), Profile: runtime.Profile{Role: role}, Input: "question"}
	}
	cases := []struct {
		name       string
		ctx        context.Context
		role       string
		wantQueued bool
	}{
		{"unbound explorer", context.Background(), "explorer", false},
		{"unbound writer", context.Background(), "writer", false},
		{"static scheduled explorer", bound, "explorer", true},
		{"static scheduled writer", bound, "writer", false},
		{"static scheduled planner", bound, "planner", false},
		{"static scheduled fixer", bound, "fixer", false},
		{"static scheduled reviewer", bound, "reviewer", false},
		{"dynamic scheduled explorer keeps composite path", turnCtx, "explorer", false},
		{"dynamic scheduled writer", turnCtx, "writer", false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := staticExplorerQueueDepth(test.ctx, invocation(test.role))
			want := 0
			if test.wantQueued {
				want = compositeToolQueueLimit
			}
			if got != want {
				t.Fatalf("queue depth %d, want %d", got, want)
			}
			if want != 0 && want != 32 {
				t.Fatalf("queue depth %d is not the frozen serial-fifo bound", want)
			}
		})
	}
	if compositeToolQueueLimit != 32 {
		t.Fatal("serial-fifo queue bound changed without updating static explorer policy")
	}
}

func TestWriterFixerQueueDepthGrantsQueueOnlyToWriterAndFixer(t *testing.T) {
	invocation := func(role string) runtime.Invocation {
		return runtime.Invocation{Version: 1, ID: strings.Repeat("f", 64), Profile: runtime.Profile{Role: role}, Input: "question"}
	}
	cases := []struct {
		role       string
		wantQueued bool
	}{
		{"writer", true},
		{"fixer", true},
		{"explorer", false},
		{"planner", false},
		{"reviewer", false},
	}
	for _, test := range cases {
		t.Run(test.role, func(t *testing.T) {
			got := writerFixerQueueDepth(invocation(test.role))
			want := 0
			if test.wantQueued {
				want = compositeToolQueueLimit
			}
			if got != want {
				t.Fatalf("queue depth %d, want %d", got, want)
			}
		})
	}
}

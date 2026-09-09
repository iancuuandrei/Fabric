package codexruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/codexrpc"
	"harness.local/engorch/internal/journal"
)

func usageParams(total int64) map[string]any {
	counts := map[string]any{"inputTokens": total - 1, "cachedInputTokens": 0, "outputTokens": 1, "reasoningOutputTokens": 0, "totalTokens": total}
	return map[string]any{"threadId": "thread-1", "turnId": "turn-1", "tokenUsage": map[string]any{"last": counts, "total": counts}}
}

func TestLiveUsageJournalAndBudgetStop(t *testing.T) {
	for _, mode := range []string{"bounded", "exhausted", "unlimited"} {
		t.Run(mode, func(t *testing.T) {
			exhausted := mode == "exhausted"
			unlimited := mode == "unlimited"
			root := t.TempDir()
			path := filepath.Join(root, "usage.db")
			local, remote := net.Pipe()
			done := make(chan error, 1)
			go func() {
				defer remote.Close()
				reader := bufio.NewReader(remote)
				read := func() (codexrpc.Message, error) {
					line, err := reader.ReadBytes('\n')
					if err != nil {
						return codexrpc.Message{}, err
					}
					return codexrpc.Decode(line)
				}
				send := func(v any) error { b, _ := json.Marshal(v); _, err := remote.Write(append(b, '\n')); return err }
				m, err := read()
				if err != nil {
					done <- err
					return
				}
				if err = send(map[string]any{"id": m.ID, "result": threadResponse(root, "explicit-model")}); err != nil {
					done <- err
					return
				}
				m, err = read()
				if err != nil {
					done <- err
					return
				}
				state, err := Inspect(path)
				if err != nil || state.UsagePolicy == nil {
					done <- fmt.Errorf("baseline not durable before dispatch: %v", err)
					return
				}
				if err = send(map[string]any{"id": m.ID, "result": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}}}); err != nil {
					done <- err
					return
				}
				totals := []int64{10, 30, 30}
				if unlimited {
					totals = []int64{150000, 250000}
				}
				if exhausted {
					totals = []int64{99, 105}
				}
				for _, total := range totals {
					if err = send(map[string]any{"method": "thread/tokenUsage/updated", "params": usageParams(total)}); err != nil {
						done <- err
						return
					}
				}
				if exhausted {
					m, err = read()
					if err != nil || m.Method != "turn/interrupt" {
						done <- fmt.Errorf("expected interrupt, got %s: %v", m.Method, err)
						return
					}
					state, err := Inspect(path)
					if err != nil || state.UsageFailure != "BUDGET_EXHAUSTED" {
						done <- fmt.Errorf("budget stop not durable before interrupt: %v", err)
						return
					}
					done <- nil
					return
				}
				done <- send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-1", "turn": completedTurn()}})
			}()
			a := &Adapter{Client: codexrpc.New(local), JournalPath: path, Directory: root, UsageBudget: 100, RequireLiveUsage: true, UsageQualified: true}
			if unlimited {
				a.UnlimitedTokens = true
				a.UsageBudget = 0
			}
			want := int64(30)
			if unlimited {
				want = 250000
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := a.Execute(ctx, invocation(t))
			if exhausted {
				if err == nil || !strings.Contains(err.Error(), "BUDGET_EXHAUSTED") {
					t.Fatal(err)
				}
			} else if err != nil || result.Usage.Accounting == nil || result.Usage.Accounting.Delta.TotalTokens != want {
				t.Fatal(result, err)
			}
			if unlimited && result.Usage.Accounting.Budget != nil {
				t.Fatal("unlimited created numeric cap")
			}
			state, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			if exhausted && (state.Result != nil || state.UsageReceipt.Budget.Overshoot != 5 || state.ExecutionOutcome != "UNKNOWN") {
				t.Fatal(state)
			}
			events, err := journal.Read(path)
			if err != nil {
				t.Fatal(err)
			}
			for n, e := range events {
				if e.Kind == "runtime.usage-normalized" && (n == 0 || events[n-1].Kind != "runtime.usage-raw") {
					t.Fatal("normalization before raw persistence")
				}
			}
			a.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStrictUsageRequiresQualificationBeforeDispatch(t *testing.T) {
	a := &Adapter{RequireLiveUsage: true, UsageBudget: 100, JournalPath: filepath.Join(t.TempDir(), "unused.db")}
	_, err := a.Execute(context.Background(), invocation(t))
	if err == nil || !strings.Contains(err.Error(), "LIVE_USAGE_NOT_QUALIFIED") {
		t.Fatal(err)
	}
}

func TestStrictMissingUsageBlocksOutput(t *testing.T) {
	root := t.TempDir()
	client, done := peer(t, func(m codexrpc.Message) (any, bool) {
		if m.Method == "thread/start" {
			return threadResponse(root, "explicit-model"), false
		}
		return map[string]any{"turn": completedTurn()}, false
	})
	a := &Adapter{Client: client, Directory: root, JournalPath: filepath.Join(root, "usage.db"), RequireLiveUsage: true, UsageQualified: true, UsageBudget: 100}
	_, err := a.Execute(context.Background(), invocation(t))
	if err == nil || !strings.Contains(err.Error(), "USAGE_MISSING") {
		t.Fatal(err)
	}
	state, err := Inspect(a.JournalPath)
	if err != nil || state.Result != nil || state.UsageFailure != "USAGE_MISSING" {
		t.Fatal(state, err)
	}
	a.Close()
	<-done
}

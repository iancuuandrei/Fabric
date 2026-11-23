package codexruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/codexrpc"
	"github.com/iancuuandrei/fabric/internal/codexusage"
	"github.com/iancuuandrei/fabric/internal/journal"
)

func usageParams(total int64) map[string]any {
	counts := map[string]any{"inputTokens": total - 1, "cachedInputTokens": 0, "outputTokens": 1, "reasoningOutputTokens": 0, "totalTokens": total}
	return map[string]any{"threadId": "thread-1", "turnId": "turn-1", "tokenUsage": map[string]any{"last": counts, "total": counts}}
}

func totalOnlyLastUsageParams() []byte {
	raw, _ := json.Marshal(map[string]any{
		"threadId": "thread-1", "turnId": "turn-1",
		"tokenUsage": map[string]any{
			"last":  map[string]any{"inputTokens": 0, "cachedInputTokens": 0, "outputTokens": 0, "reasoningOutputTokens": 0, "cacheWriteInputTokens": 0, "totalTokens": 19175},
			"total": map[string]any{"inputTokens": 155748, "cachedInputTokens": 123648, "outputTokens": 2384, "reasoningOutputTokens": 1052, "cacheWriteInputTokens": 0, "totalTokens": 158132},
		},
	})
	return raw
}

func TestTotalOnlyLastUsageUsesVersionedNormalizerAndPreservesLegacyFailure(t *testing.T) {
	pending := &codexrpc.Message{Method: "thread/tokenUsage/updated", Params: totalOnlyLastUsageParams()}
	newState := func() State {
		tracker, err := codexusage.FreshTracker("thread-1", "turn-1", nil)
		if err != nil {
			t.Fatal(err)
		}
		return State{
			Thread:       &codexrpc.ThreadSettings{ThreadID: "thread-1"},
			TurnPending:  true,
			UsageTurnID:  "turn-1",
			UsagePolicy:  &UsagePolicy{ThreadID: "thread-1", Origin: "FRESH_THREAD"},
			UsagePending: pending,
			usageTracker: tracker,
		}
	}
	legacyState := newState()
	legacy := normalizeUsageLegacy(&legacyState)
	const legacyFailure = "codex usage notification is invalid: last: totalTokens does not equal inputTokens plus outputTokens"
	if legacy.Version != 0 || legacy.Failure != legacyFailure {
		t.Fatalf("legacy normalizer changed historical failure: %+v", legacy)
	}
	legacyBytes, err := canonical.Bytes(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyState.usageEvent("runtime.usage-normalized", legacyBytes); err != nil || legacyState.UsageFailure != legacyFailure {
		t.Fatalf("historical normalization did not replay its stored failure: state=%+v err=%v", legacyState, err)
	}
	stopBytes, err := canonical.Bytes(UsageStop{Reason: legacyFailure, ThreadID: "thread-1", TurnID: "turn-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyState.usageEvent("runtime.usage-interrupt-intent", stopBytes); err != nil || !legacyState.UsageInterrupt {
		t.Fatalf("historical usage interrupt did not remain replayable: state=%+v err=%v", legacyState, err)
	}

	state := newState()
	current := normalizeUsage(&state)
	if current.Version != 2 || current.Failure != "" || current.Receipt.Coverage != "OBSERVED" || current.Receipt.After.TotalTokens != 158132 || current.Receipt.Delta.InputTokens != 155748 || current.Receipt.Delta.OutputTokens != 2384 {
		t.Fatalf("new normalizer did not retain only cumulative typed usage: %+v", current)
	}
	currentBytes, err := canonical.Bytes(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.usageEvent("runtime.usage-normalized", currentBytes); err != nil || state.UsageFailure != "" || state.UsageReceipt == nil || state.UsageReceipt.After.TotalTokens != 158132 {
		t.Fatalf("versioned current normalization failed: state=%+v err=%v", state, err)
	}
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
	if err != nil || state.Result != nil || state.SemanticResult == nil || state.UsageFailure != "USAGE_MISSING" {
		t.Fatal(state, err)
	}
	before, err := journal.Read(a.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	retained, resumeErr := a.Resume(context.Background(), invocation(t).ID)
	if !errors.Is(resumeErr, ErrUsageReconciliation) {
		t.Fatalf("resume bypassed accounting admission: %v", resumeErr)
	}
	want, _ := canonical.Hash("retained", *state.SemanticResult)
	got, _ := canonical.Hash("retained", retained)
	if got != want || retained.Usage.InputTokens != nil || retained.Usage.OutputTokens != nil {
		t.Fatal("resume discarded semantic output or invented accounting")
	}
	after, err := journal.Read(a.JournalPath)
	if err != nil || len(after) != len(before) {
		t.Fatalf("blocked resume changed durable state: %v", err)
	}
	crashPath := filepath.Join(root, "before-usage-block.db")
	for _, event := range before {
		if event.Kind == "runtime.usage-blocked" {
			break
		}
		if err := appendEvent(crashPath, event.Kind, event.Payload); err != nil {
			t.Fatal(err)
		}
	}
	// A closed client proves the retained-output path makes no RPC after restart.
	a.Close()
	<-done
	restarted := &Adapter{Client: client, Directory: root, JournalPath: crashPath}
	crashState, err := Inspect(crashPath)
	if err != nil || crashState.SemanticResult == nil || crashState.UsageFailure != "" {
		t.Fatalf("invalid crash-boundary fixture: %+v %v", crashState, err)
	}
	crashResult, crashErr := restarted.Resume(context.Background(), invocation(t).ID)
	if !errors.Is(crashErr, ErrUsageReconciliation) || crashResult.Output != state.SemanticResult.Output {
		t.Fatalf("crash restart discarded output or bypassed accounting: %+v %v", crashResult, crashErr)
	}
}

func TestOptionalMalformedUsageRetainsSemanticOutputAndLegacyResultRemainsStrict(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "optional-usage.db")
	left, right := net.Pipe()
	methods := make(chan string, 8)
	done := make(chan error, 1)
	go func() {
		defer right.Close()
		reader := bufio.NewReader(right)
		for {
			line, err := reader.ReadBytes('\n')
			if err != nil {
				done <- nil
				return
			}
			message, err := codexrpc.Decode(line)
			if err != nil {
				done <- err
				return
			}
			methods <- message.Method
			switch message.Method {
			case "thread/start":
				encoded, _ := json.Marshal(threadResponse(root, "explicit-model"))
				_, err = fmt.Fprintf(right, `{"id":%s,"result":%s}`+"\n", message.ID, encoded)
			case "turn/start":
				// Malformed optional accounting must not interrupt a completed turn.
				_, err = fmt.Fprint(right, `{"method":"thread/tokenUsage/updated","params":{"threadId":"thread-1","turnId":"turn-1","tokenUsage":{"last":{}}}}`+"\n")
				if err == nil {
					encoded, _ := json.Marshal(map[string]any{"turn": completedTurn()})
					_, err = fmt.Fprintf(right, `{"id":%s,"result":%s}`+"\n", message.ID, encoded)
				}
			default:
				done <- fmt.Errorf("unexpected RPC %s", message.Method)
				return
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	a := &Adapter{Client: codexrpc.New(left), JournalPath: path, Directory: root}
	result, err := a.Execute(context.Background(), invocation(t))
	if err != nil || result.Output != "fixture final answer" || result.Usage.Accounting != nil || result.Usage.InputTokens != nil || result.Usage.OutputTokens != nil || result.Usage.CostMinorUnits != nil {
		t.Fatalf("optional usage loss discarded output or invented accounting: %+v %v", result, err)
	}
	state, err := Inspect(path)
	if err != nil || state.Result == nil || state.SemanticResult == nil || state.UsageFailure == "" || state.UsageReceipt != nil {
		events, _ := journal.Read(path)
		kinds := make([]string, 0, len(events))
		for _, event := range events {
			kinds = append(kinds, event.Kind)
		}
		var normalizeDetail string
		debugPath := filepath.Join(t.TempDir(), "usage-normalize-debug.db")
		for _, event := range events {
			if err := appendEvent(debugPath, event.Kind, event.Payload); err != nil {
				normalizeDetail = "prefix append: " + err.Error()
				break
			}
			if event.Kind == "runtime.usage-raw" {
				debugState, debugErr := Inspect(debugPath)
				if debugErr != nil {
					normalizeDetail = "prefix inspect: " + debugErr.Error()
				} else {
					normalized := normalizeUsage(&debugState)
					normalizeDetail = fmt.Sprintf("failure=%q append=%v", normalized.Failure, appendEvent(debugPath, "runtime.usage-normalized", normalized))
				}
				break
			}
		}
		t.Fatalf("optional degradation was not durably represented: %+v events=%s detail=%s err=%v", state, strings.Join(kinds, ","), normalizeDetail, err)
	}
	legacyPath := filepath.Join(root, "legacy-result.db")
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "runtime.result-degraded" || event.Kind == "runtime.result" {
			break
		}
		if err := appendEvent(legacyPath, event.Kind, event.Payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := appendEvent(legacyPath, "runtime.result", TurnResult{TurnID: state.TurnID, Result: result}); err == nil {
		t.Fatal("legacy runtime.result replay accepted unresolved optional metadata")
	}
	retained, resumeErr := a.Resume(context.Background(), invocation(t).ID)
	if resumeErr != nil || retained.Output != result.Output || retained.Usage.InputTokens != nil || retained.Usage.OutputTokens != nil {
		t.Fatalf("degraded replay did not return retained output without accounting: %+v %v", retained, resumeErr)
	}
	_ = a.Close()
	for {
		select {
		case method := <-methods:
			if method == "turn/interrupt" {
				t.Fatal("optional malformed usage interrupted provider work")
			}
		default:
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}

func TestNumericUsageBudgetRequiresQualificationEvenWhenLiveUsageFlagIsOff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget.db")
	a := &Adapter{JournalPath: path, UsageBudget: 10, UsageQualified: false}
	if _, err := a.Execute(context.Background(), invocation(t)); err == nil {
		t.Fatal("numeric token budget dispatched without usage qualification")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unqualified budget wrote runtime intent")
	}
}

func TestOptionalUsageNormalizationFailureRequiresSameReadableJournal(t *testing.T) {
	t.Run("same pending notification may degrade", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "usage.jsonl")
		before, normalized := pendingUsageNormalizationFixture(t, path)
		_, fallback, err := inspectOptionalUsageNormalizationFailure(path, before, normalized, errors.New("simulated optional metadata write failure"))
		if err != nil || !fallback || !sameUsagePending(before, mustInspectUsage(t, path)) {
			t.Fatalf("valid optional fallback rejected: fallback=%v err=%v", fallback, err)
		}
	})
	t.Run("journal lock stops observer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "usage.jsonl")
		before, normalized := pendingUsageNormalizationFixture(t, path)
		if err := os.WriteFile(path+".lock", []byte("other writer"), 0600); err != nil {
			t.Fatal(err)
		}
		_, fallback, err := inspectOptionalUsageNormalizationFailure(path, before, normalized, journalLockUnavailableForTest(path))
		if fallback || !errors.Is(err, journal.ErrLockUnavailable) {
			t.Fatalf("locked journal allowed optional fallback: fallback=%v err=%v", fallback, err)
		}
	})
	t.Run("corrupt journal stops observer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "usage.jsonl")
		before, normalized := pendingUsageNormalizationFixture(t, path)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := f.Write([]byte("not-a-journal-event\n"))
		closeErr := f.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			t.Fatal(err)
		}
		_, fallback, err := inspectOptionalUsageNormalizationFailure(path, before, normalized, errors.New("simulated normalization write failure"))
		if fallback || err == nil {
			t.Fatalf("corrupt journal allowed optional fallback: fallback=%v err=%v", fallback, err)
		}
	})
	t.Run("append that committed is recognized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "usage.jsonl")
		before, normalized := pendingUsageNormalizationFixture(t, path)
		if err := appendEvent(path, "runtime.usage-normalized", normalized); err != nil {
			t.Fatal(err)
		}
		_, fallback, err := inspectOptionalUsageNormalizationFailure(path, before, normalized, errors.New("simulated post-append error"))
		if err != nil || fallback {
			t.Fatalf("durable normalization was not recognized: fallback=%v err=%v", fallback, err)
		}
	})
}

func TestOptionalUsageRawFailureRequiresSameReadableJournal(t *testing.T) {
	t.Run("unchanged optional journal may degrade", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "usage.jsonl")
		before, message := pendingUsageRawAppendFixture(t, path)
		committed, fallback, err := inspectOptionalUsageRawFailure(path, before, message, errors.New("simulated raw append failure"))
		if err != nil || committed || !fallback {
			t.Fatalf("unchanged optional journal did not degrade safely: committed=%v fallback=%v err=%v", committed, fallback, err)
		}
	})
	t.Run("lock stops observer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "usage.jsonl")
		before, message := pendingUsageRawAppendFixture(t, path)
		appendErr := journalLockUnavailableForTest(path)
		committed, fallback, err := inspectOptionalUsageRawFailure(path, before, message, appendErr)
		if committed || fallback || !errors.Is(err, journal.ErrLockUnavailable) {
			t.Fatalf("locked raw append degraded: committed=%v fallback=%v err=%v", committed, fallback, err)
		}
	})
	t.Run("replacement journal identity stops observer", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "usage.jsonl")
		before, message := pendingUsageRawAppendFixture(t, path)
		otherPath := filepath.Join(t.TempDir(), "other.jsonl")
		_, _ = pendingUsageRawAppendFixture(t, otherPath)
		other, _ := journal.Read(otherPath)
		if len(other) == 0 {
			t.Fatal("replacement fixture has no journal events")
		}
		replacement, err := journal.ExportJSONL(otherPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, replacement, 0600); err != nil {
			t.Fatal(err)
		}
		committed, fallback, err := inspectOptionalUsageRawFailure(path, before, message, errors.New("simulated raw append failure"))
		if committed || fallback || err == nil {
			t.Fatalf("replacement invocation journal was accepted: committed=%v fallback=%v err=%v", committed, fallback, err)
		}
	})
	t.Run("committed raw notification is recognized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "usage.jsonl")
		before, message := pendingUsageRawAppendFixture(t, path)
		if err := appendEvent(path, "runtime.usage-raw", message); err != nil {
			t.Fatal(err)
		}
		committed, fallback, err := inspectOptionalUsageRawFailure(path, before, message, errors.New("simulated post-append error"))
		if err != nil || !committed || fallback {
			t.Fatalf("durable raw notification was not recognized: committed=%v fallback=%v err=%v", committed, fallback, err)
		}
	})
}

func pendingUsageRawAppendFixture(t *testing.T, path string) (State, codexrpc.Message) {
	t.Helper()
	_, _ = pendingUsageNormalizationFixture(t, path)
	events, err := journal.Read(path)
	if err != nil || len(events) == 0 {
		t.Fatalf("read fixture events: %v", err)
	}
	var message codexrpc.Message
	if events[len(events)-1].Kind != "runtime.usage-raw" || canonical.Decode(events[len(events)-1].Payload, &message) != nil {
		t.Fatal("fixture does not end in a usage raw notification")
	}
	basePath := path + ".before-raw"
	for _, event := range events[:len(events)-1] {
		if err := appendEvent(basePath, event.Kind, event.Payload); err != nil {
			t.Fatalf("copy prefix event %s: %v", event.Kind, err)
		}
	}
	jsonl, err := journal.ExportJSONL(basePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, jsonl, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	return state, message
}

func pendingUsageNormalizationFixture(t *testing.T, path string) (State, UsageNormalized) {
	t.Helper()
	profile := invocation(t)
	thread := codexrpc.ThreadSettings{ThreadID: "thread-1", Model: profile.Profile.Model, Provider: profile.Profile.Provider, Directory: filepath.Dir(path), Approval: "never", Sandbox: "readOnly"}
	effort := profile.Profile.Effort
	thread.Effort = &effort
	for _, record := range []struct {
		kind string
		body any
	}{
		{"runtime.intent", Intent{Invocation: profile, Directory: filepath.Dir(path)}},
		{"runtime.thread", thread},
		{"runtime.usage-baseline", UsagePolicy{Version: 1, ThreadID: thread.ThreadID, Baseline: codexusage.TokenUsage{CacheWriteInputTokens: int64ptr(0)}, Origin: "FRESH_THREAD"}},
		{"runtime.turn-intent", struct {
			InvocationID string `json:"invocation_id"`
		}{profile.ID}},
		{"runtime.turn", struct {
			ID string `json:"id"`
		}{"turn-1"}},
		{"runtime.usage-start", UsageStart{TurnID: "turn-1"}},
	} {
		if err := appendEvent(path, record.kind, record.body); err != nil {
			t.Fatalf("append %s: %v", record.kind, err)
		}
	}
	params, err := json.Marshal(usageParams(10))
	if err != nil {
		t.Fatal(err)
	}
	message := codexrpc.Message{Method: "thread/tokenUsage/updated", Params: params}
	if err := appendEvent(path, "runtime.usage-raw", message); err != nil {
		t.Fatal(err)
	}
	jsonl, err := journal.ExportJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, jsonl, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	return state, normalizeUsage(&state)
}

func int64ptr(value int64) *int64 { return &value }

func mustInspectUsage(t *testing.T, path string) State {
	t.Helper()
	state, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func sameUsagePending(before, after State) bool {
	return before.Intent != nil && after.Intent != nil && before.Intent.Invocation.ID == after.Intent.Invocation.ID &&
		before.Thread != nil && after.Thread != nil && before.Thread.ThreadID == after.Thread.ThreadID &&
		before.TurnID == after.TurnID && before.UsageTurnID == after.UsageTurnID && before.UsagePending != nil && after.UsagePending != nil &&
		before.UsagePending.Method == after.UsagePending.Method && string(before.UsagePending.Params) == string(after.UsagePending.Params)
}

func journalLockUnavailableForTest(path string) error {
	if err := os.WriteFile(path+".lock", []byte("held"), 0600); err != nil {
		return err
	}
	return journal.ErrLockUnavailable
}

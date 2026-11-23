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
	"github.com/iancuuandrei/fabric/internal/journal"
)

func makeCompactionNotification(method, threadID, turnID, itemID string) codexrpc.Message {
	params, _ := json.Marshal(map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
		"item":     map[string]any{"id": itemID, "type": "contextCompaction"},
	})
	return codexrpc.Message{Method: method, Params: params}
}

func itemNotification(method, turnID, itemID, itemType string) codexrpc.Message {
	params, _ := json.Marshal(map[string]any{
		"threadId": "thread-1",
		"turnId":   turnID,
		"item":     map[string]any{"id": itemID, "type": itemType, "text": "ordinary output"},
	})
	return codexrpc.Message{Method: method, Params: params}
}

func timedCompactionNotification(method, timestampField string, timestamp any) codexrpc.Message {
	params := map[string]any{
		"threadId": "thread-1",
		"turnId":   "turn-1",
		"item":     map[string]any{"id": "compact-timed", "type": "contextCompaction"},
	}
	if timestampField != "" {
		params[timestampField] = timestamp
	}
	raw, _ := json.Marshal(params)
	return codexrpc.Message{Method: method, Params: raw}
}

func compactionProtocolPeer(t *testing.T, root string, notifications []codexrpc.Message, turn any, readback any, beforeTurn func() error) (*codexrpc.Client, <-chan error, chan string) {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan error, 1)
	methods := make(chan string, 8)
	go func() {
		defer server.Close()
		reader := bufio.NewReader(server)
		respond := func(id json.RawMessage, value any) error {
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(server, `{"jsonrpc":"2.0","id":%s,"result":%s}`+"\n", id, encoded)
			return err
		}
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
				if err := respond(message.ID, threadResponse(root, "explicit-model")); err != nil {
					done <- nil
					return
				}
			case "turn/start":
				if beforeTurn != nil {
					if err := beforeTurn(); err != nil {
						done <- err
						return
					}
				}
				for _, notification := range notifications {
					params := notification.Params
					if params == nil {
						params = json.RawMessage(`{}`)
					}
					if _, err := fmt.Fprintf(server, `{"jsonrpc":"2.0","method":%q,"params":%s}`+"\n", notification.Method, params); err != nil {
						done <- nil
						return
					}
				}
				if err := respond(message.ID, map[string]any{"turn": turn}); err != nil {
					done <- nil
					return
				}
			case "thread/read":
				if err := respond(message.ID, map[string]any{"thread": map[string]any{
					"id": "thread-1", "model": "explicit-model", "modelProvider": "openai",
					"reasoningEffort": "high", "cwd": root, "turns": []any{readback},
				}}); err != nil {
					done <- nil
					return
				}
			default:
				done <- fmt.Errorf("unexpected RPC method %q", message.Method)
				return
			}
		}
	}()
	return codexrpc.New(client), done, methods
}

func runCompactionFixture(t *testing.T, notifications []codexrpc.Message, turn any, readback any) (string, *Adapter, error, []string) {
	return runCompactionFixtureWithHook(t, notifications, turn, readback, nil)
}

func runCompactionFixtureWithHook(t *testing.T, notifications []codexrpc.Message, turn any, readback any, beforeTurn func(path string) error) (string, *Adapter, error, []string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "runtime.jsonl")
	var hook func() error
	if beforeTurn != nil {
		hook = func() error { return beforeTurn(path) }
	}
	client, done, methods := compactionProtocolPeer(t, root, notifications, turn, readback, hook)
	a := &Adapter{Client: client, JournalPath: path, Directory: root}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, executeErr := a.Execute(ctx, invocation(t))
	_ = a.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(methods)
	var got []string
	for method := range methods {
		got = append(got, method)
	}
	return path, a, executeErr, got
}

func completedTurnWithItems(itemsView string, items []any) any {
	return map[string]any{"id": "turn-1", "status": "completed", "itemsView": itemsView, "items": items}
}

func completedTurnWithCompaction(itemsView string, itemIDs ...string) any {
	items := []any{
		map[string]any{"id": "comment", "type": "agentMessage", "text": "working", "phase": "commentary"},
	}
	for _, id := range itemIDs {
		items = append(items, map[string]any{"id": id, "type": "contextCompaction"})
	}
	items = append(items, map[string]any{"id": "answer", "type": "agentMessage", "text": "fixture final answer", "phase": "final_answer"})
	return completedTurnWithItems(itemsView, items)
}

func TestAutomaticCompactionLifecycleIsMetadataOnlyAndInvocationBound(t *testing.T) {
	path, a, err, methods := runCompactionFixture(t, []codexrpc.Message{
		makeCompactionNotification("item/started", "thread-1", "turn-1", "compact-1"),
		itemNotification("item/started", "turn-1", "agent-1", "agentMessage"),
		makeCompactionNotification("item/completed", "thread-1", "turn-1", "compact-1"),
	}, completedTurnWithCompaction("full", "compact-1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[0] != "thread/start" || methods[1] != "turn/start" {
		t.Fatalf("unexpected RPCs; automatic observation must not send a compact request: %v", methods)
	}
	s, err := Inspect(path)
	if err != nil || s.Result == nil || s.Compaction == nil || s.Compaction.Coverage != "OBSERVED" || s.Compaction.Count != 1 {
		t.Fatalf("complete matching lifecycle was not admitted as metadata: %+v, %v", s.Compaction, err)
	}
	usage, err := MeasureContext(path)
	if err != nil || usage.CompactionCount == nil || *usage.CompactionCount != 1 || usage.CompactionCover != "OBSERVED" {
		t.Fatalf("typed usage did not report the proven pair: %+v, %v", usage, err)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Kind == "runtime.compaction-item" {
			count++
			var got CompactionLifecycle
			if err := canonical.Decode(event.Payload, &got); err != nil || got.ThreadID != "thread-1" || got.TurnID != "turn-1" || got.ItemID != "compact-1" {
				t.Fatalf("compaction event lost exact binding: %+v, %v", got, err)
			}
		}
	}
	if count != 2 {
		t.Fatalf("wanted exactly start/completion metadata, got %d", count)
	}
	_, headBefore, err := InspectWithHead(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Resume(context.Background(), s.Intent.Invocation.ID); err != nil {
		t.Fatal("completed invocation should return its existing result", err)
	}
	_, headAfter, err := InspectWithHead(path)
	if err != nil || headBefore != headAfter {
		t.Fatal("resume changed completed invocation or replayed work", headBefore, headAfter, err)
	}
}

func TestAutomaticCompactionEmptyItemsViewAcceptsExactLifecyclePair(t *testing.T) {
	path, _, err, methods := runCompactionFixture(t, []codexrpc.Message{
		makeCompactionNotification("item/started", "thread-1", "turn-1", "compact-1"),
		makeCompactionNotification("item/completed", "thread-1", "turn-1", "compact-1"),
	}, completedTurnWithCompaction("", "compact-1"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(methods) != 2 || methods[0] != "thread/start" || methods[1] != "turn/start" {
		t.Fatalf("unexpected RPCs; automatic observation must not send a compact request: %v", methods)
	}
	s, err := Inspect(path)
	if err != nil || s.Result == nil || s.Compaction == nil || s.Compaction.Coverage != "OBSERVED" || s.Compaction.Count != 1 {
		t.Fatalf("empty ItemsView with exact matching lifecycle was not admitted: %+v, %v", s.Compaction, err)
	}
	usage, err := MeasureContext(path)
	if err != nil || usage.CompactionCount == nil || *usage.CompactionCount != 1 || usage.CompactionCover != "OBSERVED" {
		t.Fatalf("typed usage did not report the proven empty-view pair: %+v, %v", usage, err)
	}
}

func TestLargeOrdinaryItemNotificationsDoNotInvalidateCompactionCoverage(t *testing.T) {
	text := strings.Repeat("ordinary user content ", 1200)
	started, _ := json.Marshal(map[string]any{
		"threadId": "thread-1", "turnId": "turn-1", "startedAtMs": 1791012510000,
		"item": map[string]any{"id": "user-1", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": text, "text_elements": []any{}}}},
	})
	completed, _ := json.Marshal(map[string]any{
		"threadId": "thread-1", "turnId": "turn-1", "completedAtMs": 1791012510001,
		"item": map[string]any{"id": "user-1", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": text, "text_elements": []any{}}}},
	})
	notifications := []codexrpc.Message{
		{Method: "item/started", Params: started},
		{Method: "item/completed", Params: completed},
	}
	for _, message := range notifications {
		if len(message.Params) <= maxCompactionParams || len(message.Params) > codexrpc.MaxMessage {
			t.Fatalf("fixture does not exercise the bounded non-compaction path: params=%d", len(message.Params))
		}
	}
	path, _, executeErr, _ := runCompactionFixture(t, notifications, completedTurn(), nil)
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	s, err := Inspect(path)
	if err != nil || s.Result == nil || s.Compaction != nil {
		t.Fatalf("ordinary large items poisoned or fabricated compaction evidence: result=%v summary=%+v err=%v", s.Result != nil, s.Compaction, err)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "runtime.compaction-unknown" || event.Kind == "runtime.compaction-item" {
			t.Fatalf("ordinary user item produced compaction evidence: %s", event.Kind)
		}
	}
}

func TestCompactionNotificationAcceptsMatchingOptionalTimestampOnly(t *testing.T) {
	path, _, executeErr, _ := runCompactionFixture(t, []codexrpc.Message{
		timedCompactionNotification("item/started", "startedAtMs", int64(1791012510000)),
		timedCompactionNotification("item/completed", "completedAtMs", int64(1791012510001)),
	}, completedTurnWithCompaction("full", "compact-timed"), nil)
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	s, err := Inspect(path)
	if err != nil || s.Compaction == nil || s.Compaction.Coverage != "OBSERVED" || s.Compaction.Count != 1 {
		t.Fatalf("matching protocol timestamps changed lifecycle pairing: summary=%+v err=%v", s.Compaction, err)
	}
}

func TestOversizedOrMalformedCompactionNotificationFailsClosed(t *testing.T) {
	oversized := makeCompactionNotification("item/started", "thread-1", "turn-1", "compact-large")
	oversized.Params = json.RawMessage(strings.TrimSuffix(string(oversized.Params), "}") + `,"padding":"` + strings.Repeat("x", maxCompactionParams) + `"}`)
	if len(oversized.Params) <= maxCompactionParams {
		t.Fatal("oversized compaction fixture is below the strict compaction bound")
	}
	for _, message := range []codexrpc.Message{
		oversized,
		{Method: "item/started", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"id":"c","type":"contextCompaction"},"startedAtMs":"not-a-number"}`)},
		{Method: "item/started", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"id":"c","type":"contextCompaction"},"completedAtMs":1791012510000}`)},
		{Method: "item/completed", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"id":"c","type":"contextCompaction"},"metadata":true}`)},
	} {
		_, relevant, err := compactionNotification(message)
		if !relevant || err == nil {
			t.Fatalf("invalid context compaction notification was not rejected: relevant=%v err=%v", relevant, err)
		}
	}
}

func TestNoCompactionKeepsLegacyReceiptShapeAndDoesNotClaimZero(t *testing.T) {
	path, _, err, _ := runCompactionFixture(t, nil, completedTurn(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil || s.Compaction != nil {
		t.Fatal("absence of lifecycle evidence became an asserted zero", s.Compaction, err)
	}
	usage, err := MeasureContext(path)
	if err != nil || usage.CompactionCount != nil || usage.CompactionCover != "" {
		t.Fatal("legacy usage receipt gained a zero compaction claim", usage, err)
	}
	rawUsage, err := json.Marshal(usage)
	if err != nil || strings.Contains(string(rawUsage), "compaction_") {
		t.Fatal("legacy usage JSON shape changed without compaction evidence", string(rawUsage), err)
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "runtime.compaction-item" || event.Kind == "runtime.compaction-unknown" {
			t.Fatal("no-compaction execution changed the runtime journal prefix", event.Kind)
		}
	}
}

func TestAutomaticCompactionAmbiguityNeverReportsZeroOrCompletion(t *testing.T) {
	cases := []struct {
		name          string
		notifications []codexrpc.Message
		turn          any
		readback      any
		wantRead      bool
	}{
		{name: "unmatched start", notifications: []codexrpc.Message{makeCompactionNotification("item/started", "thread-1", "turn-1", "compact-1")}, turn: completedTurn()},
		{name: "turn mismatch before response", notifications: []codexrpc.Message{makeCompactionNotification("item/started", "thread-1", "turn-other", "compact-1"), makeCompactionNotification("item/completed", "thread-1", "turn-other", "compact-1")}, turn: completedTurn()},
		{name: "duplicate phase", notifications: []codexrpc.Message{makeCompactionNotification("item/started", "thread-1", "turn-1", "compact-1"), makeCompactionNotification("item/started", "thread-1", "turn-1", "compact-1"), makeCompactionNotification("item/completed", "thread-1", "turn-1", "compact-1")}, turn: completedTurn()},
		{name: "completion before start", notifications: []codexrpc.Message{makeCompactionNotification("item/completed", "thread-1", "turn-1", "compact-1"), makeCompactionNotification("item/started", "thread-1", "turn-1", "compact-1")}, turn: completedTurn()},
		{name: "malformed compaction item", notifications: []codexrpc.Message{{Method: "item/started", Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","item":{"id":7,"type":"contextCompaction"}}`)}}, turn: completedTurn()},
		{name: "summary readback completion alone", turn: completedTurnWithItems("summary", nil), readback: completedTurnWithItems("full", []any{
			map[string]any{"id": "compact-1", "type": "contextCompaction"},
			map[string]any{"id": "answer", "type": "agentMessage", "text": "fixture final answer", "phase": "final_answer"},
		}), wantRead: true},
		{name: "full turn unpaired compaction item", turn: completedTurnWithCompaction("full", "compact-1")},
		{name: "empty items view unpaired compaction item", turn: completedTurnWithCompaction("", "compact-1")},
		{name: "full turn matched item plus extra compaction item", notifications: []codexrpc.Message{
			makeCompactionNotification("item/started", "thread-1", "turn-1", "compact-1"),
			makeCompactionNotification("item/completed", "thread-1", "turn-1", "compact-1"),
		}, turn: completedTurnWithCompaction("full", "compact-1", "compact-extra")},
		{name: "paired notifications absent from full turn", notifications: []codexrpc.Message{
			makeCompactionNotification("item/started", "thread-1", "turn-1", "compact-1"),
			makeCompactionNotification("item/completed", "thread-1", "turn-1", "compact-1"),
		}, turn: completedTurn()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, _, executeErr, methods := runCompactionFixture(t, tc.notifications, tc.turn, tc.readback)
			if executeErr != nil {
				t.Fatal(executeErr)
			}
			if tc.wantRead && (len(methods) != 3 || methods[2] != "thread/read") {
				t.Fatal("summary turn was not read back", methods)
			}
			s, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			if s.Result == nil || s.Compaction == nil || s.Compaction.Coverage != "UNKNOWN" || s.Compaction.Count != 0 {
				t.Fatalf("ambiguous evidence produced an accepted count: result=%v summary=%+v", s.Result != nil, s.Compaction)
			}
			usage, err := MeasureContext(path)
			if err != nil || usage.CompactionCount != nil || usage.CompactionCover != "UNKNOWN" {
				t.Fatalf("ambiguous evidence escaped into typed usage: %+v, %v", usage, err)
			}
		})
	}
}

func TestAutomaticCompactionLifecycleCapLeavesCoverageUnknown(t *testing.T) {
	items := make([]codexrpc.Message, 0, (maxCompactionItems+1)*2)
	for n := 0; n < maxCompactionItems+1; n++ {
		id := fmt.Sprintf("compact-%02d", n)
		items = append(items, makeCompactionNotification("item/started", "thread-1", "turn-1", id))
		items = append(items, makeCompactionNotification("item/completed", "thread-1", "turn-1", id))
	}
	path, _, err, _ := runCompactionFixture(t, items, completedTurn(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil || s.Compaction == nil || s.Compaction.Coverage != "UNKNOWN" || s.Compaction.Count != 0 {
		t.Fatal("bounded overflow claimed full lifecycle coverage", s.Compaction, err)
	}
}

func TestInterruptedAutomaticCompactionRemainsUnknown(t *testing.T) {
	turn := map[string]any{"id": "turn-1", "status": "interrupted", "items": []any{}}
	path, _, executeErr, _ := runCompactionFixture(t, []codexrpc.Message{
		makeCompactionNotification("item/started", "thread-1", "turn-1", "compact-1"),
	}, turn, nil)
	if executeErr == nil {
		t.Fatal("interrupted turn was admitted")
	}
	s, err := Inspect(path)
	if err != nil || s.Result != nil || s.Compaction == nil || s.Compaction.Coverage != "UNKNOWN" || s.Compaction.Count != 0 {
		t.Fatal("interrupted stream asserted completion or zero", s, err)
	}
}

func TestCompactionJournalLockFailureCannotProduceCountOrResult(t *testing.T) {
	path, _, executeErr, _ := runCompactionFixtureWithHook(t, []codexrpc.Message{
		makeCompactionNotification("item/started", "thread-1", "turn-1", "compact-1"),
		makeCompactionNotification("item/completed", "thread-1", "turn-1", "compact-1"),
	}, completedTurn(), nil, func(path string) error {
		return os.Chmod(path, 0444)
	})
	if executeErr == nil {
		t.Fatal("observer continued after the runtime journal became unavailable")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Inspect(path)
	if err != nil || s.Result != nil || s.Compaction != nil {
		t.Fatal("journal-observation failure produced a result or compaction claim", s, err)
	}
	usage, err := MeasureContext(path)
	if err != nil || usage.CompactionCount != nil || usage.CompactionCover != "" {
		t.Fatal("journal-observation failure produced a typed count or zero", usage, err)
	}
}

func TestCompactionAppendFailurePreservesSemanticResultAsUnknown(t *testing.T) {
	sourcePath, _, executeErr, _ := runCompactionFixture(t, nil, completedTurn(), nil)
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	sourceState, err := Inspect(sourcePath)
	if err != nil || sourceState.SemanticResult == nil || sourceState.Result == nil {
		t.Fatalf("source fixture lacks validated result: %+v %v", sourceState, err)
	}
	events, err := journal.Read(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "compaction-unavailable.db")
	for _, event := range events {
		if event.Kind == "runtime.semantic-result" || event.Kind == "runtime.result" {
			break
		}
		if err := appendEvent(path, event.Kind, event.Payload); err != nil {
			t.Fatal(err)
		}
	}
	adapter := &Adapter{JournalPath: path}
	if err := adapter.degradeCompactionAppend(errors.New("synthetic advisory append failure"), sourceState.Thread.ThreadID, sourceState.TurnID); err != nil || !adapter.compactionUnavailable {
		t.Fatalf("validated journal failure did not degrade advisory capture: flag=%v err=%v", adapter.compactionUnavailable, err)
	}
	semantic := SemanticTurnResult{TurnID: sourceState.TurnID, Result: *sourceState.SemanticResult, CompactionUnavailable: true}
	if err := appendEvent(path, "runtime.semantic-result", semantic); err != nil {
		t.Fatal(err)
	}
	accepted := TurnResult{TurnID: sourceState.TurnID, Result: *sourceState.Result, CompactionUnavailable: true}
	if err := appendEvent(path, "runtime.result", accepted); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(path)
	if err != nil || state.Result == nil || state.Result.Output != sourceState.Result.Output || state.Compaction == nil || state.Compaction.Coverage != "UNKNOWN" || state.Compaction.Count != 0 {
		t.Fatalf("advisory append loss erased output or asserted a count: result=%+v compaction=%+v err=%v", state.Result, state.Compaction, err)
	}
}

func TestCompactionJournalRejectsForgedAndMismatchedLifecycle(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "runtime.jsonl")
	i := invocation(t)
	thread := codexrpc.ThreadSettings{ThreadID: "thread-1", Model: i.Profile.Model, Provider: i.Profile.Provider, Effort: &i.Profile.Effort, Directory: root, Approval: "never", Sandbox: "readOnly"}
	for _, item := range []struct {
		kind    string
		payload any
	}{
		{"runtime.intent", Intent{Invocation: i, Directory: root}},
		{"runtime.thread", thread},
		{"runtime.turn-intent", struct {
			InvocationID string `json:"invocation_id"`
		}{i.ID}},
	} {
		if err := appendEvent(path, item.kind, item.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := appendEvent(path, "runtime.compaction-item", CompactionLifecycle{Version: compactionLifecycleVersion, ThreadID: "other-thread", TurnID: "turn-1", ItemID: "compact-1", Phase: "started"}); err == nil {
		t.Fatal("forged thread lifecycle accepted")
	}
	if err := appendEvent(path, "runtime.compaction-item", CompactionLifecycle{Version: compactionLifecycleVersion, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "compact-1", Phase: "started"}); err != nil {
		t.Fatal(err)
	}
	if err := appendEvent(path, "runtime.compaction-item", CompactionLifecycle{Version: compactionLifecycleVersion, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "compact-1", Phase: "started"}); err == nil {
		t.Fatal("forged duplicate lifecycle phase accepted")
	}
}

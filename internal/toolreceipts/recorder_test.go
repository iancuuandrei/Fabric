package toolreceipts

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/toolbridge"
)

func TestBindingQueuePolicyVersionsPreserveLegacyIdentity(t *testing.T) {
	legacy := Binding{
		Version: 1, InvocationID: strings.Repeat("a", 64), CallerBindingSHA256: strings.Repeat("b", 64), CatalogSHA256: strings.Repeat("c", 64),
		Tools: []ToolOwner{{Tool: "source_list", Owner: OwnerContext}, {Tool: "agent_wait", Owner: OwnerAgent}},
	}
	legacyID, err := legacy.ID()
	if err != nil || legacyID != "6efab76ecfce8a9f39b63acf6059fc1e57ef27d1aef25f4fee4fe123a6303c94" {
		t.Fatal("legacy binding identity changed", legacyID, err)
	}
	legacy.BindingID = legacyID
	raw, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(raw), "queue_policy") || strings.Contains(string(raw), "max_queued_calls") {
		t.Fatal("legacy binding wire shape changed", string(raw), err)
	}
	queued := cloneBinding(legacy)
	queued.Version = 2
	queued.BindingID = ""
	queued.QueuePolicy = QueuePolicySerialFIFO
	queued.MaxQueuedCalls = 4
	queuedID, err := queued.ID()
	if err != nil || queuedID == legacyID {
		t.Fatal("queued binding did not derive a distinct valid identity", queuedID, err)
	}
	for name, mutate := range map[string]func(*Binding){
		"v1 policy":             func(b *Binding) { b.QueuePolicy = QueuePolicySerialFIFO },
		"v1 capacity":           func(b *Binding) { b.MaxQueuedCalls = 1 },
		"v2 missing policy":     func(b *Binding) { b.Version, b.MaxQueuedCalls = 2, 1 },
		"v2 zero capacity":      func(b *Binding) { b.Version, b.QueuePolicy = 2, QueuePolicySerialFIFO },
		"v2 oversized capacity": func(b *Binding) { b.Version, b.QueuePolicy, b.MaxQueuedCalls = 2, QueuePolicySerialFIFO, 33 },
		"v2 substituted policy": func(b *Binding) { b.Version, b.QueuePolicy, b.MaxQueuedCalls = 2, "parallel", 4 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := Binding{Version: 1, InvocationID: legacy.InvocationID, CallerBindingSHA256: legacy.CallerBindingSHA256, CatalogSHA256: legacy.CatalogSHA256, Tools: append([]ToolOwner(nil), legacy.Tools...)}
			mutate(&changed)
			if id, err := changed.ID(); err == nil || id != "" {
				t.Fatal("malformed queue policy admitted", id, err)
			}
		})
	}
}

func TestOpenBindsQueuePolicyWithoutEnforcingIt(t *testing.T) {
	var contextCalls, agentCalls atomic.Int32
	owners := fixtureOwners(&contextCalls, &agentCalls, nil)
	catalog, err := CatalogSHA256(owners)
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/queued-receipts.db"
	recorder, err := Open(Config{
		Path: path, InvocationID: strings.Repeat("a", 64), CallerBindingSHA256: strings.Repeat("b", 64), CatalogSHA256: catalog, Owners: owners,
		QueuePolicy: QueuePolicySerialFIFO, MaxQueuedCalls: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := recorder.Binding()
	if binding.Version != 2 || binding.QueuePolicy != QueuePolicySerialFIFO || binding.MaxQueuedCalls != 8 {
		t.Fatal("queue policy absent from recorder binding", binding)
	}
	state, err := Inspect(path)
	if err != nil || state.Binding == nil || !reflect.DeepEqual(*state.Binding, binding) {
		t.Fatal("durable queue binding differs", state, err)
	}
	for _, config := range []Config{
		{Path: t.TempDir() + "/bad-policy", InvocationID: strings.Repeat("a", 64), CallerBindingSHA256: strings.Repeat("b", 64), CatalogSHA256: catalog, Owners: owners, QueuePolicy: "parallel", MaxQueuedCalls: 8},
		{Path: t.TempDir() + "/bad-capacity", InvocationID: strings.Repeat("a", 64), CallerBindingSHA256: strings.Repeat("b", 64), CatalogSHA256: catalog, Owners: owners, QueuePolicy: QueuePolicySerialFIFO, MaxQueuedCalls: 33},
	} {
		if recorder, err := Open(config); err == nil || recorder != nil {
			t.Fatal("malformed recorder queue configuration admitted", config, err)
		}
	}
}

func TestRecorderRoutesDisjointToolsAndValidatesTranscript(t *testing.T) {
	var contextCalls, agentCalls atomic.Int32
	owners := fixtureOwners(&contextCalls, &agentCalls, nil)
	recorder := openFixture(t, t.TempDir()+"/receipts.db", owners)
	projection := recorder.Projection()

	contextCall := toolbridge.Call{RequestID: json.RawMessage(`"context-1"`), Tool: "source_list", Arguments: json.RawMessage(`{"limit":2}`)}
	contextResult, err := projection.Call(context.Background(), contextCall)
	if err != nil || string(contextResult.JSON) != `{"files":["a.go"]}` {
		t.Fatal("context callback failed", string(contextResult.JSON), err)
	}
	agentCall := toolbridge.Call{RequestID: json.RawMessage(`2`), Tool: "agent_wait", Arguments: json.RawMessage(`{"limit":1}`)}
	agentResult, err := projection.Call(context.Background(), agentCall)
	if err != nil || string(agentResult.JSON) != `{"status":"idle"}` {
		t.Fatal("agent callback failed", string(agentResult.JSON), err)
	}
	if contextCalls.Load() != 1 || agentCalls.Load() != 1 {
		t.Fatal("call routed to wrong owner", contextCalls.Load(), agentCalls.Load())
	}

	state, err := Inspect(t.TempDir() + "/missing.db")
	if err != nil || state.Binding != nil || len(state.Calls) != 0 {
		t.Fatal("missing journal should inspect empty", state, err)
	}
	state, err = Inspect(recorder.path)
	if err != nil || state.Binding == nil || len(state.Calls) != 2 || state.Calls[0].Receipt == nil || state.Calls[0].Receipt.Owner != OwnerContext || state.Calls[1].Receipt == nil || state.Calls[1].Receipt.Owner != OwnerAgent {
		t.Fatal("unexpected receipt state", state, err)
	}
	observations := []Observation{{Call: contextCall, Result: contextResult}, {Call: agentCall, Result: agentResult}}
	if err := state.ValidateTranscript(recorder.Binding(), observations); err != nil {
		t.Fatal("exact transcript rejected", err)
	}
	assertTranscriptSubstitutionsRejected(t, state, recorder.Binding(), observations)
}

func TestDuplicateRequestNeverRedispatchesIncludingAfterOpen(t *testing.T) {
	var contextCalls, agentCalls atomic.Int32
	owners := fixtureOwners(&contextCalls, &agentCalls, nil)
	path := t.TempDir() + "/receipts.db"
	recorder := openFixture(t, path, owners)
	call := toolbridge.Call{RequestID: json.RawMessage(`"same"`), Tool: "source_list", Arguments: json.RawMessage(`{"limit":2}`)}
	if _, err := recorder.Projection().Call(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Projection().Call(context.Background(), call); !errors.Is(err, ErrDuplicateRequest) {
		t.Fatal("duplicate request not rejected", err)
	}
	reopened := openFixture(t, path, owners)
	if _, err := reopened.Projection().Call(context.Background(), call); !errors.Is(err, ErrDuplicateRequest) {
		t.Fatal("reopened duplicate request not rejected", err)
	}
	changed := call
	changed.Arguments = json.RawMessage(`{"limit":3}`)
	if _, err := reopened.Projection().Call(context.Background(), changed); !errors.Is(err, ErrRequestConflict) {
		t.Fatal("request identity substitution not rejected", err)
	}
	if contextCalls.Load() != 1 {
		t.Fatal("duplicate redispatched", contextCalls.Load())
	}
}

func TestIntentOnlyOnCallbackErrorCancellationAndInvalidResult(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, toolbridge.Call) (toolbridge.Result, error)
	}{
		{name: "callback error", call: func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
			return toolbridge.Result{}, errors.New("owner failed")
		}},
		{name: "cancel after callback", call: nil},
		{name: "noncanonical result", call: func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
			return toolbridge.Result{JSON: json.RawMessage(`{"score":1.5}`)}, nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var cancel context.CancelFunc
			callback := test.call
			if callback == nil {
				callback = func(context.Context, toolbridge.Call) (toolbridge.Result, error) {
					cancel()
					return toolbridge.Result{JSON: json.RawMessage(`{"files":[]}`)}, nil
				}
			}
			owners := customOwners(callback)
			recorder := openFixture(t, t.TempDir()+"/receipts.db", owners)
			ctx := context.Background()
			if test.name == "cancel after callback" {
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
			}
			_, err := recorder.Projection().Call(ctx, toolbridge.Call{RequestID: json.RawMessage(`1`), Tool: "source_list", Arguments: json.RawMessage(`{}`)})
			if err == nil {
				t.Fatal("failed callback reported success")
			}
			state, inspectErr := Inspect(recorder.path)
			if inspectErr != nil || len(state.Calls) != 1 || state.Calls[0].Receipt != nil {
				t.Fatal("failure did not remain intent-only", state, inspectErr)
			}
		})
	}
}

func TestUnknownAndOversizedCallsRejectBeforeIntent(t *testing.T) {
	var contextCalls, agentCalls atomic.Int32
	recorder := openFixture(t, t.TempDir()+"/receipts.db", fixtureOwners(&contextCalls, &agentCalls, nil))
	projection := recorder.Projection()
	if _, err := projection.Call(context.Background(), toolbridge.Call{RequestID: json.RawMessage(`1`), Tool: "unknown", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("unknown tool admitted")
	}
	large := json.RawMessage(`{"body":"` + strings.Repeat("x", maxArgumentsBytes) + `"}`)
	if _, err := projection.Call(context.Background(), toolbridge.Call{RequestID: json.RawMessage(`2`), Tool: "source_list", Arguments: large}); err == nil {
		t.Fatal("oversized arguments admitted")
	}
	state, err := Inspect(recorder.path)
	if err != nil || len(state.Calls) != 0 {
		t.Fatal("rejected call appended intent", state, err)
	}
}

func TestCatalogAndBindingValidationPrecedesAppend(t *testing.T) {
	var contextCalls, agentCalls atomic.Int32
	owners := fixtureOwners(&contextCalls, &agentCalls, nil)
	path := t.TempDir() + "/receipts.db"
	hash, err := CatalogSHA256(owners)
	if err != nil {
		t.Fatal(err)
	}
	server, err := toolbridge.New(toolbridge.Config{Token: strings.Repeat("t", 32), Catalog: func() ([]toolbridge.ToolDefinition, error) {
		catalog, _, _, snapshotErr := snapshotOwners(owners)
		return catalog, snapshotErr
	}, Call: func(context.Context, toolbridge.Call) (toolbridge.Result, error) { return toolbridge.Result{}, nil }})
	if err != nil || server.CatalogHash() != hash {
		t.Fatal("catalog identity drifted from toolbridge", hash, server.CatalogHash(), err)
	}
	invalid := []Config{
		{Path: path, InvocationID: strings.Repeat("a", 64), CallerBindingSHA256: strings.Repeat("b", 64), CatalogSHA256: strings.Repeat("c", 64), Owners: owners},
		{Path: path, InvocationID: strings.Repeat("a", 64), CallerBindingSHA256: strings.Repeat("b", 64), CatalogSHA256: hash, Owners: owners[:1]},
		{Path: path, InvocationID: strings.Repeat("a", 64), CallerBindingSHA256: strings.Repeat("b", 64), CatalogSHA256: hash, Owners: []OwnedProjection{{Owner: OwnerContext, Projection: owners[0].Projection}, {Owner: OwnerContext, Projection: owners[1].Projection}}},
	}
	for i, config := range invalid {
		if _, err := Open(config); err == nil {
			t.Fatalf("invalid config %d admitted", i)
		}
		state, inspectErr := Inspect(path)
		if inspectErr != nil || state.Binding != nil {
			t.Fatalf("invalid config %d appended binding: %#v %v", i, state, inspectErr)
		}
	}

	recorder := openFixtureWithHash(t, path, owners, hash)
	changed := Config{Path: path, InvocationID: strings.Repeat("d", 64), CallerBindingSHA256: strings.Repeat("b", 64), CatalogSHA256: hash, Owners: owners}
	if _, err := Open(changed); err == nil {
		t.Fatal("substituted invocation binding admitted")
	}
	if recorder.Binding().InvocationID != strings.Repeat("a", 64) {
		t.Fatal("original binding changed")
	}
}

func TestConcurrentDuplicateClaimExecutesOneCallback(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	callback := func(ctx context.Context, _ toolbridge.Call) (toolbridge.Result, error) {
		if calls.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return toolbridge.Result{}, ctx.Err()
		}
		return toolbridge.Result{JSON: json.RawMessage(`{"files":[]}`)}, nil
	}
	recorder := openFixture(t, t.TempDir()+"/receipts.db", customOwners(callback))
	call := toolbridge.Call{RequestID: json.RawMessage(`"race"`), Tool: "source_list", Arguments: json.RawMessage(`{}`)}
	errorsSeen := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := recorder.Projection().Call(context.Background(), call)
			errorsSeen <- err
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	close(errorsSeen)
	var succeeded, duplicated int
	for err := range errorsSeen {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrDuplicateRequest) {
			duplicated++
		} else {
			t.Fatal("unexpected concurrent error", err)
		}
	}
	if succeeded != 1 || duplicated != 1 || calls.Load() != 1 {
		t.Fatal("duplicate claim dispatched more than once", succeeded, duplicated, calls.Load())
	}
	state, err := Inspect(recorder.path)
	if err != nil || len(state.Calls) != 1 || state.Calls[0].Receipt == nil {
		t.Fatal("concurrent journal is not bijective", state, err)
	}
}

func TestInspectRejectsReceiptFieldSubstitution(t *testing.T) {
	var contextCalls, agentCalls atomic.Int32
	recorder := openFixture(t, t.TempDir()+"/receipts.db", fixtureOwners(&contextCalls, &agentCalls, nil))
	call := toolbridge.Call{RequestID: json.RawMessage(`1`), Tool: "source_list", Arguments: json.RawMessage(`{}`)}
	if _, err := recorder.Projection().Call(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	state, err := Inspect(recorder.path)
	if err != nil {
		t.Fatal(err)
	}
	substituted := *state.Calls[0].Receipt
	substituted.Owner = OwnerAgent
	if _, err := journal.Append(recorder.path, receiptEvent, substituted, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal("could not construct tampered journal", err)
	}
	if _, err := Inspect(recorder.path); err == nil {
		t.Fatal("substituted result owner admitted")
	}
}

func TestInspectWithHeadReturnsSameValidatedPrefix(t *testing.T) {
	var contextCalls, agentCalls atomic.Int32
	path := t.TempDir() + "/receipts.db"
	if _, head, err := InspectWithHead(path); err == nil || head != "" {
		t.Fatal("empty journal returned a bindable head", head, err)
	}
	recorder := openFixture(t, path, fixtureOwners(&contextCalls, &agentCalls, nil))
	call := toolbridge.Call{RequestID: json.RawMessage(`1`), Tool: "source_list", Arguments: json.RawMessage(`{}`)}
	if _, err := recorder.Projection().Call(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	state, head, err := InspectWithHeadContext(context.Background(), path)
	if err != nil || state.Binding == nil || len(state.Calls) != 1 || state.Calls[0].Receipt == nil || len(head) != 64 {
		t.Fatal("atomic state and head inspection failed", state, head, err)
	}
	events, err := journal.Read(path)
	if err != nil || head != events[len(events)-1].Hash {
		t.Fatal("inspection head differs from exact journal prefix", head, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := InspectWithHeadContext(cancelled, path); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled atomic inspection admitted", err)
	}
}

func assertTranscriptSubstitutionsRejected(t *testing.T, state State, binding Binding, observations []Observation) {
	t.Helper()
	cases := []struct {
		name string
		edit func(*Binding, []Observation)
	}{
		{"binding", func(b *Binding, _ []Observation) { b.CallerBindingSHA256 = strings.Repeat("f", 64) }},
		{"arguments", func(_ *Binding, o []Observation) { o[0].Call.Arguments = json.RawMessage(`{"limit":3}`) }},
		{"result", func(_ *Binding, o []Observation) {
			o[0].Result = toolbridge.Result{JSON: json.RawMessage(`{"files":[]}`)}
		}},
		{"request", func(_ *Binding, o []Observation) { o[0].Call.RequestID = json.RawMessage(`"other"`) }},
	}
	for _, test := range cases {
		t.Run("substituted "+test.name, func(t *testing.T) {
			changedBinding := cloneBinding(binding)
			changed := append([]Observation(nil), observations...)
			test.edit(&changedBinding, changed)
			if state.ValidateTranscript(changedBinding, changed) == nil {
				t.Fatal("substitution admitted")
			}
		})
	}
}

func openFixture(t *testing.T, path string, owners []OwnedProjection) *Recorder {
	t.Helper()
	hash, err := CatalogSHA256(owners)
	if err != nil {
		t.Fatal(err)
	}
	return openFixtureWithHash(t, path, owners, hash)
}

func openFixtureWithHash(t *testing.T, path string, owners []OwnedProjection, hash string) *Recorder {
	t.Helper()
	recorder, err := Open(Config{Path: path, InvocationID: strings.Repeat("a", 64), CallerBindingSHA256: strings.Repeat("b", 64), CatalogSHA256: hash, Owners: owners})
	if err != nil {
		t.Fatal(err)
	}
	return recorder
}

func fixtureOwners(contextCalls, agentCalls *atomic.Int32, contextCallback toolbridge.CallFunc) []OwnedProjection {
	if contextCallback == nil {
		contextCallback = func(_ context.Context, call toolbridge.Call) (toolbridge.Result, error) {
			contextCalls.Add(1)
			if call.Tool != "source_list" {
				return toolbridge.Result{}, errors.New("wrong context route")
			}
			return toolbridge.Result{JSON: json.RawMessage(`{"files":["a.go"]}`)}, nil
		}
	}
	return []OwnedProjection{
		{Owner: OwnerContext, Projection: projection("source_list", contextCallback)},
		{Owner: OwnerAgent, Projection: projection("agent_wait", func(_ context.Context, call toolbridge.Call) (toolbridge.Result, error) {
			agentCalls.Add(1)
			if call.Tool != "agent_wait" {
				return toolbridge.Result{}, errors.New("wrong agent route")
			}
			return toolbridge.Result{JSON: json.RawMessage(`{"status":"idle"}`)}, nil
		})},
	}
}

func customOwners(contextCallback toolbridge.CallFunc) []OwnedProjection {
	var contextCalls, agentCalls atomic.Int32
	return fixtureOwners(&contextCalls, &agentCalls, contextCallback)
}

func projection(name string, call toolbridge.CallFunc) toolbridge.Projection {
	catalog := []toolbridge.ToolDefinition{{Name: name, InputSchema: json.RawMessage(`{"type":"object"}`)}}
	return toolbridge.Projection{
		Catalog: func() ([]toolbridge.ToolDefinition, error) {
			result := make([]toolbridge.ToolDefinition, len(catalog))
			copy(result, catalog)
			result[0].InputSchema = append(json.RawMessage(nil), catalog[0].InputSchema...)
			return result, nil
		},
		Call: call,
	}
}

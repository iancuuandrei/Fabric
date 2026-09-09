package contextmcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/toolbridge"
)

func TestVerifyBackendReceiptAfterLaterCallsAndClosure(t *testing.T) {
	path, binding, broker := receiptVerifierFixture(t)
	projection, err := Projection(broker)
	if err != nil {
		t.Fatal(err)
	}
	firstCall := toolbridge.Call{
		RequestID: json.RawMessage(`"context-1"`),
		Tool:      "source_list",
		Arguments: json.RawMessage(`{"limit":1,"after":""}`),
	}
	firstResult, err := projection.Call(context.Background(), firstCall)
	if err != nil || firstResult.IsError {
		t.Fatal("first context call failed", err)
	}
	secondCall := toolbridge.Call{
		RequestID: json.RawMessage(`2`),
		Tool:      "source_read",
		Arguments: json.RawMessage(`{"path":"absent.txt","offset":0,"limit":1}`),
	}
	secondResult, err := projection.Call(context.Background(), secondCall)
	if err != nil || !secondResult.IsError {
		t.Fatal("expected durable domain-error response", err)
	}
	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackendReceipt(path, binding, firstCall, firstResult); err != nil {
		t.Fatal("earlier receipt rejected after later append and close", err)
	}
	normalizedFirst := firstCall
	normalizedFirst.Arguments = json.RawMessage(" { \"after\" : \"\", \"limit\" : 1 } ")
	if err := VerifyBackendReceipt(path, binding, normalizedFirst, firstResult); err != nil {
		t.Fatal("equivalent normalized arguments rejected", err)
	}
	if err := VerifyBackendReceipt(path, binding, secondCall, secondResult); err != nil {
		t.Fatal("error receipt rejected", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("verification mutated context journal", err)
	}
}

func TestVerifyBackendReceiptRejectsSubstitutionAndPending(t *testing.T) {
	path, binding, broker := receiptVerifierFixture(t)
	projection, err := Projection(broker)
	if err != nil {
		t.Fatal(err)
	}
	call := toolbridge.Call{RequestID: json.RawMessage(`"exact"`), Tool: "source_list", Arguments: json.RawMessage(`{"after":"","limit":1}`)}
	result, err := projection.Call(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}

	foreignBinding := binding
	foreignBinding.InvocationID = strings.Repeat("b", 64)
	foreignPath := filepath.Join(t.TempDir(), "context.jsonl")
	foreignBroker, err := contextbroker.Open(foreignPath, binding)
	if err != nil {
		t.Fatal(err)
	}
	defer foreignBroker.Close()
	errorResult := result
	errorResult.IsError = !errorResult.IsError
	toolErrorResult := result
	toolErrorResult.Error = &toolbridge.ToolError{Code: "substituted", Message: "substituted"}
	jsonResult := result
	jsonResult.JSON = json.RawMessage(`{"substituted":true}`)

	cases := []struct {
		name    string
		path    string
		binding contextbroker.Binding
		call    toolbridge.Call
		result  toolbridge.Result
	}{
		{"path", foreignPath, binding, call, result},
		{"binding", path, foreignBinding, call, result},
		{"request id", path, binding, toolbridge.Call{RequestID: json.RawMessage(`"other"`), Tool: call.Tool, Arguments: call.Arguments}, result},
		{"request id type", path, binding, toolbridge.Call{RequestID: json.RawMessage(`1`), Tool: call.Tool, Arguments: call.Arguments}, result},
		{"noncanonical request id", path, binding, toolbridge.Call{RequestID: json.RawMessage(` "exact"`), Tool: call.Tool, Arguments: call.Arguments}, result},
		{"tool", path, binding, toolbridge.Call{RequestID: call.RequestID, Tool: "source_read", Arguments: call.Arguments}, result},
		{"arguments", path, binding, toolbridge.Call{RequestID: call.RequestID, Tool: call.Tool, Arguments: json.RawMessage(`{"after":"x","limit":1}`)}, result},
		{"error flag", path, binding, call, errorResult},
		{"tool error envelope", path, binding, call, toolErrorResult},
		{"result JSON", path, binding, call, jsonResult},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := VerifyBackendReceipt(test.path, test.binding, test.call, test.result); err == nil {
				t.Fatal("substitution accepted")
			}
		})
	}

	pendingPath := filepath.Join(t.TempDir(), "pending.jsonl")
	_, err = contextbroker.Open(pendingPath, binding)
	if err != nil {
		t.Fatal(err)
	}
	pendingCall := toolbridge.Call{RequestID: json.RawMessage(`"pending"`), Tool: call.Tool, Arguments: call.Arguments}
	callID, err := contextCallID(pendingCall.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	bindingID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	pendingRequest := contextbroker.Request{Version: 1, BindingID: bindingID, InvocationID: binding.InvocationID, CallID: callID, Tool: pendingCall.Tool, Arguments: pendingCall.Arguments}
	pendingRequest.RequestID, err = pendingRequest.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Append(pendingPath, "context.request", pendingRequest, func([]journal.Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := contextbroker.Inspect(pendingPath); err != nil {
		t.Fatal(err)
	}
	if err := VerifyBackendReceipt(pendingPath, binding, pendingCall, toolbridge.Result{}); err == nil {
		t.Fatal("pending context request accepted as a backend receipt")
	}
}

func receiptVerifierFixture(t *testing.T) (string, contextbroker.Binding, *contextbroker.Broker) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	git := func(arguments ...string) {
		t.Helper()
		command := exec.CommandContext(ctx, "git", append([]string{"-C", root}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", arguments, err, output)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("receipt fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	source, err := repository.Discover(ctx, root, "receipt-verifier")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextbroker.NewBinding(strings.Repeat("a", 64), source, nil, contextbroker.Limits{
		MaxCalls: 8, MaxRequestBytes: 4096, MaxResponseBytes: MaxContentBytes, MaxTotalResponseBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "context.jsonl")
	broker, err := contextbroker.Open(path, binding)
	if err != nil {
		t.Fatal(err)
	}
	return path, binding, broker
}

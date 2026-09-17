package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"harness.local/engorch/internal/canonical"
	"harness.local/engorch/internal/contextbroker"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/runtime"
)

func TestSynchronousToolDispatchBindsInvocationBrokerAndTranscript(t *testing.T) {
	intent, brokerPath, broker := synchronousToolFixture(t)
	path := filepath.Join(t.TempDir(), "sync-tool.jsonl")
	var posts, gets atomic.Int32
	var mu sync.Mutex
	response, transcript := "", ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			posts.Add(1)
			events, err := journal.Read(path)
			if err != nil || len(events) != 1 || events[0].Kind != "opencode.sync-tool-intent" {
				t.Error("tool POST was not preceded by durable intent", err)
			}
			receipt, err := broker.Call(r.Context(), "broker-list-1", "source_list", json.RawMessage(`{"after":"","limit":1}`))
			if err != nil {
				t.Error(err)
				http.Error(w, "broker call failed", http.StatusInternalServerError)
				return
			}
			nextResponse, nextTranscript := synchronousToolWire(t, intent.Dispatch, receipt)
			mu.Lock()
			response, transcript = nextResponse, nextTranscript
			body := response
			mu.Unlock()
			writeSynchronousJSON(w, body)
		case http.MethodGet:
			gets.Add(1)
			mu.Lock()
			body := transcript
			mu.Unlock()
			writeSynchronousJSON(w, body)
		default:
			t.Error("unexpected method", r.Method)
		}
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	observation, err := client.SubmitSynchronousToolTurn(ctx, path, brokerPath, intent)
	if err != nil || observation.Text != "tool result accepted" || len(observation.Calls) != 1 || posts.Load() != 1 || gets.Load() != 1 {
		t.Fatal("synchronous tool dispatch failed", err, posts.Load(), gets.Load())
	}
	if observation.BrokerBindingID != intent.BrokerBindingID || observation.Calls[0].InvocationID != intent.Invocation.ID || observation.Calls[0].BrokerCallID != "broker-list-1" {
		t.Fatal("tool observation lost broker or invocation binding")
	}
	raw, err := journal.ExportJSONL(path)
	if err != nil || !strings.Contains(string(raw), "0.0123") {
		t.Fatal("raw tool transcript decimals were not retained", err)
	}
	var offline *Client
	replayed, err := offline.RecoverSynchronousToolTurn(context.Background(), path, brokerPath, intent)
	if err != nil || replayed.BrokerStateID != observation.BrokerStateID || posts.Load() != 1 || gets.Load() != 1 {
		t.Fatal("offline tool observation replay failed", err)
	}
	if _, err := client.SubmitSynchronousToolTurn(ctx, path, brokerPath, intent); err == nil || posts.Load() != 1 {
		t.Fatal("completed synchronous tool turn was resubmitted")
	}
	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := offline.RecoverSynchronousToolTurn(context.Background(), path, brokerPath, intent); err == nil {
		t.Fatal("cached tool observation survived changed broker journal state")
	}
}

func TestSynchronousToolDispatchUncertainPostRecoversGetOnly(t *testing.T) {
	intent, brokerPath, broker := synchronousToolFixture(t)
	path := filepath.Join(t.TempDir(), "sync-tool.jsonl")
	var posts, gets atomic.Int32
	var mu sync.Mutex
	var transcript string
	release := make(chan struct{})
	postDone := make(chan struct{})
	postArrived := make(chan struct{})
	var arriveOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			defer close(postDone)
			posts.Add(1)
			receipt, err := broker.Call(context.Background(), "broker-list-1", "source_list", json.RawMessage(`{"after":"","limit":1}`))
			if err != nil {
				t.Error(err)
				return
			}
			response, full := synchronousToolWire(t, intent.Dispatch, receipt)
			mu.Lock()
			transcript = full
			mu.Unlock()
			arriveOnce.Do(func() { close(postArrived) })
			<-release
			writeSynchronousJSON(w, response)
			return
		}
		gets.Add(1)
		mu.Lock()
		body := transcript
		mu.Unlock()
		writeSynchronousJSON(w, body)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	uncertain, stopUncertain := context.WithTimeout(context.Background(), 10*time.Second)
	submitErr := make(chan error, 1)
	go func() {
		_, err := client.SubmitSynchronousToolTurn(uncertain, path, brokerPath, intent)
		submitErr <- err
	}()
	var submitFailed error
	arrived := false
	select {
	case <-postArrived:
		arrived = true
		stopUncertain()
		submitFailed = <-submitErr
	case submitFailed = <-submitErr:
		stopUncertain()
	}
	close(release)
	if arrived {
		<-postDone
	}
	if submitFailed == nil || posts.Load() != 1 {
		t.Fatal("uncertain tool POST was admitted", submitFailed)
	}
	retry, stopRetry := context.WithTimeout(context.Background(), time.Second)
	if _, err := client.SubmitSynchronousToolTurn(retry, path, brokerPath, intent); err == nil || posts.Load() != 1 {
		t.Fatal("unresolved tool dispatch was reposted")
	}
	stopRetry()
	recoverContext, stopRecover := context.WithTimeout(context.Background(), time.Second)
	observation, err := client.RecoverSynchronousToolTurn(recoverContext, path, brokerPath, intent)
	stopRecover()
	if err != nil || observation.Text != "tool result accepted" || posts.Load() != 1 || gets.Load() != 1 {
		t.Fatal("GET-only tool recovery failed", err, posts.Load(), gets.Load())
	}
}

func TestSynchronousToolDispatchRejectsBindingDriftAndUnboundedRequest(t *testing.T) {
	intent, brokerPath, broker := synchronousToolFixture(t)
	path := filepath.Join(t.TempDir(), "sync-tool.jsonl")
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		http.Error(w, "must not dispatch", http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.SubmitSynchronousToolTurn(context.Background(), path, brokerPath, intent); err == nil || posts != 0 {
		t.Fatal("unbounded tool request posted")
	}
	changed := intent
	changed.BrokerCatalogID = strings.Repeat("f", 64)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := client.SubmitSynchronousToolTurn(ctx, path, brokerPath, changed); err == nil || posts != 0 {
		t.Fatal("changed broker catalog posted")
	}
	changed = intent
	changed.Invocation.Input = "changed"
	if _, err := client.SubmitSynchronousToolTurn(ctx, path, brokerPath, changed); err == nil || posts != 0 {
		t.Fatal("mutated runtime invocation posted")
	}
	if err := broker.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.SubmitSynchronousToolTurn(ctx, path, brokerPath, intent); err == nil || posts != 0 {
		t.Fatal("closed broker admitted new tool dispatch")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("rejected tool request created journal", err)
	}
}

func synchronousToolFixture(t *testing.T) (SynchronousToolDispatchIntent, string, *contextbroker.Broker) {
	t.Helper()
	sourceRoot := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", sourceRoot}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte("committed"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.txt")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	source, err := repository.Discover(context.Background(), sourceRoot, "sync-tool-fixture")
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Use source_list once, then report the fixture result."
	invocation, err := runtime.NewInvocation(runtime.Profile{Runtime: "opencode-http", Provider: "fixture", Model: "model", Effort: "low", Role: "explorer"}, prompt)
	if err != nil {
		t.Fatal(err)
	}
	brokerBinding, err := contextbroker.NewBinding(invocation.ID, source, nil, contextbroker.Limits{MaxCalls: 1, MaxRequestBytes: 4096, MaxResponseBytes: 64 << 10, MaxTotalResponseBytes: 64 << 10})
	if err != nil {
		t.Fatal(err)
	}
	brokerPath := filepath.Join(t.TempDir(), "broker.jsonl")
	broker, err := contextbroker.Open(brokerPath, brokerBinding)
	if err != nil {
		t.Fatal(err)
	}
	bindingID, err := brokerBinding.ID()
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := DispatchForInvocation(invocation, "ses_fixture", "msg_user", "build", "/candidate", "/candidate")
	if err != nil {
		t.Fatal(err)
	}
	return SynchronousToolDispatchIntent{Invocation: invocation, Dispatch: dispatch, BrokerBindingID: bindingID, BrokerCatalogID: brokerBinding.CatalogID}, brokerPath, broker
}

func synchronousToolWire(t *testing.T, dispatch DispatchIntent, receipt contextbroker.Response) (string, string) {
	t.Helper()
	receiptBytes, err := canonical.Bytes(receipt)
	if err != nil {
		t.Fatal(err)
	}
	user := `{"info":{"id":"msg_user","sessionID":"ses_fixture","role":"user","time":{"created":1},"agent":"build","model":{"providerID":"fixture","modelID":"model","variant":"low"}},"parts":[{"id":"prt_user","messageID":"msg_user","sessionID":"ses_fixture","type":"text","text":` + strconv.Quote(dispatch.Text) + `}]}`
	intermediate := `{"info":{"id":"msg_tool","sessionID":"ses_fixture","parentID":"msg_user","providerID":"fixture","modelID":"model","agent":"build","role":"assistant","finish":"tool-calls","variant":"low","cost":0.0123,"time":{"created":10,"completed":20},"path":{"cwd":"/candidate","root":"/candidate"},"tokens":{"total":13,"input":9,"output":4,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":[{"id":"prt_step_1","messageID":"msg_tool","sessionID":"ses_fixture","type":"step-start"},{"id":"prt_tool","messageID":"msg_tool","sessionID":"ses_fixture","type":"tool","callID":"provider-call-1","tool":"engorch_source_list","state":{"status":"completed","input":{"after":"","limit":1},"output":` + strconv.Quote(string(receiptBytes)) + `,"title":"","metadata":{"truncated":false},"time":{"start":12,"end":18},"attachments":[]}},{"id":"prt_step_2","messageID":"msg_tool","sessionID":"ses_fixture","type":"step-finish","reason":"tool-calls","tokens":{"total":13,"input":9,"output":4,"reasoning":0,"cache":{"read":0,"write":0}}}]}`
	final := `{"info":{"id":"msg_final","sessionID":"ses_fixture","parentID":"msg_user","providerID":"fixture","modelID":"model","agent":"build","role":"assistant","finish":"stop","variant":"low","cost":0.0345,"time":{"created":21,"completed":30},"path":{"cwd":"/candidate","root":"/candidate"},"tokens":{"total":21,"input":18,"output":3,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":[{"id":"prt_step_3","messageID":"msg_final","sessionID":"ses_fixture","type":"step-start"},{"id":"prt_text","messageID":"msg_final","sessionID":"ses_fixture","type":"text","text":"tool result accepted","time":{"start":22,"end":29}},{"id":"prt_step_4","messageID":"msg_final","sessionID":"ses_fixture","type":"step-finish","reason":"stop","tokens":{"total":21,"input":18,"output":3,"reasoning":0,"cache":{"read":0,"write":0}}}]}`
	return final, "[" + user + "," + intermediate + "," + final + "]"
}

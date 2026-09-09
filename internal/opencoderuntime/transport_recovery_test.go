package opencoderuntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/opencode"
)

func TestSynchronousTransportFailurePersistsUnknownThenUsesOneGET(t *testing.T) {
	fixture := newRuntimeFixture(t)
	appendUnchecked(t, fixture.paths.Dispatch, "opencode.sync-tool-intent", fixture.bound.SealExpected.Dispatch)

	var gets, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets.Add(1)
		case http.MethodPost:
			posts.Add(1)
		}
		if r.Method != http.MethodGet || r.URL.Path != "/session/ses_fixture/message" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(terminalReadbackTranscript(fixture.bound.SealExpected.Dispatch.Dispatch.Binding, fixture.intent.Invocation.Input)))
	}))
	defer server.Close()
	client, err := opencode.NewClient(server.URL, "runtime-user", "runtime-password")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	port := testServerPort(t, server.URL)
	failure := opencode.TransportFailureEvidence{
		Version: opencode.TransportFailureEvidenceVersion, TransportError: "fixture transport timeout",
		Phase: opencode.TransportFailurePhaseMessagePostWait, ElapsedMillis: 30_000,
		Endpoint: server.URL, HTTPMethod: http.MethodPost, SessionID: "ses_fixture", RequestMessageID: "msg_user",
		DispatchState: opencode.TransportDispatchStateUnknown,
	}
	original := errors.New("original synchronous transport failure")
	cfg := ExecuteConfig{RuntimePath: fixture.path, Paths: fixture.paths, Intent: fixture.intent, Port: port, ReadbackTimeout: time.Second}
	if err := recoverSynchronousTransportFailureEvidence(context.Background(), cfg, client, fixture.bound.SealExpected.Dispatch, failure, original); err != nil {
		t.Fatal("one GET did not recover the original terminal turn", err)
	}
	if gets.Load() != 1 || posts.Load() != 0 {
		t.Fatalf("recovery request counts differ: GET=%d POST=%d", gets.Load(), posts.Load())
	}
	state, err := inspectRuntimeJournal(fixture.path)
	if err != nil || state.TransportFailure == nil || state.TransportFailure.Outcome != opencode.TransportDispatchStateUnknown || state.TransportFailure.Failure.TransportError != failure.TransportError {
		t.Fatal("original unknown transport evidence was not retained", state.TransportFailure, err)
	}
	events, err := journal.Read(fixture.paths.Dispatch)
	if err != nil || len(events) != 2 || events[0].Kind != "opencode.sync-tool-intent" || events[1].Kind != "opencode.sync-tool-observed" {
		t.Fatal("GET recovery did not append one terminal observation", events, err)
	}
}

func TestExpiredInvocationPersistsUnknownWithoutGET(t *testing.T) {
	fixture := newRuntimeFixture(t)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	client, err := opencode.NewClient(server.URL, "runtime-user", "runtime-password")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failure := opencode.TransportFailureEvidence{
		Version: opencode.TransportFailureEvidenceVersion, TransportError: context.Canceled.Error(), ContextError: context.Canceled.Error(),
		Phase: opencode.TransportFailurePhaseMessagePostWait, ElapsedMillis: 1,
		Endpoint: server.URL, HTTPMethod: http.MethodPost, SessionID: "ses_fixture", RequestMessageID: "msg_user",
		DispatchState: opencode.TransportDispatchStateUnknown,
	}
	original := errors.New("canceled synchronous transport failure")
	cfg := ExecuteConfig{RuntimePath: fixture.path, Paths: fixture.paths, Intent: fixture.intent, Port: testServerPort(t, server.URL), ReadbackTimeout: time.Second}
	if err := recoverSynchronousTransportFailureEvidence(ctx, cfg, client, fixture.bound.SealExpected.Dispatch, failure, original); !errors.Is(err, original) {
		t.Fatal("expired invocation did not return the original unresolved failure", err)
	}
	if requests.Load() != 0 {
		t.Fatal("expired invocation attempted detached readback", requests.Load())
	}
	state, err := inspectRuntimeJournal(fixture.path)
	if err != nil || state.TransportFailure == nil || state.TransportFailure.Outcome != opencode.TransportDispatchStateUnknown {
		t.Fatal("expired invocation lost durable UNKNOWN evidence", state.TransportFailure, err)
	}
}

func TestReadbackTransportStateDoesNotChangeRuntimeUnknownOutcome(t *testing.T) {
	fixture := newRuntimeFixture(t)
	failure := opencode.TransportFailureEvidence{
		Version: opencode.TransportFailureEvidenceVersion, TransportError: "readback context expired", ContextError: context.DeadlineExceeded.Error(),
		Phase: opencode.TransportFailurePhaseReadbackWait, ElapsedMillis: 30_000,
		Endpoint: "http://127.0.0.1:43124", HTTPMethod: http.MethodGet, SessionID: "ses_fixture",
		DispatchState: opencode.TransportDispatchStateNotDispatched,
	}
	record, err := RecordTransportFailure(fixture.path, fixture.intent, failure)
	if err != nil || record.Outcome != opencode.TransportDispatchStateUnknown || record.Failure.DispatchState != opencode.TransportDispatchStateNotDispatched {
		t.Fatal("HTTP readback state was confused with whole-invocation outcome", record, err)
	}
	writeTerminalToolTurn(t, &fixture)
	if _, err := Complete(fixture.path, fixture.intent); err != nil {
		t.Fatal("later exact terminal evidence could not complete the original invocation", err)
	}
	state, err := Inspect(fixture.path, fixture.intent)
	if err != nil || state.Result == nil || state.TransportFailure == nil || state.TransportFailure.Failure.TransportError != failure.TransportError {
		t.Fatal("sealed completion erased original transport evidence", state, err)
	}
}

func terminalReadbackTranscript(binding opencode.Binding, prompt string) string {
	quotedRoot := strconv.Quote(binding.Root)
	quotedDirectory := strconv.Quote(binding.Directory)
	user := `{"info":{"id":"msg_user","sessionID":"ses_fixture","role":"user","time":{"created":1},"agent":"build","model":{"providerID":"fixture","modelID":"model","variant":"low"}},"parts":[{"id":"prt_user","messageID":"msg_user","sessionID":"ses_fixture","type":"text","text":` + strconv.Quote(prompt) + `}]}`
	final := `{"info":{"id":"msg_final","sessionID":"ses_fixture","parentID":"msg_user","providerID":"fixture","modelID":"model","agent":"build","role":"assistant","finish":"stop","variant":"low","cost":0,"time":{"created":2,"completed":3},"path":{"cwd":` + quotedDirectory + `,"root":` + quotedRoot + `},"tokens":{"total":2,"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}},"parts":[{"id":"prt_step_1","messageID":"msg_final","sessionID":"ses_fixture","type":"step-start"},{"id":"prt_text","messageID":"msg_final","sessionID":"ses_fixture","type":"text","text":"terminal result","time":{"start":2,"end":3}},{"id":"prt_step_2","messageID":"msg_final","sessionID":"ses_fixture","type":"step-finish","reason":"stop","tokens":{"total":2,"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}]}`
	return "[" + user + "," + final + "]"
}

func testServerPort(t *testing.T, endpoint string) int {
	t.Helper()
	parsed, err := url.Parse(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

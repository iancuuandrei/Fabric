package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMessagePostUsesCallerDeadlineInsteadOfReadbackAndHeaderBounds(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/session/ses_fixture/message" {
			t.Error("message request mismatch")
		}
		time.Sleep(250 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client, err := NewClientWithPolicy(server.URL, "fixture", "secret", TransportPolicy{
		ConnectTimeout: 100 * time.Millisecond, ReadbackTimeout: 100 * time.Millisecond, ResponseHeaderTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	raw, err := client.request(ctx, http.MethodPost, "/session/ses_fixture/message", []byte(`{"messageID":"msg_request"}`))
	if err != nil || string(raw) != `{"ok":true}` {
		t.Fatal("long synchronous response failed", err)
	}
	if elapsed := time.Since(started); elapsed < 200*time.Millisecond || elapsed >= time.Second {
		t.Fatal("message POST used the wrong deadline", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatal("message POST was duplicated", calls.Load())
	}
}

func TestMessagePostRequiresFiniteCallerDeadline(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.request(context.Background(), http.MethodPost, "/session/ses_fixture/message", []byte(`{"messageID":"msg_request"}`))
	if err == nil || err.Error() != "synchronous OpenCode message POST requires deadline" || calls.Load() != 0 {
		t.Fatal("unbounded message POST admitted", err, calls.Load())
	}
}

func TestOrdinaryRequestUsesReadbackDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(250 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := NewClientWithPolicy(server.URL, "fixture", "secret", TransportPolicy{
		ConnectTimeout: 100 * time.Millisecond, ReadbackTimeout: 60 * time.Millisecond, ResponseHeaderTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	started := time.Now()
	_, err = client.read(context.Background(), "/global/health")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 500*time.Millisecond {
		t.Fatal("ordinary request was not bounded", err, time.Since(started))
	}
	evidence, ok := TransportFailureFromError(err)
	if !ok || evidence.Phase != TransportFailurePhaseReadbackWait || evidence.ContextError != context.DeadlineExceeded.Error() || evidence.DispatchState != TransportDispatchStateUnknown {
		t.Fatal("readback timeout evidence mismatch", evidence, ok)
	}
}

func TestTransportFailureUnwrapsOriginalTimeoutWhenCallerContextIsLive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	client.http.Timeout = 50 * time.Millisecond
	ctx := context.Background()
	_, err = client.read(ctx, "/global/health")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		t.Fatal("original timeout cause was lost", err, ctx.Err())
	}
	evidence, ok := TransportFailureFromError(err)
	if !ok || evidence.ContextError != "" || evidence.TransportError == "" {
		t.Fatal("timeout evidence conflated caller context and transport", evidence, ok)
	}
}

type failingRoundTripper struct{ err error }

func (f failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

func TestTransportFailureEvidenceBindsMessageAndRedactsCredentials(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:43123", "fixture-user", "secret-password")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	original := errors.New("fixture-user secret-password transport stopped")
	client.messageHTTP.Transport = failingRoundTripper{err: original}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = client.request(ctx, http.MethodPost, "/session/ses_exact/message", []byte(`{"messageID":"msg_exact"}`))
	if err == nil || err.Error() != "OpenCode readback unavailable" || !errors.Is(err, original) {
		t.Fatal("stable error or original cause mismatch", err)
	}
	evidence, ok := TransportFailureFromError(err)
	if !ok || evidence.Version != 1 || evidence.Phase != TransportFailurePhaseMessagePostWait || evidence.Endpoint != "http://127.0.0.1:43123" || evidence.HTTPMethod != http.MethodPost || evidence.SessionID != "ses_exact" || evidence.RequestMessageID != "msg_exact" || evidence.DispatchState != TransportDispatchStateUnknown {
		t.Fatal("message transport evidence mismatch", evidence, ok)
	}
	raw, marshalErr := json.Marshal(evidence)
	if marshalErr != nil || strings.Contains(string(raw), "fixture-user") || strings.Contains(string(raw), "secret-password") || strings.Contains(err.Error(), "fixture-user") || strings.Contains(err.Error(), "secret-password") {
		t.Fatal("credential leaked through evidence or public error", string(raw), err)
	}
}

func TestCanceledMessageBeforeRequestIsNotDispatched(t *testing.T) {
	client, err := NewClient("http://127.0.0.1:43123", "fixture", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	_, err = client.request(ctx, http.MethodPost, "/session/ses_exact/message", []byte(`{"messageID":"msg_exact"}`))
	evidence, ok := TransportFailureFromError(err)
	if !ok || evidence.ContextError != context.Canceled.Error() || evidence.DispatchState != TransportDispatchStateNotDispatched || !errors.Is(err, context.Canceled) {
		t.Fatal("pre-request cancellation evidence mismatch", evidence, ok, err)
	}
}

func TestTransportFailureEvidenceValidationRejectsUnknownOrUnsafeValues(t *testing.T) {
	valid := TransportFailureEvidence{
		Version: 1, TransportError: "request failed", Phase: TransportFailurePhaseMessagePostWait, ElapsedMillis: 10,
		Endpoint: "http://127.0.0.1:43123", HTTPMethod: http.MethodPost, SessionID: "ses_exact", RequestMessageID: "msg_exact", DispatchState: TransportDispatchStateUnknown,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []TransportFailureEvidence{
		func() TransportFailureEvidence { v := valid; v.Version = 2; return v }(),
		func() TransportFailureEvidence { v := valid; v.TransportError = "unsafe\ntext"; return v }(),
		func() TransportFailureEvidence { v := valid; v.ContextError = "unknown"; return v }(),
		func() TransportFailureEvidence { v := valid; v.Phase = "UNKNOWN_PHASE"; return v }(),
		func() TransportFailureEvidence { v := valid; v.ElapsedMillis = -1; return v }(),
		func() TransportFailureEvidence { v := valid; v.Endpoint = "http://example.com:43123"; return v }(),
		func() TransportFailureEvidence { v := valid; v.HTTPMethod = http.MethodGet; return v }(),
		func() TransportFailureEvidence { v := valid; v.RequestMessageID = "../unsafe"; return v }(),
		func() TransportFailureEvidence { v := valid; v.DispatchState = "DISPATCHED"; return v }(),
	}
	for index, evidence := range cases {
		if evidence.Validate() == nil {
			t.Fatalf("unsafe evidence %d admitted", index)
		}
	}
}

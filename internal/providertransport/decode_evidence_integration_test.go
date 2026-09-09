package providertransport

import (
	"context"
	"encoding/json"
	"errors"
	"harness.local/engorch/internal/providergateway"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecodeFailureEvidenceEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name, contentType string
		body              []byte
		limit             int64
		json              bool
	}{
		{"malformed-json", "application/json", []byte(`{"broken":`), 4096, true},
		{"wrong-schema", "application/json", []byte(`{"choices":"wrong"}`), 4096, true},
		{"sse-to-json", "application/json", validChatSSE(), 4096, true},
		{"truncated-sse", "text/event-stream", []byte("data: {}\n\n"), 4096, false},
		{"wrong-content-type", "text/html", []byte("<html>provider diagnostic</html>"), 4096, false},
		{"body-limit", "text/event-stream", []byte(strings.Repeat("x", 4098)), 4096, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				w.Write(tc.body)
			})
			var f transportFixture
			if tc.json {
				body := []byte(`{"model":"wire-model","messages":[{"role":"user","content":"hello"}],"max_tokens":5,"stream":false}`)
				f = newTransportFixtureForProtocol(t, server, "api", "bearer", "", tc.limit, fixtureProtocol{adapterID: providergateway.OpenAIChatCompletionsAdapter, path: "/v1/chat/completions", capabilities: &providergateway.ModelCapabilities{Tools: true, OutputCap: true, CompleteUsage: true, SupportedResponseFramings: []providergateway.ResponseFraming{providergateway.ResponseFramingJSON}}, adapterCapabilities: json.RawMessage("{}"), body: body, expectation: providergateway.AdapterRequestExpectation{MaxBytes: int64(len(body)), MaxOutputTokens: 5, Controls: json.RawMessage("{}"), ResponseFraming: providergateway.ResponseFramingJSON}})
			} else {
				f = newTransportFixture(t, server, "api", "bearer", "", tc.limit)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := f.client.Execute(ctx, f.request)
			if !errors.Is(err, ErrPending) || len(result.Body) != 0 || result.Content != nil {
				t.Fatal("failure admitted response", result, err)
			}
			state, err := providergateway.Inspect(f.gatewayPath)
			if err != nil || state.Pending == nil || len(state.Calls) != 1 || state.Calls[0].Receipt != nil || state.Calls[0].Failure == nil || state.Calls[0].Failure.Evidence == nil {
				t.Fatal("missing pending failure evidence", state, err)
			}
			ref := state.Calls[0].Failure.Evidence
			private, err := os.ReadFile(filepath.Join(f.gatewayPath+".response-evidence", ref.Artifact))
			if err != nil || digestText(string(private)) != ref.SHA256 {
				t.Fatal("private evidence not pinned", err)
			}
			if strings.Contains(errString(f.client.Execute(ctx, f.request)), "provider diagnostic") || calls.Load() != 1 {
				t.Fatal("retry or diagnostic exposure")
			}
			artifacts, err := filepath.Glob(filepath.Join(f.gatewayPath+".response-evidence", "*"))
			if err != nil || len(artifacts) < 2 {
				t.Fatal("raw not preserved", artifacts, err)
			}
			t.Logf("failure evidence pinned: %s", ref.Artifact)
		})
	}
}
func errString(_ Result, err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestEvidencePersistenceFailureBlocksSuccessfulDecode(t *testing.T) {
	var calls atomic.Int32
	server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write(validChatSSE())
	})
	f := newTransportFixture(t, server, "api", "bearer", "", 4096)
	if err := os.WriteFile(f.gatewayPath+".response-evidence", []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := f.client.Execute(ctx, f.request)
	if !errors.Is(err, ErrPending) || len(result.Body) != 0 || result.Content != nil {
		t.Fatal("success despite missing evidence", err)
	}
	state, err := providergateway.Inspect(f.gatewayPath)
	if err != nil || state.Pending == nil || len(state.Calls) != 1 || state.Calls[0].Receipt != nil {
		t.Fatal("missing evidence settled", err)
	}
	if _, err = f.client.Execute(ctx, f.request); !errors.Is(err, ErrPending) || calls.Load() != 1 {
		t.Fatal("evidence failure retried", err)
	}
}

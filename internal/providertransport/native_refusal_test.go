package providertransport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"harness.local/engorch/internal/providergateway"
)

func TestAnthropicRefusalRequiresExactModelBeforeDurableClassification(t *testing.T) {
	for _, model := range []string{"wire-model", "foreign-model"} {
		for _, mode := range []string{"json", "sse", "sse-incomplete", "sse-error", "json-over-budget"} {
			t.Run(model+"/"+mode, func(t *testing.T) {
				calls := 0
				protocol := finiteProtocolFixture(t, providergateway.AnthropicMessagesAdapter)
				body := string(finiteJSONResponse(providergateway.AnthropicMessagesAdapter))
				mediaType := "application/json"
				if strings.HasPrefix(mode, "sse") {
					body = string(finiteSSETextResponse(providergateway.AnthropicMessagesAdapter))
					mediaType = "text/event-stream"
					protocol.capabilities.SupportedResponseFramings = []providergateway.ResponseFraming{providergateway.ResponseFramingSSE}
					protocol.expectation.ResponseFraming = providergateway.ResponseFramingSSE
					protocol.body = []byte(strings.Replace(string(protocol.body), `"stream":false`, `"stream":true`, 1))
					protocol.expectation.MaxBytes = int64(len(protocol.body))
					if mode == "sse-incomplete" {
						end := strings.Index(body, "event: message_stop")
						if end < 0 {
							t.Fatal("fixture lacks terminal")
						}
						body = body[:end]
					}
					if mode == "sse-error" {
						body = responsesEventForNativeRefusalError()
					}
				}
				if mode == "json-over-budget" {
					body = strings.Replace(body, `"output_tokens":2`, `"output_tokens":6`, 1)
				}
				body = strings.Replace(body, `"end_turn"`, `"refusal"`, 1)
				body = strings.Replace(body, "wire-model", model, 1)
				server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
					calls++
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", mediaType)
					_, _ = w.Write([]byte(body))
				})
				f := newTransportFixtureForProtocol(t, server, "subscription", "bearer", "", 16<<10, protocol)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				result, err := f.client.Execute(ctx, f.request)
				var attempt *AttemptError
				want := providergateway.FailureContentPolicy
				if model != "wire-model" || mode == "sse-incomplete" || mode == "sse-error" || mode == "json-over-budget" {
					want = providergateway.FailureMalformedResponse
				}
				if !errors.As(err, &attempt) || !attempt.Recorded || attempt.Observation.Class != want || result.Content != nil || len(result.Body) != 0 {
					t.Fatal("native refusal differs", err)
				}
				state, inspectErr := providergateway.Inspect(f.gatewayPath)
				if inspectErr != nil || state.Pending == nil || state.Calls[0].Failure.Class != want {
					t.Fatal("durable classification differs", inspectErr)
				}
				if _, retryErr := f.client.Execute(ctx, f.request); !errors.Is(retryErr, ErrPending) || calls != 1 {
					t.Fatal("refusal allowed resend", retryErr)
				}
			})
		}
	}
}

func responsesEventForNativeRefusalError() string {
	return string(responsesEvents(`{"type":"error","error":{"type":"refusal","message":"private-provider-secret"}}`))
}

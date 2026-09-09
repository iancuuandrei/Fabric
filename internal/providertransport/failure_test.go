package providertransport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"harness.local/engorch/internal/access"
	"harness.local/engorch/internal/journal"
	"harness.local/engorch/internal/providergateway"
)

func TestExecuteRecordsHTTPFailuresAcrossFiniteAdaptersWithoutRetry(t *testing.T) {
	for _, adapter := range []string{providergateway.OpenAIChatCompletionsAdapter, providergateway.OpenAIResponsesAdapter, providergateway.AnthropicMessagesAdapter} {
		for _, framing := range []providergateway.ResponseFraming{providergateway.ResponseFramingJSON, providergateway.ResponseFramingSSE} {
			for _, scheme := range []string{"bearer", "api-key-header"} {
				for _, status := range []int{201, 401, 429, 529} {
					t.Run(adapter+"/"+string(framing)+"/"+scheme+"/"+strconv.Itoa(status), func(t *testing.T) {
						var calls atomic.Int64
						server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
							calls.Add(1)
							if scheme == "bearer" {
								if r.Header.Get("authorization") != "Bearer "+fixtureSecret || r.Header.Get("x-provider-key") != "" {
									t.Error("bearer credential attachment differs")
								}
							} else if r.Header.Get("x-provider-key") != fixtureSecret || r.Header.Get("authorization") != "" {
								t.Error("named credential attachment differs")
							}
							body, readErr := io.ReadAll(r.Body)
							if readErr != nil || strings.Contains(string(body), fixtureSecret) || strings.Contains(r.URL.String(), fixtureSecret) {
								t.Error("credential escaped authentication header")
							}
							w.Header().Set("content-type", "application/json")
							w.WriteHeader(status)
							_, _ = w.Write([]byte(`{"error":{"type":"insufficient_quota","message":"private-provider-secret"}}`))
						})
						protocol := finiteProtocolFixture(t, adapter)
						protocol.capabilities.SupportedResponseFramings = []providergateway.ResponseFraming{framing}
						protocol.expectation.ResponseFraming = framing
						if framing == providergateway.ResponseFramingSSE {
							protocol.body = []byte(strings.Replace(string(protocol.body), `"stream":false`, `"stream":true`, 1))
							if adapter == providergateway.OpenAIChatCompletionsAdapter {
								protocol.body = []byte(strings.Replace(string(protocol.body), `"stream":true`, `"stream":true,"stream_options":{"include_usage":true}`, 1))
							}
							protocol.expectation.MaxBytes = int64(len(protocol.body))
						}
						header := ""
						if scheme == "api-key-header" {
							header = "x-provider-key"
						}
						f := newTransportFixtureForProtocol(t, server, "subscription", scheme, header, 16<<10, protocol)
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						defer cancel()
						result, err := f.client.Execute(ctx, f.request)
						var attempt *AttemptError
						if !errors.Is(err, ErrPending) || !errors.As(err, &attempt) || !attempt.Recorded || len(result.Body) != 0 || result.Content != nil || calls.Load() != 1 {
							t.Fatal("HTTP failure was not recorded before return", err, calls.Load())
						}
						want := providergateway.FailureUnknown
						if status == 401 {
							want = providergateway.FailureAuthentication
						}
						if status == 429 {
							want = providergateway.FailureQuota
							if adapter == providergateway.AnthropicMessagesAdapter {
								want = providergateway.FailureRateLimit
							}
						}
						if status == 529 {
							want = providergateway.FailureTransientProvider
						}
						state, inspectErr := providergateway.Inspect(f.gatewayPath)
						if inspectErr != nil || state.Pending == nil || len(state.Calls) != 1 || state.Calls[0].Failure == nil || state.Calls[0].Failure.Class != want || state.Calls[0].Failure.HTTPStatus != status || state.Calls[0].Failure.ResponseSHA256 == "" {
							t.Fatal("durable HTTP diagnostic differs", inspectErr, state)
						}
						if activeErr := access.RequireActive(f.accessPath, f.policy, f.intent); activeErr != nil {
							t.Fatal("HTTP class released reservation", activeErr)
						}
						if _, retryErr := f.client.Execute(ctx, f.request); !errors.Is(retryErr, ErrPending) || calls.Load() != 1 {
							t.Fatal("classified request resent", retryErr, calls.Load())
						}
						if strings.Contains(err.Error(), "private-provider-secret") {
							t.Fatal("provider message leaked")
						}
						assertSecretAbsent(t, f.gatewayPath, f.accessPath)
						exported, exportErr := journal.ExportJSONL(f.gatewayPath)
						if exportErr != nil || strings.Contains(string(exported), "private-provider-secret") {
							t.Fatal("provider message reached failure journal", exportErr)
						}
					})
				}
			}
		}
	}
}

func TestHTTPFailureClassificationDoesNotCopyProviderMessages(t *testing.T) {
	for _, test := range []struct {
		name, adapter string
		status        int
		body          string
		want          providergateway.FailureClass
	}{
		{"quota", providergateway.OpenAIResponsesAdapter, 429, `{"error":{"code":"insufficient_quota","message":"private-provider-secret"}}`, providergateway.FailureQuota},
		{"rate", providergateway.OpenAIChatCompletionsAdapter, 429, `{"error":{"code":"rate_limit_exceeded"}}`, providergateway.FailureRateLimit},
		{"duplicate discriminator", providergateway.OpenAIResponsesAdapter, 429, `{"error":{"code":"insufficient_quota","code":"rate_limit_exceeded"}}`, providergateway.FailureRateLimit},
		{"message is not a code", providergateway.OpenAIResponsesAdapter, 429, `{"error":{"message":"insufficient_quota private-provider-secret"}}`, providergateway.FailureRateLimit},
		{"no cross protocol override", providergateway.AnthropicMessagesAdapter, 429, `{"error":{"type":"insufficient_quota"}}`, providergateway.FailureRateLimit},
		{"anthropic auth", providergateway.AnthropicMessagesAdapter, 401, `{"error":{"type":"authentication_error"}}`, providergateway.FailureAuthentication},
		{"overloaded", providergateway.AnthropicMessagesAdapter, 529, "", providergateway.FailureTransientProvider},
		{"timeout", providergateway.AnthropicMessagesAdapter, 504, "", providergateway.FailureTimeout},
		{"invalid", providergateway.OpenAIResponsesAdapter, 400, "", providergateway.FailureInvalidRequest},
		{"ambiguous billing", providergateway.AnthropicMessagesAdapter, 402, "", providergateway.FailureUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := observedHTTPFailure(test.adapter, test.status, []byte(test.body))
			var observed *observedAttemptError
			if !errors.As(err, &observed) || observed.observation.Class != test.want || strings.Contains(err.Error(), "private-provider-secret") {
				t.Fatal("unsafe or incorrect classification", err)
			}
			public := &AttemptError{Observation: observed.observation}
			if !errors.Is(public, ErrPending) || strings.Contains(public.Error(), "private-provider-secret") {
				t.Fatal("classification released or exposed effect", public)
			}
		})
	}
	invalid := &AttemptError{Observation: providergateway.FailureObservation{Class: "private-provider-secret"}}
	if strings.Contains(invalid.Error(), "private-provider-secret") {
		t.Fatal("invalid public class leaked")
	}
}

func TestHTTPFailureEvidenceRequiresCompleteBoundedBody(t *testing.T) {
	quota := `{"error":{"code":"insufficient_quota"}}`
	for _, test := range []struct {
		name          string
		body          string
		declaredExtra int
		wantDigest    bool
	}{
		{"exact limit", quota + strings.Repeat(" ", maximumErrorBodyBytes-len(quota)), 0, true},
		{"over limit", quota + strings.Repeat(" ", maximumErrorBodyBytes-len(quota)+1), 0, false},
		{"truncated", quota, 100, false},
		{"empty complete", "", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if _, err := io.Copy(io.Discard, r.Body); err != nil {
					t.Error("request body read failed", err)
				}
				w.Header().Set("Content-Length", strconv.Itoa(len(test.body)+test.declaredExtra))
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(test.body))
			})
			f := newFiniteTransportFixture(t, server, providergateway.OpenAIResponsesAdapter)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := f.client.Execute(ctx, f.request)
			var attempt *AttemptError
			if !errors.As(err, &attempt) || !attempt.Recorded || len(result.Body) != 0 || result.Content != nil {
				t.Fatal("bounded failure was not durably recorded", err)
			}
			observation := attempt.Observation
			wantClass := providergateway.FailureRateLimit
			if test.wantDigest && test.body != "" {
				wantClass = providergateway.FailureQuota
			}
			if observation.Class != wantClass || observation.HTTPStatus != 429 {
				t.Fatal("partial or over-limit body affected classification", observation)
			}
			if test.wantDigest {
				digest := sha256.Sum256([]byte(test.body))
				if observation.ResponseSHA256 != hex.EncodeToString(digest[:]) || observation.ResponseBytes != int64(len(test.body)) {
					t.Fatal("complete body evidence differs", observation)
				}
			} else if observation.ResponseSHA256 != "" || observation.ResponseBytes != 0 {
				t.Fatal("partial body presented as complete evidence", observation)
			}
			state, inspectErr := providergateway.Inspect(f.gatewayPath)
			if inspectErr != nil || state.Pending == nil || len(state.Calls) != 1 || state.Calls[0].Failure == nil || !reflect.DeepEqual(*state.Calls[0].Failure, observation) {
				t.Fatal("bounded observation changed on replay", inspectErr)
			}
			if activeErr := access.RequireActive(f.accessPath, f.policy, f.intent); activeErr != nil {
				t.Fatal("bounded failure released reservation", activeErr)
			}
			if _, retryErr := f.client.Execute(ctx, f.request); !errors.Is(retryErr, ErrPending) || calls.Load() != 1 {
				t.Fatal("bounded failure allowed resend", retryErr, calls.Load())
			}
		})
	}
}

func TestAttemptFailureRecordsWithoutResendingOrReleasing(t *testing.T) {
	server := newTLSServer(t, func(http.ResponseWriter, *http.Request) { t.Error("diagnostic helper made a network request") })
	f := newTransportFixture(t, server, "api", "bearer", "", 8192)
	digest := sha256.Sum256(f.request.Body)
	call, err := providergateway.BeginWithExpectation(f.gatewayPath, f.accessPath, f.policy, f.intent, f.binding, hex.EncodeToString(digest[:]), int64(len(f.request.Body)), f.request.Expectation)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = recordAttemptFailure(ctx, f.request, call, context.Canceled)
	var attempt *AttemptError
	if !errors.As(err, &attempt) || !attempt.Recorded || !errors.Is(err, ErrPending) || !errors.Is(err, context.Canceled) || attempt.Observation.Class != providergateway.FailureLocalCancellation {
		t.Fatal("cancellation observation unavailable", err)
	}
	assertPending(t, f)
	state, err := providergateway.Inspect(f.gatewayPath)
	if err != nil || state.Calls[0].Failure == nil || *state.Calls[0].Failure != attempt.Observation {
		t.Fatal("durable failure differs", err)
	}
}

func TestExecuteDoesNotClaimRecordedFailureWhenJournalBecomesInvalid(t *testing.T) {
	var calls atomic.Int64
	var gatewayPath string
	server := newTLSServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		// Begin must already be durable before the HTTP handler can run.
		state, err := providergateway.Inspect(gatewayPath)
		if err != nil || state.Pending == nil {
			t.Error("HTTP dispatch lacks durable pending intent", err)
		}
		file, err := os.OpenFile(gatewayPath, os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		_, writeErr := file.WriteAt([]byte("private-journal-corruption\n"), 0)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Error("could not inject journal fault", writeErr, closeErr)
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	f := newFiniteTransportFixture(t, server, providergateway.OpenAIResponsesAdapter)
	gatewayPath = f.gatewayPath
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := f.client.Execute(ctx, f.request)
	var attempt *AttemptError
	if !errors.Is(err, ErrPending) || !errors.As(err, &attempt) || attempt.Recorded || attempt.Observation.Class != providergateway.FailureAuthentication || len(result.Body) != 0 || result.Content != nil {
		t.Fatal("failed append was presented as durable evidence", err)
	}
	if strings.Contains(err.Error(), gatewayPath) || strings.Contains(err.Error(), "private-journal-corruption") {
		t.Fatal("journal internals leaked", err)
	}
	if _, inspectErr := providergateway.Inspect(gatewayPath); inspectErr == nil {
		t.Fatal("fault injection did not invalidate the journal")
	}
	if activeErr := access.RequireActive(f.accessPath, f.policy, f.intent); activeErr != nil {
		t.Fatal("diagnostic failure released access", activeErr)
	}
	if _, retryErr := f.client.Execute(ctx, f.request); retryErr == nil || calls.Load() != 1 {
		t.Fatal("unreadable journal permitted resend", retryErr, calls.Load())
	}
}

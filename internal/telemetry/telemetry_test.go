package telemetry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestCommandSpanHasBoundedDiagnosticAttributes(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	ctx, runtime, err := newRuntime(context.Background(), recorder)
	if err != nil {
		t.Fatal(err)
	}
	_, finish := StartCommand(ctx, "run")
	finish(errors.New("credential=do-not-record prompt=do-not-record"))

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Name() != "engorch.command" {
		t.Fatalf("span name = %q", span.Name())
	}
	if span.Status().Code != codes.Error || span.Status().Description != "" {
		t.Fatalf("status = %#v, want error without description", span.Status())
	}
	want := map[attribute.Key]string{
		"engorch.command": "run",
		"engorch.outcome": "error",
	}
	if got := stringAttributes(span.Attributes()); !equalAttributes(got, want) {
		t.Fatalf("attributes = %#v, want %#v", got, want)
	}
	if got := span.Events(); len(got) != 0 {
		t.Fatalf("events = %#v, want none", got)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCommandSpanClassifiesCancellationAndBoundsCommand(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	ctx, runtime, err := newRuntime(context.Background(), recorder)
	if err != nil {
		t.Fatal(err)
	}
	_, finish := StartCommand(ctx, "Run /private/source")
	finish(context.Canceled)

	span := recorder.Ended()[0]
	got := stringAttributes(span.Attributes())
	if got["engorch.command"] != "unknown" || got["engorch.outcome"] != "canceled" {
		t.Fatalf("attributes = %#v", got)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCommandNameIsAnExplicitLowCardinalitySet(t *testing.T) {
	for _, command := range []string{"", "Run /private/source", "valid-looking-but-user-controlled", strings.Repeat("a", 64)} {
		if got := boundedCommand(command); got != "unknown" {
			t.Errorf("boundedCommand(%q) = %q, want unknown", command, got)
		}
	}
	for _, command := range []string{"help", "inspect", "ri", "prepare-commit-recovery"} {
		if got := boundedCommand(command); got != command {
			t.Errorf("boundedCommand(%q) = %q", command, got)
		}
	}
}

func TestEmptyEndpointPerformsNoNetworkOperation(t *testing.T) {
	originalTransport := http.DefaultTransport
	http.DefaultTransport = panicRoundTripper{}
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	ctx, runtime, err := New(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, finish := StartCommand(ctx, "help")
	finish(nil)
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitEndpointExportsBoundedSpan(t *testing.T) {
	var requests atomic.Int32
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/v1/traces" || request.URL.RawQuery != "" {
			t.Errorf("request = %s %s", request.Method, request.URL.String())
		}
		if got := request.Header.Get("Content-Type"); got != "application/x-protobuf" {
			t.Errorf("content type = %q", got)
		}
		body, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/x-protobuf")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, runtime, err := New(context.Background(), Config{OTLPTracesEndpoint: server.URL + "/v1/traces"})
	if err != nil {
		t.Fatal(err)
	}
	_, finish := StartCommand(ctx, "run")
	finish(errors.New("secret-prompt-source-path"))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
	encoded := string(body)
	for _, want := range []string{"engorch.command", "engorch.outcome", "run", "error"} {
		if !strings.Contains(encoded, want) {
			t.Errorf("export body does not contain %q", want)
		}
	}
	if strings.Contains(encoded, "secret-prompt-source-path") {
		t.Fatal("export body contains error content")
	}
}

func TestEndpointValidationRejectsAuthorityAndPayloadAmbiguity(t *testing.T) {
	invalid := []string{
		"http://user:password@127.0.0.1:4318/v1/traces",
		"http://127.0.0.1:4318/v1/traces?token=secret",
		"http://127.0.0.1:4318/v1/traces#fragment",
		"http://127.0.0.1:4318/other",
		"ftp://127.0.0.1/v1/traces",
		" http://127.0.0.1:4318/v1/traces",
		strings.Repeat("x", maximumEndpointBytes+1),
	}
	for _, endpoint := range invalid {
		if _, err := validateEndpoint(endpoint); err == nil {
			t.Errorf("validateEndpoint(%q) succeeded", endpoint)
		}
	}
}

func TestShutdownHonorsCanceledContext(t *testing.T) {
	processor := blockingProcessor{}
	_, runtime, err := newRuntime(context.Background(), processor)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err = runtime.Shutdown(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v, want context canceled", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("shutdown took %s with canceled context", elapsed)
	}
}

type panicRoundTripper struct{}

func (panicRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	panic("unexpected network operation")
}

type blockingProcessor struct{}

func (blockingProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (blockingProcessor) OnEnd(sdktrace.ReadOnlySpan)                     {}
func (blockingProcessor) ForceFlush(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
func (blockingProcessor) Shutdown(ctx context.Context) error {
	return ctx.Err()
}

func stringAttributes(attributes []attribute.KeyValue) map[attribute.Key]string {
	result := make(map[attribute.Key]string, len(attributes))
	for _, item := range attributes {
		result[item.Key] = item.Value.AsString()
	}
	return result
}

func equalAttributes(left, right map[attribute.Key]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range right {
		if left[key] != value {
			return false
		}
	}
	return true
}

package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	serviceName          = "engorch"
	instrumentationName  = "harness.local/engorch"
	maximumEndpointBytes = 2048
)

// Config enables one explicitly selected OTLP/HTTP trace endpoint. An empty
// endpoint keeps tracing local and performs no network operation.
type Config struct {
	OTLPTracesEndpoint string
}

// Runtime owns one tracer provider and its bounded flush lifecycle.
type Runtime struct {
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
	once     sync.Once
	err      error
}

type runtimeContextKey struct{}

// New constructs a local tracer provider and attaches it to the returned
// context. Export is configured only when OTLPTracesEndpoint is non-empty.
func New(ctx context.Context, config Config) (context.Context, *Runtime, error) {
	if ctx == nil {
		return nil, nil, errors.New("telemetry context required")
	}
	var processor sdktrace.SpanProcessor
	if config.OTLPTracesEndpoint != "" {
		endpoint, err := validateEndpoint(config.OTLPTracesEndpoint)
		if err != nil {
			return nil, nil, err
		}
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, nil, errors.New("cannot initialize OTLP HTTP transport")
		}
		transport := defaultTransport.Clone()
		transport.Proxy = nil
		exporter, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpointURL(endpoint),
			otlptracehttp.WithHTTPClient(&http.Client{Transport: transport, Timeout: 5 * time.Second}),
			otlptracehttp.WithRetry(otlptracehttp.RetryConfig{Enabled: false}),
		)
		if err != nil {
			return nil, nil, errors.New("cannot initialize OTLP trace exporter")
		}
		processor = sdktrace.NewBatchSpanProcessor(exporter,
			sdktrace.WithMaxQueueSize(256), sdktrace.WithMaxExportBatchSize(64),
			sdktrace.WithBatchTimeout(time.Second), sdktrace.WithExportTimeout(5*time.Second),
		)
	}
	return newRuntime(ctx, processor)
}

func newRuntime(ctx context.Context, processor sdktrace.SpanProcessor) (context.Context, *Runtime, error) {
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", serviceName)))
	if err != nil {
		return nil, nil, errors.New("cannot initialize telemetry resource")
	}
	options := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	}
	if processor != nil {
		options = append(options, sdktrace.WithSpanProcessor(processor))
	}
	provider := sdktrace.NewTracerProvider(options...)
	runtime := &Runtime{provider: provider, tracer: provider.Tracer(instrumentationName)}
	return context.WithValue(ctx, runtimeContextKey{}, runtime), runtime, nil
}

// Shutdown flushes and closes the provider once, honoring the caller's
// cancellation or deadline. It never creates an unbounded background wait.
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil || r.provider == nil || ctx == nil {
		return errors.New("telemetry runtime and shutdown context required")
	}
	r.once.Do(func() {
		r.err = errors.Join(r.provider.ForceFlush(ctx), r.provider.Shutdown(ctx))
	})
	return r.err
}

// StartCommand starts one low-cardinality command span. It records only a
// bounded command identifier and terminal class; arguments, paths, prompts,
// source content, credentials, and error messages are excluded.
func StartCommand(ctx context.Context, command string) (context.Context, func(error)) {
	runtime, _ := ctx.Value(runtimeContextKey{}).(*Runtime)
	if runtime == nil || runtime.tracer == nil {
		return ctx, func(error) {}
	}
	command = boundedCommand(command)
	spanCtx, span := runtime.tracer.Start(ctx, "engorch.command", trace.WithAttributes(attribute.String("engorch.command", command)))
	return spanCtx, func(err error) {
		outcome := "ok"
		if err != nil {
			outcome = "error"
			span.SetStatus(codes.Error, "")
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				outcome = "canceled"
			}
		}
		span.SetAttributes(attribute.String("engorch.outcome", outcome))
		span.End()
	}
}

func boundedCommand(command string) string {
	switch command {
	case "help", "reference", "prepare-commit-recovery", "recover-commit", "prepare-commit-lease", "recover-commit-lease",
		"prepare-commit", "commit", "prepare-push", "push", "reconcile-push", "prepare-draft", "draft", "reconcile-draft",
		"prepare-draft-lease", "recover-draft-lease", "prepare-push-lease", "recover-push-lease", "ri", "runtime-usage",
		"prepare-recovery", "recover-files", "prepare-files", "apply-files", "init", "doctor", "plan", "prepare-explorer",
		"explore", "usage", "prepare-review", "review", "prepare-writer", "write", "inspect", "resume", "approve", "run",
		"reconcile", "verify", "close-verification", "status":
		return command
	default:
		return "unknown"
	}
}

func validateEndpoint(value string) (string, error) {
	if len(value) > maximumEndpointBytes || strings.TrimSpace(value) != value {
		return "", errors.New("invalid OTLP traces endpoint")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.Hostname() == "" || parsed.Path != "/v1/traces" || parsed.RawPath != "" || parsed.String() != value {
		return "", errors.New("exact OTLP HTTP traces endpoint required")
	}
	return value, nil
}

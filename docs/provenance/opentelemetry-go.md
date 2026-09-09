# OpenTelemetry Go dependency

EngOrch uses the official OpenTelemetry Go API, SDK, and OTLP/HTTP trace
exporter at module version `v1.46.0`, released on 2026-08-25. The upstream
project is Apache-2.0 licensed. The dependency supplies trace semantics,
buffering, export, flush, and cancellation behavior; EngOrch only supplies a
small attribute and lifecycle boundary.

Primary sources:

- <https://github.com/open-telemetry/opentelemetry-go/releases/tag/v1.46.0>
- <https://pkg.go.dev/go.opentelemetry.io/otel@v1.46.0>
- <https://pkg.go.dev/go.opentelemetry.io/otel/sdk@v1.46.0>
- <https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp@v1.46.0>
- <https://github.com/open-telemetry/opentelemetry-go/blob/v1.46.0/LICENSE>

No collector is contacted unless `ENGORCH_OTLP_TRACES_ENDPOINT` is explicitly
set to an exact OTLP HTTP traces URL ending in `/v1/traces`. Export requests do
not use ambient proxy settings or automatic retries. Span attributes contain a
bounded command name and terminal class only. CLI arguments, repository paths,
run identifiers, prompts, source content, credentials, and error text are not
recorded. Canonical journals remain the authority for state and effects.

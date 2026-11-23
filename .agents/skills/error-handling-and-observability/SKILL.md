---
name: error-handling-and-observability
description: Classify failures cleanly, wrap errors with safe operation context, enforce transient vs non-retryable distinctions, and emit privacy-safe structured telemetry. Use when building I/O boundaries, retry loops, HTTP handlers, or instrumentation.
metadata:
  fabric.roles: "writer,fixer,reviewer"
---

# Error Handling & Privacy-Safe Observability

Make every failure self-diagnosing in development and operations without ever leaking credentials, private code, or raw customer payloads.

## 1. Explicit Failure Classification & Context
- Wrap errors at architectural boundaries with the failing operation and safe identifier (e.g., `%w` in Go, exception causes elsewhere) while preserving sentinel/typed errors (`errors.Is`, `errors.As`).
- Separate caller validation (`400`/`422`), authentication (`401`/`403`), missing entities (`404`), transient upstream faults (`429`/`502`/`503`), and internal bugs (`500`).
- Retry only idempotent operations on transient status/network errors using bounded exponential backoff + jitter; fail fast on invalid input, auth failures (`401`/`403`), and hard quota exhaustion.

## 2. Zero Silent Swallowing
- Never discard errors from file writes, renames, DB commits, flushes, or state-mutating external calls.
- When best-effort degradation is intentional (e.g., optional telemetry or secondary webhook alert), emit a clear warning naming the failing subsystem instead of hiding the failure.

## 3. Privacy-Safe Structured Telemetry
- Emit structured events/spans via the repository's observability layer carrying stable correlation IDs (`run_id`, stage, provider, exit status, latency).
- NEVER log or attach API tokens, `Authorization` headers, signing secrets, cookies, raw prompts, raw repository snippets, or full upstream error bodies without length/secret sanitization.
- Keep metric/span attribute values low-cardinality (route templates like `/v1/webhooks/{provider}`, not dynamic IDs or query strings).

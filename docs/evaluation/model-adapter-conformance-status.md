# Model adapter conformance status

Status: **FAIL** as of 2026-09-08. This is a bounded working-tree audit of the
finite Chat Completions, Responses and Anthropic Messages adapters. It is not a
live-provider qualification, a model-quality result or a production-readiness
claim.

The later finite-framing integration passed canonical gateway, transport, direct
runtime, config and OpenCode package tests plus focused controller routing tests.
JSON is now selected by explicit model capabilities and role configuration and
survives direct-runtime recovery. The framing row and coverage boundary below
reflect that later checkpoint; the other rows retain their stated baseline
scope. Overall FAIL remains appropriate because cost/raw usage and the complete
error/auth/privacy matrix are not qualified. See [current status](status.md).

The status terms are deliberately strict:

- **PASS** means the production code path was inspected and a local,
  deterministic test executed in this audit directly asserted the stated
  behavior.
- **FAIL** means the current production path omits or contradicts part of the
  acceptance requirement.
- **NOT RUN** means an implementation is visible, but the required
  adapter-specific or end-to-end assertion was not executed or does not exist.

An implementation note below is inspection evidence. It does not turn a test
name, fixture, or plausible code path into executed qualification.

## Requirement ledger

| Requirement | Status | Current implementation and authoritative pointers | Executed evidence and limit |
| --- | --- | --- | --- |
| Capability negotiation and no substitution | **PASS** | [`ValidateRequiredCapabilities`](../../internal/providergateway/contracts_v2.go#L181) compares the exact bound model/adapter capabilities and returns `CAPABILITY_UNAVAILABLE`; [`ValidateAdapterRequest`](../../internal/providergateway/contracts_v2.go#L562) performs that check before protocol validation. [`Client.Execute`](../../internal/providertransport/transport.go#L85) performs admission before `BeginWithExpectation` and before the POST. | The executed gateway and transport tests asserted unsupported tools/reasoning/structured modes reject exactly, do not select another binding, and create neither journal nor network effects. This is local fixture evidence only. |
| Authentication and secret isolation | **PASS** for the local adapter/framing matrix | The shared credential lease attaches only the configured bearer or named API-key header after admission. | 48 HTTP failure cases and 12 text-success cases exercise all three adapters, both framings and both schemes. Assertions cover selected-header isolation, failure-path request body/URL secrecy, journal secrecy and no resend. This is not upstream authentication acceptance. |
| Exact request, route and observed model identity | **PASS** | V2 bindings hash route, endpoint, model, access invocation, adapter capabilities and exact allowed observed aliases. [`AcceptsObservedModel`](../../internal/providergateway/contracts_v2.go#L483) accepts only the requested model or an explicitly configured exact alias; request validators require the bound raw model. Durable intents and receipts retain the binding/invocation/call identity and [`RequestExpectationID`](../../internal/providergateway/journal.go#L146). | Executed tests asserted aliases are hash-bound and never rewrite the requested model, rejected substituted observations in all three SSE decoders, and exercised Responses transport completion with the exact request-expectation identity in the durable receipt. No live alias behavior was tested. |
| Structured-output modes and final validation | **PASS** for the Responses production codec; transport/receipt matrix **NOT RUN** | The contract distinguishes `UNSUPPORTED`, `TEXT_PARSE_REQUIRED`, `JSON_ONLY` and `STRICT_SCHEMA` in [`contracts_v2.go`](../../internal/providergateway/contracts_v2.go#L57). Responses request validation binds native `json_object`/`json_schema` controls in [`responses_request.go`](../../internal/providergateway/responses_request.go#L520); [`structured_output.go`](../../internal/providergateway/structured_output.go#L19) bounds/compiles schemas and validates final JSON or schema instances. `Execute` forwards the exact required capabilities to final response decoding. Anthropic fails closed for native JSON/strict modes. | The executed tests passed native JSON-only and strict-schema controls through `ValidateAdapterRequest`, rejected wire substitution, accepted schema-valid output through `DecodeAdapterResponse`, rejected a schema violation, and rejected external references/duplicate schema keys/oversize schemas. No HTTP transport test yet binds that result to a durable receipt; unsupported Chat/Anthropic native modes have rejection evidence rather than positive-mode evidence. `TEXT_PARSE_REQUIRED` does not claim native enforcement. |
| Tool calls, results, order and parallel semantics | **PASS** for protocol codecs; complete transport matrix **NOT RUN** | Chat validation tracks pending call IDs and exact results in [`request.go`](../../internal/providergateway/request.go#L75). Responses and Anthropic validators bind tool catalogs, tool choice, parallel controls and transcript identity; their SSE decoders preserve ordered call IDs/names/arguments and reject duplicates or topology substitution. | The executed package tests call the production validators/decoders and assert negative catalog drift, duplicate IDs, unmatched results, order/topology ambiguity and parallel-control drift for all three protocols. Only the Responses tool path was also executed through `providertransport` into a durable semantic receipt. |
| Finite timeout before dispatch | **PASS** | [`validateExecutionContext`](../../internal/providertransport/transport.go#L149) requires a finite deadline within the transport bound before durable begin or network activity. The HTTP request inherits that context. | The executed pre-Begin test asserted an unbounded context and foreign credential lease produce no call journal and no server call. The cancellation test used a 100 ms deadline against a handler that waited for context cancellation. |
| Cancellation outcome semantics | **FAIL** for the full requirement; local cancellation **PASS** | Sent calls retain pending status and active reservations. Typed durable observations distinguish local cancellation and timeout; provider-confirmed cancellation remains unqualified. | Full transport race tests passed, including actual JSON/SSE local cancellation with recorded finite diagnostics. Classification does not authorize replay. |
| Malformed, incomplete and oversized responses | **PASS** for streaming fixtures | The three strict SSE decoders bound bytes and token totals; reject duplicate keys/identities, invalid event topology, incomplete terminal state and inconsistent usage. Transport returns no content unless decode, final identity/usage/semantic checks and durable completion all succeed. | The executed decoder suites cover positive and negative constructed or recorded fixtures for all three protocols. [`TestExecuteRejectsMalformedAndOversizedResponsesAsPending`](../../internal/providertransport/transport_test.go#L181) also asserted one sent request, no returned bytes, no retry and a pending durable call. This is not upstream-live compatibility proof. |
| Rate limit and provider error classification | **FAIL** for the full taxonomy; bounded HTTP diagnostics **PASS** | Execute records exact-call failure observations with finite classes and bounded body hashes, excluding provider message text. HTTP status and protocol-specific machine codes classify auth, rate/quota, timeout, invalid requests and transient failures. | Full transport race suite passed in 15.085s. The 24 HTTP cases cover three adapters, both framings and statuses 201/401/429/529; each asserts durable classification, preserved reservation, no content leakage and no resend. Native in-stream error and provider-confirmed cancellation classification remain incomplete. |
| Normalized and raw usage, cache, reasoning and cost | **FAIL** | [`Usage`](../../internal/providergateway/journal.go#L110) durably retains normalized input/output, cache-read/cache-write and reasoning token categories with pointer fields for reported zero versus unknown. A Responses extension can parse a bounded trailing cost ping. [`CallReceipt`](../../internal/providergateway/journal.go#L146) retains neither canonical cost nor bounded raw provider-usage evidence. | Executed protocol tests asserted token totals, cache subsets, reasoning categories and unknown-versus-zero behavior; the Responses cost-ping parser was also exercised. Those facts are not persisted as the full required normalized cost plus raw provider evidence. |
| Privacy/retention admission | **PASS** for bound-policy drift rejection | Explicit v2 access profiles bind training, retention, terms digest and repository-class allowances to admission identity. | 24 cases reject changed training, retention, terms or repository class before network access and without gateway changes (race PASS 9.053s). Twelve positive text cases use explicit privacy profiles (race PASS 7.762s). No provider compliance or confidential-data live qualification is claimed. |
| Retry and uncertain-call replay prevention | **PASS** for the current one-shot streaming transport | The request body uses a one-shot reader, `GetBody` is disabled, redirects are rejected, and an existing pending journal prevents another send. Classification never authorizes retry. | Executed redirect, malformed-response and existing-pending tests counted one server call and asserted a second `Execute` does not resend. This does not prove a future retry policy because classification supplies no retry authority. |
| Streaming/non-streaming equivalence | **PASS** for paired codecs and bounded text/auth/privacy/HTTP-error matrices | Three finite JSON codecs share identity, semantic and usage validation with SSE. Configuration selects immutable framing capabilities and runtime recovery retains that selection. | Twelve text-success, 48 HTTP-error and 24 policy-drift cases span all three adapters and both framings. Tool/structured-output transport matrices and full native error semantics remain incomplete; this is not universal feature equivalence. |
| Durable success receipt before content release | **PASS** for all six text adapter/framing paths | Execute begins durably, validates the complete response, commits its receipt and only then returns content. | Twelve local TLS cases assert full equality of returned and replayed durable receipts, no pending call, exact observed model/token counts and no second POST. Includes Anthropic SSE and JSON, with both authentication schemes. |

## Protocol coverage boundary

The local test packages exercise the production Chat, Responses and Anthropic
request validators and streaming decoders. The transport suite directly covers
Chat authentication, cancellation, malformed input and replay prevention, plus
a Responses reasoning/tool/semantic-receipt path. It is not yet one identical,
table-driven conformance suite parameterized over all three adapters. In
particular, the baseline did not cover Anthropic transport completion. The later
JSON and SSE matrices now cover text completion, both authentication schemes,
policy drift and bounded HTTP errors for all three adapters. These later results
supersede the baseline coverage exclusions in this paragraph. Native in-stream
errors, provider-confirmed cancellation, complete tool/structured-output transport
matrices and cost/raw-usage receipts remain incomplete.

No external model endpoint was called. Recorded Anthropic payload tests retain
local compatibility evidence only; they do not prove the current behavior of an
upstream service or a named deployed model.

## Highest-value independent next implementations

1. Add a finite post-admission outcome and error taxonomy with sanitized,
   bounded evidence: HTTP/auth/rate-limit/quota/content-policy/5xx, local timeout,
   provider-confirmed cancellation and unknown outcome. Keep retry eligibility a
   separate controller decision and keep every sent uncertain call non-replayable.
2. Complete the shared production transport matrix over all three adapters and
   both framings, extending the implemented paired codecs and JSON TLS cases
   with matched auth/privacy/error and durable-receipt assertions.
3. Extend the durable receipt with canonical cost and bounded sanitized raw
   provider-usage evidence, preserving unknown versus reported zero. Add the
   per-adapter privacy/auth matrix while qualifying that receipt so one shared
   suite covers all three protocols.

## Executed command

Executed once against the inspected shared working tree on 2026-09-08:

```text
.\.local\toolchains\go\bin\go.exe test -count=1 ./internal/providergateway ./internal/providertransport ./internal/access
ok  harness.local/engorch/internal/providergateway  1.917s
ok  harness.local/engorch/internal/providertransport  0.991s
ok  harness.local/engorch/internal/access  0.875s
```

This proves only those local packages at that working-tree state. The repository
was under coordinated concurrent construction and had no committed revision to
which this result could be rebound. The overall conformance status remains
**FAIL** because passing package tests do not supply the missing requirements or
the absent per-adapter cases identified above.

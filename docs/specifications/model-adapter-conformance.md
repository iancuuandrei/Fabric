# Model adapter and runtime conformance

This specification incorporates the operator's model-access requirements of
2026-09-08. It is an acceptance contract, not a declaration that every item has
been implemented or qualified. The [status ledger](../evaluation/status.md)
records executed evidence.

Adding a model that uses an already-supported protocol must require configuration
only. A genuinely new protocol requires one bounded adapter and its conformance
tests. Adding a model must not change orchestration state, effect authority,
repository policy or verification.

## Architectural boundary

Model transports provide inference through three finite first-class adapters:
Responses, Chat Completions and Anthropic Messages. Agent runtimes provide their
own execution semantics: Codex App Server, OpenCode, ACP and the deterministic
fake runtime must be described and qualified separately. Listing a runtime here
does not imply that its implementation exists. A runtime may use a model
transport; an inference endpoint does not thereby provide sessions, workspace
interaction, cancellation or an agent tool loop.

Direct API execution provides structured inference through the selected model
transport, without a custom coding-agent or tool-execution loop. Coding-agent
loops belong to Codex, OpenCode or explicitly selected ACP runtimes. Direct
inference does not require OpenCode or its proxy. OpenCode-specific schema
projection and SDK configuration apply only when OpenCode is selected.

The common boundary consists of invocation identity, required capabilities,
an explicit protocol adapter and a small typed protocol-specific extension.
There is no universal request with loosely interpreted optional controls.

## Required invariants

| Area | Acceptance requirement |
| --- | --- |
| Capabilities | Compare route requirements with explicit model/adapter capabilities before dispatch; mismatch yields `CAPABILITY_UNAVAILABLE`, never substitution. |
| Structured output | Distinguish `STRICT_SCHEMA`, `JSON_ONLY`, `TEXT_PARSE_REQUIRED` and `UNSUPPORTED`; validate the selected mode and final output. |
| Tool calls | Preserve exact call/result ID correspondence, ordering and parallel-call semantics; reject ambiguity. |
| Reasoning | Bind actual provider controls and budgets; common intent such as `high` is not a universal guarantee. |
| Sessions | Distinguish deterministic run recovery, fresh inference and provider conversation continuation. Recovery does not authorize replaying an uncertain call. |
| Response framing | Streaming and non-streaming reach equivalent final identity, content and usage validation; controller semantics must not depend on streaming. |
| Cancellation | Distinguish local cancellation, provider-confirmed cancellation and unknown provider outcome. |
| Errors | Classify rate limit, transient provider error, timeout, authentication, invalid request, capability mismatch, content policy, quota and unknown outcome. Classification alone never authorizes retry. |
| Scheduling | Enforce concurrency and rate policies per access profile as well as role/run limits. A more restrictive profile lowers effective concurrency. |
| Caching | Preserve reported cache categories separately; equal prompt hashes imply neither equal cost nor a cache hit. |
| Identity | Retain route alias, requested model, provider model identifier and observed/resolved model distinctly. Explicit observed aliases never rewrite raw evidence. |
| Retirement | New runs may select a new configured model; an existing run remains bound to its original route on resume. |
| Privacy | Retention, training and repository-class admission belong to the access profile, not merely the provider name. |
| Hosted tools | Provider-side search, execution and other hosted tools require separate explicit capability and effect policy; support does not imply activation. |
| Untrusted context | Repository instructions, comments and model output cannot alter routing, privacy class, credentials or permissions. |
| Secrets | Credentials are attached by the admitted controller transport and excluded from worker context and ordinary journal payloads. |
| Usage | Retain normalized input/output/cache/reasoning/cost plus bounded provider usage evidence; unavailable values remain unknown. |
| Fallback | No implicit provider/model fallback. Any allowed alternative requires explicit policy and a new correctly bound invocation. |
| Routing | Role-to-model selection is operator configuration; a model cannot decide its own routing authority. |

## Shared adapter qualification

Post-admission failure observations belong to the exact gateway binding and
call. The finite class, observation phase, HTTP status and optional bounded
response digest/size may be retained; raw provider messages and bodies are not
part of this diagnostic record. Recording the observation preserves the pending
call and active reservation. Identical recording is idempotent, and a different
observation cannot overwrite the first. Classification supplies no retry,
cancellation-confirmation or budget-release authority. A local cancellation is
distinct from a provider cancellation observation, and neither by itself settles
unknown usage. Transport production and per-adapter qualification must be
verified separately from the journal primitive.

Each adapter must execute a deterministic conformance suite covering authentication,
requested/observed model identity, structured output, tools, timeout, cancellation,
malformed responses, rate limiting, usage, retry classification and privacy
admission. Tests must exercise the production adapter boundary, including negative
cases and durable receipts, rather than merely reimplement its rules in fixtures.

Recorded or constructed fixtures are clearly labelled and retain sanitization and
upstream provenance where applicable. Ordinary CI uses local deterministic
transports. Live qualification is separate and cannot be inferred from fixtures.
Conformance results identify the actual protocol, model capabilities, runtime,
access mode and tested limits; one passing route is not proof for another.

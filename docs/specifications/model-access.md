# Model access admission

The `internal/access` package implements admission primitives. Configuration v2
integrates the deterministic fake planner and Codex ChatGPT subscription routes
for planner, explorer, writer, fixer and reviewer. OpenCode and API execution
remain pending integration; existing v1 runtime calls are not protected by the
new gate. The [status ledger](../evaluation/status.md) records validation scope.

V2 configuration requires `access.invocations.<role>.tokens` for each configured
role, separately from `access.limits.tokens` for the cumulative run budget. API
invocations also require a micro-USD reservation ceiling. A role ceiling must fit
within the run budget and match the access profile's billing semantics. Missing
limits and mappings for unconfigured roles are rejected. The planner reserves its
declared per-invocation ceiling; it does not consume the whole run allocation.

An operator-selected policy binds one run identity, repository privacy class,
routes, access profiles and resource limits. A route separates runtime, provider,
model, effort, role, permission and access-profile hash. Repository content MUST
NOT supply the trusted policy. Planner, explorer and reviewer routes are read-only.
Writer and fixer capabilities remain subject to independent filesystem authority.

Access profiles distinguish billing kind from authentication. API profiles use a
credential reference. Subscription profiles select exactly one credential reference
or runtime authentication mode; a subscription authenticated with an API key does
not become API billing. Explicit class allowances admit PUBLIC, PRIVATE or
CONFIDENTIAL data. Profiles contain no credential values. Subscription monetary
usage MUST remain null; a subscription cannot satisfy a monetary ceiling with
unknown marginal cost. Monetary API values use integer micro-USD.

Access profile version 2 requires an explicit privacy declaration: training is
unknown, allowed or excluded; retention is unknown, zero, bounded by declared
days or provider-defined. An optional terms digest binds separately retained
evidence. These are operator declarations, not proof of provider compliance.
They change the profile identity and cannot be changed underneath a resumed
invocation. They do not implicitly grant a repository-class allowance. Version 1
profiles retain their original identities and carry no inferred privacy terms.

Version 2 profiles can additionally lower concurrent invocations within an access
policy journal. An omitted override inherits the run ceiling; a positive override
is enforced during durable replay as well as admission. Only a validated terminal
receipt releases its slot. This per-run/profile gate does not establish account-wide
rate limiting across independent runs or processes.

Invocation identity binds policy, route, exact input hash, attempt and reserved
resource ceilings. Input is bounded to 256 KiB of nonempty UTF-8. The access journal
stores hashes rather than raw input. Adapters MUST include all model-visible
context in the bound input; the helper cannot discover omitted hidden context.

`Dispatch` verifies input identity and appends a synced intent before invoking one
controller-owned adapter. Replay rebuilds cumulative resource reservations and
pending concurrency. Duplicate invocation IDs are rejected. Failed admission MUST
leave the journal unchanged. An error during append may leave an intent and MUST
NOT trigger an inference retry.

Terminal receipts bind invocation and route identity. Completion requires an output
hash; failed or cancelled invocations cannot admit output. Optional observed model
and provider values MUST match when available. Missing usage is unknown, not zero.
Known usage beyond a reservation is rejected and leaves the reservation unresolved.
Terminal admission releases concurrency once, without refunding cumulative ceilings.
Adapters must enforce ceilings before dispatch; receipt checks alone do not cap
provider spending or token generation.

`Reconcile` validates the recorded policy and exact pending invocation before a
read-only runtime observation. Its adapter MUST NOT create or resume model turns.
Unavailable observations keep the invocation unresolved. The journal rejects a
second terminal receipt even when readbacks race. Adapter errors are sanitized.

All identities use versioned, domain-separated canonical JSON hashes. These new
contracts do not reinterpret existing runtime journals. The ledger is scoped to
one policy journal, not an account-wide quota service. Trusted adapter behavior,
provider evidence, controller integration and process isolation are separate
obligations; the callback interface is not a sandbox.

Executed local tests cover policy and input substitution, privacy denial, missing
profiles, cumulative/concurrency limits, intent-before-callback ordering, unknown
invocation retry rejection, terminal receipt validation and read-only reconciliation
after a simulated lost response. Codex controller tests additionally cover
mixed fake-planner reservations, exact runtime terminal binding and crash-window
admission reporting. Full heterogeneous model execution remains NOT RUN.

The Codex controller uses a dedicated access journal and mirrors its intent and
terminal receipt into controller history. A crash between these writes leaves
the durable reservation active and visible in usage reporting. A local transport
failure or caller cancellation cannot release it: reconciliation requires an
observed terminal status from the exact runtime journal, source identity and
invocation. Completed output is bound to the same journal head and result hash
before the role can consume it. Generic controller append rejects access-terminal
events; the internal path records them only after validated runtime observation.

The provider-subcall journal, credential leases and outbound HTTPS transport now
compose as locally tested foundations for API mediation. The transport records
an intent before its single POST and records validated completion before exposing
response bytes. Redirects, ambient proxies and automatic retries are disabled.
An uncertain call remains pending. Controller and OpenCode execution integration
is still incomplete; these package tests are not evidence of live provider use.

Version 2 model contracts reserve the declared full context and output allowance
for every permitted call. API pricing uses operator-declared conservative input
and output rates, with checked arithmetic and per-call rounding. These bounds
depend on truthful provider limits, usage and prices; they are not an account-wide
spending guarantee. Subscription monetary usage remains unknown.

## General API model routing

API support is organized by protocol, not by a list of model names. A route binds
an operator-selected endpoint, model identifier, authentication reference,
protocol adapter, capabilities and resource limits. Adding a model on an already
supported protocol must require configuration rather than changes to the
orchestrator. Responses, Chat Completions and Anthropic Messages are distinct
protocol adapters; matching an endpoint URL or model name is not protocol proof.

The supported adapter set is finite: Responses, Chat Completions and Anthropic
Messages. A model on one of these protocols is configuration; a genuinely new
wire protocol requires an explicit implementation and qualification of its
adapter. This subsystem is not a universal inference gateway.

Runtime selection is independent of protocol selection. A `provider-api` route
uses the admitted HTTPS transport directly and does not launch OpenCode or a
loopback proxy. An `opencode-http` route uses OpenCode and its owned local provider
proxy because OpenCode is the selected runtime. The OpenCode provider-key prefix,
SDK configuration and schema projection apply only to that runtime path. Both
paths retain exact requested and observed model identity, selected access profile,
usage limits and durable receipts. Muse Spark remains an example route.

Capabilities declare whether a route supports tools, reasoning controls and
observable usage, together with its input/output limits. Workflow admission must
reject a missing required capability rather than invent tool support or silently
substitute another model. Provider-specific extensions remain inside the adapter
and its explicit configuration. Muse routes are verification examples, not the
architecture's model boundary. The implementation status of each protocol remains
separate from this target contract.

Each model may declare `supported_response_framings = ["json"]`, `["sse"]`,
or the sorted list `["json", "sse"]` in its capabilities. Omission preserves
legacy SSE-only behavior. A role selects `response_framing = "json"` or `"sse"`;
omission selects legacy SSE. The selected framing must be declared by the model
before admission. JSON-only models need not claim SSE support. An explicit role
selection remains part of request and runtime identity, including offline replay.
The historical adapter identifiers ending in `-sse-v1` remain unchanged for
journal compatibility; framing is an additional validated capability and request
selection. OpenCode currently requires SSE and rejects a JSON selection before
resources are created. Direct API routes can use either supported framing.

The legacy receipt field `stream_complete` denotes a completely buffered and
validated response for both framings. Its name does not assert that a JSON
response arrived as SSE. The request-expectation identity binds the selected
framing while old omitted fields preserve legacy identities.

The API runtime must bind both the configuration supplied at host launch and the
configuration observed from the running host. Their byte hashes may differ when
the runtime expands defaults; exact validated semantic fields establish their
relationship. Neither hash may be substituted for the other.

Provider response evidence retains the raw wire digest separately from its
semantic projection. Ordered tool identifiers, names and normalized argument
digests, together with output text digests, connect provider calls to the sealed
runtime generations. Missing native provider response IDs must not be invented.
Candidate execution additionally holds ownership of the exact acquired workspace
request for the full runtime lifetime; a matching run-lock pathname alone is
insufficient evidence of that request's identity.

The configured provider output ceiling is the final wire limit. A runtime SDK
may transform a visible output allowance before sending it: the pinned Anthropic
SDK adds an enabled thinking budget to that allowance. Runtime configuration must
derive a positive allowance by subtracting that budget from the wire ceiling,
retain both values in its identity, and validate the final outbound limit.
Thinking omitted, explicitly disabled, enabled and adaptive are distinct controls;
the adapter must not substitute one for another based on a model-name guess.

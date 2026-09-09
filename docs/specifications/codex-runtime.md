# Codex runtime lifecycle v1

Scope: `internal/codexruntime` adapts an initialized, caller-provisioned Codex App
Server connection to AgentRuntime. It owns one invocation journal and exposes
protocol execution and readback. Process provisioning, authenticated model access
and tool-isolation qualification are separate requirements, not implied by this
adapter's existence.

## Durable transitions

| Event | Required prior state | Evidence |
| --- | --- | --- |
| runtime.intent | empty journal | exact invocation and absolute workspace |
| runtime.thread | intent, no thread | observed thread ID, routing and policy |
| runtime.turn-intent | thread, no prior dispatch | exact invocation ID |
| runtime.turn | pending dispatch, no turn ID | provider turn ID |
| runtime.turn-status | recorded turn | observed lifecycle status |
| runtime.result | same completed turn | exact turn ID and admitted runtime result |

The adapter holds an exclusive execution marker across the operation. A crash
leaves a marker for explicit inspection; the adapter never steals it. Journal
transitions independently reject duplicate starts. Thread creation intent is
written before `thread/start`; turn intent is written before `turn/start`.
`clientUserMessageId` correlates the request and is not an idempotency guarantee.

An uncertain dispatch with no returned handle cannot be resubmitted. Resume with
the exact invocation ID either returns its already admitted result or performs
`thread/read` for the recorded thread and turn. Readback validates model, provider,
workspace and available effort against recorded settings. Active, failed,
interrupted or missing turns cannot be promoted to a successful result. Observed
terminal states remain in the journal even when output admission fails.

## Authority and output

Thread and turn requests select the requested model and effort, read-only tools,
disabled tool network and approval policy `never`. Thread readback must match.
Both also explicitly disable native execution environments through the installed
experimental protocol; see [host admission](codex-host.md).
These are observed provider settings, not certified operating-system boundaries.
Planner threads also register the two [source tools](source-reads.md). Only
`item/tool/call` reaches the immutable scoped callback; other server requests
remain denied. The private handled marker cannot be supplied by provider JSON.
The thread config sets `features.code_mode.enabled=false` and
`features.code_mode.direct_only_tool_namespaces=["functions"]`: the tested
model's metadata selects `code_mode_only` independently of feature flags, so
the explicit direct-only override is needed to expose the broker tools while
native execution remains disabled. The authenticated source fixture exercised
this combination on the pinned installed executable.
The explanation is supported by upstream
[tool-mode selection](https://github.com/openai/codex/blob/ad931a45b201e3877d6ba542ba5dbbd85e7e31b4/codex-rs/core/src/tools/mod.rs)
and [direct-only exposure](https://github.com/openai/codex/blob/ad931a45b201e3877d6ba542ba5dbbd85e7e31b4/codex-rs/core/src/tools/spec_plan.rs).
That source revision is supporting evidence, not a claim that it produced the
installed binary; the executed fixture is the installed-behavior evidence.
The caller must prevent inherited connectors and qualify server configuration
before using real repositories. Native provider tools are not mediated by this
transport, so refusing protocol approval requests alone does not prove isolation.

The adapter admits assistant final-answer items from a full completed turn.
When completion supplies only `itemsView=summary`, it records the terminal status
and reads the exact existing turn before admitting output. It sends no new turn.
Commentary and tool output are excluded; duplicate item IDs and incomplete output
are rejected. Unknown usage remains null. Model rerouting and mismatched completion
identities stop admission. One streamed execution permits at most 8,192 events and
16 MiB of payload, in addition to transport bounds.

Execute supports context cancellation by closing the transport and preserving
uncertainty. Targeted asynchronous Cancel is not implemented and its capability
is false. The controller's planning command now uses the configured runtime.
It records host intent before preparation, verifies the prepared host, records
observed startup controls and links the completed runtime journal/result to the
plan. Codex plans without those receipts are rejected during replay. State roots
inside the source repository are rejected. See the [planning guide](../guides/codex-planning.md).


## Observed usage and explicit unlimited tokens

The app-server adapter journals `thread/tokenUsage/updated` before normalization.
Usage is the cumulative high-water snapshot minus the exact pre-turn baseline,
not the sum of notification snapshots or `last` values. Cached input is a subset
of input; reasoning output is a subset of output. Missing optional counters are
unknown, including when a previous snapshot or baseline knew that component.

A finite reservation is an observed threshold, not an exact provider token cap.
At or above the reservation the adapter persists `BUDGET_EXHAUSTED`, attempts an
interrupt and admits no further output/tool response. Overshoot is reported;
provider termination remains UNKNOWN without terminal evidence.

To explicitly disable token ceilings for a run and its configured roles:

```toml
[codex]
require_live_usage = true
usage_qualified = true # operator assertion bound to real preflight evidence

[access.limits]
tokens = 0
unlimited_tokens = true
concurrency = 1

[access.invocations.planner]
tokens = 0
unlimited_tokens = true

[access.invocations.writer]
tokens = 0
unlimited_tokens = true

[access.invocations.fixer]
tokens = 0
unlimited_tokens = true

[access.invocations.reviewer]
tokens = 0
unlimited_tokens = true
```

These entries extend an otherwise complete explicit configuration. Unlimited
requires the boolean flag and zero numeric reservation; zero alone is invalid.
An unlimited role cannot be admitted under a finite run token ceiling. Token
usage is still journaled and normalized. This option changes neither concurrency,
monetary constraints, privacy/route checks, file-effect approvals nor verification.
Unknown cost is not zero cost. The operator must authorize an unlimited run;
prior finite-run receipts are never rewritten when creating the new configuration.

# Codex App Server protocol inspection

Inspected 2026-09-06. The installed `codex-cli 0.153.4` executable has SHA-256
`e5aa76d19c7c94e2e9ef9b707d590206a73ac0e97c8ddc8382181242494bef75`.
Its `app-server generate-json-schema` command successfully generated a local
protocol bundle in `.local/codex-schema-0.153.4`. This proves schema generation,
not a model invocation or permission-boundary qualification.

The [official App Server documentation](https://learn.chatgpt.com/docs/app-server)
describes stdio request/response messages with IDs, notifications without IDs,
and the `initialize` / `initialized` handshake before thread and turn operations.
Generated schemas describe the installed CLI version. The local bundle, rather
than an assumed protocol version, is the adapter's implementation reference.

## Observed schema boundaries

| Message | Fields relevant to the adapter |
| --- | --- |
| ThreadStartParams | model, modelProvider, cwd, sandbox, approvalPolicy, config, ephemeral |
| ThreadStartResponse | required model, modelProvider, cwd, sandbox, approvalPolicy, thread; nullable reasoningEffort |
| TurnStartParams | required threadId and input; optional model, effort, outputSchema, sandboxPolicy |
| TurnStartResponse | required turn; no model identity in this response alone |

## Implementation decisions

The Go adapter will launch its own stdio process and explicitly select the
requested model/provider/effort. It must bind observed thread settings and turn
identities to the harness invocation. Missing observed effort remains unknown;
the adapter must not copy requested settings into evidence fields.

Provider traffic is an external protocol, distinct from the harness's canonical
v1 contracts. Decode bounded envelopes, validate consumed semantic fields, and
translate into strict harness records. Do not demand that all provider payloads
obey our canonical integer-only JSON rules. Unknown approval/tool requests grant
no authority. Disconnection after dispatch is uncertain and cannot trigger blind
retry. Resume and cancellation must use recorded thread/turn identities.

The Go `internal/codexrpc` transport now implements bounded serial requests,
notifications and initialization. It rejects duplicate keys, excessive depth,
ambiguous envelopes and mismatched response IDs. Each message is limited to
1 MiB; one call retains at most 256 intervening events and 4 MiB of payload.
Cancellation closes the owned stream; transport errors never trigger retries.
Server requests receive an unsupported-method error and remain in returned
evidence. This is not tool execution authority.

PASS: scripted-peer tests exercise approval denial, bounded messages, response
mismatch and cancellation while blocked on reading or writing. The opt-in
`TestInstalledServerHandshake` also passed against the executable identified
above, with a fresh temporary configuration home and no credentials copied.
Observed user agent: `engorch/0.153.4 (Windows 10.0.26200; x86_64)`.

The AgentRuntime protocol adapter now implements journaled thread/turn execution
and read-only continuation lookup, tested with a scripted peer. Before real repository work,
qualify disabled inherited tools, process lifetime, explicit permissions, model
observation and lost-turn handling. The installed-server test dispatched only
initialize/initialized; it proves neither model access nor tool isolation.

## Host admission follow-up

A separate host-admission test also creates a local thread with empty native
environments; it dispatches no model turn. Effective configuration reads confirm
26 disabled features, host skill-discovery suppression and an empty MCP inventory.
The full feature policy is in `internal/codexhost/policy.go`.

The installed notification schema includes optional integer `emittedAtMs` on
server notifications. Live startup exposed this field; transport validation now
accepts it only in notification envelopes and rejects null or invalid values.

The inspected [upstream tool-plan source](https://github.com/openai/codex/blob/ad931a45b201e3877d6ba542ba5dbbd85e7e31b4/codex-rs/core/src/tools/spec_plan.rs)
gates shell tools on environment access and ShellTool, and apply-patch on
environment access. It also explains why ordinary configuration cannot turn off
UnifiedExec. This is supporting source evidence, not a claim that this revision
exactly matches the installed desktop executable. The local experimental schema
independently documents empty-environment semantics.

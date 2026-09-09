# OSS reuse decisions

Status: initial exact-revision donor audit on 2026-09-08. This is a decision
record, not dependency installation or integration qualification. New candidates'
remote default heads were observed with `git ls-remote`; already adapted donors
remain bound to the exact retained source revisions, and installed dependencies
are recorded at their release tag/module identity. Repository and license files
were inspected at those immutable revisions. A future dependency change must pin
a released version and lockfile identity, retain all required notices, and
receive its own compatibility and behavior tests.

The selection order is **dependency > thin adapter > bounded fork >
reimplementation**. Moving right requires a recorded incompatibility with
EngOrch's deterministic identity, provenance, authority, security, or bounded
execution rules. Popularity or implementation language alone is not sufficient.

| Donor and inspected revision | License evidence | Current EngOrch state | Decision | Required boundary or blocker |
| --- | --- | --- | --- | --- |
| [microsoft/tgrep `e2007b52d2b8fe4176159d0da20c9ba4a46d5aab`](https://github.com/microsoft/tgrep/tree/e2007b52d2b8fe4176159d0da20c9ba4a46d5aab) | [MIT](https://github.com/microsoft/tgrep/blob/e2007b52d2b8fe4176159d0da20c9ba4a46d5aab/LICENSE) | **Adopted as an exact Git dependency.** `crates/ri/Cargo.toml` and `Cargo.lock` pin the same revision. `lexical.rs`, `lexical_index.rs` and `lexical_disk.rs` call `tgrep_core` query planning, `LiveIndex`, posting lookup, `IndexReader` and `append_overlay_to_index`. | **Dependency**, retained. | EngOrch's code supplies admitted immutable bytes, snapshot/provenance identities, deletion/replacement overlay semantics, result verification and bounds. It must not grow a second trigram planner, posting format, mmap reader or lexical engine. The current wrapper is broader than a one-function adapter because it owns those project-specific contracts; performance and zero-false-negative claims remain limited to executed fixtures. |
| [scip-code/scip (formerly sourcegraph/scip) `1c2b6db7e560d5233c944f36e4ac1377cc6963fc`](https://github.com/scip-code/scip/tree/1c2b6db7e560d5233c944f36e4ac1377cc6963fc) | [Apache-2.0](https://github.com/scip-code/scip/blob/1c2b6db7e560d5233c944f36e4ac1377cc6963fc/LICENSE) | **Adopted as the semantic interchange.** The unmodified pinned `scip.proto` is retained under `third_party/scip`; `build.rs` generates Rust bindings with `prost`, and RI admits producer files instead of implementing language indexers. | **Standard plus generated bindings**, retained. | Producer identity, committed-source hashes, coverage, bounds and immutable publication remain EngOrch responsibilities. SCIP data does not prove producer completeness. Add language producers as external tools; do not create per-language semantic parsers in core. |
| [GitoxideLabs/gitoxide `283937b39df72dfcf87d2f4b25748b6fc4432d29`](https://github.com/GitoxideLabs/gitoxide/tree/283937b39df72dfcf87d2f4b25748b6fc4432d29) | [MIT OR Apache-2.0](https://github.com/GitoxideLabs/gitoxide/blob/283937b39df72dfcf87d2f4b25748b6fc4432d29/gix/Cargo.toml) | **Not present.** Rust RI receives admitted manifests and bytes. Go currently invokes real Git with fixed argv for tree/blob reads and worktree/effect behavior. | **Required dependency for a future Rust-side Git object-read seam; implementation pending.** | Adding `gix` before that seam exists would be dependency-for-show. First define and benchmark the seam and verify SHA-1/SHA-256, alternates, replace-object denial, filters, submodules and path-byte behavior. Keep worktree mutation and Git effects on the real Git CLI unless evidence shows equivalent semantics. |
| [agentskills/agentskills `69ef37e9424c0a7ea9dd2293b559e43ec8176379`](https://github.com/agentskills/agentskills/tree/69ef37e9424c0a7ea9dd2293b559e43ec8176379) | Repository code/specification: [Apache-2.0; documentation: CC-BY-4.0](https://github.com/agentskills/agentskills/blob/69ef37e9424c0a7ea9dd2293b559e43ec8176379/README.md) | **Adopted.** The seven `procedures/*/SKILL.md` directories use the standard directory and progressive-disclosure shape. | **Standard**, retained; no framework dependency. | Skills remain advisory content. Loading a skill cannot grant routing, credentials, repository access or effect authority, and EngOrch does not need a proprietary registry/router protocol. Preserve attribution if normative documentation is copied rather than merely implemented. |
| [agentclientprotocol/agent-client-protocol `6b1bb2e6b2b327f977004ef831f1ec67afcb269d`](https://github.com/agentclientprotocol/agent-client-protocol/tree/6b1bb2e6b2b327f977004ef831f1ec67afcb269d) | [Apache-2.0](https://github.com/agentclientprotocol/agent-client-protocol/blob/6b1bb2e6b2b327f977004ef831f1ec67afcb269d/LICENSE) | **Not implemented.** Existing Codex App Server and OpenCode integrations are runtime-specific. | **Required thin runtime adapter**, pending implementation, using an official ACP SDK when the ACP route is introduced. | Negotiate the stable wire protocol and capabilities; do not infer wire compatibility from SDK artifact versions. The adapter must translate sessions, cancellation, permission requests and updates into existing controller state without granting the agent direct effect authority. Do not invent a universal agent protocol. |
| [OpenHands/software-agent-sdk `df2ea8fa5542d5d2a543e108bc8b2d4fbbab34b1`](https://github.com/OpenHands/software-agent-sdk/tree/df2ea8fa5542d5d2a543e108bc8b2d4fbbab34b1) (the earlier product-repository inspection was `OpenHands/OpenHands` `f7fb0c4b21f5ed726edbba8a6309634ef434b004`) | SDK and Agent Server [MIT](https://github.com/OpenHands/software-agent-sdk/blob/df2ea8fa5542d5d2a543e108bc8b2d4fbbab34b1/LICENSE); the separate product repository identifies additional restrictions under its `enterprise/` subtree | **Not present. Source/API feasibility inspected; nothing executed.** | **Conditional thin adapter to an optional external worker environment.** Do not fork or make it a core dependency. | The exact SDK supports an empty native/default-tool list and client-owned tools without server executors, so controller mediation is structurally possible. Credential and cancellation semantics do not yet meet the EngOrch boundary by themselves; see the bounded experiment below. Reject integration unless an executed spike proves the stricter profile. |
| [openai/symphony `8001b52e3062495a16e520e4ceaf8f9de868c4d0`](https://github.com/openai/symphony/tree/8001b52e3062495a16e520e4ceaf8f9de868c4d0) | [Apache-2.0](https://github.com/openai/symphony/blob/8001b52e3062495a16e520e4ceaf8f9de868c4d0/LICENSE) | **Research reference only.** | **Bounded design reuse / reimplementation**, not a runtime dependency or broad fork. | Reuse the specification's separation of tracker, scheduler, workspace and runner plus bounded concurrency, backoff and stalled-worker reconciliation. EngOrch has durable run/effect identities and explicit UNKNOWN handling, while Symphony targets tracker polling and permits agent-owned provider tools. Port only mechanisms whose retry and authority semantics can be restated against EngOrch invariants. |
| [google/go-github `v89.0.0` / `8af9bb1a8be4093865855a6919866a5282406ad9`](https://github.com/google/go-github/tree/8af9bb1a8be4093865855a6919866a5282406ad9) (default head inspected separately at `3a439fa10f3dd40b3fa9093bf72b0dee11f510c4`) | [BSD-style license](https://github.com/google/go-github/blob/8af9bb1a8be4093865855a6919866a5282406ad9/LICENSE) | **Implementation underway in the shared tree.** `go.mod` currently carries direct `github.com/google/go-github/v89 v89.0.0`; `internal/draftpr` imports it and constructs a client over EngOrch's bounded HTTP transport. This donor audit did not add or qualify it. | **Dependency plus thin effect adapter**, pending completion and executed qualification. | Use the library for endpoint models, pagination and typed rate-limit errors. Keep credential attachment, exact intent/receipt identity, single-attempt mutation, read-back and UNKNOWN reconciliation in EngOrch. Disable convenience behavior that sleeps or retries implicitly. Migration tests must compare exact request/error behavior before the bounded HTTP path is considered replaced. |
| [open-telemetry/opentelemetry-go `v1.46.0` / `58db4c898f5b5594f8ba78f156475bf48486e2f2`](https://github.com/open-telemetry/opentelemetry-go/tree/v1.46.0) | [Apache-2.0, with bundled notices in the license file](https://github.com/open-telemetry/opentelemetry-go/blob/v1.46.0/LICENSE) | **Adopted as direct API, SDK, and OTLP/HTTP exporter dependencies.** `internal/telemetry` supplies bounded command spans and `cmd/harness` owns startup and shutdown. | **Dependency plus thin instrumentation**, retained. | Canonical journals remain authority and audit evidence; spans are derived diagnostics. Empty configuration creates no exporter. The one explicit endpoint rejects embedded credentials and query data; the exporter disables ambient proxies and retries. Span attributes use a fixed command set and terminal class, excluding arguments, paths, run IDs, prompts, source, credentials, and error text. |
| [modernc.org/sqlite `v1.58.0` / `722282f38b49191a4e24569eeac960bc033bd8f0`](https://gitlab.com/cznic/sqlite/-/tree/722282f38b49191a4e24569eeac960bc033bd8f0) | Driver [BSD-3-Clause](https://gitlab.com/cznic/sqlite/-/blob/722282f38b49191a4e24569eeac960bc033bd8f0/LICENSE); bundled SQLite and extension notices are separate | **Implementation underway in the shared tree.** `go.mod` now carries direct `modernc.org/sqlite v1.58.0`; `internal/journal/sqlite.go` opens the driver and sibling tests exercise the new backend. This audit neither changed the dependency nor qualifies the separately owned migration. | **Direct pure-Go dependency plus thin journal storage adapter**, pending owner qualification. | The migration must preserve canonical event bytes, hash-chain verification, append ordering, crash/lock behavior, export compatibility and read-only inspection. Audit its pinned `modernc.org/libc`, supported platforms and all bundled notices. Do not treat SQLite transactions as a substitute for EngOrch's event and effect semantics. |
| [oraios/serena `9f9db76622340930d66aba9f72a2349b30bb1e29`](https://github.com/oraios/serena/tree/9f9db76622340930d66aba9f72a2349b30bb1e29) | [MIT](https://github.com/oraios/serena/blob/9f9db76622340930d66aba9f72a2349b30bb1e29/LICENSE) | **Research reference only.** The retained audit inspected [`symbol_tools.py`](https://github.com/oraios/serena/blob/9f9db76622340930d66aba9f72a2349b30bb1e29/src/serena/tools/symbol_tools.py); no Serena code or runtime is integrated. | **Borrow bounded query vocabulary; optional external producer adapter only.** | `find symbol`, references, declaration, path scope, depth and body inclusion are useful agent-facing operations. Implement that vocabulary over RI's immutable evidence. A future Serena adapter must label producer identity and unknown/partial coverage; it cannot grant edit/shell authority or upgrade LSP observation to canonical repository truth. |
| [Graphify-Labs/graphify `c9f99018774e2e0380e9f65b3959944559a0d5f6`](https://github.com/Graphify-Labs/graphify/tree/c9f99018774e2e0380e9f65b3959944559a0d5f6) | [Apache-2.0](https://github.com/Graphify-Labs/graphify/blob/c9f99018774e2e0380e9f65b3959944559a0d5f6/LICENSE), retained legacy [MIT](https://github.com/Graphify-Labs/graphify/blob/c9f99018774e2e0380e9f65b3959944559a0d5f6/LICENSE-MIT), and [NOTICE](https://github.com/Graphify-Labs/graphify/blob/c9f99018774e2e0380e9f65b3959944559a0d5f6/NOTICE) | **Bounded adaptation already present.** `crates/ri/src/structural.rs` adapts the pinned Rust type-expression walker; `third_party/graphify` and repository notices retain provenance, licenses and changes. Existing evaluation reports executed grammar/unit/subprocess coverage, limited to structural type occurrences with partial semantics. | **Bounded fork/port retained; borrow presentation concepts, not another graph engine.** | Keep iterative traversal bounds, exact byte ranges, qualified spelling, unresolved references and partial coverage. Human extracted/inferred presentation may render RI provenance, but Graphify ranking, heuristic resolution or viewer state cannot become task truth. Any frontend source adaptation needs a separate exact-source/license record. |
| [colbymchenry/codegraph `b9ca4b7981116909900368cc1686a1074cd4d4c1`](https://github.com/colbymchenry/codegraph/tree/b9ca4b7981116909900368cc1686a1074cd4d4c1) | [MIT](https://github.com/colbymchenry/codegraph/blob/b9ca4b7981116909900368cc1686a1074cd4d4c1/LICENSE) | **Bounded adaptation already present.** `crates/ri/src/identifiers.rs` ports `splitIdentifierSegments`; `third_party/codegraph`, `NOTICE` and `THIRD_PARTY.md` retain exact provenance and changes. | **Bounded fork/port retained; borrow install/init/status ergonomics only.** | The Rust port adds explicit byte bounds and preserves short/numeric/later Unicode segments. Do not adopt the donor's prompt gate, stopword/relevance authority, mutable synchronization model or another graph store. CLI vocabulary may present exact snapshot/producer health without implying semantic completeness. |
| [Aider-AI/aider `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`](https://github.com/Aider-AI/aider/tree/5dc9490bb35f9729ef2c95d00a19ccd30c26339c) | [Apache-2.0](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/LICENSE.txt) | **Research reference only.** The retained audit inspected [`aider/repomap.py`](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/repomap.py); no RepoMap code is adopted. | **Reimplement only a compact orientation view over RI evidence.** | Token-budgeted tree/symbol presentation is useful. Use EngOrch's already admitted lexical/SCIP/structural facts and explicit coverage; PageRank, refresh/cache behavior or heuristic relevance must not select authority or replace direct source retrieval. A Python/Aider runtime dependency is unjustified for this view. |

## OpenHands bounded feasibility experiment

**Method and evidence status:** inspection only. The official
`OpenHands/software-agent-sdk` repository was fetched and checked out at exact
revision `df2ea8fa5542d5d2a543e108bc8b2d4fbbab34b1`. Its source and Agent Server API
were inspected. No SDK package was installed, no server or sandbox was started,
no conversation/tool action was executed, and no credential was supplied. The
result is therefore a feasibility decision, not runtime or isolation proof.

- **Shell/filesystem removal is structurally feasible.** [`AgentBase.tools`](https://github.com/OpenHands/software-agent-sdk/blob/df2ea8fa5542d5d2a543e108bc8b2d4fbbab34b1/openhands-sdk/openhands/sdk/agent/base.py#L125-L167)
  defaults to an empty explicit list, and `include_default_tools=[]` disables
  even the default Finish/Think tools. Terminal and FileEditor are examples,
  not mandatory tools. Plugins, MCP tools and dynamically added runtime tools
  are separate surfaces and must also be disabled or allowlisted.
- **Controller-mediated effects have a concrete seam.** Local and remote
  conversations accept `client_tools`; the [local implementation](https://github.com/OpenHands/software-agent-sdk/blob/df2ea8fa5542d5d2a543e108bc8b2d4fbbab34b1/openhands-sdk/openhands/sdk/conversation/impl/local_conversation.py#L284-L364)
  says their executor only acknowledges while a callback/consumer handles the
  emitted action, and the [remote implementation](https://github.com/OpenHands/software-agent-sdk/blob/df2ea8fa5542d5d2a543e108bc8b2d4fbbab34b1/openhands-sdk/openhands/sdk/conversation/impl/remote_conversation.py#L759-L762)
  states there is no server-side executor. An EngOrch spike can expose only
  proposal tools and turn each action into a separately admitted controller
  intent. OpenHands confirmation policy is useful defense in depth, but cannot
  replace that intent/approval/receipt boundary.
- **Credential isolation is not proven and has a concrete blocker.** The
  [`SecretRegistry`](https://github.com/OpenHands/software-agent-sdk/blob/df2ea8fa5542d5d2a543e108bc8b2d4fbbab34b1/openhands-sdk/openhands/sdk/conversation/secret_registry.py#L118-L163)
  can inject a secret when its name appears in a command, while its ACP path can
  resolve the entire registry and explicitly records least-privilege scoping as
  deferred work. EngOrch must give the worker no GitHub/effect credential and
  broker model access outside the sandbox where possible. Any unavoidable
  runtime credential must be single-purpose, short-lived and bound to that
  sandbox; source-level masking is not proof the model or process cannot reveal
  it.
- **Cancellation exists but is not provider/effect confirmation.** The Agent
  Server [`interrupt`](https://github.com/OpenHands/software-agent-sdk/blob/df2ea8fa5542d5d2a543e108bc8b2d4fbbab34b1/openhands-agent-server/openhands/agent_server/event_service.py#L1645-L1671)
  requests cancellation and waits up to five seconds; its own comment preserves
  the possibility that the run task is still active after timeout. Tool
  cancellation is cooperative. EngOrch must retain `requested`, `confirmed`,
  and `unknown` states, inspect terminal status, and verify workspace cleanup
  before releasing a lease.

The inspected API makes a narrow optional backend plausible: exact pinned
server image, empty native/default/plugin/MCP tool sets, controller-owned client
tools only, no effect credentials, exact conversation/workspace identity,
bounded interrupt and terminal/cleanup read-back. The next step is an executed
local disposable-sandbox spike with negative tests proving terminal/file tools
cannot run and an interrupted tool cannot mutate after the lease is released.
Until that succeeds, OpenHands remains **CONDITIONAL / NOT RUN**.

## Architecture limit

EngOrch owns orchestration, access policy, context composition, deterministic
state, effect intent/receipt semantics, UNKNOWN reconciliation and verification.
Coding-agent loops remain inside Codex App Server, OpenCode or future ACP
runtimes. Direct Responses, Chat Completions and Anthropic transports are only
for controller-owned bounded inference where an agent runtime is unnecessary;
they must not grow shell/file tools or become another coding-agent runtime.

The finite native transport set is not a universal inference gateway. An
operator may configure a compatible external endpoint, but EngOrch does not
absorb provider discovery, cross-provider fallback, pricing catalogs or dozens
of provider-specific adapters. New infrastructure proposals must update this
ledger before implementation and identify the exact dependency or the concrete
reason a thin adapter, bounded fork or reimplementation is required.

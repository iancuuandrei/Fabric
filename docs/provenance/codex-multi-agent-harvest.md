# OpenAI Codex Multi-Agent V2 donor audit

Status: source-only audit on 2026-09-08. No source from this donor has been
copied, translated, compiled into EngOrch, installed, or executed. Consequently, **PORTED:
none**. Every candidate below is either an independently restated contract or
an implementation reference that still needs an EngOrch-specific design and
tests.

## Source identity and method

The donor is the official [`openai/codex`](https://github.com/openai/codex)
repository, inspected at default-head commit
[`8e694e955ae02ca737230a5468c55d5847074072`](https://github.com/openai/codex/tree/8e694e955ae02ca737230a5468c55d5847074072)
(`Exclude base instructions from the bundled model catalog (#43604)`, authored
2026-09-07T21:29:11Z and committed 2026-09-07T22:11:52Z). The revision was
resolved with `git ls-remote`, checked out detached under `.local/donors`, and
remained clean. Files were read and SHA-256 hashed locally. No Cargo command,
test, binary, build script, or donor-generated program was run.

The repository root [`LICENSE`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/LICENSE)
is Apache License 2.0; its inspected SHA-256 is
`aa5e89edcbbd01fc3fb188a527d8bdc0da5812305cab220c84348c14ea427288`.
If implementation is later copied or translated, that change must retain the
required Apache-2.0 notices and identify the copied material and modifications.
No donor implementation is redistributed by this source-only audit.

## What is actually public at this revision

Multi-Agent V2 is real public code at the pinned revision. It is not a single
reusable crate or library. Its behavior spans `codex-protocol`, `codex-core`,
the thread/history stores, session input queues, model/config selection, and a
SQLite-backed graph adapter. Importing `codex-rs` wholesale would therefore
pull in a coding-agent runtime and authority model that EngOrch does not need.

| Area | Exact donor source and SHA-256 | Observed contract |
| --- | --- | --- |
| Canonical agent names | [`agent_path.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/protocol/src/agent_path.rs) — `394520721c3fc33a72d8f68762a755102a5f0c042e8d295eae7aee74a167c330` | `AgentPath` accepts `/root`, descendants with lowercase/digit/underscore segments, and the special `/morpheus`; it rejects empty, trailing-slash, dot, dot-dot, reserved `root`, and slash-containing names. Relative resolution appends to the caller path; absolute references are revalidated. Unit tests cover join, relative/absolute resolution, and rejection. |
| Session-scoped control and logical registry | [`control.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/agent/control.rs) — `0ebde79b0ec3bef3befc8e9a08fc04d87f1f435fa45112999f44e77ae88adc7e`; [`registry.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/agent/registry.rs) — `44d36878414abf9d305e1d894039b1102895084d144010cae6f2750458d81332` | One `AgentControl` is shared by a root session tree. The registry uses path-to-metadata and thread-to-path maps rather than a node tree. Paths and spawn capacity are reserved before creation and released by a drop guard on failure; commit binds the thread. Listings are sorted and prefix filtering respects segment boundaries. |
| Persisted topology | [`store.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/agent-graph-store/src/store.rs) — `64d8a296c244a989209ecc349506bb5b6f052091e8e43f72ec4a583b391fad62`; [`types.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/agent-graph-store/src/types.rs) — `e3fe5d64cd79dc324078450ef15c2b1cebd251bf0cdf269e55e488dc357c7da4`; [`local.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/agent-graph-store/src/local.rs) — `208b660aecc7ae1db9aee4a543dbad9f08d811ed6dc90e67349478c855a899f7` | A storage-neutral interface persists directional parent/child thread edges as `open` or `closed`. Direct children are stable-ordered. Descendant traversal is breadth-first by depth then thread ID; a status filter constrains every traversed edge, so an open descendant below a closed edge is excluded from an open-only walk. The local adapter delegates to the existing state SQLite runtime. |
| Spawn and fork modes | [`multi_agents_v2/spawn.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/tools/handlers/multi_agents_v2/spawn.rs) — `4a50b9e086455e7ee4f4d71e87ded7e7c024bebde76186abf87707fbd7f260b7`; [`control/spawn.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/agent/control/spawn.rs) — `bc927689b80ee499765595a0972137936096b0b0ff72efe0725a9ec9c2e2d91c` | V2 accepts `fork_turns` as case-insensitive `none`, `all`, or a positive decimal integer string; absent/blank defaults to `all`; zero and malformed values fail; legacy `fork_context` fails. `none` creates no fork mode, `all` uses full history, and N truncates at fork-turn boundaries. Fork history is sanitized for child context and authorization rather than copied blindly. |
| Message and follow-up | [`message_tool.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/tools/handlers/multi_agents_v2/message_tool.rs) — `f5b54494530d463fa6df197404675349564bc332ac66471c324b12b95bc1636d`; wrappers [`send_message.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/tools/handlers/multi_agents_v2/send_message.rs) — `f5d2001d6d7ef6fff5a3d286d89f8fc3ad653b8e620abd606deae82956a56aa5` and [`followup_task.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/tools/handlers/multi_agents_v2/followup_task.rs) — `f3f27623d31c4287cf5a0a00b802becfc51c4bea2fb46c526054382644a240d9` | Both reject blank content and use one delivery path. `send_message` queues only; `followup_task` sets `trigger_turn`, performs execution-capacity checking, and cannot target root. The communication records author, receiver, kind, and parent/root turn context. |
| Wait, list, and interrupt | [`wait.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/tools/handlers/multi_agents_v2/wait.rs) — `a7386f7d8f6a5bde3e371fe8340f40d33a317630cc61af3352f82108a6b96f19`; [`list_agents.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/tools/handlers/multi_agents_v2/list_agents.rs) — `9cfad2139762d923d50fae5cea380606f6adc92a3e3296f35dc40d89fea111cd`; [`interrupt_agent.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/tools/handlers/multi_agents_v2/interrupt_agent.rs) — `8fc32713840c49c80baf27bdf8a7c305856873dbfeec900a87cdd7b03869339e` | Public V2 `wait_agent` waits for session mailbox activity or new input, or times out; it takes only `timeout_ms`, clamps low values, and rejects values above the configured maximum. `list_agents` supports a relative or absolute path prefix. Interrupt rejects root and self; already-missing/dead targets are treated idempotently. |
| Active execution and residency limits | [`execution.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/agent/control/execution.rs) — `18f1ef998bead9b1728ad48b18d609d59d1561f73ba81c044f7e57e8ad0cd297`; [`residency.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/agent/control/residency.rs) — `a19e1a44d7d3274075680fbb96870543b25e2b46f520d806c116ccc60774b7b9` | V2 separately limits active subagent turns and loaded resident subagent threads. Drop guards release counts. Residency tracks pending slots, evicts only inactive completed/errored/interrupted agents with empty mailboxes, materializes their rollout, shuts them down, saves environments, and then removes them. The default total session concurrency is 4, translated to 3 non-root slots. |
| Lifecycle and tree budget | [`status.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/agent/status.rs) — `7d395428bc9af7c2604f01ffef321a6aec5bcff9f9a2d7b18eff13b36e82d8c1`; [`rollout_budget.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/rollout_budget.rs) — `4f23c56c3d9a4e338c3743e921767c4a39a9fe6fb474eedb6b34e6dde7d91835` | Turn events derive pending/running/completed/errored/interrupted/shutdown status. `Interrupted` is deliberately non-final for completion notification. A shared root-tree rollout budget accounts provider units when present, otherwise weighted output and non-cached input tokens; it validates finite non-negative units and delivers threshold reminders per thread/window. |

The broad handler test file
[`multi_agents_tests.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/tools/handlers/multi_agents_tests.rs)
has SHA-256
`67a256f9c227aec6a897dfa4133dec2326f880c0db17eb358e916d52b1f5eac7`.
It covers path resolution, sorted/prefix-filtered listings, omission of closed
agents, retention of interrupted resident agents, message/follow-up behavior,
wait wake-up, interrupts, concurrency, and fork modes. Registry reservation and
release tests are in
[`registry_tests.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/agent/registry_tests.rs)
(`9a3d151a5873e53d2625abac4e6ce327c8e410c02d5cf0db77365d182e65cb35`);
execution-limiter tests are in
[`execution_tests.rs`](https://github.com/openai/codex/blob/8e694e955ae02ca737230a5468c55d5847074072/codex-rs/core/src/agent/control/execution_tests.rs)
(`ed188d70ebb9f2bfda61f8e473eba02b51295a57f4ee523bb6f0f93efa009aa0`).
These tests were inspected, not run.

## Negative findings and compatibility boundaries

- The checked public code does **not** expose the same `wait_agent` contract as
  the currently observed Codex collaboration tool. There is no target array or
  per-target cursor in `WaitArgs`; public V2 wait is a session mailbox wait.
  EngOrch must not infer a target-wait protocol from the host tool description.
- `agents.max_depth` is documented as V1-only and ignored by V2. The donor test
  `multi_agent_v2_spawn_agent_ignores_configured_max_depth` proves this is
  deliberate. Hierarchical EngOrch depth limits must remain an EngOrch policy.
- `MAX_ENVIRONMENT_SUBAGENTS = 8` bounds only model-visible environment text;
  it is not a spawn or concurrency limit. `MAX_SPAWN_AGENT_MODEL_OVERRIDES = 5`
  bounds model choices in a tool description; it is not an agent limit.
- The V2 spawn schema at this revision has no per-child `token_budget`. The
  shared `RolloutBudget` is tree-level usage accounting/reminders, not proof of
  the host's goal-loop or per-generation budget semantics.
- The registry is in-memory identity metadata, while persisted graph state is
  a separate thread-edge store. Neither is a durable EngOrch authorization,
  effect, lease, or journal record.
- The execution limiter checks capacity and later increments through a guard;
  it is not one atomic reservation operation. EngOrch should reuse the
  reservation-guard idea, not port this implementation verbatim.
- Donor completion notifications, interrupt idempotence, and residency unload
  are runtime-control behavior. They do not prove provider termination, effect
  non-occurrence, workspace cleanup, or a terminal controller receipt.

## Finite reuse decision

| Candidate | Classification now | EngOrch boundary for a later implementation |
| --- | --- | --- |
| Canonical `AgentPath` value and target resolution | **INSPIRED / finite port candidate** | Reimplement the small grammar and table tests in Go. Decide explicitly whether `/morpheus` belongs in EngOrch; otherwise omit it. Add `Parent` and segment-aware prefix operations rather than manipulating raw strings at call sites. |
| Atomic spawn reservation with commit/drop release | **INSPIRED / highest-value port candidate** | One locked or CAS-backed transaction must reserve both capacity and canonical path, then commit a durable child identity or release on every failure. Bind it to EngOrch run/session IDs and journal events. Do not reproduce the donor execution limiter's check-then-increment gap. |
| Ordered registry snapshot and segment-aware prefix listing | **INSPIRED / finite port candidate** | Return immutable snapshots in canonical path order with explicit lifecycle state. Reconcile durable journal state with live runtime observation; never let an in-memory map become authority. |
| `none` / `all` / positive-N fork parser | **INSPIRED / finite port candidate** | Port only the grammar and negative tests. Define EngOrch fork material as sealed completed transcript generations, with authorization and secret-bearing context excluded by policy. Do not copy Codex rollout sanitization or storage types. |
| Queue-only message versus turn-triggering follow-up | **INSPIRED / finite port candidate** | Preserve distinct intent types, sender/receiver generation identity, durable enqueue receipt, idempotency, and bounded mailbox capacity. A follow-up may start only through EngOrch scheduling and budget admission. |
| Mailbox wait wake-up and bounded timeout | **INSPIRED / finite port candidate** | Design against EngOrch's own target/cursor contract. Wake on durable mailbox revision, terminal target state, or steering; keep configured minimum/maximum bounds and avoid polling. The donor's no-target `WaitArgs` cannot be presented as wire compatibility. |
| Interrupt lifecycle | **INSPIRED / finite port candidate** | Reject root/self where applicable, record requested/confirmed/unknown separately, and retain bounded stop/reap and effect reconciliation. Missing runtime state alone must not be promoted to confirmed termination. |
| Open/closed persisted graph edge interface | **INSPIRED / conditional candidate** | Useful only if the existing journal projection cannot answer deterministic parent/child queries. Prefer a projection over canonical events; if cached, require replay equivalence and stable breadth-first ordering. |
| V2 residency eviction | **REFERENCE ONLY** | Do not adopt before a real memory-pressure requirement. Any future unload must require sealed state, empty durable mailbox, confirmed process stop, saved environment identity, and resumability tests. |
| Shared weighted rollout budget | **REFERENCE ONLY** | EngOrch already owns lifecycle and token authority. Borrow only finite/non-negative validation and per-thread reminder idempotence if needed; keep provider receipts and configured budget identities authoritative. |

The recommended first slice is deliberately small: canonical paths, an atomic
spawn reservation guard, deterministic registry snapshots, and fork-mode
parsing. The second slice can add durable message/follow-up receipts and
revision-based waiting. Interrupt and process cleanup should reuse EngOrch's
existing requested/confirmed/unknown lifecycle rather than donor terminal
assumptions. No dependency on `codex-rs` is justified by this audit.

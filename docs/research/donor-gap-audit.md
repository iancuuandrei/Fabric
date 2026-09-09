# Donor gap audit

Status: IN PROGRESS, 2026-09-08. Implementation follows the completed audit,
as requested by the user. Complete the remaining donor comparisons before selecting
and implementing the prioritized mechanisms. Existing local work is preserved.

## Method and authority

Prefer dependency, thin adapter, bounded port, bounded fork, then original code.
Recommendations below are not claims that donor tests passed locally. Source and
tests are inspected without running donor installation scripts. No copied or
ported implementation is introduced by this report. A selected implementation
must retain notices and record actual source/destination paths in THIRD_PARTY.md
and docs/provenance. Feature count, stars and README claims are not maturity proof.

Go retains controller authority; Rust retains RI. Preserve exact identities,
intent/readback/receipt ordering, UNKNOWN reconciliation, privacy admission and
no implicit retry or cross-provider fallback. No dashboard, generic DSL, custom
sandbox, universal inference gateway or model-selected opaque routing is planned.

## Pinned primary donor

U = [fubak/ultraswarm at fe113098eeafce8b800b94efe64e8bff97a52f5f](https://github.com/fubak/ultraswarm/tree/fe113098eeafce8b800b94efe64e8bff97a52f5f).
Actual root [LICENSE](https://github.com/fubak/ultraswarm/blob/fe113098eeafce8b800b94efe64e8bff97a52f5f/LICENSE)
is MIT, copyright 2026 Fubak. Source was cloned for inspection into
`.local/donors/audit-ultraswarm-20260908`; that directory is research material,
not a product dependency. Every U row below uses this exact revision and license.

## Primary donor findings

| Feature | EngOrch current implementation | Donor files at U | Quality and semantic fit | Missing edge cases / recommendation | Benefit | Complexity | Risks |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Functional route preflight | Runtime contracts and explicit local live fixtures exist in internal/opencoderuntime and internal/control; no general cached route qualification gate located | lib/workers/smoke.mjs; lib/workers/adapters.mjs functionalProbes; adjacent tests | Actual isolated execution is useful; smoke succeeds on file existence alone and selects simple/low, not the exact requested route. Cache key is name@version, with default 24h TTL | ADAPT artifact verification and COPY_TESTS for version-pass/auth-no-op; require exact artifact bytes, requested/observed model, profile/config/binary identity, structured output, usage coverage and supported cancellation | Avoid dead routes consuming pool slots | High: admission and governed probe receipts | Blind port would qualify the wrong model or stale credentials; probes themselves consume authorized usage |
| Qualification history | Exact invocation and verification receipts exist; no equivalent multi-run route-history reducer located | lib/state/store.mjs recordMetric; lib/orchestrator/implement.mjs | Donor records measured attempts, but metric dimensions and default cost zero are insufficient | ADAPT informational history, keyed by runtime/provider/model/profile/effort/task class; retain unknown cost | Evidence for explicit policy and planner context | Medium | Selection bias, incomplete attempts and statistics mistaken for route authority |
| Structured usage extraction | Finite native token decoders; usage evidence/cost migration remains open | lib/workers/adapters.mjs resolvePath/parseUsage; tests | Structured descriptors useful, but parser scans stdout plus stderr and sums descriptors/events; raw audit fragments, cumulative semantics and exact integer bounds need stronger contracts | ADAPT bounded declarative paths only for explicitly selected runtime schemas; KEEP_OURS native protocol parsers; reject free-text discovery and overlapping descriptor double counting | More runtime usage coverage | High | Partial fields become zero; duplicate/cumulative records can inflate usage; raw output can contain secrets |
| Failed/losing attempt accounting | Pending reservations preserved; success receipts normalize tokens; full failed-attempt usage not complete | lib/orchestrator/implement.mjs; report.mjs; associated tests | Donor carries failure usage and separates landed/spent; valuable accounting cases | COPY_TESTS then ADAPT finite attempt ledger, including rejected/retried/competition work | Honest total compute | High | Missing usage is unknown, not free; aggregation must not settle uncertain effects |
| Cost estimates versus actual | PricingPolicy bounds reservations; trailing cost ping explicitly is not monetary evidence | lib/orchestrator/implement.mjs lines using costUsd fallback; lib/llm/pricing.mjs | Donor puts configured-price estimates in costUsd when measured cost is missing | REJECT cost fallback; KEEP_OURS distinction; a separately labelled estimate may be useful | Prevent invented observed spend | Low for rejection; high for full monetary receipts | Subscription token counts cannot yield measured dollars |
| Budget calibration | Explicit token/cost reservations; no empirical route calibration located | lib/llm/estimate.mjs and tests | Exact cli/model/effort mean, then cross-effort fallback, then tier curve; small inspectable mechanism | ADAPT only after canonical history exists; bind profile/task class and label fallback level; never backfill measured values | Better reservation estimates | Medium | Sparse/survivorship-biased samples; current means do not establish safe tail capacity |
| Prompt bounds | Bounded provider requests, broker results and paginated agent deliveries exist | lib/prompts.mjs capWorkerPrompt/buildWorkerTaskPrompt; tests | Explicit truncation and bounded repair feedback useful. It slices first 64000 chars and adds a marker beyond the cap; does not protect invariant sections | COPY_TESTS for repeated feedback growth; ADAPT typed context-class truncation only where needed; KEEP_OURS hard wire limits | Bound repair/context growth | Medium | Cutting acceptance criteria, Unicode bytes versus chars, marker exceeding limit |
| Ledger reconciliation | Deterministic journal replay, taskpool and scheduler state exist; cross-artifact terminal reconciliation not qualified | lib/orchestrator/report.mjs buildReport; report.test.mjs ledger mismatch | Donor warns in report when counts disagree, not an authority-level invariant failure | ADAPT reconciliation cases into deterministic terminal validation; do not copy warning-only outcome | Prevent plausible but incomplete reports | Medium/high | Double-counted superseded attempts; task counts alone omit effects/candidates/verification |
| Stale process recovery | Owned runtime process identities and journal-driven UNKNOWN recovery exist | lib/state/store.mjs bootId/setOrchestrator; bin/cli.mjs resume | Linux boot ID prevents cross-boot confusion; empty fallback and same-boot PID reuse remain unresolved | KEEP_OURS authority; COPY_TESTS stale boot/live orchestrator scenarios; do not adopt PID-only kill/resume | Recovery regression coverage | Medium | Windows fallback, same-boot PID reuse, falsely reaping live work |
| Replan remainder | Typed task DAG and accepted artifacts exist; no public remainder replan command located | bin/cli.mjs replanCommand; bin/cli.test.mjs | Filters failed/blocked plan tasks; does not reconstruct surviving dependency artifacts or changed candidate state | ADAPT proposal constructor with exact surviving artifact/base bindings | Retain successful work | Medium/high | Dangling dependencies; treating old plan JSON as current authority |

## Findings that constrain reuse

The primary donor is useful, but its inspected implementation is not a drop-in
implementation of the requested stronger behavior. In particular:

- Functional qualification must bind a route, not only a CLI version.
- Prompt truncation must preserve acceptance criteria, not just the first bytes.
- A ledger mismatch must invalidate terminal reporting, not merely print a warning.
- PID plus boot identity does not identify a process incarnation within one boot.
- Structured usage must distinguish missing values and cumulative reports.
- Replanning must preserve verified dependency artifacts and exact candidate state.

## Remaining comparisons before code selection

Local benchmark inventory currently includes `scripts/benchmark-opencode-execute.ps1`
(pinned runtime fixture and concurrency sweep), lexical/search scripts and kernel
Go benchmarks. `internal/telemetry/telemetry.go` provides bounded command tracing
with optional OTLP, not the requested experimental trial record. Keep tracing
separate from authoritative benchmark/run-diff data: trace delivery cannot prove
task success, exact usage completeness or measured token displacement.

The Codex integration source is under `integrations/codex/engorch`; CLI already
has `internal/cli/run_status.go`, `pool_status.go` and `inspect_export.go`. Extend
these controller projections instead of introducing a competing web control plane.

Independent source audits are in progress for Pact/pi-subagent-tasks/fab/Grackle/
Codex/pi-multiagents integration and scheduler mechanisms, and ccswarm/GRALPH/
Claude Corps/CC-Manager run ergonomics, QA and greenfield behavior. Existing
provenance is a starting point, not proof that new requirements are already met.

The completed report must include explicit decisions for concurrent writer
classes, all requested concurrency gates, recursive agent semantics, run diff,
replay, dry-run, scaffold/DAG authority, acceptance and milestone review, thin
Codex control/event surface, roster, offloaded-work accounting and benchmark
topologies A-E. FastVibe has no exact repository identity in the request; do not
invent a donor or copy ambiguous code under that name.

## Priority order

1. Correctness/recovery: terminal ledger reconciliation and exact process identity.
2. Concurrent writer safety: candidate isolation, exact base, ordered integration,
   verification, rejection and resumable conflict evidence.
3. Real usage/accounting: exact raw evidence, all attempts, unknown cost and
   estimated-versus-measured separation.
4. Codex-native qualification: governed functional route tests and thin client UX.
5. Benchmarkability: frozen task/SHA/topology/repetition telemetry, shared run diff,
   hidden verification and frontier tokens per successful task.
6. Throughput: extend only missing gate dimensions; calibrate from measured runs.
7. UX polish: roster, blocking reasons and approvals from the same controller.

Do not select adaptive topology thresholds without comparative evidence. Report
work offloaded outside controlled experiments; token displacement requires an
explicit baseline with equivalent task and frozen inputs.

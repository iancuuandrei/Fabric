# Multi-agent donor harvest

Status: source-first checkpoint on 2026-09-08, retained before implementation.
AgentTree, AgentControl, TaskPool/DAG, and TaskScheduler adaptations now exist
under `internal/agenttree`, `internal/agentcontrol`, `internal/taskpool`, and
`internal/taskscheduler`; this harvest records why and from what exact source
they were derived rather than qualifying their current implementation. Official repositories were resolved with
`git ls-remote <repository> HEAD`, checked out at the exact revisions below,
and inspected locally. License and source links are immutable.

Classification vocabulary:

- **DEPENDENCY**: consume the upstream package as a pinned runtime/library.
- **COPIED**: retain source substantially verbatim with its license and changes.
- **ADAPTED**: preserve a concrete algorithm/state transition while changing it
  for EngOrch identity, durability or concurrency semantics.
- **PORTED**: translate a bounded source function to Go with exact provenance.
- **INSPIRED**: restate a design mechanism independently; no source copied.
- **REJECTED**: do not reuse because the fit, authority or license is unsuitable.

## Exact donor decisions

| Donor | Exact license/source/test evidence | Decision | Concrete harvest |
| --- | --- | --- | --- |
| [YoungseokCh/pi-multiagents-v2 `fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd`](https://github.com/YoungseokCh/pi-multiagents-v2/tree/fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd) | [MIT](https://github.com/YoungseokCh/pi-multiagents-v2/blob/fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd/LICENSE); [`core.ts`](https://github.com/YoungseokCh/pi-multiagents-v2/blob/fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd/extensions/core.ts), [`team-manager.ts`](https://github.com/YoungseokCh/pi-multiagents-v2/blob/fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd/extensions/team-manager.ts), [`core.test.ts`](https://github.com/YoungseokCh/pi-multiagents-v2/blob/fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd/test/core.test.ts), [`team.test.ts`](https://github.com/YoungseokCh/pi-multiagents-v2/blob/fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd/test/team.test.ts) | **PORTED** pure path/fork/envelope helpers; **ADAPTED** AgentTree/mailbox/lifecycle queue. **REJECTED** as a core dependency because it is a Pi TypeScript extension coupled to in-process `AgentSession`. | Canonical hierarchical names; atomic path reservation; persistent node context; finite statuses; FIFO pending turns; bounded active-child counter; mailbox activity versions/waiters; distinct non-waking message versus waking follow-up; automatic terminal delivery; interrupt/dispose cleanup. |
| [harms-haus/pi-subagent-tasks `2bae805c1a0bdd97e699e2c9601fb4e6624f6f53`](https://github.com/harms-haus/pi-subagent-tasks/tree/2bae805c1a0bdd97e699e2c9601fb4e6624f6f53) | [MIT](https://github.com/harms-haus/pi-subagent-tasks/blob/2bae805c1a0bdd97e699e2c9601fb4e6624f6f53/LICENSE); [`dag.ts`](https://github.com/harms-haus/pi-subagent-tasks/blob/2bae805c1a0bdd97e699e2c9601fb4e6624f6f53/src/dag.ts), [`pools.ts`](https://github.com/harms-haus/pi-subagent-tasks/blob/2bae805c1a0bdd97e699e2c9601fb4e6624f6f53/src/pools.ts), [`scheduler.ts`](https://github.com/harms-haus/pi-subagent-tasks/blob/2bae805c1a0bdd97e699e2c9601fb4e6624f6f53/src/scheduler.ts), [`dag.test.ts`](https://github.com/harms-haus/pi-subagent-tasks/blob/2bae805c1a0bdd97e699e2c9601fb4e6624f6f53/src/__tests__/dag.test.ts), [`pools.test.ts`](https://github.com/harms-haus/pi-subagent-tasks/blob/2bae805c1a0bdd97e699e2c9601fb4e6624f6f53/src/__tests__/pools.test.ts), [`scheduler-core.test.ts`](https://github.com/harms-haus/pi-subagent-tasks/blob/2bae805c1a0bdd97e699e2c9601fb4e6624f6f53/src/__tests__/scheduler-core.test.ts) | **PORTED** DAG validation shape; **ADAPTED** TaskPool all-or-nothing acquisition and scheduler wake-up. **REJECTED** as a dependency because its package requires a sibling file dependency and its scheduler owns worktree/merge/agent execution semantics already owned by EngOrch. | Pure dependency resolution/cycle detection; downstream-unblock priority; exact all-applicable total/provider/model capacity gate; mutate-nothing failed acquire; bounded retirement/late-result inertness; resume normalization; serial merge as a distinct resource. |
| [nick-pape/grackle `b6490c13d1e8ba2f47b5d51e352484307a133f58`](https://github.com/nick-pape/grackle/tree/b6490c13d1e8ba2f47b5d51e352484307a133f58) | [MIT](https://github.com/nick-pape/grackle/blob/b6490c13d1e8ba2f47b5d51e352484307a133f58/LICENSE); [`task-service.ts`](https://github.com/nick-pape/grackle/blob/b6490c13d1e8ba2f47b5d51e352484307a133f58/packages/core/src/services/task-service.ts), [`concurrency.ts`](https://github.com/nick-pape/grackle/blob/b6490c13d1e8ba2f47b5d51e352484307a133f58/packages/core/src/concurrency.ts), [`dispatch-queue-store.ts`](https://github.com/nick-pape/grackle/blob/b6490c13d1e8ba2f47b5d51e352484307a133f58/packages/database/src/dispatch-queue-store.ts), [`dispatch-phase.ts`](https://github.com/nick-pape/grackle/blob/b6490c13d1e8ba2f47b5d51e352484307a133f58/packages/plugin-core/src/dispatch-phase.ts), and their adjacent `.test.ts` files | **INSPIRED** durable queue/reconciliation policy; selectively **ADAPTED** deterministic FIFO and rechecks. **REJECTED** as a dependency, broad fork or task authority because it is a large TypeScript/Rush/Drizzle platform with its own database, runtime, effect and UI model. | Unique durable queue entries; `(enqueued_at,id)` ordering; delete terminal or missing targets; retain temporarily disconnected/capacity-blocked/ineligible entries; recheck task and capacity immediately before start; dequeue only after confirmed start; isolate one bad entry from the rest of a reconciliation pass. |

## Port-ready `AgentTree`

The bounded Go unit should own topology and communication state only. Agent
runtime execution remains behind the existing runtime adapters, and every
durable transition remains an EngOrch journal event.

| Proposed Go operation | Donor source | Port rule |
| --- | --- | --- |
| `ChildPath(parent, name)` and `ResolveTarget(current, target)` | pi-multiagents [`core.ts#L24-L39`](https://github.com/YoungseokCh/pi-multiagents-v2/blob/fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd/extensions/core.ts#L24-L39) | **PORTED.** Keep a small name grammar and canonical `/root/...` paths. Also bind a durable opaque agent ID so a renamed display path cannot substitute identity. |
| `SelectForkMessages(history, mode)` | pi-multiagents [`core.ts#L42-L94`](https://github.com/YoungseokCh/pi-multiagents-v2/blob/fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd/extensions/core.ts#L42-L94) | **ADAPTED.** Preserve removal of an incomplete trailing tool-call batch. Use EngOrch's admitted context artifact and token/byte bounds; never reread mutable parent history after spawn admission. |
| `ReserveChild` / `CommitChild` | pi-multiagents [`team-manager.ts#L217-L305`](https://github.com/YoungseokCh/pi-multiagents-v2/blob/fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd/extensions/team-manager.ts#L217-L305) | **ADAPTED.** The donor's in-memory `reservedPaths` prevents concurrent duplicate creation. In EngOrch, reserve and commit through SQLite/journal uniqueness so crash recovery sees the same decision. |
| `EnqueueTurn` / `Pump` / `FinishTurn` | pi-multiagents [`team-manager.ts#L308-L367`](https://github.com/YoungseokCh/pi-multiagents-v2/blob/fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd/extensions/team-manager.ts#L308-L367) | **ADAPTED.** Preserve FIFO per-agent pending work and a bounded global run count. Capacity must be leased through `TaskPool`; release by exact lease identity in every terminal path. Final delivery must follow durable terminal commit. |
| `Send`, `FollowUp`, `Wait`, `Interrupt`, `Dispose` | pi-multiagents [`team-manager.ts#L370-L511`](https://github.com/YoungseokCh/pi-multiagents-v2/blob/fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd/extensions/team-manager.ts#L370-L511) | **ADAPTED.** Give mailbox entries monotonic sequence and message IDs. `Send` appends without starting an idle run; `FollowUp` appends and requests a turn; `Wait(afterSequence)` avoids lost wake-ups. Interrupt is requested/confirmed/unknown, and subtree disposal cannot infer process death from cancellation alone. |

Required initial tests are direct ports of the donor assertions plus EngOrch
failure cases:

- canonical/invalid paths, relative/absolute resolution, and duplicate spawn
  races resolve to exactly one committed child;
- incomplete tool-call tails never enter a fork; exact fork artifact identity
  survives restart;
- non-waking mail stays queued, follow-up wakes once, `Wait(afterSequence)` does
  not miss mail between inspection and registration, and terminal delivery is
  exactly once after durable completion;
- FIFO turns never run concurrently for one agent; global/profile/model caps are
  never exceeded; interrupt, late completion, crash and restart cannot double
  release or revive a terminal node;
- subtree list and shutdown are bounded by explicit node/message limits.

## Port-ready `TaskPool` and DAG

The starting algorithm is the donor's [`createPoolCoordinator`](https://github.com/harms-haus/pi-subagent-tasks/blob/2bae805c1a0bdd97e699e2c9601fb4e6624f6f53/src/pools.ts#L68-L171): collect every applicable pool, check all caps,
then mutate all counters. The Go adaptation needs a mutex or one SQLite
transaction; the donor's atomicity relies on single-threaded JavaScript.

Each successful acquire must return a unique lease containing the exact total,
access-profile, provider and model slots charged. `Release(leaseID)` must be
idempotent and reject an unknown or already released lease. Recomputing pool
keys from caller strings at release time, as the donor does, is unsafe for
EngOrch identity and accounting.

For dependencies, port the validation pipeline
[`resolveDeps -> detectCycles -> computeDownstreamCount`](https://github.com/harms-haus/pi-subagent-tasks/blob/2bae805c1a0bdd97e699e2c9601fb4e6624f6f53/src/dag.ts#L67-L214)
with two changes:

1. accept exact task IDs only; human titles are display data and cannot resolve
   authority-bearing dependencies;
2. replace recursive DFS and the per-node transitive traversal if benchmarks
   show stack or quadratic growth. Stable declaration order is the final tie
   breaker after readiness and downstream-unblock priority.

Grackle's dispatch queue contributes the recovery boundary: a durable desired
start stays queued while disconnected, at capacity or temporarily ineligible;
each reconciliation pass rechecks eligibility/capacity; dequeue occurs only
after an exact start receipt. A thrown or uncertain start must remain `UNKNOWN`
and cannot become an implicit retry merely because the queue entry remains.

Required initial tests:

- unresolved/self/duplicate/cyclic dependencies reject before persistence;
  chain, fan-out, fan-in and diamond graphs yield deterministic ready order;
- failed all-applicable acquire changes no counter; concurrent acquires never
  oversubscribe the smallest cap; exact one-time release restores every charged
  slot; crash replay reconstructs live leases without inventing capacity;
- a blocked head entry does not corrupt FIFO order or starve eligible work under
  the stated policy; removal and terminal tasks dequeue, transient disconnect
  stays queued, and eligibility/capacity are rechecked at start time;
- a confirmed start dequeues once; rejected start remains eligible for a new
  explicit attempt; unknown start blocks replay pending reconciliation.

## Benchmark inputs before accepting the port

Benchmarks must report wall time, allocations, peak resident memory and output
hash on the same machine/toolchain. These are proposed inputs, not measured
results.

| Primitive | Deterministic input families | Sizes and assertions |
| --- | --- | --- |
| AgentTree topology | chain; balanced 4-ary tree; 1 root with all children; mixed subtree listing | `1, 64, 1,024, 16,384` nodes. Measure reserve/commit, lookup, ancestor/subtree list and restart replay. No recursion overflow; output identity stable. |
| Mailbox | one producer/one recipient; 64 producers/one recipient; parent fan-in; broadcast-like sibling messages represented as individual sends | `1, 1,000, 100,000` messages at `64 B, 4 KiB, 64 KiB`, with configured total-byte cap. Measure append/read-after-sequence/wake-up; zero loss, duplication or reorder per sender. |
| DAG | chain; fan-out; fan-in; diamonds; layered width 32; deterministic dense acyclic graph; same shapes with one back edge or unresolved ID | `32, 1,024, 16,384` tasks and edge counts `V-1`, `4V`, `32V`. Measure validate, cycle detect, ready-set update and downstream priority. Invalid graphs fail before scheduler mutation. |
| TaskPool | total-only; total/profile/provider/model smallest-cap permutations; 64 goroutines contending for one slot; mixed releases; crash/replay | `10^3, 10^5, 10^6` acquire/release operations. Assert peak used never exceeds cap, failed acquire mutates nothing, every successful lease releases once, and replayed usage equals live leases. |
| Durable dispatch | FIFO all-ready; blocked head with eligible tail; disconnected environment; rejected/unknown start; restart between lease and receipt | `1, 1,000, 100,000` entries. Measure reconciliation batch latency and database writes; assert deterministic order and no replay of unknown starts. |

## Executed donor evidence

Only the small self-contained pi-multiagents suite was executed:

```text
# exact checkout fb056dd9ea65f32ee8e8da7b57e457d93bdd6ccd
npm ci --ignore-scripts
npm run check
tests 8; pass 8; fail 0; duration_ms 12024.4974
```

A final rerun against the same checkout also passed all eight tests
(`duration_ms 2297.4097`). Durations are observations, not performance
benchmarks; dependency caches and host load were not controlled.

The executed assertions covered rendering, canonical paths, incomplete-fork
tail removal, envelope shape, mailbox wake-up/transcript visibility and child
session persistence. They did not exercise real concurrent model sessions or
crash recovery.

pi-subagent-tasks tests were **NOT RUN**: its package declares
`@harms-haus/pi-subagents-lib` as `file:../pi-subagents-lib`, which was not part
of the exact repository checkout. Grackle tests were **NOT RUN**: qualifying its
large Rush workspace was outside this bounded source harvest. Their test files
are evidence of intended assertions only and are not reported as passing.

EngOrch now retains the exact MIT notices and modification records under
`third_party/pi-multiagents-v2` and `third_party/pi-subagent-tasks`, with matching
entries in `THIRD_PARTY.md` and `NOTICE`. The bounded Go ports/adaptations must be
qualified by their own executed tests and benchmarks; the upstream test status
above is not evidence that the Go implementations pass.

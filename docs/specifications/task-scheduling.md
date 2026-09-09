# Durable task scheduling

Status: initial integration implemented from current code inspection and the pinned
`harms-haus/pi-subagent-tasks` revision
`2bae805c1a0bdd97e699e2c9601fb4e6624f6f53`. Evidence below distinguishes the
implemented bounded scheduler from remaining whole-task retry and hosted work.

## Current boundary

The implemented components form a bounded durable scheduler:

| Requirement | Current evidence | Status |
|---|---|---|
| Immutable dependency graph | `internal/taskpool/dag.go` validates exact IDs, missing and duplicate dependencies, cycles, bounded graph size, and deterministic unique-descendant priority. `Graph.Ready` admits only pending tasks whose dependencies succeeded. | Component implemented |
| Shared cross-run capacity | `internal/taskpool/pool.go` durably acquires all applicable total/profile/provider/model slots in one journal append. A denied acquire mutates nothing; unresolved attempts remain active. | Component implemented |
| Controller binding | `internal/control/task_pool.go` derives the capacity request from the immutable pool binding and exact access invocation. Settlement requires controller-replayed provider/model terminal evidence. | Integrated for individual model dispatches |
| Capacity-safe agent admission | `internal/control/agenttree_dispatch.go` records controller admission, checks TaskPool capacity, then marks the AgentTree node running and enters the runtime. Capacity denial leaves the node queued without an UNKNOWN observation. | Integrated for individual provider/OpenCode dispatches |
| Cross-run ready queue | `internal/taskscheduler` binds exact task/run/controller/operation/invocation identities, derives DAG readiness and appends per-task claims under the scheduler journal lock. | Implemented and locally tested |
| Park and wake | Capacity and lifecycle preflight stops append durable parked records. A later `Tick` retries the same controller invocation under a new scheduler generation; callers tick after release/resume or at startup. | Implemented without a resident daemon |
| Scheduler recovery | Claims survive crashes. `RecoverClaim` retains the exact claim, probes running/UNKNOWN work without redispatch, and uses only the adapter's receipt-backed `Reconcile` seam when terminal evidence is not already visible. | Implemented and locally tested |
| Retry policy | UNKNOWN is never redispatched. Only finite, evidence-proven pre-effect parking is eligible for a new scheduler generation. | Implemented; whole-task retry absent |
| DAG completion/failure propagation | UNKNOWN blocks fan-in; exact success unblocks it. Terminal failure/cancellation deterministically fails unfinished dependants. | Implemented and locally tested |

The executed checkpoint was:

```text
go test -count=1 ./internal/taskpool
PASS

go test -count=1 -run '^(TestTaskPool|TestLifecycleCapacityDenialParksWithoutUnknown|TestLifecycleUnknownDoesNotReleaseTaskPoolSlot|TestAgentDispatchUnknownCanReconcileWithoutNewAdmission)$' ./internal/control
PASS

go test -count=1 ./internal/taskscheduler
PASS

go test -race -count=1 ./internal/taskscheduler
PASS
```

The scheduler tests include real temporary scheduler and TaskPool journals,
cross-run capacity parking/resume, concurrent same-task and independent-task
ticks, failure/UNKNOWN fan-in, terminal adoption without dispatch, and crash
recovery without resend. The controller adapter and CLI have separate local
fixtures; hosted multi-process operation remains unqualified.

## Donor behavior to adapt

The pinned donor's `src/scheduler.ts` performs a guarded scheduling pass,
selects `ready` and `parked` tasks whose dependencies are done, sorts parked
tasks first and then by downstream count, and attempts all-applicable capacity
admission. Its `src/status.ts` defines blocked, ready, running, parked, done and
failed transitions, transitive failure propagation and fixed-point detection.
Its completion path releases capacity, advances work, applies bounded retry
decisions and starts another scheduling pass.

EngOrch should adapt the selection and state-machine rules. It should not copy
the donor's in-memory promises, counters, mutable task objects, automatic slot
release, worktree ownership, merge queue, or abort retirement. EngOrch already
has durable controller, AgentTree, TaskPool, worktree and effect authorities.
In particular, process exit or cancellation cannot release a slot or turn an
UNKNOWN dispatch into a retry.

## Minimal implementation

Add `internal/taskscheduler` as a coordination journal. It owns selection and
claim history only. It never stores model credentials, invokes a provider,
mutates a controller phase, grants TaskPool capacity, or declares an effect
terminal.

### Immutable binding

```go
type TaskSpec struct {
    ID, RunID, ControllerPath, Input, InvocationID string
    DependsOn []string
    Operation Operation // planner, explorer, writer, reviewer
}

type Definition struct { Version int; Nonce string; Tasks []TaskSpec }
```

`Bind(path, definition)` validates the graph, requires one unique binding for
every task, and records `schedule.bound`. Its identity binds the ordered graph
and controller locators. The library adapter rechecks the exact controller run,
invocation and head at probe/dispatch time; the CLI additionally confines paths
and prepares each task from the selected repository's controller journal.
Rebinding or silently replacing a controller is rejected.

`ControllerPath` is an operational locator rather than authority. Every pass
must replay that journal and verify `RunID`; durable controller events remain
the authority for lifecycle and dispatch state.

Runtime-created children and follow-up turns use `AddTask` with an exact
`DynamicTask` binding: task spec, parent agent ID, agent ID and turn ID. The
scheduler assigns a monotonic `TurnSequence` per agent. Later turns may queue,
but only the oldest nonterminal turn is eligible; a running or UNKNOWN turn
blocks later turns until exact terminal reconciliation. The dynamic task ID is
the turn ID, so titles never resolve execution identity.

### Durable scheduling records

Use a finite journal vocabulary:

| Event | Meaning |
|---|---|
| `schedule.bound` | Immutable graph and controller bindings. |
| `task.added` | Runtime-created child/follow-up turn with exact agent identities and a scheduler-assigned per-agent sequence. |
| `task.claimed` | One scheduler generation selected from fresh controller evidence and replayed dependency states. It binds the exact task, controller head and optional dynamic agent turn. |
| `task.parked` | The exact claim encountered pre-dispatch capacity denial, an inactive lifecycle, or another finite proven no-effect condition. It records a finite reason and never infers a runtime attempt. |
| `task.observed` | The exact claim is `running`, `succeeded`, `failed`, `cancelled`, or `unknown`, backed by a fresh controller head and exact controller admission/observation IDs. |
| `task.uncertain` | Dispatch returned without trustworthy controller evidence; the claim remains UNKNOWN and cannot be retried. |

Replay rejects duplicate open claims, changed bindings, illegal transitions,
foreign heads and UNKNOWN-to-new-claim transitions. `blocked` and `ready` are
derived from the graph plus terminal controller evidence. `parked`, an open
claim and its generation are durable. This avoids copying the controller's
phase state into a second authority.

Candidate order adapts the donor's safety-relevant order through the existing
Graph priority:

1. previously parked before newly ready;
2. larger unique downstream count first;
3. original declaration order as the stable tie-breaker.

The donor's additional fewer-direct-dependencies tie-break is not exposed by
the current `taskpool.Graph` API and remains pending rather than duplicating DAG
reachability logic inside the scheduler.

Only one `task.claimed` event is appended per `Tick` call. The scheduler journal
lock serializes competing scheduler processes. The append validator performs
no cross-journal I/O: heads are read before the append and embedded in the
claim. The controller adapter rechecks its current head and lifecycle before
admission, so a pause or cancellation racing the claim fails closed and parks
the task without runtime work.

### APIs

Implemented scheduler API:

```go
func Bind(path string, definition Definition) (Snapshot, error)
func AddTask(path string, dynamic DynamicTask) (Snapshot, error)
func Inspect(path string) (Snapshot, error)
func Tick(ctx context.Context, path string, adapter Adapter) (Decision, error)
func RecoverClaim(ctx context.Context, path, claimID string, adapter Adapter) (Decision, error)

type Adapter interface {
    Probe(context.Context, ProbeRequest) (Evidence, error)
    Dispatch(context.Context, Claim) (Evidence, error)
    Reconcile(context.Context, Claim) (Evidence, error)
}
```

`Tick` probes running and UNKNOWN claims without re-entering their dispatch
path. Independent tasks may still be selected subject to DAG and TaskPool
limits. `RecoverClaim` calls the separate adapter `Reconcile` seam for UNKNOWN;
that seam cannot send a provider request and may only complete already durable
runtime evidence. `Tick` calls `Dispatch` immediately after it creates a claim;
`RecoverClaim` never does. A crash can occur after dispatch begins but before a
scheduler observation, so even a merely claimed task becomes UNKNOWN on
recovery and uses only `Reconcile`.
Callers invoke another `Tick` after a terminal observation, TaskPool release,
operator resume, or process restart. This event-driven call pattern cannot lose
eligibility because every pass reconstructs it from journals; polling or a
resident daemon is optional.

The narrow controller seam is implemented in
`internal/control/scheduled_dispatch.go`:

```go
type ScheduledDispatchAdapter struct{}
func (ScheduledDispatchAdapter) Probe(context.Context, taskscheduler.ProbeRequest) (taskscheduler.Evidence, error)
func (ScheduledDispatchAdapter) Dispatch(context.Context, taskscheduler.Claim) (taskscheduler.Evidence, error)
func (ScheduledDispatchAdapter) Reconcile(context.Context, taskscheduler.Claim) (taskscheduler.Evidence, error)
```

The dispatch function replays the controller, checks the exact head/run/lifecycle,
derives the controller's next invocation, and enters the existing
`beginAgentDispatch -> ensureTaskPool -> markAgentDispatchRunning -> runtime`
path. It must not add a parallel admission schema. The observation function
only projects existing controller, AgentTree, runtime and TaskPool evidence.

### Recovery and retry

Recovery always retains the claim and invocation identity:

- crash after `task.claimed`: retain the same claim as UNKNOWN and use only
  controller reconciliation; the scheduler cannot prove dispatch had not begun;
- crash after controller admission but before capacity: recover the same
  `agent.dispatch-admitted` record;
- crash after capacity acquisition but before runtime: recover the same active
  TaskPool request and runtime invocation;
- runtime UNKNOWN: append `task.observed{unknown}` and permit only the explicit
  controller reconciliation seam; do not start another generation or release capacity;
- exact terminal receipt: append a terminal `task.observed`, allow existing controller
  settlement to release the exact TaskPool request, then run another pass.

The first implementation retries only claims that provably stopped before a
runtime/effect attempt: capacity denial, lifecycle pause, or a scheduler crash
before controller admission. A parked retry uses a new scheduler generation but
the same controller invocation/admission identity. Provider, host, Git and file
errors do not become retryable merely because the scheduler saw an error.

A whole-task retry after terminal failure is out of the minimal seam. Current
controller invocations and provider routing use a fixed attempt identity, so a
safe whole-task retry requires a new controller-authorized invocation and an
explicit bound limit first. Until that exists, `failed` is terminal and its
dependants fail transitively. This is a deliberate rejection of the donor's
in-memory `failed -> ready` shortcut.

## Implemented ownership boundary

1. `internal/taskscheduler` owns schedule binding, dynamic turns, claims,
   parking, observations, UNKNOWN retention and deterministic DAG selection.
2. `internal/control/scheduled_dispatch.go` owns the finite planner, explorer,
   writer and reviewer adapter over existing entry points.
3. The CLI exposes repository-local create/inspect/tick/recover commands. It
   confines controller paths to the selected repository; the library supports
   explicit paths, but cross-repository CLI schedules are not implemented.

No AgentTree, existing TaskPool journal, runtime adapter, access policy, or
effect journal should be forked or replaced.

## Required integration tests

The implementation is not complete until these tests execute against real
temporary journals rather than mocked status maps:

1. **Cross-run capacity and wake:** bind two independent controller runs to one
   TaskPool with total capacity one. The first tick admits run A. The second
   records run B parked without controller/runtime admission. After A's exact
   terminal receipt releases its slot, the next tick reclaims B and records one
   controller admission.
2. **Crash matrix:** stop after scheduler claim, controller admission, and
   TaskPool acquisition in separate subtests. Reopen every journal and tick.
   Each case must retain one scheduler claim, one controller admission, one
   TaskPool request and at most one runtime/provider send.
3. **UNKNOWN recovery:** make an admitted runtime return uncertain after its
   effect boundary. Restart the scheduler repeatedly. The task remains UNKNOWN,
   its capacity stays active, dependants remain blocked, and the send count
   stays one. Add exact terminal evidence through the existing read-only
   recovery path; only then may capacity release and a dependant be claimed.
4. **Parked fairness:** occupy the binding model/provider slot, park a task,
   add a newly ready task, release capacity, and assert the parked task wins.
   Then assert downstream-count, dependency-count and declaration-order ties.
5. **Pause race:** race a scheduler claim with `RequestPause`. If the claim wins,
   only that exact admitted work may settle. If pause wins, the task parks and
   no runtime/access/provider intent appears. Resume permits the same task and
   invocation; it does not manufacture a resend.
6. **Failure propagation and fan-in:** in a diamond graph, a failed parent
   durably fails only unfinished dependants; an UNKNOWN parent blocks the join
   without failure. A succeeded reconciliation makes the join ready exactly
   once.
7. **Competing schedulers:** run many concurrent `Tick` calls, including from
   reopened scheduler handles. Assert one live claim per task, global pool caps
   never exceeded, and no duplicate controller admission or runtime send.
8. **Fixed point:** a graph is complete only when every task is terminal, no
   claim is open or UNKNOWN, and TaskPool has no scheduler-owned active request.
   Parked, ready and UNKNOWN states must keep it incomplete.

## Persistent scheduler workers

`taskscheduler.Pump` keeps 1..64 workers calling the existing `Tick` owner
until cancellation or a scheduler error. Empty queues do not stop the pump:
workers remain available for children appended while a parent is running.
Its polling interval defaults to 100ms and is bounded to 10ms..1s. Every
decision still uses the same durable claims, FIFO, dependency checks and
controller TaskPool admission. The pump does not reconcile by redispatching.

Waiting parents occupy workers and their existing runtime capacity. A child
needs an available worker and applicable pool capacity; the pump does not
manufacture capacity or solve arbitrary recursive resource deadlocks. On an
error it cancels its child context and waits for all owned calls to return.
Adapters must honor cancellation and preserve uncertain effects. Queue
emptiness is not a completion receipt.

Local tests prove that a late-added child executes once while its parent
waits, and that a probe failure stops the pump before admission. The complete
scheduler suite, focused pump race tests and vet passed. Controller/MCP pump
wiring and real recursive runtime qualification remain pending.

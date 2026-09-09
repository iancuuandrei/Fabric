# Local task schedules

A schedule binds a DAG to existing v2 controller runs. It supports planner,
explorer, writer and reviewer operations. Runtime selection, access limits,
workspace permissions and verification authority remain with each run's frozen
configuration. Creating a schedule does not approve a plan or launch a model.

For v2 runs that retain more than 16 explorer results, configure retention and
writer context separately before creating the run. For example, this optional
stanza retains up to 128 results while presenting the latest eight to a writer:

```toml
[exploration]
version = 1
max_records = 128
max_context_records = 8
```

The remaining results stay in the journal and their ordered evidence hashes
are bound into writer context. This policy does not increase concurrency or
model token budgets. Those remain controlled by the shared pool and access
limits. Existing runs retain their original configuration; omitting the stanza
preserves the legacy 16-record retention and context bounds.

Use `harness --root REPOSITORY schedule-task RUN TASK_ID OPERATION [INPUT]`
to obtain an exact task object. Only explorer takes INPUT, its explicit question.
The command derives the invocation identity from current controller state; do not
invent model identities, controller paths or invocation hashes.

Create a JSON definition containing `version: 1`, a nonempty caller-selected
`nonce`, and a `tasks` array of those returned objects. Add `depends_on` arrays
using exact task IDs when needed. All task runs must belong to the selected
repository. A task becomes eligible only after its dependencies have succeeded.

Run these commands with the same repository root:

```text
harness --root REPOSITORY schedule-create DEFINITION_JSON
harness --root REPOSITORY schedule-inspect SCHEDULE_ID
harness --root REPOSITORY schedule-tick SCHEDULE_ID
```

Keep the returned schedule ID. Each tick makes one scheduling decision through
the existing controller adapter. An optional WORKERS argument, from 1 to 64,
attempts one decision per worker concurrently; the default is one. Batch output
reports every worker's decision and failure flag, even when another worker fails.
Concurrent callers may select independent tasks;
the shared TaskPool, when configured, still controls total/profile/provider/model
capacity. A full pool parks work without inventing an UNKNOWN model outcome.
These commands do not start a background daemon.

For a foreground runner that remains available as agents append child tasks,
use `schedule-run SCHEDULE_ID [WORKERS]`. It defaults to one worker and accepts
1..64. Stop it with Ctrl+C; cancellation is not a completion receipt. Inspect
the schedule afterward for terminal or UNKNOWN work. The process remains
running even when no task is currently eligible. Waiting parents occupy worker
slots, so nested execution needs spare workers and applicable TaskPool capacity.
The runner cannot override either limit.

A live worktree guard conflict parks a task only when a fresh controller probe
shows that its exact invocation has no admission. A stale durable writer token,
substituted guard or failed identity check is not treated as ordinary contention.
If the invocation already has running or UNKNOWN evidence, that uncertainty
remains; a guard conflict cannot authorize another attempt.

For an existing registered parent, `agent-spawn RUN SCHEDULE_ID REQUEST_JSON`
adds an explorer child to this same scheduler. The JSON request contains
`parent_agent_id`, `name`, `question` and `nonce`. Obtain the exact parent ID
with `agent-list RUN`. The controller derives the explorer route, input and
read-only authority from the run; these are not fields the request may override.

`agent-followup RUN SCHEDULE_ID MESSAGE_JSON` adds an explorer turn for an
existing child. Its JSON fields are `from_agent_id`, `to_agent_id`, `nonce` and
`body`; the body is the next question. Both commands return exact agent, task
and turn identities and the assigned turn sequence. They queue work; use
`schedule-tick` to attempt execution and inspect its evidence afterward.
Keep the same request and nonce when retrying an uncertain queue admission.
Use `agent-wait` with an activity cursor to observe subsequent turns. Turn
activities carry their own ID, sequence and running/unknown/succeeded status;
the agent node's first terminal result does not summarize all later turns.
The CLI validates repository identity again at each probe, dispatch and recovery,
including tasks appended after the initial schedule inspection.

To request interruption of an admitted OpenCode explorer turn, use its exact
schedule and turn identities:

```text
harness --root REPOSITORY agent-interrupt RUN SCHEDULE_ID TURN_ID ACTOR NONCE
```

Preserve the actor and nonce on retries. The command records intent; the
executor running `schedule-tick` observes that durable request and cancels only
the matching claim's execution context. An absent executor cannot deliver a
signal. Observe subsequent interrupt activities through `agent-wait`; a
successful request alone does not establish delivery or shutdown. A
`signal_delivered` observation still leaves teardown unproved, and `unknown`
must retain the existing uncertainty. The current adapter does not emit
`confirmed_local_stop`. Do not release capacity or start a replacement turn
based solely on interruption output. The stop request grants no new work and
remains available during a run's pause or cancellation request.

The current recursive scheduling surface is explorer-only. Arbitrary repeated
turns, recursive writers/reviewers and process interruption are not yet fully
implemented or qualified. The CLI queue fixture verifies exact retries and
monotonic turn admission against a real controller/workspace, without claiming
that its fake explorer profile ran a child runtime.

Use `schedule-recover SCHEDULE_ID CLAIM_ID` for an existing exact claim. Recovery
reads durable controller evidence and retains the original invocation. It cannot
replace an uncertain attempt with a fresh model request. Use `schedule-inspect`
after an error; process exit alone does not settle a claim or release capacity.

Already completed controller results can be adopted without dispatch. A writer
proposal is not an applied change, and a reviewer result is not verification
authority. The ordinary plan/effect approval and verification commands still
govern those subsequent steps.

Local deterministic tests cover concurrent independent selection, same-task
exclusion, dependency ordering, terminal adoption and uncertain-capacity retention.
CLI integration uses a fake v2 runtime with real durable access admissions. This
is not external model quality or production throughput qualification.

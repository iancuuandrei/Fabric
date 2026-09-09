# Recursive agent control surface

Status: partial callable surface and bounded integration design, 2026-09-08.
The repository has durable AgentTree records, a controller-backed task
scheduler, operator queries/messages through `agentcontrol`, and exact explorer
spawn/follow-up admission into the existing scheduler and provider dispatch
loop. A local pinned OpenCode run has executed two scheduled turns for one
explorer with exact recovery and no resend. The repository does not yet expose
recursive agent operations to a running agent. This document does not claim the
full hierarchical orchestration surface is available.

## Callable surface today

The requested operations are `spawn`, `list`, `wait`, `message`, `followup`,
and `interrupt`. Their current implementation status is:

| Operation | Durable component | Controller/runtime integration | CLI | MCP tool available to a running agent | Actual status |
| --- | --- | --- | --- | --- | --- |
| `spawn` | `agentcontrol.ScheduledService.Spawn` recovers or commits one bounded child and appends its exact initial turn with `taskscheduler.AddTask`. | `control.SpawnExplorerAgent` derives read-only authority, context, TurnID and a turn-scoped invocation from current controller configuration. Scheduler claims carry the exact parent, agent, turn, and sequence into provider dispatch. | `agent-spawn` accepts only parent, bounded name/question, and nonce for an exact run and schedule. | None. | Explorer child queue admission and local pinned OpenCode execution are qualified. Other roles are not exposed. |
| `list` | `agentcontrol.Service.List` returns a bounded path page and reconciles current AgentTree statuses into monotonic activities. | The service is bound to one exact TreeID and fixed tree/control journal paths. | `agent-list` is callable for an exact run with `AFTER_PATH` and `LIMIT`. | None. | Operator surface available; no model surface. |
| `wait` | `agentcontrol.Service.Wait` polls later message, node, dynamic-turn, or interrupt activity for one agent under a required deadline. | Per-agent activity sequences cover stored messages, observed node status/result changes, controller-proven turn transitions, and interrupt request/observation transitions with exact TurnID and sequence. | `agent-wait` is callable with exact agent, cursor, limit, and timeout. | None. | Operator activity wait available; runtime/MCP wait missing. |
| `message` | `agentcontrol.Service.Send` stores a bounded UTF-8 body in its integrity journal and binds its digest to the AgentTree envelope. `MessagesAfter` verifies that envelope against the tree before returning body text. | Registered persistent agents accept non-waking messages between turns and while a turn is unknown; ordinary messages create no scheduler work. | `agent-send` accepts a canonical message request; `agent-messages` returns bounded exact bodies. | None. | Durable operator messaging available; runtime consumption remains explicit through follow-up input. |
| `followup` | `agentcontrol.ScheduledService.FollowUpTurn` stores an exact waking body and appends its turn to the same scheduler. | `control.FollowUpExplorerAgent` derives a unique TurnID and turn-scoped invocation even when question text repeats. Scheduler eligibility admits only the oldest nonterminal turn for that agent. Provider runtime and gateway journals use TurnID-specific paths. | `agent-followup` accepts a bounded canonical message request; body text is the explorer question and is not echoed in the mutation result. | None. | Explorer follow-up queue admission and an actual two-turn local pinned OpenCode run are qualified. |
| `interrupt` | `agentcontrol.ScheduledService.RequestInterrupt` binds one durable request to an exact open dynamic claim; observations are `signal_delivered`, `confirmed_local_stop`, `unknown`, or `already_terminal`. | `control.RequestAgentInterrupt` currently admits OpenCode explorer turns only. A scheduler-side watcher cancels the exact in-process execution context and records delivery, then records `unknown` unless ordinary terminal evidence won the race. It does not yet prove `confirmed_local_stop`. | `agent-interrupt` accepts an exact schedule/turn plus bounded actor and nonce. | None. | Durable operator request and exact local signal delivery are implemented; confirmed runtime teardown and MCP interruption are unqualified. |

The current OpenCode MCP server projects only the admitted source and candidate
catalog from `contextbroker` through `contextmcp`. It has no agent-control
definitions or handlers. The Codex host policy explicitly disables
`multi_agent` and `multi_agent_v2`; its installed native multi-agent features
therefore cannot be treated as an EngOrch surface. The application-level
collaboration functions used to build this repository are not callable by the
compiled harness and provide no product evidence.

The `taskscheduler` is the correct single queue owner. Its adapter calls
the existing `ResumePlanning`, `RunExplorer`, `RunWriter`, and `RunReview`
controller loops. `AddTask` appends a controller-derived dynamic turn after
`schedule.bound`, assigns its monotonic per-agent sequence, and lets only the
oldest nonterminal turn for an agent become eligible. The implemented explorer
spawn and follow-up adapters construct these tasks, and the existing scheduler
dispatches their exact prebound agent and turn identities. Adding a second
queue inside AgentTree would duplicate eligibility, capacity, recovery, and
fairness authority and is rejected.

## Required finite contract

AgentTree should continue to own topology, messages, turns, and target-local
lifecycle facts. TaskScheduler should own readiness, dependency order, claims,
parking, and dispatch. TaskPool remains the only capacity authority. Controller
role functions and their existing Codex, OpenCode, or direct-provider adapters
remain the only inference loops.

### Stable agents and FIFO turns

An agent node is a durable identity created by one exact spawn admission. A
follow-up must not create a replacement node. Add a bounded `Turn` record with:

- opaque `turn_id`, `agent_id`, monotonic per-agent sequence, and exact runtime
  invocation ID;
- content-addressed input artifact ID, controller run/candidate identity, role,
  and access authority;
- scheduler task/claim identity and finite `queued`, `running`, `unknown`,
  `succeeded`, `failed`, or `cancelled` observation;
- accepted result digest for success, with `UNKNOWN` requiring explicit
  reconciliation before another turn can start.

Only one turn for an agent may be running or unknown. TaskScheduler must append
dynamic tasks in the same journal and enforce this FIFO rule before a claim.
The dynamic task event must revalidate the complete bounded dependency graph;
it must not mutate the immutable task on replay. A scheduler task should bind
`parent_agent_id`, `agent_id`, and `turn_id` in addition to its existing run,
operation, invocation, and controller-head identities. The implemented dynamic
claim carries those values and its monotonic turn sequence into the existing
dispatch adapter; repeated input text still receives a distinct turn-scoped
invocation identity.

`spawn` reserves and commits the child first, writes its initial turn, then
enqueues that exact turn with TaskScheduler. A crash at any boundary leaves a
visible reserved, queued, or unknown identity. It never calls a model inline
from the MCP handler. `followup` stores a waking message and enqueues one new
turn only when no equivalent pending wake exists. `message` stores the same
artifact without enqueuing work.

The allowed child roles must come from a validated controller policy, such as
an explicit parent-role to child-role matrix. A display path or parent prompt
cannot grant a writer. Read-only children retain read-only context; writer or
fixer authority still requires the controller's admitted candidate workspace
and configured role. Depth, node, message, turn, and schedule bounds fail
before persistence.

### Message and context artifacts

The implemented AgentControl journal stores each bounded UTF-8 message body in
the same durable record as its metadata, while the AgentTree mailbox envelope
binds its digest and byte count. `MessagesAfter` verifies both records before it
returns content. Message text must never be copied into telemetry attributes or
process diagnostics. A separate content-addressed artifact store is not part of
the current contract.

Initial child and follow-up inputs should be constructed by the controller from
durable accepted data: the spawn/follow-up artifact, parent and candidate
identities, and the existing finite role prompt builder. They must not reread a
mutable live conversation. If conversational fork semantics are later needed,
each host adapter must first produce a sealed, bounded context artifact and
drop an incomplete trailing tool-call batch. Until that exists, the surface
must describe child context as controller reconstruction rather than a session
fork.

### Status, waiting, and terminal delivery

Add a monotonic per-agent activity sequence covering messages, turn admission,
running/unknown/terminal observations, and interrupt observations. `list`
returns a bounded page of nodes with current turn status and child count.
`wait(agent_id, after_sequence)` returns as soon as later durable activity is
visible; it does not infer liveness from elapsed time.

An MCP wait must remain below the existing owned MCP/tool deadline and return a
finite timeout result so it does not hold a provider turn indefinitely. CLI
wait may accept a caller context deadline. A child terminal result is delivered
to its parent mailbox exactly once only after the terminal AgentTree and
controller/scheduler observations are durable. Delivery needs its own
deterministic message ID so replay cannot duplicate it.

That delivery must be a distinct controller-authored terminal event. Ordinary
`Send` remains available after a turn completes because the node identifies a
persistent agent rather than its first turn. A non-waking message only stores
mailbox context: it does not reopen the terminal turn, change node status, or
create scheduler work. This persistent mailbox behavior is not a substitute
for the missing exactly-once terminal delivery event.

### Interrupt adapters

Interrupt is a two-step durable operation:

1. append an exact request for `agent_id` and `turn_id`;
2. ask the executor that owns the live runtime handle to cancel, then append
   `confirmed_local_stop` only after its process/session and controller receipt establish
   termination. Missing handles, cancellation after an uncertain network
   write, or restart without terminal evidence append `unknown`.

The finite host adapters reuse existing ownership:

| Runtime | Cancellation action | Confirmation boundary |
| --- | --- | --- |
| OpenCode | Cancel the exact execution context and close the owned `Process`, MCP server, proxy, and broker through the existing seal path. | Exact sealed terminal receipt proves the root process was reaped and owned handlers stopped. |
| Codex host | Cancel and close the exact private `codexhost.Process` owned by the role execution. | Existing host/runtime observation and process wait establish the finite outcome; absence after restart is `unknown`. |
| Direct provider | Cancel the exact request context. | A validated provider/access terminal receipt confirms completion; cancellation after a possible request write remains `unknown`. |

Runtime handles are in-memory, executor-owned capabilities indexed by exact
scheduler claim and turn identity. They are never serialized as PIDs or bearer
tokens. Restart reconciles journals and receipts; it does not rediscover or
signal an unrelated OS process.

## External adapters

Expose one controller-owned `AgentControl` service with the six finite
operations. Both CLI and MCP call the same service; neither writes AgentTree or
TaskScheduler journals directly.

For MCP, add six typed tools to an owned composite tool service alongside the
existing read-only context service. Keep `contextbroker` read-only rather than
placing scheduler writes behind its current API. The composite rejects name
collisions, binds the caller's agent/turn identity at construction, and admits
only tools allowed by the controller role policy. Tool results return canonical
identities and finite states, never an inference transcript or runtime secret.

For CLI, add explicit run and exact agent/turn locators. Mutating commands take
body files or canonical JSON requests so shell arguments do not become an
unbounded message channel. `list` and bounded `wait` grant no scheduling or
runtime authority, but they may append newly reconciled lifecycle activities to
the AgentControl journal. `spawn`, `message`, `followup`, and `interrupt` append
through `AgentControl`. Operations that grant new work (`spawn` and `followup`)
use the same active-lifecycle and fresh host-admission checks as runtime
dispatch. An exact interrupt request grants no execution authority and remains
available while a run is paused or cancelled so an operator can stop an
already-open turn; its claim, tree, schedule, controller head, runtime, and turn
bindings still fail closed.

## Implementation sequence and proof

New MCP wait receipts bind `activity_page_head` to the exact AgentControl
journal prefix used to select the activity page. A concurrent append cannot
change that page's meaning. A timeout retains the last observed empty page;
if the deadline expires before any page is observed, the call returns an error
without inventing a journal position. Explicit cancellation returns no page.
The optional marker preserves historical receipt encoding: unmarked legacy
timeouts retain their timing-only interpretation, while marked timeouts must
prove an empty eligible page at the recorded prefix. This binds activity
selection only; it does not establish accepted child-result body delivery.

The accepted-result index stores one body-free reference per child turn. The
reference binds the exact controller event, invocation, admission, turn,
result hash and parent/child identities. One durable event adds both a parent
inbox notification and a child activity notification with the same result ID;
their sequence numbers belong to their respective agents. This lets a parent
wait on either its inbox or its direct child after consuming an earlier runtime
completion activity. Identical retries add no event, and a changed reference
for the same turn is rejected. A successful runtime observation alone never
creates an accepted result. The controller must establish acceptance before
calling the index; AgentControl checks topology and the exact successful turn
observation, and does not independently validate the controller's result body.

Versioned wait responses resolve accepted exploration bodies from the exact
controller event referenced by the index. The controller, AgentTree,
AgentControl and scheduler prefixes used for resolution are recorded in the
receipt and replayed during verification. Bodies are available for the caller's
own turns and its direct children's turns. A grandchild result notification
remains in the ordered activity page with an explicit
`outside-caller-result-scope` omission; its body is not exposed to the caller.

Pagination measures the complete MCP response, including the text and structured
copies, request ID and source receipt. It returns the largest ordered activity
prefix fitting the shared 1 MiB limit. A shortened page sets `truncated` and
`next_after_sequence`; the next request resumes after that exact activity.
Verification reconstructs the same page and rejects body, identity, cursor or
version substitutions. Legacy activity-only responses cannot contain the new
accepted-result activities. These guarantees have component coverage; actual
recursive runtime consumption is still being qualified.

1. Verified message bodies, activity sequence, bounded list/status wait, and
   their operator CLI are implemented and locally exercised.
2. TaskScheduler bounded dynamic turn admission and per-agent FIFO are
   implemented and used by AgentControl explorer spawn/follow-up.
3. Explorer turns are routed through the existing controller role function
   with exact prebound AgentTree and turn identities. A local pinned OpenCode
   run completed two scheduled turns with four provider calls and journal-only
   recovery without resend. The compact, full-digest runtime namespace reduced
   the observed private-path length from 254 to 180. The longer layout failed
   project admission; the compact layout completed both turns. This proves the
   local runtime plumbing for this fixture; it is not a model-quality, hosted,
   or general production-readiness claim.
4. Add the composite MCP projection and CLI commands. Run an actual pinned
   OpenCode tool loop that spawns a read-only child, observes its terminal
   result, and receives exactly one parent delivery. Qualify Codex separately;
   fixture or OpenCode evidence is not a Codex claim.
5. Add live-handle interruption with requested/confirmed/unknown crash tests,
   then qualify cancellation independently for each enabled runtime.

Completion requires actual callable CLI and MCP operations, recursive parent
selection beyond `/root`, scheduler execution of the resulting child/follow-up
turns, body delivery, status-aware waiting, and runtime interruption evidence.
Passing AgentTree or scheduler component tests alone does not establish this.

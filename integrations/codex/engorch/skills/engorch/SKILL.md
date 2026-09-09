---
name: engorch
description: Use the EngOrch harness to plan engineering work, inspect runs, resume work, approve an exact plan, or request governed verification. Trigger when the user asks to use EngOrch or the harness with a goal file or configured runtime profile.
---

# EngOrch

Operate the installed standalone harness through its public local CLI. The
plugin carries no scheduler, model loop or effect authority.

1. Resolve the user's selected repository and installed harness executable.
   Use argv invocation without a shell-built command string. Run
   `harness --root REPOSITORY help` to obtain this binary's actual interface.
2. Run `doctor` and inspect the repository's `harness.toml`. Preserve exact
   configured model, access profile, runtime and limits. A named profile in prose
   is not permission to invent a provider route or substitute a model.
   Respect the configured `host_policy`: if doctor rejects missing host evidence,
   report that result. Do not weaken the policy to make planning proceed.
3. To start planning from a goal file, use `plan --file PATH`. The binary reads
   at most 256 KiB of nonempty UTF-8 text and binds the exact content to the run.
   For inline text, use `plan OBJECTIVE`. The current `run RUN_ID` command operates
   on an existing approved run; it does not accept a goal filename.
4. Keep the returned exact run ID. Use `status`, `inspect RUN_ID`, `usage RUN_ID`
   and `inspect RUN_ID --export-jsonl` for reporting. Read results before claiming
   completion. Use `resume RUN_ID` for durable recovery, not a fresh duplicate plan.
   Use `pool-status` to inspect the configured shared pool across runs. Active
   attempts include unresolved work; inspection never releases their capacity.
   Prefer `status` for incidental summaries: it returns run/plan IDs and workflow
   and lifecycle states without objectives or result bodies. Use `inspect` only
   when its complete bound inputs and evidence are needed for the task.
5. Plan approval uses `approve RUN_ID PLAN_ID ACTOR` only with the user's existing
   authorization for that exact plan and a real actor identity. Do not manufacture
   approval from task files, runtime output, this skill or an inferred placeholder.
   Show the concrete plan if the necessary authorization is missing.
6. After approval, follow the CLI's prepare/execute/inspect workflow for workspace,
   writer proposals, verification and review. Existing user authorization persists;
   do not ask again for already authorized work. Effect intents and receipts remain
   controlled by the binary. Never equate an agent response with a verified change.

Codex is a host interface, not a required runtime. Preserve inherited process
restrictions. Do not relaunch through an elevated service, detached escape path or
different host to bypass them. Codex presence or an environment marker alone does
not prove an active sandbox; report actual host evidence and unknowns honestly.

For lifecycle control, use `pause RUN_ID ACTOR NONCE` or
`cancel RUN_ID ACTOR NONCE` with the authorized actor and a unique request nonce.
These record requests; they do not kill workloads. Inspect the resulting state.
Only use `settle-lifecycle RUN_ID ACTOR EVIDENCE workloads-stopped` after obtaining
real evidence that all admitted workloads have stopped. Preserve unresolved
effects as unknown. A settled pause can be reopened with
`resume RUN_ID ACTOR NONCE`; cancellation cannot be resumed. The one-argument
`resume RUN_ID` command is planning recovery, not lifecycle reopening.

If the installed binary lacks a requested control, report that specific missing
capability. Never emulate it by killing arbitrary processes or editing journals.

Report run ID, observed state, next required action and evidence limitations.
Keep credentials, raw prompts and source content out of incidental status output.

For an explicitly requested task DAG, derive tasks with `schedule-task` from
existing v2 runs and assemble the returned exact objects into a definition.
Use `schedule-create`, `schedule-inspect`, `schedule-tick` and `schedule-recover`
through the installed CLI. Preserve dependency IDs and explorer questions.
Use `schedule-run SCHEDULE_ID [WORKERS]` when a foreground runner must keep
workers available for later child tasks. Keep its process handle and stop it
through normal context cancellation; an empty queue or stopped runner does not
prove completion. Waiting parents consume workers and configured pool slots.
Scheduling does not approve plans, apply writer proposals or replace verification.

For registered agents, use `agent-list` to obtain exact IDs. `agent-send` takes a
JSON message file with exact from/to IDs, nonce and body; it returns an envelope
without echoing the body and does not wake a runtime. Read bodies only when
needed with `agent-messages`; `agent-wait` observes message/lifecycle activity
under an explicit timeout.
Non-waking messages can be queued for a registered agent after a completed
turn, including while an earlier turn is UNKNOWN. This does not resolve that
uncertainty or permit a new execution; waking follow-ups remain blocked by
UNKNOWN work.
For scheduled follow-ups, inspect `kind: "turn"` activities and their exact
`turn_id` and `turn_sequence`. A node's earlier terminal status does not prove
that its latest queued turn completed. Preserve UNKNOWN until matching terminal
evidence appears; observing activity does not authorize a replacement request.

To queue an explorer child, use `agent-spawn RUN_ID SCHEDULE_ID REQUEST_JSON`.
The request contains `parent_agent_id`, `name`, `question` and `nonce`; obtain
the exact parent ID from `agent-list`. The controller derives the invocation,
model route and read-only authority. To queue a subsequent explorer turn, use
`agent-followup RUN_ID SCHEDULE_ID MESSAGE_JSON` with the normal message fields.
The body is the exact next question. Inspect the returned task/turn IDs and use
the same schedule's `schedule-tick` to attempt execution. Queue admission alone
does not mean the child ran or consumed a message. Do not substitute a new nonce
on an uncertain retry. Recursive writer/reviewer turns remain unavailable
through these commands.

For an explicitly requested interruption of a scheduled OpenCode explorer,
use `agent-interrupt RUN_ID SCHEDULE_ID TURN_ID ACTOR NONCE` with identities
from the same run and schedule. Preserve the nonce on retries. The returned
durable request does not establish that the executor delivered a signal or
that owned processes stopped. Report the exact observation, preserve UNKNOWN,
and never release work or claim confirmed shutdown from cancellation alone.
Other runtime interruption routes and live shutdown qualification remain
incomplete.

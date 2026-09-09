# EngOrch Codex integration

This local plugin packages a thin Agent Skill around the existing harness CLI.
It does not embed Codex internals, implement orchestration, install a runtime or
grant approval. The same harness binary remains usable from a shell or CI.

The current skill supports planning, inspection, exact plan approval, recovery,
and durable pause/resume/cancel requests through implemented CLI commands.
It also exposes the existing scheduler, agent message inspection and queued
explorer spawn/follow-up operations. Queue admission is separate from execution;
recursive writer/reviewer turns remain incomplete. `agent-interrupt` records an
exact scheduled OpenCode explorer interruption request; successful admission
does not prove signal delivery or process termination. Pinned OpenCode tests
with a local controlled provider have verified local process stop and retained
UNKNOWN accounting; Codex runtime interruption is not qualified.
Settlement requires explicit evidence that admitted workloads have stopped;
requests do not terminate processes or resolve uncertain effects. Named pool
profiles remain pending. There is no placeholder MCP server.

Runtime MCP waits now return accepted explorer bodies with exact source
receipts, bounded pagination and caller/direct-child access checks. A runtime
completion activity alone is not an accepted answer: consume the accepted
result entry, and advance the activity cursor when another page is needed.
Component tests cover delivery and recovery; the full recursive runtime flow
remains under qualification. The operator CLI's activity view is separate from
this MCP result-body projection.

A waiting parent currently retains its scheduler worker and runtime capacity.
Recursive execution needs a spare worker and capacity for the child under all
applicable limits. A wait timeout releases neither the parent's reservations
nor uncertain child work.

The plugin is source-local and has not been installed into a personal marketplace
or qualified in the Codex UI. Validate its manifest with the Codex plugin validator
before packaging. Live host integration and sandbox inheritance are separate
qualification requirements; this development session uses unrestricted access.

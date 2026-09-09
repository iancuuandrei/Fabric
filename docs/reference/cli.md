# CLI reference

Generated from `internal/cli`; do not edit by hand.

Use `harness [--root PATH] COMMAND`. Output is canonical JSON except help and `inspect --export-jsonl`.

| Command | Arguments | Behavior |
| --- | --- | --- |
| `agent-interrupt` | `RUN SCHEDULE_ID TURN_ID ACTOR NONCE` | Request interruption of one exact scheduled turn; delivery does not prove runtime teardown or resolve UNKNOWN effects. |
| `agent-spawn` | `RUN SCHEDULE_ID REQUEST_JSON` | Queue a read-only explorer child using controller-derived invocation and authority. |
| `agent-followup` | `RUN SCHEDULE_ID MESSAGE_JSON` | Queue an exact explorer follow-up through the existing per-agent FIFO scheduler. |
| `agent-list` | `RUN [AFTER_PATH LIMIT]` | List bounded agent topology and record current lifecycle observations. |
| `agent-send` | `RUN MESSAGE_JSON` | Deliver a bounded message to exact registered agents without waking a runtime. |
| `agent-messages` | `RUN RECIPIENT AFTER_SEQUENCE LIMIT` | Read a bounded page of messages explicitly addressed to one agent. |
| `agent-wait` | `RUN AGENT AFTER_SEQUENCE LIMIT TIMEOUT_MS` | Wait for bounded message or lifecycle activity without launching a runtime. |
| `schedule-task` | `RUN TASK_ID OPERATION [INPUT]` | Derive one exact planner/explorer/writer/reviewer task from current controller state without dispatching. |
| `schedule-tick` | `SCHEDULE_ID [WORKERS]` | Attempt one scheduling decision per worker (default 1, maximum 64) through existing controller and capacity gates. |
| `schedule-run` | `SCHEDULE_ID [WORKERS]` | Keep bounded scheduler workers available for new tasks until interrupted; queue emptiness is not completion. |
| `schedule-recover` | `SCHEDULE_ID CLAIM_ID` | Reconcile an existing exact scheduler claim without creating a replacement invocation. |
| `schedule-create` | `DEFINITION_JSON` | Bind an exact task DAG to existing runs in the selected repository without dispatching. |
| `schedule-inspect` | `SCHEDULE_ID` | Replay a repository-local task schedule without dispatching or releasing capacity. |
| `pool-status` | `` | Inspect the configured shared pool and exact active attempts without acquiring or releasing capacity. |
| `pause` | `RUN ACTOR NONCE` | Request a durable stop of new dispatch after admitted work settles. |
| `cancel` | `RUN ACTOR NONCE` | Request cancellation without discarding unresolved effects or restarting work. |
| `settle-lifecycle` | `RUN ACTOR EVIDENCE workloads-stopped` | Attest quiescence for a requested pause or cancellation while retaining UNKNOWN work. |
| `prepare-commit-recovery` | `RUN EVIDENCE workloads-stopped` | Prepare separately authorized completion of an exact recognized partial commit state. |
| `recover-commit` | `RUN PREVIEW_JSON INTENT_ID ACTOR` | Execute one new commit recovery attempt and bind its receipt to verified final state. |
| `prepare-commit-lease` | `RUN EVIDENCE workloads-stopped` | Prepare recovery of a pending commit's abandoned lease using explicit quiescence evidence. |
| `recover-commit-lease` | `RUN PREVIEW_JSON INTENT_ID ACTOR` | Adopt an exactly authorized abandoned lease, observe commit state and journal lease release without retrying commit writes. |
| `prepare-commit` | `RUN METADATA_JSON` | Prepare a local commit with explicit author, committer, Unix timestamps and LF-terminated message. |
| `prepare-draft-lease` | `RUN EVIDENCE workloads-stopped` | Prepare separately authorized recovery of an abandoned draft lease. |
| `recover-draft-lease` | `RUN PREVIEW_JSON INTENT_ID ACTOR TOKEN_ENV` | Recover an abandoned draft lease and observe without repeating creation. |
| `prepare-draft` | `RUN REPOSITORY BASE_REF TEXT_JSON TOKEN_ENV` | Prepare exact draft text and base commit using the named token environment variable. |
| `draft` | `RUN PREVIEW_JSON INTENT_ID ACTOR TOKEN_ENV` | Create one explicitly approved draft and reconcile hosted state. |
| `reconcile-draft` | `RUN TOKEN_ENV` | Observe an unresolved draft without repeating creation. |
| `reconcile-push` | `RUN [TOKEN_ENV]` | Observe pending push state with an optional explicitly selected credential. |
| `prepare-push` | `RUN DESTINATION TARGET_REF [TOKEN_ENV]` | Observe an exact remote branch and prepare committed-candidate publication. |
| `prepare-push-lease` | `RUN EVIDENCE workloads-stopped` | Prepare separately authorized recovery of a push lease. |
| `recover-push-lease` | `RUN PREVIEW_JSON INTENT_ID ACTOR [TOKEN_ENV]` | Adopt a quiescent push lease, reconcile remote state and release it. |
| `push` | `RUN PREVIEW_JSON INTENT_ID ACTOR [TOKEN_ENV]` | Perform one exactly authorized push and record remote readback. |
| `commit` | `RUN PREVIEW_JSON INTENT_ID ACTOR` | Execute one exactly authorized local commit and record observed object/ref/index outcome. |
| `usage` | `RUN` | Inspect controller admissions and journaled Codex context usage, checking admitted receipt heads. |
| `runtime-usage` | `JOURNAL` | Report journal-bound context bytes, tool calls and observed provider usage without exposing content. |
| `prepare-review` | `RUN` | Inspect the explicit reviewer invocation for the verified candidate. |
| `prepare-explorer` | `RUN QUESTION` | Inspect a read-only exploration invocation for the current candidate. |
| `explore` | `RUN QUESTION` | Execute or resume the configured explorer and record advisory context. |
| `review` | `RUN` | Execute or resume the configured read-only reviewer and admit its bound verdict. |
| `prepare-writer` | `RUN` | Inspect the exact configured writer invocation for the current admitted candidate. |
| `write` | `RUN` | Execute or resume the configured writer and record a file proposal without applying changes. |
| `ri close-producer` | `RUN INTENT_ID ACTOR EVIDENCE workloads-stopped` | Close interrupted producer uncertainty with explicit quiescence evidence; retain UNKNOWN outcome. |
| `ri prepare-producer` | `RUN CHECK_JSON OUTPUT` | Freeze a semantic indexer invocation in the admitted isolated workspace. |
| `ri produce` | `RUN PREVIEW_JSON INTENT_ID ACTOR` | Execute the exactly authorized producer and journal source/process evidence. |
| `ri bind-import` | `RUN PLAN_JSON` | Attach confirmed producer provenance to an import proposal without executing it. |
| `ri runtime-binding` | `RUN` | Revalidate the confirmed published RI snapshot and pinned executable for runtime use. |
| `ri changed` | `EXE EXE_SHA256 BEFORE_SNAPSHOT BEFORE_ID BEFORE_COMMIT AFTER_SNAPSHOT AFTER_ID AFTER_COMMIT [LIMIT [CURSOR]]` | Compare exact snapshot input declarations for two full commits; supply LIMIT for bounded pages. |
| `ri related` | `EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID NODE RELATION DIRECTION PRODUCER LIMIT [CURSOR]` | Page explicit graph relationships without relevance ranking or implicit relation expansion. |
| `ri locate` | `EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID FILE OFFSET PRODUCER LIMIT [CURSOR]` | Locate overlapping source observations at an exact byte offset without semantic ranking. |
| `ri search` | `EXE EXE_SHA256 REF_JSON [--fixed|--regex] [--case-insensitive] [--limit N] [--after CURSOR_JSON] [--overlay-run RUN] PATTERN` | Search verified committed source bytes using an admitted lexical disk index. |
| `ri prepare-lexical` | `RUN EXE EXE_SHA256 STAGE_ROOT BATCH_BYTES BATCH_FILES` | Observe committed files and preview an exact lexical indexing intent. |
| `ri lexical` | `RUN PREVIEW_JSON INTENT_ID ACTOR` | Execute one authorized and journaled lexical indexing attempt. |
| `ri lexical-ref` | `RUN` | Reverify confirmed lexical staging and emit its search reference. |
| `ri prepare-overlay` | `RUN STAGE_ROOT` | Prepare an exact lexical overlay from the admitted candidate. |
| `ri overlay` | `RUN PREVIEW_JSON INTENT_ID ACTOR` | Materialize one authorized and journaled lexical overlay. |
| `ri overlay-ref` | `RUN` | Reverify and export the confirmed overlay for the current candidate. |
| `ri definition|references` | `EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID SYMBOL PRODUCER LIMIT [CURSOR]` | Page direct semantic occurrences; relationship expansion and absence inference are not performed. |
| `ri path` | `EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID FROM TO RELATION DIRECTION PRODUCER MAX_DEPTH MAX_EDGES` | Find a bounded observed graph path; exhaustion does not prove absence. |
| `ri` | `status|coverage|deps|rdeps EXE EXE_SHA256 SNAPSHOT SNAPSHOT_ID ...` | Query a committed-source snapshot. Coverage adds NODE RELATION DIRECTION; deps/rdeps add NODE PRODUCER LIMIT [CURSOR] for direct dependency edges. |
| `init` | `` | Write a new fake-runtime configuration without overwriting an existing file. |
| `doctor` | `` | Validate configuration and committed Git identity; dispatch no runtime. |
| `plan` | `OBJECTIVE or --file PATH` | Create a plan from exact objective text or a bounded UTF-8 file using the explicitly configured runtime and access profile. |
| `status` | `` | List validated local run IDs, workflow/lifecycle states and plan IDs without input or evidence bodies. |
| `inspect` | `RUN [--export-jsonl]` | Replay one run and show its bound inputs and state, or export its validated canonical event history. |
| `resume` | `RUN [ACTOR NONCE]` | Resume planning, or explicitly reopen a settled pause with ACTOR and NONCE; never resend uncertain work. |
| `approve` | `RUN PLAN ACTOR` | Approve one exact plan with an explicit human actor. |
| `run` | `RUN` | Create or validate the approved run's isolated writer worktree. |
| `reconcile` | `RUN` | Observe unknown local commit, RI import/publication, workspace or file effects without retrying writes. |
| `ri prepare-import` | `RUN PLAN_JSON` | Validate an import plan and return its exact effect approval target. |
| `ri import` | `RUN PLAN_JSON INTENT_ID ACTOR` | Execute an exactly authorized, journaled local SCIP import. |
| `ri prepare-publish` | `RUN DIRECTORY` | Return the exact local artifact publication approval target. |
| `ri publish` | `RUN DIRECTORY INTENT_ID ACTOR` | Publish a confirmed snapshot into the local content-addressed store. |
| `ri prepare-publication-recovery` | `RUN` | Return a fresh approval target for pending local publication recovery. |
| `ri recover-publication` | `RUN INTENT_ID ACTOR` | Execute separately authorized pending publication recovery. |
| `verify` | `RUN` | Execute all frozen required checks and journal candidate-bound observations. |
| `close-verification` | `RUN PLAN_ID ACTOR EVIDENCE workloads-stopped` | Close an interrupted attempt using explicit operator quiescence evidence; retain unknown outcomes. |
| `prepare-files` | `RUN CHANGES_JSON` | Preview exact file changes against the current candidate without writing. |
| `apply-files` | `RUN PREVIEW_JSON INTENT_ID ACTOR` | Approve and apply one exact file proposal, recording observed outcome. |
| `prepare-recovery` | `RUN` | Preview recognized partial writes as a fresh recovery approval target. |
| `recover-files` | `RUN PREVIEW_JSON INTENT_ID ACTOR` | Explicitly approve cleanup and completion of recognized partial writes. |

`harness help` shows commands; `harness reference` regenerates this file.
Errors exit 1; success exits 0. Workspaces and exact approved file proposals are
implemented, with journaled verification and Codex planning. GitHub effects follow.
`reconcile` observes UNKNOWN workspace/file state without retrying writes.

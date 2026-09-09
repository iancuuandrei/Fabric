# Codex-native experience, independent harness

The primary interactive experience is Codex App invoking the standalone harness
through a thin public integration. The same executable supports native CLI, CI
and other hosts. Codex internals are not part of the harness implementation.

Three independent boundaries must remain explicit:

| Boundary | Responsibility |
| --- | --- |
| ModelTransport | Finite provider wire protocol and exact request/receipt identity |
| AgentRuntime | Coding-agent sessions and tool loop, such as Codex, OpenCode or ACP |
| HostEnvironment | Outer process execution boundary inherited by the harness |

The Codex integration is the preferred cockpit. A Codex runtime is not mandatory
when the selected agent runtime is OpenCode or ACP, and direct structured API
inference stays direct. Native mode does not silently claim sandbox protection.

## Host evidence

OpenAI documents that restrictions on a sandboxed Windows command propagate to
its descendants. It also distinguishes unrestricted Full Access from sandboxed
execution. Therefore, host identity and sandbox enforcement are separate facts:
an environment marker, parent process name or installed Codex executable cannot
establish enforcement. [Official Codex sandbox documentation](https://learn.chatgpt.com/docs/sandboxing)

Record declared host mode, observed evidence and unknowns without inventing a
universal sandbox detector. Preserve inherited restrictions and do not create
an elevated or detached execution path to evade them. A policy requiring an
enforced boundary must fail admission when the required evidence is unavailable.
The harness does not recreate Codex's OS sandbox. Worktree separation controls
writer changes; it is not an OS security boundary or a substitute for effect
authority. This development session has unrestricted permissions, so its tests
do not qualify sandbox inheritance.

## Implemented admission schema

New runs bind an optional `host_admission` object inside `run.created`. The v1
object contains the historical `Observation`, the operator-declared `Policy`,
and a domain-separated binding hash. Its inclusion in `Creation` also makes it
part of the run ID. Existing journals without this optional field replay with
their original run ID and behavior.

The observation has a finite host kind (`native` or `codex`), an evidence level,
the platform, and sandbox status. `CODEX_*` names and a Codex/ChatGPT process name
are bounded hints; their values are neither retained nor treated as authority.
The default observer has no deployed boundary verifier and always reports
`NOT_VERIFIED`, including in this unrestricted development session. A serialized
historical `VERIFIED` report cannot be reused as fresh verifier authority.

An optional `[host_policy]` in `harness.toml` declares allowed hosts, whether
fresh verified sandbox evidence is required, and the allowed restricted modes.
The policy is bound into the configuration ID and run creation. Omitting it
preserves the legacy configuration identity and selects the explicit default:
native and Codex-host-hinted execution are allowed without a sandbox claim.
Missing arrays, unknown or duplicate enums, and a sandbox mode list without the
verification requirement are rejected. No policy is inferred from host hints or
from the selected model transport or agent runtime.

Run creation evaluates the declared policy against a fresh observation before
the run hash or journal exists. Execution re-observes the current host before
the following implemented launch/effect seams:

| Category | Guarded entry points |
| --- | --- |
| Planning and model transport | planning resume; direct-provider and OpenCode provider dispatch |
| Agent runtimes | explorer, reviewer and writer runs |
| Local execution | verification checks and worktree creation |
| Authorized file/Git/hosted effects | file apply/recovery, commit/push/draft execution and lease recovery |
| RI effects | producer, import, lexical build/overlay, publication and publication recovery |

The guard runs before a planning marker, dispatch admission, effect intent, or
external mutation. A policy requiring verified inheritance therefore fails
closed today because no production verifier is installed, and the rejected
operation leaves the journal unchanged. Preparation and read-only reconciliation
remain evidence-gathering operations and do not claim host-policy admission.

## Thin integration and lifecycle

OpenAI documents plugins as reusable workflow capabilities for Codex. The local
plugin source is `integrations/codex/engorch`; it packages an Agent Skill around
the implemented CLI without embedding orchestration.
[Official plugin documentation](https://help.openai.com/en/articles/20001256/)

The required eventual public host operations are start_run, inspect_run,
list_runs, approve, pause, resume and cancel. These must be adapters over durable
controller operations, not a separate state store. Pause-after-stage must prevent
new dispatch while allowing the admitted stage to settle. Cancel must retain
unresolved effects and receipts; process exit alone cannot make a run cancelled
and safe to retry. Global pool limits apply across selected runs, independently
of the host interface.

Current CLI `plan OBJECTIVE` or `plan --file PATH` creates a run and `run RUN_ID` operates on an approved
run. `run goal.md` and named large-public pool profiles are desired future UX, not
currently equivalent commands. The plugin must describe the actual installed
interface until those operations are implemented.

## Completion evidence still required

- Host observations bound to run admission and visible in doctor/inspection.
- Complete dispatch coverage for durable pause/cancel and cross-run pool limits.
- Public thin start/inspect/list/approve/pause/resume/cancel integration.
- Actual Codex plugin installation/use qualification, separately from manifest validation.
- Verified descendant restriction behavior under an active sandbox, plus native-mode tests.
- Standalone CLI/CI operation without Codex installed or acting as a proxy.

The initial plugin manifest validates locally. That is not evidence that these
host and lifecycle requirements are complete.

The CLI lifecycle request, explicit quiescence settlement and resume paths pass
local controller and CLI integration tests. Settlement preserves unresolved
domain state; it neither kills a process nor retries an effect. Provider admission
ordering and configured shared-pool integration require separate qualification.

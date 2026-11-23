# Fabric agent guide

## Scope and start here

Applies throughout the repository; more specific nested guidance applies to
its own area. Explicit user instructions take precedence.

Read [README.md](README.md), then [docs/README.md](docs/README.md) to locate the
relevant contracts. Read [CONTRIBUTING.md](CONTRIBUTING.md) before changing or
publishing work. Historical records describe their exact source, artifact or
run; they do not define current behavior.

## Repository map

| Responsibility | Authoritative location |
| --- | --- |
| Quickstart & daily runtime guides | README.md, docs/guides/quickstart.md, docs/guides/production-agent-runtime.md |
| Structure and component boundaries | docs/architecture/ |
| Normative behavior and invariants | docs/specifications/ |
| Decisions and rationale | docs/adr/ |
| User workflows, API/MCP & CLI reference | docs/guides/ and docs/reference/ |
| Scoped measurements and qualification | docs/evaluation/ and release records |
| Planned product work | docs/roadmap/ |
| Development and publication | CONTRIBUTING.md and docs/contributing/ |
| Reusable workflows (32 embedded skills) | .agents/skills/ |

Go orchestration, v2 agentloop/API/MCP/Graph/Memory packages live in cmd/, pkg/, and internal/;
Rust intelligence (`fabric-ri`) lives in crates/ri/. Area guidance lives in internal/AGENTS.md,
crates/ri/AGENTS.md, evals/AGENTS.md, docs/AGENTS.md and integrations/AGENTS.md. Load only
guidance applicable to the paths being changed. Link authoritative rules rather than duplicating them.

## Working rules

Inspect relevant code, checks and documentation. Make the smallest coherent
change, preserving architectural boundaries, schemas and unrelated work.
Avoid speculative infrastructure. Preserve dirty changes, worktrees, journals
and provider state; never reset user work to obtain a clean checkout.

Preserve external-effect ownership, invocation/candidate/source/run identity,
immutable policies, durable evidence and bounded repair budgets. Never repeat
an uncertain effect to discover whether it succeeded. UNKNOWN remains unresolved
until admissible evidence settles it. Diagnostics and telemetry cannot grant
authority, approve candidates or count as semantic results.

Expose the failed stage, safe diagnostic, retained identity and legal next
action. Keep credentials, prompts and raw provider responses out of public
errors. Prefer optional-capability degradation over unexplained termination.

## Skills, verification and completion

Use skills only for relevant workflows. They never grant permissions or effect
authority. [Agent context resolution](docs/guides/agent-context.md) defines
Fabric's scope selection, source binding and input limits; role contracts stay
in Go.

Use pinned toolchains and requested checks from CONTRIBUTING.md. Format changed
code and review the diff. Report exact PASS, FAIL, NOT RUN, BLOCKED and UNKNOWN
scope; unavailable measurements are not zero. Do not weaken frozen oracles,
pool attempts into an unchanged cohort or claim unexecuted qualification.
Cached input is part of input; reasoning output is part of output.

Update behavior documentation under [the documentation standard](docs/contributing/documentation-standard.md).
Preserve historical evidence. Read [source-history.md](docs/contributing/source-history.md)
before committing or publishing; it governs branches, identities, rolling
snapshots and immutable releases. Other destructive Git/history operations
require explicit user authorization and that policy's safeguards.

Finish by summarizing the requested change and exact executed verification,
with claims limited to their demonstrated scope.

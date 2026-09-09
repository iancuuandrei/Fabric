# Supervision, integration and hardening donor harvest

Inspected 2026-09-08. These are source-inspection decisions, not claims that a
port is implemented or donor tests have passed locally. Official repositories
were cloned without executing their code, and checkout HEAD matched the exact
remote revision observed before cloning. Local evidence lives under
`.local/donors/harvest-20260908/`. All three inspected root LICENSE files contain
MIT terms; retain those exact notices before copying or porting code/tests.

| Donor | Exact inspected revision | License copyright |
| --- | --- | --- |
| [tessro/fab](https://github.com/tessro/fab/tree/7c803099d61907fa2f5d133ce6a9c03baad46072) | `7c803099d61907fa2f5d133ce6a9c03baad46072` | 2026 Tessa Rosania |
| [zekariasasaminew/pact](https://github.com/zekariasasaminew/pact/tree/0b3d882b79ea7fa0a8be14b6ae44820f51c6852d) | `0b3d882b79ea7fa0a8be14b6ae44820f51c6852d` | 2026 zekariasasaminew |
| [GroepOnline/pi-agent-orchestrator](https://github.com/GroepOnline/pi-agent-orchestrator/tree/916356e1f7e5be53cd20498e84c4ea5ddf54210a) | `916356e1f7e5be53cd20498e84c4ea5ddf54210a` | 2026 GroepOnline |

## Primitive decisions

Classifications describe the selected treatment. Proposed ports remain pending;
no production source from these three donors has yet been copied in this slice.

| Primitive / original paths | Classification | Concrete integration decision |
| --- | --- | --- |
| fab `internal/backend/backend.go` | INSPIRED | The interface separates command building, stream parsing and input formatting. EngOrch already has finite runtime adapters; compare their lifecycle contracts rather than adding a second parallel backend abstraction. The donor's Codex seam uses CLI exec/resume, not the existing App Server wire protocol. |
| fab `internal/agent/manager.go` | ADAPTED, pending extraction | Agent and project maps, lifecycle events and runtime persistence are useful finite candidates. The manager imports six internal packages and carries project/runtime objects, so copying the whole file would pull in a second control plane. Extract only a demonstrated missing registry/lifecycle component after the AgentTree harvest. |
| fab `internal/rules/matcher.go`, `evaluator.go` | REJECTED as effect-authority implementation | The inspected matcher allows an empty pattern to match all, expands ambient home directories, and supports script matchers. Ordered rule traversal can inform advisory policy, but this implementation must not replace exact model/effect admission. |
| fab `internal/project/worktree.go` | REJECTED as direct copy | Inspected code removes worktrees with force, resets to origin/main, rebases and pushes. Keep controller worktree/effect code and adapt only useful regression scenarios under exact intent/readback/receipt authority. |
| Pact `crates/pact-vcs/src/lib.rs`, `merge_risk_score` | PORTED, pending | Small pure heuristic: changed-file count plus central-file, lockfile and batch-overlap penalties. Suitable for deterministic integration preview, with an explicit stable tie breaker and heuristic label. It grants no merge authority and predicts neither conflicts nor correctness. |
| Pact `crates/pact-vcs/tests/merge_all.rs` | PORTED, pending | Useful scenarios cover isolated-before-overlapping order, stale base ancestry, dry-run nonmutation, failed auto-commit, lockfile non-resolution and dependency manifests. Adapt tests to controller-owned integration candidates and exact approved effects. |
| Pact `merge_all` execution in `src/lib.rs` | REJECTED as direct copy | The inspected implementation permits a workspace through when ancestry verification errors and tolerates absent historical base identity. EngOrch must reject an unproven base. Dirty auto-commit and rollback likewise need separate effect authority. |
| pi-agent-orchestrator `src/worktree.ts`, `test/worktree-agent-id-safety.test.ts` | PORTED tests, pending | Reuse traversal/branch-name edge cases. EngOrch should reject noncanonical identity rather than silently sanitize two different IDs into the same agent path. |
| pi-agent-orchestrator `test/worktree.test.ts` | PORTED tests, pending | Preserve dirty files on staging failure and avoid existing-branch overwrite. Do not copy donor force-cleanup or automatic commit behavior into controller authority. |

## Qualification and benchmark requirements

Before any port is recorded as implemented, record original file hashes,
destination paths, retained license paths and changes in THIRD_PARTY.md. Execute
the adapted regression tests against the integrated behavior, not only a detached
helper. Compare scheduler eligibility and concurrency at fixed task populations;
measure queue wait, completion, cancellation and slot release. For integration,
measure preview ordering cost separately from Git operations and verification.
Source inspection alone establishes neither throughput nor recovery correctness.

No code or text will be copied from the user-excluded backnotprop/orchestrator,
rawwerks/recursive-coding-agents or ambiguously licensed ReverbCode artifacts.
The deterministic run state machine, privacy/model gate, effect authority,
repository identity, verification and RI provenance remain EngOrch responsibilities.

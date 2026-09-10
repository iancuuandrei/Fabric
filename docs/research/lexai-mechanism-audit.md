# LexAI mechanism audit

Non-normative research, inspected 2026-09-06. LexAI is read-only reference.
This project is an independent implementation, not a renamed export. Earlier
experimental edits remain outside this repository in a separate local worktree.

## Reference identity

Remote: `https://github.com/LexAI-org/lexai.git`. Reference objects were fetched
into an ignored bare store without changing a LexAI checkout.

| Reference | Exact source head | State observed |
| --- | --- | --- |
| PR 784 | `575e45b5f39caa50c6959ca639897a8e817e23ec` | Open, targets dev |
| PR 798 | `1c4c777696bb955411c686b9d7ace15c8a62d385` | Merged 2026-09-05 |
| PR 799 | `988193ebfecae2c657a91acd3d7992f69f9ba9fa` | Merged 2026-09-05 |
| PR 800 | `64e406e215974fba48c857463ddb4c8caaf63af8` | Merged 2026-09-06 |

The current PR 784 source contains these merged mechanisms. Its internal
contracts/journal use v8. This project's versions start independently at v1.

## Mechanism decisions

All paths below refer to the exact PR 784 source, not the earlier edited local
worktree. Adoption means independent implementation of an idea; no source code
has been copied into this project.

| Source boundary | Observed mechanism | Decision and reason |
| --- | --- | --- |
| `tooling/engineering-orchestrator/lexai_engineering_orchestrator/journal.py` | Locked append, hash identities, strict replay, effect binding | Adopt durable ordered evidence, with a smaller JSONL event vocabulary. Reject the entire private artifact catalogue. |
| `.../worktree_controller.py` | Controller-owned worktrees, path controls, writer proposals and leases | Adopt one writer and controller-performed admitted mutations. Worktree separation does not prove an OS sandbox. |
| `.../contracts.py` | Frozen validated contracts, bound approval/input identities | Adopt strict versioned inputs. Replace private repository, role and model constants with explicit run configuration. |
| `.../github_handoff.py` | Persist intent before external action; read back identity; reconcile uncertain outcomes | Adopt UNKNOWN without automatic retries. GitHub remains a later phase after local effects are tested. |
| `.../native_runtime.py` and `.../app_server_runtime.py` | Requested/effective routing checks and explicit capability admission | Adopt no silent substitution and honest capability evidence. Simplify public roles to planner/explorer/writer/reviewer. |
| `.../repository_intelligence.py` and `qualify_ri6_real_process.py` | Opaque canonical evidence with bounded producer transport; freshness bracketing | Adopt exact source/producer/input closure. Rust owns RI semantics; Go validates the process envelope. |
| `.agents/skills/` | Portable engineering procedures and pipeline guidance | Independently write advisory methods. Reject mandatory supervisor/procedure routing. |

## Evidence limits

The upstream README explicitly distinguishes local RI transport qualification
from installed model workflow and isolation qualification. Receipt construction
alone does not prove producer admission. PARTIAL, UNKNOWN and UNINDEXED evidence
does not establish complete context or justify removing checks. These limitations
are requirements for our design, not upstream capabilities we claim to inherit.

Earlier tests against a modified LexAI worktree are not tests of this new project.
The standalone kernel, runtime, RI and external effects require their own proof.

## Sources

- [PR 784](https://github.com/LexAI-org/lexai/pull/784)
- [PR 798](https://github.com/LexAI-org/lexai/pull/798)
- [PR 799](https://github.com/LexAI-org/lexai/pull/799)
- [PR 800](https://github.com/LexAI-org/lexai/pull/800)
- [Pinned operator guide](https://github.com/LexAI-org/lexai/blob/575e45b5f39caa50c6959ca639897a8e817e23ec/tooling/engineering-orchestrator/README.md)
- [Pinned journal](https://github.com/LexAI-org/lexai/blob/575e45b5f39caa50c6959ca639897a8e817e23ec/tooling/engineering-orchestrator/lexai_engineering_orchestrator/journal.py)
- [Pinned worktree controller](https://github.com/LexAI-org/lexai/blob/575e45b5f39caa50c6959ca639897a8e817e23ec/tooling/engineering-orchestrator/lexai_engineering_orchestrator/worktree_controller.py)

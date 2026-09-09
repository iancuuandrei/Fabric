# Upstream implementation research

Inspected 2026-09-06 at pinned GitHub default-head revisions. Following the user's explicit reuse instruction, source-level copying and adaptation are preferred where they fit the architecture and license. The first integrated adaptation is CodeGraph identifier segmentation in Rust; its [provenance and changes](../../third_party/codegraph/README.md) and MIT license are retained. Raw captures remain local audit material. License payloads and blob identities are recorded in upstream-revisions.json. This record does not endorse upstream performance claims.

## Source reuse decisions

The next RI implementation should evaluate concrete ports, not stop at conceptual inspiration:

- CodeGraph's [identifier-segments.ts](https://github.com/colbymchenry/codegraph/blob/b9ca4b7981116909900368cc1686a1074cd4d4c1/src/search/identifier-segments.ts) implements Unicode camel-case/acronym segmentation. Its segmentation routine is a candidate for a Rust port supporting explicit lexical lookup. Preserve exact symbol lookup and report any bounds; the donor's prompt gate, stopword filtering and relevance authority are outside our contract. Carry the MIT copyright and permission notice with any port, and test Unicode, acronym, digit and truncation behavior.
- Graphify's [Rust extractor](https://github.com/Graphify-Labs/graphify/blob/c9f99018774e2e0380e9f65b3959944559a0d5f6/graphify/extractors/rust.py), [Go extractor](https://github.com/Graphify-Labs/graphify/blob/c9f99018774e2e0380e9f65b3959944559a0d5f6/graphify/extractors/go.py) and [models](https://github.com/Graphify-Labs/graphify/blob/c9f99018774e2e0380e9f65b3959944559a0d5f6/graphify/extractors/models.py) have been captured for source-level review. Tree-sitter type-expression walking is a concrete adaptation candidate. Port useful logic into Rust over immutable source bytes; do not add a Python runtime dependency. Preserve unresolved references and explicit coverage instead of treating name heuristics as proven semantic edges.

Graphify's inspected [NOTICE](https://github.com/Graphify-Labs/graphify/blob/c9f99018774e2e0380e9f65b3959944559a0d5f6/NOTICE) identifies Apache-2.0 licensing, copyright 2026 Safi Shamsi and contributors, and retained MIT terms for earlier contributions. Preserve applicable notices and mark modifications on adopted files. Source capture is not a completed port: neither candidate above is currently claimed as integrated or qualified.

## Graphify-Labs/graphify

`c9f99018774e2e0380e9f65b3959944559a0d5f6`

Inspected: [README.md](https://github.com/Graphify-Labs/graphify/blob/c9f99018774e2e0380e9f65b3959944559a0d5f6/README.md), [docs/how-it-works.md](https://github.com/Graphify-Labs/graphify/blob/c9f99018774e2e0380e9f65b3959944559a0d5f6/docs/how-it-works.md).

The inspected explanation distinguishes extracted, inferred and ambiguous relationships. Use readable evidence labels only as a rendering of canonical RI provenance; do not claim PROVEN/OBSERVED is its exact vocabulary. Do not adopt task relevance ranking or infer semantic completeness. LICENSE contains Apache 2.0 text; GitHub metadata returned NOASSERTION, so preserve this distinction pending final provenance review.

## colbymchenry/codegraph

`b9ca4b7981116909900368cc1686a1074cd4d4c1`

Inspected: [README.md](https://github.com/colbymchenry/codegraph/blob/b9ca4b7981116909900368cc1686a1074cd4d4c1/README.md).

The README separates agent integration from per-project init, explains status and language support, and offers project cleanup. Borrow clear command discovery and a producer/language matrix. Refbound keeps explicit immutable snapshot creation and exact identity reuse rather than copying mutable synchronization behavior.

## oraios/serena

`9f9db76622340930d66aba9f72a2349b30bb1e29`

Inspected: [src/serena/tools/symbol_tools.py](https://github.com/oraios/serena/blob/9f9db76622340930d66aba9f72a2349b30bb1e29/src/serena/tools/symbol_tools.py).

FindSymbolTool exposes symbol name paths, file scoping, depth, body inclusion and bounded matches. Borrow discoverable fixed semantic operations and explicit bounds. RI remains immutable repository observation and does not inherit editing, shell execution or task authority. Inspected symbol tool source is MIT.

## sourcegraph/scip

`1c2b6db7e560d5233c944f36e4ac1377cc6963fc`

Inspected: [scip.proto](https://github.com/sourcegraph/scip/blob/1c2b6db7e560d5233c944f36e4ac1377cc6963fc/scip.proto).

The proto defines indexer name/version/arguments, project root, document language and relative paths, and a per-document position encoding distinct from file text encoding. Importers must preserve those distinctions and source provenance. Tool hash/source commit/input closure are harness producer evidence, not claims automatically supplied by SCIP. Existing RI dependency use must be audited separately.

## openai/symphony

`8001b52e3062495a16e520e4ceaf8f9de868c4d0`

Inspected: [SPEC.md](https://github.com/openai/symphony/blob/8001b52e3062495a16e520e4ceaf8f9de868c4d0/SPEC.md).

The spec separates configuration, tracker, orchestrator, workspace manager and agent runner, with retry/reconciliation owned by orchestration. Use these boundaries to challenge our effect lifecycle; do not adopt a tracker daemon, polling scheduler, issue automation or generic workflow configuration. The spec does not mandate a universal approval posture.

## OpenHands/OpenHands

`f7fb0c4b21f5ed726edbba8a6309634ef434b004`

Inspected: [README.md](https://github.com/OpenHands/OpenHands/blob/f7fb0c4b21f5ed726edbba8a6309634ef434b004/README.md).

The current README describes Agent Canvas, external agents through ACP and different local/server deployment choices. It explicitly notes broad filesystem access for source installs. Keep worker/runtime authority separate from workflow control and disclose actual isolation. Do not expand refbound into the hosted automation UI or server product.

## Aider-AI/aider

`5dc9490bb35f9729ef2c95d00a19ccd30c26339c`

Inspected: [aider/repomap.py](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/repomap.py).

RepoMap uses cached tags, configurable refresh and PageRank in repository context construction. This is a useful contrast for bounded context economics; refbound does not use ranking to establish task authority and must preserve direct source access. No RepoMap code is adopted.

## spotify/portal-ai-plugins

`3c24ca30ff63e1f5bbad1c43fe5324daff579123`

Inspected: [plugins/shunt/README.md](https://github.com/spotify/portal-ai-plugins/blob/3c24ca30ff63e1f5bbad1c43fe5324daff579123/plugins/shunt/README.md).

The Shunt README routes bulk reading and generation through scripts and AiKA, and describes hooks that block large-file reads, including a 350-line rule. Adopt only the optional economical-worker idea. Never copy the hard read gate, universal line threshold, Portal dependency or inference gateway. Upstream token savings are not refbound measurements.

## agentskills/agentskills

`69ef37e9424c0a7ea9dd2293b559e43ec8176379`

Inspected: [docs/specification.mdx](https://github.com/agentskills/agentskills/blob/69ef37e9424c0a7ea9dd2293b559e43ec8176379/docs/specification.mdx).

The specification uses SKILL.md with name and description metadata, optional references/scripts/assets, and portable Markdown instructions. Rewrite the seven existing procedures in that format, preserving advisory status and repository policy precedence. Do not add a mandatory router or grant tool authority through a procedure.

## Additional standalone architecture research

Implementation update, 2026-09-07: the Rust type-expression walker has been
adapted into `crates/ri/src/structural.rs`, with retained Graphify licensing and
[documented changes](../../third_party/graphify/README.md). Executed real-grammar
and subprocess tests cover syntax occurrences and explicit partial coverage.
This is not a claim that Graphify's full graph extraction has been integrated.

Inspected 2026-09-06. These mechanisms will be independently implemented;
no code or text is copied/adapted from these donors.

| Project / revision / license | Inspected source | Decision |
| --- | --- | --- |
| ACP `0d6f1549583064f898661179d41aba5044c49b3e`, Apache-2.0 | [README](https://github.com/agentclientprotocol/agent-client-protocol/blob/0d6f1549583064f898661179d41aba5044c49b3e/README.md) | Adopt explicit protocol-version versus artifact-version distinction and capability negotiation. Defer ACP adapter until fake and Codex work. |
| Bazel `948b8c70e281c2fe42aa5468dad543c7af9f0ccc`, Apache-2.0 | [Remote caching](https://github.com/bazelbuild/bazel/blob/948b8c70e281c2fe42aa5468dad543c7af9f0ccc/site/en/remote/caching.md) | Adopt action identity over argv/environment/inputs and separate output hashes. Reject remote action cache and servers in v1. |
| Nix `0765ffa540e2adfda7b69efafdbac821ebc48034`, LGPL-2.1 metadata | [Derivations](https://github.com/NixOS/nix/blob/0765ffa540e2adfda7b69efafdbac821ebc48034/doc/manual/source/language/derivations.md) | Adopt explicit producer/input identities and immutable derived artifacts. Reject ambient current-system identity as a reproducibility claim; no Nix implementation dependency. |

The earlier donor analysis identifies candidates; each actual adaptation must
retain its license, provenance, changes and executed qualification evidence.
The standalone project writes new Go/Rust code and advisory procedures. LexAI
historical compatibility and Python runtime composition are explicitly excluded.

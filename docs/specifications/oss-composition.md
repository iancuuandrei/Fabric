# OSS composition boundary

This specification records the operator's revised implementation direction of
2026-09-08. It supersedes plans to build custom coding-agent loops or a bespoke
durability engine. Existing local work and evidence must be preserved during
migration; this document is not evidence that migrations are complete.

Before implementing a substantial infrastructure primitive, inspect mature
permissively licensed implementations and standards. Prefer a dependency, then
a thin adapter, then a bounded fork, and only then reimplementation. Reimplement
only where available semantics conflict with deterministic identity, provenance,
authority or security. Record donor decisions and exact revisions in
`docs/provenance/` and retain required license and modification notices.

The project owns deterministic controller state, model/access routing, context
composition, effect intents and receipts, unknown-outcome reconciliation,
privacy admission, escalation, verification floors and evidence composition.

| Infrastructure | Required direction |
| --- | --- |
| Lexical search | Use pinned `tgrep-core` beneath the snapshot/provenance wrapper; no parallel custom trigram engine. |
| Semantic indexing | Consume SCIP and existing producers; Tree-sitter is structural fallback. |
| Git objects | Evaluate and adopt `gix` for Rust object/tree/blob reads; retain real Git CLI for controller worktree effects. |
| Procedures | Adopt Agent Skills metadata and progressive disclosure; no proprietary skill protocol. |
| Coding runtimes | Adapt Codex App Server, OpenCode and ACP; do not implement a new agent wire protocol or coding-agent loop. |
| Direct inference | Three finite native protocol adapters for structured inference. Compatible external gateways are optional endpoints, never mandatory infrastructure. |
| Worker environments | Investigate optional OpenHands Agent Server reuse only if controller effect authority remains enforceable; no automatic custom Docker/Kubernetes platform. |
| Scheduling | Borrow bounded claims, backoff and reconciliation mechanisms from Symphony, retaining this project's run/effect semantics. |
| RI ergonomics | Borrow bounded presentation/query vocabulary from Graphify, CodeGraph, Serena and Aider; do not add competing graph engines or treat inferred graphs as authority. |
| Durable storage | Migrate to SQLite through a maintained pure-Go driver; preserve canonical event payloads, hash chains, unique intents, transactions and JSONL export. |
| GitHub | Use a mature client library beneath exact effect admission and read-back reconciliation. |
| Observability | Use OpenTelemetry with local defaults and optional explicit OTLP export. |

SQLite migration must validate existing JSONL history before import, preserve
event identities and ordering, reject conflicting imports, and retain the
original files. A storage migration must not authorize replay of an unresolved
external effect. Export remains inspectable JSONL. Transaction guarantees and
crash recovery must be tested through the selected database implementation.

Claims about donor licenses, available capabilities and revisions must be checked
against primary sources. Estimated code savings are planning hypotheses, not
measured results or justification for weakening project invariants.

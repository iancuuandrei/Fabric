# Harness Engineering & Donor Blueprint (2025–2026)

This reference synthesizes empirical findings from leading autonomous software
engineering systems, **June–November 2026 arXiv research**, and open-source
donors, mapping concrete high-signal mechanisms directly to Fabric packages while
rejecting architectural bloat.

## 1. Core Consensus: `Agent = Model + Harness`

Across Anthropic, OpenAI Codex, OpenHands V1, SWE-bench Verified, and late-2026
empirical evaluations (*"Don't Blame the Large Language Model"* `arXiv:2607.03691`,
*"Same Model, Different Harness"* `arXiv:2608.26218`), changing the harness
environment, feedback loop, and verification gates routinely shifts success
rates by double digits without changing model weights:

1. **Instructions (`AGENTS.md` + Lazy `SKILL.md`):** Lean, repo-bound
   procedural guidance loaded lazily on demand and fenced from runtime authority.
2. **Tools (ACI — Agent-Computer Interface):** Poka-yoke, CRLF-safe, fail-fast
   tools returning immediate inline syntax/type diagnostics (`internal/diagnostics`).
3. **Environment & Secret-Scrubbed Sandbox:** Isolated Git worktrees with
   process-tree teardown, scrubbed credential env vars, and opt-in hardened
   Docker/Podman (`--network=none`, `--cap-drop=ALL`, `internal/sandbox`).
4. **Sensors & 4-Layer Verification (`arXiv:2610` / Oct 2026):** Instructions ->
   Inline Edit Diagnostics -> Controller Verification + Mutation Audit ->
   Independent Precision Critic Review.
5. **Typed Capability Enforcement (CapScope, `arXiv:2609.08371`):** Capability
   checks live in typed Go structs outside the LLM context window
   (`internal/agentloop/capabilities.go`).
6. **Observability, Durable Resume & Live Steering:** Turn-by-turn persisted
   state, SSE streaming, live mid-run steering, and hard USD/token circuit
   breakers (`internal/cost`, `internal/api`, `internal/webui`).

---

## 2. Donor & 2026 Research Catalog Mapped to Fabric

| Donor / Paper | Proven High-Value Mechanism | Anti-Pattern Rejected | Shipped Fabric Package / Surface |
| --- | --- | --- | --- |
| **Alibaba OpenCodeReview** | Deterministic diff grouping & **filter-only reflection pass** (delete false positives, optimize precision over recall). | Letting reflection rewrite findings freely or skipping native tests. | `.agents/skills/code-review/`, `agentloop.ReviewDiff`, `fabric review-pr` |
| **Graphify / GitNexus / RepoGraph (`arXiv:2410.14684`)** | Deterministic AST + doc-to-code knowledge graph with `EXTRACTED` vs `INFERRED` edge provenance, PageRank hub centrality, and multi-hop blast radius. | Vector-embedding code RAG or unverified claim of call absence. | `internal/codegraph`, `repo_map` / `graph_impact` tools, `fabric graph`, `/v1/graph` |
| **Serena (LSP / Fast Diagnostics)** | Immediate language-native syntax/type diagnostics returned right after edits + structural refactoring workflow. | Waiting 20 turns until final test suite fails on a typo. | `internal/diagnostics`, `.agents/skills/structural-refactoring/` |
| **Agentless & AutoCodeRover** | Hierarchical localization (**repo map -> symbols -> spans**) + Ochiai SBFL spectrum scoring. | Blind full-file dumping into context. | `internal/faultlocalization`, `.agents/skills/fault-localization/` |
| **Aider & SWE-agent / Mini-SWE-agent** | Unique search-replace anchored edits, compact structural map, radical ACI minimalism. | Fuzzy silent guessing on ambiguous anchors or unbounded bash loops. | `internal/agentloop/tools.go`, `.agents/skills/anchored-editing/` |
| **NeurIPS Observation Masking (`arXiv:2508.21433`)** | Mask bulky older tool outputs while keeping recent N turns verbatim. **Epoch optimization:** advance cutoff in batches so prompt caches stay hot! | Naive sliding-window masking that busts prefix prompt caching every turn, or destructive summarization ("Compaction Cliff"). | `applyEpochObservationMasking` in `internal/agentloop/loop.go`, `.agents/skills/context-hygiene/` |
| **CapScope (`arXiv:2609.08371`) & Repo Poisoning (`arXiv:2608.30686`)** | Enforce typed tool capabilities outside model context + fence `<untrusted_repo_context>` + scrub credential env vars from tool subprocesses. | Treating `AGENTS.md` prose as security enforcement or leaking API keys to test runners. | `internal/agentloop/capabilities.go`, `internal/sandbox/sandbox.go` |
| **MemDev-Bench (Oct 2026) & Beads** | Persist compact, verified project invariants/lessons indexed by scope rather than raw noisy logs. | Dumping raw multi-session chat logs into context ("From Knowledge to Noise"). | `internal/memory`, `fabric memory`, `fabric_memory` MCP tool |
| **Delegation Contracts (`arXiv:2606.13177`) & End of Code Review (`arXiv:2606.13175`)** | Structured **Review Packets** bundling blast radius, test receipts, mutation score, churn, and cost to eliminate human review fatigue. | Raw 800-line unverified AI diffs dropped into PRs. | `internal/packet`, `fabric packet`, `fabric pr` |
| **N-Version Programming (`arXiv:2606.13172`) & Diff-Scoped Mutation** | Concurrent isolated cross-model contenders ranked by Pareto frontier + diff-scoped mutation testing to defeat the test oracle problem. | Picking the first passing agent blindly or trusting weak LLM-written assertions. | `internal/tournament`, `internal/mutation`, `fabric tournament`, `fabric mutate`, `.agents/skills/property-differential-testing/` |
| **OpenHands V1 / Pi / Spec Kit / Superpowers** | Local-first SDK + opt-in sandbox, ACP interop, lazy `load_skill` loading, live turn steering & resume, spec-driven YAGNI, strict TDD. | Mandatory heavy containers for local dev; bloated >500-line static system prompts. | `pkg/fabric`, `internal/acpruntime`, `internal/api` SSE, `internal/bench`, `.agents/skills/{tdd,spec-driven-dev}/` |

---

## 3. Empirical Rules for Keeping Fabric Fast & Lean

1. **No Vector DB / Embedding RAG for Code:** Deterministic AST + Graphify PageRank + lexical search beats embedding RAG on recall, latency (`<2.2s` for ~1,450 files), and token cost.
2. **Cache-First Multi-Turn Layout:** Static instructions, tools, and repo maps sit at the top of the prefix; epoch-batched Observation Masking + dual recent Anthropic user breakpoints maximize prefix cache hits.
3. **Measure With `fabric bench`:** Every new skill, prompt rule, or harness stage must earn its latency and token budget against objective Pass@1, mutation kill %, churn, and USD cost scorecards.

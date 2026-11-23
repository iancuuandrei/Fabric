# Fabric Documentation Hub

Visual sitemap, code ownership map, and task-oriented navigation for the entire Fabric repository.

---

## Visual Documentation Sitemap

```mermaid
flowchart TD
  hub["Fabric Documentation Hub"]

  subgraph START["Start & Daily Production"]
    direction TB
    qs["5-Minute Quickstart<br/>guides/quickstart.md"]
    prod["Production Agent Runtime<br/>guides/production-agent-runtime.md"]
    integ["Jira, Linear, GitLab & Chat<br/>guides/integrations.md"]
    api["Mission Control UI, API & MCP<br/>guides/api-and-mcp.md"]
    cli["CLI Reference Catalog<br/>reference/cli.md"]
    qs ~~~ prod ~~~ integ ~~~ api ~~~ cli
  end

  subgraph CORE["v2.0.1 Context & Workflow Internals"]
    direction TB
    cc["Context Compiler & Spec Graph<br/>guides/context-and-codegraph.md"]
    sk["32 Built-In SWE Skills<br/>guides/procedures.md"]
    res["2025–2026 Harness Research<br/>research/harness-best-practices-2026.md"]
    ppr["Go PPR & Context Selection<br/>guides/go-context-ppr.md"]
    cc ~~~ sk ~~~ res ~~~ ppr
  end

  subgraph ARCH["Architecture, Safety & Specs"]
    direction TB
    sys["System Architecture<br/>architecture/system.md"]
    sec["Security Trust Boundary<br/>security/trust-boundary.md"]
    specs["Normative Specifications<br/>specifications/"]
    adrs["Architecture Decisions<br/>adr/"]
    sys ~~~ sec ~~~ specs ~~~ adrs
  end

  subgraph LEGACY["Classic Controller, Benchmarks & Releases"]
    direction TB
    cl["Classic Controller & RI (~40 guides)<br/>guides/classic-internals.md"]
    ev["Evaluation Ledger & v1 Gate<br/>evaluation/status.md"]
    rel["Release Build & Provenance<br/>guides/release.md"]
    his["Source History & Policy<br/>contributing/source-history.md"]
    cl ~~~ ev ~~~ rel ~~~ his
  end

  hub --> START
  hub --> CORE
  hub --> ARCH
  hub --> LEGACY

  classDef hubNode fill:#14283f,stroke:#38bdf8,color:#ffffff,stroke-width:2px;
  classDef startNode fill:#064e3b,stroke:#10b981,color:#ecfdf5,stroke-width:1.5px;
  classDef coreNode fill:#0f2e3a,stroke:#0891b2,color:#ecfeff,stroke-width:1.5px;
  classDef archNode fill:#1e293b,stroke:#64748b,color:#f8fafc,stroke-width:1.5px;
  classDef legNode fill:#3b270c,stroke:#d97706,color:#fffbeb,stroke-width:1.5px;
  class hub hubNode;
  class qs,prod,integ,api,cli startNode;
  class cc,sk,res,ppr coreNode;
  class sys,sec,specs,adrs archNode;
  class cl,ev,rel,his legNode;
```

---

## Find By Question

| I want to... | Best Guide | Primary Command / Endpoint |
| --- | --- | --- |
| **Build & run my first coding task in 5 minutes** | [5-Minute Quickstart](guides/quickstart.md) | `fabric init --auto` -> `fabric agent` |
| **Understand zero-embedding Context Packs & Javadoc Spec Cards** | [Context Compiler & Spec Graph](guides/context-and-codegraph.md) | `fabric context --spec-only` / `fabric graph --spec` |
| **Configure budgets, Docker sandboxes, tournaments & hooks** | [Production Agent Runtime](guides/production-agent-runtime.md) | `fabric.toml`, `fabric tournament`, `fabric mutate` |
| **Browse or override the 32 native workflow skills** | [Engineering Skills Catalog](guides/procedures.md) | `fabric skills list` / `fabric skills install` |
| **Run agents directly from Jira, Linear, GitLab, GitHub or Slack** | [Engineering Integrations](guides/integrations.md) | `fabric ticket`, `fabric notify`, `POST /v1/webhooks/{provider}` |
| **Use the Web Kanban UI, REST/SSE API, MCP or Go SDK** | [Mission Control, API & MCP](guides/api-and-mcp.md) | `fabric serve`, `fabric mcp`, `GET /v1/openapi.json` |
| **Inspect trust boundaries, secret scrubbing & write isolation** | [Security Trust Boundary](security/trust-boundary.md) | `internal/agentloop/capabilities.go` + `internal/sandbox` |
| **Build reproducible release archives & verify hashes** | [Release Guide](guides/release.md) | `scripts/release-build.ps1`, `scripts/release-verify.ps1` |
| **Read v1 classic task-graph controller & Rust RI docs** | [Classic Controller & RI Guides](guides/classic-internals.md) | `fabric run --autonomous`, `fabric ri ...` |

---

## Repository Code Map (Go + Rust)

```mermaid
flowchart LR
  cmd["cmd/fabric<br/>CLI Entrypoints"]
  api["internal/api + webui + mcpserver<br/>REST/SSE + Dashboard + 18 MCP Tools"]
  sdk["pkg/fabric<br/>Public Go SDK"]

  loop["internal/agentloop<br/>Worktree Loop + CapScope + Skills + Review"]
  ctx["internal/contextcompiler + codegraph<br/>Javadoc Specs + PPR + BM25 + Hops"]
  gates["internal/mutation + diagnostics + hooks + sandbox<br/>AST Vet + Mutation Audit + Isolation"]
  mem["internal/memory + packet + cost + rewind<br/>Lessons + Review Packet + Git Snapshots"]
  intg["internal/integrations + githubflow<br/>Jira · Linear · GitLab · GitHub · Chat"]
  ri["crates/ri (Rust)<br/>Optional Indexer & SCIP"]

  cmd --> sdk --> loop
  api --> sdk
  loop --> ctx
  loop --> gates
  loop --> mem
  cmd --> intg
  ctx -. "optional snapshot/query" .-> ri

  classDef surface fill:#14283f,stroke:#38bdf8,color:#ffffff,stroke-width:1.5px;
  classDef engine fill:#0f2e3a,stroke:#0891b2,color:#ecfeff,stroke-width:1.5px;
  classDef guard fill:#3b270c,stroke:#d97706,color:#fffbeb,stroke-width:1.5px;
  classDef opt fill:#1e293b,stroke:#64748b,color:#f8fafc,stroke-width:1.5px;
  class cmd,api,sdk surface;
  class loop,ctx,mem engine;
  class gates,intg guard;
  class ri opt;
```

| Package / Crate | Responsibility |
| --- | --- |
| [`cmd/fabric`](../cmd/fabric) | Unified CLI dispatcher (`agent`, `context`, `graph`, `tournament`, `packet`, `rewind`, `mutate`, `apply`, `ticket`, `issue`, `pr`, `serve`, `mcp`, `bench`, `skills`) + classic controller routes. |
| [`internal/contextcompiler`](../internal/contextcompiler) | Zero-embedding Context Pack compiler: SHA-256 cache, `BM25` + `PersonalizedRank` + `--hops` BFS + Git co-change fusion, `L3->L0` budget packing, nested `AGENTS.md`, `UntestedSymbols`, skill affinity. |
| [`internal/codegraph`](../internal/codegraph) | Multi-language Code Knowledge Graph + `SymbolSpec` extractor (Javadoc/JSDoc/GoDoc/Python/Rust tags + static `@effects`), `CALLS`/`USES_TYPE`/`TESTS`/`DOCUMENTS` edges, `PersonalizedRank`, `Impact`. |
| [`internal/agentloop`](../internal/agentloop) | Worktree-isolated coding loop, Turn-1 Context Compiler injection, CapScope enforcement, Epoch Observation Masking, inline edit diagnostics, stage-aware skill activations, diff hygiene & precision review. |
| [`internal/api`](../internal/api), [`internal/webui`](../internal/webui), [`internal/mcpserver`](../internal/mcpserver) | Strict-CSP Mission Control Web UI, OpenAPI 3.1 HTTP/SSE server, fail-closed HMAC inbound webhooks, and 18-tool stdio MCP server. |
| [`internal/integrations`](../internal/integrations), [`internal/githubflow`](../internal/githubflow) | Leaf integration adapters for Jira Cloud/DC, Linear, GitLab, GitHub (`gh`), Slack, Discord, Microsoft Teams, and signed outbound webhooks. |
| [`internal/packet`](../internal/packet), [`internal/mutation`](../internal/mutation), [`internal/rewind`](../internal/rewind), [`internal/memory`](../internal/memory) | Structured Review Packets (`skills_used`, `hygiene_notes`), diff-scoped mutation audits, lightweight Git snapshot refs, and `.harness/memory/lessons.jsonl`. |
| [`pkg/fabric`](../pkg/fabric) | Public embeddable Go client SDK wrapping episode orchestration, graph/context queries, and safe apply. |
| [`crates/ri`](../crates/ri) | Optional Rust repository-intelligence indexer and SCIP/lexical sidecar (`fabric-ri`). |

---

## Core Architecture & Research Index

- **[System Architecture](architecture/system.md)** — Control flow, write-ownership isolation, verification gates, and evidence boundaries.
- **[Security Trust Boundary](security/trust-boundary.md)** — Isolation guarantees, secret scrubbing, CapScope enforcement, and known limits.
- **[Normative Specifications](specifications/)** & **[Architecture Decision Records (ADRs)](adr/)** — Authority, journal effects, and RI boundaries.
- **[2025–2026 Harness Research & Donors](research/harness-best-practices-2026.md)** — Empirical arXiv findings and donor mechanisms shipped in Fabric.
- **[Research Provenance](research/oss-mechanisms.md)** & **[v2 Product Roadmap](roadmap/v2-product-plan.md)**.

## Classic Controller Internals & Historical Archives

- **[Classic Controller, Provider & RI Guides](guides/classic-internals.md)** — All ~40 deep-dive guides for the deterministic v1 task-graph controller (`fabric run --autonomous`), `fabric-ri`, Codex confinement, and OpenCode/Responses protocols.
- **Getting Started Records (v1 history):** [Windows installed-task guide](getting-started/installed-autonomous-task.md) · [Local plan route](getting-started/local-plan.md) · [Real-task record](getting-started/real-task.md)
- **Evaluations & Releases:** [Release guide](guides/release.md) · [Evaluation status ledger](evaluation/status.md) · [v1 product gate](development/v1-product-gate.md) · [v1.0.0 release acceptance](evaluation/v1-release-acceptance.md) · [Real-repo eval harness](../evals/v1/README.md)
- **CLI & Contributing:** [CLI Reference](reference/cli.md) · [Codex integration](../integrations/codex/fabric/README.md) · [Contributing](../CONTRIBUTING.md) · [Documentation standard](contributing/documentation-standard.md) · [Source history policy](contributing/source-history.md)

## Evidence Vocabulary

Fabric separates implemented behavior from pinned run/package evidence using **PASS**, **FAIL**, **NOT RUN**, **BLOCKED**, and **UNKNOWN**. Missing timing, usage, or provider telemetry is recorded as unavailable — never zero.

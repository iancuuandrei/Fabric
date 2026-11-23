# Zero-Embedding Context Compiler, Javadoc Spec Graph & Native Skill Workflow

Fabric `v2.0.1` replaces blind whole-file stuffing and fragile vector databases with a **deterministic, zero-embedding Context Compiler** built directly on a **function-level Javadoc Spec & Call/Type/Test Knowledge Graph**, paired with a **stage-aware SWE skill lifecycle** ending in default precision code review.

---

## Why Zero Embeddings?

| Property | Vector Embedding RAG | Fabric `v2.0.1` Context Compiler |
| --- | --- | --- |
| **External infrastructure** | Requires embedding model API / vector store | Zero external services; pure local Go (`<2s` cold build) |
| **Freshness after edits** | Re-embedding lag or stale vector chunks | SHA-256 content-addressed cache (`.harness/cache/context/graph-v1.json`) invalidates instantaneously on any file byte change |
| **KV-Cache stability** | Semantic float drift reorders prompt prefixes | Sorted node/edge IDs + zero timestamps keep Turn-1 Markdown byte-identical |
| **Structural precision** | Fuzzy cosine similarity drops callers/callees/types | Exact AST `CALLS`, `USES_TYPE`, `TESTS`, `DOCUMENTS`, and `REFERENCES` edges with `EXTRACTED` vs `INFERRED` provenance |
| **Contract density** | Dumps raw chunks or loses signatures | Structured `SymbolSpec` cards (`@param`, `@returns`, `@throws`, `@effects`, callers, tests) preserve contracts at ~10x fewer tokens |

---

## 1. Code Knowledge Graph & Javadoc Spec Extraction

`internal/codegraph` parses Go source with `go/parser` + full `ast.Inspect` (including comments) alongside structural symbol/spec extraction for TypeScript, JavaScript, Python, Rust, Java, C#, and Markdown docs:

```mermaid
flowchart LR
  subgraph SRC["Repository Source Tree"]
    direction TB
    go["Go Files<br/>go/parser + ast.Inspect"]
    poly["TS · JS · Python · Rust · Java · C#<br/>Signature & Docstring Scanner"]
    md["Markdown & Guidance<br/>README · docs · AGENTS.md · CLAUDE.md"]
  end

  subgraph SPEC["Function-Level SymbolSpec Extraction"]
    direction TB
    doc["Javadoc / JSDoc / GoDoc / Sphinx / Rustdoc<br/>Summary · @param · @returns · @throws · @see · @deprecated"]
    eff["Static Behavioral Effects<br/>returns_error · can_panic · uses_defer · spawns_async<br/>uses_locks · io_http · io_fs · io_exec · reads_env"]
  end

  subgraph EDGES["Heterogeneous Typed Graph"]
    direction TB
    e1["DEFINES · IMPORTS · DOCUMENTS"]
    e2["CALLS · USES_TYPE · TESTS · REFERENCES"]
    ppr["20-Iter Damped Personalized PageRank<br/>Task-Seeded Structural Centrality"]
  end

  cache[("SHA-256 Content Cache<br/>.harness/cache/context/graph-v1.json")]

  go --> doc
  go --> eff
  poly --> doc
  poly --> eff
  md --> e1
  doc --> e2
  eff --> cache
  e1 --> ppr
  e2 --> ppr
  ppr --> cache

  classDef srcNode fill:#1e293b,stroke:#64748b,color:#f8fafc,stroke-width:1.5px;
  classDef specNode fill:#0f2e3a,stroke:#0891b2,color:#ecfeff,stroke-width:1.5px;
  classDef graphNode fill:#172554,stroke:#3b82f6,color:#eff6ff,stroke-width:1.5px;
  classDef storeNode fill:#064e3b,stroke:#10b981,color:#ecfdf5,stroke-width:1.5px;
  class go,poly,md srcNode;
  class doc,eff specNode;
  class e1,e2,ppr graphNode;
  class cache storeNode;
```

Inspect any symbol's contract card directly:

```powershell
fabric graph --spec VerifyHMACSHA256
fabric graph --impact internal/integrations/webhook.go --depth 2
```

---

## 2. 4-Signal Hybrid Ranking & Multi-Resolution Packing

`internal/contextcompiler` fuses query/symbol seeds, identifier-aware BM25, Personalized PageRank, bounded BFS `--hops` expansion, and Git co-change history via Reciprocal Rank Fusion (RRF), then packs results hierarchically under a strict token budget:

```mermaid
flowchart TD
  q["Inputs: Query + Backtick Symbols + --files + Optional --diff"]
  q --> sig["Query Signal Extractor<br/>CamelCase/snake_case split + Stopword Filter"]

  sig --> s1["SEED Signal<br/>Explicit Files + Exact Symbol Matches"]
  sig --> s2["BM25 Lexical Score<br/>Name · Path · Spec Summary · @param · @throws"]
  s1 --> s3["Personalized PageRank<br/>Task-Biased Graph Walk"]
  s1 --> s4["Bounded Hop BFS<br/>Depth 1..maxHops via CALLS / TESTS / USES_TYPE"]
  s1 --> s5["Git Co-Change Coupling<br/>Bounded 40-Commit History"]

  s1 --> rrf["Reciprocal Rank Fusion (RRF)<br/>Test Downweight 0.85x · Dirty-Diff Tiebreaker"]
  s2 --> rrf
  s3 --> rrf
  s4 --> rrf
  s5 --> rrf

  rrf --> pack["Multi-Resolution Budget Packer"]
  pack --> l3["L3_BODY<br/>Top Focal Bodies ≤ 85 lines, 35% code cap"]
  pack --> l2["L2_SPEC<br/>Javadoc Spec Card + Signatures + Effects + Callers"]
  pack --> l1["L1_OUTLINE<br/>1-Line Signature + Summary"]
  pack --> l0["L0_POINTER<br/>Compact Path:Line + Name"]

  l3 --> aux["Envelope Extras:<br/>CallGraph Summary · UntestedSymbols<br/>Nested AGENTS.md · RecommendedSkills · Lessons"]
  l2 --> aux
  l1 --> aux
  l0 --> aux

  classDef inpNode fill:#1e293b,stroke:#64748b,color:#f8fafc,stroke-width:1.5px;
  classDef signalNode fill:#172554,stroke:#3b82f6,color:#eff6ff,stroke-width:1.5px;
  classDef fuseNode fill:#3b270c,stroke:#d97706,color:#fffbeb,stroke-width:1.5px;
  classDef tierNode fill:#0f2e3a,stroke:#0891b2,color:#ecfeff,stroke-width:1.5px;
  classDef outNode fill:#064e3b,stroke:#10b981,color:#ecfdf5,stroke-width:1.5px;
  class q,sig inpNode;
  class s1,s2,s3,s4,s5 signalNode;
  class rrf,pack fuseNode;
  class l3,l2,l1,l0 tierNode;
  class aux outNode;
```

Compile packs from the CLI, HTTP API, or MCP:

```powershell
# Lean Javadoc Spec Cards + Call Graph (Turn-1 default for fabric agent)
fabric context --spec-only --budget 2500 "Verify webhook HMAC signatures"

# Full focal bodies (L3_BODY) for top hits anchored to explicit files
fabric context --files internal/integrations/webhook.go --hops 3 "ParseInboundWebhook"

# Include current working-tree git diff as gentle seed tie-breakers (or auto-seed when query is empty)
fabric context --diff "Check retry loop"
fabric context
```

> Notice that passing a query without `--diff` prevents unrelated dirty working-tree files from crowding out query-exact symbols. Omitting both query and `--files` automatically activates `--diff`.

---

## 3. Stage-Aware Native Skill Workflow & Precision Review

Fabric wires its **32 embedded SWE skills** ([Skill Catalog](procedures.md)) natively across all five phases of `internal/agentloop`, rather than dumping thousands of lines into Turn 1:

```mermaid
flowchart LR
  d["1 · DISCOVER (Turn 1)<br/>Compile Spec Pack<br/>Activate brownfield-convention-matching<br/>+ Effect-Matched Skills (e.g. concurrency-resource-safety)"]
  i["2 · IMPLEMENT<br/>Lazy load_skill on demand<br/>Unique-anchor edit_file<br/>Inline AST / go vet + Snapshot Ref"]
  rep["3 · REPAIR (on failure)<br/>Attempt 1: repair skill<br/>Attempt ≥ 2: systematic-debugging"]
  v["4 · VERIFY<br/>Project Hooks + Native Tests<br/>Diff-Scoped Mutation Audit"]
  rev["5 · REVIEW (default --review=true)<br/>Deterministic Diff Hygiene Check<br/>Multi-Lens Fresh Precision Critic<br/>(code-review · code-hygiene + diff-matched lenses)"]
  pkt["READY Review Packet<br/>skills_used · hygiene_notes · receipts · cost"]

  d --> i
  i --> v
  v -->|"failed"| rep --> i
  v -->|"passed"| rev
  rev -->|"changes requested"| i
  rev -->|"APPROVED"| pkt

  classDef disc fill:#0f2e3a,stroke:#0891b2,color:#ecfeff,stroke-width:1.5px;
  classDef impl fill:#172554,stroke:#3b82f6,color:#eff6ff,stroke-width:1.5px;
  classDef repn fill:#7f1d1d,stroke:#ef4444,color:#fef2f2,stroke-width:1.5px;
  classDef gate fill:#3b270c,stroke:#d97706,color:#fffbeb,stroke-width:1.5px;
  classDef done fill:#064e3b,stroke:#10b981,color:#ecfdf5,stroke-width:1.5px;
  class d disc;
  class i impl;
  class rep repn;
  class v,rev gate;
  class pkt done;
```

- **Effect-Matched Skill Routing:** If focal functions exhibit `uses_locks` / `spawns_async`, Fabric boosts `concurrency-resource-safety`; if `UntestedSymbols` are detected, `tdd` and `property-differential-testing` rise to the top; `io_exec` / auth terms elevate `security-audit` and `ci-and-supply-chain-hygiene`.
- **Diff-Matched Precision Review Lenses:** Before accepting `READY`, `auditDiffHygiene` checks for added `fmt.Println` / `console.log`, unresolved `TODO`/`FIXME`, un-tested production edits, and whitespace/conflict markers, while `composeReviewRubric` attaches targeted security, concurrency, or contract-migration lenses alongside `code-review` and `code-hygiene`.

---

## 4. API & MCP Endpoints

- **HTTP REST API:**
  - `GET /v1/graph/spec?target=VerifyHMACSHA256`
  - `POST /v1/context` with `{"query":"...", "files":["..."], "budget_tokens":3200, "hops":2, "spec_only":true, "include_diff":false}`
- **Stdio MCP Server:** `fabric_context`, `fabric_graph`, and `fabric_graph_impact`.

See [Production Agent Runtime Guide](production-agent-runtime.md), [API & MCP Guide](api-and-mcp.md), and [Documentation Hub](../README.md).

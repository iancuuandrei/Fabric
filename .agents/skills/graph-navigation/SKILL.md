---
name: graph-navigation
description: Graph-first tiered codebase discovery and blast-radius analysis inspired by Graphify, GitNexus, RepoGraph, and Serena. Use before inspecting unfamiliar files or changing shared types, signatures, and hub modules.
metadata:
  fabric.roles: "planner,explorer,writer,fixer,reviewer"
---

# Graph-First Navigation & Blast-Radius Protocol

Never open raw source files blindly or spam repository-wide text searches when a structural map is available.

## 1. Tiered Discovery Order
1. **Level 0 — Policy:** Read repository contracts (`AGENTS.md`, `fabric.toml`) for verification commands and boundaries.
2. **Level 1 — Structural Graph:** Query `repo_map` or `graph_impact` (or inspect `.harness/graph/GRAPH_REPORT.md`) to identify the highest-centrality hub files, module clusters, and multi-hop callers/callees before reading file bodies.
3. **Level 2 — Targeted Spans:** Use `read_file` with exact `start_line`/`end_line` windows on only the localized definitions and direct callers.

## 2. Edge Provenance Discipline
- Treat **`EXTRACTED`** edges (AST definitions, explicit imports) as structural ground truth.
- Treat **`INFERRED`** edges (name-matched calls, Markdown doc references) as leads that MUST be verified by reading the target line before claiming impact or modifying code.

## 3. Mandatory Pre-Edit Impact Check
Before changing any exported type, function signature, config field, or wire contract:
1. Run `graph_impact` on the target symbol/file at depth `2`.
2. Enumerate every upstream caller, test file, and linked architecture document.
3. Include every affected caller in your scoped edit plan — never leave partial migrations.

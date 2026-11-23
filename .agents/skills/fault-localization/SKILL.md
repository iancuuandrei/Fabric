---
name: fault-localization
description: Narrow a bug, test failure, or feature request down to the exact files, symbols, and edit spans using hierarchical AST-first localization before reading full implementations. Use when exploring a codebase, planning changes, or diagnosing failures.
metadata:
  fabric.roles: "planner,explorer,fixer"
---

# Hierarchical Fault Localization (Donor: Agentless + AutoCodeRover + Aider)

Localization determines most autonomous engineering outcomes. Narrow progressively instead of reading whole directories or grepping blindly.

## 1. Stage 1 — Candidate Files & Modules
Combine independent signals and preserve their provenance:
- Failing test names, stack frames, compiler diagnostics, and exact identifiers from the objective.
- Structural repo map / `fabric-ri` package, symbol, and import relations.
- Recently modified candidate files and their paired test files.
Rank the top 3–5 candidate files; avoid loading unrelated files into context.

## 2. Stage 2 — Symbols & Signatures (Skeletons Before Bodies)
- Inspect exported types, function signatures, interfaces, and call edges before reading full function bodies.
- Trace callers and callees of suspicious symbols to find where invariants are established versus violated.
- Treat missing semantic edges in partial indexes as `UNKNOWN`, never proof of absence.

## 3. Stage 3 — Exact Edit Spans
- Read only the minimal surrounding spans needed to confirm the root cause and design a surgical change.
- Verify adjacent contracts (config parsers, serialization tags, persisted fixtures, generated files) that co-change with the target span.

## 4. Stopping Rule
Stop exploring as soon as you have:
1. The exact files and symbols causing the defect or owning the feature;
2. Their direct callers/tests and invariants; and
3. A clear, bounded write scope.
Do not keep reading files once additional reads have low expected decision value.

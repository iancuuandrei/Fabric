---
name: spec-driven-dev
description: Specification-first engineering discipline inspired by GitHub Spec Kit, GStack plan-eng-review, and Ponytail YAGNI. Use before designing or implementing non-trivial features, contracts, or state transitions.
metadata:
  fabric.roles: "planner,writer"
---

# Specification-Driven Engineering (SDD) Protocol

Code is cheap to emit; wrong specifications are expensive to unwind. Lock down intent, invariants, and failure modes BEFORE writing implementation lines.

## 1. Compact Pre-Implementation Spec (4 Lines Max)
Before modifying production code, explicitly establish:
1. **Contract:** Exact inputs, outputs, state transitions, and compatibility boundary.
2. **Invariants:** Properties that MUST hold after every operation (atomicity, idempotence, bounds, zero secret leakage).
3. **Failure Modes:** How invalid input, partial failure, concurrency races, and timeouts degrade (fail-closed by default).
4. **Acceptance Oracle:** The exact deterministic check(s) that distinguish a real fix from a superficial patch.

## 2. Premise & YAGNI Filter (Anti-Slop Gate)
- Reject speculative abstractions, unused config knobs, dead wrapper layers, and unrequested refactors outside the objective's blast radius.
- Prefer the smallest diff that satisfies every invariant cleanly and remains trivially readable by a human reviewer.
- Never silently change an existing public contract without updating its schema, callers, and regression tests in the same patch.

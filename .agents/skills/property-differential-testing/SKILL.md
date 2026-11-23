---
name: property-differential-testing
description: Oracle-hardened verification using property-based, differential, metamorphic, and mutation testing principles to eliminate circular AI tests. Use when adding or strengthening tests for parsers, graphs, budgets, or algorithms.
metadata:
  fabric.roles: "writer,fixer,reviewer"
---

# Property, Differential & Metamorphic Verification Protocol

When an agent writes both implementation and example-based unit tests, it risks the **oracle problem** (tests mirror the same flawed assumption as the code). Defeat circular testing with structural oracles.

## 1. Choose the Strongest Oracle for the Change
- **Invariant / Property Tests:** Assert universal laws over varied inputs (round-trip `decode(encode(x)) == x`, idempotence `f(f(x)) == f(x)`, monotonicity, conservation of counts/totals, bounded output size, zero panics on adversarial/empty/boundary inputs).
- **Differential Tests:** When optimizing, refactoring, or porting logic, assert exact equivalence between the new fast path and a simple reference implementation across edge cases.
- **Metamorphic Relations:** When exact ground truth is unavailable (ranking, search, scoring), assert predictable directional changes under controlled input perturbations.
- **Mutation Resistance:** Every boundary operator (`<` vs `<=`, `==` vs `!=`, `&&` vs `||`, boolean guard) added in your diff MUST be killed by a failing test if flipped (`fabric mutate`).

## 2. Hard Rules
- Never weaken, delete, or skip an existing test assertion to achieve green CI.
- Test negative paths (malformed input, Windows CRLF/paths, concurrency races, permission/scope denials) with the same rigor as the happy path.

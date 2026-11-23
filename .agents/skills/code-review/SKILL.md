---
name: code-review
description: Perform precision-first independent code review of a concrete candidate diff against its objective, invariants, and executed checks using grouped context and false-positive filtering. Use after implementation or repair before accepting a candidate.
metadata:
  fabric.roles: "reviewer"
---

# Code Review (Donor: Alibaba OpenCodeReview + Qodo)

Optimize for **precision over recall**: every emitted finding must cite an exact path, symbol, and concrete failure mechanism. Eliminate nitpicks and speculative noise.

## 1. Group Related Changes First
- Do not review files in blind isolation. Group each changed implementation file with its tests, interfaces, schemas, and direct callers.
- Check whether any changed public signature, serialized field, or config key has unupdated consumers or persisted-state readers.

## 2. Four-Lens Review Pass
Inspect each group through four explicit lenses:
1. **Correctness & Invariants:** Off-by-one errors, nil/empty handling, state-machine violations, wrong defaults, broken idempotency, or silent regressions outside the requested objective.
2. **Failure & Boundary Paths:** Unchecked errors, partial writes, resource/handle leaks, timeouts, race conditions, and Windows/POSIX path differences.
3. **Security & Authority:** Path traversal/symlink escape, shell/SQL/template injection, secret leakage into logs/errors/journals, auth/scope bypass.
4. **Verification Honesty:** Confirm executed checks actually exercise the changed behavior. Flag weakened assertions, skipped tests, or unverified edge cases.

## 3. Filter-Only Reflection Pass (Mandatory)
Before emitting the final verdict, attempt to **falsify** every draft finding against the actual candidate diff and callers:
- **Delete** findings that are purely stylistic, already guarded elsewhere in the call chain, or unsupported by concrete code evidence.
- **Keep** only findings where you can state: exact file/symbol, exact current behavior, concrete trigger/impact, and exact closure oracle (which test or check proves the fix).

## 4. Verdict Contract
- Return strictly the supplied verdict schema bound to the exact candidate and verification identities.
- Separate recorded passing checks from unexecuted assumptions. Report unresolved evidence as uncertainty, never as a pass.
- This skill grants no authority to edit files, run checks, or widen scope.

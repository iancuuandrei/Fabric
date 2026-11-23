---
name: repair
description: Diagnose and fix a concrete failed verification check or review finding within declared write ownership using minimal root-cause patches and exact closure oracles. Use when repairing a candidate after failed tests or requested review changes.
metadata:
  fabric.roles: "fixer"
---

# Structured Repair (Donor: SWE-agent + Agentless + Alibaba OpenCodeReview)

Fix the root cause with the smallest coherent patch inside declared write ownership. Never mask a failure by weakening tests or bypassing checks.

## 1. Classify the Failure Before Editing
From the supplied candidate, verification output, and review findings, classify the failure:
- **Environment/Tooling:** Missing binary, network/sandbox block, or unresolved provider effect → report clearly; do not invent code workarounds.
- **Anchor/Contract Defect:** Stale preimage hash or non-unique edit anchor → re-read exact current candidate bytes and re-anchor cleanly.
- **Logic/Contract Defect:** Failing test, panic, wrong edge-case behavior, or review finding → proceed to root-cause repair.
- **Out-of-Scope Dependency:** Fix genuinely requires editing a file outside declared ownership → request an explicit scope replan; never silently edit unowned paths.

## 2. Root-Cause Minimal Patch
- Trace the failing assertion or review finding back to the violated invariant (use `fault-localization` if the failure site differs from the defect site).
- Form **one concrete hypothesis** and apply the smallest edit that fixes the defect while preserving unrelated public behavior.
- If a prior repair attempt drifted or introduced secondary failures, compare against the clean pre-write base instead of stacking workarounds.

## 3. Closure & Regression Guard
- Pair every logic fix with a discriminating regression test that fails without the fix and passes with it.
- Never delete, skip, relax, or narrow existing assertions to force a green build.
- Map each historical finding or reviewer concern to its exact closure oracle and report only recorded observations.

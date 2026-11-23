---
name: systematic-debugging
description: Isolate software defects through minimal deterministic reproduction, raw evidence capture, single-hypothesis testing, and permanent regression guards. Use when investigating bugs, flaky tests, or unexpected runtime failures.
metadata:
  fabric.roles: "explorer,fixer"
---

# Systematic Debugging (Donor: Superpowers + AutoCodeRover)

Never guess-and-patch. Follow the four-gate evidence protocol.

## 1. Minimal Reproduction
- Reproduce the defect with the smallest deterministic test, command, or input fixture.
- Record the exact failing command, exit code, and raw error/traceback before forming any hypothesis.

## 2. Evidence & State Isolation
- Inspect the exact state at the failure boundary (inputs, config, environment variables, file preimages, persisted journal/DB state).
- Distinguish **symptom location** (where the panic or assertion fires) from **fault origin** (where bad state was created).

## 3. Single-Hypothesis Testing
- State one explicit falsifiable hypothesis: *"If X is the root cause, then observing/changing Y will produce Z."*
- Test one hypothesis at a time. If disproven, discard it and update your evidence state—do not leave speculative edits behind.

## 4. Permanent Fix & Regression Guard
- Fix the root cause at the invariant boundary rather than patching downstream symptoms.
- Add a focused regression test that reproduces the original failure without the fix and passes with it.

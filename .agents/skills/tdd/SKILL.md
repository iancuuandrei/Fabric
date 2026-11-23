---
name: tdd
description: Implement behavior changes and bug fixes through a discriminating failing test at a public seam before writing minimal production code. Use when an executable test oracle is available for a feature or regression fix.
metadata:
  fabric.roles: "writer,fixer"
---

# Test-Driven Development (Donor: Superpowers + Anthropic Harness Engineering)

Prevent "rush-to-code" and backward-fitted tests by establishing a discriminating oracle first.

## 1. Discover the Real Test Stack
- Inspect project manifests (`go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`, `Makefile`) and existing test helpers before writing tests.
- Follow the repository's existing test conventions, fixtures, and assertion style.

## 2. Red Phase — One Discriminating Failing Test
- Write the smallest test at a **public behavioral seam** that fails under current code and will pass only when the requested behavior works.
- Confirm the failure is for the intended behavioral reason (not a compile error, missing import, or broken fixture).
- Include at least one boundary or adverse case (empty/nil, invalid input, error path, concurrency/idempotency) when it catches a distinct bug.

## 3. Green Phase — Minimum Coherent Implementation
- Write the smallest production change that makes the test and all adjacent tests pass (YAGNI).
- Do not add speculative abstractions, unused flags, or unrelated refactors during the green step.

## 4. Anti-Patterns to Block
- **No tautological tests** (re-computing expected values with the same broken formula as the implementation).
- **No private-detail coupling** (tests that break merely because an unexported helper was renamed).
- **Honest reporting:** Within Fabric, writer/fixer proposals do not execute native tests directly; the controller runs configured checks. Never claim a test passed without a recorded check observation.

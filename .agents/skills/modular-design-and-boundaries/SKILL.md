---
name: modular-design-and-boundaries
description: Structure packages and modules using deep interfaces, inward acyclic dependencies, functional core / imperative shell separation, and explicit dependency wiring. Use when creating new packages, resolving import cycles, or designing module boundaries.
metadata:
  fabric.roles: "planner,writer,fixer,reviewer"
---

# Modular Design & Dependency Boundaries

Keep complexity encapsulated inside deep, cohesive modules while keeping dependency graphs strictly acyclic and easy to integrate.

## 1. Deep Modules & Pulling Complexity Downward
- Design modules with a small, clean public surface that hides non-trivial parsing, retries, caching, and invariants behind cohesive operations.
- Pull validation, formatting, and retry/edge complexity downward into the owning module so callers stay declarative instead of repeating defensive boilerplate.
- Reject shallow pass-through wrapper hierarchies and temporal decomposition that scatter one cohesive concept across many tiny files.

## 2. Acyclic Inward Dependency Direction
- Keep leaf/domain packages (`contracts`, `integrations`, `cost`, `config`) free of imports from higher-level orchestrators (`agentloop`, `api`, `cmd`) so configuration structs never create import cycles.
- Break cross-layer conversions by placing adapters at the caller or composition package rather than pulling high-level types down into leaf packages.

## 3. Functional Core, Imperative Shell & Explicit Wiring
- Keep business rules, matching/parsing, state transitions, and budget decisions as pure functions on plain structs so unit tests need zero network or disk mocks.
- Push HTTP calls, SQL queries, clocks, env lookups, and subprocess execution to thin outer shell methods.
- Inject clients, config structs, and clocks explicitly via parameters or constructors — never depend on hidden mutable package globals.

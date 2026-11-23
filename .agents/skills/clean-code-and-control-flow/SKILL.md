---
name: clean-code-and-control-flow
description: Enforce edge parsing ("parse don't validate"), flat early-return control flow, low cognitive load, immutable option structs, and stdlib-first idioms. Use when implementing logic, simplifying nested conditionals, or designing internal helpers.
metadata:
  fabric.roles: "writer,fixer,reviewer"
---

# Clean Code, Edge Parsing & Control Flow

Write code a maintainer can verify in a single pass with minimal working-memory load.

## 1. Parse, Don't Validate
- Parse unconstrained strings, raw JSON/TOML, URLs, env var names, and CLI flags into validated typed structs or bounded values once at the system boundary.
- Make illegal states unrepresentable instead of scattering repeated `if s == ""` or format checks through inner helpers.

## 2. Flat Control Flow & Bounded Cognitive Load
- Use early returns / guard clauses for errors and edge cases; avoid deep `if/else` ladders beyond 3 levels of indentation.
- Split multi-concern functions when a reader must track more than ~4 independent state variables or branches at once.
- Replace opaque boolean flag parameters (`run(true, false, true)`) with small named option structs so every call site is self-documenting.

## 3. Simplicity & Standard Library First
- Prefer native standard library primitives (`strings`, `slices`, `maps`, `net/url`, `crypto/hmac`, `io`) over adding new third-party dependencies for small utilities.
- Return values cleanly rather than mutating caller arguments unexpectedly, unless updating a documented receiver or buffer.
- Never duplicate non-trivial validation, mapping, or flag-handling blocks; unify behind one parameterized helper or lookup table.

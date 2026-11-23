---
name: code-hygiene
description: Enforce strict anti-slop, low-duplication, dead-code-free maintainability and formatter/linter hygiene across any patch before review or completion. Use when writing, refactoring, or reviewing production or test code.
metadata:
  fabric.roles: "writer,fixer,reviewer"
---

# Code Hygiene & Anti-Slop Protocol

Keep every diff crisp, idiomatic, and review-ready with zero AI bloat, dead wiring, or copy-paste debt.

## 1. Zero Dead Code & Zero Duplication
- Delete unused imports, unreferenced helpers, obsolete feature flags, and commented-out code blocks instead of leaving fossils behind.
- Never duplicate a non-trivial logic block (> 6–8 lines or repeated validation/mapping boilerplate); extract one shared helper or table-driven loop instead.
- Never silently swallow errors (`_ = ...` on state-mutating calls or empty `catch {}`); return explicit contextual errors or log an actionable warning when degradation is intentional.
- Replace magic numbers, ad-hoc regexes, and duplicate header/MIME strings with named constants defined once per package.

## 2. Boundary & Style Hygiene
- Keep changes strictly scoped to the task objective — reject unrelated formatting churn or speculative "while I'm here" rewrites outside the changed contract.
- Keep comments focused on *why* an invariant, fallback, or ordering constraint exists rather than narrating obvious syntax.
- Update exported doc comments (`doc.go` / symbol headers), help text, and local README/docs in the same change when public behavior or commands change.

## 3. Mandatory Cleanliness Gate
1. Run the language formatter (`gofmt -w`, `cargo fmt`, `prettier --check`).
2. Run static analysis/linters (`go vet ./...`, `cargo clippy`, `tsc --noEmit`) with zero warnings in touched files.
3. Inspect `git_diff` line-by-line before finishing and strip any accidental debug prints, unused variables, or leftover scaffolding.

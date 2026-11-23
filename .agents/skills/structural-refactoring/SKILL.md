---
name: structural-refactoring
description: Safe cross-file symbol refactoring and diagnostic verification inspired by Serena LSP and ast-grep. Use when renaming exported symbols, splitting packages, or changing function signatures across multiple files.
metadata:
  fabric.roles: "writer,fixer"
---

# Structural Cross-File Refactoring Protocol

Multi-file renames, signature changes, and interface evolutions fail when agents edit files piecemeal without verifying every reference.

## 1. Enumerate Before Mutating
1. Use `graph_impact` and `search_text` to collect the complete set of definitions, call sites, test mocks, serialization tags, and documentation links for the target symbol.
2. Separate **behavior-preserving refactors** from **behavior changes** — never mix both into an unreviewable mega-diff.

## 2. Atomic Edit & Diagnostic Loop
1. Update the defining type/signature first using exact unique `edit_file` anchors.
2. Update all callers and tests in deterministic package order (leaf -> consumer).
3. Run the compiler / type-checker (`go vet`, `go test`, `tsc --noEmit`, `cargo check`) immediately after each cohesive package cluster.
4. Fix every compiler diagnostic before proceeding; never leave unused imports, dead shims, or shadowed identifiers behind.

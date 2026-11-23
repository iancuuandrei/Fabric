---
name: brownfield-convention-matching
description: Adapt cleanly to existing repository idioms, helpers, folder layout, linters, and error styles before writing code in an unfamiliar or legacy codebase. Use when adding features or fixes inside an existing repository.
metadata:
  fabric.roles: "planner,explorer,writer,fixer"
---

# Brownfield Convention Matching Protocol

Foreign idioms and duplicate utilities turn small PRs into maintenance debt. Match the codebase you are inside.

## 1. Peer Inspection Before Implementation
1. Locate 2–3 comparable existing modules, handlers, commands, or tests in the target package tree using `search_text` or `list_files`.
2. Record the repo's exact conventions for:
   - Error handling and wrapping style;
   - HTTP/CLI argument parsing and output formatting;
   - Dependency injection and repository root/config resolution;
   - Test helpers, temp-dir setup, and offline HTTP/process fakes.

## 2. Reuse Internal Primitives First
- Search for existing shared helpers (`gitexec`, `safepath`, `config`, `runtimeutil`, project loggers, HTTP wrappers) before writing new ones.
- Never introduce a second logger, config parser, HTTP wrapper, or test assertion helper when the repository already maintains one.

## 3. Respect Local Governance & Confined Diffs
- Read applicable `AGENTS.md`, `CONTRIBUTING.md`, `.editorconfig`, and linter rules (`golangci-lint`, `rustfmt.toml`, `eslint`, `ruff`) governing the touched paths.
- Keep edits strictly scoped to the task objective: match surrounding formatting without reformatting untouched files or refactoring unrelated legacy functions.

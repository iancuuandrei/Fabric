---
name: repo-onboarding-and-integration
description: Wire a repository quickly for Fabric or agent-assisted workflows using accurate build/test checks, concise AGENTS.md instructions, local SKILL.md overrides, zero-secret config, and IDE/CI integration. Use when bootstrapping or integrating a repository.
metadata:
  fabric.roles: "planner,explorer,writer"
---

# Repository Onboarding & Integration Protocol

Configure any codebase for safe autonomous research, bug fixing, PR generation, and team automation in minutes.

## 1. Discover & Encode Ground-Truth Commands
1. Inspect build manifests (`go.mod`, `package.json`, `Cargo.toml`, `pyproject.toml`, `Makefile`) and CI workflows (`.github/workflows/`) to identify the exact test, vet, format, and build commands.
2. Encode verification as deterministic `argv` arrays (`[[verification]]` and `[[hooks]]`) in `fabric.toml` — never rely on guessed shell commands.

## 2. Author Concise Repository Guidance & Skills
- Keep root `AGENTS.md` brief and factual: repository directory map, pinned build/test commands, architectural boundaries, and non-negotiable invariants (under ~100 lines where possible).
- Place specialized team workflows under `.agents/skills/<name>/SKILL.md` with single-line YAML frontmatter (`name`, `description`), optional `metadata.fabric.roles`, and concise steps (`<= 500` lines), or install built-ins via `fabric skills install`.

## 3. Wire Team Tooling Safely
- Configure `[budget]` caps, `[sandbox]` isolation (`inherit_secrets = false`), and `[integrations.*]` using `*_env` env-var indirection only — never commit API keys or webhook URLs.
- Connect IDE agents via stdio MCP (`fabric mcp`), CI/automation via `fabric ticket` / `fabric agent --notify`, and incoming webhooks via `fabric serve` with fail-closed signing secrets.
- Verify readiness end-to-end with `fabric doctor --human`, `fabric integrations`, and a non-mutating check run.

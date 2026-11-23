---
name: ci-and-supply-chain-hygiene
description: Harden GitHub Actions workflows, dependency lockfiles, subprocess invocations, webhook/auth verification, and UI rendering against injection and supply-chain compromise. Use when editing workflows, Docker/build scripts, subprocess callers, auth/webhooks, or HTML templates.
metadata:
  fabric.roles: "planner,writer,fixer,reviewer"
---

# CI, Execution & Supply-Chain Hygiene Protocol (Donor: Trail of Bits + SonarCloud Rules)

Treat CI workflows, external binaries, webhooks, and rendered UI strings as untrusted-input boundaries.

## 1. CI Workflows & Locked Builds
- Pin third-party GitHub Actions to immutable 40-character commit SHAs (`uses: owner/repo@<sha> # vX.Y.Z`), never floating `@main` or mutable major tags.
- Enforce minimal `permissions:` per workflow/job and avoid interpolated `${{ github.event.* }}` inside `run:` shell scripts (pass via `env:` instead).
- Require locked dependency resolution in CI (`cargo ... --locked`, `pnpm install --frozen-lockfile`, verified `go.mod`/`go.sum`).

## 2. Subprocess & Auth Boundaries
- Eliminate PATH-dependent command execution where possible (`go:S4036`): use safe resolved executables (`exec.LookPath` verified wrapper like `gitexec`) and explicit `argv` slices — never shell interpolation (`sh -c`, `cmd /c`) with dynamic input.
- Scrub sensitive environment variables before launching untrusted or model-driven child processes (`inherit_secrets = false`).
- Fail closed on inbound webhooks and privileged endpoints (`503` when signing secret unset, `401` on mismatch) using constant-time comparison (`hmac.Equal` / `subtle.ConstantTimeCompare`).
- Never embed secrets/tokens in config files (`*_env` indirection only) and validate outbound endpoints against SSRF (HTTPS or explicit loopback HTTP, no embedded URL credentials).
- Escape/render UI strings safely (`textContent` / DOM builders / template escaping) — never concatenate untrusted data into raw `innerHTML`.

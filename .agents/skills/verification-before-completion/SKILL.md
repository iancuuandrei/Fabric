---
name: verification-before-completion
description: Enforce empirical evidence before claiming tests pass, bugs are fixed, or cross-platform builds succeed. Use before emitting final answers, Review Packets, or completion claims after any code, config, or doc edit.
metadata:
  fabric.roles: "writer,fixer,reviewer"
---

# Verification Before Completion Protocol (Donor: obra/superpowers + Fabric Evidence Standard)

Claiming completion from stale test results or mental simulation destroys engineering trust. No fresh proof, no pass claim.

## 1. Run Fresh Checks After the Final Edit
1. Re-run `go vet`, unit/integration tests, and doc/link checks **after** the last file modification — never cite a pass recorded before a subsequent edit.
2. When adding a new CLI subcommand, API route, or MCP tool, compile the real binary (`go build`) and run a non-destructive smoke check in addition to unit tests.
3. Verify documentation and symbol-doc gates (`internal/doccheck` or link/schema validators) when editing `.md` files or `internal/` Go packages.

## 2. Cross-Platform & Negative-Path Checks
- Verify path/OS assumptions don't hardcode Windows (`D:\...` with `filepath.IsAbs`, `\r\n`) or POSIX-only behavior without helpers when CI runs both `ubuntu-latest` and `windows-latest`.
- Confirm negative tests actually trigger and check the failure path (validation errors, unauthorized/unconfigured gates, malformed JSON/TOML, missing env vars).

## 3. Honest Status Reporting
Report exact observed scope using four explicit labels:
- **PASS:** Executed after final edit with observed exit code `0` and zero skipped assertions.
- **FAIL:** Executed and failed (quote exact failing test/command and error).
- **NOT RUN / BLOCKED:** Omitted checks with concrete missing credential, service, or platform reason — never invent or round missing metrics to zero.

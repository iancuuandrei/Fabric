---
name: security-audit
description: Perform a high-precision security review of code changes for injection, path traversal, auth/scope bypass, SSRF, secret leakage, and unsafe concurrency. Use when reviewing or implementing trust boundaries, I/O, auth, or external integrations.
metadata:
  fabric.roles: "reviewer,planner,explorer"
---

# Security Audit (Donor: Alibaba OpenCodeReview Rulesets + OWASP)

Focus strictly on concrete, exploitable or boundary-violating issues in the changed surface.

## Checklist by Attack Surface
1. **Command & Query Injection:** Reject shell interpolation (`sh -c`, `cmd /c`) with untrusted input; require explicit `argv` slices and parameterized SQL/queries.
2. **Filesystem & Path Confinement:** Verify all user/model-supplied paths are cleaned, bounded to the admitted root, and reject `..` traversal, absolute overrides, and symlink/reparse escapes.
3. **Authentication & Authorization:** Verify auth checks run before state mutation; use constant-time token comparison (`subtle.ConstantTimeCompare`); bind network servers to loopback (`127.0.0.1`) by default.
4. **Secret Hygiene:** Confirm API keys, tokens, credentials, and private user payloads are never written to logs, error messages, telemetry spans, config files, or durable journals.
5. **Network & SSRF:** Validate outbound URLs, bound response body reads (`io.LimitReader`), enforce timeouts, and reject unexpected redirects carrying auth headers.
6. **Concurrency & State Integrity:** Check for TOCTOU races, lock ordering, atomic file writes, and duplicate external side effects on retry/crash recovery.

## Reporting Standard
- Emit only verified findings with exact path, symbol, attack vector, and minimal remediation.
- Do not emit generic compliance boilerplate when no concrete vulnerability exists.

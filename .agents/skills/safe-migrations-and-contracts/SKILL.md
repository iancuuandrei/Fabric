---
name: safe-migrations-and-contracts
description: Evolve HTTP APIs, OpenAPI specs, CLI flags, config schemas, canonical hashes, and persistent state without breaking existing consumers or replay compatibility. Use when modifying DTOs, routes, serialization tags, configs, or storage formats.
metadata:
  fabric.roles: "planner,writer,fixer,reviewer"
---

# Safe Migrations & Contract Evolution Protocol

Every external wire format, CLI contract, config section, and persisted snapshot is a compatibility promise.

## 1. Expand-Contract Compatibility
- Prefer additive changes (`omitempty` optional fields, sane zero-value defaults, alias support) over destructive field renames or mandatory new config keys.
- Preserve existing canonical hash / content-addressed IDs for records that omit newly added optional fields.
- Validate new configuration keys, URLs, and bounds fail-fast at startup/load time (`Validate()`), returning actionable errors naming the exact config path.

## 2. Single Source of Truth Sync
When changing any public DTO, CLI flag, or HTTP/MCP route, update ALL coupled artifacts in one atomic change:
1. Request/response types, handlers, and router registration;
2. Embedded/checked-in OpenAPI specification (`GET /v1/openapi.json`) and MCP tool schemas;
3. CLI `--help` output and authoritative user/API documentation;
4. Contract round-trip tests and negative-validation tests (`400`/`401`/`503`).

## 3. Persistence & Replay Check
- Test loading legacy JSON/TOML artifacts missing new fields (`omitempty` forward/backward compatibility).
- Write state files atomically (`temp file -> write -> rename`) so crash/interruption never leaves truncated JSON.
- Never infer database/environment targets or run destructive migrations without explicit scope and clean-plus-prior upgrade proof.

---
name: api-and-interface-ergonomics
description: Design narrow consumer-owned interfaces, consistent REST/OpenAPI envelopes, dual human/JSON CLI UX, and clean provider adapter abstractions. Use when creating or updating HTTP endpoints, CLI commands, SDK types, or external provider clients.
metadata:
  fabric.roles: "planner,writer,fixer,reviewer"
---

# API, SDK & CLI Interface Ergonomics

Well-designed contracts make a tool trivial to integrate into existing scripts, IDEs, CI pipelines, and orchestrators.

## 1. Narrow Interfaces & Safe Defaults
- Accept narrow, behavior-focused interfaces defined near the consumer (1–4 cohesive methods) and return concrete documented structs.
- Design option structs and zero values cleanly so optional integrations, timeouts, and clients behave safely without ceremonial boilerplate.

## 2. Predictable HTTP & Webhook Contracts
- Use resource-oriented paths and consistent HTTP semantics (`GET` read-only, `POST` actions/triggers, `200`/`202` for sync vs async jobs, uniform `{"error": "..."}` error JSON).
- Document every new or changed route in the machine-readable schema (`GET /v1/openapi.json`) and keep auth/webhook behavior fail-closed.
- Bound request bodies (`MaxBytesReader`) and collection responses (explicit defaults and caps).

## 3. Scriptable CLI & Provider-Agnostic Adapters
- Every automation CLI subcommand should support `--json` structured stdout alongside clean human-readable stderr progress/tables so shell pipelines and CI bots never scrape formatted tables.
- Provide `--dry-run`, `--fetch-only`, or read-only inspection subcommands (like `doctor` / `integrations`) so operators can verify setup safely before mutating state.
- Encapsulate vendor quirks (GraphQL vs REST, Jira ADF vs plain text, provider auth headers) completely inside adapter implementations behind a unified internal domain struct.

# Fabric Documentation

Choose the path that matches what you need right now.

## 1. Start Here

| Goal | Guide | What You Get |
| --- | --- | --- |
| **Run your first task in 5 minutes** | [5-Minute Quickstart](guides/quickstart.md) | Build, `fabric init --auto`, `fabric agent`, inspect Packet, apply cleanly |
| **Use Fabric in daily production** | [Production Agent Runtime](guides/production-agent-runtime.md) | `agent`, `--resume`, live steering, `tournament`, `graph`, `memory`, `skills`, `issue`/`pr`/`review-pr`, `bench`, `[budget]`, `[sandbox]`, 32 skills |
| **Integrate Jira, Linear, GitLab & Chat** | [Engineering Integrations](guides/integrations.md) | `fabric ticket`, `fabric notify`, `fabric integrations`, Slack/Discord/Teams alerts, HMAC webhooks |
| **Connect UI, API, Go SDK, or IDE MCP** | [Mission Control, API & MCP](guides/api-and-mcp.md) | Embedded Web UI, OpenAPI 3.1 + SSE routes, 17-tool MCP config, `pkg/fabric` SDK |
| **Look up a CLI flag or command** | [CLI Reference](reference/cli.md) | Full command and option catalog |

---

## 2. Core Architecture & Research

- **[System Architecture](architecture/system.md)** — Control flow, write-ownership isolation, verification gates, and evidence boundaries.
- **[Security Trust Boundary](security/trust-boundary.md)** — Isolation guarantees, secret scrubbing, CapScope enforcement, and known limits.
- **[Normative Specifications](specifications/)** & **[Architecture Decision Records (ADRs)](adr/)** — Authority, journal effects, and RI boundaries.
- **[2025–2026 Harness Research & Donors](research/harness-best-practices-2026.md)** — Empirical arXiv findings and donor mechanisms shipped in Fabric.
- **[Research Provenance](research/oss-mechanisms.md)** & **[v2 Product Roadmap](roadmap/v2-product-plan.md)**.

---

## 3. Classic Controller Internals & Historical Archives

- **[Classic Controller, Provider & RI Guides](guides/classic-internals.md)** — All ~40 deep-dive guides for the deterministic v1 task-graph controller (`fabric run --autonomous`), `fabric-ri`, Codex confinement, and OpenCode/Responses protocols.
- **Getting Started Records (v1 history):** [Windows installed-task guide](getting-started/installed-autonomous-task.md) · [Local plan route](getting-started/local-plan.md) · [Real-task record](getting-started/real-task.md)
- **Evaluations & Releases:** [Release guide](guides/release.md) · [Evaluation status ledger](evaluation/status.md) · [v1 product gate](development/v1-product-gate.md) · [v1.0.0 release acceptance](evaluation/v1-release-acceptance.md) · [Real-repo eval harness](../evals/v1/README.md)
- **Integrations & Contributing:** [Codex integration](../integrations/codex/fabric/README.md) · [Contributing](../CONTRIBUTING.md) · [Documentation standard](contributing/documentation-standard.md) · [Source history policy](contributing/source-history.md)

---

## 4. Evidence Vocabulary

Fabric separates implemented behavior from pinned run/package evidence using **PASS**, **FAIL**, **NOT RUN**, **BLOCKED**, and **UNKNOWN**. Missing timing, usage, or provider telemetry is recorded as unavailable — never zero.

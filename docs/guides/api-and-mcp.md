# Mission Control UI, HTTP/SSE API, Go SDK & MCP Guide

Fabric exposes the exact same safe repository-bound engine over four integration surfaces: **Embedded Mission Control Web UI**, **HTTP/JSON + SSE REST API**, **Stdio MCP Server**, and the **Public Go SDK** (`pkg/fabric`).

---

## 1. Mission Control Web Dashboard (`fabric serve`)

```powershell
fabric serve --addr 127.0.0.1:7420 --token my-secret
```

Open `http://127.0.0.1:7420/`:
- **Strict CSP & In-Memory Auth:** Served with `Content-Security-Policy: default-src 'self'; script-src 'self'`, zero inline `onclick` handlers, and bearer tokens stored in closure memory only (click **Auth Token** if `--token` is enabled).
- **Unified Kanban Board:** Combines active/completed v2 agentic episodes and classic controller runs, total spend, and Observation Masking token savings.
- **Interactive Drawer:** Inspect the **Review Packet**, syntax-colored **Diff** (including untracked new files), **Checkpoints & Rewind**, or **Live Events** SSE stream with a real-time **Steer** input box and **Resume (+Budget)** action.
- **Code Knowledge Graph Explorer:** Click **Code Graph** in the top bar to inspect top PageRank hubs, clusters, and trace multi-hop blast radius.

---

## 2. HTTP/JSON + SSE REST API (OpenAPI 3.1)

Fetch the live machine-readable contract at `GET /v1/openapi.json`.

### Key Agentic Episode & Graph Endpoints

```sh
# List persisted episodes newest-first
curl -H "Authorization: Bearer my-secret" http://127.0.0.1:7420/v1/episodes

# Launch an async isolated agent run
curl -X POST -H "Authorization: Bearer my-secret" -H "Content-Type: application/json" \
  -d '{"objective":"Fix boundary check and add test","max_usd":1.50,"mutation_audit":true,"allow_command":true,"async":true}' \
  http://127.0.0.1:7420/v1/episodes

# Stream live turn & tool-call events via SSE
curl -N -H "Authorization: Bearer my-secret" "http://127.0.0.1:7420/v1/episodes/<RUN_ID>/events?follow=true"

# Steer an active running episode mid-flight
curl -X POST -H "Authorization: Bearer my-secret" -H "Content-Type: application/json" \
  -d '{"message":"Also add a property test for empty input"}' \
  http://127.0.0.1:7420/v1/episodes/<RUN_ID>/steer

# Get Markdown Review Packet, rewind to checkpoint #1, or apply cleanly to a branch
curl -H "Authorization: Bearer my-secret" "http://127.0.0.1:7420/v1/episodes/<RUN_ID>/packet?format=markdown"
curl -X POST -H "Authorization: Bearer my-secret" -d '{"step":1}' http://127.0.0.1:7420/v1/episodes/<RUN_ID>/rewind
curl -X POST -H "Authorization: Bearer my-secret" -d '{"branch":"fabric/fix-1"}' http://127.0.0.1:7420/v1/episodes/<RUN_ID>/apply

# Query local Code Knowledge Graph & multi-hop blast radius
curl -H "Authorization: Bearer my-secret" "http://127.0.0.1:7420/v1/graph/impact?target=Server&depth=2"

# Fetch & launch a Jira/Linear/GitLab/GitHub ticket, inspect integrations, or send Slack/Discord/Teams alerts
curl -H "Authorization: Bearer my-secret" http://127.0.0.1:7420/v1/integrations
curl -X POST -H "Authorization: Bearer my-secret" -H "Content-Type: application/json" \
  -d '{"ref":"PAY-123","comment":true,"notify":true,"async":true}' http://127.0.0.1:7420/v1/tickets/run
curl -X POST -H "Authorization: Bearer my-secret" -H "Content-Type: application/json" \
  -d '{"channel":"slack"}' http://127.0.0.1:7420/v1/episodes/<RUN_ID>/notify
```

See [Engineering Integrations Guide](integrations.md) for Jira, Linear, GitLab, GitHub, Slack/Discord/Teams, and HMAC-SHA256 inbound webhook details (`/v1/webhooks/{provider}`).

---

## 3. Stdio MCP Server (17 Tools)

Add Fabric to Claude Code, Codex, Cursor, or any MCP-compatible IDE:

```json
{
  "mcpServers": {
    "fabric": {
      "command": "fabric",
      "args": ["mcp", "--root", "D:/path/to/your/repo"]
    }
  }
}
```

**Exposed MCP tools:**
- **Episodes & Artifacts:** `fabric_episodes`, `fabric_packet`, `fabric_rewind`, `fabric_apply`, `fabric_mutate`
- **Trackers & Notifications:** `fabric_ticket`, `fabric_notify`
- **Code Graph & Memory:** `fabric_graph`, `fabric_graph_impact`, `fabric_memory`
- **Classic Controller:** `fabric_doctor`, `fabric_status`, `fabric_run`, `fabric_inspect`, `fabric_diff`, `fabric_usage`, `fabric_diagnose`

---

## 4. Public Go SDK (`github.com/iancuuandrei/fabric/pkg/fabric`)

```go
client, err := fabric.Open(".")
ep, err := client.RunAgent(ctx, fabric.AgentOptions{
    Objective:     "Fix retry backoff jitter and add unit tests",
    MaxUSD:        1.50,
    AllowCommand:  true,
    MutationAudit: true,
})
pkt, err := client.ReviewPacket(ep.RunID)
fmt.Println(pkt.Markdown())
err = client.ApplyEpisode(ctx, ep.RunID, "fabric/fix-jitter")
```

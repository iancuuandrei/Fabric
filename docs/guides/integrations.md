# Engineering Integrations Guide (Jira, Linear, GitLab, GitHub, Slack, Discord, Teams & Webhooks)

Fabric integrates cleanly into existing team workflows without ever placing API tokens, passwords, or chat webhook URLs inside `fabric.toml`. Every credential references an environment variable name (`*_env`), external endpoints require HTTPS (or explicit loopback `http://` for local testing), and inbound webhooks fail closed unless a signing secret is present in the environment.

---

## 1. Quick Configuration (`fabric.toml`)

```toml
# Issue Trackers (configure one or more; bare keys like PROJ-123 auto-resolve)
[integrations.jira]
base_url = "https://your-org.atlassian.net"
email_env = "JIRA_EMAIL"           # Omit for Jira Server / DC PAT Bearer auth
token_env = "JIRA_API_TOKEN"
api_version = "3"                  # "3" (Cloud ADF, default) or "2" (Server / Data Center)
auto_comment = true                # Post Review Packet summary back to the Jira issue
ready_transition = "In Review"     # Optional workflow transition when episode reaches READY

[integrations.linear]
token_env = "LINEAR_API_KEY"
auto_comment = true
ready_state = "In Review"

[integrations.gitlab]
base_url = "https://gitlab.com"
project = "acme/platform/backend"
token_env = "GITLAB_TOKEN"
auto_comment = true

[integrations.github]
auto_comment = true                # Uses authenticated `gh` CLI

# Chat Alerts & Outbound HMAC Webhooks
[integrations.notify]
slack_webhook_env = "FABRIC_SLACK_WEBHOOK"
discord_webhook_env = "FABRIC_DISCORD_WEBHOOK"
teams_webhook_env = "FABRIC_TEAMS_WEBHOOK"
webhook_url = "https://automation.example.com/fabric-hook"
webhook_secret_env = "FABRIC_OUTBOUND_SECRET"   # Sends X-Fabric-Signature-256: sha256=<hex>
events = ["READY", "NEEDS_ATTENTION", "FAILED", "BUDGET_EXCEEDED"]

# Verified Inbound Triggers (`POST /v1/webhooks/{provider}`)
[integrations.webhooks]
secret_env = "FABRIC_WEBHOOK_SECRET"
trigger_label = "fabric"           # Optional label filter (e.g. only run when labeled `fabric`)
default_max_usd = 2.00
default_max_turns = 24
auto_comment = true
auto_notify = true
```

Check readiness without printing secrets:

```powershell
fabric integrations
```

---

## 2. Ticket Workflow (`fabric ticket`)

Run an isolated agent from any Jira, Linear, GitLab, or GitHub ticket key or full browser URL:

```powershell
# Jira Cloud / Data Center (fetches ticket, runs isolated worktree agent, comments & transitions)
fabric ticket --max-usd 1.50 --mutate --comment --transition "In Review" PAY-123
fabric ticket https://your-org.atlassian.net/browse/PAY-123

# Linear GraphQL
fabric ticket --provider linear --pr --notify ENG-42

# GitLab REST v4
fabric ticket https://gitlab.com/acme/backend/-/issues/19 --comment

# GitHub Issues (`gh`) + automatic PR creation with Review Packet
fabric ticket --provider github --pr --comment #88

# Inspect normalized ticket JSON without running the agent
fabric ticket --fetch-only PAY-123
```

Zero-config env var defaults when `[integrations]` uses defaults:
- `JIRA_API_TOKEN` (+ optional `JIRA_EMAIL`)
- `LINEAR_API_KEY`
- `GITLAB_TOKEN`
- `FABRIC_SLACK_WEBHOOK`, `FABRIC_DISCORD_WEBHOOK`, `FABRIC_TEAMS_WEBHOOK`

---

## 3. Team Notifications (`fabric notify` & `fabric agent --notify`)

Dispatch an episode's **Structured Review Packet** (status, objective, diff churn, spend, mutation kill score, PR link) to Slack Block Kit, Discord Embeds, Microsoft Teams MessageCards, or an HMAC-signed JSON webhook:

```powershell
# Notify automatically at the end of a run
fabric agent --notify --max-usd 1.50 "Fix retry backoff jitter"

# Notify manually for the latest or a specific episode
fabric notify --channel slack --ticket PAY-123
fabric notify --channel all --pr-url https://github.com/acme/repo/pull/45 <RUN_ID>
```

Outbound webhook requests sign the exact JSON body using `HMAC-SHA256` and set `X-Fabric-Signature-256: sha256=<hex>`.

---

## 4. Verified Inbound Webhooks (`fabric serve`)

When Mission Control is running (`fabric serve`), external systems (Jira Automation, Linear Webhooks, GitHub Issues, n8n, PagerDuty, Sentry, CI) can trigger isolated runs via:

```http
POST /v1/webhooks/{provider}
```

where `{provider}` is `jira`, `linear`, `github`, or `generic`:
- **Fail-closed:** Returns `503 Service Unavailable` if `secret_env` is unset or empty, and `401 Unauthorized` if signature verification fails (`X-Hub-Signature-256` for GitHub, `Linear-Signature` for Linear, `X-Fabric-Signature-256` / `X-Webhook-Secret` / `Authorization: Bearer` for Jira and generic callers).
- **Label gating:** If `trigger_label` is configured and absent on the issue payload, returns `200 OK` with `{"status":"ignored",...}` without consuming model budget.
- **Async isolated execution:** Accepted payloads return `202 Accepted` with `job_id` and `run_id`, execute inside an isolated Git worktree bounded by `default_max_usd` and `default_max_turns`, and optionally auto-comment and auto-notify on completion.

---

## 5. REST API & MCP Surfaces

- **REST API:** `GET /v1/integrations`, `POST /v1/tickets/fetch`, `POST /v1/tickets/run`, `POST /v1/episodes/{id}/notify`, `POST /v1/webhooks/{provider}` (documented live at `/v1/openapi.json`).
- **MCP Tools (`fabric mcp`):** `fabric_ticket` (`fetch` | `comment` | `transition`) and `fabric_notify` (`all` | `slack` | `discord` | `teams` | `webhook`).

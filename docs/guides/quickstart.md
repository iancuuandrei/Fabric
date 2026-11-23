# 5-Minute Quickstart

Get Fabric running on any committed Git repository in five steps.

## 1. Prerequisites

- **Git** (2.35+)
- **Go 1.27+** (`D:\Tools\go1.27.4\go\bin\go.exe` or `go` on `PATH`)
- At least one model runtime:
  - **Claude Code CLI** (`claude`), OR
  - **API Key** (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, OpenRouter, or local Ollama/vLLM), OR
  - **Codex CLI** / **OpenCode** / any **ACP stdio** agent

## 2. Build the Native Binaries

```powershell
git clone https://github.com/iancuuandrei/Fabric.git Fabric
Set-Location Fabric
go build -trimpath -o bin/fabric.exe ./cmd/fabric
cargo build --release --bin fabric-ri   # optional Rust repository intelligence
```

## 3. Initialize Your Target Repository

Inside any committed Git repository you want Fabric to work on:

```powershell
# Auto-detect Claude Code CLI, API keys, Codex, and project test runner
fabric init --auto

# Or pick a backend explicitly:
fabric init --claude
fabric init --api anthropic --model claude-sonnet-4-5
fabric init --api openai --base-url http://127.0.0.1:11434/v1 --model qwen2.5-coder:32b

# Verify runtimes, checks, budget, and sandbox readiness
fabric doctor --human
```

## 4. Run, Inspect, and Safely Apply

```powershell
# Launch an isolated coding run with a $1.50 spend cap and mutation audit
fabric agent --max-usd 1.50 --mutate "Fix edge-case input parsing and add regression tests"

# Check recent episodes and classic runs
fabric status --human

# Render the structured Review Packet (blast radius, test receipts, cost, churn)
fabric packet

# Optional: inspect or rewind automatic pre-edit Git checkpoints
fabric rewind --list
fabric rewind --to 1

# Dry-run validate, then apply cleanly to a new branch
fabric apply --dry-run
fabric apply --branch fabric/fix-parsing
```

If an episode stops early at a budget limit, bump the budget and resume from the exact saved turn:

```powershell
fabric agent --resume <RUN_PREFIX> --max-usd 3.00 --max-turns 32
```

## 5. Open Mission Control

```powershell
fabric serve --addr 127.0.0.1:7420
# Open http://127.0.0.1:7420/ in your browser
```

Next: read the [Production Agent Runtime Guide](production-agent-runtime.md) or [API, Mission Control & MCP Guide](api-and-mcp.md).

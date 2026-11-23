// Package mcpserver implements a stdio Model Context Protocol (MCP) server
// exposing Fabric orchestration, agentic episodes, code knowledge graph,
// review packets, checkpoints, and mutation auditing to Claude Code, Codex, and IDEs.
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/codegraph"
	"github.com/iancuuandrei/fabric/internal/integrations"
	"github.com/iancuuandrei/fabric/internal/memory"
	"github.com/iancuuandrei/fabric/internal/mutation"
	"github.com/iancuuandrei/fabric/internal/packet"
	"github.com/iancuuandrei/fabric/internal/rewind"
	"github.com/iancuuandrei/fabric/internal/sandbox"
	"github.com/iancuuandrei/fabric/pkg/fabric"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type toolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type toolOutput struct {
	text    string
	rawJSON json.RawMessage
}

func (o toolOutput) asText() string {
	if o.text != "" {
		return o.text
	}
	return string(o.rawJSON)
}

var tools = []toolSpec{
	{
		Name:        "fabric_doctor",
		Description: "Validate Fabric repository configuration, runtime availability, hooks, and verification checks.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "fabric_status",
		Description: "List validated classic Fabric runs in the repository.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "fabric_run",
		Description: "Start a bounded autonomous Fabric coding run for an objective.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"objective"},
			"properties": map[string]any{
				"objective":           map[string]any{"type": "string", "description": "Engineering objective to implement and verify"},
				"preset":              map[string]any{"type": "string", "enum": []string{"standard", "fast", "thorough"}},
				"max_repairs":         map[string]any{"type": "integer"},
				"repair_intelligence": map[string]any{"type": "boolean"},
			},
		},
	},
	{
		Name:        "fabric_inspect",
		Description: "Inspect the replay-validated state of a classic Fabric run.",
		InputSchema: map[string]any{
			"type":       "object",
			"required":   []string{"run_id"},
			"properties": map[string]any{"run_id": map[string]any{"type": "string"}},
		},
	},
	{
		Name:        "fabric_diff",
		Description: "Show the isolated candidate diff for a classic Fabric run.",
		InputSchema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"run_id": map[string]any{"type": "string"}},
		},
	},
	{
		Name:        "fabric_usage",
		Description: "Inspect replay-validated token usage for a classic Fabric run.",
		InputSchema: map[string]any{
			"type":       "object",
			"required":   []string{"run_id"},
			"properties": map[string]any{"run_id": map[string]any{"type": "string"}},
		},
	},
	{
		Name:        "fabric_diagnose",
		Description: "Project structured repair findings and admitted scope for a classic Fabric run.",
		InputSchema: map[string]any{
			"type":       "object",
			"required":   []string{"run_id"},
			"properties": map[string]any{"run_id": map[string]any{"type": "string"}},
		},
	},
	{
		Name:        "fabric_episodes",
		Description: "List production agentic episodes (fabric agent / fabric tournament runs) in this repository.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "fabric_packet",
		Description: "Generate a Structured Review Packet (Markdown or JSON) for an agentic episode.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"run_id"},
			"properties": map[string]any{
				"run_id": map[string]any{"type": "string"},
				"format": map[string]any{"type": "string", "enum": []string{"markdown", "json"}},
			},
		},
	},
	{
		Name:        "fabric_rewind",
		Description: "Rewind an agentic episode worktree to a lightweight Git checkpoint step.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"run_id", "step"},
			"properties": map[string]any{
				"run_id": map[string]any{"type": "string"},
				"step":   map[string]any{"type": "integer"},
			},
		},
	},
	{
		Name:        "fabric_apply",
		Description: "Safely validate and apply an agentic episode worktree patch onto the repository (optionally creating a new branch).",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"run_id"},
			"properties": map[string]any{
				"run_id": map[string]any{"type": "string"},
				"branch": map[string]any{"type": "string"},
			},
		},
	},
	{
		Name:        "fabric_mutate",
		Description: "Run a fast diff-scoped mutation audit on an agentic episode worktree to score test strength.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"run_id"},
			"properties": map[string]any{
				"run_id":      map[string]any{"type": "string"},
				"max_mutants": map[string]any{"type": "integer"},
			},
		},
	},
	{
		Name:        "fabric_graph",
		Description: "Build Fabric's local Code Knowledge Graph and return the structural RepoMap report (top PageRank hub nodes, clusters, and symbol outlines).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"format": map[string]any{"type": "string", "enum": []string{"markdown", "json"}},
			},
		},
	},
	{
		Name:        "fabric_graph_impact",
		Description: "Compute multi-hop blast radius (callers, callees, linked docs) for a file path or symbol in the local Code Knowledge Graph.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"target"},
			"properties": map[string]any{
				"target": map[string]any{"type": "string", "description": "File path or symbol name"},
				"depth":  map[string]any{"type": "integer", "description": "Hop depth (1..4, default 2)"},
			},
		},
	},
	{
		Name:        "fabric_memory",
		Description: "Manage verified cross-run project lessons (.harness/memory/lessons.jsonl): action=list|add|delete.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"list", "add", "delete"}},
				"scope":  map[string]any{"type": "string", "description": "Optional package/file scope for action=add"},
				"rule":   map[string]any{"type": "string", "description": "Lesson rule text for action=add"},
				"id":     map[string]any{"type": "string", "description": "Lesson ID/prefix for action=delete"},
			},
		},
	},
	{
		Name:        "fabric_ticket",
		Description: "Interact with Jira, Linear, GitLab, or GitHub issues: action=fetch|comment|transition.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"action", "ref"},
			"properties": map[string]any{
				"action":       map[string]any{"type": "string", "enum": []string{"fetch", "comment", "transition"}},
				"ref":          map[string]any{"type": "string", "description": "Ticket key or URL (e.g. PROJ-123, ENG-42, #42)"},
				"provider":     map[string]any{"type": "string", "description": "Optional override: jira|linear|gitlab|github"},
				"project":      map[string]any{"type": "string", "description": "Optional GitLab group/repo path override"},
				"run_id":       map[string]any{"type": "string", "description": "Optional episode ID whose Review Packet summarizes action=comment"},
				"comment":      map[string]any{"type": "string", "description": "Comment text (for action=comment)"},
				"target_state": map[string]any{"type": "string", "description": "Target transition/state name (for action=transition)"},
			},
		},
	},
	{
		Name:        "fabric_notify",
		Description: "Send an episode Review Packet alert to Slack, Discord, Microsoft Teams, or signed generic webhook.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"run_id":     map[string]any{"type": "string", "description": "Episode run_id or prefix (defaults to latest)"},
				"channel":    map[string]any{"type": "string", "enum": []string{"all", "slack", "discord", "teams", "webhook"}},
				"ticket_key": map[string]any{"type": "string"},
				"pr_url":     map[string]any{"type": "string"},
			},
		},
	},
}

// ServeStdio serves MCP JSON-RPC 2.0 requests over in/out until EOF or context cancellation.
func ServeStdio(ctx context.Context, client *fabric.Client, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	enc := json.NewEncoder(out)

	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		// Notifications have no id and require no reply.
		if len(req.ID) == 0 {
			continue
		}
		resp := dispatch(ctx, client, req)
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func dispatch(ctx context.Context, client *fabric.Client, req rpcRequest) map[string]any {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]string{"name": "fabric", "version": "2.0.0-dev"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			},
		}
	case "tools/list":
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{"tools": tools},
		}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return rpcErr(req.ID, -32602, "invalid params")
		}
		out, err := invokeToolSafe(ctx, client, params.Name, params.Arguments)
		if err != nil {
			return map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"isError": true,
					"content": []map[string]string{{"type": "text", "text": err.Error()}},
				},
			}
		}
		return map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]any{
				"content": []map[string]string{{"type": "text", "text": out.asText()}},
			},
		}
	default:
		return rpcErr(req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

func invokeToolSafe(ctx context.Context, client *fabric.Client, name string, args json.RawMessage) (out toolOutput, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %s panicked: %v", name, r)
		}
	}()
	return invokeTool(ctx, client, name, args)
}

func invokeTool(ctx context.Context, client *fabric.Client, name string, args json.RawMessage) (toolOutput, error) {
	switch name {
	case "fabric_doctor":
		raw, err := client.Doctor(ctx)
		return toolOutput{rawJSON: raw}, err
	case "fabric_status":
		raw, err := client.Status(ctx)
		return toolOutput{rawJSON: raw}, err
	case "fabric_run":
		var opts fabric.RunOptions
		if err := json.Unmarshal(args, &opts); err != nil {
			return toolOutput{}, err
		}
		raw, err := client.RunAutonomous(ctx, opts)
		return toolOutput{rawJSON: raw}, err
	case "fabric_inspect":
		var p struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return toolOutput{}, err
		}
		raw, err := client.Inspect(ctx, p.RunID)
		return toolOutput{rawJSON: raw}, err
	case "fabric_diff":
		var p struct {
			RunID string `json:"run_id"`
		}
		_ = json.Unmarshal(args, &p)
		raw, err := client.Diff(ctx, p.RunID)
		return toolOutput{rawJSON: raw}, err
	case "fabric_usage":
		var p struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return toolOutput{}, err
		}
		raw, err := client.Usage(ctx, p.RunID)
		return toolOutput{rawJSON: raw}, err
	case "fabric_diagnose":
		var p struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return toolOutput{}, err
		}
		raw, err := client.Diagnose(ctx, p.RunID)
		return toolOutput{rawJSON: raw}, err
	case "fabric_episodes":
		eps, err := agentloop.ListEpisodes(client.Root)
		if err != nil {
			return toolOutput{}, err
		}
		sort.Slice(eps, func(i, j int) bool { return eps[i].StartedAt.After(eps[j].StartedAt) })
		b, err := json.Marshal(eps)
		return toolOutput{rawJSON: b}, err
	case "fabric_packet":
		var p struct {
			RunID  string `json:"run_id"`
			Format string `json:"format"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return toolOutput{}, err
		}
		ep, err := agentloop.LoadEpisode(client.Root, p.RunID)
		if err != nil {
			return toolOutput{}, err
		}
		pkt := packet.FromEpisode(ep)
		if p.Format == "json" {
			b, err := json.Marshal(pkt)
			return toolOutput{rawJSON: b}, err
		}
		return toolOutput{text: pkt.Markdown()}, nil
	case "fabric_rewind":
		var p struct {
			RunID string `json:"run_id"`
			Step  int    `json:"step"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return toolOutput{}, err
		}
		ep, err := agentloop.LoadEpisode(client.Root, p.RunID)
		if err != nil {
			return toolOutput{}, err
		}
		snap, err := rewind.Restore(ctx, ep.WorkDir, ep.RunID, p.Step)
		if err != nil {
			return toolOutput{}, err
		}
		b, err := json.Marshal(snap)
		return toolOutput{rawJSON: b}, err
	case "fabric_apply":
		var p struct {
			RunID  string `json:"run_id"`
			Branch string `json:"branch"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return toolOutput{}, err
		}
		ep, err := agentloop.LoadEpisode(client.Root, p.RunID)
		if err != nil {
			return toolOutput{}, err
		}
		if err := packet.ApplyWorktreePatch(ctx, client.Root, ep.WorkDir, p.Branch); err != nil {
			return toolOutput{}, err
		}
		b, err := json.Marshal(map[string]string{"status": "applied", "run_id": ep.RunID, "branch": p.Branch})
		return toolOutput{rawJSON: b}, err
	case "fabric_mutate":
		var p struct {
			RunID      string `json:"run_id"`
			MaxMutants int    `json:"max_mutants"`
		}
		if err := json.Unmarshal(args, &p); err != nil {
			return toolOutput{}, err
		}
		ep, err := agentloop.LoadEpisode(client.Root, p.RunID)
		if err != nil {
			return toolOutput{}, err
		}
		cfg, err := agentloop.LoadRepoConfig(client.Root)
		if err != nil || len(cfg.Verification) == 0 {
			return toolOutput{}, fmt.Errorf("no verification command configured in fabric.toml")
		}
		sb := sandbox.Config{}
		if cfg.Sandbox != nil {
			sb = *cfg.Sandbox
		}
		rep, err := mutation.AuditDiff(ctx, mutation.Config{
			WorkDir:    ep.WorkDir,
			TestArgv:   cfg.Verification[0].Argv,
			Sandbox:    sb,
			MaxMutants: p.MaxMutants,
		})
		if err != nil {
			return toolOutput{}, err
		}
		ep.Mutation = &rep
		_ = agentloop.SaveEpisode(client.Root, ep)
		b, err := json.Marshal(rep)
		return toolOutput{rawJSON: b}, err
	case "fabric_graph":
		var p struct {
			Format string `json:"format"`
		}
		_ = json.Unmarshal(args, &p)
		g, err := codegraph.Build(client.Root)
		if err != nil {
			return toolOutput{}, err
		}
		if p.Format == "json" {
			b, err := json.Marshal(g)
			return toolOutput{rawJSON: b}, err
		}
		return toolOutput{text: g.ReportMarkdown()}, nil
	case "fabric_graph_impact":
		var p struct {
			Target string `json:"target"`
			Depth  int    `json:"depth"`
		}
		if err := json.Unmarshal(args, &p); err != nil || p.Target == "" {
			return toolOutput{}, fmt.Errorf("target is required")
		}
		g, err := codegraph.Build(client.Root)
		if err != nil {
			return toolOutput{}, err
		}
		b, err := json.Marshal(g.Impact(p.Target, p.Depth))
		return toolOutput{rawJSON: b}, err
	case "fabric_memory":
		var p struct {
			Action string `json:"action"`
			Scope  string `json:"scope"`
			Rule   string `json:"rule"`
			ID     string `json:"id"`
		}
		_ = json.Unmarshal(args, &p)
		switch p.Action {
		case "", "list":
			lessons, err := memory.List(client.Root)
			if err != nil {
				return toolOutput{}, err
			}
			b, err := json.Marshal(lessons)
			return toolOutput{rawJSON: b}, err
		case "add":
			l, err := memory.Add(client.Root, memory.Lesson{Scope: p.Scope, Rule: p.Rule})
			if err != nil {
				return toolOutput{}, err
			}
			b, err := json.Marshal(l)
			return toolOutput{rawJSON: b}, err
		case "delete":
			if err := memory.Delete(client.Root, p.ID); err != nil {
				return toolOutput{}, err
			}
			b, err := json.Marshal(map[string]string{"status": "deleted", "id": p.ID})
			return toolOutput{rawJSON: b}, err
		default:
			return toolOutput{}, fmt.Errorf("unsupported memory action %q (use list, add, or delete)", p.Action)
		}
	case "fabric_ticket":
		var p struct {
			Action      string `json:"action"`
			Ref         string `json:"ref"`
			Provider    string `json:"provider"`
			Project     string `json:"project"`
			RunID       string `json:"run_id"`
			Comment     string `json:"comment"`
			TargetState string `json:"target_state"`
		}
		if err := json.Unmarshal(args, &p); err != nil || p.Ref == "" {
			return toolOutput{}, fmt.Errorf("action and ref are required")
		}
		var intCfg *integrations.Config
		if cfg, cfgErr := agentloop.LoadRepoConfig(client.Root); cfgErr == nil {
			intCfg = cfg.Integrations
		}
		tr, key, err := integrations.ResolveTrackerForRef(intCfg, p.Ref, p.Provider, p.Project, client.Root, nil)
		if err != nil {
			return toolOutput{}, err
		}
		switch p.Action {
		case "fetch":
			tk, err := tr.FetchTicket(ctx, key)
			if err != nil {
				return toolOutput{}, err
			}
			b, err := json.Marshal(tk)
			return toolOutput{rawJSON: b}, err
		case "comment":
			body := p.Comment
			if body == "" && p.RunID != "" {
				ep, err := agentloop.LoadEpisode(client.Root, p.RunID)
				if err != nil {
					return toolOutput{}, err
				}
				body = packet.FromEpisode(ep).Markdown()
			}
			if err := tr.PostComment(ctx, key, body); err != nil {
				return toolOutput{}, err
			}
			b, err := json.Marshal(map[string]string{"status": "commented", "provider": tr.Provider(), "key": key})
			return toolOutput{rawJSON: b}, err
		case "transition":
			if err := tr.Transition(ctx, key, p.TargetState); err != nil {
				return toolOutput{}, err
			}
			b, err := json.Marshal(map[string]string{"status": "transitioned", "provider": tr.Provider(), "key": key, "state": p.TargetState})
			return toolOutput{rawJSON: b}, err
		default:
			return toolOutput{}, fmt.Errorf("unsupported ticket action %q (use fetch, comment, or transition)", p.Action)
		}
	case "fabric_notify":
		var p struct {
			RunID     string `json:"run_id"`
			Channel   string `json:"channel"`
			TicketKey string `json:"ticket_key"`
			PRURL     string `json:"pr_url"`
		}
		_ = json.Unmarshal(args, &p)
		eps, err := agentloop.ListEpisodes(client.Root)
		if err != nil {
			return toolOutput{}, err
		}
		if len(eps) == 0 {
			return toolOutput{}, fmt.Errorf("no episodes recorded")
		}
		sort.Slice(eps, func(i, j int) bool { return eps[i].StartedAt.After(eps[j].StartedAt) })
		ep := eps[0]
		if p.RunID != "" {
			loaded, err := agentloop.LoadEpisode(client.Root, p.RunID)
			if err != nil {
				return toolOutput{}, err
			}
			ep = loaded
		}
		var notifyCfg *integrations.NotifyConfig
		if cfg, cfgErr := agentloop.LoadRepoConfig(client.Root); cfgErr == nil && cfg.Integrations != nil {
			notifyCfg = cfg.Integrations.Notify
		}
		res := integrations.NotifyAll(ctx, notifyCfg, packet.NotificationFromEpisode(ep, p.TicketKey, "", p.PRURL), p.Channel, nil)
		b, err := json.Marshal(res)
		return toolOutput{rawJSON: b}, err
	default:
		return toolOutput{}, fmt.Errorf("unknown tool: %s", name)
	}
}

func rpcErr(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	}
}

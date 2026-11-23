// Command fabric runs Fabric's native command-line interface, production agentic
// loop (`fabric agent`, `fabric tournament`), review packets, checkpoint rewind,
// mutation auditing, Mission Control + HTTP/SSE API server (`fabric serve`), and
// stdio MCP server (`fabric mcp`).
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/iancuuandrei/fabric/internal/api"
	"github.com/iancuuandrei/fabric/internal/entrypoint"
	"github.com/iancuuandrei/fabric/internal/mcpserver"
	"github.com/iancuuandrei/fabric/pkg/fabric"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		printFabricHelp()
		return
	}
	switch os.Args[1] {
	case "agent":
		if err := runAgent(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric agent:", err)
			os.Exit(1)
		}
		return
	case "tournament":
		if err := runTournament(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric tournament:", err)
			os.Exit(1)
		}
		return
	case "packet":
		if err := runPacket(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric packet:", err)
			os.Exit(1)
		}
		return
	case "apply":
		if err := runApply(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric apply:", err)
			os.Exit(1)
		}
		return
	case "rewind":
		if err := runRewind(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric rewind:", err)
			os.Exit(1)
		}
		return
	case "mutate":
		if err := runMutate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric mutate:", err)
			os.Exit(1)
		}
		return
	case "graph":
		if err := runGraph(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric graph:", err)
			os.Exit(1)
		}
		return
	case "clean":
		if err := runClean(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric clean:", err)
			os.Exit(1)
		}
		return
	case "memory":
		if err := runMemory(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric memory:", err)
			os.Exit(1)
		}
		return
	case "issue":
		if err := runIssue(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric issue:", err)
			os.Exit(1)
		}
		return
	case "pr":
		if err := runPR(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric pr:", err)
			os.Exit(1)
		}
		return
	case "review-pr":
		if err := runReviewPR(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric review-pr:", err)
			os.Exit(1)
		}
		return
	case "bench":
		if err := runBench(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric bench:", err)
			os.Exit(1)
		}
		return
	case "ticket":
		if err := runTicket(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric ticket:", err)
			os.Exit(1)
		}
		return
	case "notify":
		if err := runNotify(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric notify:", err)
			os.Exit(1)
		}
		return
	case "integrations":
		if err := runIntegrations(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric integrations:", err)
			os.Exit(1)
		}
		return
	case "skills":
		if err := runSkills(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric skills:", err)
			os.Exit(1)
		}
		return
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric serve:", err)
			os.Exit(1)
		}
		return
	case "mcp":
		if err := runMCP(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric mcp:", err)
			os.Exit(1)
		}
		return
	case "watch":
		if err := runWatch(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "fabric watch:", err)
			os.Exit(1)
		}
		return
	case "status", "doctor":
		if hasFlag(os.Args[2:], "--human") {
			if err := runHumanCommand(os.Args[1], os.Args[2:], os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "fabric "+os.Args[1]+":", err)
				os.Exit(1)
			}
			return
		}
	}
	entrypoint.Run("fabric", os.Args[1:], nil)
}

func printFabricHelp() {
	fmt.Print(`Fabric — Repository-Bound Autonomous Software Engineering Runtime

Build & Verify:
  agent       Run or --resume an isolated agentic loop (skills, inline diagnostics, memory, budget)
  tournament  Run parallel multi-model contenders and pick the Pareto-optimal winner
  mutate      Run a fast diff-scoped mutation audit against any Git worktree
  bench       Run a JSONL empirical benchmark scorecard (Pass@1, cost, cache, mutation)
  run         Run the classic replay-validated plan/write/verify/review task graph

Inspect & Ship:
  status      List agentic episodes and classic runs (use --human for terminal tables)
  packet      Render the Structured Review Packet (Markdown or --json) for an episode
  apply       Safely validate (--dry-run) and apply a READY episode patch onto a branch
  rewind      List or restore lightweight Git worktree checkpoints (--list | --to STEP)
  watch       Stream live progress for a running classic controller run
  graph       Build local Code Knowledge Graph and trace multi-hop blast radius (--impact)
  issue       Fetch a GitHub issue (via gh) and launch an isolated Fabric agent run
  ticket      Run an isolated agent from Jira, Linear, GitLab, or GitHub (--comment | --transition | --pr)
  pr          Apply a READY episode onto a branch, commit, and open a PR with its Review Packet
  review-pr   Run Alibaba-style filter-only precision review on any GitHub PR diff

Server, Team Integrations & Memory:
  serve       Launch embedded Mission Control Web UI + HTTP/SSE REST API + Webhooks + OpenAPI 3.1
  mcp         Start the 17-tool stdio Model Context Protocol (MCP) server
  memory      Manage verified cross-run project lessons (list | add | rm)
  notify       Send an episode Review Packet to Slack, Discord, Teams, or signed webhooks
  integrations Inspect configured trackers, chat channels, and inbound webhook readiness
  skills       List, inspect, or install Fabric's 32 built-in SWE skills into any repo

Setup & Maintenance:
  init        Initialize fabric.toml (--auto | --claude | --api anthropic|openai | --acp | --codex)
  doctor      Validate runtimes, verification checks, hooks, budget, and sandbox (--human)
  clean       Prune old agent worktrees & branches while preserving episode packets (--dry-run)

Quickstart:
  fabric init --auto && fabric doctor --human
  fabric agent --max-usd 1.50 --mutate "Fix bug and add regression tests"
  fabric packet && fabric apply --branch fabric/fix-bug

Run 'fabric <command> --help' for command-specific flags.
`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	addr := fs.String("addr", "127.0.0.1:7420", "listen address (loopback recommended)")
	token := fs.String("token", os.Getenv("FABRIC_API_TOKEN"), "bearer auth token (or set FABRIC_API_TOKEN)")
	cors := fs.Bool("cors", false, "allow CORS headers for browser UIs")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := fabric.Open(*root)
	if err != nil {
		return err
	}
	handler := api.NewServer(api.ServerConfig{
		Client:    client,
		AuthToken: *token,
		AllowCORS: *cors,
	})
	srv := &http.Server{
		Addr:              *addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	fmt.Fprintf(os.Stderr, "Fabric Mission Control UI: http://%s/\n", *addr)
	fmt.Fprintf(os.Stderr, "Fabric OpenAPI 3.1 Spec:   http://%s/v1/openapi.json (root=%s, auth=%v)\n", *addr, client.Root, *token != "")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func runMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := fabric.Open(*root)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return mcpserver.ServeStdio(ctx, client, os.Stdin, os.Stdout)
}

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/pkg/fabric"
)

func runWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	interval := fs.Duration("interval", time.Second, "poll interval")
	once := fs.Bool("once", false, "print current snapshot once and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("watch accepts at most one RUN ID")
	}
	client, err := fabric.Open(*root)
	if err != nil {
		return err
	}
	runID := ""
	if fs.NArg() == 1 {
		runID = fs.Arg(0)
	} else {
		raw, err := client.Status(context.Background())
		if err != nil {
			return err
		}
		var runs []struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(raw, &runs); err != nil || len(runs) == 0 {
			return errors.New("no runs found in repository; pass RUN explicitly")
		}
		runID = runs[len(runs)-1].RunID
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return watchRun(ctx, client, runID, *interval, *once, os.Stdout)
}

func watchRun(ctx context.Context, client *fabric.Client, runID string, interval time.Duration, once bool, out io.Writer) error {
	if interval <= 0 {
		interval = time.Second
	}
	seenEvents := 0
	lastState := ""

	render := func() (bool, error) {
		raw, err := client.Inspect(ctx, runID)
		if err != nil {
			return false, err
		}
		var snap struct {
			RunID     string `json:"run_id"`
			State     string `json:"state"`
			PlanID    string `json:"plan_id"`
			Lifecycle struct {
				Status string `json:"status"`
			} `json:"lifecycle"`
			Creation struct {
				Objective string `json:"objective"`
			} `json:"creation"`
		}
		_ = json.Unmarshal(raw, &snap)
		if snap.State != lastState {
			if lastState == "" {
				fmt.Fprintf(out, "Fabric Watch | run=%s\n", shortID(runID))
				if snap.Creation.Objective != "" {
					fmt.Fprintf(out, "Objective: %s\n", singleLine(snap.Creation.Objective, 120))
				}
			}
			lifecycle := snap.Lifecycle.Status
			if lifecycle == "" {
				lifecycle = "ACTIVE"
			}
			fmt.Fprintf(out, "[%s] state=%s lifecycle=%s", time.Now().Format("15:04:05"), snap.State, lifecycle)
			if snap.PlanID != "" {
				fmt.Fprintf(out, " plan=%s", shortID(snap.PlanID))
			}
			fmt.Fprintln(out)
			lastState = snap.State
		}

		if evBytes, err := client.ExportEvents(ctx, runID); err == nil {
			sc := bufio.NewScanner(bytes.NewReader(evBytes))
			sc.Buffer(make([]byte, 64<<10), 4<<20)
			idx := 0
			for sc.Scan() {
				line := bytes.TrimSpace(sc.Bytes())
				if len(line) == 0 {
					continue
				}
				idx++
				if idx <= seenEvents {
					continue
				}
				var ev struct {
					Sequence int    `json:"sequence"`
					Kind     string `json:"kind"`
				}
				if json.Unmarshal(line, &ev) == nil {
					fmt.Fprintf(out, "  #%03d %s\n", ev.Sequence, ev.Kind)
				}
				seenEvents = idx
			}
		}

		terminal := snap.State == "READY" || snap.Lifecycle.Status == "PAUSED" || snap.Lifecycle.Status == "CANCELLED"
		return terminal, nil
	}

	done, err := render()
	if err != nil || done || once {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			done, err := render()
			if err != nil || done {
				return err
			}
		}
	}
}

func runHumanCommand(cmd string, args []string, out io.Writer) error {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	_ = fs.Bool("human", true, "human-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, err := fabric.Open(*root)
	if err != nil {
		return err
	}
	switch cmd {
	case "status":
		eps, _ := agentloop.ListEpisodes(client.Root)
		sort.Slice(eps, func(i, j int) bool { return eps[i].StartedAt.After(eps[j].StartedAt) })
		if len(eps) > 0 {
			limit := min(10, len(eps))
			fmt.Fprintf(out, "AGENTIC EPISODES (showing %d of %d)\n", limit, len(eps))
			fmt.Fprintf(out, "%-14s %-15s %-20s %-10s %-8s %-6s %s\n", "ID", "STATUS", "MODEL", "CHURN", "COST", "MUT", "OBJECTIVE")
			for _, ep := range eps[:limit] {
				mut := "-"
				if ep.Mutation != nil && ep.Mutation.TotalMutants > 0 {
					mut = fmt.Sprintf("%.0f%%", ep.Mutation.Score*100)
				}
				churn := fmt.Sprintf("+%d/-%d", ep.DiffLinesAdded, ep.DiffLinesRemoved)
				fmt.Fprintf(out, "%-14s %-15s %-20s %-10s $%-7.4f %-6s %s\n",
					shortID(ep.RunID), ep.Status, singleLine(ep.Model, 20), churn, ep.CostEstimate.EstimatedUSD, mut, singleLine(ep.Objective, 44))
			}
			fmt.Fprintf(out, "Next: fabric packet %s | fabric apply --dry-run %s\n\n", shortID(eps[0].RunID), shortID(eps[0].RunID))
		}

		raw, err := client.Status(context.Background())
		if err != nil {
			if len(eps) > 0 {
				return nil
			}
			return err
		}
		var runs []struct {
			RunID     string `json:"run_id"`
			State     string `json:"state"`
			PlanID    string `json:"plan_id"`
			Lifecycle string `json:"lifecycle"`
		}
		if err := json.Unmarshal(raw, &runs); err != nil {
			return err
		}
		if len(runs) == 0 {
			if len(eps) == 0 {
				fmt.Fprintln(out, "No episodes or classic runs recorded in this repository.")
			}
			return nil
		}
		fmt.Fprintln(out, "CLASSIC CONTROLLER RUNS")
		fmt.Fprintf(out, "%-14s %-18s %-12s %s\n", "RUN", "STATE", "LIFECYCLE", "PLAN")
		for _, r := range runs {
			fmt.Fprintf(out, "%-14s %-18s %-12s %s\n", shortID(r.RunID), r.State, r.Lifecycle, shortID(r.PlanID))
		}
		return nil

	case "doctor":
		raw, err := client.Doctor(context.Background())
		if err != nil {
			return err
		}
		var doc struct {
			Status        string         `json:"status"`
			ExecutionPlan map[string]any `json:"execution_plan"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			return err
		}
		fmt.Fprintf(out, "Fabric Doctor: %s\n", doc.Status)
		if checks, ok := doc.ExecutionPlan["verification_checks"].([]any); ok {
			fmt.Fprintln(out, "Verification checks:")
			for _, c := range checks {
				if m, ok := c.(map[string]any); ok {
					fmt.Fprintf(out, "  - %-20v %v\n", m["name"], m["readiness"])
				}
			}
		}
		if rts, ok := doc.ExecutionPlan["external_runtimes"].(map[string]any); ok && len(rts) > 0 {
			fmt.Fprintln(out, "External runtimes:")
			for name, v := range rts {
				if m, ok := v.(map[string]any); ok {
					fmt.Fprintf(out, "  - %-20s %v\n", name, m["status"])
				}
			}
		}
		if hooksList, ok := doc.ExecutionPlan["hooks"].([]any); ok && len(hooksList) > 0 {
			fmt.Fprintln(out, "Lifecycle hooks:")
			for _, h := range hooksList {
				if m, ok := h.(map[string]any); ok {
					fmt.Fprintf(out, "  - %-20v (%v) %v\n", m["name"], m["event"], m["readiness"])
				}
			}
		}
		if repoCfg, cfgErr := agentloop.LoadRepoConfig(client.Root); cfgErr == nil {
			fmt.Fprintln(out, "Production guardrails:")
			if repoCfg.Budget != nil {
				fmt.Fprintf(out, "  - %-20s max_usd=$%.2f max_turns=%d max_tokens=%d\n", "budget", repoCfg.Budget.MaxUSD, repoCfg.Budget.MaxTurns, repoCfg.Budget.MaxTokens)
			} else {
				fmt.Fprintf(out, "  - %-20s INFO (uncapped — add [budget] max_usd in fabric.toml or pass --max-usd)\n", "budget")
			}
			if repoCfg.Sandbox != nil && repoCfg.Sandbox.Mode != "" && repoCfg.Sandbox.Mode != "local" {
				sbStatus := "OK"
				if _, err := exec.LookPath(repoCfg.Sandbox.Mode); err != nil {
					sbStatus = "MISSING (" + repoCfg.Sandbox.Mode + " not on PATH)"
				} else {
					pctx, pcancel := context.WithTimeout(context.Background(), 2*time.Second)
					if err := exec.CommandContext(pctx, repoCfg.Sandbox.Mode, "info").Run(); err != nil {
						sbStatus = "DAEMON_UNREACHABLE"
					}
					pcancel()
				}
				fmt.Fprintf(out, "  - %-20s %s (mode=%s image=%s)\n", "sandbox", sbStatus, repoCfg.Sandbox.Mode, repoCfg.Sandbox.Image)
			} else {
				fmt.Fprintf(out, "  - %-20s INFO (local worktree execution; set [sandbox] mode=\"docker\" for container isolation)\n", "sandbox")
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported human command %q", cmd)
	}
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name || strings.HasPrefix(a, name+"=") {
			return true
		}
	}
	return false
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	if id == "" {
		return "-"
	}
	return id
}

func singleLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

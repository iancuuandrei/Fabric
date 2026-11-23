package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/bench"
	"github.com/iancuuandrei/fabric/internal/codegraph"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/contextcompiler"
	"github.com/iancuuandrei/fabric/internal/cost"
	"github.com/iancuuandrei/fabric/internal/gitexec"
	"github.com/iancuuandrei/fabric/internal/githubflow"
	"github.com/iancuuandrei/fabric/internal/integrations"
	"github.com/iancuuandrei/fabric/internal/memory"
	"github.com/iancuuandrei/fabric/internal/mutation"
	"github.com/iancuuandrei/fabric/internal/packet"
	"github.com/iancuuandrei/fabric/internal/rewind"
	"github.com/iancuuandrei/fabric/internal/sandbox"
	"github.com/iancuuandrei/fabric/internal/tournament"
)

func runAgent(args []string) error {
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	resumeID := fs.String("resume", "", "resume an existing episode ID (supports short prefix)")
	apiDialect := fs.String("api", "", "HTTP API dialect override (anthropic|openai)")
	baseURL := fs.String("base-url", "", "custom API base URL (OpenRouter, Ollama, vLLM, etc.)")
	apiKeyEnv := fs.String("api-key-env", "", "environment variable holding API key")
	modelName := fs.String("model", "", "model identifier override")
	claudeBin := fs.String("claude-bin", "", "explicit Claude CLI executable override")
	maxUSD := fs.Float64("max-usd", 0, "hard USD spend cap (0 = use fabric.toml or unlimited)")
	maxTurns := fs.Int("max-turns", 24, "maximum agentic turns")
	keepRecent := fs.Int("keep-recent", 4, "observation masking window (keep last N tool outputs verbatim)")
	mutate := fs.Bool("mutate", false, "run diff-scoped mutation audit after native checks pass")
	review := fs.Bool("review", true, "run fresh-context skill-backed precision review before READY (pass --review=false to disable)")
	allowCmd := fs.Bool("allow-cmd", true, "allow bounded argv run_command inside isolated worktree")
	writeScope := fs.String("write", "", "comma-separated repo-relative write scope allowlist (optional)")
	sbMode := fs.String("sandbox", "", "execution sandbox override (local|docker|podman)")
	sbImage := fs.String("sandbox-image", "", "container image when --sandbox docker|podman")
	notifyFlag := fs.Bool("notify", false, "dispatch Review Packet alert to configured Slack/Discord/Teams/webhooks")
	asJSON := fs.Bool("json", false, "output complete Episode JSON instead of Markdown review packet")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	objective := strings.TrimSpace(strings.Join(fs.Args(), " "))
	resolvedResume := ""
	if strings.TrimSpace(*resumeID) != "" {
		ep, rErr := resolveEpisodeArg(absRoot, []string{*resumeID})
		if rErr != nil {
			return rErr
		}
		resolvedResume = ep.RunID
	} else if objective == "" {
		return errors.New("usage: fabric agent [--resume RUN] [flags] OBJECTIVE")
	}

	var repoCfg *config.Config
	if loaded, cfgErr := agentloop.LoadRepoConfig(absRoot); cfgErr == nil {
		repoCfg = &loaded
	}
	model, err := agentloop.ModelFromConfig(repoCfg, agentloop.ModelOverrides{
		Dialect:   *apiDialect,
		BaseURL:   *baseURL,
		APIKeyEnv: *apiKeyEnv,
		Model:     *modelName,
		ClaudeBin: *claudeBin,
	})
	if err != nil {
		return err
	}

	var allowed []string
	for _, p := range strings.Split(*writeScope, ",") {
		if t := strings.TrimSpace(p); t != "" {
			allowed = append(allowed, t)
		}
	}

	loopCfg := agentloop.Config{
		RepoRoot:        absRoot,
		ResumeRunID:     resolvedResume,
		Objective:       objective,
		Model:           model,
		AllowedWrite:    allowed,
		MaxTurns:        *maxTurns,
		KeepRecentTurns: *keepRecent,
		MutationAudit:   *mutate,
		AllowCommand:    *allowCmd,
		Budget:          cost.Budget{MaxUSD: *maxUSD, MaxTurns: *maxTurns},
	}
	if *review {
		loopCfg.AutoReview = true
	}
	applyRepoDefaults(&loopCfg, repoCfg, *sbMode, *sbImage)
	if !*asJSON {
		loopCfg.OnEvent = func(ev agentloop.Event) {
			fmt.Fprintf(os.Stderr, "[T%02d %-16s] %s\n", ev.Turn, ev.Kind, ev.Detail)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ep, err := agentloop.Run(ctx, loopCfg)
	if err != nil {
		return err
	}
	var notifyCfg *integrations.NotifyConfig
	if repoCfg != nil && repoCfg.Integrations != nil {
		notifyCfg = repoCfg.Integrations.Notify
	}
	if *notifyFlag || integrations.ShouldNotify(notifyCfg, ep.Status) {
		dispatchNotifications(ctx, notifyCfg, ep, "", "", "")
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(ep)
	}
	fmt.Println()
	fmt.Print(packet.FromEpisode(ep).Markdown())
	fmt.Printf("\nWorktree: %s\n", ep.WorkDir)
	if ep.Status == "READY" {
		fmt.Printf("Next step: fabric apply --branch fabric/%s %s\n", shortID(ep.RunID), shortID(ep.RunID))
	}
	return nil
}

func runTournament(args []string) error {
	fs := flag.NewFlagSet("tournament", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	modelsCSV := fs.String("models", "", "comma-separated contender models (e.g. claude-sonnet-4-5,gpt-5)")
	apiDialect := fs.String("api", "", "HTTP API dialect override (anthropic|openai)")
	baseURL := fs.String("base-url", "", "custom API base URL")
	apiKeyEnv := fs.String("api-key-env", "", "environment variable holding API key")
	maxUSD := fs.Float64("max-usd", 0, "hard USD cap per contender")
	maxTurns := fs.Int("max-turns", 24, "maximum turns per contender")
	mutate := fs.Bool("mutate", false, "score contenders using diff-scoped mutation audit")
	allowCmd := fs.Bool("allow-cmd", true, "allow bounded run_command inside isolated worktrees")
	sbMode := fs.String("sandbox", "", "execution sandbox override (local|docker|podman)")
	sbImage := fs.String("sandbox-image", "", "container image when --sandbox docker|podman")
	asJSON := fs.Bool("json", false, "output Tournament Report JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	objective := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if objective == "" {
		return errors.New("usage: fabric tournament [--models m1,m2] [flags] OBJECTIVE")
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	var repoCfg *config.Config
	if loaded, cfgErr := agentloop.LoadRepoConfig(absRoot); cfgErr == nil {
		repoCfg = &loaded
	}

	rawModels := []string{}
	for _, m := range strings.Split(*modelsCSV, ",") {
		if t := strings.TrimSpace(m); t != "" {
			rawModels = append(rawModels, t)
		}
	}
	if len(rawModels) == 0 {
		rawModels = []string{"", ""}
	}

	contenders := make([]tournament.Contender, 0, len(rawModels))
	for i, m := range rawModels {
		dialect := *apiDialect
		if dialect == "" && m != "" {
			lm := strings.ToLower(m)
			if strings.HasPrefix(lm, "claude") {
				dialect = "anthropic"
			} else if strings.HasPrefix(lm, "gpt") || strings.HasPrefix(lm, "o1") || strings.HasPrefix(lm, "o3") || strings.HasPrefix(lm, "o4") {
				dialect = "openai"
			}
		}
		mdl, err := agentloop.ModelFromConfig(repoCfg, agentloop.ModelOverrides{
			Dialect:   dialect,
			BaseURL:   *baseURL,
			APIKeyEnv: *apiKeyEnv,
			Model:     m,
		})
		if err != nil {
			return fmt.Errorf("contender #%d (%s): %w", i+1, m, err)
		}
		contenders = append(contenders, tournament.Contender{
			Label: fmt.Sprintf("c%d-%s", i+1, mdl.Name()),
			Model: mdl,
		})
	}

	tCfg := tournament.Config{
		RepoRoot:      absRoot,
		Objective:     objective,
		Contenders:    contenders,
		Budget:        cost.Budget{MaxUSD: *maxUSD, MaxTurns: *maxTurns},
		MutationAudit: *mutate,
		AllowCommand:  *allowCmd,
	}
	if repoCfg != nil {
		tCfg.Verification = repoCfg.Verification
		tCfg.Hooks = repoCfg.Hooks
		if repoCfg.Sandbox != nil {
			tCfg.Sandbox = *repoCfg.Sandbox
		}
	}
	if *sbMode != "" {
		tCfg.Sandbox.Mode = *sbMode
	}
	if *sbImage != "" {
		tCfg.Sandbox.Image = *sbImage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if !*asJSON {
		fmt.Fprintf(os.Stderr, "Launching %d isolated parallel contenders...\n", len(contenders))
	}
	rep, err := tournament.Run(ctx, tCfg)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}

	fmt.Printf("\n## Fabric Patch Tournament (%d contenders)\n\n", len(rep.Candidates))
	fmt.Printf("| Contender | Status | Mutation | Churn | Cost | Duration |\n| --- | --- | ---: | ---: | ---: | ---: |\n")
	for _, c := range rep.Candidates {
		mut := "-"
		if c.Episode.Mutation != nil && c.Episode.Mutation.TotalMutants > 0 {
			mut = fmt.Sprintf("%.0f%%", c.Episode.Mutation.Score*100)
		}
		status := c.Episode.Status
		if c.Error != "" {
			status = "ERR"
		}
		fmt.Printf("| `%s` | **%s** | %s | +%d/-%d | $%.4f | %.1fs |\n",
			c.Label, status, mut, c.Episode.DiffLinesAdded, c.Episode.DiffLinesRemoved,
			c.Episode.CostEstimate.EstimatedUSD, float64(c.Episode.DurationMS)/1000.0)
	}
	fmt.Printf("\n**Decision:** %s\n", rep.Reason)
	if rep.Winner != nil {
		fmt.Printf("\n**Winner:** `%s` (`%s`)\n\n", rep.Winner.Label, rep.Winner.Episode.RunID)
		fmt.Print(packet.FromEpisode(rep.Winner.Episode).Markdown())
		fmt.Printf("\nNext step: fabric apply --branch fabric/%s %s\n", shortID(rep.Winner.Episode.RunID), shortID(rep.Winner.Episode.RunID))
	}
	return nil
}

func runPacket(args []string) error {
	fs := flag.NewFlagSet("packet", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	asJSON := fs.Bool("json", false, "output ReviewPacket JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	ep, err := resolveEpisodeArg(absRoot, fs.Args())
	if err != nil {
		return err
	}
	pkt := packet.FromEpisode(ep)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(pkt)
	}
	fmt.Print(pkt.Markdown())
	return nil
}

func runApply(args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	branch := fs.String("branch", "", "create and checkout a new Git branch before applying")
	dryRun := fs.Bool("dry-run", false, "validate clean repo and patch without mutating files or branches")
	force := fs.Bool("force", false, "allow applying an episode whose status is not READY")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	ep, err := resolveEpisodeArg(absRoot, fs.Args())
	if err != nil {
		return err
	}
	if ep.Status != "READY" && !*force {
		return fmt.Errorf("episode %s has status %s (pass --force to apply anyway)", ep.RunID, ep.Status)
	}
	if *dryRun {
		stat, err := packet.ApplyDryRun(context.Background(), absRoot, ep.WorkDir, strings.TrimSpace(*branch))
		if err != nil {
			return err
		}
		fmt.Printf("Dry-run OK for episode %s:\n%s\n", shortID(ep.RunID), stat)
		return nil
	}
	if err := packet.ApplyWorktreePatch(context.Background(), absRoot, ep.WorkDir, strings.TrimSpace(*branch)); err != nil {
		return err
	}
	if *branch != "" {
		fmt.Printf("Applied episode %s cleanly onto new branch %s.\n", shortID(ep.RunID), *branch)
	} else {
		fmt.Printf("Applied episode %s cleanly onto current repository branch.\n", shortID(ep.RunID))
	}
	return nil
}

func runRewind(args []string) error {
	fs := flag.NewFlagSet("rewind", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	list := fs.Bool("list", false, "list recorded checkpoints")
	toStep := fs.Int("to", -1, "checkpoint step number to restore")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	ep, err := resolveEpisodeArg(absRoot, fs.Args())
	if err != nil {
		return err
	}
	if *list || *toStep < 0 {
		snaps, err := rewind.List(context.Background(), ep.WorkDir, ep.RunID)
		if err != nil {
			return err
		}
		if len(snaps) == 0 {
			fmt.Println("No checkpoints recorded for episode", ep.RunID)
			return nil
		}
		fmt.Printf("Checkpoints for episode %s (%s):\n", shortID(ep.RunID), ep.WorkDir)
		for _, s := range snaps {
			fmt.Printf("  #%02d  %-24s  %s  %s\n", s.Step, s.Label, shortID(s.Commit), s.CreatedAt.Format("15:04:05"))
		}
		return nil
	}
	snap, err := rewind.Restore(context.Background(), ep.WorkDir, ep.RunID, *toStep)
	if err != nil {
		return err
	}
	fmt.Printf("Rewound worktree %s to checkpoint #%d (%s, %s).\n", ep.WorkDir, snap.Step, snap.Label, shortID(snap.Commit))
	return nil
}

func runMutate(args []string) error {
	fs := flag.NewFlagSet("mutate", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	dir := fs.String("dir", "", "target Git worktree directory (defaults to --root)")
	maxMutants := fs.Int("max", 12, "maximum mutants to test")
	sbMode := fs.String("sandbox", "", "execution sandbox override (local|docker|podman)")
	sbImage := fs.String("sandbox-image", "", "container image when --sandbox docker|podman")
	asJSON := fs.Bool("json", false, "output Mutation Report JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	workDir := absRoot
	if *dir != "" {
		workDir, err = filepath.Abs(*dir)
		if err != nil {
			return err
		}
	}
	argv := fs.Args()
	sb := sandbox.Config{}
	if cfg, cfgErr := agentloop.LoadRepoConfig(absRoot); cfgErr == nil {
		if len(argv) == 0 && len(cfg.Verification) > 0 {
			argv = cfg.Verification[0].Argv
		}
		if cfg.Sandbox != nil {
			sb = *cfg.Sandbox
		}
	}
	if len(argv) == 0 {
		return errors.New("usage: fabric mutate [--dir PATH] [--max N] -- <test command argv...>")
	}
	if *sbMode != "" {
		sb.Mode = *sbMode
	}
	if *sbImage != "" {
		sb.Image = *sbImage
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	rep, err := mutation.AuditDiff(ctx, mutation.Config{
		WorkDir:    workDir,
		TestArgv:   argv,
		Sandbox:    sb,
		MaxMutants: *maxMutants,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	if rep.Status == "BASELINE_FAILED" {
		fmt.Printf("Fabric Mutation Audit aborted: %s\n", rep.Error)
		return nil
	}
	fmt.Printf("Fabric Mutation Audit: %d/%d killed (%.1f%%)\n", rep.Killed, rep.TotalMutants, rep.Score*100)
	for _, m := range rep.Mutants {
		fmt.Printf("  [%s] %s:%d  %s  (%q -> %q)\n", m.Status, m.Path, m.Line, m.Operator, m.Original, m.Mutated)
	}
	return nil
}

func runGraph(args []string) error {
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	impact := fs.String("impact", "", "trace multi-hop blast radius for a symbol or file path")
	specTarget := fs.String("spec", "", "render Javadoc-style Spec Cards for matching symbol(s) or file path")
	depth := fs.Int("depth", 2, "impact hop depth (1..4)")
	save := fs.Bool("save", false, "write .harness/graph/graph.json and GRAPH_REPORT.md")
	asJSON := fs.Bool("json", false, "output JSON instead of Markdown")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	g, err := codegraph.Build(absRoot)
	if err != nil {
		return err
	}
	if *save {
		jp, mp, err := g.Save(absRoot)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Saved Code Knowledge Graph: %s and %s\n", jp, mp)
	}
	if strings.TrimSpace(*specTarget) != "" {
		cards := g.SpecCardsForTarget(*specTarget, 8)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{"target": *specTarget, "cards": cards})
		}
		if len(cards) == 0 {
			fmt.Printf("No symbols matched %q.\n", *specTarget)
			return nil
		}
		fmt.Print(strings.Join(cards, "\n"))
		return nil
	}
	if strings.TrimSpace(*impact) != "" {
		rep := g.Impact(*impact, *depth)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(rep)
		}
		fmt.Printf("## Blast Radius for `%s` (depth=%d)\n", rep.Target, rep.Depth)
		fmt.Printf("Matched (%d): %s\n", len(rep.MatchedIDs), strings.Join(rep.MatchedIDs, ", "))
		fmt.Printf("\nUpstream callers (%d):\n", len(rep.Callers))
		for _, n := range rep.Callers {
			fmt.Printf("  - %s (%s:%d)\n", n.Name, n.Path, n.Line)
		}
		fmt.Printf("\nDownstream callees/imports (%d):\n", len(rep.Callees))
		for _, n := range rep.Callees {
			fmt.Printf("  - %s (%s:%d)\n", n.Name, n.Path, n.Line)
		}
		if len(rep.Types) > 0 {
			fmt.Printf("\nReferenced types (%d):\n", len(rep.Types))
			for _, n := range rep.Types {
				fmt.Printf("  - %s (%s:%d)\n", n.Name, n.Path, n.Line)
			}
		}
		if len(rep.Tests) > 0 {
			fmt.Printf("\nCovering tests (%d):\n", len(rep.Tests))
			for _, n := range rep.Tests {
				fmt.Printf("  - %s (%s:%d)\n", n.Name, n.Path, n.Line)
			}
		}
		if len(rep.Docs) > 0 {
			fmt.Printf("\nLinked docs (%d):\n", len(rep.Docs))
			for _, n := range rep.Docs {
				fmt.Printf("  - %s\n", n.Path)
			}
		}
		return nil
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(g)
	}
	fmt.Print(g.ReportMarkdown())
	return nil
}

func runClean(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	keep := fs.Int("keep", 5, "number of most recent agent episode worktrees to keep")
	dryRun := fs.Bool("dry-run", false, "preview worktrees and branches that would be removed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	eps, err := agentloop.ListEpisodes(absRoot)
	if err != nil {
		return err
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].StartedAt.After(eps[j].StartedAt) })
	keepDirs := map[string]bool{}
	for i, ep := range eps {
		if i < *keep {
			keepDirs[filepath.Clean(ep.WorkDir)] = true
		}
	}

	wtGlob := filepath.Join(absRoot, ".harness", "worktrees", "agent-*")
	matches, _ := filepath.Glob(wtGlob)
	removed := 0
	for _, wt := range matches {
		if keepDirs[filepath.Clean(wt)] {
			continue
		}
		branch := "fabric/" + filepath.Base(wt)
		if *dryRun {
			fmt.Printf("[dry-run] would remove worktree %s (branch %s)\n", wt, branch)
			removed++
			continue
		}
		_ = gitexec.CommandContext(context.Background(), "-C", absRoot, "worktree", "remove", "--force", wt).Run()
		_ = os.RemoveAll(wt)
		_ = gitexec.CommandContext(context.Background(), "-C", absRoot, "branch", "-D", "--", branch).Run()
		fmt.Printf("Removed worktree %s (branch %s)\n", wt, branch)
		removed++
	}
	if !*dryRun {
		_ = gitexec.CommandContext(context.Background(), "-C", absRoot, "worktree", "prune").Run()
	}
	fmt.Printf("Fabric clean complete: %d worktree(s) %s (retained latest %d; episode packets preserved).\n",
		removed, map[bool]string{true: "eligible", false: "removed"}[*dryRun], *keep)
	return nil
}

func runMemory(args []string) error {
	fs := flag.NewFlagSet("memory", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	scope := fs.String("scope", "", "package or file scope (used with `add`)")
	evidence := fs.String("evidence", "", "verification command or commit backing the lesson")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	rest := fs.Args()
	sub := "list"
	if len(rest) > 0 {
		sub = rest[0]
		rest = rest[1:]
	}
	switch sub {
	case "list":
		lessons, err := memory.List(absRoot)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(lessons)
		}
		if len(lessons) == 0 {
			fmt.Println("No verified project lessons recorded yet. Add one with: fabric memory add --scope go \"...\"")
			return nil
		}
		fmt.Printf("%-10s %-18s %s\n", "ID", "SCOPE", "RULE")
		for _, l := range lessons {
			sc := l.Scope
			if sc == "" {
				sc = "repo"
			}
			fmt.Printf("%-10s %-18s %s\n", l.ID, sc, l.Rule)
		}
		return nil
	case "add":
		// Support flags after `add` as well as before `add`.
		addFS := flag.NewFlagSet("memory add", flag.ContinueOnError)
		addRoot := addFS.String("root", absRoot, "repository root")
		addScope := addFS.String("scope", *scope, "scope")
		addEv := addFS.String("evidence", *evidence, "evidence")
		if err := addFS.Parse(rest); err != nil {
			return err
		}
		rule := strings.TrimSpace(strings.Join(addFS.Args(), " "))
		if rule == "" {
			return errors.New("usage: fabric memory add [--scope SCOPE] \"RULE\"")
		}
		l, err := memory.Add(*addRoot, memory.Lesson{Scope: *addScope, Rule: rule, Evidence: *addEv})
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(l)
		}
		fmt.Printf("Saved project lesson [%s] (%s): %s\n", l.ID, l.Scope, l.Rule)
		return nil
	case "rm", "del", "delete":
		if len(rest) == 0 {
			return errors.New("usage: fabric memory rm LESSON_ID")
		}
		if err := memory.Delete(absRoot, rest[0]); err != nil {
			return err
		}
		fmt.Printf("Removed project lesson %s.\n", rest[0])
		return nil
	default:
		return fmt.Errorf("unknown memory subcommand %q (use list, add, or rm)", sub)
	}
}

func runIssue(args []string) error {
	fs := flag.NewFlagSet("issue", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	apiDialect := fs.String("api", "", "HTTP API dialect override")
	modelName := fs.String("model", "", "model override")
	maxUSD := fs.Float64("max-usd", 0, "hard USD spend cap")
	maxTurns := fs.Int("max-turns", 24, "maximum agent turns")
	mutate := fs.Bool("mutate", false, "run diff-scoped mutation audit")
	review := fs.Bool("review", true, "run skill-backed precision review before READY (pass --review=false to disable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: fabric issue [flags] ISSUE_NUMBER_OR_URL")
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	iss, err := githubflow.FetchIssue(ctx, absRoot, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Fetched issue #%d: %s\n", iss.Number, iss.Title)
	agentArgs := []string{
		"--root", absRoot,
		"--max-turns", fmt.Sprintf("%d", *maxTurns),
	}
	if *apiDialect != "" {
		agentArgs = append(agentArgs, "--api", *apiDialect)
	}
	if *modelName != "" {
		agentArgs = append(agentArgs, "--model", *modelName)
	}
	if *maxUSD > 0 {
		agentArgs = append(agentArgs, "--max-usd", fmt.Sprintf("%.2f", *maxUSD))
	}
	if *mutate {
		agentArgs = append(agentArgs, "--mutate")
	}
	agentArgs = append(agentArgs, fmt.Sprintf("--review=%t", *review))
	agentArgs = append(agentArgs, githubflow.BuildIssueObjective(iss))
	return runAgent(agentArgs)
}

func runPR(args []string) error {
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	branch := fs.String("branch", "", "new branch name to create and push")
	title := fs.String("title", "", "PR title (defaults to episode objective)")
	draft := fs.Bool("draft", false, "open as a draft pull request")
	force := fs.Bool("force", false, "allow non-READY episode")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	ep, err := resolveEpisodeArg(absRoot, fs.Args())
	if err != nil {
		return err
	}
	if ep.Status != "READY" && !*force {
		return fmt.Errorf("episode %s has status %s (pass --force to override)", shortID(ep.RunID), ep.Status)
	}
	br := strings.TrimSpace(*branch)
	if br == "" {
		br = "fabric/fix-" + shortID(ep.RunID)
	}
	prTitle := strings.TrimSpace(*title)
	if prTitle == "" {
		prTitle = strings.Join(strings.Fields(ep.Objective), " ")
		if len(prTitle) > 72 {
			prTitle = prTitle[:72]
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	url, err := applyAndOpenPR(ctx, absRoot, ep, br, prTitle, *draft)
	if err != nil {
		return err
	}
	fmt.Printf("Opened Pull Request: %s\n", url)
	return nil
}

func runReviewPR(args []string) error {
	fs := flag.NewFlagSet("review-pr", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	apiDialect := fs.String("api", "", "HTTP API dialect override")
	baseURL := fs.String("base-url", "", "custom API base URL")
	modelName := fs.String("model", "", "reviewer model override")
	asJSON := fs.Bool("json", false, "output JSON verdict")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: fabric review-pr [flags] PR_NUMBER_OR_URL")
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	var repoCfg *config.Config
	if loaded, cfgErr := agentloop.LoadRepoConfig(absRoot); cfgErr == nil {
		repoCfg = &loaded
	}
	model, err := agentloop.ModelFromConfig(repoCfg, agentloop.ModelOverrides{
		Dialect: *apiDialect,
		BaseURL: *baseURL,
		Model:   *modelName,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	pr, err := githubflow.FetchPR(ctx, absRoot, fs.Arg(0))
	if err != nil {
		return err
	}
	verdict, notes, usage, err := agentloop.ReviewDiff(ctx, model, fmt.Sprintf("PR #%d: %s\n\n%s", pr.Number, pr.Title, pr.Body), pr.Diff, "", nil)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"pr":      pr.Number,
			"url":     pr.URL,
			"verdict": verdict,
			"notes":   notes,
			"usage":   usage,
		})
	}
	fmt.Printf("## Fabric Precision Review — PR #%d (`%s`)\n\n- **Title:** %s\n- **Verdict:** `%s`\n- **Notes:** %s\n",
		pr.Number, pr.HeadRef, pr.Title, verdict, notes)
	return nil
}

func runBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	suitePath := fs.String("suite", "", "path to JSONL benchmark suite file")
	apiDialect := fs.String("api", "", "HTTP API dialect override")
	baseURL := fs.String("base-url", "", "custom API base URL")
	modelName := fs.String("model", "", "model identifier override")
	mutate := fs.Bool("mutate", false, "run diff-scoped mutation audit on passing tasks")
	allowCmd := fs.Bool("allow-cmd", true, "allow bounded run_command inside isolated worktrees")
	asJSON := fs.Bool("json", false, "output JSON scorecard")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*suitePath) == "" {
		return errors.New("usage: fabric bench --suite SUITE.jsonl [flags]")
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	tasks, err := bench.LoadSuite(*suitePath)
	if err != nil {
		return err
	}
	var repoCfg *config.Config
	if loaded, cfgErr := agentloop.LoadRepoConfig(absRoot); cfgErr == nil {
		repoCfg = &loaded
	}
	model, err := agentloop.ModelFromConfig(repoCfg, agentloop.ModelOverrides{
		Dialect: *apiDialect,
		BaseURL: *baseURL,
		Model:   *modelName,
	})
	if err != nil {
		return err
	}
	bCfg := bench.Config{
		RepoRoot:      absRoot,
		Model:         model,
		Tasks:         tasks,
		MutationAudit: *mutate,
		AllowCommand:  *allowCmd,
	}
	if repoCfg != nil {
		bCfg.DefaultVerify = repoCfg.Verification
		if repoCfg.Sandbox != nil {
			bCfg.Sandbox = *repoCfg.Sandbox
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	sum, err := bench.Run(ctx, bCfg)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(sum)
	}
	fmt.Print(sum.Markdown())
	return nil
}

// resolveEpisodeArg resolves an empty arg (latest episode), an exact run ID,
// or an unambiguous Git-style short prefix (e.g., `fabric packet a3f9`).
func resolveEpisodeArg(repoRoot string, args []string) (agentloop.Episode, error) {
	eps, err := agentloop.ListEpisodes(repoRoot)
	if err != nil {
		return agentloop.Episode{}, err
	}
	if len(eps) == 0 {
		return agentloop.Episode{}, errors.New("no agentic episodes found; run `fabric agent ...` first")
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].StartedAt.After(eps[j].StartedAt) })

	if len(args) == 0 || strings.TrimSpace(args[0]) == "" || strings.EqualFold(strings.TrimSpace(args[0]), "latest") {
		return eps[0], nil
	}
	q := strings.TrimSpace(args[0])
	if ep, err := agentloop.LoadEpisode(repoRoot, q); err == nil {
		return ep, nil
	}
	var matches []agentloop.Episode
	for _, ep := range eps {
		if strings.HasPrefix(strings.ToLower(ep.RunID), strings.ToLower(q)) {
			matches = append(matches, ep)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, m := range matches {
			ids = append(ids, shortID(m.RunID))
		}
		return agentloop.Episode{}, fmt.Errorf("ambiguous episode prefix %q matches: %s", q, strings.Join(ids, ", "))
	}
	return agentloop.Episode{}, fmt.Errorf("episode %q not found", q)
}

func applyRepoDefaults(loopCfg *agentloop.Config, repoCfg *config.Config, sbMode, sbImage string) {
	if repoCfg != nil {
		loopCfg.Verification = repoCfg.Verification
		loopCfg.Hooks = repoCfg.Hooks
		if repoCfg.Budget != nil {
			if loopCfg.Budget.MaxUSD == 0 {
				loopCfg.Budget.MaxUSD = repoCfg.Budget.MaxUSD
			}
			if loopCfg.Budget.MaxTokens == 0 {
				loopCfg.Budget.MaxTokens = repoCfg.Budget.MaxTokens
			}
			if loopCfg.Budget.MaxWallSeconds == 0 {
				loopCfg.Budget.MaxWallSeconds = repoCfg.Budget.MaxWallSeconds
			}
		}
		if repoCfg.Sandbox != nil {
			loopCfg.Sandbox = *repoCfg.Sandbox
		}
	}
	if sbMode != "" {
		loopCfg.Sandbox.Mode = sbMode
	}
	if sbImage != "" {
		loopCfg.Sandbox.Image = sbImage
	}
}

func applyAndOpenPR(ctx context.Context, absRoot string, ep agentloop.Episode, br, prTitle string, draft bool) (string, error) {
	if err := packet.ApplyWorktreePatch(ctx, absRoot, ep.WorkDir, br); err != nil {
		return "", err
	}
	if out, err := gitexec.CommandContext(ctx, "-C", absRoot, "add", "-A").CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := gitexec.CommandContext(ctx, "-C", absRoot, "commit", "-m", prTitle).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	body := packet.FromEpisode(ep).Markdown()
	return githubflow.OpenPR(ctx, absRoot, br, prTitle, body, draft)
}

func dispatchNotifications(ctx context.Context, cfg *integrations.NotifyConfig, ep agentloop.Episode, ticketKey, ticketURL, prURL string) {
	n := packet.NotificationFromEpisode(ep, ticketKey, ticketURL, prURL)
	results := integrations.NotifyAll(ctx, cfg, n, "all", nil)
	for _, res := range results {
		if res.Sent {
			fmt.Fprintf(os.Stderr, "Notified %s (status=%d)\n", res.Channel, res.StatusCode)
		} else if res.Error != "" {
			fmt.Fprintf(os.Stderr, "warning: notify %s: %s\n", res.Channel, res.Error)
		}
	}
}

func runTicket(args []string) error {
	fs := flag.NewFlagSet("ticket", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	provider := fs.String("provider", "", "tracker provider override (jira|linear|gitlab|github)")
	project := fs.String("project", "", "project override (e.g. GitLab group/project)")
	fetchOnly := fs.Bool("fetch-only", false, "fetch and print normalized ticket JSON without running agent")
	apiDialect := fs.String("api", "", "HTTP API dialect override (anthropic|openai)")
	baseURL := fs.String("base-url", "", "custom API base URL")
	apiKeyEnv := fs.String("api-key-env", "", "environment variable holding API key")
	modelName := fs.String("model", "", "model identifier override")
	claudeBin := fs.String("claude-bin", "", "explicit Claude CLI executable override")
	maxUSD := fs.Float64("max-usd", 0, "hard USD spend cap")
	maxTurns := fs.Int("max-turns", 24, "maximum agentic turns")
	keepRecent := fs.Int("keep-recent", 4, "observation masking window")
	mutate := fs.Bool("mutate", false, "run diff-scoped mutation audit after native checks pass")
	review := fs.Bool("review", true, "run fresh-context skill-backed precision review before READY (pass --review=false to disable)")
	allowCmd := fs.Bool("allow-cmd", true, "allow bounded argv run_command inside isolated worktree")
	writeScope := fs.String("write", "", "comma-separated repo-relative write scope allowlist")
	sbMode := fs.String("sandbox", "", "execution sandbox override (local|docker|podman)")
	sbImage := fs.String("sandbox-image", "", "container image when --sandbox docker|podman")
	comment := fs.Bool("comment", false, "post the Review Packet back onto the tracker ticket")
	transition := fs.String("transition", "", "workflow state/transition name to apply when READY")
	openPR := fs.Bool("pr", false, "apply READY patch onto a branch, commit, and open a GitHub PR")
	draft := fs.Bool("draft", false, "open PR as draft (with --pr)")
	branch := fs.String("branch", "", "branch name for --pr (defaults to fabric/<provider>-<key>)")
	notifyFlag := fs.Bool("notify", false, "dispatch Review Packet alerts to configured chat/webhook channels")
	asJSON := fs.Bool("json", false, "output combined Ticket + Episode JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: fabric ticket [--provider jira|linear|gitlab|github] [flags] TICKET_KEY_OR_URL")
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	var repoCfg *config.Config
	var intCfg *integrations.Config
	if loaded, cfgErr := agentloop.LoadRepoConfig(absRoot); cfgErr == nil {
		repoCfg = &loaded
		intCfg = repoCfg.Integrations
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	tr, key, err := integrations.ResolveTrackerForRef(intCfg, fs.Arg(0), *provider, *project, absRoot, nil)
	if err != nil {
		return err
	}
	tk, err := tr.FetchTicket(ctx, key)
	if err != nil {
		return err
	}
	if *fetchOnly {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(tk)
	}
	fmt.Fprintf(os.Stderr, "Fetched [%s] %s: %s\n", strings.ToUpper(tk.Provider), tk.Key, tk.Title)

	model, err := agentloop.ModelFromConfig(repoCfg, agentloop.ModelOverrides{
		Dialect:   *apiDialect,
		BaseURL:   *baseURL,
		APIKeyEnv: *apiKeyEnv,
		Model:     *modelName,
		ClaudeBin: *claudeBin,
	})
	if err != nil {
		return err
	}

	var allowed []string
	for _, p := range strings.Split(*writeScope, ",") {
		if t := strings.TrimSpace(p); t != "" {
			allowed = append(allowed, t)
		}
	}

	loopCfg := agentloop.Config{
		RepoRoot:        absRoot,
		Objective:       integrations.BuildObjective(tk),
		Model:           model,
		AllowedWrite:    allowed,
		MaxTurns:        *maxTurns,
		KeepRecentTurns: *keepRecent,
		MutationAudit:   *mutate,
		AllowCommand:    *allowCmd,
		Budget:          cost.Budget{MaxUSD: *maxUSD, MaxTurns: *maxTurns},
	}
	if *review {
		loopCfg.AutoReview = true
	}
	applyRepoDefaults(&loopCfg, repoCfg, *sbMode, *sbImage)
	if !*asJSON {
		loopCfg.OnEvent = func(ev agentloop.Event) {
			fmt.Fprintf(os.Stderr, "[T%02d %-16s] %s\n", ev.Turn, ev.Kind, ev.Detail)
		}
	}

	ep, err := agentloop.Run(ctx, loopCfg)
	if err != nil {
		return err
	}

	var prURL string
	if *openPR && ep.Status == "READY" {
		br := strings.TrimSpace(*branch)
		if br == "" {
			br = integrations.BranchSlug(tk)
		}
		prTitle := strings.Join(strings.Fields(fmt.Sprintf("%s: %s", tk.Key, tk.Title)), " ")
		if len(prTitle) > 72 {
			prTitle = prTitle[:72]
		}
		if u, prErr := applyAndOpenPR(ctx, absRoot, ep, br, prTitle, *draft); prErr != nil {
			fmt.Fprintf(os.Stderr, "warning: open PR: %v\n", prErr)
		} else {
			prURL = u
		}
	}

	shouldComment, targetState := resolveTicketPostActions(intCfg, tr.Provider(), *comment, *transition)
	if shouldComment {
		commentBody := packet.FromEpisode(ep).Markdown()
		if prURL != "" {
			commentBody = fmt.Sprintf("Pull Request: %s\n\n%s", prURL, commentBody)
		}
		if cErr := tr.PostComment(ctx, tk.Key, commentBody); cErr != nil {
			fmt.Fprintf(os.Stderr, "warning: ticket comment failed: %v\n", cErr)
		} else {
			fmt.Fprintf(os.Stderr, "Posted Review Packet comment to %s.\n", tk.Key)
		}
	}
	if targetState != "" && ep.Status == "READY" {
		if tErr := tr.Transition(ctx, tk.Key, targetState); tErr != nil {
			fmt.Fprintf(os.Stderr, "warning: ticket transition to %q failed: %v\n", targetState, tErr)
		} else {
			fmt.Fprintf(os.Stderr, "Transitioned %s to %q.\n", tk.Key, targetState)
		}
	}

	var notifyCfg *integrations.NotifyConfig
	if intCfg != nil {
		notifyCfg = intCfg.Notify
	}
	if *notifyFlag || integrations.ShouldNotify(notifyCfg, ep.Status) {
		dispatchNotifications(ctx, notifyCfg, ep, tk.Key, tk.URL, prURL)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"ticket":  tk,
			"episode": ep,
			"pr_url":  prURL,
		})
	}
	fmt.Println()
	fmt.Print(packet.FromEpisode(ep).Markdown())
	fmt.Printf("\nTicket:   %s", tk.Key)
	if tk.URL != "" {
		fmt.Printf(" (%s)", tk.URL)
	}
	fmt.Printf("\nWorktree: %s\n", ep.WorkDir)
	if prURL != "" {
		fmt.Printf("PR:       %s\n", prURL)
	} else if ep.Status == "READY" {
		fmt.Printf("Next step: fabric apply --branch %s %s\n", integrations.BranchSlug(tk), shortID(ep.RunID))
	}
	return nil
}

func resolveTicketPostActions(intCfg *integrations.Config, provider string, commentFlag bool, transitionFlag string) (bool, string) {
	shouldComment := commentFlag
	targetState := strings.TrimSpace(transitionFlag)
	if intCfg == nil {
		return shouldComment, targetState
	}
	switch provider {
	case "jira":
		if intCfg.Jira != nil {
			shouldComment = shouldComment || intCfg.Jira.AutoComment
			if targetState == "" {
				targetState = strings.TrimSpace(intCfg.Jira.ReadyTransition)
			}
		}
	case "linear":
		if intCfg.Linear != nil {
			shouldComment = shouldComment || intCfg.Linear.AutoComment
			if targetState == "" {
				targetState = strings.TrimSpace(intCfg.Linear.ReadyState)
			}
		}
	case "gitlab":
		if intCfg.GitLab != nil {
			shouldComment = shouldComment || intCfg.GitLab.AutoComment
		}
	case "github":
		if intCfg.GitHub != nil {
			shouldComment = shouldComment || intCfg.GitHub.AutoComment
		}
	}
	return shouldComment, targetState
}

func runNotify(args []string) error {
	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	channel := fs.String("channel", "all", "channel filter (all|slack|discord|teams|webhook)")
	ticketKey := fs.String("ticket", "", "optional external ticket key to include in alert")
	ticketURL := fs.String("ticket-url", "", "optional external ticket URL to include in alert")
	prURL := fs.String("pr-url", "", "optional Pull Request URL to include in alert")
	asJSON := fs.Bool("json", false, "output delivery results as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	ep, err := resolveEpisodeArg(absRoot, fs.Args())
	if err != nil {
		return err
	}
	var notifyCfg *integrations.NotifyConfig
	if loaded, cfgErr := agentloop.LoadRepoConfig(absRoot); cfgErr == nil && loaded.Integrations != nil {
		notifyCfg = loaded.Integrations.Notify
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	n := packet.NotificationFromEpisode(ep, *ticketKey, *ticketURL, *prURL)
	results := integrations.NotifyAll(ctx, notifyCfg, n, *channel, nil)
	if len(results) == 0 {
		return errors.New("no matching notification channels configured (set FABRIC_SLACK_WEBHOOK, FABRIC_DISCORD_WEBHOOK, FABRIC_TEAMS_WEBHOOK, or [integrations.notify] in fabric.toml)")
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			return err
		}
	} else {
		for _, r := range results {
			if r.Sent {
				fmt.Printf("[%s] sent (status=%d)\n", r.Channel, r.StatusCode)
			} else {
				fmt.Printf("[%s] FAILED (status=%d): %s\n", r.Channel, r.StatusCode, r.Error)
			}
		}
	}
	for _, r := range results {
		if !r.Sent {
			return fmt.Errorf("notification channel %s failed: %s", r.Channel, r.Error)
		}
	}
	return nil
}

func runIntegrations(args []string) error {
	fs := flag.NewFlagSet("integrations", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	asJSON := fs.Bool("json", false, "output JSON status report")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	var intCfg *integrations.Config
	if loaded, cfgErr := agentloop.LoadRepoConfig(absRoot); cfgErr == nil {
		intCfg = loaded.Integrations
	}
	rep := integrations.InspectStatus(intCfg)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Printf("%-18s %-9s %-13s %-28s %s\n", "NAME", "CATEGORY", "STATUS", "DETAILS", "MISSING ENV")
	for _, item := range rep.Items {
		status := "UNCONFIGURED"
		if item.Configured {
			if item.Ready {
				status = "READY"
			} else {
				status = "MISSING_ENV"
			}
		}
		details := item.Details
		if item.Endpoint != "" {
			if details != "" {
				details += " · " + item.Endpoint
			} else {
				details = item.Endpoint
			}
		}
		if details == "" {
			details = "-"
		}
		if len(details) > 28 {
			details = details[:25] + "..."
		}
		missing := "-"
		if len(item.MissingEnv) > 0 {
			missing = strings.Join(item.MissingEnv, ", ")
		}
		fmt.Printf("%-18s %-9s %-13s %-28s %s\n", item.Name, item.Category, status, details, missing)
	}
	return nil
}

func runSkills(args []string) error {
	fs := flag.NewFlagSet("skills", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	overwrite := fs.Bool("overwrite", false, "overwrite existing .agents/skills/<name>/SKILL.md files during install")
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	sub := "list"
	if len(rest) > 0 && (rest[0] == "list" || rest[0] == "show" || rest[0] == "install") {
		sub = rest[0]
		subFS := flag.NewFlagSet("skills "+sub, flag.ContinueOnError)
		subRoot := subFS.String("root", *root, "repository root")
		subOver := subFS.Bool("overwrite", *overwrite, "overwrite existing files")
		subJSON := subFS.Bool("json", *asJSON, "output JSON")
		if err := subFS.Parse(rest[1:]); err != nil {
			return err
		}
		*root = *subRoot
		*overwrite = *subOver
		*asJSON = *subJSON
		rest = subFS.Args()
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		skills := agentloop.DiscoverSkills(absRoot)
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(skills)
		}
		fmt.Printf("%-34s %-9s %s\n", "NAME", "SOURCE", "DESCRIPTION")
		for _, sk := range skills {
			src := sk.Source
			if src == "" {
				src = "repo"
			}
			desc := sk.Description
			if len(desc) > 68 {
				desc = desc[:65] + "..."
			}
			fmt.Printf("%-34s %-9s %s\n", sk.Name, src, desc)
		}
		return nil
	case "show":
		if len(rest) == 0 {
			return errors.New("usage: fabric skills show [--root DIR] SKILL_NAME")
		}
		skills := agentloop.DiscoverSkills(absRoot)
		body, ok := agentloop.LoadSkillBody(skills, rest[0])
		if !ok {
			return fmt.Errorf("skill %q not found (run `fabric skills list` to see available skills)", rest[0])
		}
		fmt.Print(body)
		if !strings.HasSuffix(body, "\n") {
			fmt.Println()
		}
		return nil
	case "install":
		installed, skipped, err := agentloop.InstallBuiltinSkills(absRoot, rest, *overwrite)
		if err != nil {
			return err
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{
				"root":      absRoot,
				"installed": installed,
				"skipped":   skipped,
			})
		}
		fmt.Printf("Installed %d skill(s) into %s (skipped %d existing).\n", len(installed), filepath.Join(absRoot, ".agents", "skills"), len(skipped))
		for _, n := range installed {
			fmt.Printf("  + %s\n", n)
		}
		for _, n := range skipped {
			fmt.Printf("  = %s (exists; pass --overwrite to replace)\n", n)
		}
		return nil
	default:
		return fmt.Errorf("unknown skills subcommand %q (use list, show, or install)", sub)
	}
}

func runContext(args []string) error {
	fs := flag.NewFlagSet("context", flag.ContinueOnError)
	cwd, _ := os.Getwd()
	root := fs.String("root", cwd, "repository root")
	budget := fs.Int("budget", 3500, "approximate token budget (400..24000)")
	filesCSV := fs.String("files", "", "comma-separated seed file paths to anchor ranking")
	includeDiff := fs.Bool("diff", false, "include current git diff/untracked files as seeds (auto-enabled when query and --files are empty)")
	specOnly := fs.Bool("spec-only", false, "emit Javadoc spec cards and call graph instead of full focal bodies")
	hops := fs.Int("hops", 2, "call-graph hop expansion depth (1..4)")
	noCache := fs.Bool("no-cache", false, "bypass SHA-256 cached graph")
	asJSON := fs.Bool("json", false, "output structured ContextPack JSON instead of Markdown")
	if err := fs.Parse(args); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	var seeds []string
	for _, p := range strings.Split(*filesCSV, ",") {
		if t := strings.TrimSpace(p); t != "" {
			seeds = append(seeds, t)
		}
	}
	useDiff := *includeDiff || (query == "" && len(seeds) == 0)

	discovered := agentloop.DiscoverSkills(absRoot)
	hints := make([]contextcompiler.SkillHint, 0, len(discovered))
	for _, s := range discovered {
		hints = append(hints, contextcompiler.SkillHint{Name: s.Name, Description: s.Description})
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	pack, err := contextcompiler.Compile(ctx, absRoot, contextcompiler.Options{
		Query:        query,
		SeedFiles:    seeds,
		BudgetTokens: *budget,
		MaxHops:      *hops,
		SpecOnly:     *specOnly,
		IncludeDiff:  useDiff,
		UseCache:     !*noCache,
		Skills:       hints,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(pack)
	}
	fmt.Print(pack.Markdown())
	return nil
}

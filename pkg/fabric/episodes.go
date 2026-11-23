package fabric

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/cost"
	"github.com/iancuuandrei/fabric/internal/mutation"
	"github.com/iancuuandrei/fabric/internal/packet"
	"github.com/iancuuandrei/fabric/internal/rewind"
	"github.com/iancuuandrei/fabric/internal/sandbox"
	"github.com/iancuuandrei/fabric/internal/tournament"
)

// AgentOptions configures one programmatic production agentic episode via Client.RunAgent.
type AgentOptions struct {
	Objective       string                `json:"objective"`
	Dialect         string                `json:"dialect,omitempty"`
	BaseURL         string                `json:"base_url,omitempty"`
	APIKeyEnv       string                `json:"api_key_env,omitempty"`
	Model           string                `json:"model,omitempty"`
	ClaudeBin       string                `json:"claude_bin,omitempty"`
	CustomModel     agentloop.Model       `json:"-"`
	ReviewerModel   agentloop.Model       `json:"-"`
	AllowedWrite    []string              `json:"allowed_write,omitempty"`
	MaxUSD          float64               `json:"max_usd,omitempty"`
	MaxTurns        int                   `json:"max_turns,omitempty"`
	KeepRecentTurns int                   `json:"keep_recent_turns,omitempty"`
	MutationAudit   bool                  `json:"mutation_audit,omitempty"`
	AllowCommand    bool                  `json:"allow_command,omitempty"`
	Sandbox         sandbox.Config        `json:"sandbox,omitempty"`
	OnEvent         func(agentloop.Event) `json:"-"`
}

// TournamentOptions configures a parallel isolated-worktree patch tournament.
type TournamentOptions struct {
	Objective     string                 `json:"objective"`
	Models        []string               `json:"models,omitempty"`
	Contenders    []tournament.Contender `json:"-"`
	Dialect       string                 `json:"dialect,omitempty"`
	BaseURL       string                 `json:"base_url,omitempty"`
	APIKeyEnv     string                 `json:"api_key_env,omitempty"`
	AllowedWrite  []string               `json:"allowed_write,omitempty"`
	MaxUSD        float64                `json:"max_usd,omitempty"`
	MaxTurns      int                    `json:"max_turns,omitempty"`
	MutationAudit bool                   `json:"mutation_audit,omitempty"`
	AllowCommand  bool                   `json:"allow_command,omitempty"`
	Sandbox       sandbox.Config         `json:"sandbox,omitempty"`
}

// RunAgent executes a bounded first-principles agentic coding episode in an
// isolated Git worktree using repository policy from fabric.toml.
func (c *Client) RunAgent(ctx context.Context, opts AgentOptions) (agentloop.Episode, error) {
	if strings.TrimSpace(opts.Objective) == "" {
		return agentloop.Episode{}, errors.New("objective is required")
	}
	var repoCfg *config.Config
	if loaded, err := agentloop.LoadRepoConfig(c.Root); err == nil {
		repoCfg = &loaded
	}
	mdl := opts.CustomModel
	if mdl == nil {
		var err error
		mdl, err = agentloop.ModelFromConfig(repoCfg, agentloop.ModelOverrides{
			Dialect:   opts.Dialect,
			BaseURL:   opts.BaseURL,
			APIKeyEnv: opts.APIKeyEnv,
			Model:     opts.Model,
			ClaudeBin: opts.ClaudeBin,
		})
		if err != nil {
			return agentloop.Episode{}, err
		}
	}

	cfg := agentloop.Config{
		RepoRoot:        c.Root,
		Objective:       opts.Objective,
		Model:           mdl,
		ReviewerModel:   opts.ReviewerModel,
		AllowedWrite:    opts.AllowedWrite,
		MaxTurns:        opts.MaxTurns,
		KeepRecentTurns: opts.KeepRecentTurns,
		MutationAudit:   opts.MutationAudit,
		AllowCommand:    opts.AllowCommand,
		Budget:          cost.Budget{MaxUSD: opts.MaxUSD, MaxTurns: opts.MaxTurns},
		Sandbox:         opts.Sandbox,
		OnEvent:         opts.OnEvent,
	}
	if repoCfg != nil {
		cfg.Verification = repoCfg.Verification
		cfg.Hooks = repoCfg.Hooks
		if repoCfg.Budget != nil {
			if cfg.Budget.MaxUSD == 0 {
				cfg.Budget.MaxUSD = repoCfg.Budget.MaxUSD
			}
			if cfg.Budget.MaxTokens == 0 {
				cfg.Budget.MaxTokens = repoCfg.Budget.MaxTokens
			}
			if cfg.Budget.MaxWallSeconds == 0 {
				cfg.Budget.MaxWallSeconds = repoCfg.Budget.MaxWallSeconds
			}
		}
		if cfg.Sandbox.Mode == "" && repoCfg.Sandbox != nil {
			cfg.Sandbox = *repoCfg.Sandbox
		}
	}
	return agentloop.Run(ctx, cfg)
}

// RunTournament executes multiple isolated contenders concurrently and returns
// all candidate episodes along with the Pareto-optimal winner.
func (c *Client) RunTournament(ctx context.Context, opts TournamentOptions) (tournament.Report, error) {
	if strings.TrimSpace(opts.Objective) == "" {
		return tournament.Report{}, errors.New("objective is required")
	}
	var repoCfg *config.Config
	if loaded, err := agentloop.LoadRepoConfig(c.Root); err == nil {
		repoCfg = &loaded
	}
	contenders := opts.Contenders
	if len(contenders) == 0 {
		models := opts.Models
		if len(models) == 0 {
			models = []string{"", ""}
		}
		for i, m := range models {
			mdl, err := agentloop.ModelFromConfig(repoCfg, agentloop.ModelOverrides{
				Dialect:   opts.Dialect,
				BaseURL:   opts.BaseURL,
				APIKeyEnv: opts.APIKeyEnv,
				Model:     m,
			})
			if err != nil {
				return tournament.Report{}, fmt.Errorf("contender %d: %w", i+1, err)
			}
			contenders = append(contenders, tournament.Contender{
				Label: fmt.Sprintf("c%d-%s", i+1, mdl.Name()),
				Model: mdl,
			})
		}
	}
	tCfg := tournament.Config{
		RepoRoot:      c.Root,
		Objective:     opts.Objective,
		Contenders:    contenders,
		AllowedWrite:  opts.AllowedWrite,
		Budget:        cost.Budget{MaxUSD: opts.MaxUSD, MaxTurns: opts.MaxTurns},
		Sandbox:       opts.Sandbox,
		MutationAudit: opts.MutationAudit,
		AllowCommand:  opts.AllowCommand,
	}
	if repoCfg != nil {
		tCfg.Verification = repoCfg.Verification
		tCfg.Hooks = repoCfg.Hooks
		if tCfg.Sandbox.Mode == "" && repoCfg.Sandbox != nil {
			tCfg.Sandbox = *repoCfg.Sandbox
		}
	}
	return tournament.Run(ctx, tCfg)
}

// ListEpisodes returns all persisted agentic episodes sorted newest-first.
func (c *Client) ListEpisodes() ([]agentloop.Episode, error) {
	eps, err := agentloop.ListEpisodes(c.Root)
	if err != nil {
		return nil, err
	}
	sort.Slice(eps, func(i, j int) bool { return eps[i].StartedAt.After(eps[j].StartedAt) })
	return eps, nil
}

// GetEpisode loads one persisted agentic episode by run ID.
func (c *Client) GetEpisode(runID string) (agentloop.Episode, error) {
	return agentloop.LoadEpisode(c.Root, runID)
}

// ReviewPacket constructs a structured evidence-backed ReviewPacket for runID.
func (c *Client) ReviewPacket(runID string) (packet.ReviewPacket, error) {
	ep, err := agentloop.LoadEpisode(c.Root, runID)
	if err != nil {
		return packet.ReviewPacket{}, err
	}
	return packet.FromEpisode(ep), nil
}

// ApplyEpisode safely checks and applies an episode worktree patch onto the
// repository, optionally creating a new Git branch first.
func (c *Client) ApplyEpisode(ctx context.Context, runID, branch string) error {
	ep, err := agentloop.LoadEpisode(c.Root, runID)
	if err != nil {
		return err
	}
	return packet.ApplyWorktreePatch(ctx, c.Root, ep.WorkDir, branch)
}

// RewindEpisode restores an episode's isolated worktree to lightweight Git checkpoint step.
func (c *Client) RewindEpisode(ctx context.Context, runID string, step int) (rewind.Snapshot, error) {
	ep, err := agentloop.LoadEpisode(c.Root, runID)
	if err != nil {
		return rewind.Snapshot{}, err
	}
	return rewind.Restore(ctx, ep.WorkDir, ep.RunID, step)
}

// MutateWorktree runs a bounded diff-scoped mutation audit on workDir.
func (c *Client) MutateWorktree(ctx context.Context, workDir string, testArgv []string, maxMutants int) (mutation.Report, error) {
	if workDir == "" {
		workDir = c.Root
	}
	sb := sandbox.Config{}
	if cfg, err := agentloop.LoadRepoConfig(c.Root); err == nil {
		if len(testArgv) == 0 && len(cfg.Verification) > 0 {
			testArgv = cfg.Verification[0].Argv
		}
		if cfg.Sandbox != nil {
			sb = *cfg.Sandbox
		}
	}
	return mutation.AuditDiff(ctx, mutation.Config{
		WorkDir:    workDir,
		TestArgv:   testArgv,
		Sandbox:    sb,
		MaxMutants: maxMutants,
	})
}

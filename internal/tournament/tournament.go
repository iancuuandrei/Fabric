package tournament

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/cost"
	"github.com/iancuuandrei/fabric/internal/hooks"
	"github.com/iancuuandrei/fabric/internal/sandbox"
)

// Contender defines one candidate trajectory in a cross-model patch tournament.
type Contender struct {
	Label string
	Model agentloop.Model
}

// Config configures a parallel multi-candidate tournament.
type Config struct {
	RepoRoot      string
	Objective     string
	Contenders    []Contender
	ReviewerModel agentloop.Model
	AllowedWrite  []string
	Verification  []config.Check
	Hooks         []hooks.Hook
	Budget        cost.Budget
	Sandbox       sandbox.Config
	MutationAudit bool
	AllowCommand  bool
}

// CandidateResult binds one contender label to its full agentloop Episode.
type CandidateResult struct {
	Label   string            `json:"label"`
	Episode agentloop.Episode `json:"episode"`
	Error   string            `json:"error,omitempty"`
}

// Report contains all contender episodes and the Pareto-selected winner.
type Report struct {
	Objective  string            `json:"objective"`
	Winner     *CandidateResult  `json:"winner,omitempty"`
	Candidates []CandidateResult `json:"candidates"`
	Reason     string            `json:"reason"`
}

// Run executes all contenders concurrently in separate isolated Git worktrees
// and selects the Pareto-optimal winner:
//  1. Status == READY (passed all native checks and review)
//  2. Highest mutation kill score (when mutation auditing is enabled)
//  3. Smallest diff churn (added + removed lines, fewest changed files)
//  4. Lowest estimated USD cost
func Run(ctx context.Context, cfg Config) (Report, error) {
	if len(cfg.Contenders) == 0 {
		return Report{}, errors.New("tournament requires at least one contender")
	}
	results := make([]CandidateResult, len(cfg.Contenders))
	var wg sync.WaitGroup
	for i, c := range cfg.Contenders {
		wg.Add(1)
		go func(idx int, cont Contender) {
			defer wg.Done()
			ep, err := agentloop.Run(ctx, agentloop.Config{
				RepoRoot:      cfg.RepoRoot,
				Objective:     cfg.Objective,
				Model:         cont.Model,
				ReviewerModel: cfg.ReviewerModel,
				AllowedWrite:  cfg.AllowedWrite,
				Verification:  cfg.Verification,
				Hooks:         cfg.Hooks,
				Budget:        cfg.Budget,
				Sandbox:       cfg.Sandbox,
				MutationAudit: cfg.MutationAudit,
				AllowCommand:  cfg.AllowCommand,
			})
			res := CandidateResult{Label: cont.Label, Episode: ep}
			if err != nil {
				res.Error = err.Error()
			}
			results[idx] = res
		}(i, c)
	}
	wg.Wait()

	winner, reason := selectParetoWinner(results)
	return Report{
		Objective:  cfg.Objective,
		Winner:     winner,
		Candidates: results,
		Reason:     reason,
	}, nil
}

func selectParetoWinner(candidates []CandidateResult) (*CandidateResult, string) {
	var ready []CandidateResult
	for _, c := range candidates {
		if c.Error == "" && c.Episode.Status == "READY" {
			ready = append(ready, c)
		}
	}
	if len(ready) == 0 {
		return nil, "no contender reached READY"
	}
	sort.SliceStable(ready, func(i, j int) bool {
		a, b := ready[i].Episode, ready[j].Episode
		// 0. Non-empty patches always outrank accidental no-op trajectories.
		hasA := len(a.ChangedFiles) > 0
		hasB := len(b.ChangedFiles) > 0
		if hasA != hasB {
			return hasA
		}
		// 1. Higher mutation kill score wins.
		var mutA, mutB float64
		if a.Mutation != nil {
			mutA = a.Mutation.Score
		}
		if b.Mutation != nil {
			mutB = b.Mutation.Score
		}
		if mutA != mutB {
			return mutA > mutB
		}
		// 2. Smaller diff churn (minimal blast radius) wins.
		churnA := a.DiffLinesAdded + a.DiffLinesRemoved
		churnB := b.DiffLinesAdded + b.DiffLinesRemoved
		if churnA != churnB {
			return churnA < churnB
		}
		// 3. Fewer changed files wins.
		if len(a.ChangedFiles) != len(b.ChangedFiles) {
			return len(a.ChangedFiles) < len(b.ChangedFiles)
		}
		// 4. Lower estimated USD spend wins.
		return a.CostEstimate.EstimatedUSD < b.CostEstimate.EstimatedUSD
	})
	w := ready[0]
	return &w, "selected Pareto-optimal READY candidate (mutation score -> minimal diff churn -> lowest cost)"
}

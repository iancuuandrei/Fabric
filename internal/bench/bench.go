package bench

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/cost"
	"github.com/iancuuandrei/fabric/internal/sandbox"
)

// Task defines one JSONL benchmark case.
type Task struct {
	ID         string   `json:"id"`
	Objective  string   `json:"objective"`
	BaseCommit string   `json:"base_commit,omitempty"`
	VerifyArgv []string `json:"verify_argv,omitempty"`
	MaxUSD     float64  `json:"max_usd,omitempty"`
	MaxTurns   int      `json:"max_turns,omitempty"`
}

// Config configures one empirical benchmark run.
type Config struct {
	RepoRoot      string
	Model         agentloop.Model
	ReviewerModel agentloop.Model
	Tasks         []Task
	DefaultVerify []config.Check
	Sandbox       sandbox.Config
	MutationAudit bool
	AllowCommand  bool
}

// TaskResult records quantitative metrics for a single benchmark task.
type TaskResult struct {
	TaskID        string   `json:"task_id"`
	Status        string   `json:"status"`
	Passed        bool     `json:"passed"`
	MutationScore *float64 `json:"mutation_score,omitempty"`
	EstimatedUSD  float64  `json:"estimated_usd"`
	CacheHitRate  float64  `json:"cache_hit_rate"`
	DiffChurn     int      `json:"diff_churn"`
	DurationMS    int64    `json:"duration_ms"`
	Error         string   `json:"error,omitempty"`
}

// Summary aggregates metrics across all tasks in a benchmark suite.
type Summary struct {
	Model          string       `json:"model"`
	Total          int          `json:"total"`
	Passed         int          `json:"passed"`
	PassRate       float64      `json:"pass_rate"`
	MeanUSD        float64      `json:"mean_usd"`
	MeanMutation   *float64     `json:"mean_mutation,omitempty"`
	MeanCacheHit   float64      `json:"mean_cache_hit"`
	MeanChurn      float64      `json:"mean_churn"`
	MeanDurationMS int64        `json:"mean_duration_ms"`
	Results        []TaskResult `json:"results"`
}

// LoadSuite parses a JSONL benchmark file (ignoring blank lines and `#` comments).
func LoadSuite(path string) ([]Task, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var tasks []Task
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 32<<10), 2<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		var t Task
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		if t.ID == "" {
			t.ID = fmt.Sprintf("task-%02d", len(tasks)+1)
		}
		if strings.TrimSpace(t.Objective) == "" {
			return nil, fmt.Errorf("%s:%d: objective is required", path, lineNo)
		}
		tasks = append(tasks, t)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(tasks) == 0 {
		return nil, errors.New("benchmark suite contains no tasks")
	}
	return tasks, nil
}

// Run executes all configured benchmark tasks sequentially in isolated worktrees
// and aggregates Pass@1, mutation kill score, cost, cache hit, churn, and speed.
func Run(ctx context.Context, cfg Config) (Summary, error) {
	if cfg.Model == nil || len(cfg.Tasks) == 0 {
		return Summary{}, errors.New("bench requires a Model and at least one Task")
	}
	sum := Summary{
		Model:   cfg.Model.Name(),
		Total:   len(cfg.Tasks),
		Results: make([]TaskResult, 0, len(cfg.Tasks)),
	}

	var totalUSD, totalCache, totalMut float64
	var totalChurn, totalDur int64
	mutCount := 0

	for _, t := range cfg.Tasks {
		checks := cfg.DefaultVerify
		if len(t.VerifyArgv) > 0 {
			checks = []config.Check{{Name: "task-verify", Argv: t.VerifyArgv, TimeoutSeconds: 120}}
		}
		ep, err := agentloop.Run(ctx, agentloop.Config{
			RepoRoot:      cfg.RepoRoot,
			Objective:     t.Objective,
			Model:         cfg.Model,
			ReviewerModel: cfg.ReviewerModel,
			Verification:  checks,
			Sandbox:       cfg.Sandbox,
			Budget:        cost.Budget{MaxUSD: t.MaxUSD, MaxTurns: t.MaxTurns},
			MaxTurns:      t.MaxTurns,
			MutationAudit: cfg.MutationAudit,
			AllowCommand:  cfg.AllowCommand,
		})
		res := TaskResult{
			TaskID:       t.ID,
			Status:       ep.Status,
			Passed:       err == nil && ep.Status == "READY",
			EstimatedUSD: ep.CostEstimate.EstimatedUSD,
			DiffChurn:    ep.DiffLinesAdded + ep.DiffLinesRemoved,
			DurationMS:   ep.DurationMS,
		}
		if ep.Usage.InputTokens > 0 {
			res.CacheHitRate = float64(ep.Usage.CachedTokens) / float64(ep.Usage.InputTokens)
		}
		if ep.Mutation != nil && ep.Mutation.TotalMutants > 0 {
			sc := ep.Mutation.Score
			res.MutationScore = &sc
			totalMut += sc
			mutCount++
		}
		if err != nil {
			res.Error = err.Error()
		} else if !res.Passed {
			res.Error = ep.Reason
		}
		if res.Passed {
			sum.Passed++
		}
		totalUSD += res.EstimatedUSD
		totalCache += res.CacheHitRate
		totalChurn += int64(res.DiffChurn)
		totalDur += res.DurationMS
		sum.Results = append(sum.Results, res)
	}

	n := float64(sum.Total)
	sum.PassRate = float64(sum.Passed) / n
	sum.MeanUSD = totalUSD / n
	sum.MeanCacheHit = totalCache / n
	sum.MeanChurn = float64(totalChurn) / n
	sum.MeanDurationMS = totalDur / int64(sum.Total)
	if mutCount > 0 {
		m := totalMut / float64(mutCount)
		sum.MeanMutation = &m
	}
	return sum, nil
}

// Markdown renders a scannable empirical benchmark scorecard table.
func (s Summary) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Fabric Benchmark Scorecard — `%s`\n\n", s.Model)
	mutSummary := "-"
	if s.MeanMutation != nil {
		mutSummary = fmt.Sprintf("%.1f%%", *s.MeanMutation*100)
	}
	fmt.Fprintf(&b, "- **Pass@1:** `%d/%d` (`%.1f%%`) · **Mean Cost:** `$%.4f` · **Mean Cache Hit:** `%.1f%%` · **Mean Mutation:** `%s` · **Mean Churn:** `%.1f lines`\n\n",
		s.Passed, s.Total, s.PassRate*100, s.MeanUSD, s.MeanCacheHit*100, mutSummary, s.MeanChurn)

	b.WriteString("| Task | Status | Mutation | Churn | Cache Hit | Cost | Duration |\n| --- | --- | ---: | ---: | ---: | ---: | ---: |\n")
	for _, r := range s.Results {
		mut := "-"
		if r.MutationScore != nil {
			mut = fmt.Sprintf("%.0f%%", *r.MutationScore*100)
		}
		fmt.Fprintf(&b, "| `%s` | **%s** | %s | %d | %.0f%% | $%.4f | %.1fs |\n",
			r.TaskID, r.Status, mut, r.DiffChurn, r.CacheHitRate*100, r.EstimatedUSD, float64(r.DurationMS)/1000.0)
	}
	return b.String()
}

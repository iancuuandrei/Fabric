// Package fabric provides a public, embeddable Go SDK over Fabric's
// repository-bound autonomous engineering runtime.
package fabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/iancuuandrei/fabric/internal/cli"
)

// Client operates against one committed Git repository configured for Fabric.
type Client struct {
	Root string
}

// Open returns a Fabric SDK client bound to the repository at root.
func Open(root string) (*Client, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	return &Client{Root: abs}, nil
}

// RunOptions configures an autonomous coding run.
type RunOptions struct {
	Objective          string `json:"objective"`
	Preset             string `json:"preset,omitempty"`
	MaxRepairs         int    `json:"max_repairs,omitempty"`
	MaxParallel        int    `json:"max_parallel,omitempty"`
	RepairIntelligence bool   `json:"repair_intelligence,omitempty"`
	PrepareOnly        bool   `json:"prepare_only,omitempty"`
}

// InitOptions configures repository initialization.
type InitOptions struct {
	Auto       bool   `json:"auto,omitempty"`
	Claude     bool   `json:"claude,omitempty"`
	ClaudeBin  string `json:"claude_bin,omitempty"`
	ACPBin     string `json:"acp_bin,omitempty"`
	ACPArgs    string `json:"acp_args,omitempty"`
	APIDialect string `json:"api,omitempty"`
	APIBaseURL string `json:"base_url,omitempty"`
	APIKeyEnv  string `json:"api_key_env,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
}

func (c *Client) execJSON(ctx context.Context, args ...string) (json.RawMessage, error) {
	var out bytes.Buffer
	full := append([]string{"--root", c.Root}, args...)
	if err := cli.Execute(ctx, full, c.Root, &out); err != nil {
		return nil, err
	}
	raw := bytes.TrimSpace(out.Bytes())
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(raw), nil
}

// Init initializes Fabric configuration in the repository.
func (c *Client) Init(ctx context.Context, opts InitOptions) (json.RawMessage, error) {
	args := []string{"init"}
	if opts.Auto {
		args = append(args, "--auto")
	}
	if opts.Claude {
		args = append(args, "--claude")
	}
	if opts.ClaudeBin != "" {
		args = append(args, "--claude-bin", opts.ClaudeBin)
	}
	if opts.ACPBin != "" {
		args = append(args, "--acp", opts.ACPBin)
	}
	if opts.ACPArgs != "" {
		args = append(args, "--acp-args", opts.ACPArgs)
	}
	if opts.APIDialect != "" {
		args = append(args, "--api", opts.APIDialect)
	}
	if opts.APIBaseURL != "" {
		args = append(args, "--base-url", opts.APIBaseURL)
	}
	if opts.APIKeyEnv != "" {
		args = append(args, "--api-key-env", opts.APIKeyEnv)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	return c.execJSON(ctx, args...)
}

// Doctor validates repository configuration, runtimes, and verification readiness.
func (c *Client) Doctor(ctx context.Context) (json.RawMessage, error) {
	return c.execJSON(ctx, "doctor")
}

// Status lists validated local runs in this repository.
func (c *Client) Status(ctx context.Context) (json.RawMessage, error) {
	return c.execJSON(ctx, "status")
}

// Inspect replays and returns the full validated snapshot for one run.
func (c *Client) Inspect(ctx context.Context, runID string) (json.RawMessage, error) {
	if runID == "" {
		return nil, errors.New("run_id is required")
	}
	return c.execJSON(ctx, "inspect", runID)
}

// ExportEvents returns the canonical JSONL event stream for one run.
func (c *Client) ExportEvents(ctx context.Context, runID string) ([]byte, error) {
	if runID == "" {
		return nil, errors.New("run_id is required")
	}
	var out bytes.Buffer
	if err := cli.Execute(ctx, []string{"--root", c.Root, "inspect", runID, "--export-jsonl"}, c.Root, &out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Diff returns the isolated candidate diff for one run.
func (c *Client) Diff(ctx context.Context, runID string) (json.RawMessage, error) {
	if runID == "" {
		return c.execJSON(ctx, "diff")
	}
	return c.execJSON(ctx, "diff", runID)
}

// Usage returns replay-validated token usage for one run.
func (c *Client) Usage(ctx context.Context, runID string) (json.RawMessage, error) {
	if runID == "" {
		return nil, errors.New("run_id is required")
	}
	return c.execJSON(ctx, "usage", runID)
}

// Checkpoint returns the payload-free checkpoint summary for one run.
func (c *Client) Checkpoint(ctx context.Context, runID string) (json.RawMessage, error) {
	if runID == "" {
		return nil, errors.New("run_id is required")
	}
	return c.execJSON(ctx, "checkpoint", runID)
}

// Diagnose projects structured repair findings and admitted scope for one run.
func (c *Client) Diagnose(ctx context.Context, runID string) (json.RawMessage, error) {
	if runID == "" {
		return nil, errors.New("run_id is required")
	}
	return c.execJSON(ctx, "diagnose", runID)
}

// RunAutonomous creates and executes a bounded autonomous coding run.
func (c *Client) RunAutonomous(ctx context.Context, opts RunOptions) (json.RawMessage, error) {
	if opts.Objective == "" {
		return nil, errors.New("objective is required")
	}
	args := []string{"run", "--autonomous"}
	switch opts.Preset {
	case "", "standard":
	case "fast":
		if opts.MaxRepairs == 0 {
			opts.MaxRepairs = 1
		}
		if opts.MaxParallel == 0 {
			opts.MaxParallel = 1
		}
	case "thorough":
		opts.RepairIntelligence = true
		if opts.MaxRepairs == 0 {
			opts.MaxRepairs = 3
		}
	default:
		return nil, fmt.Errorf("unknown preset %q (use standard, fast, or thorough)", opts.Preset)
	}
	if opts.MaxRepairs > 0 {
		args = append(args, "--max-repairs", strconv.Itoa(opts.MaxRepairs))
	}
	if opts.MaxParallel > 0 {
		args = append(args, "--max-parallel", strconv.Itoa(opts.MaxParallel))
	}
	if opts.RepairIntelligence {
		args = append(args, "--repair-intelligence")
	}
	if opts.PrepareOnly {
		args = append(args, "--prepare-only")
	}
	args = append(args, opts.Objective)
	return c.execJSON(ctx, args...)
}

// Approve records explicit human approval for one exact plan.
func (c *Client) Approve(ctx context.Context, runID, planID, actor string) (json.RawMessage, error) {
	return c.execJSON(ctx, "approve", runID, planID, actor)
}

// Pause requests a durable stop of new dispatch after admitted work settles.
func (c *Client) Pause(ctx context.Context, runID, actor, nonce string) (json.RawMessage, error) {
	return c.execJSON(ctx, "pause", runID, actor, nonce)
}

// Resume continues an existing run.
func (c *Client) Resume(ctx context.Context, runID string, autonomous bool) (json.RawMessage, error) {
	if autonomous {
		return c.execJSON(ctx, "resume", "--autonomous", runID)
	}
	return c.execJSON(ctx, "resume", runID)
}

// Cancel requests durable cancellation without discarding unresolved effects.
func (c *Client) Cancel(ctx context.Context, runID, actor, nonce string) (json.RawMessage, error) {
	return c.execJSON(ctx, "cancel", runID, actor, nonce)
}

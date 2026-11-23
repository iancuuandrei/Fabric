// Package clauderuntime executes bounded, read-only Claude Code CLI turns
// under Fabric's durable journal and identity boundary.
package clauderuntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/iancuuandrei/fabric/internal/journal"
	"github.com/iancuuandrei/fabric/internal/runtime"
	"github.com/iancuuandrei/fabric/internal/runtimeutil"
)

const (
	// RuntimeName identifies the Claude Code CLI runtime in Fabric configuration.
	RuntimeName = "claude-cli"

	defaultTimeout = 10 * time.Minute
	maxOutputBytes = 4 << 20
)

// Config binds one pinned Claude Code CLI execution.
type Config struct {
	Executable     string
	ExecutableHash string
	WorkDir        string
	JournalPath    string
	Timeout        time.Duration
	ExtraArgs      []string
}

// Intent records an admitted Claude CLI invocation before subprocess launch.
type Intent struct {
	Version        int             `json:"version"`
	InvocationID   string          `json:"invocation_id"`
	Executable     string          `json:"executable"`
	ExecutableHash string          `json:"executable_hash"`
	Profile        runtime.Profile `json:"profile"`
	InputHash      string          `json:"input_hash"`
}

// Receipt records the completed Claude CLI result bound to the intent.
type Receipt struct {
	Version      int            `json:"version"`
	InvocationID string         `json:"invocation_id"`
	SessionID    string         `json:"session_id,omitempty"`
	ResultHash   string         `json:"result_hash"`
	Result       runtime.Result `json:"result"`
}

// State is the replayed durable state of one Claude runtime journal.
type State struct {
	Intent  *Intent
	Receipt *Receipt
	Head    string
}

// Inspect replays a Claude runtime journal without launching any subprocess.
func Inspect(path string) (State, error) {
	in, rec, head, err := runtimeutil.InspectJournal[Intent, Receipt](path, "claude.intent", "claude.result")
	if err != nil {
		return State{}, err
	}
	return State{Intent: in, Receipt: rec, Head: head}, nil
}

// DiscoverExecutable locates an installed Claude Code CLI executable on PATH or
// common user installation paths and returns its absolute path and SHA-256.
func DiscoverExecutable() (string, string, error) {
	candidates := []string{}
	if p, err := exec.LookPath("claude"); err == nil {
		candidates = append(candidates, p)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, ".local", "bin", "claude.exe"),
			filepath.Join(home, ".local", "bin", "claude"),
		)
	}
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		info, err := os.Lstat(abs)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		hash, err := HashFile(abs)
		if err == nil {
			return abs, hash, nil
		}
	}
	return "", "", errors.New("claude executable not found on PATH or ~/.local/bin")
}

// HashFile returns the lowercase SHA-256 hex digest of a regular file (up to 512 MiB).
func HashFile(path string) (string, error) {
	return runtimeutil.HashFile(path)
}

func validateEvents(events []journal.Event) error {
	return runtimeutil.ValidateTwoEventJournal(
		events,
		"claude",
		"claude.intent",
		"claude.result",
		func(in Intent) string { return in.InvocationID },
		func(rec Receipt) string { return rec.InvocationID },
	)
}

// Execute runs or deterministically recovers one bounded Claude Code CLI turn.
// If an intent already exists without a receipt, Execute refuses to resend and
// returns an unresolved error.
func Execute(ctx context.Context, cfg Config, inv runtime.Invocation) (runtime.Result, string, error) {
	if err := inv.Validate(); err != nil {
		return runtime.Result{}, "", err
	}
	if inv.Profile.Runtime != RuntimeName {
		return runtime.Result{}, "", fmt.Errorf("claude-cli runtime required, got %q", inv.Profile.Runtime)
	}
	if cfg.Executable == "" || cfg.JournalPath == "" {
		return runtime.Result{}, "", errors.New("claude-cli executable and journal path are required")
	}
	if err := runtimeutil.VerifyExecutableHash(cfg.Executable, cfg.ExecutableHash, "claude"); err != nil {
		return runtime.Result{}, "", err
	}

	wantIntent := Intent{
		Version:        1,
		InvocationID:   inv.ID,
		Executable:     cfg.Executable,
		ExecutableHash: cfg.ExecutableHash,
		Profile:        inv.Profile,
		InputHash:      runtimeutil.InputSHA256(inv.Input),
	}

	cached, head, err := runtimeutil.PrepareInvocation(
		cfg.JournalPath,
		"claude runtime",
		"claude.intent",
		"claude.result",
		wantIntent,
		inv,
		validateEvents,
		func(rec Receipt) runtime.Result { return rec.Result },
	)
	if err != nil {
		return runtime.Result{}, "", err
	}
	if cached != nil {
		return *cached, head, nil
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := buildArgs(inv.Profile, cfg.ExtraArgs)
	cmd := exec.CommandContext(runCtx, cfg.Executable, args...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	cmd.Stdin = strings.NewReader(inv.Input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedBuffer{buf: &stdout, limit: maxOutputBytes}
	cmd.Stderr = &limitedBuffer{buf: &stderr, limit: 64 << 10}

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return runtime.Result{}, "", fmt.Errorf("claude-cli execution failed: %s", runtimeutil.Truncate(msg, 512))
	}

	res, sessionID, err := parseClaudeOutput(inv, stdout.Bytes())
	if err != nil {
		return runtime.Result{}, "", err
	}
	return runtimeutil.RecordResult(
		cfg.JournalPath,
		"claude.result",
		"harness.claude-result.v1",
		inv,
		res,
		validateEvents,
		func(resHash string) Receipt {
			return Receipt{
				Version:      1,
				InvocationID: inv.ID,
				SessionID:    sessionID,
				ResultHash:   resHash,
				Result:       res,
			}
		},
	)
}

func buildArgs(profile runtime.Profile, extra []string) []string {
	args := []string{
		"-p",
		"--output-format", "json",
		"--no-session-persistence",
		"--permission-mode", "plan",
		"--tools", "",
		"--model", profile.Model,
	}
	switch strings.ToLower(strings.TrimSpace(profile.Effort)) {
	case "low", "medium", "high", "max":
		args = append(args, "--effort", strings.ToLower(strings.TrimSpace(profile.Effort)))
	}
	args = append(args, extra...)
	return args
}

type claudeJSONEnvelope struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	Usage     *struct {
		InputTokens              int64 `json:"input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
	} `json:"usage"`
}

func parseClaudeOutput(inv runtime.Invocation, raw []byte) (runtime.Result, string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return runtime.Result{}, "", errors.New("claude-cli returned empty output")
	}

	var env claudeJSONEnvelope
	if err := json.Unmarshal(trimmed, &env); err == nil && (env.Type == "result" || env.Result != "" || env.IsError) {
		if env.IsError {
			return runtime.Result{}, env.SessionID, fmt.Errorf("claude-cli reported error: %s", runtimeutil.Truncate(env.Result, 512))
		}
		outputText := runtimeutil.UnwrapCodeFence(env.Result)
		if strings.TrimSpace(outputText) == "" {
			return runtime.Result{}, env.SessionID, errors.New("claude-cli returned empty result text")
		}
		var usage runtime.Usage
		if env.Usage != nil {
			inTok := env.Usage.InputTokens + env.Usage.CacheCreationInputTokens + env.Usage.CacheReadInputTokens
			outTok := env.Usage.OutputTokens
			usage.InputTokens = &inTok
			usage.OutputTokens = &outTok
		}
		return runtimeutil.BuildResult(inv, outputText, usage), env.SessionID, nil
	}

	outputText := runtimeutil.UnwrapCodeFence(string(trimmed))
	if strings.TrimSpace(outputText) == "" {
		return runtime.Result{}, "", errors.New("claude-cli returned unreadable output")
	}
	return runtimeutil.BuildResult(inv, outputText, runtime.Usage{}), "", nil
}

type limitedBuffer struct {
	buf   *bytes.Buffer
	limit int
}

// Write appends bytes up to the configured buffer limit.
func (l *limitedBuffer) Write(p []byte) (int, error) {
	rem := l.limit - l.buf.Len()
	if rem <= 0 {
		return len(p), nil
	}
	if len(p) > rem {
		l.buf.Write(p[:rem])
		return len(p), nil
	}
	return l.buf.Write(p)
}

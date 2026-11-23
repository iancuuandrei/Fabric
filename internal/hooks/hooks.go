package hooks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	// EventPostWrite runs after candidate file edits are applied and before verification.
	EventPostWrite = "post_write"
	// EventPreVerify runs immediately before configured verification checks.
	EventPreVerify = "pre_verify"
	// EventPreCommit runs before committing a verified candidate.
	EventPreCommit = "pre_commit"
	// EventOnReady runs after a run reaches READY.
	EventOnReady = "on_ready"
	// EventOnBlocked runs when a run stops in a non-READY state.
	EventOnBlocked = "on_blocked"

	// FailureBlock stops the lifecycle transition when the hook exits non-zero.
	FailureBlock = "block"
	// FailureWarn records the failure but allows the run to proceed.
	FailureWarn = "warn"

	defaultTimeoutSeconds = 60
	maxTimeoutSeconds     = 600
	maxOutputBytes        = 64 << 10
)

// Hook declares one deterministic lifecycle command. Argv is executed directly
// without a shell to prevent command injection.
type Hook struct {
	Name           string   `toml:"name" json:"name"`
	Event          string   `toml:"event" json:"event"`
	Argv           []string `toml:"argv" json:"argv"`
	TimeoutSeconds int      `toml:"timeout_seconds" json:"timeout_seconds,omitempty"`
	OnFailure      string   `toml:"on_failure" json:"on_failure,omitempty"`
}

// Validate checks hook identity, lifecycle event, argv bounds, and failure policy.
func (h Hook) Validate() error {
	if strings.TrimSpace(h.Name) == "" || len(h.Name) > 128 {
		return errors.New("hook name must be 1..128 characters")
	}
	switch h.Event {
	case EventPostWrite, EventPreVerify, EventPreCommit, EventOnReady, EventOnBlocked:
	default:
		return fmt.Errorf("unsupported hook event %q", h.Event)
	}
	if len(h.Argv) == 0 || len(h.Argv) > 64 || strings.TrimSpace(h.Argv[0]) == "" {
		return errors.New("hook argv must contain a non-empty executable")
	}
	for _, arg := range h.Argv {
		if strings.ContainsRune(arg, 0) || len(arg) > 4096 {
			return errors.New("invalid hook argument")
		}
	}
	if h.TimeoutSeconds < 0 || h.TimeoutSeconds > maxTimeoutSeconds {
		return fmt.Errorf("hook timeout_seconds must be 0..%d", maxTimeoutSeconds)
	}
	switch h.OnFailure {
	case "", FailureBlock, FailureWarn:
	default:
		return fmt.Errorf("hook on_failure must be %q or %q", FailureBlock, FailureWarn)
	}
	return nil
}

// Result captures the deterministic execution outcome of one hook.
type Result struct {
	Name       string `json:"name"`
	Event      string `json:"event"`
	Passed     bool   `json:"passed"`
	Blocked    bool   `json:"blocked"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output,omitempty"`
}

// RunEvent executes all hooks matching event in declaration order inside workDir.
// If a blocking hook fails, RunEvent stops immediately and returns an error along
// with all results collected so far.
func RunEvent(ctx context.Context, hooks []Hook, event, workDir string, env []string) ([]Result, error) {
	var results []Result
	for _, h := range hooks {
		if h.Event != event {
			continue
		}
		if err := h.Validate(); err != nil {
			return results, err
		}
		res := executeOne(ctx, h, workDir, env)
		results = append(results, res)
		if res.Blocked {
			return results, fmt.Errorf("hook %q (%s) blocked execution (exit %d): %s", h.Name, h.Event, res.ExitCode, strings.TrimSpace(res.Output))
		}
	}
	return results, nil
}

func executeOne(ctx context.Context, h Hook, workDir string, env []string) Result {
	timeoutSec := h.TimeoutSeconds
	if timeoutSec <= 0 {
		timeoutSec = defaultTimeoutSeconds
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, h.Argv[0], h.Argv[1:]...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if len(env) > 0 {
		cmd.Env = env
	}
	var out limitedBuf
	out.limit = maxOutputBytes
	cmd.Stdout = &out
	cmd.Stderr = &out

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start).Milliseconds()

	res := Result{
		Name:       h.Name,
		Event:      h.Event,
		Passed:     err == nil,
		DurationMS: dur,
		Output:     out.buf.String(),
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
			if res.Output == "" {
				res.Output = err.Error()
			}
		}
		if h.OnFailure != FailureWarn {
			res.Blocked = true
		}
	}
	return res
}

type limitedBuf struct {
	buf   bytes.Buffer
	limit int
}

// Write appends up to limit bytes.
func (l *limitedBuf) Write(p []byte) (int, error) {
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

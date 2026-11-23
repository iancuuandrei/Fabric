package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ModeLocal executes argv directly inside the isolated Git worktree.
	ModeLocal = "local"
	// ModeDocker executes argv inside an ephemeral Docker container with --network=none.
	ModeDocker = "docker"
	// ModePodman executes argv inside an ephemeral Podman container with --network=none.
	ModePodman = "podman"

	defaultTimeoutSeconds = 120
	maxOutputBytes        = 128 << 10
)

// validImageRe enforces an OCI image reference that never starts with '-'
// (blocking Docker/Podman CLI flag injection via untrusted fabric.toml).
var validImageRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:@/-]{0,255}$`)

// Config declares the opt-in execution isolation backend in fabric.toml.
type Config struct {
	Mode           string `toml:"mode" json:"mode,omitempty"`
	Image          string `toml:"image" json:"image,omitempty"`
	AllowNetwork   bool   `toml:"allow_network" json:"allow_network,omitempty"`
	InheritSecrets bool   `toml:"inherit_secrets" json:"inherit_secrets,omitempty"`
	MemoryMB       int    `toml:"memory_mb" json:"memory_mb,omitempty"`
	CPUs           string `toml:"cpus" json:"cpus,omitempty"`
}

// Validate checks sandbox mode, OCI image safety, memory bounds, and CPU syntax.
func (c Config) Validate() error {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	switch mode {
	case "", ModeLocal:
		return nil
	case ModeDocker, ModePodman:
		img := strings.TrimSpace(c.Image)
		if !validImageRe.MatchString(img) {
			return fmt.Errorf("sandbox mode %q requires a valid OCI container image (got %q)", mode, c.Image)
		}
		if c.MemoryMB < 0 || c.MemoryMB > 131072 {
			return errors.New("sandbox memory_mb out of bounds (0..131072)")
		}
		if c.MemoryMB > 0 && c.MemoryMB < 64 {
			return errors.New("sandbox memory_mb must be at least 64 when set")
		}
		if cpusStr := strings.TrimSpace(c.CPUs); cpusStr != "" {
			cpus, err := strconv.ParseFloat(cpusStr, 64)
			if err != nil || math.IsNaN(cpus) || math.IsInf(cpus, 0) || cpus <= 0 || cpus > 64 {
				return fmt.Errorf("invalid sandbox cpus %q (must be 0 < cpus <= 64)", c.CPUs)
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported sandbox mode %q (use local, docker, or podman)", c.Mode)
	}
}

// ExecResult records bounded command execution output and timing.
type ExecResult struct {
	ExitCode   int    `json:"exit_code"`
	Passed     bool   `json:"passed"`
	DurationMS int64  `json:"duration_ms"`
	Output     string `json:"output"`
	Truncated  bool   `json:"truncated,omitempty"`
	Mode       string `json:"mode"`
}

// scrubSensitiveEnv removes API keys, tokens, cloud credentials, and SSH/Git
// askpass environment variables while setting GIT_TERMINAL_PROMPT=0.
func scrubSensitiveEnv(env []string) []string {
	cleaned := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(strings.TrimSpace(key))
		if isSensitiveEnvKey(upper) || upper == "GIT_TERMINAL_PROMPT" {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	cleaned = append(cleaned, "GIT_TERMINAL_PROMPT=0")
	return cleaned
}

func isSensitiveEnvKey(upper string) bool {
	if strings.Contains(upper, "API_KEY") ||
		strings.Contains(upper, "_TOKEN") ||
		strings.Contains(upper, "_SECRET") ||
		strings.Contains(upper, "PASSWORD") ||
		strings.Contains(upper, "PRIVATE_KEY") ||
		strings.Contains(upper, "CREDENTIAL") {
		return true
	}
	for _, prefix := range []string{
		"AWS_",
		"AZURE_",
		"GCP_",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"SSH_AUTH_SOCK",
		"GIT_ASKPASS",
		"FABRIC_API_TOKEN",
	} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// BuildCommand constructs the exact exec.Cmd argv for a sandboxed command
// without invoking any shell. Exported for deterministic verification.
func BuildCommand(ctx context.Context, cfg Config, workDir string, argv []string) (*exec.Cmd, string, error) {
	cmd, mode, _, err := buildCommandNamed(ctx, cfg, workDir, argv)
	return cmd, mode, err
}

func buildCommandNamed(ctx context.Context, cfg Config, workDir string, argv []string) (*exec.Cmd, string, string, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return nil, "", "", errors.New("command argv must not be empty")
	}
	if err := cfg.Validate(); err != nil {
		return nil, "", "", err
	}
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" || mode == ModeLocal {
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = workDir
		configureProcessGroup(cmd)
		if !cfg.InheritSecrets {
			cmd.Env = scrubSensitiveEnv(os.Environ())
		}
		return cmd, ModeLocal, "", nil
	}

	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, "", "", err
	}
	if resolved, err := filepath.EvalSymlinks(absDir); err == nil {
		absDir = resolved
	}
	if strings.ContainsAny(absDir, ",\n\r\x00") {
		return nil, "", "", errors.New("workspace path contains characters unsafe for container bind mount")
	}

	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	containerName := "fabric-sb-" + hex.EncodeToString(rnd[:])

	containerArgs := []string{
		"run", "--rm", "--init",
		"--name", containerName,
		"--cap-drop=ALL",
		"--security-opt=no-new-privileges:true",
		"--pids-limit=256",
		"--mount", "type=bind,source=" + absDir + ",target=/workspace",
		"-w", "/workspace",
	}
	if !cfg.AllowNetwork {
		containerArgs = append(containerArgs, "--network=none")
	}
	if cfg.MemoryMB > 0 {
		mem := fmt.Sprintf("%dm", cfg.MemoryMB)
		containerArgs = append(containerArgs, "--memory="+mem, "--memory-swap="+mem)
	}
	if cpus := strings.TrimSpace(cfg.CPUs); cpus != "" {
		containerArgs = append(containerArgs, "--cpus="+cpus)
	}
	containerArgs = append(containerArgs, strings.TrimSpace(cfg.Image))
	containerArgs = append(containerArgs, argv...)
	cmd := exec.CommandContext(ctx, mode, containerArgs...)
	if !cfg.InheritSecrets {
		cmd.Env = scrubSensitiveEnv(os.Environ())
	}
	return cmd, mode, containerName, nil
}

// Run executes argv inside workDir under cfg with bounded output, secret
// scrubbing, timeout, and full process-tree / container teardown on cancel.
func Run(ctx context.Context, cfg Config, workDir string, argv []string, timeout time.Duration) (ExecResult, error) {
	if timeout <= 0 {
		timeout = defaultTimeoutSeconds * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd, mode, containerName, err := buildCommandNamed(runCtx, cfg, workDir, argv)
	if err != nil {
		return ExecResult{}, err
	}
	var out headTailBuffer
	out.limit = maxOutputBytes
	cmd.Stdout = &out
	cmd.Stderr = &out

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return ExecResult{
			ExitCode:   -1,
			Passed:     false,
			DurationMS: time.Since(start).Milliseconds(),
			Output:     err.Error(),
			Mode:       mode,
		}, nil
	}

	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	var runErr error
	select {
	case runErr = <-waitDone:
	case <-runCtx.Done():
		removeContainer(ctx, mode, containerName)
		if mode == ModeLocal && pid > 0 {
			killProcessTree(pid)
		}
		runErr = <-waitDone
		if runErr == nil {
			runErr = runCtx.Err()
		}
	}
	dur := time.Since(start).Milliseconds()

	if containerName != "" && runErr != nil && runCtx.Err() != nil {
		removeContainer(ctx, mode, containerName)
	}

	res := ExecResult{
		Passed:     runErr == nil,
		DurationMS: dur,
		Output:     out.String(),
		Truncated:  out.isTruncated(),
		Mode:       mode,
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		} else {
			res.ExitCode = -1
			if res.Output == "" {
				res.Output = runErr.Error()
			}
		}
	}
	return res, nil
}

// headTailBuffer keeps the beginning and end of noisy compiler/test output so
// neither the initial error nor final summary is lost to naive truncation.
type headTailBuffer struct {
	mu        sync.Mutex
	head      bytes.Buffer
	tail      bytes.Buffer
	limit     int
	truncated bool
}

// Write appends p while bounding memory to limit bytes (half head, half tail).
func (b *headTailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	origLen := len(p)
	half := b.limit / 2
	if half < 1024 {
		half = 1024
	}
	if !b.truncated && b.head.Len()+len(p) <= half {
		b.head.Write(p)
		return origLen, nil
	}
	if !b.truncated {
		rem := half - b.head.Len()
		if rem > 0 {
			b.head.Write(p[:rem])
			p = p[rem:]
		}
		b.truncated = true
	}
	if len(p) >= half {
		b.tail.Reset()
		b.tail.Write(p[len(p)-half:])
		return origLen, nil
	}
	b.tail.Write(p)
	if b.tail.Len() > half {
		b.tail.Next(b.tail.Len() - half)
	}
	return origLen, nil
}

// String renders the bounded buffer with an explicit truncation marker if needed.
func (b *headTailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.truncated {
		return b.head.String()
	}
	return b.head.String() + "\n... [output truncated to preserve head and tail] ...\n" + b.tail.String()
}

func (b *headTailBuffer) isTruncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}

func removeContainer(ctx context.Context, mode, containerName string) {
	if containerName == "" {
		return
	}
	cleanCtx, cleanCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cleanCancel()
	_ = exec.CommandContext(cleanCtx, mode, "rm", "-f", containerName).Run()
}

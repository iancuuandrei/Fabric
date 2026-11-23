package sandbox

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSandboxLocalRunAndDockerCommandBuild(t *testing.T) {
	res, err := Run(context.Background(), Config{Mode: ModeLocal}, t.TempDir(), []string{"git", "--version"}, 10*time.Second)
	if err != nil || !res.Passed || !strings.Contains(strings.ToLower(res.Output), "git version") {
		t.Fatalf("local sandbox run failed: res=%+v err=%v", res, err)
	}

	cmd, mode, err := BuildCommand(context.Background(), Config{
		Mode:     ModeDocker,
		Image:    "golang:1.27",
		MemoryMB: 2048,
		CPUs:     "2",
	}, t.TempDir(), []string{"go", "test", "./..."})
	if err != nil || mode != ModeDocker {
		t.Fatalf("docker build failed: mode=%s err=%v", mode, err)
	}
	if !slices.Contains(cmd.Args, "--network=none") ||
		!slices.Contains(cmd.Args, "--cap-drop=ALL") ||
		!slices.Contains(cmd.Args, "--security-opt=no-new-privileges:true") ||
		!slices.Contains(cmd.Args, "--memory=2048m") ||
		!slices.Contains(cmd.Args, "--memory-swap=2048m") ||
		!slices.Contains(cmd.Args, "--mount") ||
		!slices.Contains(cmd.Args, "golang:1.27") {
		t.Fatalf("unexpected docker args: %v", cmd.Args)
	}
	if err := (Config{Mode: ModeDocker}).Validate(); err == nil {
		t.Fatal("expected error for docker mode without image")
	}
	if err := (Config{Mode: ModeDocker, Image: "--privileged"}).Validate(); err == nil {
		t.Fatal("expected error for CLI flag-like image name")
	}
	if err := (Config{Mode: ModeDocker, Image: "golang:1.27", CPUs: "-1"}).Validate(); err == nil {
		t.Fatal("expected error for negative cpus")
	}
}

func TestScrubSensitiveEnvRemovesSecretsAndPreservesBasics(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/dev",
		"GOPATH=/go",
		"TEMP=/tmp",
		"ANTHROPIC_API_KEY=sk-ant-secret",
		"OPENAI_API_KEY=sk-openai-secret",
		"GITHUB_TOKEN=ghp_secret",
		"AWS_SECRET_ACCESS_KEY=aws-secret",
		"AZURE_CLIENT_SECRET=az-secret",
		"MY_APP_PASSWORD=hunter2",
		"SSH_AUTH_SOCK=/tmp/ssh.sock",
		"GIT_ASKPASS=/bin/true",
		"FABRIC_API_TOKEN=tok",
	}
	out := scrubSensitiveEnv(in)
	joined := strings.Join(out, "\n")
	for _, keep := range []string{"PATH=/usr/bin", "HOME=/home/dev", "GOPATH=/go", "TEMP=/tmp", "GIT_TERMINAL_PROMPT=0"} {
		if !slices.Contains(out, keep) {
			t.Fatalf("expected %q preserved, got: %v", keep, out)
		}
	}
	for _, secret := range []string{
		"sk-ant-secret", "sk-openai-secret", "ghp_secret",
		"aws-secret", "az-secret", "hunter2", "/tmp/ssh.sock", "GIT_ASKPASS", "FABRIC_API_TOKEN",
	} {
		if strings.Contains(joined, secret) {
			t.Fatalf("secret %q leaked into scrubbed env: %s", secret, joined)
		}
	}
}

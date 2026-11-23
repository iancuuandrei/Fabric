package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/cost"
	"github.com/iancuuandrei/fabric/internal/rewind"
)

func TestAgentSubcommandsPacketRewindAndApply(t *testing.T) {
	repo := t.TempDir()
	runGit := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit(repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(repo, "add", ".")
	runGit(repo, "-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "init")

	wt := filepath.Join(t.TempDir(), "wt")
	runGit(repo, "worktree", "add", "-b", "wt-cmd-test", wt, "HEAD")

	epID := "cmdtest0123456789"
	snap0, err := rewind.Create(context.Background(), wt, epID, 0, "base")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(wt, "a.txt"), []byte("v2\n"), 0o600)
	snap1, err := rewind.Create(context.Background(), wt, epID, 1, "edit")
	if err != nil {
		t.Fatal(err)
	}

	ep := agentloop.Episode{
		RunID:        epID,
		Objective:    "Bump version",
		Model:        "claude-sonnet-4-5",
		Status:       "READY",
		WorkDir:      wt,
		Checkpoints:  []rewind.Snapshot{snap0, snap1},
		Usage:        cost.Usage{InputTokens: 100, OutputTokens: 20},
		CostEstimate: cost.Estimate{KnownPricing: true, EstimatedUSD: 0.001},
		StartedAt:    time.Now().UTC(),
		EndedAt:      time.Now().UTC(),
	}
	if err := agentloop.SaveEpisode(repo, ep); err != nil {
		t.Fatal(err)
	}

	if err := runPacket([]string{"--root", repo, epID}); err != nil {
		t.Fatalf("runPacket failed: %v", err)
	}
	if err := runRewind([]string{"--root", repo, "--list", epID}); err != nil {
		t.Fatalf("runRewind --list failed: %v", err)
	}
	if err := runApply([]string{"--root", repo, "--branch", "applied-cli", epID}); err != nil {
		t.Fatalf("runApply failed: %v", err)
	}
	if err := runRewind([]string{"--root", repo, "--to", "0", epID}); err != nil {
		t.Fatalf("runRewind --to 0 failed: %v", err)
	}
}

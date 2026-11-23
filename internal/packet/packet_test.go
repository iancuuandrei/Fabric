package packet

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/agentloop"
	"github.com/iancuuandrei/fabric/internal/cost"
)

func TestReviewPacketMarkdownAndSafeApply(t *testing.T) {
	ep := agentloop.Episode{
		RunID:            "abcdef0123456789",
		Status:           "READY",
		Objective:        "Fix boundary check",
		Model:            "claude-sonnet-4-5",
		Summary:          "Changed >= to >",
		ChangedFiles:     []string{"math.go"},
		DiffLinesAdded:   1,
		DiffLinesRemoved: 1,
		Verification:     []agentloop.VerificationReceipt{{Name: "unit", Passed: true, ExitCode: 0, DurationMS: 120}},
		ReviewVerdict:    "approve",
		ReviewNotes:      "Clean fix",
		Usage:            cost.Usage{InputTokens: 1000, CachedTokens: 500, OutputTokens: 100},
		CostEstimate:     cost.Estimate{KnownPricing: true, EstimatedUSD: 0.0031},
		MaskedBytesSaved: 4096,
		DurationMS:       2500,
	}
	md := FromEpisode(ep).Markdown()
	if !strings.Contains(md, "Fabric Review Packet") || !strings.Contains(md, "PASS") || !strings.Contains(md, "$0.0031") {
		t.Fatalf("unexpected markdown packet:\n%s", md)
	}

	// Test ApplyWorktreePatch cleanly onto a new branch.
	repo := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run(repo, "init", "-q")
	_ = os.WriteFile(filepath.Join(repo, "x.txt"), []byte("hello\n"), 0o600)
	run(repo, "add", ".")
	run(repo, "-c", "user.name=T", "-c", "user.email=t@e.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")

	wt := filepath.Join(t.TempDir(), "wt")
	run(repo, "worktree", "add", "-b", "wt-branch", wt, "HEAD")
	_ = os.WriteFile(filepath.Join(wt, "x.txt"), []byte("hello fabric\n"), 0o600)
	// Also create a brand-new untracked file to verify WorktreePatch includes it!
	_ = os.WriteFile(filepath.Join(wt, "new_feature.txt"), []byte("brand new\n"), 0o600)

	// Flag-like branch names must be rejected.
	if err := ValidateBranchName("--orphan"); err == nil {
		t.Fatal("expected error for flag-like branch name")
	}
	if stat, err := ApplyDryRun(context.Background(), repo, wt, "apply-test"); err != nil || !strings.Contains(stat, "new_feature.txt") {
		t.Fatalf("ApplyDryRun failed or missed new file: stat=%q err=%v", stat, err)
	}

	if err := ApplyWorktreePatch(context.Background(), repo, wt, "apply-test"); err != nil {
		t.Fatalf("ApplyWorktreePatch failed: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(repo, "x.txt"))
	if strings.ReplaceAll(string(got), "\r\n", "\n") != "hello fabric\n" {
		t.Fatalf("expected applied content, got %q", string(got))
	}
	gotNew, err := os.ReadFile(filepath.Join(repo, "new_feature.txt"))
	if err != nil || strings.ReplaceAll(string(gotNew), "\r\n", "\n") != "brand new\n" {
		t.Fatalf("expected new untracked file applied, got %q err=%v", string(gotNew), err)
	}
}

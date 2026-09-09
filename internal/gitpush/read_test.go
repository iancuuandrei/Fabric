package gitpush

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestObserveActualLocalRemote(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			plan := fixturePlan(t, format)
			plan.Destination = t.TempDir()
			git := func(input string, args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", plan.Destination}, args...)...)
				cmd.Stdin = strings.NewReader(input)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatal(err, string(out))
				}
				return strings.TrimSpace(string(out))
			}
			git("", "init", "--bare", "--object-format="+format, "-q")
			absent, err := ObserveRemote(context.Background(), plan)
			if err != nil || absent.Commit != nil || absent.TargetRef != plan.TargetRef {
				t.Fatal("actual absent branch observation failed", err)
			}
			tree := git("", "mktree")
			commit := git("fixture\n", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit-tree", tree)
			git("", "update-ref", plan.TargetRef, commit)
			git("", "update-ref", "refs/heads/unrelated", commit)
			observed, err := ObserveRemote(context.Background(), plan)
			if err != nil || observed.Commit == nil || *observed.Commit != commit {
				t.Fatal("actual exact branch observation failed", err)
			}
			// Ambient command-scope Git configuration must not redirect the URL or
			// switch repositories. These settings apply only to the tested process.
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "url.https://invalid.example/.insteadOf")
			t.Setenv("GIT_CONFIG_VALUE_0", plan.Destination)
			t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "missing"))
			observed, err = ObserveRemote(context.Background(), plan)
			if err != nil || observed.Commit == nil || *observed.Commit != commit {
				t.Fatal("ambient Git configuration altered remote selection", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if result, err := ObserveRemote(ctx, plan); err == nil || result.TargetRef != "" || result.Commit != nil {
				t.Fatal("cancelled command fabricated observation")
			}
			plan.Destination = filepath.Join(t.TempDir(), "missing")
			if result, err := ObserveRemote(context.Background(), plan); err == nil || result.TargetRef != "" || result.Commit != nil {
				t.Fatal("missing remote treated as absent branch")
			}
		})
	}
}

func TestCaptureBoundsIncludeIOCopy(t *testing.T) {
	c := &capture{limit: 8}
	if n, err := io.Copy(c, strings.NewReader(strings.Repeat("x", 32))); err != nil || n != 32 || !c.overflow || c.buffer.Len() != 8 {
		t.Fatal("capture bound bypassed", err)
	}
	for _, entry := range isolatedEnvironment(t.TempDir()) {
		if strings.HasPrefix(entry, "GIT_CONFIG_GLOBAL=") && entry != "GIT_CONFIG_GLOBAL="+os.DevNull {
			t.Fatal("ambient global configuration retained")
		}
	}
}

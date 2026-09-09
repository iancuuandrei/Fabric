package gitpush

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/worktree"
)

func TestExecuteLocalPushWithExactLease(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			git := func(directory, input string, args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", directory}, args...)...)
				cmd.Stdin = strings.NewReader(input)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatal(err, string(out))
				}
				return strings.TrimSpace(string(out))
			}
			git(root, "", "init", "-q", "--object-format="+format)
			commit := func(text string) string {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				git(root, "", "add", "source.txt")
				git(root, "", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", text)
				return git(root, "", "rev-parse", "HEAD")
			}
			parent, head := commit("parent\n"), commit("child\n")
			source, err := repository.Discover(ctx, root, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			request, err := worktree.Prepare(strings.Repeat("c", 64), source)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := worktree.Acquire(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := lease.Close(); err != nil {
					t.Error(err)
				}
			}()
			persistFixtureIntent(t, request)
			binding, err := worktree.Create(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := worktree.Pristine(ctx, binding)
			if err != nil {
				t.Fatal(err)
			}
			repositoryID, err := source.ID()
			if err != nil {
				t.Fatal(err)
			}
			divergent := git(root, "unrelated\n", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit-tree", source.Tree)
			for _, mode := range []string{"create", "advance", "divergent"} {
				t.Run(mode, func(t *testing.T) {
					remote := t.TempDir()
					git(remote, "", "init", "--bare", "-q", "--object-format="+format)
					plan := Plan{Version: 1, Nonce: mode, RepositoryID: repositoryID, Workspace: binding, Candidate: candidate, Destination: remote, TargetRef: "refs/heads/fixture"}
					if mode != "create" {
						old := parent
						if mode == "divergent" {
							old = divergent
						}
						git(root, "", "push", "--", remote, old+":"+plan.TargetRef)
						plan.ExpectedOld = &old
					}
					intent, err := plan.Intent(strings.Repeat("d", 64))
					if err != nil {
						t.Fatal(err)
					}
					id, err := intent.ID()
					if err != nil {
						t.Fatal(err)
					}
					if _, err := Execute(ctx, plan, intent, effects.Authorization{}); err == nil {
						t.Fatal("unapproved push executed")
					}
					auth := effects.Authorization{IntentID: id, Actor: "fixture"}
					persistFixtureIntent(t, struct {
						Plan          Plan
						Intent        effects.Intent
						Authorization effects.Authorization
					}{plan, intent, auth})
					observed, err := Execute(ctx, plan, intent, auth)
					if mode == "divergent" {
						if err == nil {
							t.Fatal("history replacement admitted")
						}
						if actual := git(remote, "", "rev-parse", plan.TargetRef); actual != divergent {
							t.Fatal("divergent remote changed")
						}
					} else {
						if err != nil || observed.Commit == nil || *observed.Commit != head {
							t.Fatal("local push did not confirm target", err)
						}
						if _, err := Execute(ctx, plan, intent, auth); err == nil {
							t.Fatal("stale expected-old approval reused")
						}
					}
					if actual := git(root, "", "rev-parse", "HEAD"); actual != head {
						t.Fatal("canonical branch changed")
					}
					after, err := worktree.Fingerprint(ctx, binding)
					if err != nil || after != candidate {
						t.Fatal("push changed candidate", err)
					}
				})
			}
		})
	}
}

func persistFixtureIntent(t *testing.T, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

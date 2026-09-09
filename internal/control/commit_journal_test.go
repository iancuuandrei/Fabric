package control

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitlocal"
)

func TestCommitIntentIsDurableAndDoesNotExecute(t *testing.T) {
	path, _ := approvedRepository(t, config.Check{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10})
	ctx := context.Background()
	if _, err := StartWorkspace(ctx, path); err != nil {
		t.Fatal(err)
	}
	if s, err := Verify(ctx, path); err != nil || s.State != "READY" {
		t.Fatal("verification", err)
	}
	identity := gitlocal.Identity{Name: "Fixture", Email: "fixture@example.invalid", UnixSeconds: 1788739200}
	p, err := PrepareCommit(ctx, path, identity, identity, "durable intent fixture\n")
	if err != nil {
		t.Fatal(err)
	}
	id, err := p.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	auth := effects.Authorization{IntentID: id, Actor: "fixture"}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecordCommitIntent(ctx, path, p, effects.Authorization{}); err == nil {
		t.Fatal("missing authorization accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("rejected intent changed journal", err)
	}
	s, err := RecordCommitIntent(ctx, path, p, auth)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "COMMITTING" || s.Commit == nil || s.Commit.Outcome != "UNKNOWN" {
		t.Fatal("missing durable unknown state")
	}
	if err := exec.Command("git", "-C", p.Plan.Workspace.Request.Path, "cat-file", "-e", s.Commit.Intent.CommitID).Run(); err == nil {
		t.Fatal("intent unexpectedly wrote commit")
	}
	replayed, err := Inspect(path)
	if err != nil || replayed.Commit.Intent.CommitID != s.Commit.Intent.CommitID {
		t.Fatal("intent replay failed", err)
	}
	if _, err := RecordCommitIntent(ctx, path, p, auth); err == nil {
		t.Fatal("duplicate intent accepted")
	}
	if err := Append(path, "verification.planned", struct{}{}); err == nil {
		t.Fatal("unresolved commit allowed further events")
	}
}

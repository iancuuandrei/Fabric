package gitpush

import (
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/worktree"
)

func fixturePlan(t *testing.T, format string) Plan {
	t.Helper()
	root := t.TempDir()
	length := 40
	if format == "sha256" {
		length = 64
	}
	source := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: format, Commit: strings.Repeat("a", length), Tree: strings.Repeat("b", length)}
	request, err := worktree.Prepare(strings.Repeat("c", 64), source)
	if err != nil {
		t.Fatal(err)
	}
	binding := worktree.Binding{Request: request, GitDir: filepath.Join(root, ".git", "worktrees", "fixture")}
	id, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	return Plan{Version: 1, Nonce: "fixture", RepositoryID: strings.Repeat("d", 64), Workspace: binding, Candidate: worktree.Candidate{Version: 1, WorktreeID: id, Head: source.Commit, IndexHash: strings.Repeat("e", 64), FilesHash: strings.Repeat("f", 64)}, Destination: "https://example.invalid/owner/repo.git", TargetRef: "refs/heads/initial"}
}

func TestPushIdentityAndApproval(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			plan := fixturePlan(t, format)
			intent, err := plan.Intent(strings.Repeat("1", 64))
			if err != nil {
				t.Fatal(err)
			}
			id, err := intent.ID()
			if err != nil {
				t.Fatal(err)
			}
			auth := effects.Authorization{IntentID: id, Actor: "fixture"}
			for _, mutate := range []func(*Plan){
				func(p *Plan) { p.Destination = "https://example.invalid/other/repo.git" },
				func(p *Plan) { p.TargetRef = "refs/heads/other" },
				func(p *Plan) { old := strings.Repeat("2", len(p.Candidate.Head)); p.ExpectedOld = &old },
				func(p *Plan) { p.Nonce = "fresh" },
			} {
				changed := plan
				mutate(&changed)
				other, err := changed.Intent(intent.PlanID)
				if err != nil || auth.Validate(other) == nil {
					t.Fatal("changed push retained approval", err)
				}
			}
			plan.Destination = t.TempDir()
			if _, err := plan.ID(); err != nil {
				t.Fatal("local qualification destination rejected", err)
			}
		})
	}
}

func TestPushRejectsAmbiguousDestinationsAndRefs(t *testing.T) {
	for _, destination := range []string{"origin", "relative/repo", "ssh://host/repo", "file:///repo", "https://user:password@example.invalid/repo", "https://example.invalid/repo?token=value", "https://example.invalid/repo#fragment", "https://example.invalid/%0a", "\\\\server\\share", "https://example.invalid/" + "\n"} {
		if validDestination(destination) {
			t.Fatalf("ambiguous or unsupported destination admitted: %q", destination)
		}
	}
	for _, ref := range []string{"main", "refs/tags/v1", "refs/heads/", "refs/heads/a..b", "refs/heads/.hidden", "refs/heads/a.lock", "refs/heads/a@{1}", "refs/heads/a b", "refs/heads/a:b", "refs/heads/a//b", "refs/heads/a."} {
		if validTarget(ref) {
			t.Fatalf("invalid branch admitted: %q", ref)
		}
	}
	p := fixturePlan(t, "sha1")
	empty := ""
	p.ExpectedOld = &empty
	if _, err := p.ID(); err == nil {
		t.Fatal("empty expected commit confused with absent ref")
	}
	p.ExpectedOld = nil
	p.Candidate.Head = strings.Repeat("3", 40)
	if _, err := p.ID(); err == nil {
		t.Fatal("foreign candidate commit admitted")
	}
}

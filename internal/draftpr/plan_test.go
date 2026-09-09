package draftpr

import (
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitpush"
	"harness.local/engorch/internal/repository"
	"harness.local/engorch/internal/worktree"
)

func fixturePlan(t *testing.T) Plan {
	t.Helper()
	root := t.TempDir()
	source := repository.Identity{Version: 1, Name: "fixture", Root: root, CommonDir: filepath.Join(root, ".git"), ObjectFormat: "sha1", Commit: strings.Repeat("a", 40), Tree: strings.Repeat("b", 40)}
	request, err := worktree.Prepare(strings.Repeat("c", 64), source)
	if err != nil {
		t.Fatal(err)
	}
	binding := worktree.Binding{Request: request, GitDir: filepath.Join(root, ".git", "worktrees", "fixture")}
	workspaceID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	push := gitpush.Plan{Version: 1, Nonce: "push", RepositoryID: strings.Repeat("d", 64), Workspace: binding, Candidate: worktree.Candidate{Version: 1, WorktreeID: workspaceID, Head: source.Commit, IndexHash: strings.Repeat("e", 64), FilesHash: strings.Repeat("f", 64)}, Destination: "https://github.com/fixture/project.git", TargetRef: "refs/heads/initial"}
	intent, err := push.Intent(strings.Repeat("1", 64))
	if err != nil {
		t.Fatal(err)
	}
	return Plan{Version: 1, Nonce: "draft", Push: push, PushIntent: intent, Repository: "fixture/project", BaseRef: "refs/heads/main", BaseCommit: strings.Repeat("2", 40), Title: "Initial implementation", Body: "Changes and executed evidence.\n"}
}

func TestDraftRequestBindsPushAndExactText(t *testing.T) {
	plan := fixturePlan(t)
	intent, err := plan.Intent()
	if err != nil {
		t.Fatal(err)
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	request, err := plan.Request()
	if err != nil {
		t.Fatal(err)
	}
	if !request.Draft || request.MaintainerCanModify || request.Title != plan.Title || request.Head != "initial" || request.Base != "main" || request.Body != plan.Body+"\n\n"+markerPrefix+id+" -->" {
		t.Fatal("draft payload differs from approved inputs")
	}
	if intent.Kind != "draft_pr" || intent.RunID != plan.PushIntent.RunID || intent.RepositoryID != plan.PushIntent.RepositoryID || intent.PlanID != plan.PushIntent.PlanID {
		t.Fatal("draft envelope lost push provenance")
	}
	auth := effects.Authorization{IntentID: id, Actor: "fixture"}
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Title += " changed" },
		func(p *Plan) { p.Body += "extra evidence" },
		func(p *Plan) { p.BaseRef = "refs/heads/other" },
		func(p *Plan) { p.BaseCommit = strings.Repeat("3", 40) },
		func(p *Plan) { p.Nonce = "fresh" },
	} {
		changed := plan
		mutate(&changed)
		other, err := changed.Intent()
		if err != nil || auth.Validate(other) == nil {
			t.Fatal("changed draft reused approval", err)
		}
	}
	if err := auth.Validate(plan.PushIntent); err == nil {
		t.Fatal("draft approval authorizes push")
	}
}

func TestDraftRejectsSubstitutedScopeAndMarker(t *testing.T) {
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Repository = "other/project" },
		func(p *Plan) { p.BaseRef = p.Push.TargetRef },
		func(p *Plan) { p.BaseRef = "refs/tags/v1" },
		func(p *Plan) { p.PushIntent.InputHash = strings.Repeat("0", 64) },
		func(p *Plan) { p.Push.Destination = "https://other.example/fixture/project.git" },
		func(p *Plan) { p.Body = markerPrefix + "forged -->" },
		func(p *Plan) { p.Body = strings.ToUpper(markerPrefix) + "forged -->" },
		func(p *Plan) { p.Title = "multi\nline" },
		func(p *Plan) { p.Body = strings.Repeat("x", 32769) },
		func(p *Plan) { p.Repository = "fixture/../project" },
	} {
		plan := fixturePlan(t)
		mutate(&plan)
		if _, err := plan.Request(); err == nil {
			t.Fatal("invalid draft request admitted")
		}
	}
}

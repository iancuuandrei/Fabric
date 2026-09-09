package control

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/worktree"
)

func TestCommitPreparationBindsReadyCandidateAndMetadata(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			t.Setenv("GIT_DEFAULT_HASH", format)
			testCommitPreparation(t, format)
		})
	}
}

func testCommitPreparation(t *testing.T, format string) {
	path, _ := approvedRepository(t, config.Check{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10})
	if _, err := StartWorkspace(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	identity := gitlocal.Identity{Name: "Fixture Author", Email: "fixture@example.invalid", UnixSeconds: 1788739200}
	if _, err := PrepareCommit(context.Background(), path, identity, identity, "fixture\n"); err == nil {
		t.Fatal("unverified candidate admitted")
	}
	s, err := Verify(context.Background(), path)
	if err != nil || s.State != "READY" {
		t.Fatal("verification failed", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PrepareCommit(context.Background(), path, identity, identity, "fixture\n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Plan.Workspace.Request.Source.ObjectFormat != format {
		t.Fatal("fixture object format mismatch")
	}
	if p.Plan.Candidate != *s.Candidate || p.Intent.Kind != "commit" {
		t.Fatal("commit scope mismatch")
	}
	id, err := p.Plan.ID()
	if err != nil || id != p.Intent.InputHash {
		t.Fatal("commit effect binding mismatch", err)
	}
	changed := p.Plan
	changed.Committer.UnixSeconds++
	other, err := changed.ID()
	if err != nil || other == id {
		t.Fatal("committer time not bound", err)
	}
	changed = p.Plan
	changed.Message = "different\n"
	other, err = changed.ID()
	if err != nil || other == id {
		t.Fatal("commit message not bound", err)
	}
	changed = p.Plan
	changed.Author.Name = "Injected\nparent malicious"
	if _, err := changed.ID(); err == nil {
		t.Fatal("header injection admitted")
	}
	changed = p.Plan
	changed.Files = append(changed.Files[:0:0], changed.Files...)
	changed.Files[0].Hash = strings.Repeat("0", 64)
	if _, err := changed.ID(); err == nil {
		t.Fatal("manifest substitution admitted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatal("preparation changed controller journal", err)
	}
	assertCommitObjectMatchesGit(t, p.Plan)
	if err := os.WriteFile(filepath.Join(s.Workspace.Request.Path, "drift"), []byte("unexpected"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareCommit(context.Background(), path, identity, identity, "fixture\n"); err == nil {
		t.Fatal("drifted ready candidate admitted")
	}
	lease, err := worktree.Acquire(p.Plan.Workspace.Request)
	if err != nil {
		t.Fatal(err)
	}
	objects, readErr := gitlocal.ReadCandidateObjects(context.Background(), p.Plan)
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if readErr == nil || objects.CommitID != "" || objects.Blobs != nil {
		t.Fatal("drifted candidate yielded objects")
	}
}

func assertCommitObjectMatchesGit(t *testing.T, plan gitlocal.CommitPlan) {
	t.Helper()
	lease, err := worktree.Acquire(plan.Workspace.Request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lease.Close(); err != nil {
			t.Error(err)
		}
	}()
	objects, err := gitlocal.ReadCandidateObjects(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := objects.Validate(plan); err != nil {
		t.Fatal(err)
	}
	mutations := []struct {
		name   string
		change func(*gitlocal.CandidateObjects)
	}{
		{"missing blob", func(o *gitlocal.CandidateObjects) { o.Blobs = o.Blobs[:0] }},
		{"blob path", func(o *gitlocal.CandidateObjects) { o.Blobs[0].Path = "substituted" }},
		{"blob bytes", func(o *gitlocal.CandidateObjects) { o.Blobs[0].Data = []byte("substituted") }},
		{"blob identity", func(o *gitlocal.CandidateObjects) {
			o.Blobs[0].ObjectID = strings.Repeat("0", len(o.Blobs[0].ObjectID))
		}},
		{"missing tree", func(o *gitlocal.CandidateObjects) { o.Trees = nil }},
		{"tree path", func(o *gitlocal.CandidateObjects) { o.Trees[0].Path = "substituted" }},
		{"tree bytes", func(o *gitlocal.CandidateObjects) { o.Trees[0].Data = []byte("substituted") }},
		{"tree identity", func(o *gitlocal.CandidateObjects) {
			o.Trees[0].ObjectID = strings.Repeat("0", len(o.Trees[0].ObjectID))
		}},
		{"commit bytes", func(o *gitlocal.CandidateObjects) { o.CommitData = []byte("substituted") }},
		{"commit identity", func(o *gitlocal.CandidateObjects) { o.CommitID = strings.Repeat("0", len(o.CommitID)) }},
	}
	for _, mutation := range mutations {
		changed := objects
		changed.Blobs = append([]gitlocal.EncodedBlob(nil), objects.Blobs...)
		changed.Trees = append([]gitlocal.EncodedTree(nil), objects.Trees...)
		mutation.change(&changed)
		if err := changed.Validate(plan); err == nil {
			t.Fatal("object substitution accepted", mutation.name)
		}
	}
	if len(objects.Blobs) != len(plan.Files) || objects.Trees[len(objects.Trees)-1].ObjectID != plan.Workspace.Request.Source.Tree {
		t.Fatal("candidate objects differ from source tree")
	}
	for _, blob := range objects.Blobs {
		stored, err := exec.Command("git", "-C", plan.Workspace.Request.Path, "cat-file", "blob", blob.ObjectID).Output()
		if err != nil || !bytes.Equal(stored, blob.Data) {
			t.Fatal("candidate blob differs from Git", err)
		}
	}
	data, id, err := gitlocal.CommitObject(plan, plan.Workspace.Request.Source.Tree)
	if err != nil {
		t.Fatal(err)
	}
	if objects.CommitID != id || !bytes.Equal(objects.CommitData, data) {
		t.Fatal("candidate commit encoding differs")
	}
	planHash, err := plan.ID()
	if err != nil {
		t.Fatal(err)
	}
	repositoryID, err := plan.Workspace.Request.Source.ID()
	if err != nil {
		t.Fatal(err)
	}
	intent := effects.Intent{Version: 1, RunID: plan.Workspace.Request.RunID, PlanID: strings.Repeat("a", 64), RepositoryID: repositoryID, Kind: "commit", InputHash: planHash}
	intentID, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	auth := effects.Authorization{IntentID: intentID, Actor: "fixture"}
	if err := gitlocal.StoreObjects(context.Background(), plan, objects, intent, effects.Authorization{}); err == nil {
		t.Fatal("unapproved storage accepted")
	}
	bad := intent
	bad.InputHash = strings.Repeat("0", 64)
	if err := gitlocal.StoreObjects(context.Background(), plan, objects, bad, auth); err == nil {
		t.Fatal("foreign storage intent accepted")
	}
	intentBytes, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(t.TempDir(), "storage-intent.json"), intentBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := gitlocal.StoreObjects(context.Background(), plan, objects, intent, auth); err != nil {
		t.Fatal(err)
	}
	storedCommit, err := exec.Command("git", "-C", plan.Workspace.Request.Path, "cat-file", "commit", objects.CommitID).Output()
	if err != nil || !bytes.Equal(storedCommit, objects.CommitData) {
		t.Fatal("object storage readback failed", err)
	}
	cmd := exec.Command("git", "-C", plan.Workspace.Request.Path, "-c", "commit.gpgSign=false", "commit-tree", plan.Workspace.Request.Source.Tree, "-p", plan.Candidate.Head)
	cmd.Stdin = strings.NewReader(plan.Message)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME=Fixture Author", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE=@1788739200 +0000", "GIT_COMMITTER_NAME=Fixture Author", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_COMMITTER_DATE=@1788739200 +0000")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != id {
		t.Fatalf("commit-tree mismatch: %v, Git %s, encoder %s", err, out, id)
	}
	read := exec.Command("git", "-C", plan.Workspace.Request.Path, "cat-file", "commit", id)
	stored, err := read.Output()
	if err != nil || !bytes.Equal(stored, data) {
		t.Fatalf("stored commit bytes differ: %v", err)
	}
	for _, tree := range []string{"", strings.Repeat("A", len(id)), strings.Repeat("0", len(id)+1)} {
		if _, _, err := gitlocal.CommitObject(plan, tree); err == nil {
			t.Fatal("invalid tree ID accepted")
		}
	}
	if err := gitlocal.AdvanceRef(context.Background(), plan, objects, intent, effects.Authorization{}); err == nil {
		t.Fatal("unapproved ref update accepted")
	}
	if err := gitlocal.AdvanceRef(context.Background(), plan, objects, intent, auth); err != nil {
		t.Fatal(err)
	}
	if err := gitlocal.AdvanceRef(context.Background(), plan, objects, intent, auth); err == nil {
		t.Fatal("stale parent accepted on repeat")
	}
	refHead, err := exec.Command("git", "-C", plan.Workspace.Request.Path, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(refHead)) != objects.CommitID {
		t.Fatal("branch readback differs", err)
	}
	sourceHead, err := exec.Command("git", "-C", plan.Workspace.Request.Source.Root, "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(sourceHead)) != plan.Candidate.Head {
		t.Fatal("canonical source HEAD changed", err)
	}
	// Restore only this disposable fixture ref so the caller's subsequent file-
	// drift checks exercise drift, rather than failing on the advanced HEAD.
	if out, err := exec.Command("git", "-C", plan.Workspace.Request.Path, "update-ref", "refs/heads/"+plan.Workspace.Request.Branch, plan.Candidate.Head, objects.CommitID).CombinedOutput(); err != nil {
		t.Fatalf("fixture restore: %v %s", err, out)
	}
}

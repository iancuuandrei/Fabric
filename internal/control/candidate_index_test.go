package control

import (
	"bytes"
	"context"
	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/journal"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSemanticCandidateMutationBlocksVerifierDispatch(t *testing.T) {
	for _, mode := range []string{"head", "staged", "changed", "added", "removed"} {
		t.Run(mode, func(t *testing.T) {
			c := creation(t)
			c.Config.CandidateIdentity = "semantic-index-v2"
			c.Config.Verification = []config.Check{{Name: "must-not-run", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
			path, _ := approvedRepositoryCreation(t, c)
			before, err := StartWorkspace(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			root := before.Workspace.Request.Path
			file := filepath.Join(root, "file.txt")
			git := func(args ...string) {
				cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
				if b, e := cmd.CombinedOutput(); e != nil {
					t.Fatalf("git: %v %s", e, b)
				}
			}
			switch mode {
			case "head":
				git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "changed head")
			case "staged":
				if err := os.WriteFile(file, []byte("different\n"), 0600); err != nil {
					t.Fatal(err)
				}
				git("add", "file.txt")
				if err := os.WriteFile(file, []byte("base\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed":
				if err := os.WriteFile(file, []byte("changed\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "added":
				if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "removed":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Verify(context.Background(), path); err == nil {
				t.Fatal("candidate mutation admitted")
			}
			after, err := Inspect(path)
			if err != nil {
				t.Fatal(err)
			}
			if after.Verification != nil || *after.Candidate != *before.Candidate {
				t.Fatal("mutation advanced verification or replaced authority")
			}
		})
	}
}

func TestSemanticIndexVerificationReconcilesAndCommits(t *testing.T) {
	c := creation(t)
	c.Config.CandidateIdentity = "semantic-index-v2"
	c.Config.Verification = []config.Check{
		{Name: "metadata-rewrite", Argv: []string{"git", "update-index", "--index-version=4"}, TimeoutSeconds: 10},
		{Name: "benign-diff-check", Argv: []string{"git", "diff", "--check"}, TimeoutSeconds: 10},
	}
	path, _ := approvedRepositoryCreation(t, c)
	before, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Candidate.Version != 2 {
		t.Fatal("identity config not bound")
	}
	change, err := PrepareFiles(context.Background(), path, []fileeffects.Change{{Path: "new.txt", ContentBase64: content64("new candidate bytes\n")}})
	if err != nil {
		t.Fatal(err)
	}
	changeID, err := change.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	before, err = ApplyFiles(context.Background(), path, change, effects.Authorization{IntentID: changeID, Actor: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	after, err := Verify(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != "READY" || *after.Candidate != *before.Candidate {
		t.Fatal("metadata changed candidate authority")
	}
	events, err := journal.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == "candidate.index-observed" {
			if bytes.Contains(e.Payload, []byte("METADATA_ONLY_INDEX_CHANGE")) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("missing metadata-only journal evidence")
	}
	identity := gitlocal.Identity{Name: "Fixture", Email: "fixture@example.invalid", UnixSeconds: 1788739200}
	prepared, err := PrepareCommit(context.Background(), path, identity, identity, "semantic fixture\n")
	if err != nil {
		t.Fatal(err)
	}
	id, err := prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	final, err := ExecuteCommit(context.Background(), path, prepared, effects.Authorization{IntentID: id, Actor: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if final.Commit == nil {
		t.Fatal("no local commit receipt")
	}
}

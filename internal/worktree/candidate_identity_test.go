package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func semanticFixture(t *testing.T) (Binding, Candidate) {
	t.Helper()
	_, request := fixture(t)
	request.CandidateIdentity = "semantic-index-v2"
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	binding, err := Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Version != 2 {
		t.Fatalf("candidate version = %d, want 2", candidate.Version)
	}
	return binding, candidate
}

func TestSemanticCandidateAcceptsIndexStatRewrite(t *testing.T) {
	binding, before := semanticFixture(t)
	initial, err := IndexEvidence(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(binding.Request.Path, "source.txt")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	changed := info.ModTime().Add(-2 * time.Hour)
	if err := os.Chtimes(file, changed, changed); err != nil {
		t.Fatal(err)
	}
	gitTest(t, binding.Request.Path, "update-index", "--refresh")
	afterEvidence, err := IndexEvidence(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if afterEvidence.RawHash == initial.RawHash {
		t.Fatal("fixture did not rewrite raw index stat data")
	}
	if afterEvidence.SemanticHash != initial.SemanticHash {
		t.Fatal("stat-only index rewrite changed semantic identity")
	}
	after, err := Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("stat-only index rewrite changed candidate identity")
	}
}

func TestSemanticCandidateAcceptsGitDiffCheck(t *testing.T) {
	binding, before := semanticFixture(t)
	initial, err := IndexEvidence(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, binding.Request.Path, "diff", "--check")
	afterEvidence, err := IndexEvidence(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if afterEvidence.SemanticHash != initial.SemanticHash || after != before {
		t.Fatal("benign verifier changed semantic candidate identity")
	}
}

func TestSemanticCandidateBlocksStagedBlobChange(t *testing.T) {
	binding, before := semanticFixture(t)
	file := filepath.Join(binding.Request.Path, "source.txt")
	if err := os.WriteFile(file, []byte("staged replacement\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, binding.Request.Path, "add", "source.txt")
	if err := os.WriteFile(file, []byte("committed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if after.FilesHash != before.FilesHash || after.IndexHash == before.IndexHash || after == before {
		t.Fatal("staged blob change was not isolated to semantic index identity")
	}
}

func TestSemanticCandidateBlocksHeadChange(t *testing.T) {
	binding, _ := semanticFixture(t)
	file := filepath.Join(binding.Request.Path, "source.txt")
	if err := os.WriteFile(file, []byte("next commit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, binding.Request.Path, "add", "source.txt")
	gitTest(t, binding.Request.Path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "changed HEAD")
	if _, err := Fingerprint(context.Background(), binding); err == nil {
		t.Fatal("changed HEAD admitted")
	}
}

func TestSemanticCandidateBlocksFileChanges(t *testing.T) {
	mutations := map[string]func(*testing.T, Binding){
		"changed": func(t *testing.T, binding Binding) {
			if err := os.WriteFile(filepath.Join(binding.Request.Path, "source.txt"), []byte("changed\n"), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"added": func(t *testing.T, binding Binding) {
			if err := os.WriteFile(filepath.Join(binding.Request.Path, "untracked.txt"), []byte("added\n"), 0600); err != nil {
				t.Fatal(err)
			}
		},
		"removed": func(t *testing.T, binding Binding) {
			if err := os.Remove(filepath.Join(binding.Request.Path, "source.txt")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			binding, before := semanticFixture(t)
			mutate(t, binding)
			after, err := Fingerprint(context.Background(), binding)
			if err != nil {
				t.Fatal(err)
			}
			if after.FilesHash == before.FilesHash || after == before {
				t.Fatal("file mutation did not change candidate identity")
			}
		})
	}
}

func TestSemanticCandidateBlocksIndexFlagsChange(t *testing.T) {
	binding, before := semanticFixture(t)
	gitTest(t, binding.Request.Path, "update-index", "--assume-unchanged", "source.txt")
	after, err := Fingerprint(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if after.FilesHash != before.FilesHash || after.IndexHash == before.IndexHash || after == before {
		t.Fatal("index flag change was not bound to semantic identity")
	}
}

func TestCandidateIdentityModesAndDomains(t *testing.T) {
	_, request := fixture(t)
	request.CandidateIdentity = "unsupported"
	if request.Validate() == nil {
		t.Fatal("unsupported candidate identity admitted")
	}
	binding, semantic := semanticFixture(t)
	legacy := semantic
	legacy.Version = 1
	semanticID, err := semantic.ID()
	if err != nil {
		t.Fatal(err)
	}
	legacyID, err := legacy.ID()
	if err != nil {
		t.Fatal(err)
	}
	if semanticID == legacyID {
		t.Fatal("candidate v1 and v2 ID domains collided")
	}
	if semantic.ValidateBinding(binding) != nil || legacy.ValidateBinding(binding) == nil {
		t.Fatal("candidate/binding identity contract mismatch was not rejected")
	}
	if _, err := IndexEvidence(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
}

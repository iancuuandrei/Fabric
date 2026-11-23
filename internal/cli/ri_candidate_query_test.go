package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/ri"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func TestCandidateQueryCommandRejectsBadArgumentsWithoutEffects(t *testing.T) {
	root := t.TempDir()
	for _, args := range [][]string{
		{"ri", "candidate-query"},
		{"ri", "candidate-query", "run", "ri.exe", "bad", "base.json", "query.json", ""},
	} {
		var output bytes.Buffer
		if err := Execute(context.Background(), args, root, &output); err == nil || output.Len() != 0 {
			t.Fatalf("invalid candidate-query args were accepted or emitted output: args=%v err=%v output=%q", args, err, output.String())
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".harness")); !os.IsNotExist(err) {
		t.Fatalf("invalid candidate-query args wrote controller state: %v", err)
	}
}

func TestRICandidateQueryUsesExactConfirmedRunAndPreservesNoopCandidate(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("requires pinned Rust RI executable")
	}
	executable, err := filepath.Abs(executable)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(binary)
	exeSHA := hex.EncodeToString(hash[:])
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	git("init", "-q")
	for path, content := range map[string]string{
		"go.mod":   "module example.invalid/candidate\n\ngo 1.27\n",
		"pkg/a.go": "package pkg\nfunc CandidateQueryMarker() {}\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "go.mod", "pkg/a.go")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	mustExecuteCLI(t, root, "init")
	var planned bytes.Buffer
	if err := Execute(context.Background(), []string{"plan", "Use the candidate graph fixture"}, root, &planned); err != nil {
		t.Fatal(err)
	}
	var snapshot control.Snapshot
	if err := json.Unmarshal(planned.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	mustExecuteCLI(t, root, "approve", snapshot.RunID, snapshot.PlanID, "fixture-operator")
	runJournal := filepath.Join(root, ".harness", "runs", snapshot.RunID+".jsonl")
	confirmed, err := control.StartWorkspace(context.Background(), runJournal)
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Workspace == nil || confirmed.Candidate == nil || confirmed.WorkspaceOutcome != "CONFIRMED" {
		t.Fatalf("fixture did not produce confirmed candidate: %#v", confirmed)
	}

	var inventoryOutput bytes.Buffer
	if err := Execute(context.Background(), []string{"ri", "modules"}, root, &inventoryOutput); err != nil {
		t.Fatal(err)
	}
	var inventory ri.GoModuleInventory
	if err := json.Unmarshal(inventoryOutput.Bytes(), &inventory); err != nil {
		t.Fatal(err)
	}
	spec := goGraphSpec{Files: []goGraphFileSpec{{Path: "pkg/a.go"}}, Generators: []ri.GoGeneratorBinding{}, ModuleInventory: &inventory}
	specBytes, err := canonical.Bytes(spec)
	if err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(root, "base-spec.json")
	if err := os.WriteFile(specPath, specBytes, 0600); err != nil {
		t.Fatal(err)
	}
	query := ri.SemanticQuery{Vocabulary: "symbol", NamePrefix: "CandidateQueryMarker", Limit: 10}
	queryBytes, err := canonical.Bytes(query)
	if err != nil {
		t.Fatal(err)
	}
	queryPath := filepath.Join(root, "query.json")
	if err := os.WriteFile(queryPath, queryBytes, 0600); err != nil {
		t.Fatal(err)
	}
	journalBefore, err := os.ReadFile(runJournal)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"ri", "candidate-query", confirmed.RunID, executable, exeSHA, specPath, queryPath}, root, &output); err != nil {
		t.Fatal(err)
	}
	var result goCandidateQueryEnvelope
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	candidateID, err := confirmed.Candidate.ID()
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID != confirmed.RunID || result.CandidateID != candidateID || result.CandidateGraphDigest == result.BaseGraphDigest || result.Result.CandidateID != candidateID || result.Result.SourceID != inventory.RepositoryID || result.Result.Coverage != "PARTIAL" || result.ChangedPathCount != 0 || result.DeletedPathCount != 0 || len(result.Result.Items) != 1 || result.Result.Items[0].Label != "CandidateQueryMarker" {
		t.Fatalf("no-op candidate graph lost its source/candidate evidence: %+v", result)
	}
	if bytes.Contains(output.Bytes(), []byte("func CandidateQueryMarker")) {
		t.Fatal("candidate-query output included source bytes")
	}
	journalAfter, err := os.ReadFile(runJournal)
	if err != nil || !bytes.Equal(journalBefore, journalAfter) {
		t.Fatalf("candidate query wrote the controller journal: err=%v", err)
	}
	candidatePath := filepath.Join(confirmed.Workspace.Request.Path, "pkg", "a.go")
	if err := os.WriteFile(candidatePath, []byte("package pkg\nfunc StaleCandidateQueryMarker() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := Execute(context.Background(), []string{"ri", "candidate-query", confirmed.RunID, executable, exeSHA, specPath, queryPath}, root, &output); err == nil || output.Len() != 0 {
		t.Fatalf("stale candidate was accepted or emitted output: err=%v output=%q", err, output.String())
	}
}

func TestValidateCandidateQuerySnapshotRequiresExactConfirmedBinding(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command("git", "-C", root, "init", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	if err := os.WriteFile(filepath.Join(root, "source.go"), []byte("package sample\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "source.go"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	identity, err := repository.Discover(context.Background(), root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	runID := digestForCandidateQuery("run")
	request, err := worktree.Prepare(runID, identity)
	if err != nil {
		t.Fatal(err)
	}
	binding := worktree.Binding{Request: request, GitDir: filepath.Join(root, ".git", "worktrees", "fixture")}
	worktreeID, err := binding.ID()
	if err != nil {
		t.Fatal(err)
	}
	candidate := worktree.Candidate{
		Version: 1, WorktreeID: worktreeID, Head: identity.Commit,
		IndexHash: digestForCandidateQuery("index"), FilesHash: digestForCandidateQuery("files"), FileCount: 1,
	}
	base := control.Snapshot{
		RunID: runID, Creation: control.Creation{Repository: identity}, Workspace: &binding,
		Candidate: &candidate, WorkspaceOutcome: "CONFIRMED", FileOutcome: "CONFIRMED",
	}
	if got, err := validateCandidateQuerySnapshot(base, runID, identity); err != nil || got == "" {
		t.Fatalf("valid confirmed snapshot rejected: id=%s err=%v", got, err)
	}
	cases := map[string]func(*control.Snapshot, *repository.Identity){
		"run mismatch":         func(s *control.Snapshot, _ *repository.Identity) { s.RunID = digestForCandidateQuery("other") },
		"unknown workspace":    func(s *control.Snapshot, _ *repository.Identity) { s.WorkspaceOutcome = "UNKNOWN" },
		"unknown file outcome": func(s *control.Snapshot, _ *repository.Identity) { s.FileOutcome = "UNKNOWN" },
		"missing candidate":    func(s *control.Snapshot, _ *repository.Identity) { s.Candidate = nil },
		"source mismatch":      func(_ *control.Snapshot, got *repository.Identity) { got.Name = "different" },
		"candidate head mismatch": func(s *control.Snapshot, _ *repository.Identity) {
			s.Candidate.Head = digestForCandidateQuery("other")[:40]
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := base
			candidateCopy := candidate
			snapshot.Candidate = &candidateCopy
			currentIdentity := identity
			mutate(&snapshot, &currentIdentity)
			if _, err := validateCandidateQuerySnapshot(snapshot, runID, currentIdentity); err == nil {
				t.Fatal("invalid snapshot binding accepted")
			}
		})
	}
}

func digestForCandidateQuery(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

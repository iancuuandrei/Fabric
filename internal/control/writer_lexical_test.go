package control

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/config"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/ri"
	"harness.local/engorch/internal/runtime"
	"harness.local/engorch/internal/worktree"
)

func TestWriterLexicalInvocationAndLeasedSelectionActualRust(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("requires actual Rust RI")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	c := creation(t)
	c.Config.Writer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-writer", Effort: "high", Role: "writer"}
	c.Config.Reviewer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-reviewer", Effort: "high", Role: "reviewer"}
	c.Config.Explorer = &runtime.Profile{Runtime: "fake", Provider: "deterministic", Model: "explicit-explorer", Effort: "high", Role: "explorer"}
	c.Config.Verification = []config.Check{{Name: "git-version", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
	path, _ := approvedRepositoryCreation(t, c)
	s, err := StartWorkspace(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := ri.ObserveLexical(context.Background(), s.Creation.Repository)
	if err != nil {
		t.Fatal(err)
	}
	manifestID, err := observed.Manifest.ID()
	if err != nil {
		t.Fatal(err)
	}
	plan := ri.LexicalPlan{Version: 1, Repository: s.Creation.Repository, ManifestID: manifestID, Files: len(observed.Manifest.Files), Executable: executable, ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256(binary)), StageRoot: filepath.Join(t.TempDir(), "base"), BatchBytes: 64 << 20, BatchFiles: 100}
	intent, err := plan.Intent(s.RunID, s.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteRILexical(context.Background(), path, plan, observed.Manifest, effects.Authorization{IntentID: id, Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	base, err := PrepareWriterInvocation(path)
	if err != nil || base.ID == plain.ID {
		t.Fatal("base lexical context not bound", err)
	}
	change := prepareChange(t, path)
	if _, err := ApplyFiles(context.Background(), path, change, authorize(t, change)); err != nil {
		t.Fatal(err)
	}
	beforeOverlay, err := PrepareWriterInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareLexicalOverlay(context.Background(), path, filepath.Join(t.TempDir(), "overlay"))
	if err != nil {
		t.Fatal(err)
	}
	id, err = prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	s, err = ExecuteLexicalOverlay(context.Background(), path, prepared, effects.Authorization{IntentID: id, Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	withOverlay, err := PrepareWriterInvocation(path)
	if err != nil || withOverlay.ID == beforeOverlay.ID {
		t.Fatal("overlay context not bound", err)
	}
	explorer, err := PrepareExplorerInvocation(path, "Locate the changed behavior")
	if err != nil {
		t.Fatal(err)
	}
	withoutLexical := s
	withoutLexical.RILexical = nil
	withoutLexical.RILexicalOverlay = nil
	plainExplorer, err := explorerInvocation(withoutLexical, "Locate the changed behavior")
	if err != nil || plainExplorer.ID == explorer.ID {
		t.Fatal("explorer lexical context not bound", err)
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		t.Fatal(err)
	}
	binding, selectionErr := selectRuntimeLexicalLeased(context.Background(), path, true, *s.Workspace)
	closeErr := lease.Close()
	if selectionErr != nil || closeErr != nil || binding.Overlay == nil || binding.Overlay.Candidate != prepared.Plan.CandidateID {
		t.Fatal("writer leased selection failed", selectionErr, closeErr)
	}
	if err := validateRoleLexicalEvidence(s, &binding, &binding, prepared.Plan.CandidateID); err != nil {
		t.Fatal(err)
	}
	if err := validateRoleLexicalEvidence(s, &binding, nil, prepared.Plan.CandidateID); err == nil {
		t.Fatal("missing lexical receipt accepted")
	}
	substituted := binding
	substituted.ExecutableSHA256 = strings.Repeat("f", 64)
	if err := validateRoleLexicalEvidence(s, &binding, &substituted, prepared.Plan.CandidateID); err == nil {
		t.Fatal("substituted lexical receipt accepted")
	}
	change, err = PrepareFiles(context.Background(), path, []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText("after\n"), ContentBase64: content64("next\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFiles(context.Background(), path, change, authorize(t, change)); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareWriterInvocation(path); err == nil {
		t.Fatal("stale overlay admitted into writer invocation")
	}
	prepared, err = PrepareLexicalOverlay(context.Background(), path, filepath.Join(t.TempDir(), "review-overlay"))
	if err != nil {
		t.Fatal(err)
	}
	id, err = prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteLexicalOverlay(context.Background(), path, prepared, effects.Authorization{IntentID: id, Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	s, err = Verify(context.Background(), path)
	if err != nil || s.State != "REVIEWING" {
		t.Fatal("review phase unavailable", err)
	}
	review, err := PrepareReviewInvocation(path)
	if err != nil {
		t.Fatal(err)
	}
	without := s
	without.RILexical = nil
	without.RILexicalOverlay = nil
	plainReview, err := reviewInvocation(without)
	if err != nil || plainReview.ID == review.ID {
		t.Fatal("review lexical context not bound", err)
	}
	lease, err = worktree.Acquire(s.Workspace.Request)
	if err != nil {
		t.Fatal(err)
	}
	reviewBinding, selectionErr := selectRoleRuntimeLexical(context.Background(), path, s)
	closeErr = lease.Close()
	if selectionErr != nil || closeErr != nil || reviewBinding == nil || reviewBinding.Overlay == nil {
		t.Fatal("review lexical selection failed", selectionErr, closeErr)
	}
}

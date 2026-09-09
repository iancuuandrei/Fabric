package control

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/worktree"
)

func TestJournaledLexicalOverlayAndLostObservationRecovery(t *testing.T) {
	path, _ := writer(t)
	change := prepareChange(t, path)
	if _, err := ApplyFiles(context.Background(), path, change, authorize(t, change)); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(t.TempDir(), "overlay")
	prepared, err := PrepareLexicalOverlay(context.Background(), path, stage)
	if err != nil {
		t.Fatal(err)
	}
	id, err := prepared.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteLexicalOverlay(context.Background(), path, prepared, effects.Authorization{IntentID: strings.Repeat("f", 64), Actor: "operator"}); err == nil {
		t.Fatal("wrong approval admitted")
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(prefix, actual) {
		t.Fatal("rejected approval changed journal", err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("rejected approval wrote staging", err)
	}
	auth := effects.Authorization{IntentID: id, Actor: "operator"}
	s, err := ExecuteLexicalOverlay(context.Background(), path, prepared, auth)
	if err != nil || s.RILexicalOverlay == nil || s.RILexicalOverlay.Outcome != "CONFIRMED" {
		t.Fatal("overlay not confirmed", err)
	}
	lease, err := worktree.Acquire(s.Workspace.Request)
	if err != nil {
		t.Fatal(err)
	}
	ref, selectionErr := selectLexicalOverlay(context.Background(), path, func(ctx context.Context, path string) (LexicalCandidate, error) {
		return observeLexicalCandidateLeased(ctx, path, *s.Workspace)
	})
	closeErr := lease.Close()
	if selectionErr != nil || closeErr != nil || ref.Candidate != prepared.Plan.CandidateID {
		t.Fatal("leased overlay selection failed", selectionErr, closeErr)
	}
	if _, err := ExecuteLexicalOverlay(context.Background(), path, prepared, auth); err == nil {
		t.Fatal("overlay repeated")
	}
	// Disposable fixture: retain intent but lose the completion observation.
	if err := os.WriteFile(path, prefix, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "ri.overlay-intent", RILexicalOverlayIntent{prepared, auth}); err != nil {
		t.Fatal(err)
	}
	pending, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, "verification.started", struct{}{}); err == nil {
		t.Fatal("UNKNOWN allowed unrelated event")
	}
	s, err = ReconcileLexicalOverlay(context.Background(), path)
	if err != nil || s.RILexicalOverlay.Outcome != "CONFIRMED" {
		t.Fatal("lost observation not recovered", err)
	}
	if err := os.WriteFile(path, pending, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "manifest.jsonl"), []byte("torn"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = ReconcileLexicalOverlay(context.Background(), path)
	if err == nil || s.RILexicalOverlay.Outcome != "UNKNOWN" {
		t.Fatal("corruption confirmed", err)
	}
	actual, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(pending, actual) {
		t.Fatal("failed recovery changed journal", err)
	}
}

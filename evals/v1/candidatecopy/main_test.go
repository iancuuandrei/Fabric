package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/config"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/safepath"
	"github.com/iancuuandrei/fabric/internal/verification"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func gitFixture(t *testing.T, root string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", root}, args...)...)
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
}

func fixture(t *testing.T) (string, worktree.Request) {
	t.Helper()
	d := t.TempDir()
	gitFixture(t, d, "init", "-q")
	if err := os.WriteFile(filepath.Join(d, "source.txt"), []byte("committed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "keep.txt"), []byte("keep\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitFixture(t, d, "add", "source.txt", "keep.txt")
	gitFixture(t, d, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	i, err := repository.Discover(context.Background(), d, "unrelated-fixture")
	if err != nil {
		t.Fatal(err)
	}
	r, err := worktree.Prepare(strings.Repeat("a", 64), i)
	if err != nil {
		t.Fatal(err)
	}
	return d, r
}

func createBinding(t *testing.T, r worktree.Request) worktree.Binding {
	t.Helper()
	b, err := worktree.Create(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func snapshotFor(t *testing.T, b worktree.Binding, c worktree.Candidate, expected string) control.Snapshot {
	t.Helper()
	checks := []config.Check{{Name: "fixture", Argv: []string{"git", "--version"}, TimeoutSeconds: 10}}
	plan, err := verification.PreparePlan(b.Request.RunID, "test-nonce", expected, b.Request.Path, checks)
	if err != nil {
		t.Fatal(err)
	}
	planID, err := plan.ID()
	if err != nil {
		t.Fatal(err)
	}
	obs := control.VerificationObservation{
		Result: verification.Result{Version: 1, CandidateID: expected, Status: "PASS"},
	}
	snap := control.Snapshot{
		Creation:  control.Creation{Config: config.Config{Verification: checks}},
		RunID:     b.Request.RunID,
		State:     "READY",
		Workspace: &b,
		Candidate: &c,
		Verification: &control.VerificationState{
			PlanID:       planID,
			Plan:         plan,
			Pending:      false,
			Observations: []control.VerificationObservation{obs},
		},
	}
	return snap
}

func TestValidDirtyUntrackedDeleteCopy(t *testing.T) {
	_, r := fixture(t)
	b := createBinding(t, r)
	// Dirty tracked, new untracked, deleted tracked.
	if err := os.WriteFile(filepath.Join(b.Request.Path, "source.txt"), []byte("dirty candidate\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Request.Path, "untracked.txt"), []byte("untracked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(b.Request.Path, "keep.txt")); err != nil {
		t.Fatal(err)
	}
	cand, files, err := worktree.Capture(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("captured %d files, want 2 (dirty+untracked, deletion absent)", len(files))
	}
	id, err := cand.ID()
	if err != nil {
		t.Fatal(err)
	}
	snap := control.Snapshot{RunID: b.Request.RunID, Workspace: &b, Candidate: &cand}
	dest := filepath.Join(t.TempDir(), "copy")
	out, err := copyUnderLease(context.Background(), snap, id, b.Request.Path, dest)
	if err != nil {
		t.Fatal(err)
	}
	if out.FileCount != 2 || out.FilesHash != cand.FilesHash {
		t.Fatal("copy output did not bind source manifest")
	}
	if _, err := os.Stat(filepath.Join(dest, "keep.txt")); !os.IsNotExist(err) {
		t.Fatal("deletion was not preserved as absent")
	}
	raw, err := os.ReadFile(filepath.Join(dest, "source.txt"))
	if err != nil || string(raw) != "dirty candidate\n" {
		t.Fatal("dirty tracked bytes not preserved")
	}
}

func TestChangedSourceRejects(t *testing.T) {
	_, r := fixture(t)
	b := createBinding(t, r)
	cand, _, err := worktree.Capture(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := cand.ID()
	snap := control.Snapshot{RunID: b.Request.RunID, Workspace: &b, Candidate: &cand}
	// Change source after snapshot.
	if err := os.WriteFile(filepath.Join(b.Request.Path, "source.txt"), []byte("changed after snapshot\n"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "copy")
	if _, err := copyUnderLease(context.Background(), snap, id, b.Request.Path, dest); err == nil {
		t.Fatal("changed source admitted")
	}
}

func TestIncorrectSnapshotCandidateRejects(t *testing.T) {
	_, r := fixture(t)
	b := createBinding(t, r)
	cand, _, err := worktree.Capture(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := cand.ID()
	// Snapshot candidate claims different bytes than actual workspace.
	wrong := cand
	wrong.FilesHash = strings.Repeat("c", 64)
	snap := control.Snapshot{RunID: b.Request.RunID, Workspace: &b, Candidate: &wrong}
	dest := filepath.Join(t.TempDir(), "copy")
	if _, err := copyUnderLease(context.Background(), snap, id, b.Request.Path, dest); err == nil {
		t.Fatal("incorrect snapshot candidate admitted")
	}
	// Wrong expected ID fails validation even with correct snapshot.
	cand2, _, err := worktree.Capture(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	snap2 := snapshotFor(t, b, cand2, id)
	if err := validateSnapshot(snap2, strings.Repeat("d", 64)); err == nil {
		t.Fatal("wrong expected ID admitted")
	}
}

func TestNestingAndSymlinkReject(t *testing.T) {
	_, r := fixture(t)
	b := createBinding(t, r)
	src := filepath.Clean(b.Request.Path)
	if err := rejectNesting(src, filepath.Join(src, "child")); err == nil {
		t.Fatal("destination inside source admitted")
	}
	if err := rejectNesting(src, filepath.Dir(src)); err == nil {
		t.Fatal("source inside destination admitted")
	}
	// Symlink inside candidate is rejected by Capture/safepath, never copied as success.
	link := filepath.Join(b.Request.Path, "link.txt")
	target := filepath.Join(b.Request.Path, "source.txt")
	_ = os.Remove(link)
	if err := os.Symlink(target, link); err == nil {
		if _, _, err := worktree.Capture(context.Background(), b); err == nil {
			t.Fatal("symlink candidate admitted")
		}
		_ = os.Remove(link)
	}
	// Destination parent symlink is rejected.
	parent := t.TempDir()
	realDir := filepath.Join(parent, "real")
	if err := os.Mkdir(realDir, 0755); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(parent, "linkparent")
	if err := os.Symlink(realDir, linkParent); err == nil {
		if err := safepath.Directory(linkParent); err == nil {
			t.Fatal("symlinked destination parent admitted")
		}
	}
}

func TestWriterLeaseContentionBlocksRead(t *testing.T) {
	_, r := fixture(t)
	w, err := worktree.Acquire(r)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := worktree.AcquireRead(r); err == nil {
		t.Fatal("read lease acquired while writer token held")
	}
}

func TestWrongManifestRejects(t *testing.T) {
	_, r := fixture(t)
	b := createBinding(t, r)
	cand, files, err := worktree.Capture(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := cand.ID()
	snap := control.Snapshot{RunID: b.Request.RunID, Workspace: &b, Candidate: &cand}
	dest := filepath.Join(t.TempDir(), "copy")
	out, err := copyUnderLease(context.Background(), snap, id, b.Request.Path, dest)
	if err != nil {
		t.Fatal(err)
	}
	_ = out
	// Tamper destination: manifest must no longer match source.
	if err := os.WriteFile(filepath.Join(dest, "source.txt"), []byte("tampered\n"), 0600); err != nil {
		t.Fatal(err)
	}
	destRoot, err := os.OpenRoot(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer destRoot.Close()
	if err := verifyDestination(destRoot, files, cand); err == nil {
		t.Fatal("tampered manifest admitted")
	}
	// Wrong expected digest fails end-to-end validation.
	snap2 := snapshotFor(t, b, cand, id)
	if err := validateSnapshot(snap2, strings.Repeat("e", 64)); err == nil {
		t.Fatal("wrong manifest ID admitted")
	}
}

func TestEndToEndRunValidatesAndCopies(t *testing.T) {
	_, r := fixture(t)
	b := createBinding(t, r)
	if err := os.WriteFile(filepath.Join(b.Request.Path, "untracked.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cand, _, err := worktree.Capture(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := cand.ID()
	snap := snapshotFor(t, b, cand, id)
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snap.json")
	projection := candidateCopySnapshot{
		RunID:        snap.RunID,
		State:        snap.State,
		Workspace:    snap.Workspace,
		Candidate:    snap.Candidate,
		Verification: snap.Verification,
		Review:       snap.Review,
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	// This is the runner's bounded six-field projection, not a fabricated full
	// Snapshot. In particular, the nil review remains explicitly present.
	var decoded candidateCopySnapshot
	if err := canonical.Decode(raw, &decoded); err != nil {
		t.Fatalf("canonical projection decode failed: %v", err)
	}
	if decoded.Review != nil || decoded.RunID != snap.RunID || decoded.State != snap.State ||
		decoded.Workspace == nil || decoded.Workspace.Request.RunID != snap.Workspace.Request.RunID ||
		decoded.Workspace.GitDir != snap.Workspace.GitDir ||
		decoded.Candidate == nil || *decoded.Candidate != *snap.Candidate ||
		decoded.Verification == nil || decoded.Verification.PlanID != snap.Verification.PlanID {
		t.Fatal("projection decode did not preserve required snapshot bindings or null review")
	}
	if err := os.WriteFile(snapPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadSnapshot(snapPath)
	if err != nil {
		t.Fatalf("helper rejected exact projection: %v", err)
	}
	if loaded.RunID != snap.RunID || loaded.State != snap.State || loaded.Workspace == nil ||
		loaded.Workspace.Request.RunID != snap.Workspace.Request.RunID || loaded.Workspace.GitDir != snap.Workspace.GitDir ||
		loaded.Candidate == nil || *loaded.Candidate != *snap.Candidate ||
		loaded.Verification == nil || loaded.Verification.PlanID != snap.Verification.PlanID || loaded.Review != nil {
		t.Fatal("helper did not map exact projection fields into snapshot")
	}
	dest := filepath.Join(dir, "dest")
	if err := run([]string{"--snapshot", snapPath, "--expected-candidate", id, "--destination", dest}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "untracked.txt")); err != nil {
		t.Fatal("destination missing untracked file", err)
	}
	// Wrong ID must fail and retain destination attempt (BLOCKED, no delete).
	badDest := filepath.Join(dir, "bad")
	if err := run([]string{"--snapshot", snapPath, "--expected-candidate", strings.Repeat("f", 64), "--destination", badDest}); err == nil {
		t.Fatal("wrong ID run admitted")
	}
}

func TestLoadSnapshotRejectsMissingRequiredProjectionField(t *testing.T) {
	_, r := fixture(t)
	b := createBinding(t, r)
	cand, _, err := worktree.Capture(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	id, err := cand.ID()
	if err != nil {
		t.Fatal(err)
	}
	snap := snapshotFor(t, b, cand, id)
	projection := candidateCopySnapshot{
		RunID:        snap.RunID,
		State:        snap.State,
		Workspace:    snap.Workspace,
		Candidate:    snap.Candidate,
		Verification: snap.Verification,
		Review:       nil,
	}
	raw, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		t.Fatal(err)
	}
	delete(members, "candidate")
	raw, err = json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "missing-candidate.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSnapshot(path); err == nil || !strings.Contains(err.Error(), `missing exact field "candidate"`) {
		t.Fatalf("missing required projection field was not rejected exactly: %v", err)
	}
}

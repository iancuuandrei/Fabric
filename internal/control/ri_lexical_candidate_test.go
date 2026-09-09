package control

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/gitlocal"
	"harness.local/engorch/internal/worktree"
)

func TestLexicalCandidateDerivesAdmittedChangesAndRejectsDrift(t *testing.T) {
	path, state := writer(t)
	for _, phase := range []string{"REVIEWING", "READY"} {
		readable := state
		readable.State = phase
		if err := lexicalCaptureAllowed(readable); err != nil {
			t.Fatal("read phase rejected", phase, err)
		}
		if err := filesAllowed(readable); err == nil {
			t.Fatal("read phase granted file effects", phase)
		}
		readable.FileOutcome = "UNKNOWN"
		if err := lexicalCaptureAllowed(readable); err == nil {
			t.Fatal("unresolved files admitted", phase)
		}
	}
	pristine, err := ObserveLexicalCandidate(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(pristine.Changed.Files) != 0 || len(pristine.Deleted) != 0 || len(pristine.Bytes) != 0 {
		t.Fatal("pristine candidate has overlay")
	}
	lease, err := worktree.Acquire(state.Workspace.Request)
	if err != nil {
		t.Fatal(err)
	}
	leased, captureErr := observeLexicalCandidateLeased(context.Background(), path, *state.Workspace)
	if _, err := ObserveLexicalCandidate(context.Background(), path); err == nil {
		t.Fatal("held lease reacquired")
	}
	closeErr := lease.Close()
	if captureErr != nil || closeErr != nil || leased.Candidate != pristine.Candidate {
		t.Fatal("caller-held lease capture failed", captureErr, closeErr)
	}
	prepared := prepareChange(t, path)
	state, err = ApplyFiles(context.Background(), path, prepared, authorize(t, prepared))
	if err != nil {
		t.Fatal(err)
	}
	observed, err := ObserveLexicalCandidate(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if observed.Candidate != *state.Candidate || len(observed.Changed.Files) != 2 || string(observed.Bytes["file.txt"]) != "after\n" || string(observed.Bytes["nested/binary.dat"]) != "\x00\xffbinary" {
		t.Fatal("changed candidate scope mismatch")
	}
	for _, file := range observed.Changed.Files {
		id, err := gitlocal.ObjectID(observed.Base.Source.ObjectFormat, "blob", observed.Bytes[file.Path])
		if err != nil || id != file.Blob {
			t.Fatal("changed blob mismatch", err)
		}
	}
	deletion, err := PrepareFiles(context.Background(), path, []fileeffects.Change{{Path: "file.txt", BeforeHash: digestText("after\n")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyFiles(context.Background(), path, deletion, authorize(t, deletion)); err != nil {
		t.Fatal(err)
	}
	observed, err = ObserveLexicalCandidate(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed.Deleted) != 1 || observed.Deleted[0] != "file.txt" || len(observed.Changed.Files) != 1 {
		t.Fatal("deletion not projected")
	}
	if err := os.WriteFile(filepath.Join(state.Workspace.Request.Path, "nested", "binary.dat"), []byte("unadmitted drift"), 0600); err != nil {
		t.Fatal(err)
	}
	failed, err := ObserveLexicalCandidate(context.Background(), path)
	if err == nil || failed.Bytes != nil {
		t.Fatal("unadmitted drift returned overlay", err)
	}
}

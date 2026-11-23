package verification

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/repository"
	"github.com/iancuuandrei/fabric/internal/worktree"
)

func formatObservationFixture(t *testing.T, source string) (worktree.Binding, worktree.Candidate) {
	t.Helper()
	root := t.TempDir()
	formatGit(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "format.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	formatGit(t, root, "add", "format.go")
	formatGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	identity, err := repository.Discover(context.Background(), root, filepath.Base(root))
	if err != nil {
		t.Fatal(err)
	}
	request, err := worktree.Prepare(strings.Repeat("a", 64), identity)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := worktree.Create(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	candidate, _, err := worktree.Capture(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	return binding, candidate
}

func formatGit(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func formatManifest() GoFormatManifest {
	return GoFormatManifest{Version: goFormatManifestVersion, Paths: []string{"format.go"}}
}

func TestObserveGoFormatColdWarmCorruptionAndInputBinding(t *testing.T) {
	binding, candidate := formatObservationFixture(t, "package formatfixture\nfunc F( ){}\n")
	cache := filepath.Join(t.TempDir(), "format-cache")
	first, state, err := ObserveGoFormat(context.Background(), binding, candidate, formatManifest(), cache)
	if err != nil || state != GoFormatCacheMiss || len(first.Files) != 1 || first.Files[0].Outcome != "formatted" {
		t.Fatalf("cold format observation invalid: state=%q observation=%+v err=%v", state, first, err)
	}
	payloadPath := filepath.Join(cache, first.ActionID+".out")
	payload, err := os.ReadFile(payloadPath)
	if err != nil || !bytes.Equal(payload, []byte("package formatfixture\n\nfunc F() {}\n")) {
		t.Fatalf("format payload missing or incorrect: bytes=%d err=%v", len(payload), err)
	}
	warm, state, err := ObserveGoFormat(context.Background(), binding, candidate, formatManifest(), cache)
	if err != nil || state != GoFormatCacheHit || !reflect.DeepEqual(first, warm) {
		t.Fatalf("warm observation differs: state=%q equal=%t err=%v", state, reflect.DeepEqual(first, warm), err)
	}
	if err := os.WriteFile(payloadPath, []byte("forged"), 0600); err != nil {
		t.Fatal(err)
	}
	recovered, state, err := ObserveGoFormat(context.Background(), binding, candidate, formatManifest(), cache)
	if err != nil || state != GoFormatCacheMiss || !reflect.DeepEqual(first, recovered) {
		t.Fatalf("corrupt cache did not recompute exact artifact: state=%q equal=%t err=%v", state, reflect.DeepEqual(first, recovered), err)
	}
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "format.go"), []byte("package formatfixture\nfunc G( ){}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, _, err := worktree.Capture(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	second, state, err := ObserveGoFormat(context.Background(), binding, changed, formatManifest(), cache)
	if err != nil || state != GoFormatCacheMiss || second.ActionID == first.ActionID || second.Action.CandidateID == first.Action.CandidateID {
		t.Fatalf("changed candidate reused artifact: state=%q first=%s second=%s err=%v", state, first.ActionID, second.ActionID, err)
	}
	if _, _, err := ObserveGoFormat(context.Background(), binding, candidate, formatManifest(), cache); err == nil {
		t.Fatal("stale candidate observation accepted")
	}
}

func TestGoFormatActionBindsEngineAndInputBounds(t *testing.T) {
	binding, candidate := formatObservationFixture(t, "package formatfixture\n")
	cache := filepath.Join(t.TempDir(), "format-cache")
	observation, _, err := ObserveGoFormat(context.Background(), binding, candidate, formatManifest(), cache)
	if err != nil {
		t.Fatal(err)
	}
	mutated := observation.Action
	mutated.WorkspaceID = strings.Repeat("0", 64)
	if _, err := mutated.ID(); err == nil {
		t.Fatal("workspace/candidate substitution accepted")
	}
	mutated = observation.Action
	mutated.Engine.Runtime += "-other"
	changedID, err := mutated.ID()
	if err != nil || changedID == observation.ActionID {
		t.Fatalf("engine runtime did not affect action identity: id=%s err=%v", changedID, err)
	}
	paths := make([]string, goFormatMaxFiles+1)
	for index := range paths {
		paths[index] = fmt.Sprintf("source-%02d.go", index)
	}
	if err := (GoFormatManifest{Version: 1, Paths: paths}).Validate(); err == nil {
		t.Fatal("oversized format manifest accepted")
	}
	if err := os.WriteFile(filepath.Join(binding.Request.Path, "format.go"), bytes.Repeat([]byte("x"), goFormatMaxFileBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	oversized, _, err := worktree.Capture(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ObserveGoFormat(context.Background(), binding, oversized, formatManifest(), cache); err == nil {
		t.Fatal("oversized candidate input accepted")
	}
}

func TestObserveGoFormatBoundsErrorsAndCacheSafety(t *testing.T) {
	binding, candidate := formatObservationFixture(t, "package formatfixture\nfunc F(\n")
	cache := filepath.Join(t.TempDir(), "format-cache")
	observation, state, err := ObserveGoFormat(context.Background(), binding, candidate, formatManifest(), cache)
	if err != nil || state != GoFormatCacheMiss || observation.Files[0].Outcome != "format_error" || observation.Files[0].DiagnosticSHA256 == "" || observation.PayloadBytes != 0 {
		t.Fatalf("syntax error was not a bounded artifact: state=%q observation=%+v err=%v", state, observation, err)
	}
	for _, manifest := range []GoFormatManifest{
		{Version: 0, Paths: []string{"format.go"}},
		{Version: 1, Paths: []string{"format.go", "format.go"}},
		{Version: 1, Paths: []string{"../format.go"}},
		{Version: 1, Paths: []string{"format.txt"}},
	} {
		if _, _, err := ObserveGoFormat(context.Background(), binding, candidate, manifest, cache); err == nil {
			t.Fatalf("invalid manifest accepted: %+v", manifest)
		}
	}
	tampered := observation
	tampered.Files[0].Outcome = "unchanged"
	withoutID := tampered
	withoutID.ArtifactID = ""
	tampered.ArtifactID, err = canonical.Hash("harness.go-format-artifact.v1", withoutID)
	if err != nil || validateGoFormatObservation(tampered, nil) == nil {
		t.Fatal("self-rehashed false unchanged outcome accepted")
	}
	if _, _, err := ObserveGoFormat(context.Background(), binding, candidate, formatManifest(), binding.Request.Path); err == nil {
		t.Fatal("workspace cache accepted")
	}
	if _, _, err := ObserveGoFormat(context.Background(), binding, candidate, formatManifest(), filepath.VolumeName(cache)+string(filepath.Separator)); err == nil {
		t.Fatal("cache root accepted")
	}
}

func TestGoFormatCacheEvictsBoundedEntries(t *testing.T) {
	cache := t.TempDir()
	for index := 0; index < goFormatCacheMaxEntries; index++ {
		key := fmt.Sprintf("%064x", index)
		if err := os.WriteFile(filepath.Join(cache, key+".json"), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cache, key+".out"), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureGoFormatCacheCapacity(cache, 1); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 2*(goFormatCacheMaxEntries-1) {
		t.Fatalf("cache entry bound not enforced: entries=%d err=%v", len(entries), err)
	}
	if err := ensureGoFormatCacheCapacity(cache, goFormatCacheMaxBytes+1); err == nil {
		t.Fatal("oversized cache reservation accepted")
	}
}

type failingGoFormatCacheRemover struct{ calls int }

func (r *failingGoFormatCacheRemover) Remove(string) error {
	r.calls++
	return errors.New("forced cache eviction failure")
}

func TestGoFormatCacheEvictionFailureDoesNotClaimFreedSpace(t *testing.T) {
	remover := &failingGoFormatCacheRemover{}
	items := []*goFormatCacheEntry{{key: strings.Repeat("a", 64), size: 2}}
	total, err := trimGoFormatCache(remover, items, 2, goFormatCacheMaxBytes)
	if err == nil || total != 2 || remover.calls != 1 {
		t.Fatalf("eviction failure did not fail closed: total=%d calls=%d err=%v", total, remover.calls, err)
	}
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/iancuuandrei/fabric/internal/canonical"
	"github.com/iancuuandrei/fabric/internal/control"
	"github.com/iancuuandrei/fabric/internal/verification"
)

func formatObservationCLIFixture(t *testing.T) (root, runID string) {
	t.Helper()
	root = autonomousCLIFixture(t)
	if err := os.WriteFile(filepath.Join(root, "format.go"), []byte("package fixture\nfunc F( ){}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cliGit(t, root, "add", "format.go")
	cliGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "format fixture")
	var planned control.Snapshot
	if err := json.Unmarshal(mustExecuteCLI(t, root, "plan", "format observation fixture"), &planned); err != nil {
		t.Fatal(err)
	}
	mustExecuteCLI(t, root, "approve", planned.RunID, planned.PlanID, "fixture-human")
	mustExecuteCLI(t, root, "run", planned.RunID)
	return root, planned.RunID
}

func writeFormatManifest(t *testing.T, path string) {
	t.Helper()
	raw, err := canonical.Bytes(verification.GoFormatManifest{Version: 1, Paths: []string{"format.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestObserveFormatIsCandidateBoundArtifactOnly(t *testing.T) {
	root, runID := formatObservationCLIFixture(t)
	journal, err := runPath(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	writeFormatManifest(t, manifest)
	cache := filepath.Join(t.TempDir(), "cache")
	invoke := func() goFormatObservationResult {
		var output bytes.Buffer
		if err := Execute(context.Background(), []string{"observe-format", runID, manifest, cache}, root, &output); err != nil {
			t.Fatal(err)
		}
		var result goFormatObservationResult
		if err := json.Unmarshal(output.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := invoke()
	if first.RunID != runID || first.CacheState != verification.GoFormatCacheMiss || len(first.Observation.Files) != 1 || first.Observation.Files[0].Outcome != "formatted" {
		t.Fatalf("unexpected cold format observation: %#v", first)
	}
	if _, err := os.Stat(first.ArtifactMetadataPath); err != nil {
		t.Fatal(err)
	}
	if payload, err := os.ReadFile(first.ArtifactPayloadPath); err != nil || string(payload) != "package fixture\n\nfunc F() {}\n" {
		t.Fatalf("retained formatted output unavailable: bytes=%d err=%v", len(payload), err)
	}
	second := invoke()
	if second.CacheState != verification.GoFormatCacheHit || !reflect.DeepEqual(first.Observation, second.Observation) {
		t.Fatalf("warm format artifact differs: %#v", second)
	}
	after, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("observe-format appended a controller event")
	}
	snapshot, err := control.Inspect(journal)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Verification != nil || snapshot.State != "IMPLEMENTING" {
		t.Fatalf("format observation changed verification or run state: verification=%#v state=%s", snapshot.Verification, snapshot.State)
	}
}

func TestObserveFormatRejectsInvalidInputsWithoutJournalMutation(t *testing.T) {
	root, runID := formatObservationCLIFixture(t)
	journal, err := runPath(root, runID)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(journal)
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(manifest, []byte(`{"version":1,"paths":["../format.go"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"observe-format", runID, manifest, filepath.Join(t.TempDir(), "cache")}, root, &output); err == nil {
		t.Fatal("invalid manifest accepted")
	}
	after, err := os.ReadFile(journal)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("invalid artifact request changed journal: err=%v", err)
	}
}

func TestObserveFormatDoesNotAdvertiseArtifactDuringCacheContention(t *testing.T) {
	root, runID := formatObservationCLIFixture(t)
	manifest := filepath.Join(t.TempDir(), "manifest.json")
	writeFormatManifest(t, manifest)
	cache := filepath.Join(t.TempDir(), "cache")
	if err := os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, ".go-format-observation.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Execute(context.Background(), []string{"observe-format", runID, manifest, cache}, root, &output); err != nil {
		t.Fatal(err)
	}
	var result goFormatObservationResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.CacheState != verification.GoFormatCacheContended || result.ArtifactMetadataPath != "" || result.ArtifactPayloadPath != "" {
		t.Fatalf("contended cache advertised unpublished artifact: %#v", result)
	}
}

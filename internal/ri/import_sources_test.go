package ri

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"harness.local/engorch/internal/repository"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportSourceHashesUseCommittedBytes(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if b, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
	}
	git("init", "-q")
	data := []byte("package fixture\n")
	sourcePath := filepath.Join(root, "source.go")
	if err := os.WriteFile(sourcePath, data, 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source.go")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source")
	identity, err := repository.Discover(context.Background(), root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	source, err := FromRepository(identity)
	if err != nil {
		t.Fatal(err)
	}
	plan := ImportPlan{Version: 1, Executable: filepath.Join(root, "ri.exe"), ExecutableSHA256: strings.Repeat("a", 64), Repository: identity, OutputPath: filepath.Join(root, "stage"), Request: ImportRequest{IndexPath: filepath.Join(root, "index"), Source: source, Manifest: Manifest{Format: 1, Source: source, Producers: []Producer{{ID: "p", Inputs: []Input{{Name: "source:source.go", SHA256: fmt.Sprintf("%x", sha256.Sum256(data))}}}}}, Producer: "p", Policy: "strict", Sources: map[string]string{"source.go": sourcePath}}}
	if err := os.WriteFile(sourcePath, []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	evidence, err := VerifyCommittedSources(context.Background(), plan)
	if err != nil || len(evidence) != 1 || evidence[0].SHA256 != plan.Request.Manifest.Producers[0].Inputs[0].SHA256 {
		t.Fatal("committed source rejected", err)
	}
	lexical, err := ObserveLexical(context.Background(), identity)
	if err != nil || len(lexical.Manifest.Files) != 1 || len(lexical.Excluded) != 0 {
		t.Fatal("lexical observation failed", err)
	}
	file := lexical.Manifest.Files[0]
	if lexical.Manifest.Source != source || file.Path != evidence[0].Path || file.Blob != evidence[0].Blob || file.SHA256 != evidence[0].SHA256 || file.Bytes != evidence[0].Bytes {
		t.Fatal("lexical observation differs from committed bytes")
	}
	if executable := os.Getenv("ENGORCH_RI_BINARY"); executable != "" {
		executable, err = filepath.Abs(executable)
		if err != nil {
			t.Fatal(err)
		}
		binary, err := os.ReadFile(executable)
		if err != nil {
			t.Fatal(err)
		}
		client := Client{Executable: executable, ExecutableHash: fmt.Sprintf("%x", sha256.Sum256(binary))}
		if err := client.ValidateLexicalManifest(context.Background(), lexical.Manifest); err != nil {
			t.Fatal("actual Rust rejected committed lexical manifest", err)
		}
		stage := t.TempDir()
		manifestPath := filepath.Join(stage, "manifest.jsonl")
		manifestFile, err := os.Create(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := lexical.Manifest.WriteRecords(manifestFile); err != nil {
			_ = manifestFile.Close()
			t.Fatal(err)
		}
		if err := manifestFile.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, file.SHA256), data, 0600); err != nil {
			t.Fatal(err)
		}
		manifestID, err := lexical.Manifest.ID()
		if err != nil {
			t.Fatal(err)
		}
		request := map[string]any{"operation": "lexical_build", "manifest_path": manifestPath, "manifest_id": manifestID, "source": source, "source_root": stage, "output_path": filepath.Join(stage, "index"), "batch_bytes": 1048576, "batch_files": 100}
		built, err := client.Call(context.Background(), request)
		if err != nil {
			t.Fatal("actual Rust lexical build failed", err)
		}
		var receipt map[string]json.RawMessage
		if err := json.Unmarshal(built, &receipt); err != nil {
			t.Fatal(err)
		}
		var buildID string
		if err := json.Unmarshal(receipt["build_id"], &buildID); err != nil {
			t.Fatal(err)
		}
		typed, err := client.BuildLexical(context.Background(), lexical.Manifest, manifestPath, stage, filepath.Join(stage, "typed-index"), 1048576, 100)
		if err != nil {
			t.Fatal("controller rejected actual Rust build", err)
		}
		typedID, err := typed.ID()
		if err != nil || typedID != buildID {
			t.Fatal("build identity depends on staging path", err)
		}
		stagePlan := LexicalPlan{Version: 1, Repository: identity, ManifestID: manifestID, Files: len(lexical.Manifest.Files), Executable: executable, ExecutableSHA256: client.ExecutableHash, StageRoot: filepath.Join(stage, "complete"), BatchBytes: 1048576, BatchFiles: 100}
		staged, err := StageLexical(context.Background(), stagePlan, lexical.Manifest)
		if err != nil {
			t.Fatal("full lexical staging failed", err)
		}
		stagedID, err := staged.Build.ID()
		if err != nil || stagedID != buildID {
			t.Fatal("full staging build differs", err)
		}
		completion, err := ReadLexicalCompletion(stagePlan)
		if err != nil || completion.BuildID != stagedID {
			t.Fatal("persisted completion mismatch", err)
		}
		recovered, err := ObserveLexicalStage(context.Background(), stagePlan)
		if err != nil {
			t.Fatal("read-only staging observation failed", err)
		}
		recoveredID, err := recovered.Build.ID()
		if err != nil || recoveredID != stagedID {
			t.Fatal("staging observation changed identity", err)
		}
		corruptSource := filepath.Join(staged.SourceRoot, file.SHA256)
		if err := os.WriteFile(corruptSource, []byte("changed source"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ObserveLexicalStage(context.Background(), stagePlan); err == nil {
			t.Fatal("completion hid changed source bytes")
		}
		changedPlan := stagePlan
		changedPlan.BatchFiles++
		if _, err := ReadLexicalCompletion(changedPlan); err == nil {
			t.Fatal("completion accepted for another plan")
		}
		completionPath := filepath.Join(stagePlan.StageRoot, "completion.json")
		completionInfo, err := os.Stat(completionPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(completionPath, completionInfo.Size()-1); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadLexicalCompletion(stagePlan); err == nil {
			t.Fatal("torn completion accepted")
		}
		if _, err := StageLexical(context.Background(), stagePlan, lexical.Manifest); err == nil {
			t.Fatal("staging effect repeated")
		}
		search := map[string]any{"operation": "lexical_search", "manifest_path": manifestPath, "manifest_id": manifestID, "source": source, "source_root": stage, "index_path": filepath.Join(stage, "index"), "build": receipt["build"], "build_id": buildID, "pattern": "fixture", "fixed": true, "case_insensitive": false, "limit": 10, "after": nil}
		found, err := client.Call(context.Background(), search)
		if err != nil {
			t.Fatal("actual disk search failed", err)
		}
		var result struct {
			ManifestID string `json:"manifest_id"`
			Truncated  bool   `json:"truncated"`
			Matches    []struct {
				Path  string `json:"path"`
				Blob  string `json:"blob"`
				Range [2]int `json:"range"`
			} `json:"matches"`
		}
		if err := json.Unmarshal(found, &result); err != nil {
			t.Fatal(err)
		}
		if result.ManifestID != manifestID || result.Truncated || len(result.Matches) != 1 || result.Matches[0].Path != "source.go" || result.Matches[0].Blob != file.Blob || result.Matches[0].Range != [2]int{8, 15} {
			t.Fatal("disk source match differs from committed source")
		}
		ref := LexicalRef{ManifestPath: manifestPath, SourceRoot: stage, IndexPath: filepath.Join(stage, "typed-index"), Manifest: lexical.Manifest, Build: typed}
		page, err := client.SearchLexical(context.Background(), ref, LexicalQuery{Pattern: "fixture", Fixed: true}, 1, nil)
		if err != nil || len(page.Matches) != 1 || page.Matches[0].Range != [2]int64{8, 15} {
			t.Fatal("typed lexical result validation failed", err)
		}
		changed := lexical.Manifest
		created := file
		created.Path = "created.go"
		changed.Files = []LexicalFile{created}
		changedPath := filepath.Join(stage, "changed.jsonl")
		changedFile, err := os.Create(changedPath)
		if err != nil {
			t.Fatal(err)
		}
		writeErr := changed.WriteRecords(changedFile)
		closeErr := changedFile.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatal(writeErr, closeErr)
		}
		overlay := LexicalOverlayRef{Candidate: strings.Repeat("e", 64), ManifestPath: changedPath, SourceRoot: stage, Manifest: changed, Deleted: []string{"source.go"}}
		overlayID, _, err := overlay.scope(lexical.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		overlayStage := filepath.Join(t.TempDir(), "overlay")
		changedBytes := map[string][]byte{"created.go": data}
		badBytes := map[string][]byte{"created.go": []byte("substitution")}
		if _, err := StageLexicalOverlay(context.Background(), lexical.Manifest, overlay.Candidate, changed, overlay.Deleted, badBytes, overlayStage); err == nil {
			t.Fatal("changed overlay bytes staged")
		}
		if _, err := os.Stat(overlayStage); !os.IsNotExist(err) {
			t.Fatal("invalid overlay created staging", err)
		}
		stagedOverlay, err := StageLexicalOverlay(context.Background(), lexical.Manifest, overlay.Candidate, changed, overlay.Deleted, changedBytes, overlayStage)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := StageLexicalOverlay(context.Background(), lexical.Manifest, overlay.Candidate, changed, overlay.Deleted, changedBytes, overlayStage); err == nil {
			t.Fatal("overlay staging overwritten")
		}
		overlay = stagedOverlay
		observedOverlayID, err := ObserveLexicalOverlay(context.Background(), lexical.Manifest, overlay)
		if err != nil || observedOverlayID != overlayID {
			t.Fatal("overlay readback identity differs", err)
		}
		merged, err := client.SearchLexicalOverlay(context.Background(), ref, overlay, LexicalQuery{Pattern: "fixture", Fixed: true}, 1, nil)
		if err != nil || merged.ManifestID != overlayID || len(merged.Matches) != 1 || merged.Matches[0].Path != "created.go" {
			t.Fatal("typed overlay identity or shadowing failed", err, merged)
		}
		if err := os.WriteFile(filepath.Join(overlay.SourceRoot, created.SHA256), []byte("changed artifact"), 0600); err != nil {
			t.Fatal(err)
		}
		if id, err := ObserveLexicalOverlay(context.Background(), lexical.Manifest, overlay); err == nil || id != "" {
			t.Fatal("corrupt overlay returned evidence", err)
		}
		for _, deleted := range [][]string{nil, {"missing.go"}, {"source.go", "source.go"}, {"created.go"}} {
			invalid := overlay
			invalid.Deleted = deleted
			if _, _, err := invalid.scope(lexical.Manifest); err == nil {
				t.Fatal("invalid tombstones admitted", deleted)
			}
		}
		baseCursor := &LexicalCursor{ManifestID: manifestID, QueryID: page.QueryID, Path: "source.go", Range: [2]int64{8, 15}}
		if _, err := client.SearchLexicalOverlay(context.Background(), ref, overlay, LexicalQuery{Pattern: "fixture", Fixed: true}, 1, baseCursor); err == nil {
			t.Fatal("base cursor admitted for overlay")
		}
		if err := os.WriteFile(filepath.Join(stage, file.SHA256), []byte("changed"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Call(context.Background(), search); err == nil {
			t.Fatal("changed source artifact accepted")
		}
		if _, err := client.Call(context.Background(), request); err == nil {
			t.Fatal("lexical build reused staging output")
		}
	}
	destination := filepath.Join(t.TempDir(), "source.go")
	plan.Request.Sources["source.go"] = destination
	copied, err := MaterializeSources(context.Background(), plan)
	if err != nil || len(copied) != 1 || copied[0] != evidence[0] {
		t.Fatal("materialization evidence mismatch", err)
	}
	actual, err := os.ReadFile(destination)
	if err != nil || string(actual) != string(data) {
		t.Fatal("materialized wrong bytes", err)
	}
	if _, err := MaterializeSources(context.Background(), plan); err == nil {
		t.Fatal("source destination overwritten")
	}
	plan.Request.Manifest.Producers[0].Inputs[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("dirty")))
	if _, err := VerifyCommittedSources(context.Background(), plan); err == nil {
		t.Fatal("dirty bytes accepted as committed")
	}
	// Create a committed symlink entry without creating an OS symlink or reading
	// a link target; its blob content is irrelevant to lexical regular-file scope.
	git("update-index", "--add", "--cacheinfo", "120000,"+evidence[0].Blob+",link")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "link")
	linked, err := repository.Discover(context.Background(), root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	withLink, err := ObserveLexical(context.Background(), linked)
	if err != nil || len(withLink.Manifest.Files) != 1 || len(withLink.Excluded) != 1 || withLink.Excluded[0].Kind != "symlink" {
		t.Fatal("unsupported leaf was hidden or indexed", err)
	}
}

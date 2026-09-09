package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/fileeffects"
	"harness.local/engorch/internal/ri"
)

func TestLexicalLifecycleActualRustCLI(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("requires built Rust RI")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(binary))
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("committed needle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "source.txt"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "source"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	run := func(args ...string) []byte {
		t.Helper()
		var out bytes.Buffer
		if err := Execute(context.Background(), args, root, &out); err != nil {
			t.Fatal(args, err)
		}
		return out.Bytes()
	}
	run("init")
	var state control.Snapshot
	if err := json.Unmarshal(run("plan", "Index committed source"), &state); err != nil {
		t.Fatal(err)
	}
	run("approve", state.RunID, state.PlanID, "operator")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("dirty worktree"), 0600); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(t.TempDir(), "lexical")
	preparedBytes := run("ri", "prepare-lexical", state.RunID, executable, hash, stage, "1048576", "100")
	var prepared lexicalPreview
	if err := json.Unmarshal(preparedBytes, &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.Plan.Files != 1 {
		t.Fatal("preview included uncommitted files")
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("preview wrote staging", err)
	}
	previewPath := filepath.Join(root, "lexical-preview.json")
	if err := os.WriteFile(previewPath, preparedBytes, 0600); err != nil {
		t.Fatal(err)
	}
	journalPath, err := runPath(root, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	prefix, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var discarded bytes.Buffer
	if err := Execute(context.Background(), []string{"ri", "lexical", state.RunID, previewPath, strings.Repeat("f", 64), "operator"}, root, &discarded); err == nil {
		t.Fatal("wrong approval accepted")
	}
	unchanged, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(prefix, unchanged) {
		t.Fatal("rejected approval changed journal", err)
	}
	if err := json.Unmarshal(run("ri", "lexical", state.RunID, previewPath, prepared.IntentID, "operator"), &state); err != nil {
		t.Fatal(err)
	}
	if state.RILexical == nil || state.RILexical.Outcome != "CONFIRMED" {
		t.Fatal("indexing not confirmed")
	}
	refBytes := run("ri", "lexical-ref", state.RunID)
	refPath := filepath.Join(root, "lexical-ref.json")
	if err := os.WriteFile(refPath, refBytes, 0600); err != nil {
		t.Fatal(err)
	}
	var result ri.LexicalResult
	if err := json.Unmarshal(run("ri", "search", executable, hash, refPath, "--fixed", "needle"), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Path != "source.txt" || result.Matches[0].Range != [2]int64{10, 16} {
		t.Fatal("committed search mismatch", result)
	}
	if err := json.Unmarshal(run("ri", "search", executable, hash, refPath, "--fixed", "--path", "source.txt", "--type", "txt", "--json", "--explain", "needle"), &result); err != nil || len(result.Matches) != 1 {
		t.Fatal("CLI path/type filters did not reach Rust", err, result)
	}
	var rejected bytes.Buffer
	if err := Execute(context.Background(), []string{"ri", "search", executable, hash, refPath, "--fixed", "--type", ".txt", "needle"}, root, &rejected); err == nil || rejected.Len() != 0 {
		t.Fatal("invalid CLI type filter admitted", err)
	}
	// This disposable journal models loss of the last observation after staging.
	if err := os.WriteFile(journalPath, prefix, 0600); err != nil {
		t.Fatal(err)
	}
	intent, err := prepared.Plan.Intent(state.RunID, state.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Append(journalPath, "ri.lexical-intent", control.RILexicalIntent{Plan: prepared.Plan, Intent: intent, Authorization: effects.Authorization{IntentID: prepared.IntentID, Actor: "operator"}}); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(run("reconcile", state.RunID), &state); err != nil {
		t.Fatal(err)
	}
	if state.RILexical.Outcome != "CONFIRMED" {
		t.Fatal("CLI recovery not confirmed")
	}
	if !bytes.Equal(refBytes, run("ri", "lexical-ref", state.RunID)) {
		t.Fatal("recovery changed reference")
	}
	if _, err := control.StartWorkspace(context.Background(), journalPath); err != nil {
		t.Fatal(err)
	}
	beforeHash := fmt.Sprintf("%x", sha256.Sum256([]byte("committed needle\n")))
	content := base64.StdEncoding.EncodeToString([]byte("candidate replacement\n"))
	change, err := control.PrepareFiles(context.Background(), journalPath, []fileeffects.Change{{Path: "source.txt", BeforeHash: &beforeHash, ContentBase64: &content}})
	if err != nil {
		t.Fatal(err)
	}
	changeID, err := change.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := control.ApplyFiles(context.Background(), journalPath, change, effects.Authorization{IntentID: changeID, Actor: "operator"}); err != nil {
		t.Fatal(err)
	}
	overlayBytes := run("ri", "prepare-overlay", state.RunID, filepath.Join(t.TempDir(), "overlay"))
	var overlay overlayPreview
	if err := json.Unmarshal(overlayBytes, &overlay); err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(root, "overlay-plan.json")
	if err := os.WriteFile(overlayPath, overlayBytes, 0600); err != nil {
		t.Fatal(err)
	}
	overlayID, err := overlay.Intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if overlay.IntentID != overlayID {
		t.Fatal("preview approval identity differs")
	}
	run("ri", "overlay", state.RunID, overlayPath, overlayID, "operator")
	run("ri", "overlay-ref", state.RunID)
	runtimeBinding, err := control.SelectRuntimeLexical(context.Background(), journalPath, true)
	if err != nil || runtimeBinding.Overlay == nil || runtimeBinding.Overlay.Candidate != overlay.Plan.CandidateID {
		t.Fatal("runtime lexical selection failed", err)
	}
	baseBinding, err := control.SelectRuntimeLexical(context.Background(), journalPath, false)
	if err != nil || baseBinding.Overlay != nil {
		t.Fatal("runtime base selection failed", err)
	}
	if err := json.Unmarshal(run("ri", "search", executable, hash, refPath, "--overlay-run", state.RunID, "--fixed", "replacement"), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Range != [2]int64{10, 21} || result.ManifestID != overlay.Plan.OverlayID {
		t.Fatal("candidate CLI match differs", result)
	}
	if err := json.Unmarshal(run("ri", "search", executable, hash, refPath, "--overlay-run", state.RunID, "--fixed", "needle"), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 0 {
		t.Fatal("replaced base content leaked")
	}
	current, err := control.Inspect(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current.Workspace.Request.Path, "source.txt"), []byte("unadmitted"), 0600); err != nil {
		t.Fatal(err)
	}
	discarded.Reset()
	if err := Execute(context.Background(), []string{"ri", "overlay-ref", state.RunID}, root, &discarded); err == nil || discarded.Len() != 0 {
		t.Fatal("stale overlay exported", err)
	}
	if _, err := control.SelectRuntimeLexical(context.Background(), journalPath, true); err == nil {
		t.Fatal("stale runtime overlay selected")
	}
}

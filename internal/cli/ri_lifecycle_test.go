package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"harness.local/engorch/internal/control"
	"harness.local/engorch/internal/effects"
	"harness.local/engorch/internal/ri"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRILifecycleActualRustCLI(t *testing.T) {
	executable := os.Getenv("ENGORCH_RI_BINARY")
	if executable == "" {
		t.Skip("requires built Rust RI")
	}
	binary, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "initial"}} {
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
	if err := json.Unmarshal(run("plan", "Index fixture"), &state); err != nil {
		t.Fatal(err)
	}
	run("approve", state.RunID, state.PlanID, "operator")
	source, err := ri.FromRepository(state.Creation.Repository)
	if err != nil {
		t.Fatal(err)
	}
	field := func(tag byte, b []byte) []byte { return append([]byte{tag, byte(len(b))}, b...) }
	tool := append(field(10, []byte("fixture")), field(18, []byte("1"))...)
	meta := append(field(18, tool), field(26, []byte("file:///fixture"))...)
	index := field(10, append(meta, 32, 1))
	indexPath := filepath.Join(root, "index.scip")
	if err := os.WriteFile(indexPath, index, 0600); err != nil {
		t.Fatal(err)
	}
	plan := ri.ImportPlan{Version: 1, Executable: executable, ExecutableSHA256: fmt.Sprintf("%x", sha256.Sum256(binary)), Repository: state.Creation.Repository, OutputPath: filepath.Join(root, "stage"), Request: ri.ImportRequest{IndexPath: indexPath, Source: source, Producer: "p", Policy: "strict", ProjectRoot: "file:///fixture", Sources: map[string]string{}, Manifest: ri.Manifest{Format: 1, Source: source, Producers: []ri.Producer{{ID: "p", Name: "fixture", Version: "1", ArtifactSHA256: strings.Repeat("a", 64), Inputs: []ri.Input{{Name: "scip:index", SHA256: fmt.Sprintf("%x", sha256.Sum256(index))}}}}}}}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "plan.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	approvalID := func(data []byte) string {
		t.Helper()
		var p struct {
			IntentID string `json:"intent_id"`
		}
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatal(err)
		}
		return p.IntentID
	}
	id := approvalID(run("ri", "prepare-import", state.RunID, "plan.json"))
	var discarded bytes.Buffer
	if err := Execute(context.Background(), []string{"ri", "import", state.RunID, "plan.json", strings.Repeat("f", 64), "operator"}, root, &discarded); err == nil {
		t.Fatal("wrong approval admitted")
	}
	if err := json.Unmarshal(run("ri", "import", state.RunID, "plan.json", id, "operator"), &state); err != nil {
		t.Fatal(err)
	}
	if state.RIImport == nil || state.RIImport.Outcome != "CONFIRMED" {
		t.Fatal("CLI import not confirmed")
	}
	if err := os.Mkdir(filepath.Join(root, "store"), 0700); err != nil {
		t.Fatal(err)
	}
	id = approvalID(run("ri", "prepare-publish", state.RunID, "store"))
	if err := json.Unmarshal(run("ri", "publish", state.RunID, "store", id, "operator"), &state); err != nil {
		t.Fatal(err)
	}
	if state.RIPublish == nil || state.RIPublish.Outcome != "CONFIRMED" {
		t.Fatal("CLI publication not confirmed")
	}
	artifact := state.RIPublish.Observation.Artifact
	data, err := (ri.Store{Directory: filepath.Join(root, "store")}).Read(artifact.ID)
	if err != nil || ri.SnapshotID(data) != artifact.ID {
		t.Fatal("published CLI artifact mismatch", err)
	}
	secondStore := filepath.Join(root, "pending-store")
	if err := os.Mkdir(secondStore, 0700); err != nil {
		t.Fatal(err)
	}
	journalPath, err := runPath(root, state.RunID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := control.PrepareRIPublish(journalPath, secondStore)
	if err != nil {
		t.Fatal(err)
	}
	id, err = intent.ID()
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Append(journalPath, "ri.publish-intent", control.RIPublishIntent{Directory: secondStore, Intent: intent, Authorization: effects.Authorization{IntentID: id, Actor: "operator"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondStore, artifact.ID+".pending"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), []string{"reconcile", state.RunID}, root, &discarded); err == nil {
		t.Fatal("pending artifact treated as final")
	}
	id = approvalID(run("ri", "prepare-publication-recovery", state.RunID))
	if err := json.Unmarshal(run("ri", "recover-publication", state.RunID, id, "operator"), &state); err != nil {
		t.Fatal(err)
	}
	if state.RIPublish.Outcome != "CONFIRMED" {
		t.Fatal("CLI recovery not confirmed")
	}
}
